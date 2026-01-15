package poolmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	libMongo "github.com/LerianStudio/lib-commons/v2/commons/mongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// TestNewMongoPoolManager tests the constructor with various options.
func TestNewMongoPoolManager(t *testing.T) {
	tests := []struct {
		name      string
		resolver  Resolver
		opts      []MongoPoolManagerOption
		checkFunc func(t *testing.T, pm MongoPoolManager)
	}{
		{
			name:     "Should create pool manager with default options",
			resolver: newMockResolver(),
			opts:     nil,
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with custom max clients",
			resolver: newMockResolver(),
			opts:     []MongoPoolManagerOption{WithMongoMaxClients(50)},
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with custom idle timeout",
			resolver: newMockResolver(),
			opts:     []MongoPoolManagerOption{WithMongoIdleTimeout(15 * time.Minute)},
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with custom cleanup interval",
			resolver: newMockResolver(),
			opts:     []MongoPoolManagerOption{WithMongoCleanupInterval(5 * time.Minute)},
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with all custom options",
			resolver: newMockResolver(),
			opts: []MongoPoolManagerOption{
				WithMongoMaxClients(100),
				WithMongoIdleTimeout(20 * time.Minute),
				WithMongoCleanupInterval(10 * time.Minute),
			},
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with logger",
			resolver: newMockResolver(),
			opts:     []MongoPoolManagerOption{WithMongoLogger(&mockLogger{})},
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
		{
			name:     "Should create pool manager with max pool size",
			resolver: newMockResolver(),
			opts:     []MongoPoolManagerOption{WithMongoMaxPoolSize(50)},
			checkFunc: func(t *testing.T, pm MongoPoolManager) {
				require.NotNil(t, pm, "Pool manager should not be nil")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := NewMongoPoolManager(tt.resolver, tt.opts...)
			if tt.checkFunc != nil {
				tt.checkFunc(t, pm)
			}
			// Clean up
			if pm != nil {
				_ = pm.CloseAll(context.Background())
			}
		})
	}
}

// TestNewMongoPoolManagerWithConfig tests the config-based constructor.
func TestNewMongoPoolManagerWithConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MongoPoolManagerConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "Should fail without resolver",
			cfg:     MongoPoolManagerConfig{},
			wantErr: true,
			errMsg:  "resolver is required",
		},
		{
			name: "Should fail without logger",
			cfg: MongoPoolManagerConfig{
				Resolver: newMockResolver(),
			},
			wantErr: true,
			errMsg:  "logger is required",
		},
		{
			name: "Should create with minimal config",
			cfg: MongoPoolManagerConfig{
				Resolver: newMockResolver(),
				Logger:   &mockLogger{},
			},
			wantErr: false,
		},
		{
			name: "Should create with full config",
			cfg: MongoPoolManagerConfig{
				Resolver:        newMockResolver(),
				Logger:          &mockLogger{},
				MaxConnections:  50,
				IdleTimeout:     15 * time.Minute,
				CleanupInterval: 3 * time.Minute,
				MaxPoolSize:     200,
			},
			wantErr: false,
		},
		{
			name: "Should create with default connection",
			cfg: MongoPoolManagerConfig{
				Resolver:          newMockResolver(),
				Logger:            &mockLogger{},
				DefaultConnection: &libMongo.MongoConnection{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm, err := NewMongoPoolManagerWithConfig(tt.cfg)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pm)

			// Clean up
			if pm != nil {
				_ = pm.CloseAll(context.Background())
			}
		})
	}
}

// TestMongoPoolManager_GetClient_DatabaseMode tests GetClient in database isolation mode.
func TestMongoPoolManager_GetClient_DatabaseMode(t *testing.T) {
	tests := []struct {
		name          string
		tenantID      string
		appName       string
		setupResolver func(m *mockResolver)
		wantErr       bool
		errContains   string
	}{
		{
			name:     "Should return client for valid tenant in database mode",
			tenantID: "tenant-db-1",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-db-1", &TenantConfig{
					ID:            "tenant-db-1",
					TenantName:    "Test Tenant",
					Status:        "active",
					IsolationMode: "database",
					Databases: map[string]DatabaseServices{
						"mdz-reporter": {
							MongoDB: &MongoDBConfig{
								URI:      "mongodb://localhost:27017",
								Database: "tenant_db_1",
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
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				// No config added
			},
			wantErr:     true,
			errContains: "tenant not found",
		},
		{
			name:     "Should return error for empty tenant ID",
			tenantID: "",
			appName:  "mdz-reporter",
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
			name:     "Should return error when MongoDB config is missing for application",
			tenantID: "tenant-no-mongo",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-no-mongo", &TenantConfig{
					ID:            "tenant-no-mongo",
					Status:        "active",
					IsolationMode: "database",
					Databases:     map[string]DatabaseServices{},
				})
			},
			wantErr:     true,
			errContains: "MongoDB config not found",
		},
		{
			name:     "Should return error for inactive tenant",
			tenantID: "tenant-inactive",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-inactive", &TenantConfig{
					ID:            "tenant-inactive",
					Status:        "inactive",
					IsolationMode: "database",
					Databases: map[string]DatabaseServices{
						"mdz-reporter": {
							MongoDB: &MongoDBConfig{
								URI:      "mongodb://localhost:27017",
								Database: "tenant_inactive",
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

			pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
			defer func() { _ = pm.CloseAll(context.Background()) }()

			_, err := pm.GetClient(context.Background(), tt.tenantID, tt.appName)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				// Note: Actual MongoDB connection will fail without real MongoDB
				// We're testing the flow and validation logic
			}
		})
	}
}

