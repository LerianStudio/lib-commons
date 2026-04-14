package mongo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/cache.(*InMemoryCache).cleanupLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)
}

func startFakeMongoServer(t *testing.T) (*mongo.Client, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			go serveFakeMongoConn(conn)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	addr := ln.Addr().String()

	mongoClient, err := mongo.Connect(ctx, options.Client().
		ApplyURI(fmt.Sprintf("mongodb://%s/?directConnection=true", addr)).
		SetServerSelectionTimeout(2*time.Second))
	require.NoError(t, err)

	require.NoError(t, mongoClient.Ping(ctx, nil))

	cleanup := func() {
		_ = mongoClient.Disconnect(context.Background())
		ln.Close()
	}

	return mongoClient, cleanup
}

func serveFakeMongoConn(conn net.Conn) {
	defer conn.Close()

	for {
		header := make([]byte, 16)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}

		msgLen := int(binary.LittleEndian.Uint32(header[0:4]))
		reqID := binary.LittleEndian.Uint32(header[4:8])

		body := make([]byte, msgLen-16)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}

		resp := bson.D{
			{Key: "ismaster", Value: true},
			{Key: "ok", Value: 1.0},
			{Key: "maxWireVersion", Value: int32(21)},
			{Key: "minWireVersion", Value: int32(0)},
			{Key: "maxBsonObjectSize", Value: int32(16777216)},
			{Key: "maxMessageSizeBytes", Value: int32(48000000)},
			{Key: "maxWriteBatchSize", Value: int32(100000)},
			{Key: "localTime", Value: time.Now()},
			{Key: "connectionId", Value: int32(1)},
		}

		respBytes, _ := bson.Marshal(resp)

		var payload []byte
		payload = append(payload, 0, 0, 0, 0)
		payload = append(payload, 0)
		payload = append(payload, respBytes...)

		totalLen := uint32(16 + len(payload))
		respHeader := make([]byte, 16)
		binary.LittleEndian.PutUint32(respHeader[0:4], totalLen)
		binary.LittleEndian.PutUint32(respHeader[4:8], reqID+1)
		binary.LittleEndian.PutUint32(respHeader[8:12], reqID)
		binary.LittleEndian.PutUint32(respHeader[12:16], 2013)

		_, _ = conn.Write(respHeader)
		_, _ = conn.Write(payload)
	}
}

// mustNewTestClient creates a test client or fails the test immediately.
// Centralises the repeated client.NewClient + error-check boilerplate.
// Tests use httptest servers (http://), so WithAllowInsecureHTTP is applied.
func mustNewTestClient(t testing.TB, baseURL string) *client.Client {
	t.Helper()

	c, err := client.NewClient(baseURL, testutil.NewMockLogger(), client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	return c
}

func TestNewManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		c := &client.Client{}
		manager := NewManager(c, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
	})
}

func TestManager_GetConnection_NoTenantID(t *testing.T) {
	c := &client.Client{}
	manager := NewManager(c, "ledger")

	_, err := manager.GetConnection(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestManager_GetConnection_ManagerClosed(t *testing.T) {
	c := &client.Client{}
	manager := NewManager(c, "ledger")
	require.NoError(t, manager.Close(context.Background()))

	_, err := manager.GetConnection(context.Background(), "tenant-123")

	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

func TestManager_GetDatabaseForTenant_NoTenantID(t *testing.T) {
	c := &client.Client{}
	manager := NewManager(c, "ledger")

	_, err := manager.GetDatabaseForTenant(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestManager_GetConnection_NilDBCachedConnection(t *testing.T) {
	t.Run("returns nil client when cached connection has nil DB", func(t *testing.T) {
		manager := NewManager(nil, "ledger")

		// Pre-populate cache with a connection that has nil DB
		cachedConn := &MongoConnection{
			DB: nil,
		}
		manager.connections["tenant-123"] = cachedConn

		// Nil cached DB now triggers a reconnect path. With nil tenant-manager
		// client configured, this should return a deterministic error instead of panic.
		result, err := manager.GetConnection(context.Background(), "tenant-123")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tenant manager client is required")
		assert.Nil(t, result)
	})
}

func TestManager_CloseConnection_EvictsFromCache(t *testing.T) {
	t.Run("evicts connection from cache on close", func(t *testing.T) {
		c := &client.Client{}
		manager := NewManager(c, "ledger")

		// Pre-populate cache with a connection that has nil DB (to avoid disconnect errors)
		cachedConn := &MongoConnection{
			DB: nil,
		}
		manager.connections["tenant-123"] = cachedConn

		err := manager.CloseConnection(context.Background(), "tenant-123")

		assert.NoError(t, err)

		manager.mu.RLock()
		_, exists := manager.connections["tenant-123"]
		manager.mu.RUnlock()

		assert.False(t, exists, "connection should have been evicted from cache")
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

			c := &client.Client{}
			manager := NewManager(c, "ledger", opts...)

			// Pre-populate pool with connections (nil DB to avoid real MongoDB)
			if tt.preloadCount >= 1 {
				manager.connections["tenant-old"] = &MongoConnection{DB: nil}
				manager.lastAccessed["tenant-old"] = time.Now().Add(-tt.oldTenantAge)
			}

			if tt.preloadCount >= 2 {
				manager.connections["tenant-new"] = &MongoConnection{DB: nil}
				manager.lastAccessed["tenant-new"] = time.Now().Add(-tt.newTenantAge)
			}

			// For unlimited test, add more connections
			for i := 2; i < tt.preloadCount; i++ {
				id := "tenant-extra-" + time.Now().Add(time.Duration(i)*time.Second).Format("150405")
				manager.connections[id] = &MongoConnection{DB: nil}
				manager.lastAccessed[id] = time.Now().Add(-time.Duration(i) * time.Minute)
			}

			// Call evictLRU (caller must hold write lock)
			manager.mu.Lock()
			manager.evictLRU(context.Background(), testutil.NewMockLogger())
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

	c := &client.Client{}
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxTenantPools(2),
		WithIdleTimeout(5*time.Minute),
	)

	// Pre-populate with 2 connections, both accessed recently (within idle timeout)
	for _, id := range []string{"tenant-1", "tenant-2"} {
		manager.connections[id] = &MongoConnection{DB: nil}
		manager.lastAccessed[id] = time.Now().Add(-1 * time.Minute)
	}

	// Try to evict - should not evict because all connections are active
	manager.mu.Lock()
	manager.evictLRU(context.Background(), testutil.NewMockLogger())
	manager.mu.Unlock()

	// Pool should remain at 2 (no eviction occurred)
	assert.Equal(t, 2, len(manager.connections),
		"pool should not shrink when all connections are active")

	// Simulate adding a third connection (pool grows beyond soft limit)
	manager.connections["tenant-3"] = &MongoConnection{DB: nil}
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

			c := &client.Client{}
			manager := NewManager(c, "ledger",
				WithIdleTimeout(tt.idleTimeout),
			)

			assert.Equal(t, tt.expectedTimeout, manager.idleTimeout)
		})
	}
}

func TestManager_LRU_LastAccessedUpdatedOnCacheHit(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxTenantPools(5),
	)

	// Pre-populate cache with a connection that has nil DB.
	cachedConn := &MongoConnection{DB: nil}

	initialTime := time.Now().Add(-5 * time.Minute)
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = initialTime

	// Accessing the connection now follows the reconnect path for nil DB.
	result, err := manager.GetConnection(context.Background(), "tenant-123")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get tenant config")
	assert.Nil(t, result, "nil DB should return nil client")

	// Verify lastAccessed entry was evicted because reconnect path removes stale cache entry.
	manager.mu.RLock()
	updatedTime, exists := manager.lastAccessed["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, exists, "lastAccessed entry should be removed on reconnect path")
	assert.True(t, updatedTime.IsZero(),
		"lastAccessed should be zero value when entry is removed: initial=%v, updated=%v",
		initialTime, updatedTime)
}

func TestManager_CloseConnection_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache with a connection that has nil DB
	manager.connections["tenant-123"] = &MongoConnection{DB: nil}
	manager.lastAccessed["tenant-123"] = time.Now()

	// Close the specific tenant client
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

			c := &client.Client{}
			manager := NewManager(c, "ledger",
				WithMaxTenantPools(tt.maxConnections),
			)

			assert.Equal(t, tt.expectedMax, manager.maxConnections)
		})
	}
}

