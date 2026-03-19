//go:build unit

package ratelimit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	chttp "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	libRedis "github.com/LerianStudio/lib-commons/v4/commons/redis"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// warnSpy is a minimal log.Logger that captures warning messages for assertions.
type warnSpy struct {
	mu   sync.Mutex
	msgs []string
}

func (s *warnSpy) Log(_ context.Context, level libLog.Level, msg string, _ ...libLog.Field) {
	if level == libLog.LevelWarn {
		s.mu.Lock()
		s.msgs = append(s.msgs, msg)
		s.mu.Unlock()
	}
}

func (s *warnSpy) With(_ ...libLog.Field) libLog.Logger { return s }
func (s *warnSpy) WithGroup(_ string) libLog.Logger     { return s }
func (s *warnSpy) Enabled(_ libLog.Level) bool          { return true }
func (s *warnSpy) Sync(_ context.Context) error         { return nil }

func (s *warnSpy) hasWarn(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range s.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}

	return false
}

func newTestMiddlewareRedisConnection(t *testing.T, mr *miniredis.Miniredis) *libRedis.Client {
	t.Helper()

	conn, err := libRedis.New(t.Context(), libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &libLog.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func newTestApp(handler fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(handler)
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	return app
}

// newTestAppWithProxyHeader creates a Fiber app that reads the client IP from
// X-Forwarded-For. This lets tests inject any address — including IPv6 — without
// depending on the synthetic RemoteAddr assigned by app.Test().
func newTestAppWithProxyHeader(handler fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Use(handler)
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	return app
}

func doRequest(t *testing.T, app *fiber.App) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	return resp
}

func doRequestWithHeader(t *testing.T, app *fiber.App, header, value string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(header, value)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	return resp
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		conn    *libRedis.Client
		opts    []Option
		wantNil bool
		checkFn func(t *testing.T, rl *RateLimiter)
	}{
		{
			name:    "nil connection returns nil",
			conn:    nil,
			wantNil: true,
		},
		{
			name: "valid connection returns non-nil",
			conn: func() *libRedis.Client {
				mr := miniredis.RunT(t)
				return newTestMiddlewareRedisConnection(t, mr)
			}(),
			wantNil: false,
		},
		{
			name: "with options applied",
			conn: func() *libRedis.Client {
				mr := miniredis.RunT(t)
				return newTestMiddlewareRedisConnection(t, mr)
			}(),
			opts: []Option{
				WithKeyPrefix("test"),
				WithFailOpen(false),
			},
			wantNil: false,
			checkFn: func(t *testing.T, rl *RateLimiter) {
				t.Helper()
				assert.Equal(t, "test", rl.keyPrefix)
				assert.False(t, rl.failOpen)
			},
		},
		{
			name: "with logger option",
			conn: func() *libRedis.Client {
				mr := miniredis.RunT(t)
				return newTestMiddlewareRedisConnection(t, mr)
			}(),
			opts: []Option{
				WithLogger(&libLog.NopLogger{}),
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rl := New(tt.conn, tt.opts...)

			if tt.wantNil {
				assert.Nil(t, rl)
				return
			}

			require.NotNil(t, rl)

			if tt.checkFn != nil {
				tt.checkFn(t, rl)
			}
		})
	}
}

