//go:build unit

package ratelimit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			for _, key := range []string{
				"RATE_LIMIT_MAX",
				"RATE_LIMIT_WINDOW_SEC",
				"AGGRESSIVE_RATE_LIMIT_MAX",
				"AGGRESSIVE_RATE_LIMIT_WINDOW_SEC",
				"RELAXED_RATE_LIMIT_MAX",
				"RELAXED_RATE_LIMIT_WINDOW_SEC",
			} {
				t.Setenv(key, "")
			}

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

// --- WithExceededHandler tests: consumer-controlled 429 body responder ---

// TestMiddleware_ExceededHandlerNil_DefaultBodySnapshot locks the v5.1.0 wire format
// for the default 429 response: status code, headers, and the chttp.ErrorResponse
// envelope shape. This is the byte-identity guardrail the upstream prompt requires —
// if anything in this snapshot changes, the contract that says "exceededHandler==nil
// is byte-identical to v5.1.0" is broken.
func TestMiddleware_ExceededHandlerNil_DefaultBodySnapshot(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "snapshot", Max: 1, Window: 60 * time.Second}
	rl := New(conn) // no WithExceededHandler

	app := newTestApp(rl.WithRateLimit(tier))

	resp := doRequest(t, app)
	resp.Body.Close()

	resp = doRequest(t, app)
	defer resp.Body.Close()

	// Status code
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Headers — these are written before the body in handleLimitExceeded.
	assert.Equal(t, "60", resp.Header.Get("Retry-After"))
	assert.Equal(t, "1", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"))

	// Body — exact JSON shape of chttp.ErrorResponse with the legacy constants.
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp chttp.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))

	assert.Equal(t, http.StatusTooManyRequests, errResp.Code)
	assert.Equal(t, "rate_limit_exceeded", errResp.Title)
	assert.Equal(t, "rate limit exceeded", errResp.Message)
}

// TestMiddleware_ExceededHandlerSet_CustomBody verifies that when WithExceededHandler
// is configured, the consumer's responder is invoked, the custom body is written, and
// the headers continue to be set as before. The handler's return value MUST propagate.
func TestMiddleware_ExceededHandlerSet_CustomBody(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	var (
		handlerCalls atomic.Int32
		seenTier     atomic.Value
		seenTTL      atomic.Int64 // store ttl in nanoseconds
	)

	tier := Tier{Name: "custom-body", Max: 1, Window: 60 * time.Second}

	customBody := `{"error":{"code":"BTF-0429","service":"plugin","message":"slow down"}}`
	rl := New(conn, WithExceededHandler(func(c *fiber.Ctx, t Tier, ttl time.Duration) error {
		handlerCalls.Add(1)
		seenTier.Store(t.Name)
		seenTTL.Store(ttl.Nanoseconds())

		c.Status(http.StatusTooManyRequests)
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

		return c.SendString(customBody)
	}))

	app := newTestApp(rl.WithRateLimit(tier))

	// First request passes.
	resp := doRequest(t, app)
	resp.Body.Close()

	// Second request must trip the limit and invoke the custom responder.
	resp = doRequest(t, app)
	defer resp.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Headers must STILL be written (the option does not skip them).
	assert.Equal(t, "60", resp.Header.Get("Retry-After"))
	assert.Equal(t, "1", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"))

	// Body must be the custom JSON, NOT the chttp.ErrorResponse envelope.
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, customBody, string(body))

	// Handler invocation contract.
	require.Equal(t, int32(1), handlerCalls.Load(), "exceededHandler must fire exactly once")
	assert.Equal(t, tier.Name, seenTier.Load())
	assert.Greater(t, seenTTL.Load(), int64(0), "ttl must be positive")
	assert.LessOrEqual(t, time.Duration(seenTTL.Load()), tier.Window, "ttl must be at most the configured window")
}

