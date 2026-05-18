//go:build unit

package idempotency

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v5/commons/constants"
	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newRedisClient creates a *libRedis.Client backed by a miniredis instance.
// The connection is closed automatically when the test finishes.
func newRedisClient(t *testing.T, mr *miniredis.Miniredis) *libRedis.Client {
	t.Helper()

	conn, err := libRedis.New(t.Context(), libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &libLog.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("redis close: %v", err)
		}
	})

	return conn
}

// newPostApp builds a Fiber app that routes POST /test through the given
// middleware, then calls a handler that writes 201 + JSON body.
// An optional pre-middleware is called before the idempotency middleware
// to let tests inject tenant context.
func newPostApp(mw fiber.Handler, preMiddleware ...fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	for _, pm := range preMiddleware {
		app.Use(pm)
	}

	app.Use(mw)

	app.Post("/test", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	// Also register GET and OPTIONS for pass-through tests.
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok-get")
	})

	app.Options("/test", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	return app
}

// tenantMiddleware returns a Fiber handler that injects tenantID into the
// request's user context via tmcore.ContextWithTenantID, mimicking real
// tenant-extraction middleware.
func tenantMiddleware(tenantID string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := tmcore.ContextWithTenantID(c.UserContext(), tenantID)
		c.SetUserContext(ctx)

		return c.Next()
	}
}

// doPost sends a POST /test with the given idempotency key header.
func doPost(t *testing.T, app *fiber.App, idempotencyKey string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	if idempotencyKey != "" {
		req.Header.Set(chttp.IdempotencyKey, idempotencyKey)
	}

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	return resp
}

// readBody reads and returns the full response body, closing it.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(b)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNew_NilConn(t *testing.T) {
	t.Parallel()

	m := New(nil)
	assert.Nil(t, m, "New(nil) must return nil middleware")
}

func TestCheck_NilMiddleware(t *testing.T) {
	t.Parallel()

	var m *Middleware // nil

	handler := m.Check()
	require.NotNil(t, handler, "Check() on nil receiver must return a handler")

	// The handler must be a pass-through.
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(handler)
	app.Post("/test", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "some-key")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode,
		"nil middleware must pass through to the actual handler")
}

func TestCheck_GET_PassesThrough(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := newPostApp(m.Check())

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "should-be-ignored")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	body := readBody(t, resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok-get", body)
}

func TestCheck_OPTIONS_PassesThrough(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := newPostApp(m.Check())

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "should-be-ignored")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestCheck_NoHeader_PassesThrough(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := newPostApp(m.Check())

	// POST without idempotency header — proceeds normally.
	resp := doPost(t, app, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestCheck_KeyTooLong_Rejected(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn, WithMaxKeyLength(10))

	app := newPostApp(m.Check())

	longKey := strings.Repeat("x", 11)
	resp := doPost(t, app, longKey)
	body := readBody(t, resp)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, body, "VALIDATION_ERROR")
	assert.Contains(t, body, chttp.IdempotencyKey)
}

func TestCheck_KeyTooLong_CustomHandler(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	m := New(conn,
		WithMaxKeyLength(5),
		WithRejectedHandler(func(c *fiber.Ctx) error {
			return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{
				"custom": "rejected",
			})
		}),
	)

	app := newPostApp(m.Check())

	longKey := strings.Repeat("k", 6)
	resp := doPost(t, app, longKey)
	body := readBody(t, resp)

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Contains(t, body, "rejected")
}

func TestCheck_FirstRequest_Proceeds(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := newPostApp(m.Check(), tenantMiddleware("tenant-1"))

	resp := doPost(t, app, "unique-key-1")
	body := readBody(t, resp)

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Contains(t, body, "created")

	// Verify the response was cached in Redis.
	keys := mr.Keys()
	// Expect two keys: the lock key and the :response key.
	assert.GreaterOrEqual(t, len(keys), 2,
		"expected lock key + response key in Redis, got: %v", keys)
}

