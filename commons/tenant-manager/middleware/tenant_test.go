package middleware

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/postgres"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManagers creates a postgres and mongo Manager backed by a test client.
// Centralises the repeated client.NewClient + NewManager boilerplate so each
// sub-test only declares what is unique to its scenario.
func newTestManagers(t testing.TB) (*tmpostgres.Manager, *tmmongo.Manager) {
	t.Helper()
	c, err := client.NewClient("http://localhost:8080", nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)
	return tmpostgres.NewManager(c, "ledger"), tmmongo.NewManager(c, "ledger")
}

func TestNewTenantMiddleware(t *testing.T) {
	t.Run("creates disabled middleware when no managers are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()

		assert.NotNil(t, middleware)
		assert.False(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with PostgreSQL only", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		middleware := NewTenantMiddleware(WithPG(pgManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with MongoDB only", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		middleware := NewTenantMiddleware(WithMB(mongoManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})

	t.Run("creates middleware with both PostgreSQL and MongoDB managers", func(t *testing.T) {
		pgManager, mongoManager := newTestManagers(t)

		middleware := NewTenantMiddleware(
			WithPG(pgManager),
			WithMB(mongoManager),
		)

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})
}

func TestWithPG_NoModule(t *testing.T) {
	t.Run("sets postgres manager on middleware in single-manager mode", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.postgres)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithPG(pgManager)
		opt(middleware)

		assert.Equal(t, pgManager, middleware.postgres)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.pgModules)
	})

	t.Run("enables middleware when postgres manager is set", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithPG(pgManager)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestWithMB_NoModule(t *testing.T) {
	t.Run("sets mongo manager on middleware in single-manager mode", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.mongo)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithMB(mongoManager)
		opt(middleware)

		assert.Equal(t, mongoManager, middleware.mongo)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.mongoModules)
	})

	t.Run("enables middleware when mongo manager is set", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithMB(mongoManager)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestTenantMiddleware_Enabled(t *testing.T) {
	t.Run("returns false when no managers are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()
		assert.False(t, middleware.Enabled())
	})

	t.Run("returns true when only PostgreSQL manager is set", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		middleware := NewTenantMiddleware(WithPG(pgManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when only MongoDB manager is set", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		middleware := NewTenantMiddleware(WithMB(mongoManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when both managers are set", func(t *testing.T) {
		pgManager, mongoManager := newTestManagers(t)

		middleware := NewTenantMiddleware(
			WithPG(pgManager),
			WithMB(mongoManager),
		)
		assert.True(t, middleware.Enabled())
	})
}

// buildTestJWT constructs a minimal unsigned JWT token string from the given claims.
// The token is not cryptographically signed (signature is empty), which is acceptable
// because the middleware uses ParseUnverified (lib-auth already validated the token).
func buildTestJWT(t testing.TB, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))

	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

	return header + "." + encodedPayload + "."
}

// simulateAuthMiddleware returns a Fiber handler that sets c.Locals("user_id")
// to simulate upstream lib-auth middleware having validated the request.
// hasUpstreamAuthAssertion checks c.Locals("user_id"), not HTTP headers.
func simulateAuthMiddleware(userID string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals("user_id", userID)
		return c.Next()
	}
}

func TestTenantMiddleware_WithTenantDB(t *testing.T) {
	t.Run("no Authorization header returns 401", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		middleware := NewTenantMiddleware(WithPG(pgManager))

		app := fiber.New()
		app.Use(middleware.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Unauthorized")
	})

	t.Run("malformed JWT returns 401", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		middleware := NewTenantMiddleware(WithMB(mongoManager))

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Use(middleware.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Unauthorized")
	})

	t.Run("valid JWT missing tenantId claim returns 401", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		middleware := NewTenantMiddleware(WithPG(pgManager))

		token := buildTestJWT(t, map[string]any{
			"sub":   "user-123",
			"email": "test@example.com",
		})

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Use(middleware.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Unauthorized")
	})

	t.Run("valid JWT with tenantId calls next handler", func(t *testing.T) {
		// Create an enabled middleware with no real managers configured.
		// Both postgres and mongo pointers remain nil, so the middleware skips
		// DB resolution and proceeds to c.Next() after JWT parsing.
		middleware := &TenantMiddleware{enabled: true}

		token := buildTestJWT(t, map[string]any{
			"sub":      "user-123",
			"tenantId": "tenant-abc",
		})

		var capturedTenantID string
		nextCalled := false

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Use(middleware.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			nextCalled = true
			capturedTenantID = core.GetTenantIDContext(c.UserContext())
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, nextCalled, "next handler should have been called")
		assert.Equal(t, "tenant-abc", capturedTenantID, "tenantId should be injected in context")
	})
}

