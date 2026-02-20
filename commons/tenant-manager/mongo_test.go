package tenantmanager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	mongolib "github.com/LerianStudio/lib-commons/v2/commons/mongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMongoManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		manager := NewMongoManager(client, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
	})
}

func TestMongoManager_GetClient_NoTenantID(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger")

	_, err := manager.GetClient(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestMongoManager_GetClient_ManagerClosed(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger")
	manager.Close(context.Background())

	_, err := manager.GetClient(context.Background(), "tenant-123")

	assert.ErrorIs(t, err, ErrManagerClosed)
}

func TestMongoManager_GetClient_SuspendedTenant(t *testing.T) {
	t.Run("propagates TenantSuspendedError from client", func(t *testing.T) {
		// Set up a mock Tenant Manager that returns 403 Forbidden for suspended tenants
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service ledger is suspended for this tenant","status":"suspended"}`))
		}))
		defer server.Close()

		tmClient := NewClient(server.URL, &mockLogger{})
		manager := NewMongoManager(tmClient, "ledger", WithMongoLogger(&mockLogger{}))

		_, err := manager.GetClient(context.Background(), "tenant-123")

		require.Error(t, err)
		assert.True(t, IsTenantSuspendedError(err), "expected TenantSuspendedError, got: %T", err)

		var suspErr *TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "suspended", suspErr.Status)
		assert.Equal(t, "tenant-123", suspErr.TenantID)
	})
}

func TestMongoManager_GetDatabaseForTenant_SuspendedTenant(t *testing.T) {
	t.Run("propagates TenantSuspendedError from GetTenantConfig", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service ledger is purged for this tenant","status":"purged"}`))
		}))
		defer server.Close()

		tmClient := NewClient(server.URL, &mockLogger{})
		manager := NewMongoManager(tmClient, "ledger", WithMongoLogger(&mockLogger{}))

		_, err := manager.GetDatabaseForTenant(context.Background(), "tenant-456")

		require.Error(t, err)
		assert.True(t, IsTenantSuspendedError(err), "expected TenantSuspendedError, got: %T", err)

		var suspErr *TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "purged", suspErr.Status)
	})
}

func TestBuildMongoURI(t *testing.T) {
	t.Run("returns URI when provided", func(t *testing.T) {
		cfg := &MongoDBConfig{
			URI: "mongodb://custom-uri",
		}

		uri := buildMongoURI(cfg)

		assert.Equal(t, "mongodb://custom-uri", uri)
	})

	t.Run("builds URI with credentials", func(t *testing.T) {
		cfg := &MongoDBConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "testdb",
			Username: "user",
			Password: "pass",
		}

		uri := buildMongoURI(cfg)

		assert.Equal(t, "mongodb://user:pass@localhost:27017/testdb", uri)
	})

	t.Run("builds URI without credentials", func(t *testing.T) {
		cfg := &MongoDBConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "testdb",
		}

		uri := buildMongoURI(cfg)

		assert.Equal(t, "mongodb://localhost:27017/testdb", uri)
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
				cfg := &MongoDBConfig{
					Host:     "localhost",
					Port:     27017,
					Database: "testdb",
					Username: tt.username,
					Password: tt.password,
				}

				uri := buildMongoURI(cfg)

				expectedURI := fmt.Sprintf("mongodb://%s:%s@localhost:27017/testdb",
					tt.expectedUser, tt.expectedPassword)
				assert.Equal(t, expectedURI, uri)
				assert.Contains(t, uri, tt.expectedUser)
				assert.Contains(t, uri, tt.expectedPassword)
			})
		}
	})
}

func TestContextWithTenantMongo(t *testing.T) {
	t.Run("stores and retrieves mongo database", func(t *testing.T) {
		// We can't create a real mongo.Database without a connection,
		// so we test the nil case
		ctx := context.Background()

		db := GetMongoFromContext(ctx)

		assert.Nil(t, db)
	})
}

