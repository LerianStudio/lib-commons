package poolmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMiddleware_NewMiddleware tests middleware constructor.
func TestMiddleware_NewMiddleware(t *testing.T) {
	t.Run("Should create middleware with valid config and resolver", func(t *testing.T) {
		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  "http://pool-manager:8080",
			CacheTTL:        24 * time.Hour,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		middleware := NewMiddleware(config, resolver)

		require.NotNil(t, middleware)
	})

	t.Run("Should return nil if config is nil", func(t *testing.T) {
		resolver := NewResolver("http://pool-manager:8080")
		middleware := NewMiddleware(nil, resolver)

		assert.Nil(t, middleware)
	})

	t.Run("Should return nil if resolver is nil", func(t *testing.T) {
		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  "http://pool-manager:8080",
		}

		middleware := NewMiddleware(config, nil)

		assert.Nil(t, middleware)
	})
}

// TestMiddleware_Bypass tests the bypass logic when multi-tenant is disabled.
func TestMiddleware_Bypass(t *testing.T) {
	t.Run("Should bypass middleware when Enabled is false", func(t *testing.T) {
		config := &Config{
			Enabled:         false,
			ApplicationName: "midaz",
			PoolManagerURL:  "http://pool-manager:8080",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// TestMiddleware_HealthEndpointSkip tests that health endpoints are skipped.
func TestMiddleware_HealthEndpointSkip(t *testing.T) {
	skipPaths := []string{"/health", "/ready", "/metrics", "/livez", "/readyz"}

	for _, path := range skipPaths {
		t.Run(fmt.Sprintf("Should skip %s endpoint", path), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(TenantConfig{
					ID:         "tenant-123",
					TenantName: "Test Tenant",
					Status:     "active",
				})
			}))
			defer server.Close()

			config := &Config{
				Enabled:         true,
				ApplicationName: "midaz",
				PoolManagerURL:  server.URL,
				TenantClaimKey:  "owner",
			}

			resolver := NewResolver(config.PoolManagerURL)
			mw := NewMiddleware(config, resolver)
			require.NotNil(t, mw)

			app := fiber.New()
			app.Use(mw.Handler())
			app.Get(path, func(c *fiber.Ctx) error {
				return c.SendString("OK")
			})

			req := httptest.NewRequest(http.MethodGet, path, nil)
			resp, err := app.Test(req)

			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

// TestMiddleware_JWTExtraction tests JWT extraction from Authorization header.
func TestMiddleware_JWTExtraction(t *testing.T) {
	t.Run("Should return 401 when no Authorization header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{ID: "tenant-123"})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("Should return 401 when Authorization header is invalid", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{ID: "tenant-123"})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "invalid-token")
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("Should return 401 when JWT has no owner claim", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{ID: "tenant-123"})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		// JWT without owner claim
		claims := map[string]interface{}{
			"sub":  "user-123",
			"name": "Test User",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("Should extract tenant ID from JWT owner claim", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         "tenant-123",
				TenantName: "Test Tenant",
				Status:     "active",
				Databases: map[string]DatabaseServices{
					"midaz": {
						PostgreSQL: &PostgreSQLConfig{
							Host:     "pg.tenant-123.local",
							Port:     5432,
							Database: "midaz",
							Username: "midaz",
							Password: "secret",
						},
					},
				},
			})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		var extractedTenantID string
		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			extractedTenantID = GetTenantID(c.UserContext())
			return c.SendString("OK")
		})

		// JWT with owner claim
		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "tenant-123", extractedTenantID)
	})
}

