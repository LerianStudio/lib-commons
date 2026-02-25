package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v3/commons/log"
	libPostgres "github.com/LerianStudio/lib-commons/v3/commons/postgres"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLogger is a no-op implementation of libLog.Logger for unit tests.
// It discards all log output, allowing tests to focus on business logic.
type mockLogger struct{}

func (m *mockLogger) Info(_ ...any)                                    {}
func (m *mockLogger) Infof(_ string, _ ...any)                         {}
func (m *mockLogger) Infoln(_ ...any)                                  {}
func (m *mockLogger) Error(_ ...any)                                   {}
func (m *mockLogger) Errorf(_ string, _ ...any)                        {}
func (m *mockLogger) Errorln(_ ...any)                                 {}
func (m *mockLogger) Warn(_ ...any)                                    {}
func (m *mockLogger) Warnf(_ string, _ ...any)                         {}
func (m *mockLogger) Warnln(_ ...any)                                  {}
func (m *mockLogger) Debug(_ ...any)                                   {}
func (m *mockLogger) Debugf(_ string, _ ...any)                        {}
func (m *mockLogger) Debugln(_ ...any)                                 {}
func (m *mockLogger) Fatal(_ ...any)                                   {}
func (m *mockLogger) Fatalf(_ string, _ ...any)                        {}
func (m *mockLogger) Fatalln(_ ...any)                                 {}
func (m *mockLogger) WithFields(_ ...any) libLog.Logger                { return m }
func (m *mockLogger) WithDefaultMessageTemplate(_ string) libLog.Logger { return m }
func (m *mockLogger) Sync() error                                       { return nil }

// capturingLogger captures log messages for test assertions.
type capturingLogger struct {
	mu       sync.Mutex
	messages []string
}

func (cl *capturingLogger) record(msg string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.messages = append(cl.messages, msg)
}

func (cl *capturingLogger) containsSubstring(sub string) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	for _, msg := range cl.messages {
		if strings.Contains(msg, sub) {
			return true
		}
	}

	return false
}

func (cl *capturingLogger) Info(args ...any)                  { cl.record(fmt.Sprint(args...)) }
func (cl *capturingLogger) Infof(format string, args ...any)  { cl.record(fmt.Sprintf(format, args...)) }
func (cl *capturingLogger) Infoln(args ...any)                { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingLogger) Error(args ...any)                 { cl.record(fmt.Sprint(args...)) }
func (cl *capturingLogger) Errorf(format string, args ...any) { cl.record(fmt.Sprintf(format, args...)) }
func (cl *capturingLogger) Errorln(args ...any)               { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingLogger) Warn(args ...any)                  { cl.record(fmt.Sprint(args...)) }
func (cl *capturingLogger) Warnf(format string, args ...any)  { cl.record(fmt.Sprintf(format, args...)) }
func (cl *capturingLogger) Warnln(args ...any)                { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingLogger) Debug(args ...any)                 { cl.record(fmt.Sprint(args...)) }
func (cl *capturingLogger) Debugf(format string, args ...any) { cl.record(fmt.Sprintf(format, args...)) }
func (cl *capturingLogger) Debugln(args ...any)               { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingLogger) Fatal(args ...any)                 { cl.record(fmt.Sprint(args...)) }
func (cl *capturingLogger) Fatalf(format string, args ...any) { cl.record(fmt.Sprintf(format, args...)) }
func (cl *capturingLogger) Fatalln(args ...any)               { cl.record(fmt.Sprintln(args...)) }
func (cl *capturingLogger) WithFields(_ ...any) libLog.Logger                { return cl }
func (cl *capturingLogger) WithDefaultMessageTemplate(_ string) libLog.Logger { return cl }
func (cl *capturingLogger) Sync() error                                       { return nil }

// pingableDB implements dbresolver.DB with configurable PingContext behavior
// for testing connection health check logic.
type pingableDB struct {
	pingErr error
	closed  bool
}

var _ dbresolver.DB = (*pingableDB)(nil)

