//go:build unit

package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	tmmongo "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTenantJWT builds a minimal JWT string with the given tenantId claim
// (no signature verification — middleware uses ParseUnverified).
func makeTenantJWT(t *testing.T, tenantID string) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]string{"tenantId": tenantID, "sub": "user-123"})
	require.NoError(t, err)
	payload := base64.RawURLEncoding.EncodeToString(claims)

	return header + "." + payload + ".fake-sig"
}

// makeNoTenantJWT builds a JWT with no tenantId claim.
func makeNoTenantJWT(t *testing.T) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]string{"sub": "user-123"})
	require.NoError(t, err)
	payload := base64.RawURLEncoding.EncodeToString(claims)

	return header + "." + payload + ".fake-sig"
}

// newTestManagersWithServer creates PG and Mongo managers backed by the given
// HTTP server URL. This allows resolvePostgres/resolveMongo to be called,
// which exercise the GetConnection path that returns an error.
func newTestManagersWithServer(t *testing.T, serverURL string) (*tmpostgres.Manager, *tmmongo.Manager) {
	t.Helper()
	c, err := client.NewClient(serverURL, nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)
	return tmpostgres.NewManager(c, "ledger"), tmmongo.NewManager(c, "ledger")
}

// newFiberApp creates a minimal Fiber app with the middleware under test.
func newFiberApp(mw *TenantMiddleware) *fiber.App {
	app := fiber.New()
	app.Get("/test", mw.WithTenantDB, func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})
	return app
}

// -------------------------------------------------------------------
// WithTenantDB — disabled middleware passes through
// -------------------------------------------------------------------

func TestWithTenantDB_Disabled_PassesThrough(t *testing.T) {
	t.Parallel()

	mw := NewTenantMiddleware() // no options → disabled
	app := newFiberApp(mw)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "disabled middleware should pass through")
}

// -------------------------------------------------------------------
// WithTenantDB — valid JWT with MongoDB manager, GetConnection fails → error
// -------------------------------------------------------------------

func TestWithTenantDB_MongoGetConnectionFails_ReturnsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, mgMgr := newTestManagersWithServer(t, server.URL)
	mw := NewTenantMiddleware(WithMB(mgMgr))
	app := newFiberApp(mw)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+makeTenantJWT(t, "tenant-xyz"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// -------------------------------------------------------------------
// WithPG — multi-module mode for resolvePostgres coverage
// -------------------------------------------------------------------

func TestWithPG_TwoModules_BothRegistered(t *testing.T) {
	t.Parallel()

	pgMgr, _ := newTestManagers(t)
	mw := NewTenantMiddleware(
		WithPG(pgMgr, "payments"),
		WithPG(pgMgr, "reporting"),
	)

	assert.Len(t, mw.pgModules, 2)
	assert.Contains(t, mw.pgModules, "payments")
	assert.Contains(t, mw.pgModules, "reporting")
}

// -------------------------------------------------------------------
// WithMB — multi-module mode
// -------------------------------------------------------------------

func TestWithMB_TwoModules_BothRegistered(t *testing.T) {
	t.Parallel()

	_, mgMgr := newTestManagers(t)
	mw := NewTenantMiddleware(
		WithMB(mgMgr, "docs"),
		WithMB(mgMgr, "analytics"),
	)

	assert.Len(t, mw.mongoModules, 2)
	assert.Contains(t, mw.mongoModules, "docs")
	assert.Contains(t, mw.mongoModules, "analytics")
}

// -------------------------------------------------------------------
// WithTenantDB — multi-module PG, GetConnection fails for each module
// -------------------------------------------------------------------

func TestWithTenantDB_MultiModulePG_GetConnectionFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	pgMgr, _ := newTestManagersWithServer(t, server.URL)
	mw := NewTenantMiddleware(
		WithPG(pgMgr, "onboarding"),
		WithPG(pgMgr, "transaction"),
	)
	app := newFiberApp(mw)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+makeTenantJWT(t, "tenant-multi"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	// Should fail since GetConnection fails for at least one module
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// -------------------------------------------------------------------
// TenantMiddleware.Enabled / Disabled checks
// -------------------------------------------------------------------

func TestWithTenantDB_IsEnabled_WhenConfigured(t *testing.T) {
	t.Parallel()

	pgMgr, _ := newTestManagers(t)
	mw := NewTenantMiddleware(WithPG(pgMgr))
	assert.True(t, mw.Enabled())
}

func TestWithTenantDB_IsDisabled_WhenNothingConfigured(t *testing.T) {
	t.Parallel()

	mw := NewTenantMiddleware()
	assert.False(t, mw.Enabled())
}
