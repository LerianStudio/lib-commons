//go:build unit

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// NewRequestInfo
// ---------------------------------------------------------------------------

func TestNewRequestInfo_Basic(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var info *RequestInfo

	app.Get("/api/test", func(c *fiber.Ctx) error {
		info = NewRequestInfo(c, false)
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set(cn.HeaderID, "trace-123")
	req.Header.Set(cn.HeaderUserAgent, "test-agent")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.NotNil(t, info)
	assert.Equal(t, http.MethodGet, info.Method)
	assert.Equal(t, "/api/test", info.URI)
	assert.Equal(t, "trace-123", info.TraceID)
	assert.Equal(t, "test-agent", info.UserAgent)
	assert.Equal(t, "-", info.Username)
	assert.Equal(t, "-", info.Referer)
	assert.False(t, info.Date.IsZero())
}

func TestNewRequestInfo_WithReferer(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var info *RequestInfo

	app.Get("/", func(c *fiber.Ctx) error {
		info = NewRequestInfo(c, false)
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Referer", "https://example.com")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, "https://example.com", info.Referer)
}

func TestSanitizeReferer_StripsCredentialsQueryAndFragment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://example.com/path", sanitizeReferer("https://user:pass@example.com/path?token=123#frag"))
}

func TestSanitizeReferer_InvalidValueFallsBackToDash(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "-", sanitizeReferer("://bad-url"))
}

func TestNewRequestInfo_SanitizesUserAgentControlCharacters(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var info *RequestInfo

	app.Get("/", func(c *fiber.Ctx) error {
		info = NewRequestInfo(c, false)
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(cn.HeaderUserAgent, "good-agent\r\nforged")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.NotNil(t, info)
	assert.NotContains(t, info.UserAgent, "\r")
	assert.NotContains(t, info.UserAgent, "\n")
	assert.Contains(t, info.UserAgent, "good-agent")
	assert.Contains(t, info.UserAgent, "forged")
}

// ---------------------------------------------------------------------------
// CLFString
// ---------------------------------------------------------------------------

func TestCLFString(t *testing.T) {
	t.Parallel()

	info := &RequestInfo{
		RemoteAddress: "192.168.1.1",
		Username:      "admin",
		Protocol:      "http",
		Date:          time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Method:        "POST",
		URI:           "/api/v1/resource",
		Status:        200,
		Size:          1024,
		Referer:       "-",
		UserAgent:     "curl/7.68.0",
	}

	clf := info.CLFString()

	assert.Contains(t, clf, "192.168.1.1")
	assert.Contains(t, clf, "admin")
	assert.Contains(t, clf, `"POST /api/v1/resource"`)
	assert.Contains(t, clf, "200")
	assert.Contains(t, clf, "1024")
	assert.Contains(t, clf, "curl/7.68.0")
}

func TestCLFString_DoesNotIncludeControlCharactersFromUserAgent(t *testing.T) {
	t.Parallel()

	info := &RequestInfo{
		RemoteAddress: "192.168.1.1",
		Username:      "admin",
		Protocol:      "http",
		Date:          time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Method:        "POST",
		URI:           "/api/v1/resource",
		Status:        200,
		Size:          1024,
		Referer:       "-",
		UserAgent:     "curl/7.68.0\r\nforged\x00",
	}

	clf := info.CLFString()
	assert.NotContains(t, clf, "\r")
	assert.NotContains(t, clf, "\n")
	assert.NotContains(t, clf, "\x00")
	assert.Contains(t, clf, "curl/7.68.0forged")
}

func TestSanitizeLogValue_RemovesNullByte(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abcdef", sanitizeLogValue("abc\x00def"))
}

func TestStringImplementsStringer(t *testing.T) {
	t.Parallel()

	info := &RequestInfo{
		RemoteAddress: "127.0.0.1",
		Username:      "-",
		Protocol:      "http",
		Date:          time.Now(),
		Method:        "GET",
		URI:           "/",
		Referer:       "-",
		UserAgent:     "-",
	}

	assert.Equal(t, info.CLFString(), info.String())
}

// ---------------------------------------------------------------------------
// FinishRequestInfo
// ---------------------------------------------------------------------------

func TestFinishRequestInfo(t *testing.T) {
	t.Parallel()

	info := &RequestInfo{
		Date: time.Now().Add(-100 * time.Millisecond),
	}

	rw := &ResponseMetricsWrapper{
		StatusCode: 201,
		Size:       512,
	}

	info.FinishRequestInfo(rw)

	assert.Equal(t, 201, info.Status)
	assert.Equal(t, 512, info.Size)
	assert.True(t, info.Duration >= 90*time.Millisecond, "expected duration >= 90ms, got %v", info.Duration)
}

// ---------------------------------------------------------------------------
// buildOpts / WithCustomLogger
// ---------------------------------------------------------------------------

func TestBuildOpts_Default(t *testing.T) {
	t.Parallel()

	mid := buildOpts()
	assert.NotNil(t, mid.Logger)
	assert.IsType(t, &log.GoLogger{}, mid.Logger)
}

func TestBuildOpts_WithCustomLogger(t *testing.T) {
	t.Parallel()

	custom := &mockLogger{}
	mid := buildOpts(WithCustomLogger(custom))
	assert.Equal(t, custom, mid.Logger)
}

func TestWithCustomLogger_NilDoesNotOverride(t *testing.T) {
	t.Parallel()

	mid := buildOpts(WithCustomLogger(nil))
	assert.NotNil(t, mid.Logger)
	assert.IsType(t, &log.GoLogger{}, mid.Logger)
}

func TestWithCustomLogger_TypedNilDoesNotOverride(t *testing.T) {
	t.Parallel()

	var typedNil *mockLogger
	mid := buildOpts(WithCustomLogger(typedNil))
	assert.NotNil(t, mid.Logger)
	assert.IsType(t, &log.GoLogger{}, mid.Logger)
}

func TestWithHTTPLogging_TypedNilCustomLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()

	var typedNil *mockLogger
	app := fiber.New()
	app.Use(WithHTTPLogging(WithCustomLogger(typedNil)))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNormalizeRequestID_TrimsWhitespaceAndControlCharacters(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "trace-123", normalizeRequestID(" \r\ntrace-123\x00 "))
	assert.Empty(t, normalizeRequestID(" \r\n\x00 "))
}

// ---------------------------------------------------------------------------
// Body obfuscation
// ---------------------------------------------------------------------------

func TestHandleJSONBody_SensitiveFields(t *testing.T) {
	t.Parallel()

	input := `{"username":"admin","password":"secret123","email":"a@b.com"}`
	result := handleJSONBody([]byte(input))

	assert.NotContains(t, result, "secret123")
	assert.Contains(t, result, cn.ObfuscatedValue)
	assert.Contains(t, result, "admin")
}

func TestHandleJSONBody_InvalidJSON(t *testing.T) {
	t.Parallel()

	input := `not json`
	result := handleJSONBody([]byte(input))
	assert.Equal(t, input, result)
}

func TestHandleJSONBody_NestedSensitive(t *testing.T) {
	t.Parallel()

	input := `{"user":{"name":"alice","password":"pw"},"items":[{"secret_key":"abc"}]}`
	result := handleJSONBody([]byte(input))

	assert.NotContains(t, result, "pw")
	assert.Contains(t, result, "alice")
}

func TestHandleURLEncodedBody_SensitiveFields(t *testing.T) {
	t.Parallel()

	input := "username=admin&password=secret123&name=test"
	result := handleURLEncodedBody([]byte(input))

	assert.NotContains(t, result, "secret123")
	// ObfuscatedValue gets URL-encoded by url.Values.Encode()
	assert.Contains(t, result, "password=")
	assert.Contains(t, result, "admin")
}

func TestHandleURLEncodedBody_InvalidForm(t *testing.T) {
	t.Parallel()

	input := "%ZZinvalid"
	result := handleURLEncodedBody([]byte(input))
	assert.Equal(t, input, result)
}

// ---------------------------------------------------------------------------
// WithHTTPLogging middleware integration
// ---------------------------------------------------------------------------

func TestWithHTTPLogging_SkipsHealthEndpoint(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(WithHTTPLogging())
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestWithHTTPLogging_SetsHeaderID(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(WithHTTPLogging())
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	headerID := resp.Header.Get(cn.HeaderID)
	assert.NotEmpty(t, headerID)
}

func TestWithHTTPLogging_NormalizesIncomingHeaderID(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(WithHTTPLogging())
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(cn.HeaderID, "  trace-123  ")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "trace-123", resp.Header.Get(cn.HeaderID))
}

func TestWithHTTPLogging_SkipsSwagger(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(WithHTTPLogging())
	app.Get("/swagger/doc.json", func(c *fiber.Ctx) error {
		return c.SendString("{}")
	})

	req := httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestWithHTTPLogging_PostWithJSONBody(t *testing.T) {
	t.Parallel()

	logger := newCaptureLogger()
	app := fiber.New()
	app.Use(WithHTTPLogging(WithCustomLogger(logger)))
	app.Post("/api", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusCreated)
	})

	body := strings.NewReader(`{"username":"admin","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api?token=abc123", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", "https://user:pass@example.com/path?token=abc123#frag")
	req.Header.Set(cn.HeaderUserAgent, "good-agent\r\nforged")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	entries := logger.entries()
	require.Len(t, entries, 1)
	assert.Equal(t, log.LevelInfo, entries[0].level)
	assert.NotContains(t, entries[0].msg, "secret")
	assert.NotContains(t, entries[0].msg, "abc123")
	assert.NotContains(t, entries[0].msg, "\r")
	assert.NotContains(t, entries[0].msg, "\n")
	assert.Contains(t, entries[0].msg, "https://example.com/path")
}

func TestGetBodyObfuscatedString_DispatchesByContentType(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var got string

	app.Post("/api", func(c *fiber.Ctx) error {
		got = getBodyObfuscatedString(c, c.Body())
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(`{"password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.NotContains(t, got, "secret")
	assert.Contains(t, got, cn.ObfuscatedValue)
}

func TestGetBodyObfuscatedString_UnknownContentTypeReturnsRawBody(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var got string

	app.Post("/api", func(c *fiber.Ctx) error {
		got = getBodyObfuscatedString(c, c.Body())
		return c.SendStatus(http.StatusOK)
	})

	body := "plain text body"
	req := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, body, got)
}

// ---------------------------------------------------------------------------
// handleJSONBody: array support
// ---------------------------------------------------------------------------

func TestHandleJSONBody_ArrayTopLevel(t *testing.T) {
	t.Parallel()

	input := `[{"name":"alice","password":"secret"},{"name":"bob","api_key":"key123"}]`
	result := handleJSONBody([]byte(input))

	assert.NotContains(t, result, "secret")
	assert.NotContains(t, result, "key123")
	assert.Contains(t, result, "alice")
	assert.Contains(t, result, "bob")
	assert.Contains(t, result, cn.ObfuscatedValue)
}

func TestHandleJSONBody_ArrayOfPrimitives(t *testing.T) {
	t.Parallel()

	input := `[1, 2, 3]`
	result := handleJSONBody([]byte(input))
	assert.Equal(t, `[1,2,3]`, result)
}

func TestHandleJSONBody_EmptyArray(t *testing.T) {
	t.Parallel()

	input := `[]`
	result := handleJSONBody([]byte(input))
	assert.Equal(t, `[]`, result)
}

// ---------------------------------------------------------------------------
// Obfuscation depth limit
// ---------------------------------------------------------------------------

func nestedMapWithPassword(levels int, password string) map[string]any {
	node := map[string]any{"password": password}

	for i := 0; i < levels; i++ {
		node = map[string]any{"level": node}
	}

	return node
}

func nestedMapPassword(data map[string]any, levels int) string {
	current := data
	for i := 0; i < levels; i++ {
		next, ok := current["level"].(map[string]any)
		if !ok {
			return ""
		}

		current = next
	}

	password, _ := current["password"].(string)

	return password
}

func nestedSliceWithPassword(wrappers int, password string) []any {
	var node any = map[string]any{"password": password}

	for i := 0; i < wrappers; i++ {
		node = []any{node}
	}

	data, _ := node.([]any)

	return data
}

func nestedSlicePassword(data []any, wrappers int) string {
	var current any = data
	for i := 0; i < wrappers; i++ {
		next, ok := current.([]any)
		if !ok || len(next) == 0 {
			return ""
		}

		current = next[0]
	}

	node, ok := current.(map[string]any)
	if !ok {
		return ""
	}

	password, _ := node["password"].(string)

	return password
}

func TestObfuscateMapRecursively_DepthLimit(t *testing.T) {
	t.Parallel()

	t.Run("obfuscates before boundary", func(t *testing.T) {
		t.Parallel()

		levels := maxObfuscationDepth - 1 // password at depth 31 when max is 32
		data := nestedMapWithPassword(levels, "deep-secret")

		obfuscateMapRecursively(data, 0)
		assert.Equal(t, cn.ObfuscatedValue, nestedMapPassword(data, levels))
	})

	t.Run("does not obfuscate at boundary", func(t *testing.T) {
		t.Parallel()

		levels := maxObfuscationDepth // password at depth 32 when max is 32
		data := nestedMapWithPassword(levels, "deep-secret")

		obfuscateMapRecursively(data, 0)
		assert.Equal(t, "deep-secret", nestedMapPassword(data, levels))
	})
}

func TestObfuscateSliceRecursively_DepthLimit(t *testing.T) {
	t.Parallel()

	t.Run("obfuscates before boundary", func(t *testing.T) {
		t.Parallel()

		wrappers := maxObfuscationDepth - 1 // map processed at depth 31
		data := nestedSliceWithPassword(wrappers, "deep-secret")

		obfuscateSliceRecursively(data, 0)
		assert.Equal(t, cn.ObfuscatedValue, nestedSlicePassword(data, wrappers))
	})

	t.Run("does not obfuscate at boundary", func(t *testing.T) {
		t.Parallel()

		wrappers := maxObfuscationDepth // map reached at depth 32
		data := nestedSliceWithPassword(wrappers, "deep-secret")

		obfuscateSliceRecursively(data, 0)
		assert.Equal(t, "deep-secret", nestedSlicePassword(data, wrappers))
	})
}

// ---------------------------------------------------------------------------
// handleMultipartBody
// ---------------------------------------------------------------------------

func TestHandleMultipartBody_ViaMiddleware(t *testing.T) {
	t.Parallel()

	// We test multipart by going through the middleware stack.
	// The handleMultipartBody function requires a fiber.Ctx with a parsed multipart form.
	boundary := "testboundary"
	body := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"username\"\r\n\r\n" +
		"admin\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"password\"\r\n\r\n" +
		"my-secret\r\n" +
		"--" + boundary + "--\r\n"

	app := fiber.New()

	var capturedBody string

	app.Post("/test", func(c *fiber.Ctx) error {
		capturedBody = handleMultipartBody(c)
		return c.SendStatus(200)
	})

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.NotContains(t, capturedBody, "my-secret")
	assert.Contains(t, capturedBody, "username=admin")
}

// ---------------------------------------------------------------------------
// NewRequestInfo and FinishRequestInfo nil guards
// ---------------------------------------------------------------------------

func TestNewRequestInfo_NilContext(t *testing.T) {
	t.Parallel()

	info := NewRequestInfo(nil, false)
	require.NotNil(t, info)
	assert.False(t, info.Date.IsZero(), "should set Date even with nil context")
}

func TestFinishRequestInfo_NilWrapper(t *testing.T) {
	t.Parallel()

	info := &RequestInfo{Date: time.Now().Add(-50 * time.Millisecond)}

	// Should not panic
	info.FinishRequestInfo(nil)

	// Status and Size should remain zero
	assert.Equal(t, 0, info.Status)
	assert.Equal(t, 0, info.Size)
}

// ---------------------------------------------------------------------------
// WithObfuscationDisabled option
// ---------------------------------------------------------------------------

func TestWithObfuscationDisabled_True(t *testing.T) {
	t.Parallel()

	mid := buildOpts(WithObfuscationDisabled(true))
	assert.True(t, mid.ObfuscationDisabled)
}

func TestWithObfuscationDisabled_False(t *testing.T) {
	t.Parallel()

	mid := buildOpts(WithObfuscationDisabled(false))
	assert.False(t, mid.ObfuscationDisabled)
}

func TestWithObfuscationDisabled_OverridesEnvDefault(t *testing.T) {
	t.Parallel()

	// Default value comes from env var (logObfuscationDisabled).
	// WithObfuscationDisabled should override it.
	mid := buildOpts(WithObfuscationDisabled(true))
	assert.True(t, mid.ObfuscationDisabled)

	mid2 := buildOpts(WithObfuscationDisabled(false))
	assert.False(t, mid2.ObfuscationDisabled)
}

// ---------------------------------------------------------------------------
// mockLogger for WithCustomLogger tests
// ---------------------------------------------------------------------------

type mockLogger struct{}

func (m *mockLogger) Log(context.Context, log.Level, string, ...log.Field) {}
func (m *mockLogger) With(...log.Field) log.Logger                         { return m }
func (m *mockLogger) WithGroup(string) log.Logger                          { return m }
func (m *mockLogger) Enabled(log.Level) bool                               { return true }
func (m *mockLogger) Sync(context.Context) error                           { return nil }