// --- Cache + Lazy-Load tests ---

// setupCacheTestServer creates an httptest server that serves tenant config for cache tests.
// It counts requests to verify whether the loader actually made HTTP calls.
func setupCacheTestServer(
	t *testing.T,
	tenantID string,
	config *core.TenantConfig,
	requestCounter *atomic.Int64,
) *httptest.Server {
	t.Helper()

	const testServiceName = "test-service"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/tenants/" + tenantID + "/associations/" + testServiceName + "/connections"

		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == expectedPath && r.Method == http.MethodGet {
			if requestCounter != nil {
				requestCounter.Add(1)
			}

			if err := json.NewEncoder(w).Encode(config); err != nil {
				t.Errorf("failed to encode config: %v", err)
			}

			return
		}

		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))

	t.Cleanup(func() { server.Close() })

	return server
}

// setupCacheErrorServer creates an httptest server that returns a specific HTTP error.
func setupCacheErrorServer(
	t *testing.T,
	tenantID string,
	statusCode int,
	responseBody any,
) *httptest.Server {
	t.Helper()

	const testServiceName = "test-service"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/tenants/" + tenantID + "/associations/" + testServiceName + "/connections"

		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == expectedPath && r.Method == http.MethodGet {
			w.WriteHeader(statusCode)

			if responseBody != nil {
				_ = json.NewEncoder(w).Encode(responseBody)
			}

			return
		}

		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))

	t.Cleanup(func() { server.Close() })

	return server
}

// newCacheTestClient creates a test client pointing at the given server URL.
func newCacheTestClient(t *testing.T, serverURL string) *client.Client {
	t.Helper()

	c, err := client.NewClient(
		serverURL,
		log.NewNop(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-api-key"),
	)
	require.NoError(t, err)

	t.Cleanup(func() { c.Close() })

	return c
}

// newTestTenantConfig creates a TenantConfig with essential fields for cache tests.
func newTestTenantConfig(tenantID string) *core.TenantConfig {
	return &core.TenantConfig{
		ID: tenantID, TenantSlug: tenantID + "-slug", TenantName: "Test Tenant",
		Service: "test-service", Status: "active", IsolationMode: "database",
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {PostgreSQL: &core.PostgreSQLConfig{
				Host: "localhost", Port: 5432, Database: tenantID + "_db",
				Username: "user", Password: "pass", SSLMode: "disable",
			}},
		},
	}
}

func TestNewTenantMiddleware_WithCacheOptions(t *testing.T) {
	t.Run("sets cache and loader fields via options", func(t *testing.T) {
		cache := tenantcache.NewTenantCache()

		tenantID := "tenant-opt-test"
		config := newTestTenantConfig(tenantID)

		server := setupCacheTestServer(t, tenantID, config, nil)
		pmClient := newCacheTestClient(t, server.URL)

		loader := tenantcache.NewTenantLoader(
			pmClient, cache, "test-service",
			tenantcache.DefaultTenantCacheTTL, log.NewNop(),
		)

		mid := NewTenantMiddleware(
			WithTenantCache(cache),
			WithTenantLoader(loader),
		)

		assert.NotNil(t, mid)
		assert.Equal(t, cache, mid.cache)
		assert.Equal(t, loader, mid.loader)
	})

	t.Run("cache and loader are nil by default", func(t *testing.T) {
		mid := NewTenantMiddleware()

		assert.Nil(t, mid.cache)
		assert.Nil(t, mid.loader)
	})
}