func TestGetMongoForTenant(t *testing.T) {
	t.Run("returns error when no database in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetMongoForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}

func TestMongoManager_GetDatabaseForTenant_NoTenantID(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger")

	_, err := manager.GetDatabaseForTenant(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestMongoManager_GetClient_NilDBCachedConnection(t *testing.T) {
	t.Run("returns nil client when cached connection has nil DB", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		manager := NewMongoManager(client, "ledger")

		// Pre-populate cache with a connection that has nil DB
		cachedConn := &mongolib.MongoConnection{
			DB: nil,
		}
		manager.connections["tenant-123"] = cachedConn

		// Should return nil without attempting ping (nil DB skips health check)
		result, err := manager.GetClient(context.Background(), "tenant-123")

		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMongoManager_CloseClient_EvictsFromCache(t *testing.T) {
	t.Run("evicts connection from cache on close", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		manager := NewMongoManager(client, "ledger")

		// Pre-populate cache with a connection that has nil DB (to avoid disconnect errors)
		cachedConn := &mongolib.MongoConnection{
			DB: nil,
		}
		manager.connections["tenant-123"] = cachedConn

		err := manager.CloseClient(context.Background(), "tenant-123")

		assert.NoError(t, err)

		manager.mu.RLock()
		_, exists := manager.connections["tenant-123"]
		manager.mu.RUnlock()

		assert.False(t, exists, "connection should have been evicted from cache")
	})
}

func TestMongoManager_EvictLRU(t *testing.T) {
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

			opts := []MongoOption{
				WithMongoLogger(&mockLogger{}),
				WithMongoMaxTenantPools(tt.maxConnections),
			}
			if tt.idleTimeout > 0 {
				opts = append(opts, WithMongoIdleTimeout(tt.idleTimeout))
			}

			client := &Client{baseURL: "http://localhost:8080"}
			manager := NewMongoManager(client, "ledger", opts...)

			// Pre-populate pool with connections (nil DB to avoid real MongoDB)
			if tt.preloadCount >= 1 {
				manager.connections["tenant-old"] = &mongolib.MongoConnection{DB: nil}
				manager.lastAccessed["tenant-old"] = time.Now().Add(-tt.oldTenantAge)
			}

			if tt.preloadCount >= 2 {
				manager.connections["tenant-new"] = &mongolib.MongoConnection{DB: nil}
				manager.lastAccessed["tenant-new"] = time.Now().Add(-tt.newTenantAge)
			}

			// For unlimited test, add more connections
			for i := 2; i < tt.preloadCount; i++ {
				id := "tenant-extra-" + time.Now().Add(time.Duration(i)*time.Second).Format("150405")
				manager.connections[id] = &mongolib.MongoConnection{DB: nil}
				manager.lastAccessed[id] = time.Now().Add(-time.Duration(i) * time.Minute)
			}

			// Call evictLRU (caller must hold write lock)
			manager.mu.Lock()
			manager.evictLRU(context.Background(), &mockLogger{})
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

func TestMongoManager_PoolGrowsBeyondSoftLimit_WhenAllActive(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger",
		WithMongoLogger(&mockLogger{}),
		WithMongoMaxTenantPools(2),
		WithMongoIdleTimeout(5*time.Minute),
	)

	// Pre-populate with 2 connections, both accessed recently (within idle timeout)
	for _, id := range []string{"tenant-1", "tenant-2"} {
		manager.connections[id] = &mongolib.MongoConnection{DB: nil}
		manager.lastAccessed[id] = time.Now().Add(-1 * time.Minute)
	}

	// Try to evict - should not evict because all connections are active
	manager.mu.Lock()
	manager.evictLRU(context.Background(), &mockLogger{})
	manager.mu.Unlock()

	// Pool should remain at 2 (no eviction occurred)
	assert.Equal(t, 2, len(manager.connections),
		"pool should not shrink when all connections are active")

	// Simulate adding a third connection (pool grows beyond soft limit)
	manager.connections["tenant-3"] = &mongolib.MongoConnection{DB: nil}
	manager.lastAccessed["tenant-3"] = time.Now()

	assert.Equal(t, 3, len(manager.connections),
		"pool should grow beyond soft limit when all connections are active")
}

func TestMongoManager_WithMongoIdleTimeout_Option(t *testing.T) {
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
			manager := NewMongoManager(client, "ledger",
				WithMongoIdleTimeout(tt.idleTimeout),
			)

			assert.Equal(t, tt.expectedTimeout, manager.idleTimeout)
		})
	}
}

func TestMongoManager_LRU_LastAccessedUpdatedOnCacheHit(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger",
		WithMongoLogger(&mockLogger{}),
		WithMongoMaxTenantPools(5),
	)

	// Pre-populate cache with a connection that has nil DB (skips health check)
	cachedConn := &mongolib.MongoConnection{DB: nil}

	initialTime := time.Now().Add(-5 * time.Minute)
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = initialTime

	// Access the connection (cache hit)
	result, err := manager.GetClient(context.Background(), "tenant-123")

	require.NoError(t, err)
	assert.Nil(t, result, "nil DB should return nil client")

	// Verify lastAccessed was updated to a more recent time
	manager.mu.RLock()
	updatedTime := manager.lastAccessed["tenant-123"]
	manager.mu.RUnlock()

	assert.True(t, updatedTime.After(initialTime),
		"lastAccessed should be updated after cache hit: initial=%v, updated=%v",
		initialTime, updatedTime)
}

func TestMongoManager_CloseClient_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger",
		WithMongoLogger(&mockLogger{}),
	)

	// Pre-populate cache with a connection that has nil DB
	manager.connections["tenant-123"] = &mongolib.MongoConnection{DB: nil}
	manager.lastAccessed["tenant-123"] = time.Now()

	// Close the specific tenant client
	err := manager.CloseClient(context.Background(), "tenant-123")

	require.NoError(t, err)

	manager.mu.RLock()
	_, connExists := manager.connections["tenant-123"]
	_, accessExists := manager.lastAccessed["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, connExists, "connection should be removed after CloseClient")
	assert.False(t, accessExists, "lastAccessed should be removed after CloseClient")
}