func (m *pingableDB) Begin() (dbresolver.Tx, error) { return nil, nil }
func (m *pingableDB) BeginTx(_ context.Context, _ *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}
func (m *pingableDB) Close() error                                        { m.closed = true; return nil }
func (m *pingableDB) Conn(_ context.Context) (dbresolver.Conn, error)     { return nil, nil }
func (m *pingableDB) Driver() driver.Driver                               { return nil }
func (m *pingableDB) Exec(_ string, _ ...interface{}) (sql.Result, error) { return nil, nil }
func (m *pingableDB) ExecContext(_ context.Context, _ string, _ ...interface{}) (sql.Result, error) {
	return nil, nil
}
func (m *pingableDB) Ping() error                               { return m.pingErr }
func (m *pingableDB) PingContext(_ context.Context) error       { return m.pingErr }
func (m *pingableDB) Prepare(_ string) (dbresolver.Stmt, error) { return nil, nil }
func (m *pingableDB) PrepareContext(_ context.Context, _ string) (dbresolver.Stmt, error) {
	return nil, nil
}
func (m *pingableDB) Query(_ string, _ ...interface{}) (*sql.Rows, error) { return nil, nil }
func (m *pingableDB) QueryContext(_ context.Context, _ string, _ ...interface{}) (*sql.Rows, error) {
	return nil, nil
}
func (m *pingableDB) QueryRow(_ string, _ ...interface{}) *sql.Row { return nil }
func (m *pingableDB) QueryRowContext(_ context.Context, _ string, _ ...interface{}) *sql.Row {
	return nil
}
func (m *pingableDB) SetConnMaxIdleTime(_ time.Duration) {}
func (m *pingableDB) SetConnMaxLifetime(_ time.Duration) {}
func (m *pingableDB) SetMaxIdleConns(_ int)              {}
func (m *pingableDB) SetMaxOpenConns(_ int)              {}
func (m *pingableDB) PrimaryDBs() []*sql.DB              { return nil }
func (m *pingableDB) ReplicaDBs() []*sql.DB              { return nil }
func (m *pingableDB) Stats() sql.DBStats                 { return sql.DBStats{} }

// trackingDB extends pingableDB to track SetMaxOpenConns/SetMaxIdleConns calls.
// Fields use int32 with atomic operations to avoid data races when written
// by async goroutines (revalidateSettings) and read by test assertions.
type trackingDB struct {
	pingableDB
	maxOpenConns int32
	maxIdleConns int32
}

func (t *trackingDB) SetMaxOpenConns(n int) { atomic.StoreInt32(&t.maxOpenConns, int32(n)) }
func (t *trackingDB) SetMaxIdleConns(n int) { atomic.StoreInt32(&t.maxIdleConns, int32(n)) }
func (t *trackingDB) MaxOpenConns() int32   { return atomic.LoadInt32(&t.maxOpenConns) }
func (t *trackingDB) MaxIdleConns() int32   { return atomic.LoadInt32(&t.maxIdleConns) }

func TestNewManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", &mockLogger{})
		manager := NewManager(c, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
	})
}

