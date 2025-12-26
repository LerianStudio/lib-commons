package poolmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewResolver tests the constructor with various options.
func TestNewResolver(t *testing.T) {
	tests := []struct {
		name       string
		serviceURL string
		opts       []ResolverOption
		wantErr    bool
		checkFunc  func(t *testing.T, r Resolver)
	}{
		{
			name:       "Should create resolver with default options",
			serviceURL: "http://pool-manager:8080",
			opts:       nil,
			wantErr:    false,
			checkFunc: func(t *testing.T, r Resolver) {
				require.NotNil(t, r, "Resolver should not be nil")
			},
		},
		{
			name:       "Should create resolver with custom TTL",
			serviceURL: "http://pool-manager:8080",
			opts:       []ResolverOption{WithCacheTTL(1 * time.Hour)},
			wantErr:    false,
			checkFunc: func(t *testing.T, r Resolver) {
				require.NotNil(t, r, "Resolver should not be nil")
			},
		},
		{
			name:       "Should create resolver with custom HTTP client",
			serviceURL: "http://pool-manager:8080",
			opts:       []ResolverOption{WithHTTPClient(&http.Client{Timeout: 5 * time.Second})},
			wantErr:    false,
			checkFunc: func(t *testing.T, r Resolver) {
				require.NotNil(t, r, "Resolver should not be nil")
			},
		},
		{
			name:       "Should create resolver with API key",
			serviceURL: "http://pool-manager:8080",
			opts:       []ResolverOption{WithAPIKey("test-api-key")},
			wantErr:    false,
			checkFunc: func(t *testing.T, r Resolver) {
				require.NotNil(t, r, "Resolver should not be nil")
			},
		},
		{
			name:       "Should create resolver with all options",
			serviceURL: "http://pool-manager:8080",
			opts: []ResolverOption{
				WithCacheTTL(12 * time.Hour),
				WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
				WithAPIKey("my-api-key"),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, r Resolver) {
				require.NotNil(t, r, "Resolver should not be nil")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewResolver(tt.serviceURL, tt.opts...)
			if tt.checkFunc != nil {
				tt.checkFunc(t, r)
			}
		})
	}
}

// TestResolver_Resolve tests the basic Resolve method.
func TestResolver_Resolve(t *testing.T) {
	tests := []struct {
		name           string
		tenantID       string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantConfig     *TenantConfig
		wantErr        bool
		errContains    string
	}{
		{
			name:     "Should resolve tenant config successfully",
			tenantID: "tenant-123",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/tenants/tenant-123/config", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(TenantConfig{
					ID:            "tenant-123",
					TenantName:    "Test Tenant",
					Status:        "active",
					IsolationMode: "shared",
				})
			},
			wantConfig: &TenantConfig{
				ID:            "tenant-123",
				TenantName:    "Test Tenant",
				Status:        "active",
				IsolationMode: "shared",
			},
			wantErr: false,
		},
		{
			name:     "Should return error for empty tenant ID",
			tenantID: "",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("Server should not be called for empty tenant ID")
			},
			wantConfig:  nil,
			wantErr:     true,
			errContains: "empty tenant ID",
		},
		{
			name:     "Should return error when tenant not found",
			tenantID: "non-existent",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantConfig:  nil,
			wantErr:     true,
			errContains: "not found",
		},
		{
			name:     "Should return error on server error",
			tenantID: "tenant-500",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantConfig:  nil,
			wantErr:     true,
			errContains: "server error",
		},
		{
			name:     "Should return error on invalid JSON response",
			tenantID: "tenant-bad-json",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("invalid json"))
			},
			wantConfig:  nil,
			wantErr:     true,
			errContains: "decode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			resolver := NewResolver(server.URL)
			config, err := resolver.Resolve(context.Background(), tt.tenantID)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, config)
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				assert.Equal(t, tt.wantConfig.ID, config.ID)
				assert.Equal(t, tt.wantConfig.TenantName, config.TenantName)
				assert.Equal(t, tt.wantConfig.Status, config.Status)
				assert.Equal(t, tt.wantConfig.IsolationMode, config.IsolationMode)
			}
		})
	}
}

