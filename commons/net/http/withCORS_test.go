//go:build unit

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons"
	constant "github.com/LerianStudio/lib-commons/v4/commons/constants"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowFullOptionsWithCORS_NilApp(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		AllowFullOptionsWithCORS(nil)
	})
}

func TestWithCORS_UsesEnvironmentConfiguration(t *testing.T) {
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_ORIGIN", "https://example.com"))
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_METHODS", "GET,POST,OPTIONS"))
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_HEADERS", "Authorization,Content-Type"))
	require.NoError(t, os.Setenv("ACCESS_CONTROL_EXPOSE_HEADERS", "X-Trace-ID"))
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_CREDENTIALS", "true"))
	t.Cleanup(func() {
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_ORIGIN"))
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_METHODS"))
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_HEADERS"))
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_EXPOSE_HEADERS"))
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_CREDENTIALS"))
	})

	app := fiber.New()
	app.Use(WithCORS())
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "https://example.com", resp.Header.Get(fiber.HeaderAccessControlAllowOrigin))
	require.Equal(t, "true", resp.Header.Get(fiber.HeaderAccessControlAllowCredentials))
	require.Contains(t, resp.Header.Get(fiber.HeaderAccessControlAllowMethods), http.MethodGet)
	require.Contains(t, resp.Header.Get(fiber.HeaderAccessControlAllowHeaders), constant.Authorization)
}

func TestAllowFullOptionsWithCORS_RegistersOptionsRoute(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	AllowFullOptionsWithCORS(app)

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestWithCORS_ExplicitFalseCredentials(t *testing.T) {
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_CREDENTIALS", "false"))
	t.Cleanup(func() {
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_CREDENTIALS"))
	})

	app := fiber.New()
	app.Use(WithCORS())
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "", resp.Header.Get(fiber.HeaderAccessControlAllowCredentials))
}

// ---------------------------------------------------------------------------
// WithCORSLogger option
// ---------------------------------------------------------------------------

func TestWithCORSLogger_NilDoesNotPanic(t *testing.T) {
	t.Parallel()

	// WithCORSLogger(nil) should not override the default (nil logger stays nil)
	cfg := &corsConfig{}
	opt := WithCORSLogger(nil)
	opt(cfg)
	assert.Nil(t, cfg.logger)
}

func TestWithCORSLogger_SetsLogger(t *testing.T) {
	t.Parallel()

	logger := &testCORSLogger{}
	cfg := &corsConfig{}
	opt := WithCORSLogger(logger)
	opt(cfg)
	assert.Equal(t, logger, cfg.logger)
}

func TestWithCORS_WithLoggerOption(t *testing.T) {
	// This test verifies that WithCORS accepts the WithCORSLogger option
	// and uses it for the wildcard warning.
	// Not parallel because it sets env vars.
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_ORIGIN", "*"))
	t.Cleanup(func() {
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_ORIGIN"))
	})

	logger := &testCORSLogger{}

	app := fiber.New()
	app.Use(WithCORS(WithCORSLogger(logger)))
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(200)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	// The logger should have received at least the wildcard warning
	assert.True(t, logger.logCalled, "expected the logger to be called with wildcard warning")
	assert.Equal(t, libLog.LevelWarn, logger.lastLevel)
	assert.Contains(t, logger.lastMessage, "AllowOrigins is set to wildcard")
}

// testCORSLogger is a test logger that records whether Log was called.
type testCORSLogger struct {
	logCalled   bool
	lastLevel   libLog.Level
	lastMessage string
}

func (l *testCORSLogger) Log(_ context.Context, level libLog.Level, msg string, _ ...libLog.Field) {
	l.logCalled = true
	l.lastLevel = level
	l.lastMessage = msg
}
func (l *testCORSLogger) With(_ ...libLog.Field) libLog.Logger { return l }
func (l *testCORSLogger) WithGroup(string) libLog.Logger       { return l }
func (l *testCORSLogger) Enabled(libLog.Level) bool            { return true }
func (l *testCORSLogger) Sync(context.Context) error           { return nil }

func TestWithCORS_InvalidAllowCredentialsFallsBackToDefault(t *testing.T) {
	require.NoError(t, os.Setenv("ACCESS_CONTROL_ALLOW_CREDENTIALS", "not-a-bool"))
	t.Cleanup(func() {
		require.NoError(t, os.Unsetenv("ACCESS_CONTROL_ALLOW_CREDENTIALS"))
	})

	app := fiber.New()
	app.Use(WithCORS())
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "", resp.Header.Get(fiber.HeaderAccessControlAllowCredentials))
}

func TestWithCORS_StrictEnforcedWildcardFallsBackToDenyAll(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv("ACCESS_CONTROL_ALLOW_ORIGIN", "*")
	t.Setenv("ACCESS_CONTROL_ALLOW_CREDENTIALS", "true")

	app := fiber.New()
	app.Use(WithCORS())
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Empty(t, resp.Header.Get(fiber.HeaderAccessControlAllowOrigin))
	assert.Empty(t, resp.Header.Get(fiber.HeaderAccessControlAllowCredentials))
}

func TestWithCORS_StrictEnforcedOverrideAllowsWildcard(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowCORSWildcard, "trusted gateway owns enforcement")
	t.Setenv("ACCESS_CONTROL_ALLOW_ORIGIN", "*")

	app := fiber.New()
	app.Use(WithCORS())
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(fiber.HeaderOrigin, "https://example.com")
	req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get(fiber.HeaderAccessControlAllowOrigin))
}