func TestMiddleware_NilRateLimiter(t *testing.T) {
	t.Parallel()

	var rl *RateLimiter

	handler := rl.WithRateLimit(DefaultTier())
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_AllowsWithinLimit(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test", Max: 5, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	for range 5 {
		resp := doRequest(t, app)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

func TestMiddleware_BlocksExceedingLimit(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-block", Max: 3, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	// Use all allowed requests
	for range 3 {
		resp := doRequest(t, app)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// Fourth request should be blocked
	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestMiddleware_RetryAfterHeader(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-retry", Max: 1, Window: 120 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	// First request passes
	resp := doRequest(t, app)
	resp.Body.Close()

	// Second request is blocked
	resp = doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "120", resp.Header.Get("Retry-After"))
}

func TestMiddleware_RateLimitHeaders(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-headers", Max: 10, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "10", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "9", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"))

	// Verify reset is a valid unix timestamp in the future
	resetStr := resp.Header.Get("X-RateLimit-Reset")
	resetUnix, err := strconv.ParseInt(resetStr, 10, 64)
	require.NoError(t, err)
	assert.Greater(t, resetUnix, time.Now().Unix()-1)
}

func TestMiddleware_RateLimitHeadersOnBlocked(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-headers-block", Max: 1, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	// First request passes
	resp := doRequest(t, app)
	resp.Body.Close()

	// Second request is blocked — check headers
	resp = doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"))
	assert.Equal(t, "60", resp.Header.Get("Retry-After"))
}

func TestMiddleware_ResponseBody(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-body", Max: 1, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	// First request passes
	resp := doRequest(t, app)
	resp.Body.Close()

	// Second request is blocked — check body
	resp = doRequest(t, app)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp chttp.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))

	assert.Equal(t, http.StatusTooManyRequests, errResp.Code)
	assert.Equal(t, "rate_limit_exceeded", errResp.Title)
	assert.Equal(t, "rate limit exceeded", errResp.Message)
}

func TestMiddleware_TierIsolation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tierA := Tier{Name: "tier-a", Max: 2, Window: 60 * time.Second}
	tierB := Tier{Name: "tier-b", Max: 2, Window: 60 * time.Second}
	rl := New(conn)

	appA := fiber.New(fiber.Config{DisableStartupMessage: true})
	appA.Use(rl.WithRateLimit(tierA))
	appA.Get("/test", func(c *fiber.Ctx) error { return c.SendString("ok") })

	appB := fiber.New(fiber.Config{DisableStartupMessage: true})
	appB.Use(rl.WithRateLimit(tierB))
	appB.Get("/test", func(c *fiber.Ctx) error { return c.SendString("ok") })

	// Exhaust tier A
	for range 2 {
		resp := doRequest(t, appA)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// Tier A is now blocked
	resp := doRequest(t, appA)
	resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Tier B should still allow requests
	resp = doRequest(t, appB)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_FailOpen(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-failopen", Max: 10, Window: 60 * time.Second}
	rl := New(conn, WithFailOpen(true))

	app := newTestApp(rl.WithRateLimit(tier))

	// Close miniredis to simulate Redis failure
	mr.Close()

	resp := doRequest(t, app)
	defer resp.Body.Close()

	// Should pass through (fail-open)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_FailClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-failclosed", Max: 10, Window: 60 * time.Second}
	rl := New(conn, WithFailOpen(false))

	app := newTestApp(rl.WithRateLimit(tier))

	// Close miniredis to simulate Redis failure
	mr.Close()

	resp := doRequest(t, app)
	defer resp.Body.Close()

	// Should return 503 (fail-closed)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp chttp.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))

	assert.Equal(t, http.StatusServiceUnavailable, errResp.Code)
	assert.Equal(t, "service_unavailable", errResp.Title)
}

func TestMiddleware_CustomIdentityFunc(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-custom-id", Max: 2, Window: 60 * time.Second}
	rl := New(conn, WithIdentityFunc(IdentityFromHeader("X-User-ID")))

	app := newTestApp(rl.WithRateLimit(tier))

	// User A: 2 requests allowed
	for range 2 {
		resp := doRequestWithHeader(t, app, "X-User-ID", "user-a")
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// User A: 3rd request blocked
	resp := doRequestWithHeader(t, app, "X-User-ID", "user-a")
	resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// User B: should still be allowed (different identity)
	resp = doRequestWithHeader(t, app, "X-User-ID", "user-b")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_KeyPrefix(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-prefix", Max: 1, Window: 60 * time.Second}
	rl := New(conn, WithKeyPrefix("my-svc"))

	app := newTestApp(rl.WithRateLimit(tier))

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify the key was created with the expected prefix in Redis
	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Contains(t, keys[0], "my-svc:ratelimit:test-prefix:")
}

func TestMiddleware_MultipleTiers(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	globalTier := Tier{Name: "global", Max: 10, Window: 60 * time.Second}
	strictTier := Tier{Name: "strict", Max: 2, Window: 60 * time.Second}

	rl := New(conn)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(rl.WithRateLimit(globalTier))

	strict := app.Group("/strict")
	strict.Use(rl.WithRateLimit(strictTier))
	strict.Get("/endpoint", func(c *fiber.Ctx) error { return c.SendString("ok") })

	app.Get("/normal", func(c *fiber.Ctx) error { return c.SendString("ok") })

	// Strict endpoint: 2 requests allowed, 3rd blocked by strict tier
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/strict/endpoint", nil)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	req := httptest.NewRequest(http.MethodGet, "/strict/endpoint", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Normal endpoint should still be allowed under global tier
	req = httptest.NewRequest(http.MethodGet, "/normal", nil)

	resp, err = app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIdentityFromIP(t *testing.T) {
	t.Parallel()

	fn := IdentityFromIP()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/test", func(c *fiber.Ctx) error {
		identity := fn(c)
		return c.SendString(identity)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Fiber returns "0.0.0.0" for test requests without a real connection
	assert.NotEmpty(t, string(body))
}

func TestIdentityFromHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     string
		headerVal  string
		wantPrefix string
	}{
		{
			name:       "header present",
			header:     "X-User-ID",
			headerVal:  "user-123",
			wantPrefix: "hdr:user-123",
		},
		{
			name:       "header absent falls back to IP",
			header:     "X-User-ID",
			headerVal:  "",
			wantPrefix: "", // will be "ip:<encoded-ip>", just check non-empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fn := IdentityFromHeader(tt.header)

			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Get("/test", func(c *fiber.Ctx) error {
				return c.SendString(fn(c))
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.headerVal != "" {
				req.Header.Set(tt.header, tt.headerVal)
			}

			resp, err := app.Test(req, -1)
			require.NoError(t, err)
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			if tt.wantPrefix != "" {
				assert.Equal(t, tt.wantPrefix, string(body))
			} else {
				assert.NotEmpty(t, string(body))
			}
		})
	}
}

func TestIdentityFromIPAndHeader(t *testing.T) {
	t.Parallel()

	fn := IdentityFromIPAndHeader("X-Tenant-ID")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString(fn(c))
	})

	t.Run("with header", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Tenant-ID", "tenant-abc")

		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// Should contain the URL-encoded, prefixed form of the tenant header.
		assert.Contains(t, string(body), "hdr:tenant-abc")
	})

	t.Run("without header", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/test", nil)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// Should not contain the tenant ID — only the IP is used as identity.
		assert.NotContains(t, string(body), "tenant-abc")
	})
}

func TestBuildKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prefix   string
		tier     Tier
		identity string
		wantKey  string
	}{
		{
			name:     "no prefix",
			prefix:   "",
			tier:     Tier{Name: "global"},
			identity: "192.168.1.1",
			wantKey:  "ratelimit:global:192.168.1.1",
		},
		{
			name:     "with prefix",
			prefix:   "tenant-manager",
			tier:     Tier{Name: "export"},
			identity: "10.0.0.1",
			wantKey:  "tenant-manager:ratelimit:export:10.0.0.1",
		},
		{
			name:     "with service prefix",
			prefix:   "my-svc",
			tier:     Tier{Name: "dispatch"},
			identity: "user-123",
			wantKey:  "my-svc:ratelimit:dispatch:user-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			conn := newTestMiddlewareRedisConnection(t, mr)

			rl := New(conn, WithKeyPrefix(tt.prefix))
			require.NotNil(t, rl)

			key := rl.buildKey(tt.tier, tt.identity)
			assert.Equal(t, tt.wantKey, key)
		})
	}
}

func TestWithDefaultRateLimit(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	handler := WithDefaultRateLimit(conn)
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "500", resp.Header.Get("X-RateLimit-Limit"))
}

func TestWithDefaultRateLimit_NilConnection(t *testing.T) {
	t.Parallel()

	// WithDefaultRateLimit with nil conn should return a pass-through handler
	handler := WithDefaultRateLimit(nil)
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_OnLimitedCallback(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	var callbackCalled atomic.Bool

	var (
		mu           sync.Mutex
		callbackTier Tier
	)

	tier := Tier{Name: "test-callback", Max: 1, Window: 60 * time.Second}
	rl := New(conn, WithOnLimited(func(_ *fiber.Ctx, t Tier) {
		callbackCalled.Store(true)
		mu.Lock()
		callbackTier = t
		mu.Unlock()
	}))

	app := newTestApp(rl.WithRateLimit(tier))

	// First request passes
	resp := doRequest(t, app)
	resp.Body.Close()

	// Second request triggers callback
	resp = doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.True(t, callbackCalled.Load())
	mu.Lock()
	tierName := callbackTier.Name
	mu.Unlock()
	assert.Equal(t, "test-callback", tierName)
}

func TestTierPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tier       Tier
		wantName   string
		wantMax    int
		wantWindow time.Duration
	}{
		{
			name:       "DefaultTier",
			tier:       DefaultTier(),
			wantName:   "default",
			wantMax:    500,
			wantWindow: 60 * time.Second,
		},
		{
			name:       "AggressiveTier",
			tier:       AggressiveTier(),
			wantName:   "aggressive",
			wantMax:    100,
			wantWindow: 60 * time.Second,
		},
		{
			name:       "RelaxedTier",
			tier:       RelaxedTier(),
			wantName:   "relaxed",
			wantMax:    1000,
			wantWindow: 60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantName, tt.tier.Name)
			assert.Equal(t, tt.wantMax, tt.tier.Max)
			assert.Equal(t, tt.wantWindow, tt.tier.Window)
		})
	}
}

