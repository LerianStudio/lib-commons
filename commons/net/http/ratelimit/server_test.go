//go:build unit

// Package ratelimit_test demonstrates the rate limit middleware in realistic Fiber
// server configurations. Unlike middleware_test.go (which tests individual behaviors
// in isolation using white-box access), this file uses only the public API and builds
// complete API servers that mirror production usage patterns.
package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/net/http/ratelimit"
	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func newServerTestConn(t *testing.T, mr *miniredis.Miniredis) *libRedis.Client {
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

// buildAPIServer returns a Fiber app that resembles a production multi-tier API:
//
//	GET  /public/info   → relaxed tier  (max = 20 req / 60 s)
//	GET  /admin/config  → strict tier   (max = 3  req / 60 s)
//	GET  /api/items     → read tier     (max = 10 req / 60 s)
//	POST /api/items     → write tier    (max = 3  req / 60 s)
//
// Write/read tiers are applied via WithDynamicRateLimit + MethodTierSelector,
// demonstrating method-sensitive rate limiting on the same route group.
func buildAPIServer(rl *ratelimit.RateLimiter) *fiber.App {
	relaxed := ratelimit.Tier{Name: "public", Max: 20, Window: 60 * time.Second}
	strict := ratelimit.Tier{Name: "admin", Max: 3, Window: 60 * time.Second}
	write := ratelimit.Tier{Name: "write", Max: 3, Window: 60 * time.Second}
	read := ratelimit.Tier{Name: "read", Max: 10, Window: 60 * time.Second}

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		// ProxyHeader lets tests inject any IP (including IPv6) via X-Forwarded-For.
		ProxyHeader: fiber.HeaderXForwardedFor,
	})

	public := app.Group("/public")
	public.Use(rl.WithRateLimit(relaxed))
	public.Get("/info", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	admin := app.Group("/admin")
	admin.Use(rl.WithRateLimit(strict))
	admin.Get("/config", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"config": "redacted"})
	})

	api := app.Group("/api")
	api.Use(rl.WithDynamicRateLimit(ratelimit.MethodTierSelector(write, read)))
	api.Get("/items", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"items": []string{"a", "b", "c"}})
	})
	api.Post("/items", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"created": true})
	})

	return app
}

func serverGet(t *testing.T, app *fiber.App, path, ip string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	return resp
}

func serverPost(t *testing.T, app *fiber.App, path, ip string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, nil)
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestServer_MultiTierRouteGroups verifies that different route groups enforce
// their own independent limits. Exhausting the /admin tier must not affect /public.
func TestServer_MultiTierRouteGroups(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newServerTestConn(t, mr)

	rl := ratelimit.New(conn, ratelimit.WithKeyPrefix("svc"))
	app := buildAPIServer(rl)

	const ip = "10.0.0.1"

	// Exhaust the strict admin tier (max = 3).
	for i := 1; i <= 3; i++ {
		resp := serverGet(t, app, "/admin/config", ip)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "admin request %d should pass", i)
	}

	resp := serverGet(t, app, "/admin/config", ip)
	resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "4th admin request should be blocked")

	// The public tier (max = 20) must be completely unaffected.
	resp = serverGet(t, app, "/public/info", ip)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "public route should remain accessible")
	assert.Equal(t, "20", resp.Header.Get("X-RateLimit-Limit"))
}

// TestServer_WindowReset verifies that the rate limit counter resets automatically
// after the window elapses. miniredis.FastForward is used to advance the internal
// clock without real wall-clock waiting.
func TestServer_WindowReset(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newServerTestConn(t, mr)

	shortWindow := ratelimit.Tier{Name: "short", Max: 2, Window: 5 * time.Second}
	rl := ratelimit.New(conn)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Use(rl.WithRateLimit(shortWindow))
	app.Get("/ping", func(c *fiber.Ctx) error { return c.SendString("pong") })

	doReq := func() *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0.3")

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		return resp
	}

	// Exhaust the window (max = 2).
	for i := 1; i <= 2; i++ {
		resp := doReq()
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "request %d within window should pass", i)
	}

	// Third request is blocked.
	resp := doReq()
	resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "3rd request should be blocked")

	// Advance miniredis clock past the 5-second window.
	mr.FastForward(6 * time.Second)

	// First request of the new window must pass, remaining resets to max-1.
	resp = doReq()
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "request after window reset should pass")
	assert.Equal(t, "1", resp.Header.Get("X-RateLimit-Remaining"), "counter should have reset")
}

// TestServer_ClientIsolation verifies that each client IP (IPv4 and IPv6) maintains
// its own independent counter, so one client exhausting its quota does not affect others.
func TestServer_ClientIsolation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newServerTestConn(t, mr)

	tier := ratelimit.Tier{Name: "iso", Max: 2, Window: 60 * time.Second}
	rl := ratelimit.New(conn)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Use(rl.WithRateLimit(tier))
	app.Get("/data", func(c *fiber.Ctx) error { return c.SendString("ok") })

	clients := []string{
		"10.1.1.1",    // IPv4
		"10.1.1.2",    // IPv4 different subnet
		"2001:db8::1", // IPv6
		"2001:db8::2", // IPv6 different host
	}

	type result struct {
		ip      string
		blocked bool
		err     error
	}

	results := make([]result, len(clients))

	var wg sync.WaitGroup

	for i, ip := range clients {
		wg.Add(1)

		go func() {
			defer wg.Done()

			// Each client fires 2 requests within quota.
			for range 2 {
				req := httptest.NewRequest(http.MethodGet, "/data", nil)
				req.Header.Set("X-Forwarded-For", ip)

				resp, err := app.Test(req, -1)
				if err != nil {
					results[i] = result{ip: ip, err: err}
					return
				}

				resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					results[i] = result{ip: ip, blocked: true}
					return
				}
			}

			// 3rd request should be blocked for this client only.
			req := httptest.NewRequest(http.MethodGet, "/data", nil)
			req.Header.Set("X-Forwarded-For", ip)

			resp, err := app.Test(req, -1)
			if err != nil {
				results[i] = result{ip: ip, err: err}
				return
			}

			resp.Body.Close()
			results[i] = result{ip: ip, blocked: resp.StatusCode == http.StatusTooManyRequests}
		}()
	}

	wg.Wait()

	for _, r := range results {
		require.NoError(t, r.err, "client %s: unexpected request error", r.ip)
		assert.True(t, r.blocked, "client %s: 3rd request should have been blocked", r.ip)
	}
}