func TestCheck_DuplicateRequest_ReplaysResponse(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := newPostApp(m.Check(), tenantMiddleware("tenant-dup"))

	// First request — proceeds normally.
	resp1 := doPost(t, app, "dup-key")
	body1 := readBody(t, resp1)
	assert.Equal(t, http.StatusCreated, resp1.StatusCode)
	assert.Contains(t, body1, "created")

	// Second request — same key — must replay.
	resp2 := doPost(t, app, "dup-key")
	body2 := readBody(t, resp2)

	assert.Equal(t, http.StatusCreated, resp2.StatusCode,
		"replayed response must have the original status code")
	assert.Contains(t, body2, "created",
		"replayed response must have the original body")
	assert.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed),
		"replayed response must set X-Idempotency-Replayed: true")
}

func TestCheck_DuplicateRequest_StillProcessing(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	// Simulate a first request that is "still processing" by setting the lock
	// key directly in Redis without a :response companion.
	tenantID := "tenant-proc"
	idempotencyKey := "processing-key"
	lockKey := "idempotency:" + tenantID + ":" + idempotencyKey

	require.NoError(t, mr.Set(lockKey, keyStateProcessing))
	mr.SetTTL(lockKey, 7*24*time.Hour)

	app := newPostApp(m.Check(), tenantMiddleware(tenantID))

	resp := doPost(t, app, idempotencyKey)
	body := readBody(t, resp)

	// The current production code (idempotency.go) returns 409 Conflict when the
	// key is in "processing" state — the request is still in-flight. The generic
	// 200 IDEMPOTENT response is only returned when the key is "complete" but the
	// response body was not cached (e.g., body exceeded maxBodyCache).
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, body, "IDEMPOTENCY_CONFLICT")
	assert.Equal(t, "true", resp.Header.Get(chttp.IdempotencyReplayed))
}

func TestCheck_FailedRequest_KeyDeleted(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	handlerErr := errors.New("handler boom")

	// Build a custom app whose handler returns an error.
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(tenantMiddleware("tenant-fail"))
	app.Use(m.Check())
	app.Post("/test", func(_ *fiber.Ctx) error {
		return handlerErr
	})

	resp := doPost(t, app, "fail-key")
	defer resp.Body.Close()

	// Fiber translates an unhandled error to 500.
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	// The lock key must have been deleted so the client can retry.
	keys := mr.Keys()
	assert.Empty(t, keys, "all keys must be deleted after handler failure, got: %v", keys)
}

func TestCheck_TenantIsolation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	sharedKey := "same-idem-key"

	// Tenant A — first request proceeds.
	appA := newPostApp(m.Check(), tenantMiddleware("tenant-A"))
	respA := doPost(t, appA, sharedKey)
	bodyA := readBody(t, respA)
	assert.Equal(t, http.StatusCreated, respA.StatusCode)
	assert.Contains(t, bodyA, "created")

	// Tenant B — same idempotency key, different tenant — must also proceed.
	appB := newPostApp(m.Check(), tenantMiddleware("tenant-B"))
	respB := doPost(t, appB, sharedKey)
	bodyB := readBody(t, respB)
	assert.Equal(t, http.StatusCreated, respB.StatusCode,
		"same key for a different tenant must proceed independently")
	assert.Contains(t, bodyB, "created")

	// Tenant A — duplicate of same key — must replay.
	respA2 := doPost(t, appA, sharedKey)
	assert.Equal(t, "true", respA2.Header.Get(chttp.IdempotencyReplayed),
		"same key + same tenant must replay")
	respA2.Body.Close()
}

func TestOptions_Defaults(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	require.NotNil(t, m)
	assert.Equal(t, "idempotency:", m.keyPrefix)
	assert.Equal(t, 7*24*time.Hour, m.keyTTL)
	assert.Equal(t, 256, m.maxKeyLength)
	assert.Equal(t, 500*time.Millisecond, m.redisTimeout)
	assert.Nil(t, m.onRejected, "default rejected handler should be nil (use built-in)")
}

