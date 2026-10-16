//go:build unit

package postgres

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	libPostgres "github.com/LerianStudio/lib-commons/v7/commons/postgres"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	liblog "github.com/LerianStudio/lib-observability/v4/log"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const postureTenantPassword = "tenant-pw-must-not-leak"

// countingListener accepts TCP connections and closes them at once, counting
// each one: any dial the manager attempts toward the tenant database shows up
// here, which is how these tests prove a refusal happened before dialing.
type countingListener struct {
	ln       net.Listener
	accepted atomic.Int32
}

func newCountingListener(t *testing.T) *countingListener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cl := &countingListener{ln: ln}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			cl.accepted.Add(1)
			_ = conn.Close()
		}
	}()

	t.Cleanup(func() { _ = ln.Close() })

	return cl
}

func (c *countingListener) port(t *testing.T) int {
	t.Helper()

	_, portStr, err := net.SplitHostPort(c.ln.Addr().String())
	require.NoError(t, err)

	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	return port
}

func postureTenantPG(port int, sslmode string) *core.PostgreSQLConfig {
	return &core.PostgreSQLConfig{
		Host: "127.0.0.1", Port: port, Database: "ledger",
		Username: "app", Password: postureTenantPassword, SSLMode: sslmode,
	}
}

// serveTenantConfig answers every Tenant Manager request with cfg.
func serveTenantConfig(t *testing.T, cfg *core.TenantConfig) *httptest.Server {
	t.Helper()

	body, err := json.Marshal(cfg)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	return server
}

func postureTenantConfig(primary, replica *core.PostgreSQLConfig) *core.TenantConfig {
	return &core.TenantConfig{
		ID: "tenant-1",
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {PostgreSQL: primary, PostgreSQLReplica: replica},
		},
	}
}

// TestManager_TLSPosture_UnsetSSLModeRefusedBeforeDial: an empty tenant
// sslmode would become "disable" in the built DSN; under a posture it is read
// as unset and refused, naming the tenant, before anything is dialed.
func TestManager_TLSPosture_UnsetSSLModeRefusedBeforeDial(t *testing.T) {
	t.Parallel()

	db := newCountingListener(t)
	server := serveTenantConfig(t, postureTenantConfig(postureTenantPG(db.port(t), ""), nil))

	m := NewManager(mustNewTestClient(t, server.URL), "ledger",
		WithModule("onboarding"),
		WithLogger(testutil.NewMockLogger()),
		WithTLSPosture(libPostgres.TLSPostureHardened, ""),
	)

	_, err := m.GetConnection(context.Background(), "tenant-1")
	require.ErrorIs(t, err, libPostgres.ErrWeakSSLMode)
	assert.Contains(t, err.Error(), "tenant-1")
	assert.NotContains(t, err.Error(), postureTenantPassword)

	var weak *libPostgres.WeakSSLModeError
	require.ErrorAs(t, err, &weak)
	assert.Equal(t, "primary", weak.Label)
	assert.Empty(t, weak.Got)
	assert.Equal(t, libPostgres.SSLModeVerifyFull, weak.Min)

	assert.Zero(t, db.accepted.Load(), "the posture must refuse before any dial")
	assert.Empty(t, m.ConnectedTenantIDs())
}

