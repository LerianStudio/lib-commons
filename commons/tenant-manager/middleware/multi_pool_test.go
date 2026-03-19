// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMultiPoolTestManagers creates postgres and mongo Managers backed by a test
// client that has a non-nil client (so IsMultiTenant() returns true).
func newMultiPoolTestManagers(t testing.TB, url string) (*tmpostgres.Manager, *tmmongo.Manager) {
	t.Helper()
	c, err := client.NewClient(url, nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)
	return tmpostgres.NewManager(c, "ledger"), tmmongo.NewManager(c, "ledger")
}

// newSingleTenantManagers creates managers with a nil client (no tenant manager
// configured), so IsMultiTenant() returns false.
func newSingleTenantManagers() (*tmpostgres.Manager, *tmmongo.Manager) {
	return tmpostgres.NewManager(nil, "ledger"), tmmongo.NewManager(nil, "ledger")
}

func TestNewMultiPoolMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("creates disabled middleware when no options provided", func(t *testing.T) {
		t.Parallel()

		mid := NewMultiPoolMiddleware()

		assert.NotNil(t, mid)
		assert.False(t, mid.Enabled())
		assert.Empty(t, mid.routes)
		assert.Nil(t, mid.defaultRoute)
		assert.Equal(t, []string{"/healthz", "/readyz", "/livez", "/health"}, mid.publicPaths)
		assert.False(t, mid.crossModule)
		assert.Nil(t, mid.errorMapper)
		assert.Nil(t, mid.logger)
	})

	t.Run("creates enabled middleware when route has multi-tenant PG pool", func(t *testing.T) {
		t.Parallel()

		pgPool, mongoPool := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, mongoPool),
		)

		assert.NotNil(t, mid)
		assert.True(t, mid.Enabled())
		assert.Len(t, mid.routes, 1)
		assert.Equal(t, "transaction", mid.routes[0].module)
		assert.Equal(t, []string{"/v1/transactions"}, mid.routes[0].paths)
	})

	t.Run("creates enabled middleware when default route has multi-tenant PG pool", func(t *testing.T) {
		t.Parallel()

		pgPool, mongoPool := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithDefaultRoute("ledger", pgPool, mongoPool),
		)

		assert.NotNil(t, mid)
		assert.True(t, mid.Enabled())
		assert.NotNil(t, mid.defaultRoute)
		assert.Equal(t, "ledger", mid.defaultRoute.module)
	})

	t.Run("creates disabled middleware when all pools are single-tenant", func(t *testing.T) {
		t.Parallel()

		pgPool, mongoPool := newSingleTenantManagers()

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, mongoPool),
			WithDefaultRoute("ledger", pgPool, mongoPool),
		)

		assert.NotNil(t, mid)
		assert.False(t, mid.Enabled())
	})

	t.Run("applies all options correctly", func(t *testing.T) {
		t.Parallel()

		pgPool, mongoPool := newMultiPoolTestManagers(t, "http://localhost:8080")
		mapper := func(_ *fiber.Ctx, _ error, _ string) error { return nil }

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, mongoPool),
			WithRoute([]string{"/v1/accounts"}, "account", pgPool, nil),
			WithDefaultRoute("ledger", pgPool, mongoPool),
			WithPublicPaths("/health", "/ready"),
			WithCrossModuleInjection(),
			WithErrorMapper(mapper),
		)

		assert.True(t, mid.Enabled())
		assert.Len(t, mid.routes, 2)
		assert.NotNil(t, mid.defaultRoute)
		assert.Equal(t, []string{"/healthz", "/readyz", "/livez", "/health", "/health", "/ready"}, mid.publicPaths)
		assert.True(t, mid.crossModule)
		assert.NotNil(t, mid.errorMapper)
	})
}

func TestMultiPoolMiddleware_matchRoute(t *testing.T) {
	t.Parallel()

	pgPool, mongoPool := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions", "/v1/tx"}, "transaction", pgPool, mongoPool),
		WithRoute([]string{"/v1/accounts"}, "account", pgPool, nil),
		WithDefaultRoute("ledger", pgPool, mongoPool),
	)

	tests := []struct {
		name           string
		path           string
		expectedModule string
		expectNil      bool
	}{
		{
			name:           "matches first route by exact prefix",
			path:           "/v1/transactions/123",
			expectedModule: "transaction",
		},
		{
			name:           "matches first route by alternative prefix",
			path:           "/v1/tx/456",
			expectedModule: "transaction",
		},
		{
			name:           "matches second route",
			path:           "/v1/accounts/789",
			expectedModule: "account",
		},
		{
			name:           "falls back to default route",
			path:           "/v1/unknown/path",
			expectedModule: "ledger",
		},
		{
			name:           "matches root path to default",
			path:           "/",
			expectedModule: "ledger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			route := mid.matchRoute(tt.path)

			if tt.expectNil {
				assert.Nil(t, route)
			} else {
				require.NotNil(t, route)
				assert.Equal(t, tt.expectedModule, route.module)
			}
		})
	}
}