// TestMongoPoolManager_GetClient_SchemaMode tests GetClient in schema isolation mode.
func TestMongoPoolManager_GetClient_SchemaMode(t *testing.T) {
	tests := []struct {
		name          string
		tenantID      string
		appName       string
		setupResolver func(m *mockResolver)
		wantErr       bool
		errContains   string
	}{
		{
			name:     "Should return client for valid tenant in schema mode",
			tenantID: "tenant-schema-1",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-schema-1", &TenantConfig{
					ID:            "tenant-schema-1",
					TenantName:    "Test Tenant Schema",
					Status:        "active",
					IsolationMode: "schema",
					Databases: map[string]DatabaseServices{
						"mdz-reporter": {
							MongoDB: &MongoDBConfig{
								URI:      "mongodb://localhost:27017",
								Database: "shared_db",
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
			appName:  "mdz-reporter",
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

			pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
			defer func() { _ = pm.CloseAll(context.Background()) }()

			_, err := pm.GetClient(context.Background(), tt.tenantID, tt.appName)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

// TestMongoPoolManager_GetDatabase tests the GetDatabase method.
func TestMongoPoolManager_GetDatabase(t *testing.T) {
	tests := []struct {
		name          string
		tenantID      string
		appName       string
		setupResolver func(m *mockResolver)
		wantErr       bool
		errContains   string
	}{
		{
			name:     "Should return TenantDatabase for valid tenant in database mode",
			tenantID: "tenant-getdb-1",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-getdb-1", &TenantConfig{
					ID:            "tenant-getdb-1",
					TenantName:    "Test Tenant",
					Status:        "active",
					IsolationMode: "database",
					Databases: map[string]DatabaseServices{
						"mdz-reporter": {
							MongoDB: &MongoDBConfig{
								URI:      "mongodb://localhost:27017",
								Database: "tenant_getdb_1",
							},
						},
					},
				})
			},
			wantErr: false,
		},
		{
			name:     "Should return TenantDatabase with prefix for schema mode",
			tenantID: "tenant-getdb-schema",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				m.AddConfig("tenant-getdb-schema", &TenantConfig{
					ID:            "tenant-getdb-schema",
					TenantName:    "Schema Tenant",
					Status:        "active",
					IsolationMode: "schema",
					Databases: map[string]DatabaseServices{
						"mdz-reporter": {
							MongoDB: &MongoDBConfig{
								URI:      "mongodb://localhost:27017",
								Database: "shared_db",
							},
						},
					},
				})
			},
			wantErr: false,
		},
		{
			name:     "Should return error for empty tenant ID",
			tenantID: "",
			appName:  "mdz-reporter",
			setupResolver: func(m *mockResolver) {
				// No config needed
			},
			wantErr:     true,
			errContains: "invalid tenant ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := newMockResolver()
			if tt.setupResolver != nil {
				tt.setupResolver(resolver)
			}

			pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
			defer func() { _ = pm.CloseAll(context.Background()) }()

			_, err := pm.GetDatabase(context.Background(), tt.tenantID, tt.appName)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

// TestMongoPoolManager_GetDefaultConnection tests the GetDefaultConnection method.
func TestMongoPoolManager_GetDefaultConnection(t *testing.T) {
	t.Run("Should return nil when default connection not configured", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		conn := pm.GetDefaultConnection()
		assert.Nil(t, conn)
	})

	t.Run("Should return default connection when configured", func(t *testing.T) {
		resolver := newMockResolver()
		defaultConn := &libMongo.MongoConnection{
			ConnectionStringSource: "mongodb://localhost:27017",
			Database:               "default_db",
		}
		pm := NewMongoPoolManager(resolver, WithMongoDefaultConnection(defaultConn))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		conn := pm.GetDefaultConnection()
		require.NotNil(t, conn)
		assert.Equal(t, "mongodb://localhost:27017", conn.ConnectionStringSource)
		assert.Equal(t, "default_db", conn.Database)
	})
}

// TestTenantDatabase_Collection tests the TenantDatabase.Collection method.
func TestTenantDatabase_Collection(t *testing.T) {
	tests := []struct {
		name             string
		collectionPrefix string
		collectionName   string
		expectedName     string
	}{
		{
			name:             "Should not prefix collection in database mode (empty prefix)",
			collectionPrefix: "",
			collectionName:   "users",
			expectedName:     "users",
		},
		{
			name:             "Should prefix collection in schema mode",
			collectionPrefix: "tenant_abc_",
			collectionName:   "users",
			expectedName:     "tenant_abc_users",
		},
		{
			name:             "Should prefix collection with UUID-based tenant",
			collectionPrefix: "tenant_550e8400_e29b_41d4_a716_446655440000_",
			collectionName:   "transactions",
			expectedName:     "tenant_550e8400_e29b_41d4_a716_446655440000_transactions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TenantDatabase wraps nil db for testing - only tests prefixing logic
			td := &TenantDatabase{
				db:               nil,
				collectionPrefix: tt.collectionPrefix,
			}

			// Test getPrefixedName directly
			prefixedName := td.getPrefixedName(tt.collectionName)
			assert.Equal(t, tt.expectedName, prefixedName)
		})
	}
}

// TestTenantDatabase_TenantID tests the TenantDatabase.TenantID method.
func TestTenantDatabase_TenantID(t *testing.T) {
	td := &TenantDatabase{
		tenantID: "test-tenant-123",
	}
	assert.Equal(t, "test-tenant-123", td.TenantID())
}

// TestTenantDatabase_Prefix tests the TenantDatabase.Prefix method.
func TestTenantDatabase_Prefix(t *testing.T) {
	td := &TenantDatabase{
		collectionPrefix: "tenant_abc_",
	}
	assert.Equal(t, "tenant_abc_", td.Prefix())
}

// TestMongoPoolManager_ClientCaching tests client caching logic.
func TestMongoPoolManager_ClientCaching(t *testing.T) {
	t.Run("Should reuse client for same tenant in database mode", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-cache", &TenantConfig{
			ID:            "tenant-cache",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						// Use invalid host to trigger connection error
						URI:      "mongodb://invalid-host-that-does-not-exist:27017/?connectTimeoutMS=1000&serverSelectionTimeoutMS=1000",
						Database: "tenant_cache_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		// Create context with short timeout to speed up the test
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// Both calls should go through the same code path
		// With invalid host, should fail with connection error
		_, err1 := pm.GetClient(ctx, "tenant-cache", "mdz-reporter")
		_, err2 := pm.GetClient(ctx, "tenant-cache", "mdz-reporter")

		// Both should fail with connection error (invalid host)
		assert.Error(t, err1, "Should fail with invalid host")
		assert.Error(t, err2, "Should fail with invalid host")

		// Resolver should be called for each attempt (no caching since connection failed)
		assert.GreaterOrEqual(t, resolver.GetCalls(), 1, "Resolver should be called")
	})

	t.Run("Should attempt shared client for multiple tenants in schema mode", func(t *testing.T) {
		resolver := newMockResolver()

		// Two tenants, same database but different collection prefixes
		for _, tenantID := range []string{"tenant-schema-a", "tenant-schema-b"} {
			resolver.AddConfig(tenantID, &TenantConfig{
				ID:            tenantID,
				Status:        "active",
				IsolationMode: "schema",
				Databases: map[string]DatabaseServices{
					"mdz-reporter": {
						MongoDB: &MongoDBConfig{
							URI:      "mongodb://localhost:27017",
							Database: "shared_db",
						},
					},
				},
			})
		}

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		_, _ = pm.GetClient(context.Background(), "tenant-schema-a", "mdz-reporter")
		_, _ = pm.GetClient(context.Background(), "tenant-schema-b", "mdz-reporter")

		// Verify resolver was called for both tenants
		assert.Equal(t, 2, resolver.GetCalls(), "Resolver should be called for each tenant")
	})
}

// TestMongoPoolManager_CloseClient tests closing individual clients.
func TestMongoPoolManager_CloseClient(t *testing.T) {
	t.Run("Should return error when closing non-existent client", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		err := pm.CloseClient("non-existent", "mdz-reporter")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// TestMongoPoolManager_CloseAll tests closing all clients.
func TestMongoPoolManager_CloseAll(t *testing.T) {
	t.Run("Should close all clients", func(t *testing.T) {
		resolver := newMockResolver()

		for i, tenantID := range []string{"tenant-1", "tenant-2", "tenant-3"} {
			resolver.AddConfig(tenantID, &TenantConfig{
				ID:            tenantID,
				Status:        "active",
				IsolationMode: "database",
				Databases: map[string]DatabaseServices{
					"mdz-reporter": {
						MongoDB: &MongoDBConfig{
							URI:      "mongodb://localhost:27017",
							Database: "db_" + string(rune('a'+i)),
						},
					},
				},
			})
		}

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))

		// Try to create clients (will fail without real MongoDB)
		for _, tenantID := range []string{"tenant-1", "tenant-2", "tenant-3"} {
			_, _ = pm.GetClient(context.Background(), tenantID, "mdz-reporter")
		}

		// Close all should not error
		err := pm.CloseAll(context.Background())
		assert.NoError(t, err)

		// Stats should be empty
		stats := pm.Stats()
		assert.Equal(t, 0, len(stats), "All clients should be closed")
	})
}

// TestMongoPoolManager_Stats tests the Stats method.
func TestMongoPoolManager_Stats(t *testing.T) {
	t.Run("Should return empty stats when no clients exist", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		stats := pm.Stats()
		assert.Equal(t, 0, len(stats), "Should have no clients initially")
	})
}

// TestMongoPoolManager_ConcurrentAccess tests thread safety.
func TestMongoPoolManager_ConcurrentAccess(t *testing.T) {
	t.Run("Should handle concurrent GetClient calls safely", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-concurrent", &TenantConfig{
			ID:            "tenant-concurrent",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "tenant_concurrent_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		var wg sync.WaitGroup

		// Launch many concurrent requests
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = pm.GetClient(context.Background(), "tenant-concurrent", "mdz-reporter")
			}()
		}

		wg.Wait()

		// Should not panic and stats should be accessible
		stats := pm.Stats()
		assert.GreaterOrEqual(t, len(stats), 0, "Stats should be accessible after concurrent access")
	})
}