// TestMiddleware_ExceededHandlerSet_PropagatesError verifies that the error returned
// by the consumer's responder is the error returned by the Fiber handler.
func TestMiddleware_ExceededHandlerSet_PropagatesError(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "propagate-err", Max: 1, Window: 60 * time.Second}

	sentinelErr := fiber.NewError(http.StatusInternalServerError, "custom-error-from-handler")
	rl := New(conn, WithExceededHandler(func(_ *fiber.Ctx, _ Tier, _ time.Duration) error {
		return sentinelErr
	}))

	// Use a fiber app with the default error handler so the error becomes a response.
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(rl.WithRateLimit(tier))
	app.Get("/test", func(c *fiber.Ctx) error { return c.SendString("ok") })

	// Prime the limit.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	resp.Body.Close()

	// Second request triggers the handler, which returns sentinelErr.
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	resp, err = app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Fiber's default error handler maps the returned *fiber.Error to its status code,
	// so propagation is observable through the response status (500 from sentinelErr,
	// not the 429 the handler would have written).
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"error returned from exceededHandler must propagate to Fiber chain")
}

// TestMiddleware_ExceededHandler_WithOnLimited verifies both options fire on a single
// 429, in declared order: onLimited (side-effect hook) then exceededHandler (body writer).
func TestMiddleware_ExceededHandler_WithOnLimited(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	// invocation order is captured into a slice protected by a mutex so the race
	// detector is satisfied even if Fiber's internal scheduling moved.
	var (
		mu    sync.Mutex
		order []string
	)

	tier := Tier{Name: "ordering", Max: 1, Window: 60 * time.Second}
	rl := New(conn,
		WithOnLimited(func(_ *fiber.Ctx, _ Tier) {
			mu.Lock()
			order = append(order, "onLimited")
			mu.Unlock()
		}),
		WithExceededHandler(func(c *fiber.Ctx, _ Tier, _ time.Duration) error {
			mu.Lock()
			order = append(order, "exceededHandler")
			mu.Unlock()

			return c.Status(http.StatusTooManyRequests).SendString("custom")
		}),
	)

	app := newTestApp(rl.WithRateLimit(tier))

	resp := doRequest(t, app)
	resp.Body.Close()

	resp = doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "custom", string(body))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"onLimited", "exceededHandler"}, order,
		"WithOnLimited must fire before WithExceededHandler on the same 429")
}

// TestMiddleware_ExceededHandler_FailClosedNotAffected verifies that the 503 fail-closed
// path (Redis unreachable, WithFailOpen(false)) is NOT routed through exceededHandler.
// The 503 path has different semantics from 429 and must keep its built-in body.
func TestMiddleware_ExceededHandler_FailClosedNotAffected(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	var handlerCalls atomic.Int32

	tier := Tier{Name: "503-isolation", Max: 10, Window: 60 * time.Second}
	rl := New(conn,
		WithFailOpen(false),
		WithExceededHandler(func(_ *fiber.Ctx, _ Tier, _ time.Duration) error {
			handlerCalls.Add(1)

			return nil
		}),
	)

	app := newTestApp(rl.WithRateLimit(tier))

	// Kill Redis to force the fail-closed path.
	mr.Close()

	resp := doRequest(t, app)
	defer resp.Body.Close()

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp chttp.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))

	// The built-in service_unavailable envelope must be emitted verbatim.
	assert.Equal(t, http.StatusServiceUnavailable, errResp.Code)
	assert.Equal(t, "service_unavailable", errResp.Title)
	assert.Equal(t, "rate limiter temporarily unavailable", errResp.Message)

	// exceededHandler MUST NOT have been invoked on the 503 path.
	assert.Zero(t, handlerCalls.Load(),
		"exceededHandler must not fire on Redis-unavailable (503) path")
}

// TestMiddleware_ExceededHandler_NilOptionIsNoOp verifies WithExceededHandler(nil) leaves
// the limiter in its default state (no handler installed; default body emitted on 429).
func TestMiddleware_ExceededHandler_NilOptionIsNoOp(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestMiddlewareRedisConnection(t, mr)

	tier := Tier{Name: "nil-option", Max: 1, Window: 60 * time.Second}
	rl := New(conn, WithExceededHandler(nil))
	require.NotNil(t, rl)
	assert.Nil(t, rl.exceededHandler, "WithExceededHandler(nil) must not install a non-nil handler")

	app := newTestApp(rl.WithRateLimit(tier))

	resp := doRequest(t, app)
	resp.Body.Close()

	resp = doRequest(t, app)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Default envelope still applies.
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp chttp.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "rate_limit_exceeded", errResp.Title)
}