func TestManager_ApplyConnectionSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		module        string
		config        *core.TenantConfig
		hasCachedConn bool
	}{
		{
			name:   "no-op with top-level connection settings and cached connection",
			module: "onboarding",
			config: &core.TenantConfig{
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 30,
				},
			},
			hasCachedConn: true,
		},
		{
			name:   "no-op with module-level connection settings and cached connection",
			module: "onboarding",
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns: 50,
						},
					},
				},
			},
			hasCachedConn: true,
		},
		{
			name:          "no-op with connection settings but no cached connection",
			module:        "onboarding",
			config:        &core.TenantConfig{ConnectionSettings: &core.ConnectionSettings{MaxOpenConns: 30}},
			hasCachedConn: false,
		},
		{
			name:   "no-op with config that has no connection settings",
			module: "onboarding",
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						MongoDB: &core.MongoDBConfig{Host: "localhost"},
					},
				},
			},
			hasCachedConn: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := testutil.NewCapturingLogger()
			c := &client.Client{}
			manager := NewManager(c, "ledger",
				WithModule(tt.module),
				WithLogger(logger),
			)

			if tt.hasCachedConn {
				manager.connections["tenant-123"] = &MongoConnection{DB: nil}
			}

			// ApplyConnectionSettings is a no-op for MongoDB.
			// The MongoDB driver does not support runtime pool resize.
			// Verify it does not panic and produces no log output.
			manager.ApplyConnectionSettings("tenant-123", tt.config)

			assert.Empty(t, logger.GetMessages(),
				"ApplyConnectionSettings should be a no-op and produce no log output")
		})
	}
}

