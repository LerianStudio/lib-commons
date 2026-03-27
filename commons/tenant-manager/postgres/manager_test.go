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

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustNewTestClient creates a test client or fails the test immediately.
// Centralises the repeated client.NewClient + error-check boilerplate.
// Tests use httptest servers (http://), so WithAllowInsecureHTTP is applied.
func mustNewTestClient(t testing.TB, baseURL string) *client.Client {
	t.Helper()
	c, err := client.NewClient(baseURL, testutil.NewMockLogger(), client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)
	return c
}

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

// trackingDB extends pingableDB to track SetMaxOpenConns/SetMaxIdleConns calls
// and ExecContext queries (e.g., SET statement_timeout).
// Fields use int32 with atomic operations to avoid data races when written
// by async goroutines (revalidatePoolSettings) and read by test assertions.
type trackingDB struct {
	pingableDB
	maxOpenConns int32
	maxIdleConns int32
	mu           sync.Mutex
	execQueries  []string
}

func (t *trackingDB) SetMaxOpenConns(n int) { atomic.StoreInt32(&t.maxOpenConns, int32(n)) }
func (t *trackingDB) SetMaxIdleConns(n int) { atomic.StoreInt32(&t.maxIdleConns, int32(n)) }
func (t *trackingDB) MaxOpenConns() int32   { return atomic.LoadInt32(&t.maxOpenConns) }
func (t *trackingDB) MaxIdleConns() int32   { return atomic.LoadInt32(&t.maxIdleConns) }

func (t *trackingDB) ExecContext(_ context.Context, query string, _ ...interface{}) (sql.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.execQueries = append(t.execQueries, query)

	return nil, nil
}

// ExecQueries returns a thread-safe copy of all executed queries.
func (t *trackingDB) ExecQueries() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	copied := make([]string, len(t.execQueries))
	copy(copied, t.execQueries)

	return copied
}

func TestNewManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		c := mustNewTestClient(t, "http://localhost:8080")
		manager := NewManager(c, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
	})
}

func TestManager_GetConnection_NoTenantID(t *testing.T) {
	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger")

	_, err := manager.GetConnection(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestManager_Close(t *testing.T) {
	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger")

	err := manager.Close(context.Background())

	assert.NoError(t, err)
	assert.True(t, manager.closed)
}

func TestManager_GetConnection_ManagerClosed(t *testing.T) {
	c := mustNewTestClient(t, "http://localhost:8080")
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
			expected: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
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
			expected: "postgres://user:pass@localhost:5432/testdb?options=-csearch_path%3Dtenant_abc&sslmode=disable",
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
			expected: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
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
			expected: "postgres://user:pass@localhost:5432/testdb?sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := buildConnectionString(tt.cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildConnectionString_SSLCertificates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *core.PostgreSQLConfig
		contains []string
		excludes []string
	}{
		{
			name: "adds sslrootcert when SSLRootCert is set",
			cfg: &core.PostgreSQLConfig{
				Host:        "localhost",
				Port:        5432,
				Username:    "user",
				Password:    "pass",
				Database:    "testdb",
				SSLMode:     "verify-full",
				SSLRootCert: "/etc/ssl/ca.pem",
			},
			contains: []string{"sslmode=verify-full", "sslrootcert=%2Fetc%2Fssl%2Fca.pem"},
		},
		{
			name: "adds sslcert and sslkey when both are set",
			cfg: &core.PostgreSQLConfig{
				Host:        "localhost",
				Port:        5432,
				Username:    "user",
				Password:    "pass",
				Database:    "testdb",
				SSLMode:     "verify-full",
				SSLRootCert: "/etc/ssl/ca.pem",
				SSLCert:     "/etc/ssl/client-cert.pem",
				SSLKey:      "/etc/ssl/client-key.pem",
			},
			contains: []string{
				"sslmode=verify-full",
				"sslrootcert=%2Fetc%2Fssl%2Fca.pem",
				"sslcert=%2Fetc%2Fssl%2Fclient-cert.pem",
				"sslkey=%2Fetc%2Fssl%2Fclient-key.pem",
			},
		},
		{
			name: "does not add ssl cert params when not set",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "require",
			},
			contains: []string{"sslmode=require"},
			excludes: []string{"sslrootcert", "sslcert=", "sslkey="},
		},
		{
			name: "adds only sslrootcert without client certs",
			cfg: &core.PostgreSQLConfig{
				Host:        "localhost",
				Port:        5432,
				Username:    "user",
				Password:    "pass",
				Database:    "testdb",
				SSLMode:     "verify-ca",
				SSLRootCert: "/etc/ssl/ca.pem",
			},
			contains: []string{"sslmode=verify-ca", "sslrootcert=%2Fetc%2Fssl%2Fca.pem"},
			excludes: []string{"sslcert=", "sslkey="},
		},
		{
			name: "ssl cert params work with schema mode",
			cfg: &core.PostgreSQLConfig{
				Host:        "localhost",
				Port:        5432,
				Username:    "user",
				Password:    "pass",
				Database:    "testdb",
				SSLMode:     "verify-full",
				SSLRootCert: "/etc/ssl/ca.pem",
				Schema:      "tenant_abc",
			},
			contains: []string{
				"sslmode=verify-full",
				"sslrootcert=%2Fetc%2Fssl%2Fca.pem",
				"options=-csearch_path%3Dtenant_abc",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := buildConnectionString(tt.cfg)

			require.NoError(t, err)

			for _, s := range tt.contains {
				assert.Contains(t, result, s, "connection string should contain %q", s)
			}

			for _, s := range tt.excludes {
				assert.NotContains(t, result, s, "connection string should NOT contain %q", s)
			}
		})
	}
}