// TestResolver_ResolveWithService tests resolving with service filter.
func TestResolver_ResolveWithService(t *testing.T) {
	tests := []struct {
		name           string
		tenantID       string
		serviceName    string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantConfig     *TenantConfig
		wantErr        bool
		errContains    string
	}{
		{
			name:        "Should resolve tenant config with service filter",
			tenantID:    "tenant-123",
			serviceName: "midaz",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/tenants/tenant-123/config", r.URL.Path)
				assert.Equal(t, "midaz", r.URL.Query().Get("service"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(TenantConfig{
					ID:            "tenant-123",
					TenantName:    "Test Tenant",
					Status:        "active",
					IsolationMode: "dedicated",
					Databases: map[string]DatabaseServices{
						"midaz": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "db.example.com",
								Port:     5432,
								Database: "midaz_tenant_123",
								Username: "tenant_123",
								Password: "secret",
							},
						},
					},
				})
			},
			wantConfig: &TenantConfig{
				ID:            "tenant-123",
				TenantName:    "Test Tenant",
				Status:        "active",
				IsolationMode: "dedicated",
				Databases: map[string]DatabaseServices{
					"midaz": {
						PostgreSQL: &PostgreSQLConfig{
							Host:     "db.example.com",
							Port:     5432,
							Database: "midaz_tenant_123",
							Username: "tenant_123",
							Password: "secret",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:        "Should return error for empty service name",
			tenantID:    "tenant-123",
			serviceName: "",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("Server should not be called for empty service name")
			},
			wantConfig:  nil,
			wantErr:     true,
			errContains: "empty service name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			resolver := NewResolver(server.URL)
			config, err := resolver.ResolveWithService(context.Background(), tt.tenantID, tt.serviceName)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, config)
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				assert.Equal(t, tt.wantConfig.ID, config.ID)
				assert.Equal(t, tt.wantConfig.IsolationMode, config.IsolationMode)
				assert.NotNil(t, config.Databases)
			}
		})
	}
}

// TestResolver_Cache tests caching behavior.
func TestResolver_Cache(t *testing.T) {
	t.Run("Should return cached result on second call", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         "tenant-cache-test",
				TenantName: "Cached Tenant",
				Status:     "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		// First call - should hit the server
		config1, err := resolver.Resolve(context.Background(), "tenant-cache-test")
		require.NoError(t, err)
		require.NotNil(t, config1)
		assert.Equal(t, 1, callCount, "First call should hit the server")

		// Second call - should return cached result
		config2, err := resolver.Resolve(context.Background(), "tenant-cache-test")
		require.NoError(t, err)
		require.NotNil(t, config2)
		assert.Equal(t, 1, callCount, "Second call should use cache")

		// Both should return same data
		assert.Equal(t, config1.ID, config2.ID)
		assert.Equal(t, config1.TenantName, config2.TenantName)
	})

	t.Run("Should refresh cache after TTL expires", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         "tenant-ttl-test",
				TenantName: "TTL Tenant",
				Status:     "active",
			})
		}))
		defer server.Close()

		// Very short TTL for testing
		resolver := NewResolver(server.URL, WithCacheTTL(50*time.Millisecond))

		// First call
		_, err := resolver.Resolve(context.Background(), "tenant-ttl-test")
		require.NoError(t, err)
		assert.Equal(t, 1, callCount)

		// Wait for TTL to expire
		time.Sleep(100 * time.Millisecond)

		// Second call - should hit the server again
		_, err = resolver.Resolve(context.Background(), "tenant-ttl-test")
		require.NoError(t, err)
		assert.Equal(t, 2, callCount, "Should refresh cache after TTL expires")
	})

	t.Run("Should cache different tenants separately", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			// Extract tenant ID from URL path
			tenantID := r.URL.Path[len("/tenants/"):]
			tenantID = tenantID[:len(tenantID)-len("/config")]
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         tenantID,
				TenantName: "Tenant " + tenantID,
				Status:     "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		// First tenant
		config1, err := resolver.Resolve(context.Background(), "tenant-1")
		require.NoError(t, err)
		assert.Equal(t, "tenant-1", config1.ID)
		assert.Equal(t, 1, callCount)

		// Second tenant - different tenant, should hit server
		config2, err := resolver.Resolve(context.Background(), "tenant-2")
		require.NoError(t, err)
		assert.Equal(t, "tenant-2", config2.ID)
		assert.Equal(t, 2, callCount)

		// First tenant again - should use cache
		config3, err := resolver.Resolve(context.Background(), "tenant-1")
		require.NoError(t, err)
		assert.Equal(t, "tenant-1", config3.ID)
		assert.Equal(t, 2, callCount, "Should use cached result for tenant-1")
	})
}