// TestMiddleware_TenantResolution tests tenant resolution via Tenant Service.
func TestMiddleware_TenantResolution(t *testing.T) {
	t.Run("Should return 403 when tenant not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "tenant not found"})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "unknown-tenant",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("Should return 403 when tenant is inactive", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         "tenant-123",
				TenantName: "Inactive Tenant",
				Status:     "inactive",
			})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("Should return 403 when tenant is suspended", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:         "tenant-123",
				TenantName: "Suspended Tenant",
				Status:     "suspended",
			})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("Should return 503 when Pool Manager is unavailable", func(t *testing.T) {
		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  "http://localhost:59999", // Invalid port
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}

// TestMiddleware_ContextInjection tests context injection with tenant data.
func TestMiddleware_ContextInjection(t *testing.T) {
	t.Run("Should inject tenant config into context", func(t *testing.T) {
		expectedConfig := TenantConfig{
			ID:         "tenant-123",
			TenantName: "Test Tenant",
			Status:     "active",
			Databases: map[string]DatabaseServices{
				"midaz": {
					PostgreSQL: &PostgreSQLConfig{
						Host:     "pg.tenant-123.local",
						Port:     5432,
						Database: "midaz",
						Username: "midaz",
						Password: "secret",
					},
					MongoDB: &MongoDBConfig{
						URI:      "mongodb://midaz:secret@mongo.tenant-123.local:27017/midaz",
						Database: "midaz",
					},
				},
			},
			IsolationMode: "database",
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedConfig)
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		var extractedConfig *TenantConfig
		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			extractedConfig = GetTenantConfig(c.UserContext())
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.NotNil(t, extractedConfig)
		assert.Equal(t, expectedConfig.ID, extractedConfig.ID)
		assert.Equal(t, expectedConfig.TenantName, extractedConfig.TenantName)
		assert.Equal(t, expectedConfig.Status, extractedConfig.Status)
		assert.Equal(t, expectedConfig.IsolationMode, extractedConfig.IsolationMode)
	})

	t.Run("Should inject PostgreSQL config into context", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     "tenant-123",
				Status: "active",
				Databases: map[string]DatabaseServices{
					"midaz": {
						PostgreSQL: &PostgreSQLConfig{
							Host:     "pg.tenant-123.local",
							Port:     5432,
							Database: "midaz",
							Username: "midaz",
							Password: "secret",
						},
					},
				},
			})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		var pgConfig *PostgreSQLConfig
		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			pgConfig = GetTenantPostgreSQL(c.UserContext())
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.NotNil(t, pgConfig)
		assert.Equal(t, "pg.tenant-123.local", pgConfig.Host)
		assert.Equal(t, 5432, pgConfig.Port)
	})

	t.Run("Should inject MongoDB config into context", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{
				ID:     "tenant-123",
				Status: "active",
				Databases: map[string]DatabaseServices{
					"midaz": {
						MongoDB: &MongoDBConfig{
							URI:      "mongodb://midaz:secret@mongo.tenant-123.local:27017/midaz",
							Database: "midaz",
						},
					},
				},
			})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		var mongoConfig *MongoDBConfig
		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			mongoConfig = GetTenantMongoDB(c.UserContext())
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.NotNil(t, mongoConfig)
		assert.Equal(t, "midaz", mongoConfig.Database)
		assert.Contains(t, mongoConfig.URI, "mongo.tenant-123.local")
	})
}

