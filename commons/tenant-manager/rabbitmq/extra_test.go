//go:build unit

package rabbitmq

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithModule covers the WithModule option (was 0%).
func TestWithModule(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithModule("payments"))
	assert.Equal(t, "payments", m.module)
}

// TestWithLogger covers the WithLogger option.
func TestWithLogger(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	logger := testutil.NewMockLogger()
	m := NewManager(c, "ledger", WithLogger(logger))
	assert.NotNil(t, m.logger)
}

// TestWithMaxTenantPools covers the WithMaxTenantPools option.
func TestWithMaxTenantPools(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithMaxTenantPools(10))
	assert.Equal(t, 10, m.maxConnections)
}

// TestWithIdleTimeout covers the WithIdleTimeout option.
func TestWithIdleTimeout(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithIdleTimeout(15*time.Minute))
	assert.Equal(t, 15*time.Minute, m.idleTimeout)
}

// TestWithConnectionsCheckInterval covers the WithConnectionsCheckInterval option.
func TestWithConnectionsCheckInterval(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithConnectionsCheckInterval(2*time.Minute))
	assert.Equal(t, 2*time.Minute, m.connectionsCheckInterval)
}

func TestWithConnectionsCheckInterval_Disabled(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithConnectionsCheckInterval(-1))
	assert.Equal(t, time.Duration(0), m.connectionsCheckInterval)
}

// TestWithTLS covers the WithTLS option.
func TestWithTLS(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithTLS())
	assert.True(t, m.useTLS)
}

// TestApplyConnectionSettings covers the no-op ApplyConnectionSettings.
func TestApplyConnectionSettings_NoOp(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	// Should not panic
	assert.NotPanics(t, func() {
		m.ApplyConnectionSettings("tenant-1", &core.TenantConfig{})
	})
}

// TestIsMultiTenant covers the IsMultiTenant method.
func TestIsMultiTenant_WithClient(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")
	assert.True(t, m.IsMultiTenant())
}

func TestIsMultiTenant_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")
	assert.False(t, m.IsMultiTenant())
}

// TestGetConnection_EmptyTenantID covers the empty tenant ID guard.
func TestGetConnection_EmptyTenantID(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	_, err := m.GetConnection(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

// TestGetConnection_ManagerClosed covers the closed manager guard.
func TestGetConnection_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")
	_ = m.Close(context.Background())

	_, err := m.GetConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

// TestGetChannel_EmptyTenantID covers the GetChannel method.
func TestGetChannel_EmptyTenantID(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	_, err := m.GetChannel(context.Background(), "")
	require.Error(t, err)
}

// TestClose_Empty covers Close on empty manager.
func TestClose_Empty(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	err := m.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, m.closed)
}

// TestCloseConnection_NonExistentTenant covers CloseConnection for missing tenant.
func TestCloseConnection_NonExistentTenant(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	err := m.CloseConnection(context.Background(), "non-existent")
	require.NoError(t, err)
}

// TestStats_EmptyManager covers the Stats method.
func TestStats_EmptyManager(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	stats := m.Stats()
	assert.Equal(t, 0, stats.TotalConnections)
	assert.Equal(t, 0, stats.ActiveConnections)
	assert.False(t, stats.Closed)
}

func TestStats_ClosedManager(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")
	_ = m.Close(context.Background())

	stats := m.Stats()
	assert.True(t, stats.Closed)
}

// TestBuildRabbitMQURI covers the URI builder.
func TestBuildRabbitMQURI_PlainText(t *testing.T) {
	t.Parallel()

	cfg := &core.RabbitMQConfig{
		Host:     "rabbitmq.example.com",
		Port:     5672,
		Username: "user",
		Password: "pass",
		VHost:    "/",
	}

	uri := buildRabbitMQURI(cfg, false)
	assert.Contains(t, uri, "amqp://")
	assert.Contains(t, uri, "rabbitmq.example.com")
	assert.Contains(t, uri, ":5672/")
}

func TestBuildRabbitMQURI_TLS(t *testing.T) {
	t.Parallel()

	cfg := &core.RabbitMQConfig{
		Host:     "rabbitmq.example.com",
		Port:     5671,
		Username: "user",
		Password: "pass",
		VHost:    "myvhost",
	}

	uri := buildRabbitMQURI(cfg, true)
	assert.Contains(t, uri, "amqps://")
}

func TestBuildRabbitMQURI_SpecialCharacters(t *testing.T) {
	t.Parallel()

	cfg := &core.RabbitMQConfig{
		Host:     "rabbitmq.example.com",
		Port:     5672,
		Username: "user@domain",
		Password: "p@ss:word",
		VHost:    "/my-vhost",
	}

	uri := buildRabbitMQURI(cfg, false)
	// Percent-encoded credentials should not contain bare '@' in userinfo part
	assert.NotContains(t, uri, "user@domain:p@ss:word@")
}

// TestResolveTLS covers the resolveTLS method.
func TestResolveTLS_NoPerTenantOverride(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithTLS())

	cfg := &core.RabbitMQConfig{}
	assert.True(t, m.resolveTLS(cfg))
}

func TestResolveTLS_PerTenantOverrideTrue(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger") // useTLS = false

	b := true
	cfg := &core.RabbitMQConfig{TLS: &b}
	assert.True(t, m.resolveTLS(cfg))
}

func TestResolveTLS_PerTenantOverrideFalse(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithTLS()) // useTLS = true

	b := false
	cfg := &core.RabbitMQConfig{TLS: &b}
	assert.False(t, m.resolveTLS(cfg))
}
