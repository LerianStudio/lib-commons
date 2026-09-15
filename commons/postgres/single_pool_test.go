//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Single pool without a replica
//
// A client configured without a read replica (ReplicaDSN empty, or equal to
// the primary DSN) must open exactly ONE *sql.DB. Before this rule, the client
// opened two independent pools against the same primary, so the effective
// connection ceiling was 2 x MaxOpenConnections.
// ---------------------------------------------------------------------------

const (
	primaryTestDSN = "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable"
	replicaTestDSN = "postgres://postgres:secret@localhost:5433/postgres?sslmode=disable"
)

// poolOpenProbe records every pool the client opens and the handles the
// resolver factory receives, so a test can count pools instead of trusting a
// log line.
type poolOpenProbe struct {
	opened          []string
	resolverPrimary *sql.DB
	resolverReplica *sql.DB
	resolverCalls   int
}

func installPoolOpenProbe(t *testing.T) *poolOpenProbe {
	t.Helper()

	probe := &poolOpenProbe{}

	withPatchedDependencies(t,
		func(_, dsn string) (*sql.DB, error) {
			probe.opened = append(probe.opened, dsn)

			return testDB(t), nil
		},
		func(primary, replica *sql.DB, _ obs.Logger) (dbresolver.DB, error) {
			probe.resolverCalls++
			probe.resolverPrimary = primary
			probe.resolverReplica = replica

			return &fakeResolver{}, nil
		},
		nil,
	)

	return probe
}

func TestConnectOpensASinglePoolWhenNoReplicaIsConfigured(t *testing.T) {
	tests := []struct {
		name        string
		replicaDSN  string
		wantOpened  int
		wantReplica bool
	}{
		{name: "empty replica dsn", replicaDSN: "", wantOpened: 1},
		{name: "whitespace replica dsn", replicaDSN: "   ", wantOpened: 1},
		{name: "replica dsn equal to primary", replicaDSN: primaryTestDSN, wantOpened: 1},
		{name: "replica dsn equal to primary after trim", replicaDSN: primaryTestDSN + "  ", wantOpened: 1},
		// Positive control: a real, distinct replica still gets its own pool.
		{name: "distinct replica dsn", replicaDSN: replicaTestDSN, wantOpened: 2, wantReplica: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := installPoolOpenProbe(t)

			client, err := New(Config{PrimaryDSN: primaryTestDSN, ReplicaDSN: tt.replicaDSN})
			require.NoError(t, err)

			require.NoError(t, client.Connect(context.Background()))
			t.Cleanup(func() { _ = client.Close() })

			assert.Len(t, probe.opened, tt.wantOpened, "sql.Open calls: %v", probe.opened)
			assert.Equal(t, primaryTestDSN, probe.opened[0], "the first pool is always the primary")
			assert.Equal(t, 1, probe.resolverCalls)
			require.NotNil(t, probe.resolverPrimary)

			primary, err := client.Primary()
			require.NoError(t, err)
			assert.Same(t, probe.resolverPrimary, primary, "the resolver must be built over the kept primary handle")

			if tt.wantReplica {
				require.NotNil(t, probe.resolverReplica, "a distinct replica must reach the resolver")
				assert.NotSame(t, probe.resolverPrimary, probe.resolverReplica)
				assert.Equal(t, replicaTestDSN, probe.opened[1])
				assert.NotNil(t, client.replica)

				return
			}

			assert.Nil(t, probe.resolverReplica, "no replica pool may reach the resolver when none is configured")
			assert.Nil(t, client.replica, "the client must not keep a ghost replica handle")
		})
	}
}

func TestCreateResolverWithoutReplicaKeepsReadsOnThePrimary(t *testing.T) {
	primary := testDB(t)

	t.Run("nil replica builds a resolver with no replica set", func(t *testing.T) {
		resolver, err := createResolverFn(primary, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, resolver)
		t.Cleanup(func() { _ = resolver.Close() })

		require.Len(t, resolver.PrimaryDBs(), 1)
		assert.Same(t, primary, resolver.PrimaryDBs()[0])
		assert.Empty(t, resolver.ReplicaDBs(), "a replica set with a nil handle would panic on the first read")
	})

	t.Run("distinct replica is registered as replica", func(t *testing.T) {
		replica := testDB(t)

		resolver, err := createResolverFn(primary, replica, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resolver.Close() })

		require.Len(t, resolver.ReplicaDBs(), 1)
		assert.Same(t, replica, resolver.ReplicaDBs()[0])
	})
}

// TestSinglePoolServesReadsAndWrites drives a real dbresolver (only the driver
// is fake) and checks WHERE each statement lands: with no replica both the
// read and the write hit the primary DSN and no connection is ever opened for
// a replica; with a distinct replica the read goes to the replica (control).
func TestSinglePoolServesReadsAndWrites(t *testing.T) {
	tests := []struct {
		name         string
		replicaDSN   string
		wantReadOn   string
		wantReplicas int
	}{
		{name: "empty replica: read and write on the primary", replicaDSN: "", wantReadOn: primaryTestDSN},
		{name: "replica equal to primary: read and write on the primary", replicaDSN: primaryTestDSN, wantReadOn: primaryTestDSN},
		{name: "distinct replica: read on the replica", replicaDSN: replicaTestDSN, wantReadOn: replicaTestDSN, wantReplicas: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newRecordingDriver(t)

			withPatchedDependencies(t,
				func(_, dsn string) (*sql.DB, error) { return sql.Open(recorder.name, dsn) },
				createResolverFn,
				nil,
			)

			client, err := New(Config{PrimaryDSN: primaryTestDSN, ReplicaDSN: tt.replicaDSN})
			require.NoError(t, err)
			require.NoError(t, client.Connect(context.Background()))
			t.Cleanup(func() { _ = client.Close() })

			resolver, err := client.Resolver(context.Background())
			require.NoError(t, err)

			rows, err := resolver.QueryContext(context.Background(), "SELECT 1")
			require.NoError(t, err)
			require.True(t, rows.Next())
			require.NoError(t, rows.Close())

			_, err = resolver.ExecContext(context.Background(), "INSERT INTO t VALUES (1)")
			require.NoError(t, err)

			assert.Equal(t, []string{"SELECT 1"}, recorder.statements(tt.wantReadOn, "query"))
			assert.Equal(t, []string{"INSERT INTO t VALUES (1)"}, recorder.statements(primaryTestDSN, "exec"))

			if tt.wantReplicas == 0 {
				assert.Zero(t, recorder.connections(replicaTestDSN), "no connection may be dialed for a replica that does not exist")
			} else {
				assert.Positive(t, recorder.connections(replicaTestDSN))
			}
		})
	}
}