func TestManager_GetConnection_NoTenantID(t *testing.T) {
	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger")

	_, err := manager.GetConnection(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestManager_Close(t *testing.T) {
	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger")

	err := manager.Close(context.Background())

	assert.NoError(t, err)
	assert.True(t, manager.closed)
}

func TestManager_GetConnection_ManagerClosed(t *testing.T) {
	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger")
	manager.Close(context.Background())

	_, err := manager.GetConnection(context.Background(), "tenant-123")

	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

func TestIsolationModeConstants(t *testing.T) {
	t.Run("isolation mode constants have expected values", func(t *testing.T) {
		assert.Equal(t, "isolated", IsolationModeIsolated)
		assert.Equal(t, "schema", IsolationModeSchema)
	})
}

func TestBuildConnectionString(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *core.PostgreSQLConfig
		expected string
	}{
		{
			name: "builds connection string without schema",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "disable",
			},
			expected: "host=localhost port=5432 user=user password='pass' dbname=testdb sslmode=disable",
		},
		{
			name: "builds connection string with schema in options",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "disable",
				Schema:   "tenant_abc",
			},
			expected: "host=localhost port=5432 user=user password='pass' dbname=testdb sslmode=disable options=-csearch_path=\"tenant_abc\"",
		},
		{
			name: "defaults sslmode to disable when empty",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
			},
			expected: "host=localhost port=5432 user=user password='pass' dbname=testdb sslmode=disable",
		},
		{
			name: "uses provided sslmode",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "require",
			},
			expected: "host=localhost port=5432 user=user password='pass' dbname=testdb sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildConnectionString(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildConnectionStrings_PrimaryAndReplica(t *testing.T) {
	t.Run("builds separate connection strings for primary and replica", func(t *testing.T) {
		primaryConfig := &core.PostgreSQLConfig{
			Host:     "primary-host",
			Port:     5432,
			Username: "user",
			Password: "pass",
			Database: "testdb",
			SSLMode:  "disable",
		}
		replicaConfig := &core.PostgreSQLConfig{
			Host:     "replica-host",
			Port:     5433,
			Username: "user",
			Password: "pass",
			Database: "testdb",
			SSLMode:  "disable",
		}

		primaryConnStr := buildConnectionString(primaryConfig)
		replicaConnStr := buildConnectionString(replicaConfig)

		assert.Contains(t, primaryConnStr, "host=primary-host")
		assert.Contains(t, primaryConnStr, "port=5432")
		assert.Contains(t, replicaConnStr, "host=replica-host")
		assert.Contains(t, replicaConnStr, "port=5433")
		assert.NotEqual(t, primaryConnStr, replicaConnStr)
	})

	t.Run("fallback to primary when replica not configured", func(t *testing.T) {
		config := &core.TenantConfig{
			Databases: map[string]core.DatabaseConfig{
				"onboarding": {
					PostgreSQL: &core.PostgreSQLConfig{
						Host:     "primary-host",
						Port:     5432,
						Username: "user",
						Password: "pass",
						Database: "testdb",
					},
					// No PostgreSQLReplica configured
				},
			},
		}

		pgConfig := config.GetPostgreSQLConfig("ledger", "onboarding")
		pgReplicaConfig := config.GetPostgreSQLReplicaConfig("ledger", "onboarding")

		assert.NotNil(t, pgConfig)
		assert.Nil(t, pgReplicaConfig)

		// When replica is nil, system should use primary connection string
		primaryConnStr := buildConnectionString(pgConfig)

		replicaConnStr := primaryConnStr
		if pgReplicaConfig != nil {
			replicaConnStr = buildConnectionString(pgReplicaConfig)
		}

		assert.Equal(t, primaryConnStr, replicaConnStr)
	})

	t.Run("uses replica config when available", func(t *testing.T) {
		config := &core.TenantConfig{
			Databases: map[string]core.DatabaseConfig{
				"onboarding": {
					PostgreSQL: &core.PostgreSQLConfig{
						Host:     "primary-host",
						Port:     5432,
						Username: "user",
						Password: "pass",
						Database: "testdb",
					},
					PostgreSQLReplica: &core.PostgreSQLConfig{
						Host:     "replica-host",
						Port:     5433,
						Username: "user",
						Password: "pass",
						Database: "testdb",
					},
				},
			},
		}

		pgConfig := config.GetPostgreSQLConfig("ledger", "onboarding")
		pgReplicaConfig := config.GetPostgreSQLReplicaConfig("ledger", "onboarding")

		assert.NotNil(t, pgConfig)
		assert.NotNil(t, pgReplicaConfig)

		primaryConnStr := buildConnectionString(pgConfig)

		replicaConnStr := primaryConnStr
		if pgReplicaConfig != nil {
			replicaConnStr = buildConnectionString(pgReplicaConfig)
		}

		assert.NotEqual(t, primaryConnStr, replicaConnStr)
		assert.Contains(t, primaryConnStr, "host=primary-host")
		assert.Contains(t, replicaConnStr, "host=replica-host")
	})

	t.Run("handles replica with different database name", func(t *testing.T) {
		config := &core.TenantConfig{
			Databases: map[string]core.DatabaseConfig{
				"onboarding": {
					PostgreSQL: &core.PostgreSQLConfig{
						Host:     "primary-host",
						Port:     5432,
						Username: "user",
						Password: "pass",
						Database: "primary_db",
					},
					PostgreSQLReplica: &core.PostgreSQLConfig{
						Host:     "replica-host",
						Port:     5433,
						Username: "user",
						Password: "pass",
						Database: "replica_db",
					},
				},
			},
		}

		pgConfig := config.GetPostgreSQLConfig("ledger", "onboarding")
		pgReplicaConfig := config.GetPostgreSQLReplicaConfig("ledger", "onboarding")

		assert.Equal(t, "primary_db", pgConfig.Database)
		assert.Equal(t, "replica_db", pgReplicaConfig.Database)
	})
}

func TestManager_GetConnection_HealthyCache(t *testing.T) {
	t.Run("returns cached connection when ping succeeds", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", &mockLogger{})
		manager := NewManager(c, "ledger")

		// Pre-populate cache with a healthy connection
		healthyDB := &pingableDB{pingErr: nil}
		var db dbresolver.DB = healthyDB

		cachedConn := &libPostgres.PostgresConnection{
			ConnectionDB: &db,
		}
		manager.connections["tenant-123"] = cachedConn

		conn, err := manager.GetConnection(context.Background(), "tenant-123")

		require.NoError(t, err)
		assert.Equal(t, cachedConn, conn)
	})
}

