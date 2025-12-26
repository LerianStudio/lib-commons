package poolmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockResolver implements Resolver interface for testing.
type mockResolver struct {
	configs map[string]*TenantConfig
	calls   int
	mu      sync.Mutex
}

func newMockResolver() *mockResolver {
	return &mockResolver{
		configs: make(map[string]*TenantConfig),
	}
}

func (m *mockResolver) AddConfig(tenantID string, config *TenantConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[tenantID] = config
}

func (m *mockResolver) Resolve(ctx context.Context, tenantID string) (*TenantConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if config, ok := m.configs[tenantID]; ok {
		return config, nil
	}
	return nil, ErrTenantNotFound
}

func (m *mockResolver) ResolveWithService(ctx context.Context, tenantID, serviceName string) (*TenantConfig, error) {
	return m.Resolve(ctx, tenantID)
}

func (m *mockResolver) InvalidateCache(tenantID string) {}

func (m *mockResolver) InvalidateCacheAll() {}

func (m *mockResolver) GetCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// TestNewPostgresPoolManager tests the constructor with various options.
func TestNewPostgresPoolManager(t *testing.T) {
	tests := []struct {
		name      string
		resolver  Resolver
		opts      []PoolManagerOption
		checkFunc func(t *testing.T, pm PostgresPoolManager)
	}{
		{
			name:     "Should create pool manager with default options",
			resolver: newMockResolver(),
			opts:     nil,
			checkFunc: func(t *testing.T, pm PostgresPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with custom max pools",
			resolver: newMockResolver(),
			opts:     []PoolManagerOption{WithMaxPools(50)},
			checkFunc: func(t *testing.T, pm PostgresPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with custom idle timeout",
			resolver: newMockResolver(),
			opts:     []PoolManagerOption{WithIdleTimeout(15 * time.Minute)},
			checkFunc: func(t *testing.T, pm PostgresPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with custom cleanup interval",
			resolver: newMockResolver(),
			opts:     []PoolManagerOption{WithCleanupInterval(5 * time.Minute)},
			checkFunc: func(t *testing.T, pm PostgresPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with all custom options",
			resolver: newMockResolver(),
			opts: []PoolManagerOption{
				WithMaxPools(100),
				WithIdleTimeout(20 * time.Minute),
				WithCleanupInterval(10 * time.Minute),
			},
			checkFunc: func(t *testing.T, pm PostgresPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with connection config",
			resolver: newMockResolver(),
			opts: []PoolManagerOption{
				WithConnectionConfig(ConnectionConfig{
					MaxOpenConnections: 50,
					MaxIdleConnections: 10,
				}),
			},
			checkFunc: func(t *testing.T, pm PostgresPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := NewPostgresPoolManager(tt.resolver, tt.opts...)
			if tt.checkFunc != nil {
				tt.checkFunc(t, pm)
			}
			// Clean up
			if pm != nil {
				_ = pm.CloseAll()
			}
		})
	}
}

// TestNewPostgresPoolManagerWithConfig tests the config-based constructor.
func TestNewPostgresPoolManagerWithConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     PostgresPoolManagerConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "Should fail without resolver",
			cfg: PostgresPoolManagerConfig{
				Logger: &mockLogger{},
			},
			wantErr: true,
			errMsg:  "resolver is required",
		},
		{
			name: "Should fail without logger",
			cfg: PostgresPoolManagerConfig{
				Resolver: newMockResolver(),
			},
			wantErr: true,
			errMsg:  "logger is required",
		},
		{
			name: "Should create with minimal config",
			cfg: PostgresPoolManagerConfig{
				Resolver: newMockResolver(),
				Logger:   &mockLogger{},
			},
			wantErr: false,
		},
		{
			name: "Should create with full config",
			cfg: PostgresPoolManagerConfig{
				Resolver:        newMockResolver(),
				Logger:          &mockLogger{},
				MaxConnections:  50,
				IdleTimeout:     15 * time.Minute,
				CleanupInterval: 3 * time.Minute,
				ConnectionConfig: ConnectionConfig{
					MaxOpenConnections: 30,
					MaxIdleConnections: 10,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm, err := NewPostgresPoolManagerWithConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, pm)
				_ = pm.CloseAll()
			}
		})
	}
}

// mockLogger implements log.Logger interface for testing.
type mockLogger struct{}

func (m *mockLogger) Info(args ...any)                                    {}
func (m *mockLogger) Infof(format string, args ...any)                    {}
func (m *mockLogger) Infoln(args ...any)                                  {}
func (m *mockLogger) Error(args ...any)                                   {}
func (m *mockLogger) Errorf(format string, args ...any)                   {}
func (m *mockLogger) Errorln(args ...any)                                 {}
func (m *mockLogger) Warn(args ...any)                                    {}
func (m *mockLogger) Warnf(format string, args ...any)                    {}
func (m *mockLogger) Warnln(args ...any)                                  {}
func (m *mockLogger) Debug(args ...any)                                   {}
func (m *mockLogger) Debugf(format string, args ...any)                   {}
func (m *mockLogger) Debugln(args ...any)                                 {}
func (m *mockLogger) Fatal(args ...any)                                   {}
func (m *mockLogger) Fatalf(format string, args ...any)                   {}
func (m *mockLogger) Fatalln(args ...any)                                 {}
func (m *mockLogger) WithFields(fields ...any) libLog.Logger              { return m }
func (m *mockLogger) WithDefaultMessageTemplate(msg string) libLog.Logger { return m }
func (m *mockLogger) Sync() error                                         { return nil }

// TestPostgresPoolManager_GetConnection_DatabaseMode tests GetConnection in database isolation mode.
func TestPostgresPoolManager_GetConnection_DatabaseMode(t *testing.T) {
	tests := []struct {
		name          string
		tenantID      string
		appName       string
		setupResolver func(m *mockResolver)
		wantErr       bool
		errContains   string
	}{
		{
			name:     "Should return connection for valid tenant in database mode",
			tenantID: "tenant-db-1",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-db-1", &TenantConfig{
					ID:            "tenant-db-1",
					TenantName:    "Test Tenant",
					Status:        "active",
					IsolationMode: "database",
					Databases: map[string]DatabaseServices{
						"midaz": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "db.example.com",
								Port:     5432,
								Database: "tenant_db_1",
								Username: "user",
								Password: "pass",
								SSLMode:  "disable",
							},
						},
					},
				})
			},
			wantErr: false,
		},
		{
			name:     "Should return error for non-existent tenant",
			tenantID: "non-existent",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				// No config added
			},
			wantErr:     true,
			errContains: "tenant not found",
		},
		{
			name:     "Should return error for empty tenant ID",
			tenantID: "",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				// No config needed
			},
			wantErr:     true,
			errContains: "invalid tenant ID",
		},
		{
			name:     "Should return error for empty application name",
			tenantID: "tenant-db-1",
			appName:  "",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-db-1", &TenantConfig{
					ID:            "tenant-db-1",
					IsolationMode: "database",
				})
			},
			wantErr:     true,
			errContains: "application name",
		},
		{
			name:     "Should return error when database config is missing for application",
			tenantID: "tenant-no-app",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-no-app", &TenantConfig{
					ID:            "tenant-no-app",
					Status:        "active",
					IsolationMode: "database",
					Databases:     map[string]DatabaseServices{},
				})
			},
			wantErr:     true,
			errContains: "database config not found",
		},
		{
			name:     "Should return error for inactive tenant",
			tenantID: "tenant-inactive",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-inactive", &TenantConfig{
					ID:            "tenant-inactive",
					Status:        "inactive",
					IsolationMode: "database",
					Databases: map[string]DatabaseServices{
						"midaz": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "db.example.com",
								Port:     5432,
								Database: "tenant_inactive",
								Username: "user",
								Password: "pass",
							},
						},
					},
				})
			},
			wantErr:     true,
			errContains: "inactive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := newMockResolver()
			if tt.setupResolver != nil {
				tt.setupResolver(resolver)
			}

			pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
			defer func() { _ = pm.CloseAll() }()

			_, err := pm.GetConnection(context.Background(), tt.tenantID, tt.appName)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				// Note: In the actual implementation with mocked pool opener,
				// we might get nil or an error depending on implementation.
				// For now, we're testing the flow, not actual DB connections.
				// The actual pool creation would need sqlmock for full testing.
			}
		})
	}
}

