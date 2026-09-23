//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPool returns an open sqlmock-backed pool and its expectation recorder.
// Expectations are asserted at cleanup, so a pool that is touched when the test
// said it should not be touched fails loudly.
func mockPool(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	t.Cleanup(func() {
		assert.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})

	return db, mock
}

// failingOpen installs a dialer that fails the test if anything tries to dial.
// NewFromPools must never open a connection: the pools are already open.
func failingOpen(t *testing.T) {
	t.Helper()

	original := dbOpenFn

	dbOpenFn = func(_, _ string) (*sql.DB, error) {
		t.Error("NewFromPools must not dial; the caller's pools are already open")

		return nil, assert.AnError
	}

	t.Cleanup(func() { dbOpenFn = original })
}

func TestNewFromPoolsNilPrimary(t *testing.T) {
	replica, _ := mockPool(t)

	tests := []struct {
		name    string
		replica *sql.DB
	}{
		{name: "no replica either"},
		{name: "replica present", replica: replica},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewFromPools(nil, tt.replica, Config{})

			require.ErrorIs(t, err, ErrNilPool)
			assert.Nil(t, client)
		})
	}
}

// TestNewFromPoolsServesInjectedPools pins the whole point of the constructor:
// the returned client answers from the handles it was given, with no dial and
// no DSN, and a query routed through the resolver reaches the caller's mock.
func TestNewFromPoolsServesInjectedPools(t *testing.T) {
	tests := []struct {
		name        string
		withReplica bool
	}{
		{name: "primary only"},
		{name: "primary and replica", withReplica: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failingOpen(t)

			primary, primaryMock := mockPool(t)

			var replica *sql.DB

			readMock := primaryMock

			if tt.withReplica {
				var replicaMock sqlmock.Sqlmock

				replica, replicaMock = mockPool(t)
				readMock = replicaMock
			}

			// cfg carries no DSN on purpose: a dial would need one.
			client, err := NewFromPools(primary, replica, Config{})
			require.NoError(t, err)
			require.NotNil(t, client)

			connected, err := client.IsConnected()
			require.NoError(t, err)
			assert.True(t, connected, "an injected client is connected from the start")

			gotPrimary, err := client.Primary()
			require.NoError(t, err)
			assert.Same(t, primary, gotPrimary, "Primary must be the injected handle, not a replacement")

			resolver, err := client.Resolver(context.Background())
			require.NoError(t, err)
			require.NotNil(t, resolver)

			require.Len(t, resolver.PrimaryDBs(), 1)
			assert.Same(t, primary, resolver.PrimaryDBs()[0])

			if tt.withReplica {
				require.Len(t, resolver.ReplicaDBs(), 1)
				assert.Same(t, replica, resolver.ReplicaDBs()[0])
				assert.Same(t, replica, client.replica, "the client must keep the replica handle it was given")
			} else {
				assert.Empty(t, resolver.ReplicaDBs(), "a nil replica must leave the replica set empty")
				assert.Nil(t, client.replica, "the client must not keep a ghost replica handle")
			}

			// A read through the resolver must land on a real injected pool:
			// the replica when there is one, the primary otherwise.
			readMock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

			var n int

			require.NoError(t, resolver.QueryRowContext(context.Background(), "SELECT 1").Scan(&n))
			assert.Equal(t, 1, n)
		})
	}
}

// TestNewFromPoolsDefaultsConfig checks the client behaves like one built by
// New for the config the injected path still reads: a zero Config must come out
// with a non-nil logger and the package pool defaults, without the DSN
// validation New would apply.
func TestNewFromPoolsDefaultsConfig(t *testing.T) {
	primary, _ := mockPool(t)

	client, err := NewFromPools(primary, nil, Config{})
	require.NoError(t, err)

	assert.NotNil(t, client.cfg.Logger, "a nil logger must be defaulted, not carried")
	assert.Equal(t, defaultMaxOpenConns, client.cfg.MaxOpenConnections)
	assert.Equal(t, defaultMaxIdleConns, client.cfg.MaxIdleConnections)
	assert.Equal(t, defaultConnMaxLifetime, client.cfg.ConnMaxLifetime)
	assert.Equal(t, defaultConnMaxIdleTime, client.cfg.ConnMaxIdleTime)
	assert.Empty(t, client.cfg.PrimaryDSN, "no DSN is required or invented")
	assert.Empty(t, client.statsCleanups, "injected pools register no telemetry cleanups")
}

// TestNewFromPoolsCloseClosesInjectedPools pins the ownership transfer: Close
// closes the handles the caller injected, and the client then reports itself
// disconnected.
func TestNewFromPoolsCloseClosesInjectedPools(t *testing.T) {
	primary, primaryMock := mockPool(t)
	replica, replicaMock := mockPool(t)

	client, err := NewFromPools(primary, replica, Config{})
	require.NoError(t, err)

	primaryMock.ExpectClose()
	replicaMock.ExpectClose()

	require.NoError(t, client.Close())

	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected)

	gotPrimary, err := client.Primary()
	require.ErrorIs(t, err, ErrNotConnected)
	assert.Nil(t, gotPrimary)

	assert.ErrorContains(t, primary.PingContext(context.Background()), "database is closed")
	assert.ErrorContains(t, replica.PingContext(context.Background()), "database is closed")
}

// TestNewFromPoolsResolverFailure covers the only other error path: the shared
// resolver builder refusing to produce a resolver.
func TestNewFromPoolsResolverFailure(t *testing.T) {
	primary, _ := mockPool(t)

	original := createResolverFn
	createResolverFn = func(*sql.DB, *sql.DB, obs.Logger) (dbresolver.DB, error) {
		return nil, assert.AnError
	}

	t.Cleanup(func() { createResolverFn = original })

	client, err := NewFromPools(primary, nil, Config{})

	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, client)
}