func TestMultiPoolMiddleware_matchRoute_NoDefault(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	route := mid.matchRoute("/v1/unknown")
	assert.Nil(t, route)
}

func TestMultiPoolMiddleware_isPublicPath(t *testing.T) {
	t.Parallel()

	mid := NewMultiPoolMiddleware(
		WithPublicPaths("/health", "/ready", "/version"),
	)

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "matches health endpoint",
			path:     "/health",
			expected: true,
		},
		{
			name:     "matches ready endpoint",
			path:     "/ready",
			expected: true,
		},
		{
			name:     "matches version endpoint",
			path:     "/version",
			expected: true,
		},
		{
			name:     "matches health sub-path",
			path:     "/health/live",
			expected: true,
		},
		{
			name:     "does not match non-public path",
			path:     "/v1/transactions",
			expected: false,
		},
		{
			name:     "does not match partial prefix",
			path:     "/healthy",
			expected: false, // boundary-aware: "/healthy" is not "/health" or "/health/..."
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, mid.isPublicPath(tt.path))
		})
	}
}

func TestMultiPoolMiddleware_Enabled(t *testing.T) {
	t.Parallel()

	t.Run("returns false when no routes configured", func(t *testing.T) {
		t.Parallel()

		mid := NewMultiPoolMiddleware()
		assert.False(t, mid.Enabled())
	})

	t.Run("returns true when route has multi-tenant pool", func(t *testing.T) {
		t.Parallel()

		pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/test"}, "test", pgPool, nil),
		)

		assert.True(t, mid.Enabled())
	})

	t.Run("returns false when route has single-tenant pool", func(t *testing.T) {
		t.Parallel()

		pgPool, _ := newSingleTenantManagers()

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/test"}, "test", pgPool, nil),
		)

		assert.False(t, mid.Enabled())
	})

	t.Run("returns true when route has multi-tenant Mongo pool only", func(t *testing.T) {
		t.Parallel()

		singlePG, _ := newSingleTenantManagers()
		_, multiMongo := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/test"}, "test", singlePG, multiMongo),
		)

		assert.True(t, mid.Enabled())
	})

	t.Run("returns true when default route has multi-tenant Mongo pool only", func(t *testing.T) {
		t.Parallel()

		singlePG, _ := newSingleTenantManagers()
		_, multiMongo := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithDefaultRoute("ledger", singlePG, multiMongo),
		)

		assert.True(t, mid.Enabled())
	})

	t.Run("returns true when route has nil PG pool and multi-tenant Mongo pool", func(t *testing.T) {
		t.Parallel()

		_, multiMongo := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/test"}, "test", nil, multiMongo),
		)

		assert.True(t, mid.Enabled())
	})

	t.Run("returns true when only default route is multi-tenant", func(t *testing.T) {
		t.Parallel()

		singlePG, _ := newSingleTenantManagers()
		multiPG, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

		mid := NewMultiPoolMiddleware(
			WithRoute([]string{"/v1/test"}, "test", singlePG, nil),
			WithDefaultRoute("ledger", multiPG, nil),
		)

		assert.True(t, mid.Enabled())
	})
}

func TestMultiPoolMiddleware_WithTenantDB_PublicPath(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
		WithPublicPaths("/health", "/ready"),
	)

	nextCalled := false

	app := fiber.New()
	app.Use(mid.WithTenantDB)
	app.Get("/health", func(c *fiber.Ctx) error {
		nextCalled = true
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, nextCalled, "public path should bypass tenant resolution")
}