// ---------------------------------------------------------------------------
// Option application tests
// ---------------------------------------------------------------------------

func TestOptions_Custom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    []Option
		checkFn func(t *testing.T, m *Middleware)
	}{
		{
			name: "WithKeyPrefix",
			opts: []Option{WithKeyPrefix("custom:")},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, "custom:", m.keyPrefix)
			},
		},
		{
			name: "WithKeyPrefix empty ignored",
			opts: []Option{WithKeyPrefix("")},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, "idempotency:", m.keyPrefix, "empty prefix must be ignored")
			},
		},
		{
			name: "WithKeyTTL",
			opts: []Option{WithKeyTTL(1 * time.Hour)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, 1*time.Hour, m.keyTTL)
			},
		},
		{
			name: "WithKeyTTL zero ignored",
			opts: []Option{WithKeyTTL(0)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, 7*24*time.Hour, m.keyTTL, "zero TTL must be ignored")
			},
		},
		{
			name: "WithMaxKeyLength",
			opts: []Option{WithMaxKeyLength(64)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, 64, m.maxKeyLength)
			},
		},
		{
			name: "WithMaxKeyLength zero ignored",
			opts: []Option{WithMaxKeyLength(0)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, 256, m.maxKeyLength, "zero maxKeyLength must be ignored")
			},
		},
		{
			name: "WithRedisTimeout",
			opts: []Option{WithRedisTimeout(2 * time.Second)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, 2*time.Second, m.redisTimeout)
			},
		},
		{
			name: "WithRedisTimeout zero ignored",
			opts: []Option{WithRedisTimeout(0)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.Equal(t, 500*time.Millisecond, m.redisTimeout, "zero timeout must be ignored")
			},
		},
		{
			name: "WithLogger nil ignored",
			opts: []Option{WithLogger(nil)},
			checkFn: func(t *testing.T, m *Middleware) {
				t.Helper()
				assert.NotNil(t, m.logger, "nil logger must keep the default nop logger")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			conn := newRedisClient(t, mr)
			m := New(conn, tt.opts...)
			require.NotNil(t, m)

			tt.checkFn(t, m)
		})
	}
}

// ---------------------------------------------------------------------------
// Redis failure — fail-open behavior
// ---------------------------------------------------------------------------

func TestCheck_RedisDown_FailsOpen(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := newPostApp(m.Check(), tenantMiddleware("tenant-failopen"))

	// Kill Redis before the request.
	mr.Close()

	resp := doPost(t, app, "key-while-redis-down")
	defer resp.Body.Close()

	// fail-open: handler proceeds despite Redis being unreachable.
	assert.Equal(t, http.StatusCreated, resp.StatusCode,
		"must fail open when Redis is unavailable")
}

// ---------------------------------------------------------------------------
// Verify that the response key uses correct prefix
// ---------------------------------------------------------------------------

func TestCheck_RedisKeyFormat(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn, WithKeyPrefix("idem:"))

	app := newPostApp(m.Check(), tenantMiddleware("t1"))

	resp := doPost(t, app, "my-key")
	resp.Body.Close()

	keys := mr.Keys()
	require.Len(t, keys, 2, "expected lock + response keys")

	// Verify the key format: prefix + tenantID + idempotency key.
	foundLock := false
	foundResp := false

	for _, k := range keys {
		if k == "idem:t1:my-key" {
			foundLock = true
		}

		if k == "idem:t1:my-key:response" {
			foundResp = true
		}
	}

	assert.True(t, foundLock, "lock key must match expected format, got: %v", keys)
	assert.True(t, foundResp, "response key must match expected format, got: %v", keys)
}

