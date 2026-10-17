//go:build integration

package postgres

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// requireTenantNotice raises message on a pinned primary connection of the tenant's current pool
// and asserts the callback saw it on that backend, in the expected database.
func requireTenantNotice(t *testing.T, manager *Manager, pids *sync.Map, wantDatabase, message string) *PostgresConnection {
	t.Helper()

	ctx := context.Background()

	tenantConn, err := manager.GetConnection(ctx, "tenant-a")
	require.NoError(t, err)

	db, err := tenantConn.GetDB()
	require.NoError(t, err)
	require.Len(t, db.PrimaryDBs(), 1)

	pinned, err := db.PrimaryDBs()[0].Conn(ctx)
	require.NoError(t, err)

	defer func() { _ = pinned.Close() }()

	var (
		pid     uint32
		current string
	)

	require.NoError(t, pinned.QueryRowContext(ctx, "SELECT pg_backend_pid(), current_database()").Scan(&pid, &current))
	assert.Equal(t, wantDatabase, current)

	_, err = pinned.ExecContext(ctx, fmt.Sprintf("DO $$ BEGIN RAISE NOTICE '%s'; END $$", message))
	require.NoError(t, err)

	got, seen := pids.Load(message)
	require.True(t, seen, "OnNotice must receive %q", message)
	assert.Equal(t, pid, got, "%q must be attributed to the backend that raised it", message)

	return tenantConn
}

// The manager's callback must reach the pool GetConnection dials and the pool a config-change
// reconnect replaces it with, attributed to the backend that raised each notice.
func TestIntegration_Manager_OnNoticeFollowsTenantReconnect(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("tenant_a"),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
			hostConfig.PortBindings = network.PortMap{
				network.MustParsePort("5432/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1")}},
			}
		}),
	)
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)

	port, err := pg.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)

	var database atomic.Value
	database.Store("tenant_a")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id": "tenant-a", "databases": {"onboarding": {"postgresql": {
			"host": "127.0.0.1", "port": %d, "database": %q, "username": "postgres", "password": "postgres"}}}}`,
			port.Num(), database.Load())
	}))
	t.Cleanup(server.Close)

	tmClient, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	var pids sync.Map // notice message -> backend pid; one server, so the pid is unambiguous

	manager := NewManager(tmClient, "ledger",
		WithModule("onboarding"),
		WithConnectionsCheckInterval(time.Millisecond),
		WithOnNotice(func(conn *pgconn.PgConn, notice *pgconn.Notice) { pids.Store(notice.Message, conn.PID()) }),
	)

	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	first := requireTenantNotice(t, manager, &pids, "tenant_a", "first-pool")

	// Moving the tenant to the image's built-in postgres database is a config change: a cache hit
	// past the check interval starts the async revalidation that reconnects.
	database.Store("postgres")

	_, err = manager.GetConnection(ctx, "tenant-a")
	require.NoError(t, err)
	manager.revalidateWG.Wait()

	replaced := requireTenantNotice(t, manager, &pids, "postgres", "reconnected-pool")
	assert.NotSame(t, first, replaced, "the config change must replace the tenant connection")
}
