//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// Helper: minimal dbresolver.DB that supports PingContext and Close
// -------------------------------------------------------------------

type healthyDB struct {
	closeErr error
}

var _ dbresolver.DB = (*healthyDB)(nil)

func (h *healthyDB) Begin() (dbresolver.Tx, error) { return nil, nil }
func (h *healthyDB) BeginTx(_ context.Context, _ *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}
func (h *healthyDB) Close() error                                        { return h.closeErr }
func (h *healthyDB) Conn(_ context.Context) (dbresolver.Conn, error)     { return nil, nil }
func (h *healthyDB) Driver() driver.Driver                               { return nil }
func (h *healthyDB) Exec(_ string, _ ...interface{}) (sql.Result, error) { return nil, nil }
func (h *healthyDB) ExecContext(_ context.Context, _ string, _ ...interface{}) (sql.Result, error) {
	return nil, nil
}
func (h *healthyDB) Ping() error                               { return nil }
func (h *healthyDB) PingContext(_ context.Context) error       { return nil }
func (h *healthyDB) Prepare(_ string) (dbresolver.Stmt, error) { return nil, nil }
func (h *healthyDB) PrepareContext(_ context.Context, _ string) (dbresolver.Stmt, error) {
	return nil, nil
}
func (h *healthyDB) Query(_ string, _ ...interface{}) (*sql.Rows, error) { return nil, nil }
func (h *healthyDB) QueryContext(_ context.Context, _ string, _ ...interface{}) (*sql.Rows, error) {
	return nil, nil
}
func (h *healthyDB) QueryRow(_ string, _ ...interface{}) *sql.Row { return nil }
func (h *healthyDB) QueryRowContext(_ context.Context, _ string, _ ...interface{}) *sql.Row {
	return nil
}
func (h *healthyDB) SetConnMaxIdleTime(_ time.Duration) {}
func (h *healthyDB) SetConnMaxLifetime(_ time.Duration) {}
func (h *healthyDB) SetMaxIdleConns(_ int)              {}
func (h *healthyDB) SetMaxOpenConns(_ int)              {}
func (h *healthyDB) PrimaryDBs() []*sql.DB              { return nil }
func (h *healthyDB) ReplicaDBs() []*sql.DB              { return nil }
func (h *healthyDB) Stats() sql.DBStats                 { return sql.DBStats{} }

// resolverPtr wraps a dbresolver.DB as *dbresolver.DB for PostgresConnection.
func resolverPtr(db dbresolver.DB) *dbresolver.DB {
	return &db
}

// -------------------------------------------------------------------
// PostgresConnection.GetDB (line 177 — always-nil path)
// -------------------------------------------------------------------

func TestPostgresConnection_GetDB_NilReceiver(t *testing.T) {
	t.Parallel()

	var c *PostgresConnection
	_, err := c.GetDB()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestPostgresConnection_GetDB_NilConnectionDB(t *testing.T) {
	t.Parallel()

	c := &PostgresConnection{}
	_, err := c.GetDB()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestPostgresConnection_GetDB_ValidConnectionDB(t *testing.T) {
	t.Parallel()

	db := &healthyDB{}
	c := &PostgresConnection{ConnectionDB: resolverPtr(db)}

	result, err := c.GetDB()
	require.NoError(t, err)
	assert.NotNil(t, result)
}

// -------------------------------------------------------------------
// closePostgresConn — valid DB path (nil paths are in extra_test.go)
// -------------------------------------------------------------------

func TestClosePostgresConn_ValidDB(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	db := &healthyDB{}
	conn := &PostgresConnection{ConnectionDB: resolverPtr(db)}

	assert.NotPanics(t, func() {
		m.closePostgresConn(conn, "msg for %s", "tenant-1")
	})
}

// -------------------------------------------------------------------
// swapPostgresConnection
// -------------------------------------------------------------------

func TestSwapPostgresConnection_ReplacesEntry(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	oldConn := &PostgresConnection{}
	m.connections["tenant-swap"] = oldConn
	m.lastAccessed["tenant-swap"] = time.Now().Add(-1 * time.Hour)

	newConn := &PostgresConnection{}
	m.swapPostgresConnection("tenant-swap", newConn)

	m.mu.RLock()
	stored := m.connections["tenant-swap"]
	m.mu.RUnlock()

	assert.Same(t, newConn, stored, "new connection should replace old one")
}

// -------------------------------------------------------------------
// resolveConnectionPoolSettings — additional branches
// -------------------------------------------------------------------

func TestResolveConnectionPoolSettings_DefaultsWhenNoConfig(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxOpenConns(10),
		WithMaxIdleConns(5),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	cfg := &core.TenantConfig{}

	maxOpen, maxIdle := m.resolveConnectionPoolSettings(cfg, "tenant-1", logger)
	assert.Equal(t, 10, maxOpen)
	assert.Equal(t, 5, maxIdle)
}

func TestResolveConnectionPoolSettings_AppliesPerModuleSettings(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		WithMaxOpenConns(5),
		WithMaxIdleConns(2),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	cfg := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 20,
					MaxIdleConns: 10,
				},
			},
		},
	}

	maxOpen, maxIdle := m.resolveConnectionPoolSettings(cfg, "tenant-1", logger)
	assert.Equal(t, 20, maxOpen)
	assert.Equal(t, 10, maxIdle)
}