// ---------------------------------------------------------------------------
// Concurrent same-key requests
// ---------------------------------------------------------------------------

// TestCheck_ConcurrentSameKey launches 10 goroutines all POST-ing with the
// same idempotency key simultaneously. Exactly 1 should reach the upstream
// handler (get 201), while the rest receive either the cached 201 replay
// (Idempotency-Replayed: true) or a 409 IDEMPOTENCY_CONFLICT (in-flight).
func TestCheck_ConcurrentSameKey(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	// Build a Fiber app and start it on a real listener so many goroutines can
	// hit it concurrently — app.Test() serialises internally.
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(tenantMiddleware("tenant-conc"))
	app.Use(m.Check())
	app.Post("/test", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	ln, listenErr := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, listenErr)

	go func() { _ = app.Listener(ln) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	addr := ln.Addr().String()

	const goroutines = 10

	type result struct {
		status   int
		replayed string
		body     string
	}

	results := make([]result, goroutines)

	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/test", nil)
			if err != nil {
				return
			}

			req.Header.Set(chttp.IdempotencyKey, "shared-concurrent-key")

			resp, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				return
			}

			defer resp.Body.Close()

			b, _ := io.ReadAll(resp.Body)
			results[idx] = result{
				status:   resp.StatusCode,
				replayed: resp.Header.Get(chttp.IdempotencyReplayed),
				body:     string(b),
			}
		}(i)
	}

	wg.Wait()

	// Count how many got the original 201 without the replayed header.
	// The rest must be 201 replays (Idempotency-Replayed: true) or 409 in-flight.
	originals := 0

	for _, r := range results {
		if r.status == http.StatusCreated && r.replayed == "" {
			originals++
		} else {
			// Must be either a replay (201+replayed header) or in-flight (409).
			ok := (r.status == http.StatusCreated && r.replayed == "true") ||
				r.status == http.StatusConflict
			assert.True(t, ok,
				"expected 201-replay or 409, got status=%d replayed=%q body=%s",
				r.status, r.replayed, r.body)
		}
	}

	assert.Equal(t, 1, originals,
		"exactly one goroutine must receive the original 201 from the handler")
}

// ---------------------------------------------------------------------------
// Max body cache limit — oversized response falls back to IDEMPOTENT reply
// ---------------------------------------------------------------------------

// TestCheck_WithMaxBodyCache verifies that when a response body exceeds the
// configured maxBodyCache, the first request still succeeds (201) but a
// duplicate request receives the generic "IDEMPOTENT" 200 response (the body
// was too large to cache).
func TestCheck_WithMaxBodyCache(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	// Allow only 10 bytes of body cache — our handler returns ~35 bytes.
	m := New(conn, WithMaxBodyCache(10))

	// Handler returns a body clearly larger than 10 bytes.
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(tenantMiddleware("tenant-maxcache"))
	app.Use(m.Check())
	app.Post("/test", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"result": "ok", "extra": "padding-to-exceed-limit"})
	})

	// First request — proceeds to the handler.
	req1 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req1.Header.Set(chttp.IdempotencyKey, "big-body-key")

	resp1, err := app.Test(req1, -1)
	require.NoError(t, err)

	defer resp1.Body.Close()

	assert.Equal(t, http.StatusCreated, resp1.StatusCode, "first request must reach the handler")

	// Second request — same key. Body was not cached (too large), so must get
	// the generic IDEMPOTENT fallback, not the original 201 body.
	req2 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req2.Header.Set(chttp.IdempotencyKey, "big-body-key")

	resp2, err := app.Test(req2, -1)
	require.NoError(t, err)

	body2 := readBody(t, resp2)

	assert.Equal(t, http.StatusOK, resp2.StatusCode,
		"duplicate request with uncached body must get the generic 200 IDEMPOTENT response")
	assert.Contains(t, body2, "IDEMPOTENT")
	assert.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
}