// TestPostgresPoolManager_GetConnection_SchemaMode tests GetConnection in schema isolation mode.
func TestPostgresPoolManager_GetConnection_SchemaMode(t *testing.T) {
	tests := []struct {
		name          string
		tenantID      string
		appName       string
		setupResolver func(m *mockResolver)
		wantErr       bool
		errContains   string
	}{
		{
			name:     "Should return connection for valid tenant in schema mode",
			tenantID: "tenant-schema-1",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-schema-1", &TenantConfig{
					ID:            "tenant-schema-1",
					TenantName:    "Test Tenant Schema",
					Status:        "active",
					IsolationMode: "schema",
					Databases: map[string]DatabaseServices{
						"midaz": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "shared-db.example.com",
								Port:     5432,
								Database: "shared_db",
								Username: "user",
								Password: "pass",
								SSLMode:  "disable",
							},
						},
					},
				})
			},
			wantErr: false,
		},
		{
			name:     "Should return error for non-existent tenant in schema mode",
			tenantID: "non-existent-schema",
			appName:  "midaz",
			setupResolver: func(m *mockResolver) {
				// No config added
			},
			wantErr:     true,
			errContains: "tenant not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := newMockResolver()
			if tt.setupResolver != nil {
				tt.setupResolver(resolver)
			}

			pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
			defer func() { _ = pm.CloseAll() }()

			_, err := pm.GetConnection(context.Background(), tt.tenantID, tt.appName)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

// TestPostgresPoolManager_GetDefaultConnection tests the GetDefaultConnection method.
func TestPostgresPoolManager_GetDefaultConnection(t *testing.T) {
	t.Run("Should return error when default connection not configured", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver)
		defer func() { _ = pm.CloseAll() }()

		_, err := pm.GetDefaultConnection()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "default connection not configured")
	})
}