// TestResolver_InvalidateCache tests cache invalidation.
func TestResolver_InvalidateCache(t *testing.T) {
	t.Run("Should invalidate cache for specific tenant", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         "tenant-invalidate",
				TenantName: "Invalidate Test",
				Status:     "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		// First call
		_, err := resolver.Resolve(context.Background(), "tenant-invalidate")
		require.NoError(t, err)
		assert.Equal(t, 1, callCount)

		// Second call - cached
		_, err = resolver.Resolve(context.Background(), "tenant-invalidate")
		require.NoError(t, err)
		assert.Equal(t, 1, callCount)

		// Invalidate cache
		resolver.InvalidateCache("tenant-invalidate")

		// Third call - should hit server again
		_, err = resolver.Resolve(context.Background(), "tenant-invalidate")
		require.NoError(t, err)
		assert.Equal(t, 2, callCount, "Should hit server after cache invalidation")
	})

	t.Run("Should invalidate all cache entries", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			tenantID := r.URL.Path[len("/tenants/"):]
			tenantID = tenantID[:len(tenantID)-len("/config")]
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     tenantID,
				Status: "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		// Cache multiple tenants
		_, _ = resolver.Resolve(context.Background(), "tenant-a")
		_, _ = resolver.Resolve(context.Background(), "tenant-b")
		assert.Equal(t, 2, callCount)

		// Verify cached
		_, _ = resolver.Resolve(context.Background(), "tenant-a")
		_, _ = resolver.Resolve(context.Background(), "tenant-b")
		assert.Equal(t, 2, callCount)

		// Invalidate all
		resolver.InvalidateCacheAll()

		// Both should hit server again
		_, _ = resolver.Resolve(context.Background(), "tenant-a")
		_, _ = resolver.Resolve(context.Background(), "tenant-b")
		assert.Equal(t, 4, callCount, "Should hit server for all tenants after InvalidateCacheAll")
	})
}

// TestResolver_CacheInvalidationOnError tests that cache is invalidated on HTTP errors.
func TestResolver_CacheInvalidationOnError(t *testing.T) {
	t.Run("Should not cache error responses", func(t *testing.T) {
		callCount := 0
		returnError := true
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if returnError {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     "tenant-error-test",
				Status: "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		// First call - error
		_, err := resolver.Resolve(context.Background(), "tenant-error-test")
		require.Error(t, err)
		assert.Equal(t, 1, callCount)

		// Second call - should hit server again (error not cached)
		_, err = resolver.Resolve(context.Background(), "tenant-error-test")
		require.Error(t, err)
		assert.Equal(t, 2, callCount, "Error responses should not be cached")

		// Now return success
		returnError = false
		_, err = resolver.Resolve(context.Background(), "tenant-error-test")
		require.NoError(t, err)
		assert.Equal(t, 3, callCount)

		// Success should be cached
		_, err = resolver.Resolve(context.Background(), "tenant-error-test")
		require.NoError(t, err)
		assert.Equal(t, 3, callCount, "Success response should be cached")
	})
}

// TestResolver_ContextCancellation tests behavior with cancelled context.
func TestResolver_ContextCancellation(t *testing.T) {
	t.Run("Should return error when context is cancelled", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate slow response
			time.Sleep(500 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		resolver := NewResolver(server.URL)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := resolver.Resolve(ctx, "tenant-cancel")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context canceled")
	})

	t.Run("Should return error when context times out", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate slow response
			time.Sleep(500 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		resolver := NewResolver(server.URL)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		_, err := resolver.Resolve(ctx, "tenant-timeout")
		require.Error(t, err)
		// Error could be context deadline exceeded or timeout
	})
}

// TestResolver_APIKey tests that API key is sent in request.
func TestResolver_APIKey(t *testing.T) {
	t.Run("Should send API key in Authorization header", func(t *testing.T) {
		var receivedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     "tenant-api-key",
				Status: "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithAPIKey("my-secret-key"))
		_, err := resolver.Resolve(context.Background(), "tenant-api-key")
		require.NoError(t, err)
		assert.Equal(t, "Bearer my-secret-key", receivedAuth)
	})
}

// TestResolver_ConcurrentAccess tests thread safety.
func TestResolver_ConcurrentAccess(t *testing.T) {
	t.Run("Should handle concurrent requests safely", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Small delay to increase chance of race conditions
			time.Sleep(10 * time.Millisecond)
			tenantID := r.URL.Path[len("/tenants/"):]
			tenantID = tenantID[:len(tenantID)-len("/config")]
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     tenantID,
				Status: "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		var wg sync.WaitGroup
		errors := make(chan error, 100)

		// Launch many concurrent requests
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				tenantID := "tenant-concurrent"
				if id%2 == 0 {
					tenantID = "tenant-concurrent-2"
				}
				_, err := resolver.Resolve(context.Background(), tenantID)
				if err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			t.Errorf("Concurrent access error: %v", err)
		}
	})
}