func TestManager_GetConnection_UnhealthyCacheEvicts(t *testing.T) {
	t.Run("evicts cached connection when ping fails", func(t *testing.T) {
		// Set up a mock Tenant Manager that returns 500 to simulate unavailability
		// after eviction. The key assertion is that the stale connection is evicted.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		tmClient := client.NewClient(server.URL, &mockLogger{})
		manager := NewManager(tmClient, "ledger", WithLogger(&mockLogger{}))

		// Pre-populate cache with an unhealthy connection (simulates auth failure after credential rotation)
		unhealthyDB := &pingableDB{pingErr: errors.New("FATAL: password authentication failed (SQLSTATE 28P01)")}
		var db dbresolver.DB = unhealthyDB

		cachedConn := &libPostgres.PostgresConnection{
			ConnectionDB: &db,
		}
		manager.connections["tenant-123"] = cachedConn

		// GetConnection will try to ping, fail, evict, then call createConnection.
		// createConnection will fail because mock Tenant Manager returns 500,
		// but the important thing is the stale connection was evicted.
		_, err := manager.GetConnection(context.Background(), "tenant-123")

		// Expect an error because createConnection cannot get config from Tenant Manager
		assert.Error(t, err)

		// Verify the stale connection was evicted from cache
		manager.mu.RLock()
		_, exists := manager.connections["tenant-123"]
		manager.mu.RUnlock()

		assert.False(t, exists, "stale connection should have been evicted from cache")
		assert.True(t, unhealthyDB.closed, "stale connection's DB should have been closed")
	})
}

func TestManager_GetConnection_SuspendedTenant(t *testing.T) {
	t.Run("propagates TenantSuspendedError from client", func(t *testing.T) {
		// Set up a mock Tenant Manager that returns 403 Forbidden for suspended tenants
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service ledger is suspended for this tenant","status":"suspended"}`))
		}))
		defer server.Close()

		tmClient := client.NewClient(server.URL, &mockLogger{})
		manager := NewManager(tmClient, "ledger", WithLogger(&mockLogger{}))

		_, err := manager.GetConnection(context.Background(), "tenant-123")

		require.Error(t, err)
		assert.True(t, core.IsTenantSuspendedError(err), "expected TenantSuspendedError, got: %T", err)

		var suspErr *core.TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "suspended", suspErr.Status)
		assert.Equal(t, "tenant-123", suspErr.TenantID)
	})
}

func TestManager_GetConnection_NilConnectionDB(t *testing.T) {
	t.Run("returns cached connection when ConnectionDB is nil without ping", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", &mockLogger{})
		manager := NewManager(c, "ledger")

		// Pre-populate cache with a connection that has nil ConnectionDB
		cachedConn := &libPostgres.PostgresConnection{
			ConnectionDB: nil,
		}
		manager.connections["tenant-123"] = cachedConn

		conn, err := manager.GetConnection(context.Background(), "tenant-123")

		require.NoError(t, err)
		assert.Equal(t, cachedConn, conn)
	})
}

func TestManager_EvictLRU(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		maxConnections      int
		idleTimeout         time.Duration
		preloadCount        int
		oldTenantAge        time.Duration // how long ago tenant-old was accessed
		newTenantAge        time.Duration // how long ago tenant-new was accessed
		expectEviction      bool
		expectedPoolSize    int
		expectedEvictedID   string
		expectedEvictClosed bool
	}{
		{
			name:                "evicts oldest idle connection when pool is at soft limit",
			maxConnections:      2,
			idleTimeout:         5 * time.Minute,
			preloadCount:        2,
			oldTenantAge:        10 * time.Minute,
			newTenantAge:        1 * time.Minute,
			expectEviction:      true,
			expectedPoolSize:    1,
			expectedEvictedID:   "tenant-old",
			expectedEvictClosed: true,
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
			oldTenantAge:     2 * time.Minute, // within 5min idle timeout
			newTenantAge:     1 * time.Minute, // within 5min idle timeout
			expectEviction:   false,
			expectedPoolSize: 2,
		},
		{
			name:                "respects custom idle timeout",
			maxConnections:      2,
			idleTimeout:         30 * time.Second,
			preloadCount:        2,
			oldTenantAge:        1 * time.Minute,  // beyond 30s idle timeout
			newTenantAge:        10 * time.Second, // within 30s idle timeout
			expectEviction:      true,
			expectedPoolSize:    1,
			expectedEvictedID:   "tenant-old",
			expectedEvictClosed: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := []Option{
				WithLogger(&mockLogger{}),
				WithMaxTenantPools(tt.maxConnections),
			}
			if tt.idleTimeout > 0 {
				opts = append(opts, WithIdleTimeout(tt.idleTimeout))
			}

			c := client.NewClient("http://localhost:8080", &mockLogger{})
			manager := NewManager(c, "ledger", opts...)

			// Pre-populate pool with connections
			if tt.preloadCount >= 1 {
				oldDB := &pingableDB{}
				var oldDBIface dbresolver.DB = oldDB

				manager.connections["tenant-old"] = &libPostgres.PostgresConnection{
					ConnectionDB: &oldDBIface,
				}
				manager.lastAccessed["tenant-old"] = time.Now().Add(-tt.oldTenantAge)
			}

			if tt.preloadCount >= 2 {
				newDB := &pingableDB{}
				var newDBIface dbresolver.DB = newDB

				manager.connections["tenant-new"] = &libPostgres.PostgresConnection{
					ConnectionDB: &newDBIface,
				}
				manager.lastAccessed["tenant-new"] = time.Now().Add(-tt.newTenantAge)
			}

			// For unlimited test, add more connections
			for i := 2; i < tt.preloadCount; i++ {
				db := &pingableDB{}
				var dbIface dbresolver.DB = db

				id := "tenant-extra-" + time.Now().Add(time.Duration(i)*time.Second).Format("150405")
				manager.connections[id] = &libPostgres.PostgresConnection{
					ConnectionDB: &dbIface,
				}
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

func TestManager_PoolGrowsBeyondSoftLimit_WhenAllActive(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger",
		WithLogger(&mockLogger{}),
		WithMaxTenantPools(2),
		WithIdleTimeout(5*time.Minute),
	)

	// Pre-populate with 2 connections, both accessed recently (within idle timeout)
	for _, id := range []string{"tenant-1", "tenant-2"} {
		db := &pingableDB{}
		var dbIface dbresolver.DB = db

		manager.connections[id] = &libPostgres.PostgresConnection{
			ConnectionDB: &dbIface,
		}
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
	db := &pingableDB{}
	var dbIface dbresolver.DB = db

	manager.connections["tenant-3"] = &libPostgres.PostgresConnection{
		ConnectionDB: &dbIface,
	}
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

			c := client.NewClient("http://localhost:8080", &mockLogger{})
			manager := NewManager(c, "ledger",
				WithIdleTimeout(tt.idleTimeout),
			)

			assert.Equal(t, tt.expectedTimeout, manager.idleTimeout)
		})
	}
}