// ---------------------------------------------------------------------------
// In-flight detection — 409 Conflict
// ---------------------------------------------------------------------------

// TestCheck_InFlight_Returns409 verifies that while a request is being processed
// (key is in "processing" state with no cached response), a duplicate request
// receives 409 IDEMPOTENCY_CONFLICT. Then the first request completes normally.
func TestCheck_InFlight_Returns409(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	// Pre-set the Redis key to "processing" without a response key — this
	// simulates a first request that is currently in-flight.
	tenantID := "tenant-inflight"
	idempotencyKey := "inflight-key"
	lockKey := "idempotency:" + tenantID + ":" + idempotencyKey

	require.NoError(t, mr.Set(lockKey, keyStateProcessing))
	mr.SetTTL(lockKey, 7*24*time.Hour)

	// Build an app and send a duplicate — no response key exists.
	app := newPostApp(m.Check(), tenantMiddleware(tenantID))

	resp := doPost(t, app, idempotencyKey)
	body := readBody(t, resp)

	assert.Equal(t, http.StatusConflict, resp.StatusCode,
		"duplicate of an in-flight request must return 409 Conflict")
	assert.Contains(t, body, "IDEMPOTENCY_CONFLICT",
		"response body must contain IDEMPOTENCY_CONFLICT code")
	assert.Equal(t, "true", resp.Header.Get(chttp.IdempotencyReplayed))
}

// ---------------------------------------------------------------------------
// HEAD request passthrough
// ---------------------------------------------------------------------------

// TestCheck_HeadRequest_PassesThrough verifies that HEAD requests bypass the
// idempotency middleware even when an Idempotency-Key header is present.
func TestCheck_HeadRequest_PassesThrough(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var handlerCalled atomic.Bool

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(m.Check())
	app.Head("/test", func(c *fiber.Ctx) error {
		handlerCalled.Store(true)
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodHead, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "head-key-should-be-ignored")

	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"HEAD request must reach the handler directly")
	assert.True(t, handlerCalled.Load(),
		"handler must be called for HEAD requests (idempotency middleware is bypassed)")

	// The idempotency middleware must not have set the replayed header.
	assert.Empty(t, resp.Header.Get(chttp.IdempotencyReplayed),
		"HEAD pass-through must not set the Idempotency-Replayed header")

	// Verify that no Redis keys were written — HEAD bypasses the whole flow.
	assert.Empty(t, mr.Keys(), "HEAD pass-through must not write any Redis keys")
}

// ---------------------------------------------------------------------------
// PUT method enforcement — idempotency applies to all mutating methods
// ---------------------------------------------------------------------------

// TestCheck_PUTRequest_Enforced verifies that PUT requests are subject to
// idempotency enforcement: the first PUT proceeds to the handler and the
// second PUT with the same key replays the cached response.
func TestCheck_PUTRequest_Enforced(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(tenantMiddleware("tenant-put"))
	app.Use(m.Check())
	app.Put("/resource", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"updated": true})
	})

	doPut := func(key string) *http.Response {
		t.Helper()

		req := httptest.NewRequest(http.MethodPut, "/resource", nil)
		if key != "" {
			req.Header.Set(chttp.IdempotencyKey, key)
		}

		resp, err := app.Test(req, -1)
		require.NoError(t, err)

		return resp
	}

	// First PUT — should reach the handler.
	resp1 := doPut("put-key-1")
	body1 := readBody(t, resp1)

	assert.Equal(t, http.StatusOK, resp1.StatusCode)
	assert.Contains(t, body1, "updated")
	assert.Empty(t, resp1.Header.Get(chttp.IdempotencyReplayed),
		"first request must not be marked as replayed")

	// Second PUT — same key — must replay the cached response.
	resp2 := doPut("put-key-1")
	body2 := readBody(t, resp2)

	assert.Equal(t, http.StatusOK, resp2.StatusCode,
		"replayed response must have the original status code")
	assert.Contains(t, body2, "updated",
		"replayed response must have the original body")
	assert.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed),
		"replayed response must set Idempotency-Replayed: true")
}