func TestResolveConnectionPoolSettings_ZeroInSettingsRestoresDefault(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxOpenConns(10),
		WithMaxIdleConns(5),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	// ConnectionSettings present but MaxOpenConns=0 and MaxIdleConns=0
	cfg := &core.TenantConfig{
		ConnectionSettings: &core.ConnectionSettings{
			MaxOpenConns: 0,
			MaxIdleConns: 0,
		},
	}

	maxOpen, maxIdle := m.resolveConnectionPoolSettings(cfg, "tenant-1", logger)
	// Zero values should restore to manager defaults
	assert.Equal(t, 10, maxOpen)
	assert.Equal(t, 5, maxIdle)
}

func TestResolveConnectionPoolSettings_ClampsExcessiveSettings(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithConnectionLimitCaps(15, 8),
		WithMaxOpenConns(5),
		WithMaxIdleConns(2),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	cfg := &core.TenantConfig{
		ConnectionSettings: &core.ConnectionSettings{
			MaxOpenConns: 100,
			MaxIdleConns: 50,
		},
	}

	maxOpen, maxIdle := m.resolveConnectionPoolSettings(cfg, "tenant-1", logger)
	assert.Equal(t, 15, maxOpen)
	assert.Equal(t, 8, maxIdle)
}

// -------------------------------------------------------------------
// clampPoolSettings — additional branches with logger
// -------------------------------------------------------------------

func TestClampPoolSettings_NoCap(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger")

	logger := logcompat.New(testutil.NewMockLogger())
	maxOpen, maxIdle := m.clampPoolSettings(25, 10, "tenant-1", logger)
	assert.Equal(t, 25, maxOpen)
	assert.Equal(t, 10, maxIdle)
}

func TestClampPoolSettings_CapsOpenConns_WithLogger(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithConnectionLimitCaps(10, 0),
		WithLogger(testutil.NewMockLogger()),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	maxOpen, maxIdle := m.clampPoolSettings(50, 5, "tenant-1", logger)
	assert.Equal(t, 10, maxOpen)
	assert.Equal(t, 5, maxIdle)
}

func TestClampPoolSettings_CapsIdleConns_WithLogger(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithConnectionLimitCaps(0, 3),
		WithLogger(testutil.NewMockLogger()),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	maxOpen, maxIdle := m.clampPoolSettings(20, 20, "tenant-1", logger)
	assert.Equal(t, 20, maxOpen)
	assert.Equal(t, 3, maxIdle)
}

// -------------------------------------------------------------------
// resolveReplicaConnection
// -------------------------------------------------------------------

func TestResolveReplicaConnection_NoReplicaConfig(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger")

	logger := logcompat.New(testutil.NewMockLogger())
	cfg := &core.TenantConfig{}
	pgConfig := &core.PostgreSQLConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		SSLMode:  "disable",
	}

	primaryStr := "postgres://user:pass@localhost:5432/testdb?sslmode=disable"
	connStr, dbName, err := m.resolveReplicaConnection(cfg, pgConfig, primaryStr, "tenant-1", logger)
	require.NoError(t, err)
	assert.Equal(t, primaryStr, connStr)
	assert.Equal(t, "testdb", dbName)
}