func TestBuildMongoURI(t *testing.T) {
	t.Run("rejects empty host when URI not provided", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Port:     27017,
			Database: "testdb",
		}

		_, err := buildMongoURI(cfg, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mongo host is required")
	})

	t.Run("rejects zero port when URI not provided", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Host:     "localhost",
			Database: "testdb",
		}

		_, err := buildMongoURI(cfg, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mongo port is required")
	})

	t.Run("rejects both empty host and zero port when URI not provided", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Database: "testdb",
		}

		_, err := buildMongoURI(cfg, nil)

		require.Error(t, err)
		// Host is checked first
		assert.Contains(t, err.Error(), "mongo host is required")
	})

	t.Run("allows empty host and port when URI is provided", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			URI: "mongodb://custom-uri",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.Equal(t, "mongodb://custom-uri", uri)
	})

	t.Run("returns URI when provided", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			URI: "mongodb://custom-uri",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.Equal(t, "mongodb://custom-uri", uri)
	})

	t.Run("rejects unsupported URI scheme", func(t *testing.T) {
		cfg := &core.MongoDBConfig{URI: "http://example.com"}

		_, err := buildMongoURI(cfg, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid mongo URI scheme")
	})

	t.Run("builds URI with credentials", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "testdb",
			Username: "user",
			Password: "pass",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.Equal(t, "mongodb://user:pass@localhost:27017/testdb?authSource=admin", uri)
	})

	t.Run("builds URI without credentials", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "testdb",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.Equal(t, "mongodb://localhost:27017/testdb", uri)
	})

	t.Run("defaults authSource to admin with database and credentials", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Host:     "mongo.example.com",
			Port:     27017,
			Database: "tenantdb",
			Username: "appuser",
			Password: "secret",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.Contains(t, uri, "authSource=admin")
	})

	t.Run("explicit authSource overrides default", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Host:       "mongo.example.com",
			Port:       27017,
			Database:   "tenantdb",
			Username:   "appuser",
			Password:   "secret",
			AuthSource: "customauth",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.Contains(t, uri, "authSource=customauth")
		assert.NotContains(t, uri, "authSource=admin")
	})

	t.Run("no authSource without credentials", func(t *testing.T) {
		cfg := &core.MongoDBConfig{
			Host:     "mongo.example.com",
			Port:     27017,
			Database: "tenantdb",
		}

		uri, err := buildMongoURI(cfg, nil)

		require.NoError(t, err)
		assert.NotContains(t, uri, "authSource")
	})

	t.Run("URL-encodes special characters in credentials", func(t *testing.T) {
		tests := []struct {
			name             string
			username         string
			password         string
			expectedUser     string
			expectedPassword string
		}{
			{
				name:             "at sign in password",
				username:         "admin",
				password:         "p@ss",
				expectedUser:     "admin",
				expectedPassword: "p%40ss",
			},
			{
				name:             "colon in password",
				username:         "admin",
				password:         "p:ss",
				expectedUser:     "admin",
				expectedPassword: "p%3Ass",
			},
			{
				name:             "slash in password",
				username:         "admin",
				password:         "p/ss",
				expectedUser:     "admin",
				expectedPassword: "p%2Fss",
			},
			{
				name:             "special characters in both username and password",
				username:         "user@domain",
				password:         "p@ss:w/rd",
				expectedUser:     "user%40domain",
				expectedPassword: "p%40ss%3Aw%2Frd",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := &core.MongoDBConfig{
					Host:     "localhost",
					Port:     27017,
					Database: "testdb",
					Username: tt.username,
					Password: tt.password,
				}

				uri, err := buildMongoURI(cfg, nil)
				require.NoError(t, err)

				expectedURI := fmt.Sprintf("mongodb://%s:%s@localhost:27017/testdb?authSource=admin",
					tt.expectedUser, tt.expectedPassword)
				assert.Equal(t, expectedURI, uri)
				assert.Contains(t, uri, tt.expectedUser)
				assert.Contains(t, uri, tt.expectedPassword)
			})
		}
	})
}

func TestManager_Stats(t *testing.T) {
	t.Parallel()

	t.Run("returns stats with no connections", func(t *testing.T) {
		c := &client.Client{}
		manager := NewManager(c, "ledger",
			WithMaxTenantPools(10),
		)

		stats := manager.Stats()

		assert.Equal(t, 0, stats.TotalConnections)
		assert.Equal(t, 10, stats.MaxConnections)
		assert.Equal(t, 0, stats.ActiveConnections)
		assert.Empty(t, stats.TenantIDs)
		assert.False(t, stats.Closed)
	})

	t.Run("returns stats with active and idle connections", func(t *testing.T) {
		c := &client.Client{}
		manager := NewManager(c, "ledger",
			WithMaxTenantPools(10),
			WithIdleTimeout(5*time.Minute),
		)

		// Add an active connection (accessed recently)
		manager.connections["tenant-active"] = &MongoConnection{DB: nil}
		manager.lastAccessed["tenant-active"] = time.Now().Add(-1 * time.Minute)

		// Add an idle connection (accessed long ago)
		manager.connections["tenant-idle"] = &MongoConnection{DB: nil}
		manager.lastAccessed["tenant-idle"] = time.Now().Add(-10 * time.Minute)

		stats := manager.Stats()

		assert.Equal(t, 2, stats.TotalConnections)
		assert.Equal(t, 10, stats.MaxConnections)
		assert.Equal(t, 1, stats.ActiveConnections)
		assert.Len(t, stats.TenantIDs, 2)
		assert.False(t, stats.Closed)
	})

	t.Run("returns closed status after close", func(t *testing.T) {
		c := &client.Client{}
		manager := NewManager(c, "ledger")

		require.NoError(t, manager.Close(context.Background()))

		stats := manager.Stats()

		assert.True(t, stats.Closed)
		assert.Equal(t, 0, stats.TotalConnections)
	})
}

