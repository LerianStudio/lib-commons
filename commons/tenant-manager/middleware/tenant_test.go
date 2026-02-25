package middleware

import (
	"encoding/base64"
	"encoding/json"
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

func TestNewTenantMiddleware(t *testing.T) {
	t.Run("creates disabled middleware when no managers are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()

		assert.NotNil(t, middleware)
		assert.False(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with PostgreSQL only", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with MongoDB only", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", nil)
		mongoManager := tmmongo.NewManager(c, "ledger")

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})

	t.Run("creates middleware with both PostgreSQL and MongoDB managers", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")
		mongoManager := tmmongo.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithPostgresManager(pgManager)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestWithMongoManager(t *testing.T) {
	t.Run("sets mongo manager on middleware", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", nil)
		mongoManager := tmmongo.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		mongoManager := tmmongo.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when only MongoDB manager is set", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", nil)
		mongoManager := tmmongo.NewManager(c, "ledger")

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when both managers are set", func(t *testing.T) {
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")
		mongoManager := tmmongo.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		mongoManager := tmmongo.NewManager(c, "ledger")

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
		c := client.NewClient("http://localhost:8080", nil)
		pgManager := tmpostgres.NewManager(c, "ledger")

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