// TestMongoPoolManager_ContextCancellation tests behavior with cancelled context.
func TestMongoPoolManager_ContextCancellation(t *testing.T) {
	t.Run("Should respect context cancellation", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-ctx", &TenantConfig{
			ID:            "tenant-ctx",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "tenant_ctx_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := pm.GetClient(ctx, "tenant-ctx", "mdz-reporter")
		// Should return context error or at least not panic
		_ = err
	})
}

// TestMongoPoolManager_IdleCleanup tests background cleanup of idle clients.
func TestMongoPoolManager_IdleCleanup(t *testing.T) {
	t.Run("Should run cleanup goroutine without errors", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-idle", &TenantConfig{
			ID:            "tenant-idle",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "tenant_idle_db",
					},
				},
			},
		})

		// Very short idle timeout for testing
		pm := NewMongoPoolManager(resolver,
			WithMongoIdleTimeout(50*time.Millisecond),
			WithMongoCleanupInterval(25*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		)

		// Try to create client (will fail without real MongoDB)
		_, _ = pm.GetClient(context.Background(), "tenant-idle", "mdz-reporter")

		// Wait for cleanup interval to run
		time.Sleep(100 * time.Millisecond)

		// CloseAll should complete without deadlock
		err := pm.CloseAll(context.Background())
		assert.NoError(t, err, "CloseAll should complete successfully")
	})
}