func TestManager_LRU_LastAccessedUpdatedOnCacheHit(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger",
		WithLogger(&mockLogger{}),
		WithMaxTenantPools(5),
	)

	// Pre-populate cache with a healthy connection
	healthyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = healthyDB

	cachedConn := &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}

	initialTime := time.Now().Add(-5 * time.Minute)
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = initialTime

	// Access the connection (cache hit)
	conn, err := manager.GetConnection(context.Background(), "tenant-123")

	require.NoError(t, err)
	assert.Equal(t, cachedConn, conn)

	// Verify lastAccessed was updated to a more recent time
	manager.mu.RLock()
	updatedTime := manager.lastAccessed["tenant-123"]
	manager.mu.RUnlock()

	assert.True(t, updatedTime.After(initialTime),
		"lastAccessed should be updated after cache hit: initial=%v, updated=%v",
		initialTime, updatedTime)
}

func TestManager_CloseConnection_CleansUpLastAccessed(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger",
		WithLogger(&mockLogger{}),
	)

	// Pre-populate cache
	healthyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = healthyDB

	manager.connections["tenant-123"] = &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
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

			c := client.NewClient("http://localhost:8080", &mockLogger{})
			manager := NewManager(c, "ledger",
				WithMaxTenantPools(tt.maxConnections),
			)

			assert.Equal(t, tt.expectedMax, manager.maxConnections)
		})
	}
}

func TestManager_Stats_IncludesMaxConnections(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger",
		WithMaxTenantPools(50),
	)

	stats := manager.Stats()

	assert.Equal(t, 50, stats.MaxConnections)
	assert.Equal(t, 0, stats.TotalConnections)
	assert.Equal(t, 0, stats.ActiveConnections)
}

func TestManager_WithSettingsCheckInterval_Option(t *testing.T) {
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

			c := client.NewClient("http://localhost:8080", &mockLogger{})
			manager := NewManager(c, "ledger",
				WithSettingsCheckInterval(tt.interval),
			)

			assert.Equal(t, tt.expectedInterval, manager.settingsCheckInterval)
		})
	}
}

func TestManager_DefaultSettingsCheckInterval(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger")

	assert.Equal(t, defaultSettingsCheckInterval, manager.settingsCheckInterval,
		"default settings check interval should be set from named constant")
	assert.NotNil(t, manager.lastSettingsCheck,
		"lastSettingsCheck map should be initialized")
}