func TestBuildMongoURI_TLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *core.MongoDBConfig
		contains []string
		excludes []string
	}{
		{
			name: "adds tls=true when TLS is enabled",
			cfg: &core.MongoDBConfig{
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				TLS:      true,
			},
			contains: []string{"tls=true"},
		},
		{
			name: "does not add tls param when TLS is disabled",
			cfg: &core.MongoDBConfig{
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				TLS:      false,
			},
			excludes: []string{"tls="},
		},
		{
			name: "adds tlsCAFile when TLS is enabled with CA file",
			cfg: &core.MongoDBConfig{
				Host:      "localhost",
				Port:      27017,
				Database:  "testdb",
				TLS:       true,
				TLSCAFile: "/etc/ssl/ca.pem",
			},
			contains: []string{"tls=true", "tlsCAFile=%2Fetc%2Fssl%2Fca.pem"},
		},
		{
			name: "adds tlsCertificateKeyFile when TLS is enabled with cert file",
			cfg: &core.MongoDBConfig{
				Host:        "localhost",
				Port:        27017,
				Database:    "testdb",
				TLS:         true,
				TLSCertFile: "/etc/ssl/client.pem",
			},
			contains: []string{"tls=true", "tlsCertificateKeyFile=%2Fetc%2Fssl%2Fclient.pem"},
		},
		{
			name: "uses key file as tlsCertificateKeyFile when cert file is empty",
			cfg: &core.MongoDBConfig{
				Host:       "localhost",
				Port:       27017,
				Database:   "testdb",
				TLS:        true,
				TLSKeyFile: "/etc/ssl/client-key.pem",
			},
			contains: []string{"tls=true", "tlsCertificateKeyFile=%2Fetc%2Fssl%2Fclient-key.pem"},
		},
		{
			name: "omits tlsCertificateKeyFile when cert and key are separate files",
			cfg: &core.MongoDBConfig{
				Host:        "localhost",
				Port:        27017,
				Database:    "testdb",
				TLS:         true,
				TLSCertFile: "/etc/ssl/client-cert.pem",
				TLSKeyFile:  "/etc/ssl/client-key.pem",
			},
			contains: []string{"tls=true"},
			excludes: []string{"tlsCertificateKeyFile", "tlsCAFile", "tlsInsecure"},
		},
		{
			name: "uses tlsCertificateKeyFile when cert and key point to the same combined PEM",
			cfg: &core.MongoDBConfig{
				Host:        "localhost",
				Port:        27017,
				Database:    "testdb",
				TLS:         true,
				TLSCertFile: "/etc/ssl/client-combined.pem",
				TLSKeyFile:  "/etc/ssl/client-combined.pem",
			},
			contains: []string{"tlsCertificateKeyFile=%2Fetc%2Fssl%2Fclient-combined.pem"},
		},
		{
			name: "adds tlsInsecure when TLS skip verify is enabled",
			cfg: &core.MongoDBConfig{
				Host:          "localhost",
				Port:          27017,
				Database:      "testdb",
				TLS:           true,
				TLSSkipVerify: true,
			},
			contains: []string{"tls=true", "tlsInsecure=true"},
		},
		{
			name: "does not add tlsInsecure when skip verify is false",
			cfg: &core.MongoDBConfig{
				Host:          "localhost",
				Port:          27017,
				Database:      "testdb",
				TLS:           true,
				TLSSkipVerify: false,
			},
			contains: []string{"tls=true"},
			excludes: []string{"tlsInsecure"},
		},
		{
			name: "does not add TLS params when TLS is disabled even with files set",
			cfg: &core.MongoDBConfig{
				Host:        "localhost",
				Port:        27017,
				Database:    "testdb",
				TLS:         false,
				TLSCAFile:   "/etc/ssl/ca.pem",
				TLSCertFile: "/etc/ssl/client.pem",
			},
			excludes: []string{"tls=", "tlsCAFile", "tlsCertificateKeyFile"},
		},
		{
			name: "full TLS config with all options",
			cfg: &core.MongoDBConfig{
				Host:          "mongo.prod.internal",
				Port:          27017,
				Database:      "tenantdb",
				Username:      "appuser",
				Password:      "secret",
				TLS:           true,
				TLSCAFile:     "/etc/ssl/ca.pem",
				TLSCertFile:   "/etc/ssl/client.pem",
				TLSSkipVerify: false,
			},
			contains: []string{
				"tls=true",
				"tlsCAFile=%2Fetc%2Fssl%2Fca.pem",
				"tlsCertificateKeyFile=%2Fetc%2Fssl%2Fclient.pem",
				"authSource=admin",
			},
			excludes: []string{"tlsInsecure"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uri, err := buildMongoURI(tt.cfg, nil)

			require.NoError(t, err)

			for _, s := range tt.contains {
				assert.Contains(t, uri, s, "URI should contain %q", s)
			}

			for _, s := range tt.excludes {
				assert.NotContains(t, uri, s, "URI should NOT contain %q", s)
			}
		})
	}
}

func TestManager_IsMultiTenant(t *testing.T) {
	t.Parallel()

	t.Run("returns true when client is configured", func(t *testing.T) {
		c := &client.Client{}
		manager := NewManager(c, "ledger")

		assert.True(t, manager.IsMultiTenant())
	})

	t.Run("returns false when client is nil", func(t *testing.T) {
		manager := NewManager(nil, "ledger")

		assert.False(t, manager.IsMultiTenant())
	})
}

func TestHasSeparateCertAndKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *core.MongoDBConfig
		expected bool
	}{
		{
			name:     "true when TLS enabled with distinct cert and key files",
			cfg:      &core.MongoDBConfig{TLS: true, TLSCertFile: "/cert.pem", TLSKeyFile: "/key.pem"},
			expected: true,
		},
		{
			name:     "false when TLS disabled",
			cfg:      &core.MongoDBConfig{TLS: false, TLSCertFile: "/cert.pem", TLSKeyFile: "/key.pem"},
			expected: false,
		},
		{
			name:     "false when cert and key are the same file (combined PEM)",
			cfg:      &core.MongoDBConfig{TLS: true, TLSCertFile: "/combined.pem", TLSKeyFile: "/combined.pem"},
			expected: false,
		},
		{
			name:     "false when only cert file is set",
			cfg:      &core.MongoDBConfig{TLS: true, TLSCertFile: "/cert.pem"},
			expected: false,
		},
		{
			name:     "false when only key file is set",
			cfg:      &core.MongoDBConfig{TLS: true, TLSKeyFile: "/key.pem"},
			expected: false,
		},
		{
			name:     "false when neither is set",
			cfg:      &core.MongoDBConfig{TLS: true},
			expected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, hasSeparateCertAndKey(tt.cfg))
		})
	}
}

// generateTestCertAndKey creates a self-signed certificate and private key in
// the given directory. Returns the paths to the cert and key files.
func generateTestCertAndKey(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(dir, "cert.pem")
	certFile, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, certFile.Close())

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	keyPath = filepath.Join(dir, "key.pem")
	keyFile, err := os.Create(keyPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, keyFile.Close())

	return certPath, keyPath
}

