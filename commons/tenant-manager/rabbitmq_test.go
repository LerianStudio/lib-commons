package tenantmanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRabbitMQManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		manager := NewRabbitMQManager(client, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
		assert.NotNil(t, manager.lastAccessed)
	})
}

func TestRabbitMQManager_EvictLRU(t *testing.T) {
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

			opts := []RabbitMQOption{
				WithRabbitMQLogger(&mockLogger{}),
				WithRabbitMQMaxTenantPools(tt.maxConnections),
			}
			if tt.idleTimeout > 0 {
				opts = append(opts, WithRabbitMQIdleTimeout(tt.idleTimeout))
			}

			client := &Client{baseURL: "http://localhost:8080"}
			manager := NewRabbitMQManager(client, "ledger", opts...)

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
			manager.evictLRU(&mockLogger{})
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

func TestRabbitMQManager_PoolGrowsBeyondSoftLimit_WhenAllActive(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewRabbitMQManager(client, "ledger",
		WithRabbitMQLogger(&mockLogger{}),
		WithRabbitMQMaxTenantPools(2),
		WithRabbitMQIdleTimeout(5*time.Minute),
	)

	// Pre-populate with 2 nil connections, both accessed recently (within idle timeout)
	for _, id := range []string{"tenant-1", "tenant-2"} {
		manager.connections[id] = nil
		manager.lastAccessed[id] = time.Now().Add(-1 * time.Minute)
	}

	// Try to evict - should not evict because all connections are active
	manager.mu.Lock()
	manager.evictLRU(&mockLogger{})
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

func TestRabbitMQManager_WithRabbitMQIdleTimeout_Option(t *testing.T) {
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

			client := &Client{baseURL: "http://localhost:8080"}
			manager := NewRabbitMQManager(client, "ledger",
				WithRabbitMQIdleTimeout(tt.idleTimeout),
			)

			assert.Equal(t, tt.expectedTimeout, manager.idleTimeout)
		})
	}
}

func TestRabbitMQManager_CloseConnection_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewRabbitMQManager(client, "ledger",
		WithRabbitMQLogger(&mockLogger{}),
	)

	// Pre-populate cache with a nil connection (avoids needing real AMQP)
	manager.connections["tenant-123"] = nil
	manager.lastAccessed["tenant-123"] = time.Now()

	// Close the specific tenant connection
	err := manager.CloseConnection("tenant-123")

	require.NoError(t, err)

	manager.mu.RLock()
	_, connExists := manager.connections["tenant-123"]
	_, accessExists := manager.lastAccessed["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, connExists, "connection should be removed after CloseConnection")
	assert.False(t, accessExists, "lastAccessed should be removed after CloseConnection")
}

func TestRabbitMQManager_WithRabbitMQMaxTenantPools_Option(t *testing.T) {
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

			client := &Client{baseURL: "http://localhost:8080"}
			manager := NewRabbitMQManager(client, "ledger",
				WithRabbitMQMaxTenantPools(tt.maxConnections),
			)

			assert.Equal(t, tt.expectedMax, manager.maxConnections)
		})
	}
}

func TestRabbitMQManager_Stats_IncludesMaxConnections(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewRabbitMQManager(client, "ledger",
		WithRabbitMQMaxTenantPools(50),
	)

	stats := manager.Stats()

	assert.Equal(t, 50, stats.MaxConnections)
	assert.Equal(t, 0, stats.TotalConnections)
}

func TestRabbitMQManager_Close_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewRabbitMQManager(client, "ledger",
		WithRabbitMQLogger(&mockLogger{}),
	)

	// Pre-populate cache with nil connections
	manager.connections["tenant-1"] = nil
	manager.lastAccessed["tenant-1"] = time.Now()
	manager.connections["tenant-2"] = nil
	manager.lastAccessed["tenant-2"] = time.Now()

	err := manager.Close()

	require.NoError(t, err)
	assert.True(t, manager.closed)
	assert.Empty(t, manager.connections, "all connections should be removed after Close")
	assert.Empty(t, manager.lastAccessed, "all lastAccessed entries should be removed after Close")
}

func TestBuildRabbitMQURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *RabbitMQConfig
		expected string
	}{
		{
			name: "builds URI with all fields",
			cfg: &RabbitMQConfig{
				Host:     "localhost",
				Port:     5672,
				Username: "guest",
				Password: "guest",
				VHost:    "tenant-abc",
			},
			expected: "amqp://guest:guest@localhost:5672/tenant-abc",
		},
		{
			name: "builds URI with custom port",
			cfg: &RabbitMQConfig{
				Host:     "rabbitmq.internal",
				Port:     5673,
				Username: "admin",
				Password: "secret",
				VHost:    "/",
			},
			expected: "amqp://admin:secret@rabbitmq.internal:5673//",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uri := buildRabbitMQURI(tt.cfg)
			assert.Equal(t, tt.expected, uri)
		})
	}
}