// TestMiddleware_CustomSkipPaths tests custom skip paths configuration.
func TestMiddleware_CustomSkipPaths(t *testing.T) {
	t.Run("Should skip custom paths", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TenantConfig{ID: "tenant-123", Status: "active"})
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver, WithSkipPaths("/public", "/swagger"))
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/public/docs", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})
		app.Get("/swagger/index.html", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		// Test /public
		req := httptest.NewRequest(http.MethodGet, "/public/docs", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Test /swagger
		req = httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// TestMiddleware_Interface tests the Middleware interface methods.
func TestMiddleware_Interface(t *testing.T) {
	t.Run("Handler should return fiber.Handler", func(t *testing.T) {
		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  "http://pool-manager:8080",
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		handler := mw.Handler()
		assert.NotNil(t, handler)
	})

	t.Run("IsEnabled should return config Enabled state", func(t *testing.T) {
		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  "http://pool-manager:8080",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		assert.True(t, mw.IsEnabled())

		config.Enabled = false
		mw2 := NewMiddleware(config, resolver)
		require.NotNil(t, mw2)
		assert.False(t, mw2.IsEnabled())
	})
}

// TestGetTenantID tests the GetTenantID helper function.
func TestGetTenantID(t *testing.T) {
	t.Run("Should return empty string when context has no tenant ID", func(t *testing.T) {
		ctx := context.Background()
		tenantID := GetTenantID(ctx)
		assert.Empty(t, tenantID)
	})

	t.Run("Should return tenant ID from context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), TenantIDContextKey, "tenant-123")
		tenantID := GetTenantID(ctx)
		assert.Equal(t, "tenant-123", tenantID)
	})
}

// TestGetTenantConfig tests the GetTenantConfig helper function.
func TestGetTenantConfig(t *testing.T) {
	t.Run("Should return nil when context has no tenant config", func(t *testing.T) {
		ctx := context.Background()
		config := GetTenantConfig(ctx)
		assert.Nil(t, config)
	})

	t.Run("Should return tenant config from context", func(t *testing.T) {
		expected := &TenantConfig{
			ID:         "tenant-123",
			TenantName: "Test Tenant",
			Status:     "active",
		}
		ctx := context.WithValue(context.Background(), tenantConfigContextKey, expected)
		config := GetTenantConfig(ctx)
		assert.Equal(t, expected, config)
	})
}

// TestGetTenantPostgreSQL tests the GetTenantPostgreSQL helper function.
func TestGetTenantPostgreSQL(t *testing.T) {
	t.Run("Should return nil when context has no PostgreSQL config", func(t *testing.T) {
		ctx := context.Background()
		pgConfig := GetTenantPostgreSQL(ctx)
		assert.Nil(t, pgConfig)
	})

	t.Run("Should return PostgreSQL config from context", func(t *testing.T) {
		expected := &PostgreSQLConfig{
			Host:     "pg.local",
			Port:     5432,
			Database: "midaz",
		}
		ctx := context.WithValue(context.Background(), tenantPostgreSQLContextKey, expected)
		pgConfig := GetTenantPostgreSQL(ctx)
		assert.Equal(t, expected, pgConfig)
	})
}

// TestGetTenantMongoDB tests the GetTenantMongoDB helper function.
func TestGetTenantMongoDB(t *testing.T) {
	t.Run("Should return nil when context has no MongoDB config", func(t *testing.T) {
		ctx := context.Background()
		mongoConfig := GetTenantMongoDB(ctx)
		assert.Nil(t, mongoConfig)
	})

	t.Run("Should return MongoDB config from context", func(t *testing.T) {
		expected := &MongoDBConfig{
			URI:      "mongodb://mongo.local:27017/midaz",
			Database: "midaz",
		}
		ctx := context.WithValue(context.Background(), tenantMongoDBContextKey, expected)
		mongoConfig := GetTenantMongoDB(ctx)
		assert.Equal(t, expected, mongoConfig)
	})
}

// TestGetTenantID_NilContext tests nil context handling.
func TestGetTenantID_NilContext(t *testing.T) {
	t.Run("Should return empty string when context is nil", func(t *testing.T) {
		tenantID := GetTenantID(nil)
		assert.Empty(t, tenantID)
	})
}

// TestGetTenantConfig_NilContext tests nil context handling.
func TestGetTenantConfig_NilContext(t *testing.T) {
	t.Run("Should return nil when context is nil", func(t *testing.T) {
		config := GetTenantConfig(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when context value is wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), tenantConfigContextKey, "invalid")
		config := GetTenantConfig(ctx)
		assert.Nil(t, config)
	})
}

// TestGetTenantPostgreSQL_NilContext tests nil context handling.
func TestGetTenantPostgreSQL_NilContext(t *testing.T) {
	t.Run("Should return nil when context is nil", func(t *testing.T) {
		config := GetTenantPostgreSQL(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when context value is wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), tenantPostgreSQLContextKey, "invalid")
		config := GetTenantPostgreSQL(ctx)
		assert.Nil(t, config)
	})
}

