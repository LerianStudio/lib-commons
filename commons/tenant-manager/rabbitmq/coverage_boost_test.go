//go:build unit

package rabbitmq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// createConnection — nil client guard
// -------------------------------------------------------------------

func TestCreateConnection_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")

	_, err := m.createConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant manager client is required")
}

// -------------------------------------------------------------------
// createConnection — manager closed guard (via GetConnection)
// -------------------------------------------------------------------

func TestGetConnection_ClosedManager_ReturnsError(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	_, err := m.GetConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

// -------------------------------------------------------------------
// createConnection — happy path with HTTP mock (gets config but dial fails)
// -------------------------------------------------------------------

func TestCreateConnection_GetsConfigButDialFails(t *testing.T) {
	t.Parallel()

	// Server returns a minimal valid RabbitMQ config
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-1",
			"tenantSlug": "test-tenant",
			"messaging": {
				"rabbitMQ": {
					"host": "unreachable-rabbit-host-99999",
					"port": 5672,
					"username": "user",
					"password": "pass",
					"vhost": "/"
				}
			}
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	_, err = m.createConnection(context.Background(), "tenant-1")
	require.Error(t, err, "should fail because unreachable-rabbit-host-99999 is not reachable")
}

// -------------------------------------------------------------------
// canStoreRabbitMQConnection
// -------------------------------------------------------------------

func TestCanStoreRabbitMQConnection_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	// Should return false and discard the nil connection (safe since conn is nil)
	result := m.canStoreRabbitMQConnection("tenant-1", nil)
	assert.False(t, result)
}

func TestCanStoreRabbitMQConnection_TenantEvicted(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	// Tenant is NOT in the connections map
	result := m.canStoreRabbitMQConnection("tenant-not-in-map", nil)
	assert.False(t, result)
}

func TestCanStoreRabbitMQConnection_TenantExists(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	// Pre-populate with a nil connection so tenant exists in map
	m.mu.Lock()
	m.connections["tenant-exists"] = nil
	m.mu.Unlock()

	result := m.canStoreRabbitMQConnection("tenant-exists", nil)
	assert.True(t, result)
}

// -------------------------------------------------------------------
// swapRabbitMQConnection
// -------------------------------------------------------------------

func TestSwapRabbitMQConnection_UpdatesCache(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	// Pre-populate
	m.connections["tenant-swap"] = nil
	m.lastAccessed["tenant-swap"] = time.Now().Add(-1 * time.Hour)

	freshKey := "amqp://user:pass@newhost:5672//"
	m.swapRabbitMQConnection("tenant-swap", nil, freshKey)

	m.mu.RLock()
	cachedURI := m.cachedURIs["tenant-swap"]
	m.mu.RUnlock()

	assert.Equal(t, freshKey, cachedURI)
}

// -------------------------------------------------------------------
// closeRabbitMQConn
// -------------------------------------------------------------------

func TestCloseRabbitMQConn_NilConn(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	assert.NotPanics(t, func() {
		m.closeRabbitMQConn(nil, "msg for %s", "tenant-1")
	})
}

// -------------------------------------------------------------------
// GetChannel
// -------------------------------------------------------------------

func TestGetChannel_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	_, err := m.GetChannel(context.Background(), "tenant-1")
	require.Error(t, err)
}

func TestGetChannel_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")

	_, err := m.GetChannel(context.Background(), "tenant-1")
	require.Error(t, err)
}

// -------------------------------------------------------------------
// ApplyConnectionSettings — no-op but now covered
// -------------------------------------------------------------------

func TestApplyConnectionSettings_IsNoOp(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger")

	// Should not panic and should be a no-op
	assert.NotPanics(t, func() {
		m.ApplyConnectionSettings("tenant-1", &core.TenantConfig{})
		m.ApplyConnectionSettings("tenant-2", nil)
	})
}

// -------------------------------------------------------------------
// revalidatePoolSettings — nil client panic guard
// -------------------------------------------------------------------