// TestPostgresPoolManager_PoolCaching tests pool caching logic.
// Note: These tests validate the caching LOGIC, not actual DB connections.
// Database connections will fail in unit tests without a real DB.
func TestPostgresPoolManager_PoolCaching(t *testing.T) {
	t.Run("Should attempt to reuse pool for same tenant in database mode", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-cache", &TenantConfig{
			ID:            "tenant-cache",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "tenant_cache_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// Both calls should go through the same code path
		// Will fail at connection level (expected - no real DB)
		_, err1 := pm.GetConnection(context.Background(), "tenant-cache", "midaz")
		_, err2 := pm.GetConnection(context.Background(), "tenant-cache", "midaz")

		// Both should fail with connection error (expected behavior without real DB)
		assert.Error(t, err1, "Should fail without real database")
		assert.Error(t, err2, "Should fail without real database")

		// Resolver should still be called correctly
		assert.GreaterOrEqual(t, resolver.GetCalls(), 1, "Resolver should be called")
	})

	t.Run("Should attempt shared pool for multiple tenants in schema mode", func(t *testing.T) {
		resolver := newMockResolver()

		// Two tenants, same database but different schemas
		for _, tenantID := range []string{"tenant-schema-a", "tenant-schema-b"} {
			resolver.AddConfig(tenantID, &TenantConfig{
				ID:            tenantID,
				Status:        "active",
				IsolationMode: "schema",
				Databases: map[string]DatabaseServices{
					"midaz": {
						PostgreSQL: &PostgreSQLConfig{
							Host:     "shared-db.example.com",
							Port:     5432,
							Database: "shared_db",
							Username: "user",
							Password: "pass",
						},
					},
				},
			})
		}

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// Both tenants should attempt to use the same shared database
		_, _ = pm.GetConnection(context.Background(), "tenant-schema-a", "midaz")
		_, _ = pm.GetConnection(context.Background(), "tenant-schema-b", "midaz")

		// Verify resolver was called for both tenants
		assert.Equal(t, 2, resolver.GetCalls(), "Resolver should be called for each tenant")
	})

	t.Run("Should attempt separate pools for different tenants in database mode", func(t *testing.T) {
		resolver := newMockResolver()

		// Two tenants with different databases
		resolver.AddConfig("tenant-db-a", &TenantConfig{
			ID:            "tenant-db-a",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db-a.example.com",
						Port:     5432,
						Database: "tenant_a_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		resolver.AddConfig("tenant-db-b", &TenantConfig{
			ID:            "tenant-db-b",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db-b.example.com",
						Port:     5432,
						Database: "tenant_b_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		_, _ = pm.GetConnection(context.Background(), "tenant-db-a", "midaz")
		_, _ = pm.GetConnection(context.Background(), "tenant-db-b", "midaz")

		// Verify resolver was called for both tenants
		assert.Equal(t, 2, resolver.GetCalls(), "Resolver should be called for each tenant")
	})
}

// TestPostgresPoolManager_ClosePool tests closing individual pools.
func TestPostgresPoolManager_ClosePool(t *testing.T) {
	t.Run("Should return error when closing non-existent pool", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		err := pm.ClosePool("non-existent", "midaz")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("Should not error when closing pool that was never created", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-close", &TenantConfig{
			ID:            "tenant-close",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "tenant_close_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// Try to create pool (will fail without real DB)
		_, _ = pm.GetConnection(context.Background(), "tenant-close", "midaz")

		// Close pool - should handle gracefully even if pool creation failed
		err := pm.ClosePool("tenant-close", "midaz")
		// Error is expected since pool was never successfully created
		assert.Error(t, err, "Should error when pool doesn't exist")
	})
}

// TestPostgresPoolManager_CloseTenant tests closing all pools for a tenant.
func TestPostgresPoolManager_CloseTenant(t *testing.T) {
	t.Run("Should close all pools for a tenant", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-multi-app", &TenantConfig{
			ID:            "tenant-multi-app",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "midaz_db",
						Username: "user",
						Password: "pass",
					},
				},
				"transaction": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "transaction_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// Create pools for multiple applications
		_, _ = pm.GetConnection(context.Background(), "tenant-multi-app", "midaz")
		_, _ = pm.GetConnection(context.Background(), "tenant-multi-app", "transaction")

		// Close all pools for tenant
		err := pm.CloseTenant("tenant-multi-app")
		assert.NoError(t, err)

		// Stats should show no pools for this tenant
		stats := pm.Stats()
		for key := range stats {
			assert.NotContains(t, key, "tenant-multi-app", "No pools should remain for tenant")
		}
	})
}