func TestResolveReplicaConnection_WithReplicaConfig(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger",
		WithModule("onboarding"),
		WithLogger(testutil.NewMockLogger()),
	)

	logger := logcompat.New(testutil.NewMockLogger())
	cfg := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				PostgreSQLReplica: &core.PostgreSQLConfig{
					Host:     "replica-host",
					Port:     5432,
					Database: "testdb-replica",
					SSLMode:  "disable",
				},
			},
		},
	}
	pgConfig := &core.PostgreSQLConfig{
		Host:     "primary-host",
		Port:     5432,
		Database: "testdb",
		SSLMode:  "disable",
	}

	primaryStr := "postgres://primary-host:5432/testdb?sslmode=disable"
	connStr, dbName, err := m.resolveReplicaConnection(cfg, pgConfig, primaryStr, "tenant-1", logger)
	require.NoError(t, err)
	assert.NotEqual(t, primaryStr, connStr)
	assert.Equal(t, "testdb-replica", dbName)
	assert.Contains(t, connStr, "replica-host")
}

// -------------------------------------------------------------------
// WithDefaultConnection (deprecated builder method)
// -------------------------------------------------------------------

func TestWithDefaultConnection_Builder(t *testing.T) {
	t.Parallel()

	conn := &PostgresConnection{
		ConnectionStringPrimary: "postgres://localhost:5432/defaultdb?sslmode=disable",
	}

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger").WithDefaultConnection(conn)

	require.NotNil(t, m)
	assert.Same(t, conn, m.defaultConn)
}

// -------------------------------------------------------------------
// ConnectedTenantIDs with multiple connections
// -------------------------------------------------------------------

func TestConnectedTenantIDs_ReturnsBothEntries(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger")

	// Pre-populate with two connections (ConnectedTenantIDs returns all in map)
	db := &healthyDB{}
	m.connections["tenant-healthy"] = &PostgresConnection{ConnectionDB: resolverPtr(db)}
	m.connections["tenant-nil"] = &PostgresConnection{}

	ids := m.ConnectedTenantIDs()
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "tenant-healthy")
	assert.Contains(t, ids, "tenant-nil")
}

// -------------------------------------------------------------------
// tryReuseOrEvictCachedConnectionLocked
// -------------------------------------------------------------------

func TestTryReuseOrEvictCachedConnectionLocked_MissingEntry(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger")

	m.mu.Lock()
	logger := logcompat.New(testutil.NewMockLogger())
	conn, ok := m.tryReuseOrEvictCachedConnectionLocked(context.Background(), "tenant-missing", logger)
	m.mu.Unlock()

	assert.Nil(t, conn)
	assert.False(t, ok)
}

func TestTryReuseOrEvictCachedConnectionLocked_NilConnectionDB(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger")

	// Entry exists but ConnectionDB is nil: should be deleted
	m.connections["tenant-nildb"] = &PostgresConnection{}

	m.mu.Lock()
	logger := logcompat.New(testutil.NewMockLogger())
	conn, ok := m.tryReuseOrEvictCachedConnectionLocked(context.Background(), "tenant-nildb", logger)
	m.mu.Unlock()

	assert.Nil(t, conn)
	assert.False(t, ok)

	// Entry should be deleted from the map
	m.mu.RLock()
	_, exists := m.connections["tenant-nildb"]
	m.mu.RUnlock()
	assert.False(t, exists)
}

func TestTryReuseOrEvictCachedConnectionLocked_HealthyConnection(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger")

	db := &healthyDB{}
	cached := &PostgresConnection{ConnectionDB: resolverPtr(db)}
	m.connections["tenant-healthy"] = cached

	m.mu.Lock()
	logger := logcompat.New(testutil.NewMockLogger())
	conn, ok := m.tryReuseOrEvictCachedConnectionLocked(context.Background(), "tenant-healthy", logger)
	m.mu.Unlock()

	assert.True(t, ok)
	assert.Same(t, cached, conn)
}

// -------------------------------------------------------------------
// getPostgresConfigForTenant — indirect via GetConnection
// -------------------------------------------------------------------

func TestGetConnection_NilClient_ReturnsError(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger", WithLogger(testutil.NewMockLogger()))

	_, err := m.GetConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant manager client is required")
}

// -------------------------------------------------------------------
// Connect — nil receiver guard
// -------------------------------------------------------------------

func TestPostgresConnection_Connect_NilReceiver(t *testing.T) {
	t.Parallel()

	var c *PostgresConnection
	err := c.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}