// ---------------------------------------------------------------------------
// Missing tenant context — fail-open bypass
// ---------------------------------------------------------------------------

// TestCheck_NoTenantContext_BypassesIdempotency verifies that when the tenant
// context is missing (empty string), the middleware bypasses idempotency
// enforcement entirely (fail-open) to avoid collapsing all tenant-less
// requests onto a shared key that breaks tenant isolation.
func TestCheck_NoTenantContext_BypassesIdempotency(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	// No tenantMiddleware — tenantID will be "".
	app := newPostApp(m.Check())

	resp := doPost(t, app, "key-without-tenant")
	body := readBody(t, resp)

	assert.Equal(t, http.StatusCreated, resp.StatusCode,
		"missing tenant context must bypass idempotency (fail-open)")
	assert.Contains(t, body, "created",
		"handler must be called normally when tenant context is missing")

	// No Redis keys should be written since the middleware was bypassed.
	assert.Empty(t, mr.Keys(),
		"no Redis keys should be created when tenant context is missing")
}

// ---------------------------------------------------------------------------
// 5xx response — keys deleted for retryability
// ---------------------------------------------------------------------------

// TestCheck_5xxResponse_KeysDeleted verifies that when a handler writes a 5xx
// status code and returns nil (a common Fiber pattern), the middleware does NOT
// cache the response. Instead, it deletes the idempotency keys so the client
// can retry the same idempotency key.
func TestCheck_5xxResponse_KeysDeleted(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	callCount := atomic.Int32{}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(tenantMiddleware("tenant-5xx"))
	app.Use(m.Check())
	app.Post("/test", func(c *fiber.Ctx) error {
		callCount.Add(1)
		// Handler writes 503 but returns nil — a common pattern.
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "temporarily unavailable"})
	})

	// First request — handler returns 503, nil error.
	req1 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req1.Header.Set(chttp.IdempotencyKey, "retry-5xx-key")

	resp1, err := app.Test(req1, -1)
	require.NoError(t, err)
	defer resp1.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp1.StatusCode,
		"first request must return the 503 from the handler")
	assert.Equal(t, int32(1), callCount.Load())

	// Keys must have been deleted — 5xx should not be cached.
	assert.Empty(t, mr.Keys(),
		"5xx response must not be cached; keys should be deleted for retry")

	// Second request — same key — must reach the handler again (not replayed).
	req2 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req2.Header.Set(chttp.IdempotencyKey, "retry-5xx-key")

	resp2, err := app.Test(req2, -1)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp2.StatusCode,
		"second request must also reach the handler (5xx was not cached)")
	assert.Equal(t, int32(2), callCount.Load(),
		"handler must be called twice — 5xx responses are not cached")
	assert.Empty(t, resp2.Header.Get(chttp.IdempotencyReplayed),
		"second request must not be marked as replayed")
}

// ---------------------------------------------------------------------------
// Negative option values — defaults must be preserved
// ---------------------------------------------------------------------------

// TestOptions_NegativeValues verifies that negative values passed to option
// constructors are treated as invalid and the configured defaults are preserved.
func TestOptions_NegativeValues(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	m := New(conn,
		WithMaxKeyLength(-1),
		WithKeyTTL(-1*time.Hour),
		WithRedisTimeout(-1*time.Second),
	)

	require.NotNil(t, m)

	assert.Equal(t, 256, m.maxKeyLength,
		"negative maxKeyLength must be ignored; default (256) must be preserved")
	assert.Equal(t, 7*24*time.Hour, m.keyTTL,
		"negative keyTTL must be ignored; default (7 days) must be preserved")
	assert.Equal(t, 500*time.Millisecond, m.redisTimeout,
		"negative redisTimeout must be ignored; default (500ms) must be preserved")
}
