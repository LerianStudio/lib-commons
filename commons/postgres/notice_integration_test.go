//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

// loopbackOnly publishes the container's PostgreSQL port on 127.0.0.1 instead of every interface.
func loopbackOnly() testcontainers.CustomizeRequestOption {
	return testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
		hostConfig.PortBindings = network.PortMap{
			network.MustParsePort("5432/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1")}},
		}
	})
}

// noticeLog maps each notice message to the backend pid it arrived on.
type noticeLog struct {
	mu   sync.Mutex
	seen map[string]uint32
}

func (l *noticeLog) record(conn *pgconn.PgConn, notice *pgconn.Notice) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seen[notice.Message] = conn.PID()
}

func (l *noticeLog) pid(message string) (uint32, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	pid, ok := l.seen[message]

	return pid, ok
}

func pinConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()

	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// raiseNotice raises message on conn and returns conn's backend pid.
func raiseNotice(t *testing.T, conn *sql.Conn, message string) uint32 {
	t.Helper()

	ctx := context.Background()

	var pid uint32
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))

	_, err := conn.ExecContext(ctx, fmt.Sprintf("DO $$ BEGIN RAISE NOTICE '%s'; END $$", message))
	require.NoError(t, err)

	return pid
}

func requireNoticeOn(t *testing.T, notices *noticeLog, conn *sql.Conn, message string) {
	t.Helper()

	pid := raiseNotice(t, conn, message)

	got, ok := notices.pid(message)
	require.True(t, ok, "the notice handler must receive %q", message)
	assert.Equal(t, pid, got, "%q must be attributed to the connection that raised it", message)
}

func applicationName(t *testing.T, conn *sql.Conn) string {
	t.Helper()

	var name string
	require.NoError(t, conn.QueryRowContext(context.Background(), "SHOW application_name").Scan(&name))

	return name
}

// The notice handler must reach every new physical primary connection under telemetry wrapping, re-read
// the libpq environment per connection, stay off the replica and survive a pool replacement.
func TestIntegration_Postgres_OnNoticeFollowsPrimaryConnections(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t, loopbackOnly())
	t.Cleanup(cleanup)

	t.Setenv(commons.EnvAllowInsecureTLS, "true")
	t.Setenv("PGAPPNAME", "notice-first")

	notices := &noticeLog{seen: map[string]uint32{}}

	client, err := NewWithNotice(Config{
		PrimaryDSN: dsn,
		ReplicaDSN: dsn + "&application_name=notice-replica",
		Logger:     obs.Nop(),
	}, notices.record)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))

	primary, err := client.Primary()
	require.NoError(t, err)

	first := pinConn(t, primary)
	assert.Equal(t, "notice-first", applicationName(t, first))
	requireNoticeOn(t, notices, first, "primary-first")

	t.Setenv("PGAPPNAME", "notice-second")

	second := pinConn(t, primary)
	assert.Equal(t, "notice-second", applicationName(t, second), "each physical connection re-reads PG*")
	requireNoticeOn(t, notices, second, "primary-second")

	raiseNotice(t, pinConn(t, client.replica), "replica-only")

	_, seen := notices.pid("replica-only")
	assert.False(t, seen, "the replica pool must not call the primary callback")

	require.NoError(t, client.Connect(ctx))

	replaced, err := client.Primary()
	require.NoError(t, err)
	require.NotSame(t, primary, replaced, "Connect must replace the primary pool")
	requireNoticeOn(t, notices, pinConn(t, replaced), "primary-after-reconnect")
}