// TestManager_TLSPosture_StrongModeReachesTheDial is the positive control for
// the test above: the same fixture with verify-full passes the posture and
// does dial (and then fails, since the listener is not Postgres).
func TestManager_TLSPosture_StrongModeReachesTheDial(t *testing.T) {
	t.Parallel()

	db := newCountingListener(t)
	server := serveTenantConfig(t, postureTenantConfig(postureTenantPG(db.port(t), "verify-full"), nil))

	m := NewManager(mustNewTestClient(t, server.URL), "ledger",
		WithModule("onboarding"),
		WithLogger(testutil.NewMockLogger()),
		WithTLSPosture(libPostgres.TLSPostureSaaS, ""),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := m.GetConnection(ctx, "tenant-1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, libPostgres.ErrWeakSSLMode)
	assert.Positive(t, db.accepted.Load(), "a mode that meets the floor must reach the dial")
}

func TestManager_TLSPosture_WeakReplicaRefused(t *testing.T) {
	t.Parallel()

	db := newCountingListener(t)
	port := db.port(t)
	server := serveTenantConfig(t, postureTenantConfig(postureTenantPG(port, "verify-full"), postureTenantPG(port, "require")))

	m := NewManager(mustNewTestClient(t, server.URL), "ledger",
		WithModule("onboarding"),
		WithLogger(testutil.NewMockLogger()),
		WithTLSPosture(libPostgres.TLSPostureHardened, libPostgres.SSLModeVerifyCA),
	)

	_, err := m.GetConnection(context.Background(), "tenant-1")

	var weak *libPostgres.WeakSSLModeError
	require.ErrorAs(t, err, &weak)
	assert.Equal(t, "replica", weak.Label)
	assert.Equal(t, "require", weak.Got)
	assert.Equal(t, libPostgres.SSLModeVerifyCA, weak.Min)
	assert.Contains(t, err.Error(), "tenant-1")
	assert.NotContains(t, err.Error(), postureTenantPassword)
	assert.Zero(t, db.accepted.Load())
}

// TestManager_TLSPosture_InvalidOptionFailsEveryConnection: NewManager cannot
// return an error, so an invalid posture is logged at construction and every
// GetConnection refuses with ErrInvalidConfig.
func TestManager_TLSPosture_InvalidOptionFailsEveryConnection(t *testing.T) {
	t.Parallel()

	logger := testutil.NewLevelCapturingLogger()
	m := NewManager(mustNewTestClient(t, "http://localhost:8080"), "ledger",
		WithLogger(logger),
		WithTLSPosture(libPostgres.TLSPostureSaaS, libPostgres.SSLModeRequire),
	)

	assert.True(t, logger.ContainsAtLevel(liblog.LevelError, "TLS posture"), "got %v", logger.Entries())

	_, err := m.GetConnection(context.Background(), "tenant-1")
	require.ErrorIs(t, err, libPostgres.ErrInvalidConfig)

	_, err = m.GetDB(context.Background(), "tenant-1")
	require.ErrorIs(t, err, libPostgres.ErrInvalidConfig)
}

// TestManager_TLSPosture_ReconnectToWeakModeKeepsOldConnection: a settings
// revalidation that finds the tenant moved to a weaker sslmode refuses the new
// connection, keeps the current one and logs ERROR.
func TestManager_TLSPosture_ReconnectToWeakModeKeepsOldConnection(t *testing.T) {
	t.Parallel()

	db := newCountingListener(t)
	port := db.port(t)

	logger := testutil.NewLevelCapturingLogger()
	m := NewManager(mustNewTestClient(t, "http://localhost:8080"), "ledger",
		WithModule("onboarding"),
		WithLogger(logger),
		WithTLSPosture(libPostgres.TLSPostureHardened, ""),
	)

	var cachedDB dbresolver.DB = &pingableDB{}

	cachedDSN, err := BuildConnectionString(postureTenantPG(port, "verify-full"))
	require.NoError(t, err)

	cached := &PostgresConnection{ConnectionStringPrimary: cachedDSN, ConnectionDB: &cachedDB}
	m.connections["tenant-1"] = cached
	m.lastAccessed["tenant-1"] = time.Now()

	reconnected := m.detectAndReconnectPostgres(context.Background(), "tenant-1",
		postureTenantConfig(postureTenantPG(port, "require"), nil))

	assert.True(t, reconnected, "a changed config is a reconnection attempt")
	assert.Same(t, cached, m.connections["tenant-1"], "the current connection must be kept")
	assert.Zero(t, db.accepted.Load(), "the weak config must not be dialed")
	assert.True(t, logger.ContainsAtLevel(liblog.LevelError, "tenant-1", "sslmode"), "got %v", logger.Entries())

	for _, e := range logger.Entries() {
		assert.NotContains(t, e.Message, postureTenantPassword)
	}
}

// TestManager_TLSPosture_DefaultIsNoPosture: without the option the
// manager does not apply a posture, so an unset tenant sslmode still reaches
// the client's default rule (which ALLOW_INSECURE_TLS may lift).
func TestManager_TLSPosture_DefaultIsNoPosture(t *testing.T) {
	t.Parallel()

	m := NewManager(mustNewTestClient(t, "http://localhost:8080"), "ledger")
	assert.NoError(t, m.checkTenantTLSPosture("tenant-1", postureTenantConfig(postureTenantPG(5432, ""), nil), postureTenantPG(5432, "")))
}

func TestPostgresConnection_ConnectAppliesPosture(t *testing.T) {
	t.Parallel()

	conn := &PostgresConnection{
		ConnectionStringPrimary: "postgres://app:" + postureTenantPassword + "@127.0.0.1:1/ledger?sslmode=require",
		TLSPosture:              libPostgres.TLSPostureSaaS,
	}

	err := conn.Connect(context.Background())
	require.ErrorIs(t, err, libPostgres.ErrWeakSSLMode)
	assert.NotContains(t, err.Error(), postureTenantPassword)

	raw, err := json.Marshal(conn)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "TLSPosture")
	assert.NotContains(t, string(raw), "MinSSLMode")
}