func TestMiddleware_RemainingDecrementsCorrectly(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "test-remaining", Max: 5, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestApp(rl.WithRateLimit(tier))

	for i := range 5 {
		resp := doRequest(t, app)

		expectedRemaining := strconv.Itoa(4 - i)
		assert.Equal(t, expectedRemaining, resp.Header.Get("X-RateLimit-Remaining"),
			"request %d should have remaining=%s", i+1, expectedRemaining)

		resp.Body.Close()
	}
}

func TestMiddleware_NilIdentityFuncIgnored(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	// WithIdentityFunc(nil) should keep the default (IP-based)
	rl := New(conn, WithIdentityFunc(nil))
	require.NotNil(t, rl)
	require.NotNil(t, rl.identityFunc)
}

func TestMiddleware_NilLoggerIgnored(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	// WithLogger(nil) should keep the default (nop logger)
	rl := New(conn, WithLogger(nil))
	require.NotNil(t, rl)
	require.NotNil(t, rl.logger)
}

func TestNew_RateLimitEnabledEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantNil bool
	}{
		{
			name:    "disabled when RATE_LIMIT_ENABLED=false",
			envVal:  "false",
			wantNil: true,
		},
		{
			name:    "enabled when RATE_LIMIT_ENABLED=true",
			envVal:  "true",
			wantNil: false,
		},
		{
			name:    "enabled when RATE_LIMIT_ENABLED is empty",
			envVal:  "",
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RATE_LIMIT_ENABLED", tt.envVal)

			mr := miniredis.RunT(t)
			conn := newTestMiddlewareRedisConnection(t, mr)

			rl := New(conn)

			if tt.wantNil {
				assert.Nil(t, rl)
			} else {
				assert.NotNil(t, rl)
			}
		})
	}
}

func TestNew_RateLimitDisabled_PassThrough(t *testing.T) {
	t.Setenv("RATE_LIMIT_ENABLED", "false")

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	rl := New(conn)
	require.Nil(t, rl)

	// nil receiver should return pass-through handler
	handler := rl.WithRateLimit(DefaultTier())
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIncrementCounter_TTLSetOnFirstIncrement(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "ttl-test", Max: 10, Window: 60 * time.Second}
	rl := New(conn, WithKeyPrefix("svc"))

	// Make one request to trigger INCR
	app := newTestApp(rl.WithRateLimit(tier))
	resp := doRequest(t, app)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify that the key has a TTL set (Lua script atomicity guarantee)
	keys := mr.Keys()
	require.Len(t, keys, 1)
	ttl := mr.TTL(keys[0])
	assert.Greater(t, ttl, time.Duration(0), "key must have TTL after first increment")
}