func TestWithTenantDB_CacheHit_SkipsLazyLoad(t *testing.T) {
	tenantID := "tenant-cache-hit"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupCacheTestServer(t, tenantID, config, &requestCount)
	pmClient := newCacheTestClient(t, server.URL)

	cache := tenantcache.NewTenantCache()
	loader := tenantcache.NewTenantLoader(
		pmClient, cache, "test-service",
		tenantcache.DefaultTenantCacheTTL, log.NewNop(),
	)

	// Pre-populate cache so it is a HIT
	cache.Set(tenantID, config, 1*time.Hour)

	// Build middleware with cache+loader but no DB managers (enabled=true via direct struct).
	// The middleware should check cache, find the tenant, and proceed to c.Next() without calling loader.
	mid := &TenantMiddleware{
		enabled: true,
		cache:   cache,
		loader:  loader,
	}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": tenantID,
	})

	var capturedTenantID string

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c *fiber.Ctx) error {
		capturedTenantID = core.GetTenantIDContext(c.UserContext())
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, tenantID, capturedTenantID, "tenantId should be in context")
	assert.Equal(t, int64(0), requestCount.Load(),
		"no HTTP call should be made when tenant is in cache")
}

func TestWithTenantDB_CacheMiss_LazyLoads(t *testing.T) {
	tenantID := "tenant-cache-miss"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupCacheTestServer(t, tenantID, config, &requestCount)
	pmClient := newCacheTestClient(t, server.URL)

	cache := tenantcache.NewTenantCache()
	loader := tenantcache.NewTenantLoader(
		pmClient, cache, "test-service",
		tenantcache.DefaultTenantCacheTTL, log.NewNop(),
	)

	// Cache is empty -- this is a cache MISS

	mid := &TenantMiddleware{
		enabled: true,
		cache:   cache,
		loader:  loader,
	}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": tenantID,
	})

	var capturedTenantID string

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c *fiber.Ctx) error {
		capturedTenantID = core.GetTenantIDContext(c.UserContext())
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, tenantID, capturedTenantID, "tenantId should be in context after lazy-load")
	assert.Equal(t, int64(1), requestCount.Load(),
		"exactly 1 HTTP call should be made for cache miss lazy-load")

	// Verify tenant is now in cache
	entry, ok := cache.Get(tenantID)
	assert.True(t, ok, "tenant should be cached after lazy-load")
	assert.Equal(t, tenantID, entry.Config.ID)
}

func TestWithTenantDB_CacheExpired_LazyLoads(t *testing.T) {
	tenantID := "tenant-cache-expired"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupCacheTestServer(t, tenantID, config, &requestCount)
	pmClient := newCacheTestClient(t, server.URL)

	cache := tenantcache.NewTenantCache()
	loader := tenantcache.NewTenantLoader(
		pmClient, cache, "test-service",
		tenantcache.DefaultTenantCacheTTL, log.NewNop(),
	)

	// Pre-populate cache with an already-expired entry
	cache.Set(tenantID, config, -1*time.Second)

	mid := &TenantMiddleware{
		enabled: true,
		cache:   cache,
		loader:  loader,
	}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": tenantID,
	})

	var capturedTenantID string

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c *fiber.Ctx) error {
		capturedTenantID = core.GetTenantIDContext(c.UserContext())
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, tenantID, capturedTenantID, "tenantId should be in context after lazy-load from expired entry")
	assert.Equal(t, int64(1), requestCount.Load(),
		"exactly 1 HTTP call should be made when cache entry is expired")

	// Verify fresh entry is now in cache
	entry, ok := cache.Get(tenantID)
	assert.True(t, ok, "tenant should be cached after lazy-load")
	assert.Equal(t, tenantID, entry.Config.ID)
}

func TestWithTenantDB_CacheMiss_LoadFails_Suspended(t *testing.T) {
	tenantID := "tenant-cache-suspended"

	suspendedResponse := map[string]string{
		"code": "FORBIDDEN", "error": "tenant service is suspended", "status": "suspended",
	}
	server := setupCacheErrorServer(t, tenantID, http.StatusForbidden, suspendedResponse)
	pmClient := newCacheTestClient(t, server.URL)

	cache := tenantcache.NewTenantCache()
	loader := tenantcache.NewTenantLoader(
		pmClient, cache, "test-service",
		tenantcache.DefaultTenantCacheTTL, log.NewNop(),
	)

	mid := &TenantMiddleware{
		enabled: true,
		cache:   cache,
		loader:  loader,
	}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": tenantID,
	})

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"suspended tenant should return 403")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "suspended",
		"response body should mention suspended status")
}