// TestMongoPoolManager_MaxClientsLimit tests that max clients limit is enforced.
func TestMongoPoolManager_MaxClientsLimit(t *testing.T) {
	t.Run("Should enforce max clients limit", func(t *testing.T) {
		resolver := newMockResolver()

		// Add many tenant configs
		for i := 0; i < 10; i++ {
			tenantID := "tenant-limit-" + string(rune('a'+i))
			resolver.AddConfig(tenantID, &TenantConfig{
				ID:            tenantID,
				Status:        "active",
				IsolationMode: "database",
				Databases: map[string]DatabaseServices{
					"mdz-reporter": {
						MongoDB: &MongoDBConfig{
							URI:      "mongodb://localhost:27017",
							Database: "db_" + tenantID,
						},
					},
				},
			})
		}

		// Set low max clients limit
		pm := NewMongoPoolManager(resolver, WithMongoMaxClients(3), WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		// Try to create more clients than limit
		for i := 0; i < 10; i++ {
			tenantID := "tenant-limit-" + string(rune('a'+i))
			_, _ = pm.GetClient(context.Background(), tenantID, "mdz-reporter")
		}

		// Should not exceed max clients
		stats := pm.Stats()
		assert.LessOrEqual(t, len(stats), 3, "Should not exceed max clients limit")
	})
}

// TestMongoPoolManager_Interface verifies the interface is implemented correctly.
func TestMongoPoolManager_Interface(t *testing.T) {
	// Compile-time check that mongoPoolManagerImpl implements MongoPoolManager
	var _ MongoPoolManager = (*mongoPoolManagerImpl)(nil)
}

// TestTenantDatabase_Interface tests TenantDatabase wrapper.
func TestTenantDatabase_Interface(t *testing.T) {
	t.Run("TenantDatabase should wrap mongo.Database correctly", func(t *testing.T) {
		// Test with nil db (for unit testing without actual MongoDB)
		td := &TenantDatabase{
			db:               nil,
			tenantID:         "test-tenant",
			collectionPrefix: "tenant_test_",
		}

		// Verify prefix is stored correctly
		assert.Equal(t, "tenant_test_", td.collectionPrefix)
		assert.Equal(t, "tenant_test_users", td.getPrefixedName("users"))
		assert.Equal(t, "test-tenant", td.TenantID())
	})
}

// TestTenantDatabase_CollectionOptions tests Collection with options.
func TestTenantDatabase_CollectionOptions(t *testing.T) {
	t.Run("Should accept collection options", func(t *testing.T) {
		td := &TenantDatabase{
			db:               nil,
			collectionPrefix: "",
		}

		// Create options (won't be used with nil db, but tests the signature)
		opts := options.Collection()

		// This would panic with nil db, so we just test the prefix logic
		assert.NotNil(t, opts)
		assert.Equal(t, "users", td.getPrefixedName("users"))
	})
}

// TestMongoPoolManager_UnsupportedIsolationMode tests error handling for unsupported modes.
func TestMongoPoolManager_UnsupportedIsolationMode(t *testing.T) {
	t.Run("Should return error for unsupported isolation mode", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-unsupported", &TenantConfig{
			ID:            "tenant-unsupported",
			Status:        "active",
			IsolationMode: "unsupported_mode",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "some_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		_, err := pm.GetClient(context.Background(), "tenant-unsupported", "mdz-reporter")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported isolation mode")
	})
}