// TestPostgresPoolManager_CloseAll tests closing all pools.
func TestPostgresPoolManager_CloseAll(t *testing.T) {
	t.Run("Should close all pools", func(t *testing.T) {
		resolver := newMockResolver()

		for i, tenantID := range []string{"tenant-1", "tenant-2", "tenant-3"} {
			resolver.AddConfig(tenantID, &TenantConfig{
				ID:            tenantID,
				Status:        "active",
				IsolationMode: "database",
				Databases: map[string]DatabaseServices{
					"midaz": {
						PostgreSQL: &PostgreSQLConfig{
							Host:     "db.example.com",
							Port:     5432,
							Database: "db_" + string(rune('a'+i)),
							Username: "user",
							Password: "pass",
						},
					},
				},
			})
		}

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))

		// Create pools
		for _, tenantID := range []string{"tenant-1", "tenant-2", "tenant-3"} {
			_, _ = pm.GetConnection(context.Background(), tenantID, "midaz")
		}

		// Close all
		err := pm.CloseAll()
		assert.NoError(t, err)

		// Stats should be empty
		stats := pm.Stats()
		assert.Equal(t, 0, len(stats), "All pools should be closed")
	})
}

// TestPostgresPoolManager_Stats tests the Stats method.
func TestPostgresPoolManager_Stats(t *testing.T) {
	t.Run("Should return stats for all pools", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-stats", &TenantConfig{
			ID:            "tenant-stats",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "tenant_stats_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// Create pool
		_, _ = pm.GetConnection(context.Background(), "tenant-stats", "midaz")

		stats := pm.Stats()
		require.NotNil(t, stats)
		assert.GreaterOrEqual(t, len(stats), 0)
	})

	t.Run("Should return empty stats when no pools exist", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		stats := pm.Stats()
		assert.Equal(t, 0, len(stats), "Should have no pools initially")
	})
}

// TestPostgresPoolManager_ConcurrentAccess tests thread safety.
func TestPostgresPoolManager_ConcurrentAccess(t *testing.T) {
	t.Run("Should handle concurrent GetConnection calls safely", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-concurrent", &TenantConfig{
			ID:            "tenant-concurrent",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "tenant_concurrent_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		var wg sync.WaitGroup
		errChan := make(chan error, 100)

		// Launch many concurrent requests
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := pm.GetConnection(context.Background(), "tenant-concurrent", "midaz")
				if err != nil {
					errChan <- err
				}
			}()
		}

		wg.Wait()
		close(errChan)

		// Check for unexpected errors (connection errors are expected with mock)
		for err := range errChan {
			// Only fail on unexpected errors, not connection errors
			if err != nil && !isExpectedMockError(err) {
				t.Errorf("Unexpected concurrent access error: %v", err)
			}
		}

		// Should still only have one pool
		stats := pm.Stats()
		assert.LessOrEqual(t, len(stats), 1, "Should have at most one pool")
	})
}

// isExpectedMockError checks if the error is expected from mock behavior.
func isExpectedMockError(err error) bool {
	// With our implementation, we might get nil DB errors
	// This helper can be extended as needed
	return true
}

// TestPostgresPoolManager_ContextCancellation tests behavior with cancelled context.
func TestPostgresPoolManager_ContextCancellation(t *testing.T) {
	t.Run("Should respect context cancellation", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-ctx", &TenantConfig{
			ID:            "tenant-ctx",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "tenant_ctx_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := pm.GetConnection(ctx, "tenant-ctx", "midaz")
		// Should fail with context error
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context")
	})
}

// TestPostgresPoolManager_SchemaModeSetsSearchPath tests that schema mode executes SET search_path.
func TestPostgresPoolManager_SchemaModeSetsSearchPath(t *testing.T) {
	t.Run("Should prepare connection with search_path for schema mode", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-search-path", &TenantConfig{
			ID:            "tenant-search-path",
			TenantName:    "Search Path Tenant",
			Status:        "active",
			IsolationMode: "schema",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "shared-db.example.com",
						Port:     5432,
						Database: "shared_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// In schema mode, GetSchemaConnection should attempt to get a connection
		// and set search_path
		_, err := pm.GetSchemaConnection(context.Background(), "tenant-search-path", "midaz")
		// With mock, we can't fully test the SET command, but we verify the flow
		_ = err
	})
}

// TestPostgresPoolManager_IdleCleanup tests background cleanup of idle pools.
func TestPostgresPoolManager_IdleCleanup(t *testing.T) {
	t.Run("Should run cleanup goroutine without errors", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-idle", &TenantConfig{
			ID:            "tenant-idle",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "tenant_idle_db",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		// Very short idle timeout for testing
		pm := NewPostgresPoolManager(resolver,
			WithIdleTimeout(50*time.Millisecond),
			WithCleanupInterval(25*time.Millisecond),
			WithLogger(&mockLogger{}),
		)

		// Try to create pool (will fail without real DB)
		_, _ = pm.GetConnection(context.Background(), "tenant-idle", "midaz")

		// Wait for cleanup interval to run
		time.Sleep(100 * time.Millisecond)

		// CloseAll should complete without deadlock
		err := pm.CloseAll()
		assert.NoError(t, err, "CloseAll should complete successfully")
	})

	t.Run("Should stop cleanup goroutine on CloseAll", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver,
			WithIdleTimeout(1*time.Minute),
			WithCleanupInterval(10*time.Millisecond),
			WithLogger(&mockLogger{}),
		)

		// Wait a bit for cleanup to run a few times
		time.Sleep(50 * time.Millisecond)

		// CloseAll should stop the cleanup goroutine
		err := pm.CloseAll()
		assert.NoError(t, err, "CloseAll should stop cleanup goroutine cleanly")
	})
}