func TestMongoManager_WithMongoMaxTenantPools_Option(t *testing.T) {
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
			manager := NewMongoManager(client, "ledger",
				WithMongoMaxTenantPools(tt.maxConnections),
			)

			assert.Equal(t, tt.expectedMax, manager.maxConnections)
		})
	}
}

// capturingMongoLogger implements log.Logger and captures log messages for assertion.
type capturingMongoLogger struct {
	mu       sync.Mutex
	messages []string
}

func (cl *capturingMongoLogger) record(msg string)            { cl.mu.Lock(); cl.messages = append(cl.messages, msg); cl.mu.Unlock() }
func (cl *capturingMongoLogger) Info(args ...any)              { cl.record(fmt.Sprint(args...)) }
func (cl *capturingMongoLogger) Infof(f string, a ...any)     { cl.record(fmt.Sprintf(f, a...)) }
func (cl *capturingMongoLogger) Infoln(args ...any)            { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingMongoLogger) Error(args ...any)             { cl.record(fmt.Sprint(args...)) }
func (cl *capturingMongoLogger) Errorf(f string, a ...any)    { cl.record(fmt.Sprintf(f, a...)) }
func (cl *capturingMongoLogger) Errorln(args ...any)           { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingMongoLogger) Warn(args ...any)              { cl.record(fmt.Sprint(args...)) }
func (cl *capturingMongoLogger) Warnf(f string, a ...any)     { cl.record(fmt.Sprintf(f, a...)) }
func (cl *capturingMongoLogger) Warnln(args ...any)            { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingMongoLogger) Debug(args ...any)             { cl.record(fmt.Sprint(args...)) }
func (cl *capturingMongoLogger) Debugf(f string, a ...any)    { cl.record(fmt.Sprintf(f, a...)) }
func (cl *capturingMongoLogger) Debugln(args ...any)           { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingMongoLogger) Fatal(args ...any)             { cl.record(fmt.Sprint(args...)) }
func (cl *capturingMongoLogger) Fatalf(f string, a ...any)    { cl.record(fmt.Sprintf(f, a...)) }
func (cl *capturingMongoLogger) Fatalln(args ...any)           { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingMongoLogger) WithFields(f ...any) log.Logger            { return cl }
func (cl *capturingMongoLogger) WithDefaultMessageTemplate(s string) log.Logger { return cl }
func (cl *capturingMongoLogger) Sync() error                   { return nil }

func (cl *capturingMongoLogger) containsSubstring(sub string) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	for _, msg := range cl.messages {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

func TestMongoManager_ApplyConnectionSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		module        string
		config        *TenantConfig
		hasCachedConn bool
		expectWarning bool
	}{
		{
			name:   "logs warning when top-level settings exist",
			module: "onboarding",
			config: &TenantConfig{
				ConnectionSettings: &ConnectionSettings{
					MaxOpenConns: 30,
				},
			},
			hasCachedConn: true,
			expectWarning: true,
		},
		{
			name:   "logs warning when module-level settings exist",
			module: "onboarding",
			config: &TenantConfig{
				Databases: map[string]DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &ConnectionSettings{
							MaxOpenConns: 50,
						},
					},
				},
			},
			hasCachedConn: true,
			expectWarning: true,
		},
		{
			name:          "no warning when no cached connection",
			module:        "onboarding",
			config:        &TenantConfig{ConnectionSettings: &ConnectionSettings{MaxOpenConns: 30}},
			hasCachedConn: false,
			expectWarning: false,
		},
		{
			name:   "no warning when config has no connection settings",
			module: "onboarding",
			config: &TenantConfig{
				Databases: map[string]DatabaseConfig{
					"onboarding": {
						MongoDB: &MongoDBConfig{Host: "localhost"},
					},
				},
			},
			hasCachedConn: true,
			expectWarning: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := &capturingMongoLogger{}
			client := &Client{baseURL: "http://localhost:8080"}
			manager := NewMongoManager(client, "ledger",
				WithMongoModule(tt.module),
				WithMongoLogger(logger),
			)

			if tt.hasCachedConn {
				manager.connections["tenant-123"] = &mongolib.MongoConnection{DB: nil}
			}

			manager.ApplyConnectionSettings("tenant-123", tt.config)

			if tt.expectWarning {
				assert.True(t, logger.containsSubstring("MongoDB connection settings changed"),
					"expected warning about MongoDB pool resize limitation")
			} else {
				assert.False(t, logger.containsSubstring("MongoDB connection settings changed"),
					"should not log warning when no settings change is applicable")
			}
		})
	}
}