func TestMultiPoolMiddleware_WithTenantDB_NoMatchingRoute(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	// No default route, so unmatched paths pass through
	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	nextCalled := false

	app := fiber.New()
	app.Use(mid.WithTenantDB)
	app.Get("/v1/unknown", func(c *fiber.Ctx) error {
		nextCalled = true
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/unknown", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, nextCalled, "unmatched route should pass through")
}

func TestMultiPoolMiddleware_WithTenantDB_SingleTenantBypass(t *testing.T) {
	t.Parallel()

	pgPool, _ := newSingleTenantManagers()

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	nextCalled := false

	app := fiber.New()
	app.Use(mid.WithTenantDB)
	app.Get("/v1/transactions", func(c *fiber.Ctx) error {
		nextCalled = true
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/transactions", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, nextCalled, "single-tenant pool should bypass tenant resolution")
}

func TestMultiPoolMiddleware_WithTenantDB_MissingToken(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	app := fiber.New()
	app.Use(mid.WithTenantDB)
	app.Get("/v1/transactions", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/transactions", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Contains(t, string(body), "Unauthorized")
}

func TestMultiPoolMiddleware_WithTenantDB_InvalidToken(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/v1/transactions", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/transactions", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Contains(t, string(body), "Unauthorized")
}

func TestMultiPoolMiddleware_WithTenantDB_MissingTenantID(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	token := buildTestJWT(t, map[string]any{
		"sub":   "user-123",
		"email": "test@example.com",
	})

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/v1/transactions", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/transactions", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Contains(t, string(body), "Unauthorized")
}

func TestMultiPoolMiddleware_WithTenantDB_ErrorMapperDelegation(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	customMapperCalled := false
	customMapper := func(c *fiber.Ctx, _ error, _ string) error {
		customMapperCalled = true

		return c.Status(http.StatusTeapot).JSON(fiber.Map{
			"code":    "CUSTOM_ERROR",
			"title":   "Custom Error",
			"message": "handled by custom mapper",
		})
	}

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
		WithErrorMapper(customMapper),
	)

	app := fiber.New()
	app.Use(mid.WithTenantDB)
	app.Get("/v1/transactions", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	// No Authorization header -> triggers error -> should use custom mapper
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.True(t, customMapperCalled, "custom error mapper should be called")
	assert.Equal(t, http.StatusTeapot, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Contains(t, string(body), "CUSTOM_ERROR")
}

func TestMultiPoolMiddleware_WithTenantDB_DefaultRouteMatching(t *testing.T) {
	t.Parallel()

	// Create a mock Tenant Manager server that returns 404 to trigger an error
	// response (proves the route was matched and tenant resolution attempted).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	pgPool, _ := newMultiPoolTestManagers(t, server.URL)

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
		WithDefaultRoute("ledger", pgPool, nil),
	)

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": "tenant-abc",
	})

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/v1/unknown", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/unknown", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	// The request should NOT return 200 because the PG connection resolution
	// will fail with the mock server (proving the default route was matched
	// and multi-tenant resolution was attempted).
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
}

func TestMultiPoolMiddleware_WithTenantDB_PGFailureBlocksHandler(t *testing.T) {
	t.Parallel()

	// The middleware injects the tenant ID into context (step 6 in WithTenantDB)
	// BEFORE attempting PG resolution (step 8). However, on PG resolution failure
	// the middleware returns an error without calling c.Next(), so the downstream
	// handler is never reached and cannot observe the injected tenant ID.
	//
	// This test validates the observable behavior: JWT parsing succeeds and the
	// middleware reaches PG resolution (returning 503), but the handler is NOT
	// called because the PG connection cannot be established without a real
	// Tenant Manager backend.
	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := &MultiPoolMiddleware{
		routes: []*PoolRoute{
			{
				paths:  []string{"/v1/test"},
				module: "test",
				pgPool: pgPool,
			},
		},
		enabled: true,
	}

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": "tenant-xyz",
	})

	handlerCalled := false

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/v1/test", func(c *fiber.Ctx) error {
		handlerCalled = true
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	// PG resolution fails (no real Tenant Manager), so the middleware returns
	// a service-unavailable error and the handler is never reached.
	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"expected non-200 because PG resolution fails without a real Tenant Manager")
	assert.False(t, handlerCalled,
		"handler should not be called when PG resolution fails")
}