func TestManager_GetConnection_RevalidatesSettingsAfterInterval(t *testing.T) {
	t.Parallel()

	// Set up a mock Tenant Manager that returns updated connection settings
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return config with updated connection settings (maxOpenConns changed to 50)
		w.Write([]byte(`{
			"id": "tenant-123",
			"tenantSlug": "test-tenant",
			"databases": {
				"onboarding": {
					"postgresql": {"host": "localhost", "port": 5432, "database": "testdb", "username": "user", "password": "pass"},
					"connectionSettings": {"maxOpenConns": 50, "maxIdleConns": 15}
				}
			}
		}`))
	}))
	defer server.Close()

	tmClient := client.NewClient(server.URL, &mockLogger{})
	manager := NewManager(tmClient, "ledger",
		WithLogger(&mockLogger{}),
		WithModule("onboarding"),
		// Use a very short interval so the test triggers revalidation immediately
		WithSettingsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate cache with a healthy connection and an old settings check time
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastSettingsCheck to a time well in the past so revalidation triggers
	manager.lastSettingsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	// Call GetConnection - should return cached conn AND trigger async revalidation
	conn, err := manager.GetConnection(context.Background(), "tenant-123")

	require.NoError(t, err)
	assert.Equal(t, cachedConn, conn, "should return the cached connection")

	// Wait for the async goroutine to complete
	time.Sleep(200 * time.Millisecond)

	// Verify that the Tenant Manager was called to fetch fresh config
	assert.Greater(t, atomic.LoadInt32(&callCount), int32(0), "should have fetched fresh config from Tenant Manager")

	// Verify that ApplyConnectionSettings was called with the new values
	assert.Equal(t, int32(50), tDB.MaxOpenConns(), "maxOpenConns should be updated to 50")
	assert.Equal(t, int32(15), tDB.MaxIdleConns(), "maxIdleConns should be updated to 15")
}

