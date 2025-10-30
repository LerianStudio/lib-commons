package http

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/net/http/ratelimit"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
)

// mockLimiterForHTTP is a mock implementation of ratelimit.Limiter for HTTP testing
type mockLimiterForHTTP struct {
	config      ratelimit.Config
	allowResult *ratelimit.Result
	allowError  error
	resetError  error
}

func (m *mockLimiterForHTTP) Allow(ctx context.Context, key string) (*ratelimit.Result, error) {
	if m.allowError != nil {
		return nil, m.allowError
	}
	if m.allowResult != nil {
		return m.allowResult, nil
	}
	return &ratelimit.Result{
		Allowed:    true,
		Remaining:  m.config.Max - 1,
		Limit:      m.config.Max,
		ResetAt:    time.Now().Add(m.config.Window),
		RetryAfter: m.config.Window,
	}, nil
}

func (m *mockLimiterForHTTP) Reset(ctx context.Context, key string) error {
	return m.resetError
}

func (m *mockLimiterForHTTP) GetConfig() ratelimit.Config {
	return m.config
}

func TestRateLimitMiddleware_Success(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
		allowResult: &ratelimit.Result{
			Allowed:    true,
			Remaining:  99,
			Limit:      100,
			ResetAt:    time.Now().Add(time.Minute),
			RetryAfter: time.Minute,
		},
	}

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		IncludeHeaders: true,
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.Equal(t, "100", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "99", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"))

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "OK", string(body))
}

func TestRateLimitMiddleware_Exceeded(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
		allowResult: &ratelimit.Result{
			Allowed:    false,
			Remaining:  0,
			Limit:      100,
			ResetAt:    time.Now().Add(time.Minute),
			RetryAfter: time.Minute,
		},
	}

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		ErrorCode:      "CUSTOM_RATE_LIMIT",
		ErrorMessage:   "Custom rate limit message",
		IncludeHeaders: true,
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "100", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
}

func TestRateLimitMiddleware_SkipPaths(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
	}

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		SkipPaths: []string{"/health", "/metrics"},
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("Healthy")
	})
	app.Get("/metrics", func(c *fiber.Ctx) error {
		return c.SendString("Metrics")
	})
	app.Get("/api", func(c *fiber.Ctx) error {
		return c.SendString("API")
	})

	// Test skip path /health
	req := httptest.NewRequest("GET", "/health", nil)
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	// Test skip path /metrics
	req = httptest.NewRequest("GET", "/metrics", nil)
	resp, err = app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	// Test non-skip path /api
	req = httptest.NewRequest("GET", "/api", nil)
	resp, err = app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
}

func TestRateLimitMiddleware_EmptyKey(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
	}

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			// Return empty key to skip rate limiting
			return ""
		},
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
}

func TestRateLimitMiddleware_FailOpen(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
		allowError: errors.New("redis connection failed"),
	}

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		FailureMode: ratelimit.FailOpen,
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
}

func TestRateLimitMiddleware_FailClosed(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
		allowError: errors.New("redis connection failed"),
	}

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		FailureMode: ratelimit.FailClosed,
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusServiceUnavailable, resp.StatusCode)
}

func TestRateLimitMiddleware_DefaultValues(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
	}

	// Config with minimal settings - should use defaults
	config := RateLimitConfig{
		Limiter:        limiter,
		IncludeHeaders: true, // Explicitly set to test header inclusion
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	// Verify headers are included when explicitly enabled
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Limit"))
}

func TestRateLimitMiddleware_WithCallbacks(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
		allowResult: &ratelimit.Result{
			Allowed:    false,
			Remaining:  0,
			Limit:      100,
			ResetAt:    time.Now().Add(time.Minute),
			RetryAfter: time.Minute,
		},
	}

	exceededCalled := false
	errorCalled := false

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		OnRateLimitExceeded: func(c *fiber.Ctx, key string, result *ratelimit.Result) {
			exceededCalled = true
		},
		OnError: func(c *fiber.Ctx, err error) {
			errorCalled = true
		},
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusTooManyRequests, resp.StatusCode)
	assert.True(t, exceededCalled)
	assert.False(t, errorCalled)
}

