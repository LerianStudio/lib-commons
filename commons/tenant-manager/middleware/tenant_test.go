package middleware

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManagers creates a postgres and mongo Manager backed by a test client.
// Centralises the repeated client.NewClient + NewManager boilerplate so each
// sub-test only declares what is unique to its scenario.
func newTestManagers() (*tmpostgres.Manager, *tmmongo.Manager) {
	c := client.NewClient("http://localhost:8080", nil)
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
		pgManager, _ := newTestManagers()

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with MongoDB only", func(t *testing.T) {
		_, mongoManager := newTestManagers()

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})

	t.Run("creates middleware with both PostgreSQL and MongoDB managers", func(t *testing.T) {
		pgManager, mongoManager := newTestManagers()

		middleware := NewTenantMiddleware(
			WithPostgresManager(pgManager),
			WithMongoManager(mongoManager),
		)

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})
}

func TestWithPostgresManager(t *testing.T) {
	t.Run("sets postgres manager on middleware", func(t *testing.T) {
		pgManager, _ := newTestManagers()

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.postgres)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithPostgresManager(pgManager)
		opt(middleware)

		assert.Equal(t, pgManager, middleware.postgres)
		assert.True(t, middleware.Enabled())
	})

	t.Run("enables middleware when postgres manager is set", func(t *testing.T) {
		pgManager, _ := newTestManagers()

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithPostgresManager(pgManager)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestWithMongoManager(t *testing.T) {
	t.Run("sets mongo manager on middleware", func(t *testing.T) {
		_, mongoManager := newTestManagers()

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.mongo)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithMongoManager(mongoManager)
		opt(middleware)

		assert.Equal(t, mongoManager, middleware.mongo)
		assert.True(t, middleware.Enabled())
	})

	t.Run("enables middleware when mongo manager is set", func(t *testing.T) {
		_, mongoManager := newTestManagers()

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithMongoManager(mongoManager)
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
		pgManager, _ := newTestManagers()

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when only MongoDB manager is set", func(t *testing.T) {
		_, mongoManager := newTestManagers()

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when both managers are set", func(t *testing.T) {
		pgManager, mongoManager := newTestManagers()

		middleware := NewTenantMiddleware(
			WithPostgresManager(pgManager),
			WithMongoManager(mongoManager),
		)
		assert.True(t, middleware.Enabled())
	})
}

// buildTestJWT constructs a minimal unsigned JWT token string from the given claims.
// The token is not cryptographically signed (signature is empty), which is acceptable
// because the middleware uses ParseUnverified (lib-auth already validated the token).
func buildTestJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))

	payload, _ := json.Marshal(claims)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

	return header + "." + encodedPayload + "."
}