// TestServer_TenantIsolation verifies that IdentityFromIPAndHeader creates per-tenant
// buckets. The same IP with different X-Tenant-ID values gets independent counters.
func TestServer_TenantIsolation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newServerTestConn(t, mr)

	tier := ratelimit.Tier{Name: "tenant", Max: 2, Window: 60 * time.Second}
	rl := ratelimit.New(conn,
		ratelimit.WithIdentityFunc(ratelimit.IdentityFromIPAndHeader("X-Tenant-ID")),
	)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(rl.WithRateLimit(tier))
	app.Get("/resource", func(c *fiber.Ctx) error { return c.SendString("ok") })

	doTenantReq := func(tenantID string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/resource", nil)
		req.Header.Set("X-Tenant-ID", tenantID)

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		return resp
	}

	// Tenant A exhausts its quota (max = 2).
	for range 2 {
		resp := doTenantReq("tenant-a")
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	resp := doTenantReq("tenant-a")
	resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "tenant-a should be blocked")

	// Tenant B has its own counter — completely unaffected.
	resp = doTenantReq("tenant-b")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "tenant-b should not be affected")
	assert.Equal(t, "1", resp.Header.Get("X-RateLimit-Remaining"))
}

// TestServer_HeadersProgression verifies that X-RateLimit-Remaining decrements
// accurately across a full sequence of requests and that Retry-After is set on
// the blocking response with a ceiling-rounded value.
func TestServer_HeadersProgression(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newServerTestConn(t, mr)

	tier := ratelimit.Tier{Name: "progress", Max: 5, Window: 60 * time.Second}
	rl := ratelimit.New(conn)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Use(rl.WithRateLimit(tier))
	app.Get("/count", func(c *fiber.Ctx) error { return c.SendString("ok") })

	type snapshot struct{ limit, remaining string }

	expected := []snapshot{
		{"5", "4"},
		{"5", "3"},
		{"5", "2"},
		{"5", "1"},
		{"5", "0"},
	}

	for i, want := range expected {
		req := httptest.NewRequest(http.MethodGet, "/count", nil)
		req.Header.Set("X-Forwarded-For", "172.16.0.1")

		resp, err := app.Test(req, -1)
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode, "request %d", i+1)
		assert.Equal(t, want.limit, resp.Header.Get("X-RateLimit-Limit"), "request %d: limit", i+1)
		assert.Equal(t, want.remaining, resp.Header.Get("X-RateLimit-Remaining"), "request %d: remaining", i+1)
		assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"), "request %d: reset timestamp", i+1)
	}

	// 6th request — verify the blocking response headers.
	req := httptest.NewRequest(http.MethodGet, "/count", nil)
	req.Header.Set("X-Forwarded-For", "172.16.0.1")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "5", resp.Header.Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", resp.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, resp.Header.Get("X-RateLimit-Reset"))
	// Retry-After must be ≥ 1 (ceiling division guarantees this).
	retryAfter := resp.Header.Get("Retry-After")
	assert.NotEmpty(t, retryAfter)
	assert.NotEqual(t, "0", retryAfter, "Retry-After must be at least 1 second")
}

// TestServer_RetryAfter_CeilingDivision verifies that when the remaining TTL is
// sub-second (e.g. 100ms), the Retry-After header is 1, not 0.
//
// This exercises the ceiling division in handleLimitExceeded:
//
//	retryAfterSec := int(ttl / time.Second)   // = 0 for 100ms
//	if ttl%time.Second > 0 { retryAfterSec++ } // ceil → 1
func TestServer_RetryAfter_CeilingDivision(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newServerTestConn(t, mr)

	// 3-second window so FastForward can leave a sub-second TTL remainder.
	tier := ratelimit.Tier{Name: "ceiling", Max: 1, Window: 3 * time.Second}
	rl := ratelimit.New(conn)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ProxyHeader:           fiber.HeaderXForwardedFor,
	})
	app.Use(rl.WithRateLimit(tier))
	app.Get("/ping", func(c *fiber.Ctx) error { return c.SendString("pong") })

	doReq := func() *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set("X-Forwarded-For", "10.99.0.1")

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		return resp
	}

	// First request exhausts the quota and sets TTL = 3s.
	resp := doReq()
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Advance to ~100ms before expiry: TTL drops to sub-second.
	mr.FastForward(2900 * time.Millisecond)

	// Blocked response: TTL ≈ 100ms → ceiling division must yield 1, not 0.
	resp = doReq()
	defer resp.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"),
		"Retry-After should be 1 (ceiling of sub-second TTL), not 0")
}