// TestResolver_Interface verifies the interface is implemented correctly.
func TestResolver_Interface(t *testing.T) {
	// Compile-time check that resolverImpl implements Resolver
	var _ Resolver = (*resolverImpl)(nil)
}

// TestResolver_InvalidateCache_WithServicePrefix tests cache invalidation with service-prefixed entries.
func TestResolver_InvalidateCache_WithServicePrefix(t *testing.T) {
	t.Run("Should invalidate service-specific cache entries", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			tenantID := "tenant-svc"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     tenantID,
				Status: "active",
			})
		}))
		defer server.Close()

		resolver := NewResolver(server.URL, WithCacheTTL(1*time.Hour))

		// Cache entries with service prefix
		_, _ = resolver.ResolveWithService(context.Background(), "tenant-svc", "midaz")
		_, _ = resolver.ResolveWithService(context.Background(), "tenant-svc", "reporter")
		assert.Equal(t, 2, callCount)

		// Verify cached
		_, _ = resolver.ResolveWithService(context.Background(), "tenant-svc", "midaz")
		assert.Equal(t, 2, callCount, "Should use cache")

		// Invalidate cache for tenant (should also invalidate service-prefixed entries)
		resolver.InvalidateCache("tenant-svc")

		// Both service entries should require new fetch
		_, _ = resolver.ResolveWithService(context.Background(), "tenant-svc", "midaz")
		_, _ = resolver.ResolveWithService(context.Background(), "tenant-svc", "reporter")
		assert.Equal(t, 4, callCount, "Should fetch again after invalidation")
	})
}

// TestResolver_FetchConfig_UnexpectedStatus tests unexpected HTTP status handling.
func TestResolver_FetchConfig_UnexpectedStatus(t *testing.T) {
	t.Run("Should return error for unexpected status code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden) // Unexpected status
		}))
		defer server.Close()

		resolver := NewResolver(server.URL)
		_, err := resolver.Resolve(context.Background(), "tenant-forbidden")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status")
	})
}

// TestTenantConfig_Types tests the TenantConfig types.
func TestTenantConfig_Types(t *testing.T) {
	t.Run("Should marshal and unmarshal TenantConfig correctly", func(t *testing.T) {
		original := TenantConfig{
			ID:            "tenant-123",
			TenantName:    "Test Tenant",
			Status:        "active",
			IsolationMode: "dedicated",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "db.example.com",
						Port:     5432,
						Database: "midaz_db",
						Username: "user",
						Password: "pass",
						SSLMode:  "require",
					},
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://localhost:27017",
						Database: "midaz_mongo",
					},
				},
			},
			Valkey: &ValkeyConfig{
				Addresses: []string{"valkey:6379"},
				Password:  "valkey-pass",
				DB:        0,
			},
			RabbitMQ: &RabbitMQConfig{
				URL: "amqp://guest:guest@localhost:5672/",
			},
		}

		// Marshal
		data, err := json.Marshal(original)
		require.NoError(t, err)

		// Unmarshal
		var decoded TenantConfig
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		// Verify
		assert.Equal(t, original.ID, decoded.ID)
		assert.Equal(t, original.TenantName, decoded.TenantName)
		assert.Equal(t, original.Status, decoded.Status)
		assert.Equal(t, original.IsolationMode, decoded.IsolationMode)
		require.NotNil(t, decoded.Databases["midaz"])
		require.NotNil(t, decoded.Databases["midaz"].PostgreSQL)
		assert.Equal(t, "db.example.com", decoded.Databases["midaz"].PostgreSQL.Host)
		require.NotNil(t, decoded.Valkey)
		assert.Equal(t, []string{"valkey:6379"}, decoded.Valkey.Addresses)
		require.NotNil(t, decoded.RabbitMQ)
		assert.Equal(t, original.RabbitMQ.URL, decoded.RabbitMQ.URL)
	})
}