func TestBuildTLSConfigFromFiles(t *testing.T) {
	t.Parallel()

	t.Run("loads separate cert and key files successfully", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath, keyPath := generateTestCertAndKey(t, dir)

		cfg := &core.MongoDBConfig{
			TLS:         true,
			TLSCertFile: certPath,
			TLSKeyFile:  keyPath,
		}

		tlsCfg, err := buildTLSConfigFromFiles(cfg)

		require.NoError(t, err)
		require.NotNil(t, tlsCfg)
		assert.Len(t, tlsCfg.Certificates, 1, "should have loaded one certificate")
		assert.Nil(t, tlsCfg.RootCAs, "should not have RootCAs when no CA file is set")
		assert.False(t, tlsCfg.InsecureSkipVerify, "should not skip verify by default")
	})

	t.Run("loads CA file into RootCAs", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath, keyPath := generateTestCertAndKey(t, dir)

		// Write a self-signed CA cert (same cert, for test purposes)
		caPath := filepath.Join(dir, "ca.pem")
		certData, err := os.ReadFile(certPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(caPath, certData, 0o600))

		cfg := &core.MongoDBConfig{
			TLS:         true,
			TLSCertFile: certPath,
			TLSKeyFile:  keyPath,
			TLSCAFile:   caPath,
		}

		tlsCfg, err := buildTLSConfigFromFiles(cfg)

		require.NoError(t, err)
		require.NotNil(t, tlsCfg)
		assert.NotNil(t, tlsCfg.RootCAs, "should have RootCAs when CA file is set")
	})

	t.Run("sets InsecureSkipVerify when TLSSkipVerify is true", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath, keyPath := generateTestCertAndKey(t, dir)

		cfg := &core.MongoDBConfig{
			TLS:           true,
			TLSCertFile:   certPath,
			TLSKeyFile:    keyPath,
			TLSSkipVerify: true,
		}

		tlsCfg, err := buildTLSConfigFromFiles(cfg)

		require.NoError(t, err)
		assert.True(t, tlsCfg.InsecureSkipVerify, "should skip verify when TLSSkipVerify is true")
	})

	t.Run("returns error for invalid cert file", func(t *testing.T) {
		t.Parallel()

		cfg := &core.MongoDBConfig{
			TLS:         true,
			TLSCertFile: "/nonexistent/cert.pem",
			TLSKeyFile:  "/nonexistent/key.pem",
		}

		_, err := buildTLSConfigFromFiles(cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load TLS certificate key pair")
	})

	t.Run("returns error for invalid CA file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath, keyPath := generateTestCertAndKey(t, dir)

		cfg := &core.MongoDBConfig{
			TLS:         true,
			TLSCertFile: certPath,
			TLSKeyFile:  keyPath,
			TLSCAFile:   "/nonexistent/ca.pem",
		}

		_, err := buildTLSConfigFromFiles(cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read CA certificate file")
	})

	t.Run("returns error for unparseable CA PEM", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath, keyPath := generateTestCertAndKey(t, dir)

		badCAPath := filepath.Join(dir, "bad-ca.pem")
		require.NoError(t, os.WriteFile(badCAPath, []byte("not a PEM"), 0o600))

		cfg := &core.MongoDBConfig{
			TLS:         true,
			TLSCertFile: certPath,
			TLSKeyFile:  keyPath,
			TLSCAFile:   badCAPath,
		}

		_, err := buildTLSConfigFromFiles(cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse CA certificate")
	})
}

func TestManager_WithConnectionsCheckInterval_Option(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		interval         time.Duration
		expectedInterval time.Duration
	}{
		{
			name:             "sets custom settings check interval",
			interval:         1 * time.Minute,
			expectedInterval: 1 * time.Minute,
		},
		{
			name:             "sets short settings check interval",
			interval:         5 * time.Second,
			expectedInterval: 5 * time.Second,
		},
		{
			name:             "disables revalidation with zero duration",
			interval:         0,
			expectedInterval: 0,
		},
		{
			name:             "disables revalidation with negative duration",
			interval:         -1 * time.Second,
			expectedInterval: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t, "http://localhost:8080")
			manager := NewManager(c, "ledger",
				WithConnectionsCheckInterval(tt.interval),
			)

			assert.Equal(t, tt.expectedInterval, manager.connectionsCheckInterval)
		})
	}
}

func TestManager_DefaultConnectionsCheckInterval(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger")

	assert.Equal(t, defaultConnectionsCheckInterval, manager.connectionsCheckInterval,
		"default settings check interval should be set from named constant")
	assert.NotNil(t, manager.lastConnectionsCheck,
		"lastConnectionsCheck map should be initialized")
}

func TestManager_GetConnection_RevalidatesSettingsAfterInterval(t *testing.T) {
	t.Parallel()

	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-123",
			"tenantSlug": "test-tenant",
			"databases": {
				"onboarding": {
					"mongodb": {"host": "localhost", "port": 27017, "database": "testdb", "username": "user", "password": "pass"}
				}
			}
		}`))
	}))
	defer server.Close()

	fakeDB, cleanupFake := startFakeMongoServer(t)
	defer cleanupFake()

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	cachedConn := &MongoConnection{DB: fakeDB}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	manager.lastConnectionsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	db, err := manager.GetConnection(context.Background(), "tenant-123")
	require.NoError(t, err)
	assert.Equal(t, fakeDB, db)

	assert.Eventually(t, func() bool {
		return atomic.LoadInt32(&callCount) > 0
	}, 500*time.Millisecond, 10*time.Millisecond, "should have fetched fresh config from Tenant Manager")

	manager.mu.RLock()
	lastCheck := manager.lastConnectionsCheck["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, lastCheck.IsZero(), "lastConnectionsCheck should have been updated")

	manager.revalidateWG.Wait()
	require.NoError(t, manager.Close(context.Background()))
}

func TestManager_GetConnection_DisabledRevalidation_WithZero(t *testing.T) {
	t.Parallel()

	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-123",
			"tenantSlug": "test-tenant",
			"databases": {
				"onboarding": {
					"mongodb": {"host": "localhost", "port": 27017, "database": "testdb"}
				}
			}
		}`))
	}))
	defer server.Close()

	fakeDB, cleanupFake := startFakeMongoServer(t)
	defer cleanupFake()

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(0),
	)

	assert.Equal(t, time.Duration(0), manager.connectionsCheckInterval)

	cachedConn := &MongoConnection{DB: fakeDB}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	manager.lastConnectionsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	db, err := manager.GetConnection(context.Background(), "tenant-123")
	require.NoError(t, err)
	assert.Equal(t, fakeDB, db)

	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount), "should NOT have fetched config - revalidation is disabled")

	require.NoError(t, manager.Close(context.Background()))
}