func TestRevalidatePoolSettings_PanicGuard_NilClient(t *testing.T) {
	t.Parallel()

	capLogger := testutil.NewCapturingLogger()
	m := &Manager{
		logger:                   logcompat.New(capLogger),
		connections:              make(map[string]*amqp.Connection),
		cachedURIs:               make(map[string]string),
		lastAccessed:             make(map[string]time.Time),
		lastConnectionsCheck:     make(map[string]time.Time),
		connectionsCheckInterval: 1 * time.Millisecond,
	}

	// nil client → panic inside revalidatePoolSettings; recover() should catch it
	assert.NotPanics(t, func() {
		m.revalidatePoolSettings("tenant-panic")
	})
}

// -------------------------------------------------------------------
// revalidatePoolSettings — suspended tenant eviction
// -------------------------------------------------------------------

func TestRevalidatePoolSettings_EvictsSuspendedTenant(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service suspended","status":"suspended"}`))
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient, err := client.NewClient(server.URL, capLogger,
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	m := NewManager(tmClient, "ledger",
		WithLogger(capLogger),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate a cached connection
	m.connections["tenant-susp"] = nil
	m.lastAccessed["tenant-susp"] = time.Now()
	m.lastConnectionsCheck["tenant-susp"] = time.Now()

	m.revalidatePoolSettings("tenant-susp")

	// Verify eviction
	m.mu.RLock()
	_, exists := m.connections["tenant-susp"]
	m.mu.RUnlock()
	assert.False(t, exists, "suspended tenant should be evicted")

	assert.True(t, capLogger.ContainsSubstring("service suspended"))
}

// -------------------------------------------------------------------
// revalidatePoolSettings — transient failure keeps connection
// -------------------------------------------------------------------

func TestRevalidatePoolSettings_TransientFailureKeepsConnection(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient, err := client.NewClient(server.URL, capLogger,
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	m := NewManager(tmClient, "ledger",
		WithLogger(capLogger),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	m.connections["tenant-fail"] = nil
	m.lastAccessed["tenant-fail"] = time.Now()

	m.revalidatePoolSettings("tenant-fail")

	// Connection should still exist
	m.mu.RLock()
	_, exists := m.connections["tenant-fail"]
	m.mu.RUnlock()
	assert.True(t, exists, "connection should NOT be evicted on transient failure")

	assert.True(t, capLogger.ContainsSubstring("failed to revalidate"))
}

// -------------------------------------------------------------------
// detectAndReconnectRabbitMQ — nil rabbitConfig guard
// -------------------------------------------------------------------

func TestDetectAndReconnectRabbitMQ_NilRabbitConfig(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	// TenantConfig with no messaging config
	cfg := &core.TenantConfig{}

	// Should not panic
	assert.NotPanics(t, func() {
		m.detectAndReconnectRabbitMQ("tenant-1", cfg)
	})
}

func TestDetectAndReconnectRabbitMQ_NoCachedURI(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	cfg := &core.TenantConfig{
		Messaging: &core.MessagingConfig{
			RabbitMQ: &core.RabbitMQConfig{
				Host:     "localhost",
				Port:     5672,
				Username: "user",
				Password: "pass",
				VHost:    "/",
			},
		},
	}

	// No cached URI for this tenant → should return early without panic
	assert.NotPanics(t, func() {
		m.detectAndReconnectRabbitMQ("tenant-nocache", cfg)
	})
}

func TestDetectAndReconnectRabbitMQ_ConfigUnchanged(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	rabbitCfg := &core.RabbitMQConfig{
		Host:     "rabbit-host",
		Port:     5672,
		Username: "user",
		Password: "pass",
		VHost:    "/",
	}

	// Build the URI that matches the cached entry
	expectedURI := buildRabbitMQURI(rabbitCfg, false)
	m.cachedURIs["tenant-same"] = expectedURI
	m.connections["tenant-same"] = nil

	cfg := &core.TenantConfig{
		Messaging: &core.MessagingConfig{RabbitMQ: rabbitCfg},
	}

	// Config unchanged → should return early, no reconnection attempt
	assert.NotPanics(t, func() {
		m.detectAndReconnectRabbitMQ("tenant-same", cfg)
	})

	// Connection should still exist
	m.mu.RLock()
	_, exists := m.connections["tenant-same"]
	m.mu.RUnlock()
	assert.True(t, exists)
}