func TestWithRedisTimeout_Applied(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	rl := New(conn, WithRedisTimeout(200*time.Millisecond))
	require.NotNil(t, rl)
	assert.Equal(t, 200*time.Millisecond, rl.redisTimeout)
}

func TestWithRedisTimeout_ZeroIgnored(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	rl := New(conn, WithRedisTimeout(0))
	require.NotNil(t, rl)
	assert.Equal(t, 500*time.Millisecond, rl.redisTimeout, "zero value should keep default timeout")
}

func TestMiddleware_DefaultRedisTimeout(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	rl := New(conn)
	require.NotNil(t, rl)
	assert.Equal(t, 500*time.Millisecond, rl.redisTimeout)
}

func TestMethodTierSelector_WriteMethods(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	writeTier := Tier{Name: "write", Max: 2, Window: 60 * time.Second}
	readTier := Tier{Name: "read", Max: 10, Window: 60 * time.Second}
	rl := New(conn)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(rl.WithDynamicRateLimit(MethodTierSelector(writeTier, readTier)))
	app.Post("/test", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/test", func(c *fiber.Ctx) error { return c.SendString("ok") })

	// POST uses write tier (max 2)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// 3rd POST is blocked by write tier
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// GET uses read tier (max 10) — still allowed
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err = app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMethodTierSelector_ReadMethods(t *testing.T) {
	t.Parallel()

	writeTier := Tier{Name: "write", Max: 5, Window: 60 * time.Second}
	readTier := Tier{Name: "read", Max: 100, Window: 60 * time.Second}

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	rl := New(conn)

	for _, method := range []string{
		fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Use(rl.WithDynamicRateLimit(MethodTierSelector(writeTier, readTier)))
			app.Add(method, "/test", func(c *fiber.Ctx) error { return c.SendString("ok") })

			req := httptest.NewRequest(method, "/test", nil)
			resp, err := app.Test(req, -1)
			require.NoError(t, err)
			defer resp.Body.Close()

			// read tier (max 100) — first request must be allowed
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			// X-RateLimit-Limit header reflects the read tier max
			assert.Equal(t, "100", resp.Header.Get("X-RateLimit-Limit"),
				"method %s should use read tier (max 100)", method)
		})
	}
}

func TestWithDynamicRateLimit_NilTierFunc(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	// Non-nil receiver with nil TierFunc should return a pass-through handler,
	// not panic. This differs from the nil-receiver test below.
	rl := New(conn)
	require.NotNil(t, rl)

	handler := rl.WithDynamicRateLimit(nil)
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	// No rate-limit headers should be set — the request passed through without counting.
	assert.Empty(t, resp.Header.Get("X-RateLimit-Limit"))
}