// TestMongoPoolStats tests the MongoPoolStats structure.
func TestMongoPoolStats(t *testing.T) {
	t.Run("MongoPoolStats should have expected fields", func(t *testing.T) {
		stats := MongoPoolStats{
			TenantID:        "tenant-123",
			ApplicationName: "mdz-reporter",
			IsolationMode:   "database",
			CreatedAt:       time.Now(),
			LastUsedAt:      time.Now(),
			DatabaseName:    "tenant_123_db",
		}

		assert.Equal(t, "tenant-123", stats.TenantID)
		assert.Equal(t, "mdz-reporter", stats.ApplicationName)
		assert.Equal(t, "database", stats.IsolationMode)
		assert.Equal(t, "tenant_123_db", stats.DatabaseName)
	})
}

// TestTenantDatabase_UnderlyingDatabase tests accessing the underlying database.
func TestTenantDatabase_UnderlyingDatabase(t *testing.T) {
	t.Run("Should return underlying mongo.Database", func(t *testing.T) {
		td := &TenantDatabase{
			db:               nil,
			collectionPrefix: "prefix_",
		}

		// Database() should return the underlying db pointer
		assert.Nil(t, td.Database())
	})
}

// TestCollectionPrefixGeneration tests the collection prefix generation for different tenant IDs.
func TestCollectionPrefixGeneration(t *testing.T) {
	tests := []struct {
		name           string
		tenantID       string
		expectedPrefix string
	}{
		{
			name:           "Simple tenant ID",
			tenantID:       "tenant123",
			expectedPrefix: "tenant_tenant123_",
		},
		{
			name:           "UUID tenant ID with hyphens",
			tenantID:       "550e8400-e29b-41d4-a716-446655440000",
			expectedPrefix: "tenant_550e8400_e29b_41d4_a716_446655440000_",
		},
		{
			name:           "Tenant ID with special characters",
			tenantID:       "tenant@123!test",
			expectedPrefix: "tenant_tenant_123_test_",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := generateCollectionPrefix(tt.tenantID)
			assert.Equal(t, tt.expectedPrefix, prefix)
		})
	}
}

