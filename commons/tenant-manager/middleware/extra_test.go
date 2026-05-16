//go:build unit

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFiberTestApp creates a minimal Fiber app to test middleware response helpers.
func newFiberTestApp(handler fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/test", handler)

	return app
}

// TestMapDomainErrorToHTTP_AuthErrors covers the 401 path.
func TestMapDomainErrorToHTTP_AuthorizationTokenRequired(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrAuthorizationTokenRequired, "tenant-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMapDomainErrorToHTTP_InvalidAuthorizationToken(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrInvalidAuthorizationToken, "tenant-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMapDomainErrorToHTTP_InvalidTenantClaims(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrInvalidTenantClaims, "tenant-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMapDomainErrorToHTTP_MissingTenantIDClaim(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrMissingTenantIDClaim, "tenant-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_TenantNotFound covers the 404 path.
func TestMapDomainErrorToHTTP_TenantNotFound(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrTenantNotFound, "tenant-not-found")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_TenantSuspended covers the 403 path for TenantSuspendedError.
func TestMapDomainErrorToHTTP_TenantSuspended(t *testing.T) {
	t.Parallel()

	suspErr := &core.TenantSuspendedError{TenantID: "t1", Status: "suspended"}
	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, suspErr, "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_GenericAccessDenied covers the 403 path for ErrTenantServiceAccessDenied.
func TestMapDomainErrorToHTTP_GenericAccessDenied(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrTenantServiceAccessDenied, "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_ManagerClosed covers the 503 path for ErrManagerClosed.
func TestMapDomainErrorToHTTP_ManagerClosed(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrManagerClosed, "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_ServiceNotConfigured covers the 503 path.
func TestMapDomainErrorToHTTP_ServiceNotConfigured(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrServiceNotConfigured, "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_CircuitBreakerOpen covers the 503 path.
func TestMapDomainErrorToHTTP_CircuitBreakerOpen(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrCircuitBreakerOpen, "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_ConnectionFailed covers the 503 path.
func TestMapDomainErrorToHTTP_ConnectionFailed(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, core.ErrConnectionFailed, "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestMapDomainErrorToHTTP_DefaultError covers the 500 path.
func TestMapDomainErrorToHTTP_DefaultError(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return mapDomainErrorToHTTP(c, errors.New("unexpected error"), "t1")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// TestInternalServerError covers the internalServerError helper.
func TestInternalServerError(t *testing.T) {
	t.Parallel()

	app := newFiberTestApp(func(c *fiber.Ctx) error {
		return internalServerError(c, "TEST_CODE", "Test Title")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}