func TestWithTenantDB_CacheMiss_LoadFails_NotFound(t *testing.T) {
	tenantID := "tenant-cache-notfound"

	server := setupCacheErrorServer(t, tenantID, http.StatusNotFound, map[string]string{"error": "not found"})
	pmClient := newCacheTestClient(t, server.URL)

	cache := tenantcache.NewTenantCache()
	loader := tenantcache.NewTenantLoader(
		pmClient, cache, "test-service",
		tenantcache.DefaultTenantCacheTTL, log.NewNop(),
	)

	mid := &TenantMiddleware{
		enabled: true,
		cache:   cache,
		loader:  loader,
	}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": tenantID,
	})

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"not-found tenant should return 404")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Tenant Not Found",
		"response body should indicate tenant not found")
}

func TestWithTenantDB_NoCacheConfigured_ExistingBehavior(t *testing.T) {
	// When cache and loader are NOT configured, existing behavior is preserved:
	// JWT is parsed, tenantID extracted, and the middleware proceeds to DB resolution.
	// With no DB managers set (but enabled=true), it just calls c.Next().
	mid := &TenantMiddleware{enabled: true}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": "tenant-no-cache",
	})

	var capturedTenantID string
	nextCalled := false

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c *fiber.Ctx) error {
		nextCalled = true
		capturedTenantID = core.GetTenantIDContext(c.UserContext())
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, nextCalled, "next handler should have been called")
	assert.Equal(t, "tenant-no-cache", capturedTenantID,
		"tenantId should be in context even without cache configured")
}

// --- WithPG / WithMB variadic API tests ---

func TestWithPG_SingleModule(t *testing.T) {
	t.Run("registers one PG module and enables middleware", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		mid := NewTenantMiddleware(WithPG(pgManager, "onboarding"))

		assert.True(t, mid.Enabled())
		assert.Len(t, mid.pgModules, 1)
		assert.Equal(t, pgManager, mid.pgModules["onboarding"])
		// Single-manager field should remain nil (module path used).
		assert.Nil(t, mid.postgres)
	})

	t.Run("context injection with single WithPG module", func(t *testing.T) {
		// WithPG with a single module should inject both the module key
		// and the generic PGConnection key for backward compat.
		// We test with enabled=true but no real DB managers (so DB resolution is skipped).
		mid := &TenantMiddleware{
			enabled:   true,
			pgModules: map[string]*tmpostgres.Manager{},
		}

		token := buildTestJWT(t, map[string]any{
			"sub":      "user-123",
			"tenantId": "tenant-pg-single",
		})

		var capturedTenantID string
		nextCalled := false

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Use(mid.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			nextCalled = true
			capturedTenantID = core.GetTenantIDContext(c.UserContext())
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, nextCalled)
		assert.Equal(t, "tenant-pg-single", capturedTenantID)
	})
}

func TestWithPG_MultiModule(t *testing.T) {
	t.Run("registers multiple PG modules", func(t *testing.T) {
		pgOnboarding, _ := newTestManagers(t)
		pgTransaction, _ := newTestManagers(t)

		mid := NewTenantMiddleware(
			WithPG(pgOnboarding, "onboarding"),
			WithPG(pgTransaction, "transaction"),
		)

		assert.True(t, mid.Enabled())
		assert.Len(t, mid.pgModules, 2)
		assert.Equal(t, pgOnboarding, mid.pgModules["onboarding"])
		assert.Equal(t, pgTransaction, mid.pgModules["transaction"])
		// Single-manager field should remain nil.
		assert.Nil(t, mid.postgres)
	})

	t.Run("context injection with multiple WithPG modules passes through", func(t *testing.T) {
		// When pgModules is set but empty (all modules resolved successfully with no real managers),
		// the middleware should still call c.Next().
		mid := &TenantMiddleware{
			enabled:   true,
			pgModules: map[string]*tmpostgres.Manager{},
		}

		token := buildTestJWT(t, map[string]any{
			"sub":      "user-123",
			"tenantId": "tenant-pg-multi",
		})

		var capturedTenantID string
		nextCalled := false

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Use(mid.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			nextCalled = true
			capturedTenantID = core.GetTenantIDContext(c.UserContext())
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, nextCalled)
		assert.Equal(t, "tenant-pg-multi", capturedTenantID)
	})
}