func TestWithDynamicRateLimit_NilRateLimiter(t *testing.T) {
	t.Parallel()

	var rl *RateLimiter

	fn := MethodTierSelector(DefaultTier(), RelaxedTier())
	handler := rl.WithDynamicRateLimit(fn)
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestTierPresets_FromEnv(t *testing.T) {
	tests := []struct {
		name       string
		envVars    map[string]string
		tierFn     func() Tier
		wantMax    int
		wantWindow time.Duration
	}{
		{
			name:       "DefaultTier reads RATE_LIMIT_MAX",
			envVars:    map[string]string{"RATE_LIMIT_MAX": "200"},
			tierFn:     DefaultTier,
			wantMax:    200,
			wantWindow: 60 * time.Second,
		},
		{
			name:       "DefaultTier reads RATE_LIMIT_WINDOW_SEC",
			envVars:    map[string]string{"RATE_LIMIT_WINDOW_SEC": "30"},
			tierFn:     DefaultTier,
			wantMax:    500,
			wantWindow: 30 * time.Second,
		},
		{
			name:       "AggressiveTier reads AGGRESSIVE_RATE_LIMIT_MAX",
			envVars:    map[string]string{"AGGRESSIVE_RATE_LIMIT_MAX": "50"},
			tierFn:     AggressiveTier,
			wantMax:    50,
			wantWindow: 60 * time.Second,
		},
		{
			name:       "AggressiveTier reads AGGRESSIVE_RATE_LIMIT_WINDOW_SEC",
			envVars:    map[string]string{"AGGRESSIVE_RATE_LIMIT_WINDOW_SEC": "120"},
			tierFn:     AggressiveTier,
			wantMax:    100,
			wantWindow: 120 * time.Second,
		},
		{
			name:       "RelaxedTier reads RELAXED_RATE_LIMIT_MAX",
			envVars:    map[string]string{"RELAXED_RATE_LIMIT_MAX": "5000"},
			tierFn:     RelaxedTier,
			wantMax:    5000,
			wantWindow: 60 * time.Second,
		},
		{
			name:       "RelaxedTier reads RELAXED_RATE_LIMIT_WINDOW_SEC",
			envVars:    map[string]string{"RELAXED_RATE_LIMIT_WINDOW_SEC": "300"},
			tierFn:     RelaxedTier,
			wantMax:    1000,
			wantWindow: 300 * time.Second,
		},
		{
			name:       "invalid env falls back to default",
			envVars:    map[string]string{"RATE_LIMIT_MAX": "not-a-number"},
			tierFn:     DefaultTier,
			wantMax:    500,
			wantWindow: 60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			tier := tt.tierFn()

			assert.Equal(t, tt.wantMax, tier.Max)
			assert.Equal(t, tt.wantWindow, tier.Window)
		})
	}
}

// ── IPv6 tests ────────────────────────────────────────────────────────────────
//
// These tests verify that identity extractors and the rate limit middleware handle
// IPv6 client addresses correctly. IPv6 addresses contain colons (e.g. "2001:db8::1"),
// which is why the previous assertion in TestIdentityFromIPAndHeader ("without header"
// sub-test) used NotContains(":") — it would have incorrectly failed for IPv6 clients.

func TestIdentityFromIP_IPv6(t *testing.T) {
	t.Parallel()

	fn := IdentityFromIP()

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString(fn(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "2001:db8::1")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, "2001:db8::1", string(body))
}