func TestTenantMiddleware_WithTenantDB(t *testing.T) {
	t.Run("no Authorization header returns 401", func(t *testing.T) {
		pgManager, _ := newTestManagers()

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))

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
		assert.Contains(t, string(body), "MISSING_TOKEN")
	})

	t.Run("malformed JWT returns 401", func(t *testing.T) {
		_, mongoManager := newTestManagers()

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))

		app := fiber.New()
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
		assert.Contains(t, string(body), "INVALID_TOKEN")
	})

	t.Run("valid JWT missing tenantId claim returns 401", func(t *testing.T) {
		pgManager, _ := newTestManagers()

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))

		token := buildTestJWT(map[string]any{
			"sub":   "user-123",
			"email": "test@example.com",
		})

		app := fiber.New()
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
		assert.Contains(t, string(body), "MISSING_TENANT")
	})

	t.Run("valid JWT with tenantId calls next handler", func(t *testing.T) {
		// Create an enabled middleware with no real managers configured.
		// Both postgres and mongo pointers remain nil, so the middleware skips
		// DB resolution and proceeds to c.Next() after JWT parsing.
		middleware := &TenantMiddleware{enabled: true}

		token := buildTestJWT(map[string]any{
			"sub":      "user-123",
			"tenantId": "tenant-abc",
		})

		var capturedTenantID string
		nextCalled := false

		app := fiber.New()
		app.Use(middleware.WithTenantDB)
		app.Get("/test", func(c *fiber.Ctx) error {
			nextCalled = true
			capturedTenantID = core.GetTenantIDFromContext(c.UserContext())
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

func TestTenantMiddleware_ErrorResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		handler      func(c *fiber.Ctx) error
		expectedCode int
		expectedBody string
	}{
		{
			name: "notFoundError returns 404 with TENANT_NOT_FOUND",
			handler: func(c *fiber.Ctx) error {
				return notFoundError(c, "TENANT_NOT_FOUND", "Tenant Not Found",
					"tenant not found: tenant-123")
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "TENANT_NOT_FOUND",
		},
		{
			name: "unprocessableError returns 422 with SERVICE_NOT_CONFIGURED",
			handler: func(c *fiber.Ctx) error {
				return unprocessableError(c, "SERVICE_NOT_CONFIGURED", "Service Not Configured",
					"service not configured for tenant: tenant-123")
			},
			expectedCode: http.StatusUnprocessableEntity,
			expectedBody: "SERVICE_NOT_CONFIGURED",
		},
		{
			name: "unprocessableError returns 422 with TENANT_NOT_PROVISIONED",
			handler: func(c *fiber.Ctx) error {
				return unprocessableError(c, "TENANT_NOT_PROVISIONED", "Tenant Not Provisioned",
					"tenant database not provisioned: tenant-123")
			},
			expectedCode: http.StatusUnprocessableEntity,
			expectedBody: "TENANT_NOT_PROVISIONED",
		},
		{
			name: "forbiddenError returns 403 for suspended tenant",
			handler: func(c *fiber.Ctx) error {
				return forbiddenError(c, "0131", "Service Suspended",
					"tenant service is suspended")
			},
			expectedCode: http.StatusForbidden,
			expectedBody: "Service Suspended",
		},
		{
			name: "internalServerError returns 500 for unknown errors",
			handler: func(c *fiber.Ctx) error {
				return internalServerError(c, "TENANT_DB_ERROR", "Failed to resolve tenant database",
					"unexpected error")
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TENANT_DB_ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", tt.handler)

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

func TestTenantMiddleware_ErrorTypeDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		expectedCode int
		expectedBody string
	}{
		{
			name:         "ErrTenantNotFound produces 404",
			err:          core.ErrTenantNotFound,
			expectedCode: http.StatusNotFound,
			expectedBody: "TENANT_NOT_FOUND",
		},
		{
			name:         "wrapped ErrTenantNotFound produces 404",
			err:          fmt.Errorf("pg connection failed: %w", core.ErrTenantNotFound),
			expectedCode: http.StatusNotFound,
			expectedBody: "TENANT_NOT_FOUND",
		},
		{
			name:         "ErrServiceNotConfigured produces 422",
			err:          core.ErrServiceNotConfigured,
			expectedCode: http.StatusUnprocessableEntity,
			expectedBody: "SERVICE_NOT_CONFIGURED",
		},
		{
			name:         "wrapped ErrServiceNotConfigured produces 422",
			err:          fmt.Errorf("lookup failed: %w", core.ErrServiceNotConfigured),
			expectedCode: http.StatusUnprocessableEntity,
			expectedBody: "SERVICE_NOT_CONFIGURED",
		},
		{
			name:         "ErrTenantNotProvisioned produces 422",
			err:          core.ErrTenantNotProvisioned,
			expectedCode: http.StatusUnprocessableEntity,
			expectedBody: "TENANT_NOT_PROVISIONED",
		},
		{
			name:         "42P01 PostgreSQL error produces 422",
			err:          errors.New("ERROR: relation \"organization\" does not exist (SQLSTATE 42P01)"),
			expectedCode: http.StatusUnprocessableEntity,
			expectedBody: "TENANT_NOT_PROVISIONED",
		},
		{
			name:         "TenantSuspendedError produces 403",
			err:          &core.TenantSuspendedError{TenantID: "t1", Status: "suspended"},
			expectedCode: http.StatusForbidden,
			expectedBody: "Service Suspended",
		},
		{
			name:         "generic error produces 500",
			err:          errors.New("something unexpected"),
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TENANT_DB_ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				// Simulate the error classification logic from WithTenantDB
				return classifyConnectionError(c, tt.err, "tenant-123")
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

// classifyConnectionError replicates the error classification logic from
// WithTenantDB's PostgreSQL/MongoDB error blocks. This function is used
// exclusively in tests to validate error-to-HTTP-status mapping without
// requiring a real database manager.
func classifyConnectionError(c *fiber.Ctx, err error, tenantID string) error {
	var suspErr *core.TenantSuspendedError
	if errors.As(err, &suspErr) {
		return forbiddenError(c, "0131", "Service Suspended",
			fmt.Sprintf("tenant service is %s", suspErr.Status))
	}

	if errors.Is(err, core.ErrTenantNotFound) {
		return notFoundError(c, "TENANT_NOT_FOUND", "Tenant Not Found",
			fmt.Sprintf("tenant not found: %s", tenantID))
	}

	if errors.Is(err, core.ErrServiceNotConfigured) {
		return unprocessableError(c, "SERVICE_NOT_CONFIGURED", "Service Not Configured",
			fmt.Sprintf("service not configured for tenant: %s", tenantID))
	}

	if core.IsTenantNotProvisionedError(err) {
		return unprocessableError(c, "TENANT_NOT_PROVISIONED", "Tenant Not Provisioned",
			fmt.Sprintf("tenant database not provisioned: %s", tenantID))
	}

	return internalServerError(c, "TENANT_DB_ERROR", "Failed to resolve tenant database", err.Error())
}