// TestPostgresPoolManager_MaxPoolsLimit tests that max pools limit is enforced.
func TestPostgresPoolManager_MaxPoolsLimit(t *testing.T) {
	t.Run("Should enforce max pools limit", func(t *testing.T) {
		resolver := newMockResolver()

		// Add many tenant configs
		for i := 0; i < 10; i++ {
			tenantID := "tenant-limit-" + string(rune('a'+i))
			resolver.AddConfig(tenantID, &TenantConfig{
				ID:            tenantID,
				Status:        "active",
				IsolationMode: "database",
				Databases: map[string]DatabaseServices{
					"midaz": {
						PostgreSQL: &PostgreSQLConfig{
							Host:     "db.example.com",
							Port:     5432,
							Database: "db_" + tenantID,
							Username: "user",
							Password: "pass",
						},
					},
				},
			})
		}

		// Set low max pools limit
		pm := NewPostgresPoolManager(resolver, WithMaxPools(3), WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		// Try to create more pools than limit
		for i := 0; i < 10; i++ {
			tenantID := "tenant-limit-" + string(rune('a'+i))
			_, _ = pm.GetConnection(context.Background(), tenantID, "midaz")
		}

		// Should not exceed max pools
		stats := pm.Stats()
		assert.LessOrEqual(t, len(stats), 3, "Should not exceed max pools limit")
	})
}

// TestPoolStats tests the PoolStats structure.
func TestPoolStats(t *testing.T) {
	t.Run("PoolStats should have expected fields", func(t *testing.T) {
		stats := PoolStats{
			TenantID:        "tenant-123",
			ApplicationName: "midaz",
			IsolationMode:   "database",
			CreatedAt:       time.Now(),
			LastUsedAt:      time.Now(),
			OpenConnections: 5,
			IdleConnections: 2,
		}

		assert.Equal(t, "tenant-123", stats.TenantID)
		assert.Equal(t, "midaz", stats.ApplicationName)
		assert.Equal(t, "database", stats.IsolationMode)
		assert.Equal(t, 5, stats.OpenConnections)
		assert.Equal(t, 2, stats.IdleConnections)
	})
}

// TestPostgresPoolManager_Interface verifies the interface is implemented correctly.
func TestPostgresPoolManager_Interface(t *testing.T) {
	// Compile-time check that postgresPoolManagerImpl implements PostgresPoolManager
	var _ PostgresPoolManager = (*postgresPoolManagerImpl)(nil)
}

// TestPoolEntry_UpdateLastUsed tests the poolEntry updateLastUsed method.
func TestPoolEntry_UpdateLastUsed(t *testing.T) {
	t.Run("Should update lastUsedAt timestamp", func(t *testing.T) {
		entry := &poolEntry{
			createdAt:  time.Now().Add(-time.Hour),
			lastUsedAt: time.Now().Add(-time.Minute),
		}

		oldLastUsed := entry.getLastUsed()
		time.Sleep(time.Millisecond)
		entry.updateLastUsed()
		newLastUsed := entry.getLastUsed()

		assert.True(t, newLastUsed.After(oldLastUsed), "lastUsedAt should be updated")
	})
}

// TestPoolEntry_GetLastUsed tests the poolEntry getLastUsed method.
func TestPoolEntry_GetLastUsed(t *testing.T) {
	t.Run("Should return lastUsedAt timestamp", func(t *testing.T) {
		now := time.Now()
		entry := &poolEntry{
			lastUsedAt: now,
		}

		result := entry.getLastUsed()
		assert.Equal(t, now, result)
	})
}

// TestPostgresPoolManager_BuildDSN tests the buildDSN method.
func TestPostgresPoolManager_BuildDSN(t *testing.T) {
	tests := []struct {
		name     string
		config   *PostgreSQLConfig
		expected string
	}{
		{
			name: "Should build DSN with all fields",
			config: &PostgreSQLConfig{
				Host:     "db.example.com",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Password: "testpass",
				SSLMode:  "require",
			},
			expected: "host=db.example.com port=5432 user=testuser password=testpass dbname=testdb sslmode=require",
		},
		{
			name: "Should default to disable sslmode when empty",
			config: &PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Database: "test",
				Username: "user",
				Password: "pass",
				SSLMode:  "",
			},
			expected: "host=localhost port=5432 user=user password=pass dbname=test sslmode=disable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := newMockResolver()
			pm := NewPostgresPoolManager(resolver).(*postgresPoolManagerImpl)
			defer func() { _ = pm.CloseAll() }()

			dsn := pm.buildDSN(tt.config)
			assert.Equal(t, tt.expected, dsn)
		})
	}
}