func TestRateLimitMiddleware_ErrorCallback(t *testing.T) {
	limiter := &mockLimiterForHTTP{
		config: ratelimit.Config{
			Max:    100,
			Window: time.Minute,
		},
		allowError: errors.New("test error"),
	}

	errorCalled := false

	config := RateLimitConfig{
		Limiter: limiter,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		FailureMode: ratelimit.FailOpen,
		OnError: func(c *fiber.Ctx, err error) {
			errorCalled = true
		},
	}

	app := fiber.New()
	app.Use(RateLimitMiddleware(config))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.True(t, errorCalled)
}

func TestKeyByIP(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		key := KeyByIP(c)
		return c.SendString(key)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	resp, err := app.Test(req)

	assert.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "ip:")
}

func TestKeyByIPAndEndpoint(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		key := KeyByIPAndEndpoint(c)
		return c.SendString(key)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	resp, err := app.Test(req)

	assert.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "ip:")
	assert.Contains(t, string(body), "endpoint:")
}

func TestKeyByHeader(t *testing.T) {
	tests := []struct {
		name       string
		headerName string
		headerValue string
		expectFallback bool
	}{
		{
			name:        "header present",
			headerName:  "X-API-Key",
			headerValue: "test-api-key-123",
			expectFallback: false,
		},
		{
			name:        "header missing - fallback to IP",
			headerName:  "X-API-Key",
			headerValue: "",
			expectFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New()
			keyGen := KeyByHeader(tt.headerName)

			app.Get("/test", func(c *fiber.Ctx) error {
				key := keyGen(c)
				return c.SendString(key)
			})

			req := httptest.NewRequest("GET", "/test", nil)
			if tt.headerValue != "" {
				req.Header.Set(tt.headerName, tt.headerValue)
			}
			resp, err := app.Test(req)

			assert.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)

			if tt.expectFallback {
				assert.Contains(t, string(body), "ip:")
			} else {
				assert.Contains(t, string(body), "header:")
				assert.Contains(t, string(body), tt.headerValue)
			}
		})
	}
}

func TestKeyComposite(t *testing.T) {
	app := fiber.New()

	keyGen := KeyComposite(
		func(c *fiber.Ctx) string { return "ip:" + c.IP() },
		func(c *fiber.Ctx) string { return "endpoint:" + c.Path() },
	)

	app.Get("/test", func(c *fiber.Ctx) error {
		key := keyGen(c)
		return c.SendString(key)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "ip:")
	assert.Contains(t, string(body), "endpoint:")
}

func TestKeyComposite_EmptyParts(t *testing.T) {
	app := fiber.New()

	keyGen := KeyComposite(
		func(c *fiber.Ctx) string { return "ip:" + c.IP() },
		func(c *fiber.Ctx) string { return "" }, // Empty part
		func(c *fiber.Ctx) string { return "endpoint:" + c.Path() },
	)

	app.Get("/test", func(c *fiber.Ctx) error {
		key := keyGen(c)
		return c.SendString(key)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "ip:")
	assert.Contains(t, string(body), "endpoint:")
}

func TestShouldSkipPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		skipPaths []string
		expected  bool
	}{
		{
			name:      "path in skip list",
			path:      "/health",
			skipPaths: []string{"/health", "/metrics"},
			expected:  true,
		},
		{
			name:      "path not in skip list",
			path:      "/api",
			skipPaths: []string{"/health", "/metrics"},
			expected:  false,
		},
		{
			name:      "empty skip list",
			path:      "/health",
			skipPaths: []string{},
			expected:  false,
		},
		{
			name:      "nil skip list",
			path:      "/health",
			skipPaths: nil,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSkipPath(tt.path, tt.skipPaths)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSetRateLimitHeaders(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		result := &ratelimit.Result{
			Allowed:    true,
			Remaining:  99,
			Limit:      100,
			ResetAt:    time.Unix(1700000000, 0),
			RetryAfter: time.Minute,
		}
		setRateLimitHeaders(c, result)
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, "100", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "99", resp.Header.Get("X-RateLimit-Remaining"))
	assert.Equal(t, "1700000000", resp.Header.Get("X-RateLimit-Reset"))
}
