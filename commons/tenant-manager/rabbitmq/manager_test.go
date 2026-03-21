package rabbitmq

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNewTestClient(t *testing.T) *client.Client {
	t.Helper()

	c, err := client.NewClient("http://localhost:8080", testutil.NewMockLogger(), client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	return c
}

func TestNewManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		c := mustNewTestClient(t)
		manager := NewManager(c, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
		assert.NotNil(t, manager.lastAccessed)
	})
}

func TestManager_EvictLRU(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		maxConnections    int
		idleTimeout       time.Duration
		preloadCount      int
		oldTenantAge      time.Duration
		newTenantAge      time.Duration
		expectEviction    bool
		expectedPoolSize  int
		expectedEvictedID string
	}{
		{
			name:              "evicts oldest idle connection when pool is at soft limit",
			maxConnections:    2,
			idleTimeout:       5 * time.Minute,
			preloadCount:      2,
			oldTenantAge:      10 * time.Minute,
			newTenantAge:      1 * time.Minute,
			expectEviction:    true,
			expectedPoolSize:  1,
			expectedEvictedID: "tenant-old",
		},
		{
			name:             "does not evict when pool is below soft limit",
			maxConnections:   3,
			idleTimeout:      5 * time.Minute,
			preloadCount:     2,
			oldTenantAge:     10 * time.Minute,
			newTenantAge:     1 * time.Minute,
			expectEviction:   false,
			expectedPoolSize: 2,
		},
		{
			name:             "does not evict when maxConnections is zero (unlimited)",
			maxConnections:   0,
			preloadCount:     5,
			oldTenantAge:     10 * time.Minute,
			newTenantAge:     1 * time.Minute,
			expectEviction:   false,
			expectedPoolSize: 5,
		},
		{
			name:             "does not evict when all connections are active (within idle timeout)",
			maxConnections:   2,
			idleTimeout:      5 * time.Minute,
			preloadCount:     2,
			oldTenantAge:     2 * time.Minute,
			newTenantAge:     1 * time.Minute,
			expectEviction:   false,
			expectedPoolSize: 2,
		},
		{
			name:              "respects custom idle timeout",
			maxConnections:    2,
			idleTimeout:       30 * time.Second,
			preloadCount:      2,
			oldTenantAge:      1 * time.Minute,
			newTenantAge:      10 * time.Second,
			expectEviction:    true,
			expectedPoolSize:  1,
			expectedEvictedID: "tenant-old",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := []Option{
				WithLogger(testutil.NewMockLogger()),
				WithMaxTenantPools(tt.maxConnections),
			}
			if tt.idleTimeout > 0 {
				opts = append(opts, WithIdleTimeout(tt.idleTimeout))
			}

			c := mustNewTestClient(t)
			manager := NewManager(c, "ledger", opts...)

			// Pre-populate pool with nil connections (cannot create real amqp.Connection in unit test)
			// evictLRU checks conn != nil && !conn.IsClosed() before closing,
			// so nil connections are safe for testing the eviction logic.
			if tt.preloadCount >= 1 {
				manager.connections["tenant-old"] = nil
				manager.lastAccessed["tenant-old"] = time.Now().Add(-tt.oldTenantAge)
			}

			if tt.preloadCount >= 2 {
				manager.connections["tenant-new"] = nil
				manager.lastAccessed["tenant-new"] = time.Now().Add(-tt.newTenantAge)
			}

			// For unlimited test, add more connections
			for i := 2; i < tt.preloadCount; i++ {
				id := "tenant-extra-" + time.Now().Add(time.Duration(i)*time.Second).Format("150405")
				manager.connections[id] = nil
				manager.lastAccessed[id] = time.Now().Add(-time.Duration(i) * time.Minute)
			}

			// Call evictLRU (caller must hold write lock)
			manager.mu.Lock()
			manager.evictLRU(testutil.NewMockLogger())
			manager.mu.Unlock()

			// Verify pool size
			assert.Equal(t, tt.expectedPoolSize, len(manager.connections),
				"pool size mismatch after eviction")

			if tt.expectEviction {
				// Verify the oldest tenant was evicted
				_, exists := manager.connections[tt.expectedEvictedID]
				assert.False(t, exists,
					"expected tenant %s to be evicted from pool", tt.expectedEvictedID)

				// Verify lastAccessed was also cleaned up
				_, accessExists := manager.lastAccessed[tt.expectedEvictedID]
				assert.False(t, accessExists,
					"expected lastAccessed entry for %s to be removed", tt.expectedEvictedID)
			}
		})
	}
}