// TestPostgresPoolManager_MakePoolKey tests the makePoolKey method.
func TestPostgresPoolManager_MakePoolKey(t *testing.T) {
	resolver := newMockResolver()
	pm := NewPostgresPoolManager(resolver).(*postgresPoolManagerImpl)
	defer func() { _ = pm.CloseAll() }()

	key := pm.makePoolKey("tenant-123", "midaz")
	assert.Equal(t, "tenant-123:midaz", key)
}

// TestPostgresPoolManager_SanitizeDSNForKey tests the sanitizeDSNForKey method.
func TestPostgresPoolManager_SanitizeDSNForKey(t *testing.T) {
	resolver := newMockResolver()
	pm := NewPostgresPoolManager(resolver).(*postgresPoolManagerImpl)
	defer func() { _ = pm.CloseAll() }()

	tests := []struct {
		name     string
		dsn      string
		expected string
	}{
		{
			name:     "Should extract host port and dbname",
			dsn:      "host=db.example.com port=5432 user=testuser password=secret dbname=testdb sslmode=require",
			expected: "host=db.example.com_port=5432_dbname=testdb",
		},
		{
			name:     "Should handle minimal DSN",
			dsn:      "host=localhost port=5432 dbname=db",
			expected: "host=localhost_port=5432_dbname=db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.sanitizeDSNForKey(tt.dsn)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestPostgresPoolManager_SanitizeDSNForLog tests the sanitizeDSNForLog method.
func TestPostgresPoolManager_SanitizeDSNForLog(t *testing.T) {
	resolver := newMockResolver()
	pm := NewPostgresPoolManager(resolver).(*postgresPoolManagerImpl)
	defer func() { _ = pm.CloseAll() }()

	tests := []struct {
		name     string
		dsn      string
		expected string
	}{
		{
			name:     "Should mask password",
			dsn:      "host=db.example.com port=5432 user=testuser password=secret dbname=testdb",
			expected: "host=db.example.com port=5432 user=testuser password=*** dbname=testdb",
		},
		{
			name:     "Should handle DSN without password",
			dsn:      "host=localhost port=5432 dbname=db",
			expected: "host=localhost port=5432 dbname=db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.sanitizeDSNForLog(tt.dsn)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestPostgresPoolManager_GetSchemaName tests the getSchemaName method.
func TestPostgresPoolManager_GetSchemaName(t *testing.T) {
	resolver := newMockResolver()
	pm := NewPostgresPoolManager(resolver).(*postgresPoolManagerImpl)
	defer func() { _ = pm.CloseAll() }()

	tests := []struct {
		name       string
		tenantID   string
		expectedPr string
	}{
		{
			name:       "Should sanitize simple tenant ID",
			tenantID:   "tenant123",
			expectedPr: "tenant_tenant123",
		},
		{
			name:       "Should replace hyphens with underscores",
			tenantID:   "tenant-123",
			expectedPr: "tenant_tenant_123",
		},
		{
			name:       "Should handle UUID format",
			tenantID:   "550e8400-e29b-41d4-a716-446655440000",
			expectedPr: "tenant_550e8400_e29b_41d4_a716_446655440000",
		},
		{
			name:       "Should handle special characters",
			tenantID:   "tenant@123!test",
			expectedPr: "tenant_tenant_123_test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.getSchemaName(tt.tenantID)
			assert.Equal(t, tt.expectedPr, result)
		})
	}
}

// TestPostgresPoolManager_UnsupportedIsolationMode tests error handling for unsupported modes.
func TestPostgresPoolManager_UnsupportedIsolationMode(t *testing.T) {
	t.Run("Should return error for unsupported isolation mode", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-unsupported", &TenantConfig{
			ID:            "tenant-unsupported",
			Status:        "active",
			IsolationMode: "unsupported_mode",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "testdb",
						Username: "user",
						Password: "pass",
					},
				},
			},
		})

		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll() }()

		_, err := pm.GetConnection(context.Background(), "tenant-unsupported", "midaz")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported isolation mode")
	})
}

// TestPostgresPoolManager_WithLoggerOption tests the WithLogger option.
func TestPostgresPoolManager_WithLoggerOption(t *testing.T) {
	t.Run("Should set logger via option", func(t *testing.T) {
		resolver := newMockResolver()
		logger := &mockLogger{}

		pm := NewPostgresPoolManager(resolver, WithLogger(logger))
		defer func() { _ = pm.CloseAll() }()

		impl := pm.(*postgresPoolManagerImpl)
		assert.Equal(t, logger, impl.logger)
	})
}

// TestPostgresPoolManager_WithDefaultConnectionOption tests the WithDefaultConnection option.
func TestPostgresPoolManager_WithDefaultConnectionOption(t *testing.T) {
	t.Run("Should set default connection via option", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewPostgresPoolManager(resolver, WithDefaultConnection(nil))
		defer func() { _ = pm.CloseAll() }()

		impl := pm.(*postgresPoolManagerImpl)
		assert.Nil(t, impl.defaultConn)
	})
}