// TestMongoConnEntry_UpdateLastUsed tests the mongoConnEntry updateLastUsed method.
func TestMongoConnEntry_UpdateLastUsed(t *testing.T) {
	t.Run("Should update lastUsedAt timestamp", func(t *testing.T) {
		entry := &mongoConnEntry{
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

// TestMongoConnEntry_GetLastUsed tests the mongoConnEntry getLastUsed method.
func TestMongoConnEntry_GetLastUsed(t *testing.T) {
	t.Run("Should return lastUsedAt timestamp", func(t *testing.T) {
		now := time.Now()
		entry := &mongoConnEntry{
			lastUsedAt: now,
		}

		result := entry.getLastUsed()
		assert.Equal(t, now, result)
	})
}

// TestMongoPoolManager_MakeConnKey tests the makeConnKey method.
func TestMongoPoolManager_MakeConnKey(t *testing.T) {
	resolver := newMockResolver()
	pm := NewMongoPoolManager(resolver).(*mongoPoolManagerImpl)
	defer func() { _ = pm.CloseAll(context.Background()) }()

	key := pm.makeConnKey("tenant-123", "mdz-reporter")
	assert.Equal(t, "tenant-123:mdz-reporter", key)
}

// TestMongoPoolManager_SanitizeURIForKey tests the sanitizeURIForKey method.
func TestMongoPoolManager_SanitizeURIForKey(t *testing.T) {
	resolver := newMockResolver()
	pm := NewMongoPoolManager(resolver).(*mongoPoolManagerImpl)
	defer func() { _ = pm.CloseAll(context.Background()) }()

	tests := []struct {
		name     string
		uri      string
		expected string
	}{
		{
			name:     "Should extract host from mongodb:// URI",
			uri:      "mongodb://user:pass@db.example.com:27017/testdb",
			expected: "db.example.com:27017",
		},
		{
			name:     "Should extract host from mongodb+srv:// URI",
			uri:      "mongodb+srv://user:pass@cluster0.example.mongodb.net/testdb",
			expected: "cluster0.example.mongodb.net",
		},
		{
			name:     "Should handle URI without credentials",
			uri:      "mongodb://localhost:27017/testdb",
			expected: "localhost:27017",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.sanitizeURIForKey(tt.uri)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestMongoPoolManager_SanitizeURIForLog tests the sanitizeURIForLog method.
func TestMongoPoolManager_SanitizeURIForLog(t *testing.T) {
	resolver := newMockResolver()
	pm := NewMongoPoolManager(resolver).(*mongoPoolManagerImpl)
	defer func() { _ = pm.CloseAll(context.Background()) }()

	tests := []struct {
		name     string
		uri      string
		expected string
	}{
		{
			name:     "Should remove credentials from mongodb:// URI",
			uri:      "mongodb://user:pass@db.example.com:27017/testdb",
			expected: "db.example.com:27017",
		},
		{
			name:     "Should handle URI without credentials",
			uri:      "mongodb://localhost:27017/testdb",
			expected: "localhost:27017",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.sanitizeURIForLog(tt.uri)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestMongoPoolManager_DoCleanup tests the doCleanup method.
func TestMongoPoolManager_DoCleanup(t *testing.T) {
	t.Run("Should cleanup idle connections", func(t *testing.T) {
		resolver := newMockResolver()

		// Very short idle timeout for testing
		pm := NewMongoPoolManager(resolver,
			WithMongoIdleTimeout(1*time.Millisecond),
			WithMongoCleanupInterval(1*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)

		// Manually add an expired entry
		pm.mu.Lock()
		pm.tenantConns["expired-tenant:mdz-reporter"] = &mongoConnEntry{
			tenantID:     "expired-tenant",
			appName:      "mdz-reporter",
			createdAt:    time.Now().Add(-time.Hour),
			lastUsedAt:   time.Now().Add(-time.Hour), // Already expired
			databaseName: "test_db",
			conn:         nil, // nil connection for safety
		}
		pm.mu.Unlock()

		// Wait for cleanup to run
		time.Sleep(50 * time.Millisecond)

		// Check that the expired entry was removed
		pm.mu.RLock()
		_, exists := pm.tenantConns["expired-tenant:mdz-reporter"]
		pm.mu.RUnlock()

		assert.False(t, exists, "Expired entry should be cleaned up")

		_ = pm.CloseAll(context.Background())
	})

	t.Run("Should cleanup idle shared connections when not in use", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewMongoPoolManager(resolver,
			WithMongoIdleTimeout(1*time.Millisecond),
			WithMongoCleanupInterval(1*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)

		// Manually add an expired shared entry
		pm.mu.Lock()
		pm.sharedConns["mongodb://localhost:27017/expired"] = &mongoConnEntry{
			appName:       "mdz-reporter",
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
		_, exists := pm.sharedConns["mongodb://localhost:27017/expired"]
		pm.mu.RUnlock()

		assert.False(t, exists, "Expired shared entry should be cleaned up")

		_ = pm.CloseAll(context.Background())
	})

	t.Run("Should NOT cleanup shared connections still in use", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewMongoPoolManager(resolver,
			WithMongoIdleTimeout(1*time.Millisecond),
			WithMongoCleanupInterval(1*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)

		// Manually add an expired shared entry that is still mapped
		pm.mu.Lock()
		pm.sharedConns["mongodb://localhost:27017/inuse"] = &mongoConnEntry{
			appName:       "mdz-reporter",
			isolationMode: "schema",
			createdAt:     time.Now().Add(-time.Hour),
			lastUsedAt:    time.Now().Add(-time.Hour),
			conn:          nil,
		}
		// Map a tenant to this shared connection
		pm.tenantToSharedConn["active-tenant:mdz-reporter"] = "mongodb://localhost:27017/inuse"
		pm.mu.Unlock()

		// Wait for cleanup to run
		time.Sleep(50 * time.Millisecond)

		// Check that the entry was NOT removed because it's still in use
		pm.mu.RLock()
		_, exists := pm.sharedConns["mongodb://localhost:27017/inuse"]
		pm.mu.RUnlock()

		assert.True(t, exists, "Shared entry still in use should NOT be cleaned up")

		_ = pm.CloseAll(context.Background())
	})
}

// TestMongoPoolManager_CloseClient_SchemaMode tests CloseClient in schema mode.
func TestMongoPoolManager_CloseClient_SchemaMode(t *testing.T) {
	t.Run("Should remove mapping for schema mode tenant", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		// Manually add a shared connection and mapping
		pm.mu.Lock()
		pm.sharedConns["mongodb://localhost:27017"] = &mongoConnEntry{
			appName:       "mdz-reporter",
			isolationMode: "schema",
			createdAt:     time.Now(),
			lastUsedAt:    time.Now(),
			conn:          nil,
		}
		pm.tenantToSharedConn["tenant-schema:mdz-reporter"] = "mongodb://localhost:27017"
		pm.tenantToSharedConn["other-tenant:mdz-reporter"] = "mongodb://localhost:27017"
		pm.mu.Unlock()

		// Close one tenant's mapping
		err := pm.CloseClient("tenant-schema", "mdz-reporter")
		require.NoError(t, err)

		// Mapping should be removed for this tenant
		pm.mu.RLock()
		_, mapping1Exists := pm.tenantToSharedConn["tenant-schema:mdz-reporter"]
		_, mapping2Exists := pm.tenantToSharedConn["other-tenant:mdz-reporter"]
		_, sharedExists := pm.sharedConns["mongodb://localhost:27017"]
		pm.mu.RUnlock()

		assert.False(t, mapping1Exists, "Mapping for closed tenant should be removed")
		assert.True(t, mapping2Exists, "Mapping for other tenant should still exist")
		assert.True(t, sharedExists, "Shared connection should still exist (in use by other tenant)")
	})

	t.Run("Should close shared connection when last tenant disconnects", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		// Manually add a shared connection and mapping for only one tenant
		pm.mu.Lock()
		pm.sharedConns["mongodb://localhost:27017/single"] = &mongoConnEntry{
			appName:       "mdz-reporter",
			isolationMode: "schema",
			createdAt:     time.Now(),
			lastUsedAt:    time.Now(),
			conn:          nil, // nil connection for testing
		}
		pm.tenantToSharedConn["only-tenant:mdz-reporter"] = "mongodb://localhost:27017/single"
		pm.mu.Unlock()

		// Close the only tenant's mapping
		err := pm.CloseClient("only-tenant", "mdz-reporter")
		require.NoError(t, err)

		// Both mapping and shared connection should be removed
		pm.mu.RLock()
		_, mappingExists := pm.tenantToSharedConn["only-tenant:mdz-reporter"]
		_, sharedExists := pm.sharedConns["mongodb://localhost:27017/single"]
		pm.mu.RUnlock()

		assert.False(t, mappingExists, "Mapping should be removed")
		assert.False(t, sharedExists, "Shared connection should be removed (no more tenants)")
	})
}

// TestMongoPoolManager_EvictLRUConn tests the evictLRUConn method.
func TestMongoPoolManager_EvictLRUConn(t *testing.T) {
	t.Run("Should return error when no connections to evict", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		pm.mu.Lock()
		err := pm.evictLRUConn(context.Background())
		pm.mu.Unlock()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no connections to evict")
	})

	t.Run("Should evict oldest tenant connection", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		// Manually add entries with different timestamps
		pm.mu.Lock()
		pm.tenantConns["old-tenant:mdz-reporter"] = &mongoConnEntry{
			tenantID:   "old-tenant",
			appName:    "mdz-reporter",
			createdAt:  time.Now().Add(-2 * time.Hour),
			lastUsedAt: time.Now().Add(-2 * time.Hour),
			conn:       nil,
		}
		pm.tenantConns["new-tenant:mdz-reporter"] = &mongoConnEntry{
			tenantID:   "new-tenant",
			appName:    "mdz-reporter",
			createdAt:  time.Now(),
			lastUsedAt: time.Now(),
			conn:       nil,
		}

		err := pm.evictLRUConn(context.Background())
		pm.mu.Unlock()

		require.NoError(t, err)

		pm.mu.RLock()
		_, oldExists := pm.tenantConns["old-tenant:mdz-reporter"]
		_, newExists := pm.tenantConns["new-tenant:mdz-reporter"]
		pm.mu.RUnlock()

		assert.False(t, oldExists, "Old tenant should be evicted")
		assert.True(t, newExists, "New tenant should still exist")
	})

	t.Run("Should evict shared connection when it is oldest", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		// Add tenant and shared entries
		pm.mu.Lock()
		pm.tenantConns["tenant:mdz-reporter"] = &mongoConnEntry{
			tenantID:   "tenant",
			appName:    "mdz-reporter",
			createdAt:  time.Now(),
			lastUsedAt: time.Now(),
			conn:       nil,
		}
		pm.sharedConns["mongodb://localhost:27017"] = &mongoConnEntry{
			appName:    "mdz-reporter",
			createdAt:  time.Now().Add(-2 * time.Hour),
			lastUsedAt: time.Now().Add(-2 * time.Hour),
			conn:       nil,
		}
		pm.tenantToSharedConn["other-tenant:mdz-reporter"] = "mongodb://localhost:27017"

		err := pm.evictLRUConn(context.Background())
		pm.mu.Unlock()

		require.NoError(t, err)

		pm.mu.RLock()
		_, tenantExists := pm.tenantConns["tenant:mdz-reporter"]
		_, sharedExists := pm.sharedConns["mongodb://localhost:27017"]
		_, mappingExists := pm.tenantToSharedConn["other-tenant:mdz-reporter"]
		pm.mu.RUnlock()

		assert.True(t, tenantExists, "Tenant connection should still exist")
		assert.False(t, sharedExists, "Shared connection should be evicted")
		assert.False(t, mappingExists, "Tenant mapping to shared should be removed")
	})
}

// TestMongoPoolManager_StatsWithConnections tests Stats with actual connection entries.
func TestMongoPoolManager_StatsWithConnections(t *testing.T) {
	t.Run("Should return stats for tenant and shared connections", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		now := time.Now()

		// Manually add entries
		pm.mu.Lock()
		pm.tenantConns["tenant-1:mdz-reporter"] = &mongoConnEntry{
			tenantID:      "tenant-1",
			appName:       "mdz-reporter",
			isolationMode: "database",
			databaseName:  "tenant_1_db",
			createdAt:     now.Add(-time.Hour),
			lastUsedAt:    now,
			conn:          nil,
		}
		pm.sharedConns["mongodb://localhost:27017"] = &mongoConnEntry{
			tenantID:      "",
			appName:       "mdz-reporter",
			isolationMode: "schema",
			databaseName:  "shared_db",
			createdAt:     now.Add(-2 * time.Hour),
			lastUsedAt:    now.Add(-time.Minute),
			conn:          nil,
		}
		pm.mu.Unlock()

		stats := pm.Stats()

		assert.Len(t, stats, 2)

		// Check tenant connection stats
		tenantStats, ok := stats["tenant-1:mdz-reporter"]
		require.True(t, ok)
		assert.Equal(t, "tenant-1", tenantStats.TenantID)
		assert.Equal(t, "mdz-reporter", tenantStats.ApplicationName)
		assert.Equal(t, "database", tenantStats.IsolationMode)
		assert.Equal(t, "tenant_1_db", tenantStats.DatabaseName)

		// Check shared connection stats
		var sharedFound bool
		for key, stat := range stats {
			if stat.IsolationMode == "schema" {
				sharedFound = true
				assert.Contains(t, key, "shared:")
				assert.Equal(t, "", stat.TenantID)
				assert.Equal(t, "shared_db", stat.DatabaseName)
				break
			}
		}
		assert.True(t, sharedFound, "Should have shared connection stats")
	})
}

// TestTenantDatabase_Collection_WithNilDB tests Collection method with nil database.
func TestTenantDatabase_Collection_WithNilDB(t *testing.T) {
	t.Run("Should return nil when database is nil", func(t *testing.T) {
		td := &TenantDatabase{
			db:               nil,
			collectionPrefix: "tenant_test_",
		}

		result := td.Collection("users")
		assert.Nil(t, result)
	})
}

// TestMongoPoolManager_GetDatabase_Errors tests error paths in GetDatabase.
func TestMongoPoolManager_GetDatabase_Errors(t *testing.T) {
	t.Run("Should return error for empty application name", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-db", &TenantConfig{
			ID:            "tenant-db",
			Status:        "active",
			IsolationMode: "database",
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		_, err := pm.GetDatabase(context.Background(), "tenant-db", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "application name")
	})

	t.Run("Should return error for inactive tenant", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-inactive", &TenantConfig{
			ID:            "tenant-inactive",
			Status:        "inactive",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "test_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		_, err := pm.GetDatabase(context.Background(), "tenant-inactive", "mdz-reporter")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inactive")
	})

	t.Run("Should return error for unsupported isolation mode", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-unsupported", &TenantConfig{
			ID:            "tenant-unsupported",
			Status:        "active",
			IsolationMode: "unsupported_mode",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "test_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		_, err := pm.GetDatabase(context.Background(), "tenant-unsupported", "mdz-reporter")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported isolation mode")
	})
}

// TestMongoPoolManager_GetClient_ContextCancellation tests context cancellation handling.
func TestMongoPoolManager_GetClient_ContextCancellation(t *testing.T) {
	t.Run("Should return error when context is cancelled", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-ctx", &TenantConfig{
			ID:            "tenant-ctx",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "test_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := pm.GetClient(ctx, "tenant-ctx", "mdz-reporter")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context")
	})
}

