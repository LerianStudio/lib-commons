//go:build unit

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNewPGTestClient(t *testing.T) *client.Client {
	t.Helper()

	c, err := client.NewClient("http://localhost:8080", testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	return c
}

// TestWithMaxOpenConns covers the option.
func TestWithMaxOpenConns(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithMaxOpenConns(50))
	assert.Equal(t, 50, m.maxOpenConns)
}

// TestWithMaxIdleConns covers the option.
func TestWithMaxIdleConns(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithMaxIdleConns(10))
	assert.Equal(t, 10, m.maxIdleConns)
}

// TestWithConnectionLimitCaps covers the option.
func TestWithConnectionLimitCaps(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithConnectionLimitCaps(100, 20))
	assert.Equal(t, 100, m.maxAllowedOpenConns)
	assert.Equal(t, 20, m.maxAllowedIdleConns)
}

func TestWithConnectionLimitCaps_ZeroIgnored(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithConnectionLimitCaps(0, 0))
	// Zero values do NOT update the field - they preserve the defaults
	// (defaultMaxAllowedOpenConns=200, defaultMaxAllowedIdleConns=50)
	assert.Greater(t, m.maxAllowedOpenConns, 0)
	assert.Greater(t, m.maxAllowedIdleConns, 0)
}

// TestGetDB_EmptyTenantID covers the empty tenant guard.
func TestGetDB_EmptyTenantID(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	_, err := m.GetDB(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

// TestConnectedTenantIDs_Empty covers the empty state.
func TestConnectedTenantIDs_Empty(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	ids := m.ConnectedTenantIDs()
	assert.Empty(t, ids)
}

// TestClose_Empty covers Close on fresh manager.
func TestPGClose_Empty(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	err := m.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, m.closed)
}

// TestCloseConnection_NonExistentTenant covers CloseConnection for missing tenant.
func TestPGCloseConnection_NonExistent(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	err := m.CloseConnection(context.Background(), "non-existent")
	require.NoError(t, err)
}

// TestIsMultiTenant_WithClient covers the method.
func TestPGIsMultiTenant_WithClient(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")
	assert.True(t, m.IsMultiTenant())
}

func TestPGIsMultiTenant_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")
	assert.False(t, m.IsMultiTenant())
}

// TestGetDefaultConnection covers the method.
func TestGetDefaultConnection_Nil(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")
	assert.Nil(t, m.GetDefaultConnection())
}

func TestGetDefaultConnection_Set(t *testing.T) {
	t.Parallel()

	conn := &PostgresConnection{}
	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithDefaultConnection(conn))
	assert.Same(t, conn, m.GetDefaultConnection())
}

// TestWithConnectionLimits_Option covers the functional option.
func TestWithConnectionLimits_FuncOption(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithConnectionLimits(30, 8))
	assert.Equal(t, 30, m.maxOpenConns)
	assert.Equal(t, 8, m.maxIdleConns)
}

// TestWithConnectionLimits_Method covers the deprecated method form.
func TestWithConnectionLimits_Method(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")
	result := m.WithConnectionLimits(25, 6)
	assert.Same(t, m, result)
	assert.Equal(t, 25, m.maxOpenConns)
}

// TestCreateDirectConnection_NilConfig covers the nil config guard.
func TestCreateDirectConnection_NilConfig(t *testing.T) {
	t.Parallel()

	_, err := CreateDirectConnection(context.Background(), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNilConfig)
}

// TestClampPoolSettings covers the clamp function.
func TestClampPoolSettings_BelowMax(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithConnectionLimitCaps(50, 10))

	open, idle := m.clampPoolSettings(20, 5, "tenant-1", nil)
	assert.Equal(t, 20, open)
	assert.Equal(t, 5, idle)
}

func TestClampPoolSettings_AboveMax(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithConnectionLimitCaps(50, 10))

	open, idle := m.clampPoolSettings(100, 20, "tenant-1", nil)
	assert.Equal(t, 50, open)
	assert.Equal(t, 10, idle)
}