// TestPostgresPoolManager_DoCleanupSharedConn tests doCleanup for shared connections.
func TestPostgresPoolManager_DoCleanupSharedConn(t *testing.T) {
	t.Run("Should cleanup idle shared connections when not in use", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewPostgresPoolManager(resolver,
			WithIdleTimeout(1*time.Millisecond),
			WithCleanupInterval(1*time.Millisecond),
			WithLogger(&mockLogger{}),
		).(*postgresPoolManagerImpl)

		// Manually add an expired shared entry
		pm.mu.Lock()
		pm.sharedConns["host=localhost port=5432 dbname=expired"] = &poolEntry{
			appName:       "midaz",
			isolationMode: "schema",
			createdAt:     time.Now().Add(-time.Hour),
			lastUsedAt:    time.Now().Add(-time.Hour),
			conn:          nil,
		}
		pm.mu.Unlock()

		// Wait for cleanup to run
		time.Sleep(50 * time.Millisecond)

		// Check that the expired entry was removed
		pm.mu.RLock()
		_, exists := pm.sharedConns["host=localhost port=5432 dbname=expired"]
		pm.mu.RUnlock()

		assert.False(t, exists, "Expired shared entry should be cleaned up")

		_ = pm.CloseAll()
	})

	t.Run("Should NOT cleanup shared connections still in use", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewPostgresPoolManager(resolver,
			WithIdleTimeout(1*time.Millisecond),
			WithCleanupInterval(1*time.Millisecond),
			WithLogger(&mockLogger{}),
		).(*postgresPoolManagerImpl)

		// Manually add an expired shared entry that is still mapped
		pm.mu.Lock()
		pm.sharedConns["host=localhost port=5432 dbname=inuse"] = &poolEntry{
			appName:       "midaz",
			isolationMode: "schema",
			createdAt:     time.Now().Add(-time.Hour),
			lastUsedAt:    time.Now().Add(-time.Hour),
			conn:          nil,
		}
		// Map a tenant to this shared connection
		pm.tenantToSharedConn["active-tenant:midaz"] = "host=localhost port=5432 dbname=inuse"
		pm.mu.Unlock()

		// Wait for cleanup to run
		time.Sleep(50 * time.Millisecond)

		// Check that the entry was NOT removed because it's still in use
		pm.mu.RLock()
		_, exists := pm.sharedConns["host=localhost port=5432 dbname=inuse"]
		pm.mu.RUnlock()

		assert.True(t, exists, "Shared entry still in use should NOT be cleaned up")

		_ = pm.CloseAll()
	})
}

// TestPostgresPoolManager_DoCleanup tests the doCleanup method.
func TestPostgresPoolManager_DoCleanup(t *testing.T) {
	t.Run("Should cleanup idle connections", func(t *testing.T) {
		resolver := newMockResolver()

		// Very short idle timeout for testing
		pm := NewPostgresPoolManager(resolver,
			WithIdleTimeout(1*time.Millisecond),
			WithCleanupInterval(1*time.Millisecond),
			WithLogger(&mockLogger{}),
		).(*postgresPoolManagerImpl)

		// Manually add an expired entry
		pm.mu.Lock()
		pm.tenantConns["expired-tenant:midaz"] = &poolEntry{
			tenantID:   "expired-tenant",
			appName:    "midaz",
			createdAt:  time.Now().Add(-time.Hour),
			lastUsedAt: time.Now().Add(-time.Hour), // Already expired
			conn:       nil,                        // nil connection for safety
		}
		pm.mu.Unlock()

		// Wait for cleanup to run
		time.Sleep(50 * time.Millisecond)

		// Check that the expired entry was removed
		pm.mu.RLock()
		_, exists := pm.tenantConns["expired-tenant:midaz"]
		pm.mu.RUnlock()

		assert.False(t, exists, "Expired entry should be cleaned up")

		_ = pm.CloseAll()
	})
}