// TestGetTenantMongoDB_NilContext tests nil context handling.
func TestGetTenantMongoDB_NilContext(t *testing.T) {
	t.Run("Should return nil when context is nil", func(t *testing.T) {
		config := GetTenantMongoDB(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when context value is wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), tenantMongoDBContextKey, "invalid")
		config := GetTenantMongoDB(ctx)
		assert.Nil(t, config)
	})
}

// TestIsConnectionError tests the isConnectionError function.
func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
		{
			name:     "connection refused error returns true",
			err:      fmt.Errorf("connection refused"),
			expected: true,
		},
		{
			name:     "no such host error returns true",
			err:      fmt.Errorf("no such host"),
			expected: true,
		},
		{
			name:     "network is unreachable error returns true",
			err:      fmt.Errorf("network is unreachable"),
			expected: true,
		},
		{
			name:     "dial tcp error returns true",
			err:      fmt.Errorf("dial tcp 127.0.0.1:8080: connection refused"),
			expected: true,
		},
		{
			name:     "i/o timeout error returns true",
			err:      fmt.Errorf("i/o timeout"),
			expected: true,
		},
		{
			name:     "request failed error returns true",
			err:      fmt.Errorf("request failed: connection reset"),
			expected: true,
		},
		{
			name:     "generic error returns false",
			err:      fmt.Errorf("some random error"),
			expected: false,
		},
		{
			name:     "case insensitive matching for CONNECTION REFUSED",
			err:      fmt.Errorf("CONNECTION REFUSED"),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isConnectionError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetTenantConfig_ExportedKey tests the exported context key handling.
func TestGetTenantConfig_ExportedKey(t *testing.T) {
	t.Run("Should return config from exported TenantConfigContextKey", func(t *testing.T) {
		expected := &TenantConfig{
			ID:         "tenant-exported",
			TenantName: "Exported Key Tenant",
			Status:     "active",
		}
		ctx := context.WithValue(context.Background(), TenantConfigContextKey, expected)
		config := GetTenantConfig(ctx)
		assert.Equal(t, expected, config)
	})
}

// TestGetTenantPostgreSQL_ExportedKey tests the exported context key handling.
func TestGetTenantPostgreSQL_ExportedKey(t *testing.T) {
	t.Run("Should return config from exported TenantPGContextKey", func(t *testing.T) {
		expected := &PostgreSQLConfig{
			Host:     "pg.exported.local",
			Port:     5432,
			Database: "exported",
		}
		ctx := context.WithValue(context.Background(), TenantPGContextKey, expected)
		config := GetTenantPostgreSQL(ctx)
		assert.Equal(t, expected, config)
	})
}

// TestGetTenantMongoDB_ExportedKey tests the exported context key handling.
func TestGetTenantMongoDB_ExportedKey(t *testing.T) {
	t.Run("Should return config from exported TenantMongoContextKey", func(t *testing.T) {
		expected := &MongoDBConfig{
			URI:      "mongodb://mongo.exported.local:27017/exported",
			Database: "exported",
		}
		ctx := context.WithValue(context.Background(), TenantMongoContextKey, expected)
		config := GetTenantMongoDB(ctx)
		assert.Equal(t, expected, config)
	})
}

// TestMiddleware_ServerErrorFromResolver tests server error handling from resolver.
func TestMiddleware_ServerErrorFromResolver(t *testing.T) {
	t.Run("Should return 503 when resolver returns server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		config := &Config{
			Enabled:         true,
			ApplicationName: "midaz",
			PoolManagerURL:  server.URL,
			TenantClaimKey:  "owner",
		}

		resolver := NewResolver(config.PoolManagerURL)
		mw := NewMiddleware(config, resolver)
		require.NotNil(t, mw)

		app := fiber.New()
		app.Use(mw.Handler())
		app.Get("/api/test", func(c *fiber.Ctx) error {
			return c.SendString("OK")
		})

		claims := map[string]interface{}{
			"sub":   "user-123",
			"owner": "tenant-123",
		}
		token := createTestJWT(claims)

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)

		require.NoError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})
}