// TestResolveConnectionSettingsFromConfig covers the config resolution.
func TestResolveConnectionSettingsFromConfig_NilConfig(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	result := m.resolveConnectionSettingsFromConfig(nil)
	assert.Nil(t, result)
}

func TestResolveConnectionSettingsFromConfig_WithModuleSettings(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithModule("payments"))

	cfg := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"payments": {
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 30,
					MaxIdleConns: 8,
				},
			},
		},
	}

	result := m.resolveConnectionSettingsFromConfig(cfg)
	require.NotNil(t, result)
	assert.Equal(t, 30, result.MaxOpenConns)
}

// TestResolveConnectionPoolSettings covers the pool settings resolver.
func TestResolveConnectionPoolSettings_Defaults(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	// nil config should use defaults
	open, idle := m.resolveConnectionPoolSettings(nil, "tenant-1", nil)
	assert.Equal(t, m.maxOpenConns, open)
	assert.Equal(t, m.maxIdleConns, idle)
}

// TestStats_Empty covers Stats on fresh manager.
func TestPGStats_Empty(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	stats := m.Stats()
	assert.Equal(t, 0, stats.TotalConnections)
	assert.False(t, stats.Closed)
}

// TestWithIdleTimeout covers the option.
func TestWithIdleTimeout(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithIdleTimeout(10*time.Minute))
	assert.Equal(t, 10*time.Minute, m.idleTimeout)
}

// TestWithMaxTenantPools covers the option.
func TestPGWithMaxTenantPools(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithMaxTenantPools(5))
	assert.Equal(t, 5, m.maxConnections)
}

// TestWithConnectionsCheckInterval covers the option.
func TestPGWithConnectionsCheckInterval(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithConnectionsCheckInterval(5*time.Minute))
	assert.Equal(t, 5*time.Minute, m.connectionsCheckInterval)
}

// TestWithTestConnections covers the test helper option.
func TestWithTestConnections(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithTestConnections("tenant-1", "tenant-2"))
	assert.Len(t, m.connections, 2)
	assert.NotNil(t, m.connections["tenant-1"])
	assert.NotNil(t, m.connections["tenant-2"])
}

// TestCanStorePostgresConnection_ClosedManager covers the closed manager path.
func TestCanStorePostgresConnection_ClosedManager(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")
	_ = m.Close(context.Background())

	// Should return false when manager is closed
	result := m.canStorePostgresConnection("tenant-1", &PostgresConnection{})
	assert.False(t, result)
}

// TestCanStorePostgresConnection_TenantNotExists covers the non-existent tenant path.
func TestCanStorePostgresConnection_TenantNotExists(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	// Tenant not in connections map - should return false
	result := m.canStorePostgresConnection("non-existent-tenant", &PostgresConnection{})
	assert.False(t, result)
}

// TestCanStorePostgresConnection_TenantExists covers the exists path.
func TestCanStorePostgresConnection_TenantExists(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger", WithTestConnections("tenant-existing"))

	result := m.canStorePostgresConnection("tenant-existing", &PostgresConnection{})
	assert.True(t, result)
}

// TestClosePostgresConn_NilConn covers the nil connection guard.
func TestClosePostgresConn_NilConn(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")

	// Nil conn should not panic
	assert.NotPanics(t, func() {
		m.closePostgresConn(nil, "error for tenant %s", "tenant-1")
	})
}

// TestClosePostgresConn_NilConnectionDB covers the nil ConnectionDB guard.
func TestClosePostgresConn_NilConnectionDB(t *testing.T) {
	t.Parallel()

	c := mustNewPGTestClient(t)
	m := NewManager(c, "ledger")
	conn := &PostgresConnection{}

	// Nil ConnectionDB should not panic
	assert.NotPanics(t, func() {
		m.closePostgresConn(conn, "error for tenant %s", "tenant-1")
	})
}