func TestManager_GetConnection_DoesNotRevalidateBeforeInterval(t *testing.T) {
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
					"connectionSettings": {"maxOpenConns": 50, "maxIdleConns": 15}
				}
			}
		}`))
	}))
	defer server.Close()

	tmClient := client.NewClient(server.URL, &mockLogger{})
	manager := NewManager(tmClient, "ledger",
		WithLogger(&mockLogger{}),
		WithModule("onboarding"),
		// Use a very long interval so revalidation does NOT trigger
		WithSettingsCheckInterval(1*time.Hour),
	)

	// Pre-populate cache with a healthy connection and a recent settings check time
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastSettingsCheck to now - should NOT trigger revalidation
	manager.lastSettingsCheck["tenant-123"] = time.Now()

	// Call GetConnection - should return cached conn without revalidation
	conn, err := manager.GetConnection(context.Background(), "tenant-123")

	require.NoError(t, err)
	assert.Equal(t, cachedConn, conn)

	// Wait to ensure no async goroutine fires
	time.Sleep(100 * time.Millisecond)

	// Verify that Tenant Manager was NOT called
	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount), "should NOT have fetched config - interval not elapsed")

	// Verify that connection settings were NOT changed
	assert.Equal(t, int32(0), tDB.MaxOpenConns(), "maxOpenConns should NOT be changed")
	assert.Equal(t, int32(0), tDB.MaxIdleConns(), "maxIdleConns should NOT be changed")
}

func TestManager_GetConnection_FailedRevalidationDoesNotBreakConnection(t *testing.T) {
	t.Parallel()

	// Set up a mock Tenant Manager that returns 500 (simulates unavailability)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmClient := client.NewClient(server.URL, &mockLogger{})
	manager := NewManager(tmClient, "ledger",
		WithLogger(&mockLogger{}),
		WithModule("onboarding"),
		WithSettingsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate cache with a healthy connection
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastSettingsCheck to the past so revalidation triggers
	manager.lastSettingsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	// Call GetConnection - should return cached conn even though revalidation will fail
	conn, err := manager.GetConnection(context.Background(), "tenant-123")

	require.NoError(t, err, "GetConnection should NOT fail when revalidation fails")
	assert.Equal(t, cachedConn, conn, "should still return the cached connection")

	// Wait for the async goroutine to complete (and fail)
	time.Sleep(200 * time.Millisecond)

	// Verify that connection settings were NOT changed (fetch failed)
	assert.Equal(t, int32(0), tDB.MaxOpenConns(), "maxOpenConns should NOT be changed on failed revalidation")
	assert.Equal(t, int32(0), tDB.MaxIdleConns(), "maxIdleConns should NOT be changed on failed revalidation")
}

func TestManager_CloseConnection_CleansUpLastSettingsCheck(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger",
		WithLogger(&mockLogger{}),
	)

	// Pre-populate cache
	healthyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = healthyDB

	manager.connections["tenant-123"] = &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.lastAccessed["tenant-123"] = time.Now()
	manager.lastSettingsCheck["tenant-123"] = time.Now()

	// Close the specific tenant connection
	err := manager.CloseConnection(context.Background(), "tenant-123")

	require.NoError(t, err)

	manager.mu.RLock()
	_, connExists := manager.connections["tenant-123"]
	_, accessExists := manager.lastAccessed["tenant-123"]
	_, settingsCheckExists := manager.lastSettingsCheck["tenant-123"]
	manager.mu.RUnlock()

	assert.False(t, connExists, "connection should be removed after CloseConnection")
	assert.False(t, accessExists, "lastAccessed should be removed after CloseConnection")
	assert.False(t, settingsCheckExists, "lastSettingsCheck should be removed after CloseConnection")
}

func TestManager_Close_CleansUpLastSettingsCheck(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger",
		WithLogger(&mockLogger{}),
	)

	// Pre-populate cache with multiple tenants
	for _, id := range []string{"tenant-1", "tenant-2"} {
		db := &pingableDB{}
		var dbIface dbresolver.DB = db

		manager.connections[id] = &libPostgres.PostgresConnection{
			ConnectionDB: &dbIface,
		}
		manager.lastAccessed[id] = time.Now()
		manager.lastSettingsCheck[id] = time.Now()
	}

	err := manager.Close(context.Background())

	require.NoError(t, err)

	assert.Empty(t, manager.connections, "all connections should be removed after Close")
	assert.Empty(t, manager.lastAccessed, "all lastAccessed should be removed after Close")
	assert.Empty(t, manager.lastSettingsCheck, "all lastSettingsCheck should be removed after Close")
}

func TestManager_ApplyConnectionSettings_LogsValues(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})

	// Use a capturing logger to verify that ApplyConnectionSettings logs when it applies values
	capLogger := &capturingLogger{}
	manager := NewManager(c, "ledger",
		WithModule("onboarding"),
		WithLogger(capLogger),
	)

	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	manager.connections["tenant-123"] = &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}

	config := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 30,
					MaxIdleConns: 10,
				},
			},
		},
	}

	manager.ApplyConnectionSettings("tenant-123", config)

	assert.Equal(t, int32(30), tDB.MaxOpenConns())
	assert.Equal(t, int32(10), tDB.MaxIdleConns())
	assert.True(t, capLogger.containsSubstring("applying connection settings"),
		"ApplyConnectionSettings should log when applying values")
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
					"postgresql": {"host": "localhost", "port": 5432, "database": "testdb", "username": "user", "password": "pass"},
					"connectionSettings": {"maxOpenConns": 50, "maxIdleConns": 15}
				}
			}
		}`))
	}))
	defer server.Close()

	tmClient := client.NewClient(server.URL, &mockLogger{})
	manager := NewManager(tmClient, "ledger",
		WithLogger(&mockLogger{}),
		WithModule("onboarding"),
		// Disable revalidation with zero duration
		WithSettingsCheckInterval(0),
	)

	// Pre-populate cache with a healthy connection and an old settings check time
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastSettingsCheck to the past - but should NOT trigger revalidation since disabled
	manager.lastSettingsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	// Call GetConnection multiple times - should NOT spawn any goroutines
	for i := 0; i < 5; i++ {
		conn, err := manager.GetConnection(context.Background(), "tenant-123")

		require.NoError(t, err)
		assert.Equal(t, cachedConn, conn, "should return the cached connection")
	}

	// Wait to ensure no async goroutine fires
	time.Sleep(200 * time.Millisecond)

	// Verify that Tenant Manager was NEVER called (no revalidation)
	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount), "should NOT have fetched config - revalidation is disabled")

	// Verify that connection settings were NOT changed
	assert.Equal(t, int32(0), tDB.MaxOpenConns(), "maxOpenConns should NOT be changed")
	assert.Equal(t, int32(0), tDB.MaxIdleConns(), "maxIdleConns should NOT be changed")
}