func TestBuildConnectionString_SSLModeDisableWithCerts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *core.PostgreSQLConfig
	}{
		{
			name: "rejects sslmode=disable with SSLRootCert set",
			cfg: &core.PostgreSQLConfig{
				Host:        "localhost",
				Port:        5432,
				Username:    "user",
				Password:    "pass",
				Database:    "testdb",
				SSLMode:     "disable",
				SSLRootCert: "/etc/ssl/ca.pem",
			},
		},
		{
			name: "rejects sslmode=disable with SSLCert set",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "disable",
				SSLCert:  "/etc/ssl/client-cert.pem",
			},
		},
		{
			name: "rejects sslmode=disable with SSLKey set",
			cfg: &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "disable",
				SSLKey:   "/etc/ssl/client-key.pem",
			},
		},
		{
			name: "rejects sslmode=disable with all SSL cert fields set",
			cfg: &core.PostgreSQLConfig{
				Host:        "localhost",
				Port:        5432,
				Username:    "user",
				Password:    "pass",
				Database:    "testdb",
				SSLMode:     "disable",
				SSLRootCert: "/etc/ssl/ca.pem",
				SSLCert:     "/etc/ssl/client-cert.pem",
				SSLKey:      "/etc/ssl/client-key.pem",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := buildConnectionString(tt.cfg)

			require.Error(t, err)
			assert.Empty(t, result)
			assert.Contains(t, err.Error(), "sslmode is \"disable\" but SSL certificate parameters are set")
		})
	}

	t.Run("allows sslmode=disable without cert fields", func(t *testing.T) {
		t.Parallel()

		cfg := &core.PostgreSQLConfig{
			Host:     "localhost",
			Port:     5432,
			Username: "user",
			Password: "pass",
			Database: "testdb",
			SSLMode:  "disable",
		}

		result, err := buildConnectionString(cfg)

		require.NoError(t, err)
		assert.Contains(t, result, "sslmode=disable")
	})
}