func TestSinglePoolCloseAndReconnectCloseThePoolOnce(t *testing.T) {
	probe := installPoolOpenProbe(t)

	client, err := New(Config{PrimaryDSN: primaryTestDSN})
	require.NoError(t, err)
	require.NoError(t, client.Connect(context.Background()))

	first, err := client.Primary()
	require.NoError(t, err)

	// Reconnect swaps the single pool: the old handle must be closed, the new
	// one live, and still exactly one pool per connection.
	require.NoError(t, client.Connect(context.Background()))
	assert.Len(t, probe.opened, 2, "one pool per Connect, never two")

	second, err := client.Primary()
	require.NoError(t, err)
	assert.NotSame(t, first, second)
	assert.EqualError(t, first.PingContext(context.Background()), "sql: database is closed",
		"the replaced pool must be closed on swap")
	assert.Nil(t, client.replica)

	require.NoError(t, client.Close(), "Close must not report a double-close error")
	assert.EqualError(t, second.PingContext(context.Background()), "sql: database is closed")

	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected)

	assert.NotPanics(t, func() { require.NoError(t, client.Close()) }, "a second Close is a no-op")
}

func TestValidateAcceptsAnEmptyReplicaDSN(t *testing.T) {
	t.Parallel()

	t.Run("empty replica is valid", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, Config{PrimaryDSN: "dsn", ReplicaDSN: ""}.validate())
	})

	t.Run("replica equal to primary is valid", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.validate())
	})

	t.Run("malformed replica is still refused", func(t *testing.T) {
		t.Parallel()

		err := Config{PrimaryDSN: "dsn", ReplicaDSN: "postgres://bad host:5432/db"}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
		assert.Contains(t, err.Error(), "replica dsn")
	})

	t.Run("New accepts a config without replica", func(t *testing.T) {
		t.Parallel()

		client, err := New(Config{PrimaryDSN: primaryTestDSN})
		require.NoError(t, err)
		assert.NotNil(t, client)
	})
}

func TestConfigHasReplica(t *testing.T) {
	t.Parallel()

	assert.False(t, Config{PrimaryDSN: "a"}.hasReplica())
	assert.False(t, Config{PrimaryDSN: "a", ReplicaDSN: " a "}.hasReplica())
	assert.True(t, Config{PrimaryDSN: "a", ReplicaDSN: "b"}.hasReplica())
}

// ---------------------------------------------------------------------------
// recordingDriver: an in-memory database/sql driver that records, per DSN, the
// connections opened and the statements executed. It lets a test observe the
// routing decisions of a REAL dbresolver without a database.
// ---------------------------------------------------------------------------

type recordingDriver struct {
	name string

	mu    sync.Mutex
	conns map[string]int
	stmts map[string][]recordedStatement
}

type recordedStatement struct {
	kind  string
	query string
}

var (
	recordingDriverSeq   int
	recordingDriverSeqMu sync.Mutex
)

func newRecordingDriver(t *testing.T) *recordingDriver {
	t.Helper()

	recordingDriverSeqMu.Lock()
	recordingDriverSeq++
	name := "recording-driver-" + strconv.Itoa(recordingDriverSeq)
	recordingDriverSeqMu.Unlock()

	d := &recordingDriver{
		name:  name,
		conns: map[string]int{},
		stmts: map[string][]recordedStatement{},
	}

	sql.Register(name, d)

	return d
}

func (d *recordingDriver) Open(dsn string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.conns[dsn]++

	return &recordingConn{driver: d, dsn: dsn}, nil
}

func (d *recordingDriver) record(dsn, kind, query string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stmts[dsn] = append(d.stmts[dsn], recordedStatement{kind: kind, query: query})
}

func (d *recordingDriver) connections(dsn string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.conns[dsn]
}

func (d *recordingDriver) statements(dsn, kind string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []string

	for _, s := range d.stmts[dsn] {
		if s.kind == kind {
			out = append(out, s.query)
		}
	}

	return out
}

type recordingConn struct {
	driver *recordingDriver
	dsn    string
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recordingConn: Prepare is not supported")
}

func (c *recordingConn) Close() error { return nil }

func (c *recordingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recordingConn: Begin is not supported")
}

func (c *recordingConn) Ping(context.Context) error { return nil }

func (c *recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.record(c.dsn, "exec", query)

	return driver.RowsAffected(1), nil
}

func (c *recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.record(c.dsn, "query", query)

	return &singleRow{}, nil
}

type singleRow struct{ done bool }

func (r *singleRow) Columns() []string { return []string{"one"} }

func (r *singleRow) Close() error { return nil }

func (r *singleRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}

	r.done = true
	dest[0] = int64(1)

	return nil
}