func TestIdentityFromIPAndHeader_IPv6_WithoutHeader(t *testing.T) {
	t.Parallel()

	fn := IdentityFromIPAndHeader("X-Tenant-ID")

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString(fn(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "2001:db8::1")
	// No X-Tenant-ID — only the IPv6 address is used.

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	identity := string(body)

	// With URL encoding, the IPv6 address becomes "2001%3Adb8%3A%3A1" and the identity
	// is prefixed with "ip:". No tenant header is present so there is no ":hdr:" segment.
	assert.Equal(t, "ip:2001%3Adb8%3A%3A1", identity)
	assert.NotContains(t, identity, "tenant-abc")
}

func TestIdentityFromIPAndHeader_IPv6_WithHeader(t *testing.T) {
	t.Parallel()

	fn := IdentityFromIPAndHeader("X-Tenant-ID")

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString(fn(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "2001:db8::1")
	req.Header.Set("X-Tenant-ID", "tenant-abc")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Combined identity: "ip:<encoded-ipv6>#hdr:<tenant>" — # is the inter-component
	// separator; IPv6 colons are URL-encoded to %3A so they can't be confused with it.
	assert.Equal(t, "ip:2001%3Adb8%3A%3A1#hdr:tenant-abc", string(body))
}

func TestMiddleware_IPv6_RateLimiting(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "ipv6-test", Max: 2, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestAppWithProxyHeader(rl.WithRateLimit(tier))

	doIPv6Req := func() *http.Response {
		t.Helper()

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Forwarded-For", "2001:db8::1")

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		return resp
	}

	// First two requests are allowed.
	for range 2 {
		resp := doIPv6Req()
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// Third request is blocked.
	resp := doIPv6Req()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// IdentityFromIP() returns the raw IP without encoding, so the Redis key embeds
	// the IPv6 address as-is. URL encoding only applies to IdentityFromHeader and
	// IdentityFromIPAndHeader.
	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Contains(t, keys[0], "2001:db8::1")
}

func TestMiddleware_IPv6_Isolation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "ipv6-isolation", Max: 1, Window: 60 * time.Second}
	rl := New(conn)

	app := newTestAppWithProxyHeader(rl.WithRateLimit(tier))

	doReq := func(ip string) *http.Response {
		t.Helper()

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Forwarded-For", ip)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		return resp
	}

	// IPv6 client exhausts its quota.
	resp := doReq("2001:db8::1")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp = doReq("2001:db8::1")
	resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// A different IPv6 address has its own independent counter.
	resp = doReq("2001:db8::2")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// An IPv4 client also has its own independent counter.
	resp = doReq("192.168.1.1")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestWithRateLimit_HighTierWarning verifies that configuring a tier with Max above
// maxReasonableTierMax causes a warning to be logged at setup time (not per request).
func TestWithRateLimit_ZeroWindow(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	rl := New(conn)
	require.NotNil(t, rl)

	// A zero window rounds down to PEXPIRE 0, immediately expiring all keys.
	// The middleware must reject all requests rather than silently bypassing the limit.
	zeroTier := Tier{Name: "bad-window", Max: 100, Window: 0}
	handler := rl.WithRateLimit(zeroTier)
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestWithRateLimit_SubMillisecondWindow(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	rl := New(conn)
	require.NotNil(t, rl)

	// A window smaller than 1ms truncates to 0 when converted via .Milliseconds() — also invalid.
	subMsTier := Tier{Name: "subms-window", Max: 100, Window: 999 * time.Microsecond}
	handler := rl.WithRateLimit(subMsTier)
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestWithDynamicRateLimit_ZeroWindow(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	rl := New(conn)
	require.NotNil(t, rl)

	// TierFunc returns a zero-window tier on every request — must be rejected per request.
	handler := rl.WithDynamicRateLimit(func(_ *fiber.Ctx) Tier {
		return Tier{Name: "dynamic-bad-window", Max: 100, Window: 0}
	})
	app := newTestApp(handler)

	resp := doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestNew_RedisTimeoutNonPositiveEnv(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
	}{
		{name: "zero", envVal: "0"},
		{name: "negative", envVal: "-100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RATE_LIMIT_REDIS_TIMEOUT_MS", tt.envVal)

			mr := miniredis.RunT(t)
			conn := newTestMiddlewareRedisConnection(t, mr)

			rl := New(conn)
			require.NotNil(t, rl)

			assert.Equal(t, 500*time.Millisecond, rl.redisTimeout,
				"non-positive env value should clamp to fallback timeout")
		})
	}
}

func TestWithRateLimit_HighTierWarning(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	spy := &warnSpy{}
	rl := New(conn, WithLogger(spy))
	require.NotNil(t, rl)

	highTier := Tier{Name: "high", Max: maxReasonableTierMax + 1, Window: 60 * time.Second}
	handler := rl.WithRateLimit(highTier)
	require.NotNil(t, handler)

	assert.True(t, spy.hasWarn("rate limit tier max is unusually high"),
		"expected warning when tier.Max exceeds %d", maxReasonableTierMax)
}

// TestMethodTierSelector_OtherWriteMethods verifies that PUT, PATCH, and DELETE are
// treated as write-tier methods, consistent with POST.
func TestMethodTierSelector_OtherWriteMethods(t *testing.T) {
	t.Parallel()

	writeTier := Tier{Name: "write", Max: 5, Window: 60 * time.Second}
	readTier := Tier{Name: "read", Max: 100, Window: 60 * time.Second}

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)
	rl := New(conn)

	for _, method := range []string{
		fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete,
	} {
		m := method
		t.Run(m, func(t *testing.T) {
			t.Parallel()

			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Use(rl.WithDynamicRateLimit(MethodTierSelector(writeTier, readTier)))
			app.Add(m, "/test", func(c *fiber.Ctx) error { return c.SendString("ok") })

			req := httptest.NewRequest(m, "/test", nil)

			resp, err := app.Test(req, -1)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "5", resp.Header.Get("X-RateLimit-Limit"),
				"method %s should use write tier (max 5)", m)
		})
	}
}