func TestManager_GetConnection_DisabledRevalidation_WithNegative(t *testing.T) {
	t.Parallel()

	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	fakeDB, cleanupFake := startFakeMongoServer(t)
	defer cleanupFake()

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "payment",
		WithLogger(testutil.NewMockLogger()),
		WithModule("payment"),
		WithConnectionsCheckInterval(-5*time.Second),
	)

	assert.Equal(t, time.Duration(0), manager.connectionsCheckInterval)

	cachedConn := &MongoConnection{DB: fakeDB}
	manager.connections["tenant-456"] = cachedConn
	manager.lastAccessed["tenant-456"] = time.Now()
	manager.lastConnectionsCheck["tenant-456"] = time.Now().Add(-1 * time.Hour)

	db, err := manager.GetConnection(context.Background(), "tenant-456")
	require.NoError(t, err)
	assert.Equal(t, fakeDB, db)

	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount), "should NOT have fetched config - revalidation is disabled via negative interval")

	require.NoError(t, manager.Close(context.Background()))
}

func TestManager_RevalidateSettings_EvictsSuspendedTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		responseStatus     int
		responseBody       string
		expectEviction     bool
		expectLogSubstring string
	}{
		{
			name:               "evicts_cached_connection_when_tenant_is_suspended",
			responseStatus:     http.StatusForbidden,
			responseBody:       `{"code":"TS-SUSPENDED","error":"service suspended","status":"suspended"}`,
			expectEviction:     true,
			expectLogSubstring: "tenant tenant-suspended service suspended, evicting cached connection",
		},
		{
			name:               "evicts_cached_connection_when_tenant_is_purged",
			responseStatus:     http.StatusForbidden,
			responseBody:       `{"code":"TS-SUSPENDED","error":"service purged","status":"purged"}`,
			expectEviction:     true,
			expectLogSubstring: "tenant tenant-suspended service suspended, evicting cached connection",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.responseStatus)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			capLogger := testutil.NewCapturingLogger()
			tmClient, err := client.NewClient(server.URL, capLogger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
			require.NoError(t, err)
			manager := NewManager(tmClient, "ledger",
				WithLogger(capLogger),
				WithConnectionsCheckInterval(1*time.Millisecond),
			)

			// Pre-populate a cached connection for the tenant (nil DB to avoid real MongoDB)
			manager.connections["tenant-suspended"] = &MongoConnection{DB: nil}
			manager.lastAccessed["tenant-suspended"] = time.Now()
			manager.lastConnectionsCheck["tenant-suspended"] = time.Now()

			// Verify the connection exists before revalidation
			statsBefore := manager.Stats()
			assert.Equal(t, 1, statsBefore.TotalConnections,
				"should have 1 connection before revalidation")

			// Trigger revalidatePoolSettings directly
			manager.revalidatePoolSettings("tenant-suspended")

			if tt.expectEviction {
				// Verify the connection was evicted
				statsAfter := manager.Stats()
				assert.Equal(t, 0, statsAfter.TotalConnections,
					"connection should be evicted after suspended tenant detected")

				// Verify lastAccessed and lastConnectionsCheck were cleaned up
				manager.mu.RLock()
				_, accessExists := manager.lastAccessed["tenant-suspended"]
				_, connectionsCheckExists := manager.lastConnectionsCheck["tenant-suspended"]
				manager.mu.RUnlock()

				assert.False(t, accessExists,
					"lastAccessed should be removed for evicted tenant")
				assert.False(t, connectionsCheckExists,
					"lastConnectionsCheck should be removed for evicted tenant")
			}

			// Verify the appropriate log message was produced
			assert.True(t, capLogger.ContainsSubstring(tt.expectLogSubstring),
				"expected log message containing %q, got: %v",
				tt.expectLogSubstring, capLogger.GetMessages())
		})
	}
}