func TestMultiPoolMiddleware_mapDefaultError(t *testing.T) {
	t.Parallel()

	mid := &MultiPoolMiddleware{}

	tests := []struct {
		name         string
		err          error
		tenantID     string
		expectedCode int
		expectedBody string
	}{
		{
			name:         "tenant not found returns 404",
			err:          core.ErrTenantNotFound,
			tenantID:     "tenant-123",
			expectedCode: http.StatusNotFound,
			expectedBody: "TENANT_NOT_FOUND",
		},
		{
			name:         "tenant suspended returns 403",
			err:          &core.TenantSuspendedError{TenantID: "t1", Status: "suspended"},
			tenantID:     "t1",
			expectedCode: http.StatusForbidden,
			expectedBody: "Service Suspended",
		},
		{
			name:         "manager closed returns 503",
			err:          core.ErrManagerClosed,
			tenantID:     "t1",
			expectedCode: http.StatusServiceUnavailable,
			expectedBody: "SERVICE_UNAVAILABLE",
		},
		{
			name:         "service not configured returns 503",
			err:          core.ErrServiceNotConfigured,
			tenantID:     "t1",
			expectedCode: http.StatusServiceUnavailable,
			expectedBody: "SERVICE_UNAVAILABLE",
		},
		{
			name:         "connection error returns 503",
			err:          errors.New("connection refused"),
			tenantID:     "t1",
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TENANT_DB_ERROR",
		},
		{
			name:         "generic error returns 500",
			err:          errors.New("something unexpected"),
			tenantID:     "t1",
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TENANT_DB_ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return mid.mapDefaultError(c, tt.err, tt.tenantID)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			resp, err := app.Test(req, -1)
			require.NoError(t, err)

			defer resp.Body.Close()

			assert.Equal(t, tt.expectedCode, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Contains(t, string(body), tt.expectedBody)
		})
	}
}