func TestManager_GetConnection_DisabledRevalidation_WithNegative(t *testing.T) {
	t.Parallel()

	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-456",
			"tenantSlug": "test-tenant",
			"databases": {
				"payment": {
					"postgresql": {"host": "localhost", "port": 5432, "database": "testdb", "username": "user", "password": "pass"},
					"connectionSettings": {"maxOpenConns": 40, "maxIdleConns": 12}
				}
			}
		}`))
	}))
	defer server.Close()

	tmClient := client.NewClient(server.URL, &mockLogger{})
	manager := NewManager(tmClient, "payment",
		WithLogger(&mockLogger{}),
		WithModule("payment"),
		// Disable revalidation with negative duration
		WithSettingsCheckInterval(-5*time.Second),
	)

	// Pre-populate cache with a healthy connection
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-456"] = cachedConn
	manager.lastAccessed["tenant-456"] = time.Now()
	// Set lastSettingsCheck to the past
	manager.lastSettingsCheck["tenant-456"] = time.Now().Add(-1 * time.Hour)

	// Call GetConnection - should NOT trigger revalidation
	conn, err := manager.GetConnection(context.Background(), "tenant-456")

	require.NoError(t, err)
	assert.Equal(t, cachedConn, conn)

	// Wait to ensure no async goroutine fires
	time.Sleep(100 * time.Millisecond)

	// Verify that Tenant Manager was NOT called
	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount), "should NOT have fetched config - revalidation is disabled via negative interval")

	// Verify that connection settings were NOT changed
	assert.Equal(t, int32(0), tDB.MaxOpenConns(), "maxOpenConns should NOT be changed")
	assert.Equal(t, int32(0), tDB.MaxIdleConns(), "maxIdleConns should NOT be changed")
}

func TestManager_ApplyConnectionSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		module          string
		config          *core.TenantConfig
		hasCachedConn   bool
		hasConnectionDB bool
		expectMaxOpen   int
		expectMaxIdle   int
		expectNoChange  bool
	}{
		{
			name:   "applies module-level settings",
			module: "onboarding",
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns: 30,
							MaxIdleConns: 10,
						},
					},
				},
			},
			hasCachedConn:   true,
			hasConnectionDB: true,
			expectMaxOpen:   30,
			expectMaxIdle:   10,
		},
		{
			name:   "applies top-level settings as fallback",
			module: "onboarding",
			config: &core.TenantConfig{
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 20,
					MaxIdleConns: 8,
				},
			},
			hasCachedConn:   true,
			hasConnectionDB: true,
			expectMaxOpen:   20,
			expectMaxIdle:   8,
		},
		{
			name:   "module-level takes precedence over top-level",
			module: "onboarding",
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns: 50,
							MaxIdleConns: 15,
						},
					},
				},
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 20,
					MaxIdleConns: 8,
				},
			},
			hasCachedConn:   true,
			hasConnectionDB: true,
			expectMaxOpen:   50,
			expectMaxIdle:   15,
		},
		{
			name:           "no-op when no cached connection exists",
			module:         "onboarding",
			config:         &core.TenantConfig{},
			hasCachedConn:  false,
			expectNoChange: true,
		},
		{
			name:   "no-op when ConnectionDB is nil",
			module: "onboarding",
			config: &core.TenantConfig{
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns: 30,
				},
			},
			hasCachedConn:   true,
			hasConnectionDB: false,
			expectNoChange:  true,
		},
		{
			name:   "no-op when config has no connection settings",
			module: "onboarding",
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						PostgreSQL: &core.PostgreSQLConfig{Host: "localhost"},
					},
				},
			},
			hasCachedConn:   true,
			hasConnectionDB: true,
			expectNoChange:  true,
		},
		{
			name:   "applies only maxOpenConns when maxIdleConns is zero",
			module: "onboarding",
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns: 40,
							MaxIdleConns: 0,
						},
					},
				},
			},
			hasCachedConn:   true,
			hasConnectionDB: true,
			expectMaxOpen:   40,
			expectMaxIdle:   0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := client.NewClient("http://localhost:8080", &mockLogger{})
			manager := NewManager(c, "ledger",
				WithModule(tt.module),
				WithLogger(&mockLogger{}),
			)

			tDB := &trackingDB{}

			if tt.hasCachedConn {
				conn := &libPostgres.PostgresConnection{}
				if tt.hasConnectionDB {
					var db dbresolver.DB = tDB
					conn.ConnectionDB = &db
				}
				manager.connections["tenant-123"] = conn
			}

			manager.ApplyConnectionSettings("tenant-123", tt.config)

			if tt.expectNoChange {
				assert.Equal(t, int32(0), tDB.MaxOpenConns(),
					"maxOpenConns should not be changed")
				assert.Equal(t, int32(0), tDB.MaxIdleConns(),
					"maxIdleConns should not be changed")
			} else {
				assert.Equal(t, int32(tt.expectMaxOpen), tDB.MaxOpenConns(),
					"maxOpenConns mismatch")
				assert.Equal(t, int32(tt.expectMaxIdle), tDB.MaxIdleConns(),
					"maxIdleConns mismatch")
			}
		})
	}
}

func TestManager_Stats_ActiveConnections(t *testing.T) {
	t.Parallel()

	c := client.NewClient("http://localhost:8080", &mockLogger{})
	manager := NewManager(c, "ledger")

	// Pre-populate with connections
	for _, id := range []string{"tenant-1", "tenant-2", "tenant-3"} {
		db := &pingableDB{}
		var dbIface dbresolver.DB = db

		manager.connections[id] = &libPostgres.PostgresConnection{
			ConnectionDB: &dbIface,
		}
	}

	stats := manager.Stats()

	assert.Equal(t, 3, stats.TotalConnections)
	assert.Equal(t, 3, stats.ActiveConnections,
		"ActiveConnections should equal TotalConnections for postgres")
}