func TestManager_RevalidateSettings_BypassesClientCache(t *testing.T) {
	t.Parallel()

	// This test verifies that revalidatePoolSettings uses WithSkipCache()
	// to bypass the client's in-memory cache. Without it, a cached "active"
	// response would hide a subsequent 403 (suspended/purged) from tenant-manager.
	//
	// Setup: The httptest server returns 200 (active) on the first request
	// and 403 (suspended) on all subsequent requests. We first call
	// GetTenantConfig directly to populate the client cache, then trigger
	// revalidatePoolSettings. If WithSkipCache is working, the revalidation
	// hits the server (gets 403) and evicts the connection. If the cache
	// were used, it would return the stale 200 and the connection would
	// remain.
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")

		if count == 1 {
			// First request: return active config (populates client cache)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "tenant-cache-test",
				"tenantSlug": "cached-tenant",
				"service": "ledger",
				"status": "active",
				"databases": {
					"onboarding": {
						"mongodb": {"host": "localhost", "port": 27017, "database": "testdb", "username": "user", "password": "pass"}
					}
				}
			}`))

			return
		}

		// Subsequent requests: return 403 (tenant suspended)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service suspended","status":"suspended"}`))
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient, err := client.NewClient(server.URL, capLogger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	// Populate the client cache by calling GetTenantConfig directly
	cfg, err := tmClient.GetTenantConfig(context.Background(), "tenant-cache-test", "ledger")
	require.NoError(t, err)
	assert.Equal(t, "tenant-cache-test", cfg.ID)
	assert.Equal(t, int32(1), requestCount.Load(), "should have made exactly 1 HTTP request")

	// Create a manager with a cached connection for this tenant
	manager := NewManager(tmClient, "ledger",
		WithLogger(capLogger),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate a cached connection (nil DB to avoid real MongoDB)
	manager.connections["tenant-cache-test"] = &MongoConnection{DB: nil}
	manager.lastAccessed["tenant-cache-test"] = time.Now()
	manager.lastConnectionsCheck["tenant-cache-test"] = time.Now()

	// Trigger revalidatePoolSettings -- should bypass cache and hit the server
	manager.revalidatePoolSettings("tenant-cache-test")

	// Verify a second HTTP request was made (cache was bypassed)
	assert.Equal(t, int32(2), requestCount.Load(),
		"revalidatePoolSettings should bypass client cache and make a fresh HTTP request")

	// Verify the connection was evicted (server returned 403)
	statsAfter := manager.Stats()
	assert.Equal(t, 0, statsAfter.TotalConnections,
		"connection should be evicted after revalidation detected suspended tenant via cache bypass")

	// Verify lastAccessed and lastConnectionsCheck were cleaned up
	manager.mu.RLock()
	_, accessExists := manager.lastAccessed["tenant-cache-test"]
	_, connectionsCheckExists := manager.lastConnectionsCheck["tenant-cache-test"]
	manager.mu.RUnlock()

	assert.False(t, accessExists, "lastAccessed should be removed for evicted tenant")
	assert.False(t, connectionsCheckExists, "lastConnectionsCheck should be removed for evicted tenant")
}

func TestManager_RevalidateSettings_FailedDoesNotBreakConnection(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(capLogger),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate cache
	manager.connections["tenant-123"] = &MongoConnection{DB: nil}
	manager.lastAccessed["tenant-123"] = time.Now()
	manager.lastConnectionsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	// Trigger revalidation directly - should fail but not evict
	manager.revalidatePoolSettings("tenant-123")

	// Connection should still exist (not evicted on transient failure)
	stats := manager.Stats()
	assert.Equal(t, 1, stats.TotalConnections,
		"connection should NOT be evicted after transient revalidation failure")

	// Verify a warning was logged
	assert.True(t, capLogger.ContainsSubstring("failed to revalidate connection settings"),
		"should log a warning when revalidation fails")
}

func TestManager_RevalidateSettings_RecoverFromPanic(t *testing.T) {
	t.Parallel()

	capLogger := testutil.NewCapturingLogger()

	// Create a manager with nil client to trigger a panic path
	manager := &Manager{
		logger:                   logcompat.New(capLogger),
		connections:              make(map[string]*MongoConnection),
		databaseNames:            make(map[string]string),
		lastAccessed:             make(map[string]time.Time),
		lastConnectionsCheck:     make(map[string]time.Time),
		connectionsCheckInterval: 1 * time.Millisecond,
	}

	// Should not panic -- the recovery handler should catch it
	assert.NotPanics(t, func() {
		manager.revalidatePoolSettings("tenant-panic")
	})
}

func TestManager_CloseConnection_CleansUpLastConnectionsCheck(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache
	manager.connections["tenant-123"] = &MongoConnection{DB: nil}
	manager.lastAccessed["tenant-123"] = time.Now()
	manager.lastConnectionsCheck["tenant-123"] = time.Now()

	err := manager.CloseConnection(context.Background(), "tenant-123")

	require.NoError(t, err)

	manager.mu.RLock()
	_, connExists := manager.connections["tenant-123"]
	_, accessExists := manager.lastAccessed["tenant-123"]
	_, connectionsCheckExists := manager.lastConnectionsCheck["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, connExists, "connection should be removed after CloseConnection")
	assert.False(t, accessExists, "lastAccessed should be removed after CloseConnection")
	assert.False(t, connectionsCheckExists, "lastConnectionsCheck should be removed after CloseConnection")
}

func TestManager_Close_CleansUpLastConnectionsCheck(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache with multiple tenants
	for _, id := range []string{"tenant-1", "tenant-2"} {
		manager.connections[id] = &MongoConnection{DB: nil}
		manager.lastAccessed[id] = time.Now()
		manager.lastConnectionsCheck[id] = time.Now()
	}

	err := manager.Close(context.Background())

	require.NoError(t, err)

	assert.Empty(t, manager.connections, "all connections should be removed after Close")
	assert.Empty(t, manager.lastAccessed, "all lastAccessed should be removed after Close")
	assert.Empty(t, manager.lastConnectionsCheck, "all lastConnectionsCheck should be removed after Close")
}

func TestManager_RevalidateSettings_DetectsConfigChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		cachedURI         string
		freshHost         string
		freshPort         int
		freshDB           string
		freshUser         string
		freshPass         string
		expectReconnect   bool
		expectLogContains string
	}{
		{
			name:              "reconnects when host changes",
			cachedURI:         "mongodb://user:pass@oldhost:27017/testdb?authSource=admin",
			freshHost:         "newhost",
			freshPort:         27017,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "MongoDB config changed",
		},
		{
			name:              "reconnects when port changes",
			cachedURI:         "mongodb://user:pass@localhost:27017/testdb?authSource=admin",
			freshHost:         "localhost",
			freshPort:         27018,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "MongoDB config changed",
		},
		{
			name:              "reconnects when database changes",
			cachedURI:         "mongodb://user:pass@localhost:27017/olddb?authSource=admin",
			freshHost:         "localhost",
			freshPort:         27017,
			freshDB:           "newdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "MongoDB config changed",
		},
		{
			name:              "reconnects when username changes",
			cachedURI:         "mongodb://olduser:pass@localhost:27017/testdb?authSource=admin",
			freshHost:         "localhost",
			freshPort:         27017,
			freshDB:           "testdb",
			freshUser:         "newuser",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "MongoDB config changed",
		},
		{
			name:              "reconnects when password changes",
			cachedURI:         "mongodb://user:oldpass@localhost:27017/testdb?authSource=admin",
			freshHost:         "localhost",
			freshPort:         27017,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "newpass",
			expectReconnect:   true,
			expectLogContains: "MongoDB config changed",
		},
		{
			name:              "no reconnect when config is identical",
			cachedURI:         "mongodb://user:pass@localhost:27017/testdb?authSource=admin",
			freshHost:         "localhost",
			freshPort:         27017,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   false,
			expectLogContains: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(fmt.Sprintf(`{
					"id": "tenant-cfg",
					"tenantSlug": "config-test",
					"databases": {
						"onboarding": {
							"mongodb": {"host": %q, "port": %d, "database": %q, "username": %q, "password": %q}
						}
					}
				}`, tt.freshHost, tt.freshPort, tt.freshDB, tt.freshUser, tt.freshPass)))
			}))
			defer server.Close()

			capLogger := testutil.NewCapturingLogger()
			tmClient := mustNewTestClient(t, server.URL)
			manager := NewManager(tmClient, "ledger",
				WithLogger(capLogger),
				WithModule("onboarding"),
				WithConnectionsCheckInterval(1*time.Millisecond),
			)

			manager.connections["tenant-cfg"] = &MongoConnection{
				ConnectionStringSource: tt.cachedURI,
				DB:                     nil, // nil DB avoids real connection
			}
			manager.lastAccessed["tenant-cfg"] = time.Now()
			manager.lastConnectionsCheck["tenant-cfg"] = time.Now()

			// Trigger revalidation directly
			manager.revalidatePoolSettings("tenant-cfg")

			if tt.expectReconnect {
				// The reconnection will fail (no real MongoDB) but the old conn
				// should be kept. Verify the log message was produced.
				assert.True(t, capLogger.ContainsSubstring(tt.expectLogContains),
					"expected log containing %q, got: %v", tt.expectLogContains, capLogger.GetMessages())

				// Old connection should still exist (reconnection fails gracefully)
				stats := manager.Stats()
				assert.Equal(t, 1, stats.TotalConnections,
					"old connection should be kept when reconnection fails")
			} else {
				// No config change => no reconnect log
				assert.False(t, capLogger.ContainsSubstring("config changed"),
					"should NOT log config change when config is identical")
			}
		})
	}
}