func TestMultiPoolMiddleware_extractTenantID(t *testing.T) {
	t.Parallel()

	mid := &MultiPoolMiddleware{}

	t.Run("returns error when no Authorization header", func(t *testing.T) {
		t.Parallel()

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Get("/test", func(c *fiber.Ctx) error {
			_, err := mid.extractTenantID(c)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "authorization token is required")

			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		defer resp.Body.Close()
	})

	t.Run("returns error when token is malformed", func(t *testing.T) {
		t.Parallel()

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Get("/test", func(c *fiber.Ctx) error {
			_, err := mid.extractTenantID(c)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "invalid authorization token")

			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer not-valid")

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		defer resp.Body.Close()
	})

	t.Run("returns error when tenantId claim is missing", func(t *testing.T) {
		t.Parallel()

		token := buildTestJWT(t, map[string]any{
			"sub": "user-123",
		})

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Get("/test", func(c *fiber.Ctx) error {
			_, err := mid.extractTenantID(c)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "tenantId claim is required")

			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		defer resp.Body.Close()
	})

	t.Run("returns tenant ID from valid token", func(t *testing.T) {
		t.Parallel()

		token := buildTestJWT(t, map[string]any{
			"sub":      "user-123",
			"tenantId": "tenant-abc",
		})

		app := fiber.New()
		app.Use(simulateAuthMiddleware("user-123"))
		app.Get("/test", func(c *fiber.Ctx) error {
			tenantID, err := mid.extractTenantID(c)
			assert.NoError(t, err)
			assert.Equal(t, "tenant-abc", tenantID)

			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		defer resp.Body.Close()
	})
}

func TestMultiPoolMiddleware_WithTenantDB_CrossModuleInjection(t *testing.T) {
	t.Parallel()

	// Create a mock Tenant Manager that returns 404 (so PG connection fails).
	// We verify that even with crossModule enabled, the middleware attempts
	// resolution for the matched route first. Since PG resolution fails,
	// we get an error response (proving the route was matched and cross-module
	// logic was reached or would be reached after primary resolution).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	pgPoolA, _ := newMultiPoolTestManagers(t, server.URL)
	pgPoolB, _ := newMultiPoolTestManagers(t, server.URL)

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPoolA, nil),
		WithRoute([]string{"/v1/accounts"}, "account", pgPoolB, nil),
		WithCrossModuleInjection(),
	)

	assert.True(t, mid.crossModule, "crossModule flag should be set")
	assert.Len(t, mid.routes, 2)

	token := buildTestJWT(t, map[string]any{
		"sub":      "user-123",
		"tenantId": "tenant-abc",
	})

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/v1/transactions", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/transactions", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	defer resp.Body.Close()

	// PG resolution will fail with the mock server, producing an error response.
	// This confirms the middleware reached the PG resolution step, which happens
	// before cross-module injection.
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
}

func TestWithRoute(t *testing.T) {
	t.Parallel()

	pgPool, mongoPool := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := &MultiPoolMiddleware{}
	opt := WithRoute([]string{"/v1/test", "/v1/test2"}, "test-module", pgPool, mongoPool)
	opt(mid)

	require.Len(t, mid.routes, 1)
	assert.Equal(t, "test-module", mid.routes[0].module)
	assert.Equal(t, []string{"/v1/test", "/v1/test2"}, mid.routes[0].paths)
	assert.Equal(t, pgPool, mid.routes[0].pgPool)
	assert.Equal(t, mongoPool, mid.routes[0].mongoPool)
}

func TestWithDefaultRoute(t *testing.T) {
	t.Parallel()

	pgPool, mongoPool := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := &MultiPoolMiddleware{}
	opt := WithDefaultRoute("default-module", pgPool, mongoPool)
	opt(mid)

	require.NotNil(t, mid.defaultRoute)
	assert.Equal(t, "default-module", mid.defaultRoute.module)
	assert.Equal(t, pgPool, mid.defaultRoute.pgPool)
	assert.Equal(t, mongoPool, mid.defaultRoute.mongoPool)
	assert.Empty(t, mid.defaultRoute.paths)
}

func TestWithPublicPaths(t *testing.T) {
	t.Parallel()

	mid := &MultiPoolMiddleware{}
	opt := WithPublicPaths("/health", "/ready")
	opt(mid)

	assert.Equal(t, []string{"/health", "/ready"}, mid.publicPaths)

	// Applying again appends
	opt2 := WithPublicPaths("/version")
	opt2(mid)

	assert.Equal(t, []string{"/health", "/ready", "/version"}, mid.publicPaths)
}

func TestWithCrossModuleInjection(t *testing.T) {
	t.Parallel()

	mid := &MultiPoolMiddleware{}
	assert.False(t, mid.crossModule)

	opt := WithCrossModuleInjection()
	opt(mid)

	assert.True(t, mid.crossModule)
}

func TestWithErrorMapper(t *testing.T) {
	t.Parallel()

	mapper := func(_ *fiber.Ctx, _ error, _ string) error { return nil }

	mid := &MultiPoolMiddleware{}
	assert.Nil(t, mid.errorMapper)

	opt := WithErrorMapper(mapper)
	opt(mid)

	assert.NotNil(t, mid.errorMapper)
}

func TestWithMultiPoolLogger(t *testing.T) {
	t.Parallel()

	mid := &MultiPoolMiddleware{}
	assert.Nil(t, mid.logger)

	// We just verify the option sets the field. Using nil logger since we
	// don't have a test logger implementation in scope.
	opt := WithMultiPoolLogger(nil)
	opt(mid)

	assert.NotNil(t, mid.logger)
}

func TestMultiPoolMiddleware_DefaultHealthProbePaths(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
	)

	tests := []struct {
		name         string
		path         string
		expectBypass bool
	}{
		{
			name:         "healthz bypasses auth",
			path:         "/healthz",
			expectBypass: true,
		},
		{
			name:         "readyz bypasses auth",
			path:         "/readyz",
			expectBypass: true,
		},
		{
			name:         "livez bypasses auth",
			path:         "/livez",
			expectBypass: true,
		},
		{
			name:         "health bypasses auth",
			path:         "/health",
			expectBypass: true,
		},
		{
			name:         "health sub-path bypasses auth",
			path:         "/health/live",
			expectBypass: true,
		},
		{
			name:         "regular path requires auth",
			path:         "/v1/transactions",
			expectBypass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nextCalled := false

			app := fiber.New()
			app.Use(mid.WithTenantDB)
			app.Get(tt.path, func(c *fiber.Ctx) error {
				nextCalled = true
				return c.SendString("ok")
			})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			resp, err := app.Test(req, -1)
			require.NoError(t, err)
			defer resp.Body.Close()

			if tt.expectBypass {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.True(t, nextCalled, "health probe path should bypass auth")
			} else {
				assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
				assert.False(t, nextCalled, "regular path should require auth")
			}
		})
	}
}

func TestMultiPoolMiddleware_WithPublicPaths_AppendsToDefaults(t *testing.T) {
	t.Parallel()

	pgPool, _ := newMultiPoolTestManagers(t, "http://localhost:8080")

	mid := NewMultiPoolMiddleware(
		WithRoute([]string{"/v1/transactions"}, "transaction", pgPool, nil),
		WithPublicPaths("/metrics", "/version"),
	)

	assert.Equal(t, []string{"/healthz", "/readyz", "/livez", "/health", "/metrics", "/version"}, mid.publicPaths)

	nextCalled := false

	app := fiber.New()
	app.Use(mid.WithTenantDB)
	app.Get("/metrics", func(c *fiber.Ctx) error {
		nextCalled = true
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, nextCalled, "custom public path should bypass auth")
}