func TestBuildConnectionString_InvalidSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{
			name:   "rejects schema with SQL injection attempt",
			schema: "public; DROP TABLE users--",
		},
		{
			name:   "rejects schema with spaces",
			schema: "my schema",
		},
		{
			name:   "rejects schema with special characters",
			schema: "tenant-abc",
		},
		{
			name:   "rejects schema starting with a digit",
			schema: "1tenant",
		},
		{
			name:   "rejects schema with double quotes",
			schema: `"public"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &core.PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				Schema:   tt.schema,
			}

			result, err := buildConnectionString(cfg)

			require.Error(t, err)
			assert.Empty(t, result)
			assert.Contains(t, err.Error(), "invalid schema name")
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

		primaryConnStr, err := buildConnectionString(primaryConfig)
		require.NoError(t, err)
		replicaConnStr, err := buildConnectionString(replicaConfig)
		require.NoError(t, err)

		assert.Contains(t, primaryConnStr, "postgres://user:pass@primary-host:5432/")
		assert.Contains(t, replicaConnStr, "postgres://user:pass@replica-host:5433/")
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
		primaryConnStr, err := buildConnectionString(pgConfig)
		require.NoError(t, err)

		replicaConnStr := primaryConnStr
		if pgReplicaConfig != nil {
			var replicaErr error
			replicaConnStr, replicaErr = buildConnectionString(pgReplicaConfig)
			require.NoError(t, replicaErr)
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

		primaryConnStr, err := buildConnectionString(pgConfig)
		require.NoError(t, err)

		replicaConnStr := primaryConnStr
		if pgReplicaConfig != nil {
			var replicaErr error
			replicaConnStr, replicaErr = buildConnectionString(pgReplicaConfig)
			require.NoError(t, replicaErr)
		}

		assert.NotEqual(t, primaryConnStr, replicaConnStr)
		assert.Contains(t, primaryConnStr, "postgres://user:pass@primary-host:5432/")
		assert.Contains(t, replicaConnStr, "postgres://user:pass@replica-host:5433/")
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
		c := mustNewTestClient(t, "http://localhost:8080")
		manager := NewManager(c, "ledger")

		// Pre-populate cache with a healthy connection
		healthyDB := &pingableDB{pingErr: nil}
		var db dbresolver.DB = healthyDB

		cachedConn := &PostgresConnection{
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

		tmClient := mustNewTestClient(t, server.URL)
		manager := NewManager(tmClient, "ledger", WithLogger(testutil.NewMockLogger()))

		// Pre-populate cache with an unhealthy connection (simulates auth failure after credential rotation)
		unhealthyDB := &pingableDB{pingErr: errors.New("FATAL: password authentication failed (SQLSTATE 28P01)")}
		var db dbresolver.DB = unhealthyDB

		cachedConn := &PostgresConnection{
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

		tmClient := mustNewTestClient(t, server.URL)
		manager := NewManager(tmClient, "ledger", WithLogger(testutil.NewMockLogger()))

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
		c := mustNewTestClient(t, "http://localhost:8080")
		manager := NewManager(c, "ledger")

		// Pre-populate cache with a connection that has nil ConnectionDB
		cachedConn := &PostgresConnection{
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
				WithLogger(testutil.NewMockLogger()),
				WithMaxTenantPools(tt.maxConnections),
			}
			if tt.idleTimeout > 0 {
				opts = append(opts, WithIdleTimeout(tt.idleTimeout))
			}

			c := mustNewTestClient(t, "http://localhost:8080")
			manager := NewManager(c, "ledger", opts...)

			// Pre-populate pool with connections
			if tt.preloadCount >= 1 {
				oldDB := &pingableDB{}
				var oldDBIface dbresolver.DB = oldDB

				manager.connections["tenant-old"] = &PostgresConnection{
					ConnectionDB: &oldDBIface,
				}
				manager.lastAccessed["tenant-old"] = time.Now().Add(-tt.oldTenantAge)
			}

			if tt.preloadCount >= 2 {
				newDB := &pingableDB{}
				var newDBIface dbresolver.DB = newDB

				manager.connections["tenant-new"] = &PostgresConnection{
					ConnectionDB: &newDBIface,
				}
				manager.lastAccessed["tenant-new"] = time.Now().Add(-tt.newTenantAge)
			}

			// For unlimited test, add more connections
			for i := 2; i < tt.preloadCount; i++ {
				db := &pingableDB{}
				var dbIface dbresolver.DB = db

				id := "tenant-extra-" + time.Now().Add(time.Duration(i)*time.Second).Format("150405")
				manager.connections[id] = &PostgresConnection{
					ConnectionDB: &dbIface,
				}
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

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxTenantPools(2),
		WithIdleTimeout(5*time.Minute),
	)

	// Pre-populate with 2 connections, both accessed recently (within idle timeout)
	for _, id := range []string{"tenant-1", "tenant-2"} {
		db := &pingableDB{}
		var dbIface dbresolver.DB = db

		manager.connections[id] = &PostgresConnection{
			ConnectionDB: &dbIface,
		}
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
	db := &pingableDB{}
	var dbIface dbresolver.DB = db

	manager.connections["tenant-3"] = &PostgresConnection{
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

			c := mustNewTestClient(t, "http://localhost:8080")
			manager := NewManager(c, "ledger",
				WithIdleTimeout(tt.idleTimeout),
			)

			assert.Equal(t, tt.expectedTimeout, manager.idleTimeout)
		})
	}
}

func TestManager_LRU_LastAccessedUpdatedOnCacheHit(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithMaxTenantPools(5),
	)

	// Pre-populate cache with a healthy connection
	healthyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = healthyDB

	cachedConn := &PostgresConnection{
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

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache
	healthyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = healthyDB

	manager.connections["tenant-123"] = &PostgresConnection{
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

			c := mustNewTestClient(t, "http://localhost:8080")
			manager := NewManager(c, "ledger",
				WithMaxTenantPools(tt.maxConnections),
			)

			assert.Equal(t, tt.expectedMax, manager.maxConnections)
		})
	}
}

func TestManager_Stats_IncludesMaxConnections(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithMaxTenantPools(50),
	)

	stats := manager.Stats()

	assert.Equal(t, 50, stats.MaxConnections)
	assert.Equal(t, 0, stats.TotalConnections)
	assert.Equal(t, 0, stats.ActiveConnections)
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

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		// Use a very short interval so the test triggers revalidation immediately
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate cache with a healthy connection and an old settings check time.
	// The ConnectionStringPrimary must match the fresh config so that
	// detectAndReconnectPostgres sees no config change and falls through
	// to ApplyConnectionSettings (the pool-settings-only path).
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		ConnectionDB:            &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastConnectionsCheck to a time well in the past so revalidation triggers
	manager.lastConnectionsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

	// Call GetConnection - should return cached conn AND trigger async revalidation
	conn, err := manager.GetConnection(context.Background(), "tenant-123")

	require.NoError(t, err)
	assert.Equal(t, cachedConn, conn, "should return the cached connection")

	assert.Eventually(t, func() bool {
		return atomic.LoadInt32(&callCount) > 0
	}, 500*time.Millisecond, 20*time.Millisecond, "should have fetched fresh config from Tenant Manager")

	assert.Eventually(t, func() bool {
		return tDB.MaxOpenConns() == int32(50) && tDB.MaxIdleConns() == int32(15)
	}, 500*time.Millisecond, 20*time.Millisecond, "connection settings should be updated from async revalidation")
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

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		// Use a very long interval so revalidation does NOT trigger
		WithConnectionsCheckInterval(1*time.Hour),
	)

	// Pre-populate cache with a healthy connection and a recent settings check time
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastConnectionsCheck to now - should NOT trigger revalidation
	manager.lastConnectionsCheck["tenant-123"] = time.Now()

	// Call GetConnection - should return cached conn without revalidation
	conn, err := manager.GetConnection(context.Background(), "tenant-123")

	require.NoError(t, err)
	assert.Equal(t, cachedConn, conn)

	assert.Never(t, func() bool {
		return atomic.LoadInt32(&callCount) > 0
	}, 200*time.Millisecond, 20*time.Millisecond, "should NOT have fetched config - interval not elapsed")

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

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		WithConnectionsCheckInterval(1*time.Millisecond),
	)

	// Pre-populate cache with a healthy connection
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastConnectionsCheck to the past so revalidation triggers
	manager.lastConnectionsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

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

func TestManager_CloseConnection_CleansUpLastConnectionsCheck(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	// Pre-populate cache
	healthyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = healthyDB

	manager.connections["tenant-123"] = &PostgresConnection{
		ConnectionDB: &db,
	}
	manager.lastAccessed["tenant-123"] = time.Now()
	manager.lastConnectionsCheck["tenant-123"] = time.Now()

	// Close the specific tenant connection
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
		db := &pingableDB{}
		var dbIface dbresolver.DB = db

		manager.connections[id] = &PostgresConnection{
			ConnectionDB: &dbIface,
		}
		manager.lastAccessed[id] = time.Now()
		manager.lastConnectionsCheck[id] = time.Now()
		manager.lastAppliedSettings[id] = appliedSettings{maxOpenConns: 25, maxIdleConns: 5}
	}

	err := manager.Close(context.Background())

	require.NoError(t, err)

	assert.Empty(t, manager.connections, "all connections should be removed after Close")
	assert.Empty(t, manager.lastAccessed, "all lastAccessed should be removed after Close")
	assert.Empty(t, manager.lastConnectionsCheck, "all lastConnectionsCheck should be removed after Close")
	assert.Empty(t, manager.lastAppliedSettings, "all lastAppliedSettings should be removed after Close")
}

func TestManager_ApplyConnectionSettings_LogsValues(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")

	// Use a capturing logger to verify that ApplyConnectionSettings logs when it applies values
	capLogger := testutil.NewCapturingLogger()
	manager := NewManager(c, "ledger",
		WithModule("onboarding"),
		WithLogger(capLogger),
	)

	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	manager.connections["tenant-123"] = &PostgresConnection{
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
	assert.True(t, capLogger.ContainsSubstring("applying connection settings"),
		"ApplyConnectionSettings should log on first apply")
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

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithModule("onboarding"),
		// Disable revalidation with zero duration
		WithConnectionsCheckInterval(0),
	)

	// Pre-populate cache with a healthy connection and an old settings check time
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-123"] = cachedConn
	manager.lastAccessed["tenant-123"] = time.Now()
	// Set lastConnectionsCheck to the past - but should NOT trigger revalidation since disabled
	manager.lastConnectionsCheck["tenant-123"] = time.Now().Add(-1 * time.Hour)

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

	tmClient := mustNewTestClient(t, server.URL)
	manager := NewManager(tmClient, "payment",
		WithLogger(testutil.NewMockLogger()),
		WithModule("payment"),
		// Disable revalidation with negative duration
		WithConnectionsCheckInterval(-5*time.Second),
	)

	// Pre-populate cache with a healthy connection
	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	cachedConn := &PostgresConnection{
		ConnectionDB: &db,
	}
	manager.connections["tenant-456"] = cachedConn
	manager.lastAccessed["tenant-456"] = time.Now()
	// Set lastConnectionsCheck to the past
	manager.lastConnectionsCheck["tenant-456"] = time.Now().Add(-1 * time.Hour)

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
			name:   "applies manager defaults when config has no connection settings",
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
			expectMaxOpen:   fallbackMaxOpenConns, // manager default when no settings present
			expectMaxIdle:   fallbackMaxIdleConns, // manager default when no settings present
		},
		{
			name:   "falls back to manager default idle conns when maxIdleConns is zero",
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
			expectMaxIdle:   fallbackMaxIdleConns, // falls back to manager default
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t, "http://localhost:8080")
			manager := NewManager(c, "ledger",
				WithModule(tt.module),
				WithLogger(testutil.NewMockLogger()),
			)

			tDB := &trackingDB{}

			if tt.hasCachedConn {
				conn := &PostgresConnection{}
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

func TestManager_ApplyConnectionSettings_ChangeDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		preApplySettings    *appliedSettings // nil = no previous settings (first apply)
		config              *core.TenantConfig
		expectSetMaxOpen    bool
		expectSetMaxIdle    bool
		expectLog           bool
		expectLogSubstring  string
		noLogSubstring      string
		expectExecQuery     string // expected SET statement_timeout query
		expectNoExecQueries bool
	}{
		{
			name:             "first_apply_logs_and_applies_all",
			preApplySettings: nil,
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns:     30,
							MaxIdleConns:     10,
							StatementTimeout: "5s",
						},
					},
				},
			},
			expectSetMaxOpen:   true,
			expectSetMaxIdle:   true,
			expectLog:          true,
			expectLogSubstring: "applying connection settings",
			expectExecQuery:    "SET statement_timeout = '5s'",
		},
		{
			name: "no_change_skips_apply_and_log",
			preApplySettings: &appliedSettings{
				maxOpenConns:     30,
				maxIdleConns:     10,
				statementTimeout: "5s",
			},
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns:     30,
							MaxIdleConns:     10,
							StatementTimeout: "5s",
						},
					},
				},
			},
			expectSetMaxOpen:    false,
			expectSetMaxIdle:    false,
			expectLog:           false,
			expectNoExecQueries: true,
		},
		{
			name: "maxOpenConns_change_logs_delta",
			preApplySettings: &appliedSettings{
				maxOpenConns:     30,
				maxIdleConns:     10,
				statementTimeout: "5s",
			},
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns:     50,
							MaxIdleConns:     10,
							StatementTimeout: "5s",
						},
					},
				},
			},
			expectSetMaxOpen:    true,
			expectSetMaxIdle:    false,
			expectLog:           true,
			expectLogSubstring:  "maxOpenConns 30->50",
			noLogSubstring:      "maxIdleConns",
			expectNoExecQueries: true,
		},
		{
			name: "statementTimeout_change_executes_SET",
			preApplySettings: &appliedSettings{
				maxOpenConns:     30,
				maxIdleConns:     10,
				statementTimeout: "5s",
			},
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns:     30,
							MaxIdleConns:     10,
							StatementTimeout: "10s",
						},
					},
				},
			},
			expectSetMaxOpen:   false,
			expectSetMaxIdle:   false,
			expectLog:          true,
			expectLogSubstring: "statementTimeout 5s->10s",
			expectExecQuery:    "SET statement_timeout = '10s'",
		},
		{
			name: "statementTimeout_empty_skips_exec",
			preApplySettings: &appliedSettings{
				maxOpenConns:     30,
				maxIdleConns:     10,
				statementTimeout: "",
			},
			config: &core.TenantConfig{
				Databases: map[string]core.DatabaseConfig{
					"onboarding": {
						ConnectionSettings: &core.ConnectionSettings{
							MaxOpenConns: 50,
							MaxIdleConns: 10,
						},
					},
				},
			},
			expectSetMaxOpen:    true,
			expectSetMaxIdle:    false,
			expectLog:           true,
			expectNoExecQueries: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t, "http://localhost:8080")
			capLogger := testutil.NewCapturingLogger()
			manager := NewManager(c, "ledger",
				WithModule("onboarding"),
				WithLogger(capLogger),
			)

			tDB := &trackingDB{}
			var db dbresolver.DB = tDB

			manager.connections["tenant-123"] = &PostgresConnection{
				ConnectionDB: &db,
			}

			if tt.preApplySettings != nil {
				manager.lastAppliedSettings["tenant-123"] = *tt.preApplySettings
			}

			manager.ApplyConnectionSettings("tenant-123", tt.config)

			if tt.expectSetMaxOpen {
				assert.NotEqual(t, int32(0), tDB.MaxOpenConns(),
					"maxOpenConns should have been set")
			}

			if tt.expectSetMaxIdle {
				assert.NotEqual(t, int32(0), tDB.MaxIdleConns(),
					"maxIdleConns should have been set")
			}

			if tt.expectLog {
				assert.True(t, capLogger.ContainsSubstring(tt.expectLogSubstring),
					"expected log message containing %q, got: %v", tt.expectLogSubstring, capLogger.GetMessages())
			} else {
				// When no log is expected, verify the capturing logger got nothing
				// (other than potential clamping warnings which we don't trigger here)
				for _, msg := range capLogger.GetMessages() {
					assert.False(t, strings.Contains(msg, "applying connection settings") || strings.Contains(msg, "connection settings changed"),
						"unexpected log message: %s", msg)
				}
			}

			if tt.noLogSubstring != "" {
				assert.False(t, capLogger.ContainsSubstring(tt.noLogSubstring),
					"log should NOT contain %q, got: %v", tt.noLogSubstring, capLogger.GetMessages())
			}

			queries := tDB.ExecQueries()
			if tt.expectNoExecQueries {
				assert.Empty(t, queries, "no ExecContext queries expected")
			}

			if tt.expectExecQuery != "" {
				assert.Contains(t, queries, tt.expectExecQuery,
					"expected ExecContext query %q", tt.expectExecQuery)
			}
		})
	}
}

func TestManager_ApplyConnectionSettings_StatementTimeout(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	capLogger := testutil.NewCapturingLogger()
	manager := NewManager(c, "ledger",
		WithModule("onboarding"),
		WithLogger(capLogger),
	)

	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	manager.connections["tenant-123"] = &PostgresConnection{
		ConnectionDB: &db,
	}

	// First apply with statementTimeout
	config := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns:     30,
					MaxIdleConns:     10,
					StatementTimeout: "5s",
				},
			},
		},
	}

	manager.ApplyConnectionSettings("tenant-123", config)

	queries := tDB.ExecQueries()
	require.Len(t, queries, 1, "should execute SET statement_timeout on first apply")
	assert.Equal(t, "SET statement_timeout = '5s'", queries[0])

	// Second apply with same settings — no ExecContext
	capLogger.Clear()
	manager.ApplyConnectionSettings("tenant-123", config)

	assert.Len(t, tDB.ExecQueries(), 1, "should NOT execute SET again when statementTimeout unchanged")

	// Third apply with changed statementTimeout
	config.Databases["onboarding"] = core.DatabaseConfig{
		ConnectionSettings: &core.ConnectionSettings{
			MaxOpenConns:     30,
			MaxIdleConns:     10,
			StatementTimeout: "15s",
		},
	}

	manager.ApplyConnectionSettings("tenant-123", config)

	queries = tDB.ExecQueries()
	require.Len(t, queries, 2, "should execute SET again when statementTimeout changed")
	assert.Equal(t, "SET statement_timeout = '15s'", queries[1])
	assert.True(t, capLogger.ContainsSubstring("statementTimeout 5s->15s"),
		"should log the statementTimeout change")
}

func TestValidStatementTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "plain_integer_milliseconds", value: "30000", valid: true},
		{name: "zero", value: "0", valid: true},
		{name: "with_ms_suffix", value: "500ms", valid: true},
		{name: "with_s_suffix", value: "30s", valid: true},
		{name: "with_min_suffix", value: "2min", valid: true},
		{name: "with_h_suffix", value: "1h", valid: true},
		{name: "with_d_suffix", value: "7d", valid: true},
		{name: "with_us_suffix", value: "100us", valid: true},
		{name: "sql_injection_attempt", value: "'; DROP TABLE users; --", valid: false},
		{name: "negative_value", value: "-1", valid: false},
		{name: "empty_string", value: "", valid: false},
		{name: "letters_only", value: "abc", valid: false},
		{name: "invalid_unit", value: "30x", valid: false},
		{name: "special_chars", value: "30; DROP", valid: false},
		{name: "quoted_value", value: "'30s'", valid: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.valid, validStatementTimeout(tt.value),
				"validStatementTimeout(%q) = %v, want %v", tt.value, !tt.valid, tt.valid)
		})
	}
}

func TestManager_ApplyConnectionSettings_RejectsInvalidStatementTimeout(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	capLogger := testutil.NewCapturingLogger()
	manager := NewManager(c, "ledger",
		WithModule("onboarding"),
		WithLogger(capLogger),
	)

	tDB := &trackingDB{}
	var db dbresolver.DB = tDB

	manager.connections["tenant-123"] = &PostgresConnection{
		ConnectionDB: &db,
	}

	config := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				ConnectionSettings: &core.ConnectionSettings{
					MaxOpenConns:     30,
					MaxIdleConns:     10,
					StatementTimeout: "'; DROP TABLE users; --",
				},
			},
		},
	}

	manager.ApplyConnectionSettings("tenant-123", config)

	queries := tDB.ExecQueries()
	assert.Empty(t, queries, "should NOT execute SET with invalid statement_timeout value")
	assert.True(t, capLogger.ContainsSubstring("invalid statement_timeout"),
		"should log warning about invalid statement_timeout value")
}

func TestManager_CloseConnection_CleansUpLastAppliedSettings(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger",
		WithLogger(testutil.NewMockLogger()),
	)

	db := &pingableDB{}
	var dbIface dbresolver.DB = db

	manager.connections["tenant-123"] = &PostgresConnection{
		ConnectionDB: &dbIface,
	}
	manager.lastAppliedSettings["tenant-123"] = appliedSettings{maxOpenConns: 25, maxIdleConns: 5}

	err := manager.CloseConnection(context.Background(), "tenant-123")
	require.NoError(t, err)

	assert.Empty(t, manager.lastAppliedSettings, "lastAppliedSettings should be cleaned up after CloseConnection")
}

func TestManager_Stats_ActiveConnections(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	manager := NewManager(c, "ledger")

	// Pre-populate with connections and mark them as recently accessed
	now := time.Now()
	for _, id := range []string{"tenant-1", "tenant-2", "tenant-3"} {
		db := &pingableDB{}
		var dbIface dbresolver.DB = db

		manager.connections[id] = &PostgresConnection{
			ConnectionDB: &dbIface,
		}
		manager.lastAccessed[id] = now
	}

	stats := manager.Stats()

	assert.Equal(t, 3, stats.TotalConnections)
	assert.Equal(t, 3, stats.ActiveConnections,
		"ActiveConnections should equal TotalConnections for postgres")
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

			// Set up a mock Tenant Manager that returns 403 with TenantSuspendedError body
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

			// Pre-populate a cached connection for the tenant
			mockDB := &pingableDB{}
			var dbIface dbresolver.DB = mockDB

			manager.connections["tenant-suspended"] = &PostgresConnection{
				ConnectionDB: &dbIface,
			}
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

				// Verify the DB was closed
				assert.True(t, mockDB.closed,
					"cached connection's DB should have been closed")

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
						"postgresql": {"host": "localhost", "port": 5432, "database": "testdb", "username": "user", "password": "pass"}
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

	mockDB := &pingableDB{}
	var dbIface dbresolver.DB = mockDB

	manager.connections["tenant-cache-test"] = &PostgresConnection{ConnectionDB: &dbIface}
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

	// Verify the DB was closed
	assert.True(t, mockDB.closed,
		"cached connection's DB should have been closed on eviction")
}

func TestManager_RevalidateSettings_DetectsConfigChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		cachedConnStr     string
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
			cachedConnStr:     "postgres://user:pass@oldhost:5432/testdb?sslmode=disable",
			freshHost:         "newhost",
			freshPort:         5432,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "PostgreSQL config changed",
		},
		{
			name:              "reconnects when port changes",
			cachedConnStr:     "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
			freshHost:         "localhost",
			freshPort:         5433,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "PostgreSQL config changed",
		},
		{
			name:              "reconnects when database changes",
			cachedConnStr:     "postgres://user:pass@localhost:5432/olddb?sslmode=disable",
			freshHost:         "localhost",
			freshPort:         5432,
			freshDB:           "newdb",
			freshUser:         "user",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "PostgreSQL config changed",
		},
		{
			name:              "reconnects when username changes",
			cachedConnStr:     "postgres://olduser:pass@localhost:5432/testdb?sslmode=disable",
			freshHost:         "localhost",
			freshPort:         5432,
			freshDB:           "testdb",
			freshUser:         "newuser",
			freshPass:         "pass",
			expectReconnect:   true,
			expectLogContains: "PostgreSQL config changed",
		},
		{
			name:              "reconnects when password changes",
			cachedConnStr:     "postgres://user:oldpass@localhost:5432/testdb?sslmode=disable",
			freshHost:         "localhost",
			freshPort:         5432,
			freshDB:           "testdb",
			freshUser:         "user",
			freshPass:         "newpass",
			expectReconnect:   true,
			expectLogContains: "PostgreSQL config changed",
		},
		{
			name:              "no reconnect when config is identical",
			cachedConnStr:     "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
			freshHost:         "localhost",
			freshPort:         5432,
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
							"postgresql": {"host": %q, "port": %d, "database": %q, "username": %q, "password": %q}
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

			mockDB := &pingableDB{}
			var dbIface dbresolver.DB = mockDB

			manager.connections["tenant-cfg"] = &PostgresConnection{
				ConnectionStringPrimary: tt.cachedConnStr,
				ConnectionDB:            &dbIface,
			}
			manager.lastAccessed["tenant-cfg"] = time.Now()
			manager.lastConnectionsCheck["tenant-cfg"] = time.Now()

			// Trigger revalidation directly
			manager.revalidatePoolSettings("tenant-cfg")

			if tt.expectReconnect {
				// The reconnection will fail (no real DB) but the old conn should
				// be kept. Verify the log message was produced.
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

	// Mock server returns a config with a different host (config change detected)
	// but the new host is unreachable. The old connection should be kept.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-fail",
			"tenantSlug": "fail-test",
			"databases": {
				"onboarding": {
					"postgresql": {"host": "unreachable-host", "port": 5432, "database": "testdb", "username": "user", "password": "pass"}
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

	mockDB := &pingableDB{}
	var dbIface dbresolver.DB = mockDB

	manager.connections["tenant-fail"] = &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		ConnectionDB:            &dbIface,
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

	// Mock server returns the SAME config as the cached connection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "tenant-same",
			"tenantSlug": "same-test",
			"databases": {
				"onboarding": {
					"postgresql": {"host": "localhost", "port": 5432, "database": "testdb", "username": "user", "password": "pass"},
					"connectionSettings": {"maxOpenConns": 30, "maxIdleConns": 10}
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

	tDB := &trackingDB{}
	var dbIface dbresolver.DB = tDB

	manager.connections["tenant-same"] = &PostgresConnection{
		ConnectionStringPrimary: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		ConnectionDB:            &dbIface,
	}
	manager.lastAccessed["tenant-same"] = time.Now()
	manager.lastConnectionsCheck["tenant-same"] = time.Now()

	// Trigger revalidation directly
	manager.revalidatePoolSettings("tenant-same")

	// Should NOT log any config change
	assert.False(t, capLogger.ContainsSubstring("config changed"),
		"should NOT log config change when config is identical")

	// Pool settings should still be applied (ApplyConnectionSettings path)
	assert.Eventually(t, func() bool {
		return tDB.MaxOpenConns() == int32(30) && tDB.MaxIdleConns() == int32(10)
	}, 500*time.Millisecond, 20*time.Millisecond, "pool settings should be applied even without config change")
}