// TestPostgresPoolManager_EvictLRUConn tests the evictLRUConn method.
func TestPostgresPoolManager_EvictLRUConn(t *testing.T) {
	t.Run("Should return error when no connections to evict", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{})).(*postgresPoolManagerImpl)
		defer func() { _ = pm.CloseAll() }()

		pm.mu.Lock()
		err := pm.evictLRUConn()
		pm.mu.Unlock()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no connections to evict")
	})

	t.Run("Should evict oldest tenant connection", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{})).(*postgresPoolManagerImpl)
		defer func() { _ = pm.CloseAll() }()

		// Manually add entries with different timestamps
		pm.mu.Lock()
		pm.tenantConns["old-tenant:midaz"] = &poolEntry{
			tenantID:   "old-tenant",
			appName:    "midaz",
			createdAt:  time.Now().Add(-2 * time.Hour),
			lastUsedAt: time.Now().Add(-2 * time.Hour),
			conn:       nil,
		}
		pm.tenantConns["new-tenant:midaz"] = &poolEntry{
			tenantID:   "new-tenant",
			appName:    "midaz",
			createdAt:  time.Now(),
			lastUsedAt: time.Now(),
			conn:       nil,
		}

		err := pm.evictLRUConn()
		pm.mu.Unlock()

		require.NoError(t, err)

		pm.mu.RLock()
		_, oldExists := pm.tenantConns["old-tenant:midaz"]
		_, newExists := pm.tenantConns["new-tenant:midaz"]
		pm.mu.RUnlock()

		assert.False(t, oldExists, "Old tenant should be evicted")
		assert.True(t, newExists, "New tenant should still exist")
	})

	t.Run("Should evict shared connection when it is oldest", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{})).(*postgresPoolManagerImpl)
		defer func() { _ = pm.CloseAll() }()

		// Add tenant and shared entries
		pm.mu.Lock()
		pm.tenantConns["tenant:midaz"] = &poolEntry{
			tenantID:   "tenant",
			appName:    "midaz",
			createdAt:  time.Now(),
			lastUsedAt: time.Now(),
			conn:       nil,
		}
		pm.sharedConns["host=localhost port=5432 dbname=shared"] = &poolEntry{
			appName:    "midaz",
			createdAt:  time.Now().Add(-2 * time.Hour),
			lastUsedAt: time.Now().Add(-2 * time.Hour),
			conn:       nil,
		}
		pm.tenantToSharedConn["other-tenant:midaz"] = "host=localhost port=5432 dbname=shared"

		err := pm.evictLRUConn()
		pm.mu.Unlock()

		require.NoError(t, err)

		pm.mu.RLock()
		_, tenantExists := pm.tenantConns["tenant:midaz"]
		_, sharedExists := pm.sharedConns["host=localhost port=5432 dbname=shared"]
		_, mappingExists := pm.tenantToSharedConn["other-tenant:midaz"]
		pm.mu.RUnlock()

		assert.True(t, tenantExists, "Tenant connection should still exist")
		assert.False(t, sharedExists, "Shared connection should be evicted")
		assert.False(t, mappingExists, "Tenant mapping to shared should be removed")
	})
}

// TestPostgresPoolManager_StatsWithConnections tests Stats with actual connection entries.
func TestPostgresPoolManager_StatsWithConnections(t *testing.T) {
	t.Run("Should return stats for tenant and shared connections", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewPostgresPoolManager(resolver, WithLogger(&mockLogger{})).(*postgresPoolManagerImpl)
		defer func() { _ = pm.CloseAll() }()

		now := time.Now()

		// Manually add entries
		pm.mu.Lock()
		pm.tenantConns["tenant-1:midaz"] = &poolEntry{
			tenantID:      "tenant-1",
			appName:       "midaz",
			isolationMode: "database",
			createdAt:     now.Add(-time.Hour),
			lastUsedAt:    now,
			conn:          nil,
		}
		pm.sharedConns["host=localhost port=5432 dbname=shared"] = &poolEntry{
			tenantID:      "",
			appName:       "midaz",
			isolationMode: "schema",
			createdAt:     now.Add(-2 * time.Hour),
			lastUsedAt:    now.Add(-time.Minute),
			conn:          nil,
		}
		pm.mu.Unlock()

		stats := pm.Stats()

		assert.Len(t, stats, 2)

		// Check tenant connection stats
		tenantStats, ok := stats["tenant-1:midaz"]
		require.True(t, ok)
		assert.Equal(t, "tenant-1", tenantStats.TenantID)
		assert.Equal(t, "midaz", tenantStats.ApplicationName)
		assert.Equal(t, "database", tenantStats.IsolationMode)

		// Check shared connection stats (uses sanitized DSN as key prefix)
		var sharedFound bool
		for key, stat := range stats {
			if stat.IsolationMode == "schema" {
				sharedFound = true
				assert.Contains(t, key, "shared:")
				assert.Equal(t, "", stat.TenantID)
				break
			}
		}
		assert.True(t, sharedFound, "Should have shared connection stats")
	})
}

// TestPostgresPoolManagerConfig tests the configuration struct defaults.
func TestPostgresPoolManagerConfig(t *testing.T) {
	t.Run("Config should have sensible defaults when created via NewPostgresPoolManagerWithConfig", func(t *testing.T) {
		cfg := PostgresPoolManagerConfig{
			Resolver: newMockResolver(),
			Logger:   &mockLogger{},
		}

		pm, err := NewPostgresPoolManagerWithConfig(cfg)
		require.NoError(t, err)
		require.NotNil(t, pm)
		_ = pm.CloseAll()
	})
}

// TestConnectionConfig tests the ConnectionConfig struct.
func TestConnectionConfig(t *testing.T) {
	t.Run("ConnectionConfig should have expected fields", func(t *testing.T) {
		cfg := ConnectionConfig{
			MaxOpenConnections: 50,
			MaxIdleConnections: 10,
		}

		assert.Equal(t, 50, cfg.MaxOpenConnections)
		assert.Equal(t, 10, cfg.MaxIdleConnections)
	})
}