func TestWithMB_SingleModule(t *testing.T) {
	t.Run("registers one MongoDB module and enables middleware", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		mid := NewTenantMiddleware(WithMB(mongoManager, "onboarding"))

		assert.True(t, mid.Enabled())
		assert.Len(t, mid.mongoModules, 1)
		assert.Equal(t, mongoManager, mid.mongoModules["onboarding"])
		assert.Nil(t, mid.mongo)
	})
}

func TestWithMB_MultiModule(t *testing.T) {
	t.Run("registers multiple MongoDB modules", func(t *testing.T) {
		_, mongoOnboarding := newTestManagers(t)
		_, mongoTransaction := newTestManagers(t)

		mid := NewTenantMiddleware(
			WithMB(mongoOnboarding, "onboarding"),
			WithMB(mongoTransaction, "transaction"),
		)

		assert.True(t, mid.Enabled())
		assert.Len(t, mid.mongoModules, 2)
		assert.Equal(t, mongoOnboarding, mid.mongoModules["onboarding"])
		assert.Equal(t, mongoTransaction, mid.mongoModules["transaction"])
		// Single-manager field should remain nil.
		assert.Nil(t, mid.mongo)
	})

	t.Run("context injection with empty mongoModules passes through", func(t *testing.T) {
		mid := &TenantMiddleware{
			enabled:      true,
			mongoModules: map[string]*tmmongo.Manager{},
		}

		token := buildTestJWT(t, map[string]any{
			"sub":      "user-123",
			"tenantId": "tenant-mb-multi",
		})

		var capturedTenantID string
		nextCalled := false

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Use(mid.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			nextCalled = true
			capturedTenantID = core.GetTenantIDContext(c.UserContext())
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, nextCalled)
		assert.Equal(t, "tenant-mb-multi", capturedTenantID)
	})
}

func TestWithPG_BackwardCompat(t *testing.T) {
	t.Run("WithPG without module works like single-manager mode", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		mid := NewTenantMiddleware(
			WithPG(pgManager),
		)

		assert.True(t, mid.Enabled())
		assert.Equal(t, pgManager, mid.postgres)
		// pgModules should be nil when no module is specified.
		assert.Nil(t, mid.pgModules)
	})

	t.Run("WithMB without module works like single-manager mode", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		mid := NewTenantMiddleware(
			WithMB(mongoManager),
		)

		assert.True(t, mid.Enabled())
		assert.Equal(t, mongoManager, mid.mongo)
		assert.Nil(t, mid.mongoModules)
	})
}

func TestNewTenantMiddleware_EnabledWithMultiModule(t *testing.T) {
	t.Run("enabled when only WithPG with module is configured", func(t *testing.T) {
		pgManager, _ := newTestManagers(t)

		mid := NewTenantMiddleware(WithPG(pgManager, "onboarding"))

		assert.True(t, mid.Enabled())
	})

	t.Run("enabled when only WithMB with module is configured", func(t *testing.T) {
		_, mongoManager := newTestManagers(t)

		mid := NewTenantMiddleware(WithMB(mongoManager, "onboarding"))

		assert.True(t, mid.Enabled())
	})

	t.Run("enabled when WithPG and WithMB with modules are both configured", func(t *testing.T) {
		pgManager, mongoManager := newTestManagers(t)

		mid := NewTenantMiddleware(
			WithPG(pgManager, "onboarding"),
			WithMB(mongoManager, "onboarding"),
		)

		assert.True(t, mid.Enabled())
		assert.Len(t, mid.pgModules, 1)
		assert.Len(t, mid.mongoModules, 1)
	})

	t.Run("disabled when no managers at all", func(t *testing.T) {
		mid := NewTenantMiddleware()

		assert.False(t, mid.Enabled())
		assert.Nil(t, mid.pgModules)
		assert.Nil(t, mid.mongoModules)
	})
}