func TestManager_RevalidateSettings_ConfigChangeKeepsOldConnOnFailure(t *testing.T) {
	t.Parallel()

	// Mock server returns a config with a different host (unreachable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-fail",
			"tenantSlug": "fail-test",
			"databases": {
				"onboarding": {
					"mongodb": {"host": "unreachable-host", "port": 27017, "database": "testdb", "username": "user", "password": "pass"}
				}
			}
		}`))
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(capLogger),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	manager.connections["tenant-fail"] = &MongoConnection{
		ConnectionStringSource: "mongodb://user:pass@localhost:27017/testdb?authSource=admin",
		DB:                     nil,
	}
	manager.lastAccessed["tenant-fail"] = time.Now()
	manager.lastConnectionsCheck["tenant-fail"] = time.Now()

	// Trigger revalidation directly
	manager.revalidatePoolSettings("tenant-fail")

	// Old connection should still exist (graceful: don't break existing tenants)
	stats := manager.Stats()
	assert.Equal(t, 1, stats.TotalConnections,
		"old connection should be kept when reconnection to new host fails")

	// Verify the failure was logged
	assert.True(t, capLogger.ContainsSubstring("keeping old connection"),
		"should log that old connection is being kept on failure")
}

func TestManager_RevalidateSettings_NoReconnectWhenConfigSame(t *testing.T) {
	t.Parallel()

	fakeDB, cleanupFake := startFakeMongoServer(t)
	defer cleanupFake()

	// Build the URI that matches the fake server's config
	matchingURI := "mongodb://user:pass@localhost:27017/testdb?authSource=admin"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-same",
			"tenantSlug": "same-test",
			"databases": {
				"onboarding": {
					"mongodb": {"host": "localhost", "port": 27017, "database": "testdb", "username": "user", "password": "pass"}
				}
			}
		}`))
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(capLogger),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	manager.connections["tenant-same"] = &MongoConnection{
		ConnectionStringSource: matchingURI,
		DB:                     fakeDB,
	}
	manager.lastAccessed["tenant-same"] = time.Now()
	manager.lastConnectionsCheck["tenant-same"] = time.Now()

	// Trigger revalidation directly
	manager.revalidatePoolSettings("tenant-same")

	// Should NOT log any config change
	assert.False(t, capLogger.ContainsSubstring("config changed"),
		"should NOT log config change when config is identical")

	// Connection should still exist unchanged
	stats := manager.Stats()
	assert.Equal(t, 1, stats.TotalConnections,
		"connection should remain when config is unchanged")

	manager.revalidateWG.Wait()
	require.NoError(t, manager.Close(context.Background()))
}

func TestManager_Close_WaitsForRevalidateSettings(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-slow",
			"tenantSlug": "slow-tenant",
			"databases": {
				"onboarding": {
					"mongodb": {"host": "localhost", "port": 27017, "database": "testdb", "username": "user", "password": "pass"}
				}
			}
		}`))
	}))
	defer server.Close()

	fakeDB, cleanupFake := startFakeMongoServer(t)
	defer cleanupFake()

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "test-service",
		WithLogger(testutil.NewMockLogger()),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	cachedConn := &MongoConnection{DB: fakeDB}
	manager.connections["tenant-slow"] = cachedConn
	manager.lastAccessed["tenant-slow"] = time.Now()
	manager.lastConnectionsCheck["tenant-slow"] = time.Time{}

	_, err := manager.GetConnection(context.Background(), "tenant-slow")
	require.NoError(t, err)

	err = manager.Close(context.Background())
	require.NoError(t, err)

	assert.True(t, manager.closed, "manager should be closed")
	assert.Empty(t, manager.connections, "connections should be cleared after Close")
}