func TestManager_PoolGrowsBeyondSoftLimit_WhenAllActive(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxTenantPools(2),
		WithIdleTimeout(5*time.Minute),
	)

	// Pre-populate with 2 nil connections, both accessed recently (within idle timeout)
	for _, id := range []string{"tenant-1", "tenant-2"} {
		manager.connections[id] = nil
		manager.lastAccessed[id] = time.Now().Add(-1 * time.Minute)
	}

	// Try to evict - should not evict because all connections are active
	manager.mu.Lock()
	manager.evictLRU(testutil.NewMockLogger())
	manager.mu.Unlock()

	// Pool should remain at 2 (no eviction occurred)
	assert.Equal(t, 2, len(manager.connections),
		"pool should not shrink when all connections are active")

	// Simulate adding a third connection (pool grows beyond soft limit)
	manager.connections["tenant-3"] = nil
	manager.lastAccessed["tenant-3"] = time.Now()

	assert.Equal(t, 3, len(manager.connections),
		"pool should grow beyond soft limit when all connections are active")
}

func TestManager_WithIdleTimeout_Option(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		idleTimeout     time.Duration
		expectedTimeout time.Duration
	}{
		{
			name:            "sets custom idle timeout",
			idleTimeout:     10 * time.Minute,
			expectedTimeout: 10 * time.Minute,
		},
		{
			name:            "sets short idle timeout",
			idleTimeout:     30 * time.Second,
			expectedTimeout: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t)
			manager := NewManager(c, "ledger",
				WithIdleTimeout(tt.idleTimeout),
			)

			assert.Equal(t, tt.expectedTimeout, manager.idleTimeout)
		})
	}
}

func TestManager_CloseConnection_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache with a nil connection (avoids needing real AMQP)
	manager.connections["tenant-123"] = nil
	manager.lastAccessed["tenant-123"] = time.Now()

	// Close the specific tenant connection
	err := manager.CloseConnection(context.Background(), "tenant-123")

	require.NoError(t, err)

	manager.mu.RLock()
	_, connExists := manager.connections["tenant-123"]
	_, accessExists := manager.lastAccessed["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, connExists, "connection should be removed after CloseConnection")
	assert.False(t, accessExists, "lastAccessed should be removed after CloseConnection")
}

func TestManager_WithMaxTenantPools_Option(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		maxConnections int
		expectedMax    int
	}{
		{
			name:           "sets max connections via option",
			maxConnections: 10,
			expectedMax:    10,
		},
		{
			name:           "zero means unlimited",
			maxConnections: 0,
			expectedMax:    0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t)
			manager := NewManager(c, "ledger",
				WithMaxTenantPools(tt.maxConnections),
			)

			assert.Equal(t, tt.expectedMax, manager.maxConnections)
		})
	}
}

func TestManager_Stats_IncludesMaxConnections(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger",
		WithMaxTenantPools(50),
	)

	stats := manager.Stats()

	assert.Equal(t, 50, stats.MaxConnections)
	assert.Equal(t, 0, stats.TotalConnections)
}

func TestManager_Close_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache with nil connections
	manager.connections["tenant-1"] = nil
	manager.lastAccessed["tenant-1"] = time.Now()
	manager.connections["tenant-2"] = nil
	manager.lastAccessed["tenant-2"] = time.Now()

	err := manager.Close(context.Background())

	require.NoError(t, err)
	assert.True(t, manager.closed)
	assert.Empty(t, manager.connections, "all connections should be removed after Close")
	assert.Empty(t, manager.lastAccessed, "all lastAccessed entries should be removed after Close")
}

func TestBuildRabbitMQURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *core.RabbitMQConfig
		useTLS   bool
		expected string
	}{
		{
			name: "builds URI with all fields",
			cfg: &core.RabbitMQConfig{
				Host:     "localhost",
				Port:     5672,
				Username: "guest",
				Password: "guest",
				VHost:    "tenant-abc",
			},
			useTLS:   false,
			expected: "amqp://guest:guest@localhost:5672/tenant-abc",
		},
		{
			name: "builds URI with custom port",
			cfg: &core.RabbitMQConfig{
				Host:     "rabbitmq.internal",
				Port:     5673,
				Username: "admin",
				Password: "secret",
				VHost:    "/",
			},
			useTLS:   false,
			expected: "amqp://admin:secret@rabbitmq.internal:5673/%2F",
		},
		{
			name: "builds TLS URI with amqps scheme",
			cfg: &core.RabbitMQConfig{
				Host:     "rabbitmq.prod.internal",
				Port:     5671,
				Username: "admin",
				Password: "secret",
				VHost:    "tenant-xyz",
			},
			useTLS:   true,
			expected: "amqps://admin:secret@rabbitmq.prod.internal:5671/tenant-xyz",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uri := buildRabbitMQURI(tt.cfg, tt.useTLS)
			assert.Equal(t, tt.expected, uri)
		})
	}
}

func TestManager_ResolveTLS(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name      string
		globalTLS bool
		tenantTLS *bool
		expected  bool
	}{
		{
			name:      "uses global TLS when tenant TLS is nil",
			globalTLS: true,
			tenantTLS: nil,
			expected:  true,
		},
		{
			name:      "uses global false when tenant TLS is nil",
			globalTLS: false,
			tenantTLS: nil,
			expected:  false,
		},
		{
			name:      "per-tenant true overrides global false",
			globalTLS: false,
			tenantTLS: boolPtr(true),
			expected:  true,
		},
		{
			name:      "per-tenant false overrides global true",
			globalTLS: true,
			tenantTLS: boolPtr(false),
			expected:  false,
		},
		{
			name:      "per-tenant true with global true",
			globalTLS: true,
			tenantTLS: boolPtr(true),
			expected:  true,
		},
		{
			name:      "per-tenant false with global false",
			globalTLS: false,
			tenantTLS: boolPtr(false),
			expected:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t)

			var opts []Option
			if tt.globalTLS {
				opts = append(opts, WithTLS())
			}

			manager := NewManager(c, "ledger", opts...)

			cfg := &core.RabbitMQConfig{
				Host:     "localhost",
				Port:     5672,
				Username: "guest",
				Password: "guest",
				VHost:    "test",
				TLS:      tt.tenantTLS,
			}

			result := manager.resolveTLS(cfg)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestManager_DialRabbitMQ_InvalidCAFile(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger")

	// Attempt to dial with a non-existent CA file
	_, err := manager.dialRabbitMQ("amqps://guest:guest@localhost:5671/test", true, "/nonexistent/ca.pem")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read TLS CA file")
}

func TestManager_DialRabbitMQ_InvalidCACert(t *testing.T) {
	t.Parallel()

	// Create a temp file with invalid PEM content
	tmpFile, err := os.CreateTemp("", "invalid-ca-*.pem")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString("this is not a valid PEM certificate")
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger")

	_, err = manager.dialRabbitMQ("amqps://guest:guest@localhost:5671/test", true, tmpFile.Name())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse CA certificate")
}

func TestManager_ApplyConnectionSettings_IsNoOp(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t)
	manager := NewManager(c, "ledger")

	// Should not panic or error - it's a no-op
	manager.ApplyConnectionSettings("tenant-123", &core.TenantConfig{
		ID: "tenant-123",
	})
}