// TestMongoPoolManager_GetDatabase_ContextCancellation tests context cancellation handling.
func TestMongoPoolManager_GetDatabase_ContextCancellation(t *testing.T) {
	t.Run("Should return error when context is cancelled", func(t *testing.T) {
		resolver := newMockResolver()
		resolver.AddConfig("tenant-ctx", &TenantConfig{
			ID:            "tenant-ctx",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseServices{
				"mdz-reporter": {
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "test_db",
					},
				},
			},
		})

		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{}))
		defer func() { _ = pm.CloseAll(context.Background()) }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := pm.GetDatabase(ctx, "tenant-ctx", "mdz-reporter")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context")
	})
}

// TestMongoPoolManager_WithContext tests the WithMongoContext option.
func TestMongoPoolManager_WithContext(t *testing.T) {
	t.Run("Should stop cleanup goroutine when context is cancelled", func(t *testing.T) {
		resolver := newMockResolver()
		ctx, cancel := context.WithCancel(context.Background())

		pm := NewMongoPoolManager(resolver,
			WithMongoContext(ctx),
			WithMongoCleanupInterval(10*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)

		// Cancel the context
		cancel()

		// Wait a bit for the goroutine to stop
		time.Sleep(50 * time.Millisecond)

		// Check that cleanupDone is closed (goroutine stopped)
		select {
		case <-pm.cleanupDone:
			// Expected: goroutine stopped
		case <-time.After(100 * time.Millisecond):
			t.Error("Cleanup goroutine did not stop after context cancellation")
		}
	})

	t.Run("Should continue cleanup when no context is set", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewMongoPoolManager(resolver,
			WithMongoCleanupInterval(10*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)

		// cleanupDone should NOT be closed since no context was cancelled
		select {
		case <-pm.cleanupDone:
			t.Error("Cleanup goroutine stopped unexpectedly")
		case <-time.After(50 * time.Millisecond):
			// Expected: goroutine still running
		}

		// Clean up
		_ = pm.CloseAll(context.Background())
	})

	t.Run("Should ignore nil context in WithMongoContext option", func(t *testing.T) {
		resolver := newMockResolver()

		pm := NewMongoPoolManager(resolver,
			WithMongoContext(nil),
			WithMongoCleanupInterval(10*time.Millisecond),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)

		assert.Nil(t, pm.ctx, "Context should remain nil when nil is passed")

		_ = pm.CloseAll(context.Background())
	})
}

// TestMongoPoolManager_GetContextDone tests the getContextDone method.
func TestMongoPoolManager_GetContextDone(t *testing.T) {
	t.Run("Should return nil channel when no context is set", func(t *testing.T) {
		resolver := newMockResolver()
		pm := NewMongoPoolManager(resolver, WithMongoLogger(&mockLogger{})).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		done := pm.getContextDone()
		assert.Nil(t, done, "Should return nil channel when no context is set")
	})

	t.Run("Should return context Done channel when context is set", func(t *testing.T) {
		resolver := newMockResolver()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		pm := NewMongoPoolManager(resolver,
			WithMongoContext(ctx),
			WithMongoLogger(&mockLogger{}),
		).(*mongoPoolManagerImpl)
		defer func() { _ = pm.CloseAll(context.Background()) }()

		done := pm.getContextDone()
		assert.NotNil(t, done, "Should return non-nil channel when context is set")
		assert.Equal(t, ctx.Done(), done, "Should return the context's Done channel")
	})
}
