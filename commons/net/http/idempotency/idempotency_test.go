//go:build unit

package idempotency

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons"
	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// TestMain configures package-wide env vars before any parallel tests run.
// miniredis is plaintext; allow the security gate to pass for the entire
// test binary so individual tests can call t.Parallel() safely.
func TestMain(m *testing.M) {
	if err := os.Setenv(commons.EnvAllowInsecureTLS, "true"); err != nil {
		panic("idempotency tests: cannot set ALLOW_INSECURE_TLS: " + err.Error())
	}

	os.Exit(m.Run())
}

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
	app := fiber.New()

	for _, pm := range preMiddleware {
		app.Use(pm)
	}

	app.Use(mw)

	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	// Also register GET and OPTIONS for pass-through tests.
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok-get")
	})

	app.Options("/test", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	return app
}

// tenantMiddleware returns a Fiber handler that injects tenantID into the
// request's user context via tmcore.ContextWithTenantID, mimicking real
// tenant-extraction middleware.
func tenantMiddleware(tenantID string) fiber.Handler {
	return func(c fiber.Ctx) error {
		ctx := tmcore.ContextWithTenantID(c.Context(), tenantID)
		c.SetContext(ctx)

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

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
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

func seedStoreRecord(t *testing.T, mr *miniredis.Miniredis, key string, record storeRecord) {
	t.Helper()

	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, mr.Set(key, string(data)))
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
	app := fiber.New()
	app.Use(handler)
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "some-key")

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
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

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
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

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
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
		WithRejectedHandler(func(c fiber.Ctx) error {
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

func TestCheck_MaxKeyLength_MeasuresUTF8Bytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        string
		wantStatus int
		wantDetail string
	}{
		{
			name:       "four bytes across two runes accepted",
			key:        "éé",
			wantStatus: http.StatusCreated,
		},
		{
			name:       "six bytes across three runes rejected",
			key:        "ééé",
			wantStatus: http.StatusBadRequest,
			wantDetail: "4 bytes",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			conn := newRedisClient(t, mr)
			middleware := New(conn, WithMaxKeyLength(4))
			app := newPostApp(middleware.Check(), tenantMiddleware("tenant-unicode"))

			response := doPost(t, app, testCase.key)
			body := readBody(t, response)

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			if testCase.wantDetail != "" {
				assert.Contains(t, body, testCase.wantDetail)
			}
		})
	}
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

	// Verify the completed record was cached in Redis.
	keys := mr.Keys()
	assert.Len(t, keys, 1, "expected one atomic idempotency record in Redis, got: %v", keys)
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

	seedStoreRecord(t, mr, lockKey, storeRecord{
		State:       keyStateProcessing,
		Fingerprint: requestFingerprint(http.MethodPost, "/test", nil),
		Owner:       "owner-processing",
	})
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
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-fail"))
	app.Use(m.Check())
	app.Post("/test", func(_ fiber.Ctx) error {
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
	require.Len(t, keys, 1, "expected one atomic idempotency record")

	// Verify the key format: prefix + tenantID + idempotency key.
	foundLock := false

	for _, k := range keys {
		if k == "idem:t1:my-key" {
			foundLock = true
		}
	}

	assert.True(t, foundLock, "lock key must match expected format, got: %v", keys)
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
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-conc"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
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
// Max body cache limit — oversized response fails closed
// ---------------------------------------------------------------------------

// TestCheck_WithMaxBodyCache verifies that an oversized response never creates
// a completion marker without an exact replay payload. The original call fails
// closed after the handler, and the retained processing record blocks retries.
func TestCheck_WithMaxBodyCache(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	// Allow only 10 bytes of body cache — our handler returns ~35 bytes.
	m := New(conn, WithMaxBodyCache(10))

	// Handler returns a body clearly larger than 10 bytes.
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-maxcache"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"result": "ok", "extra": "padding-to-exceed-limit"})
	})

	// First request — proceeds to the handler.
	req1 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req1.Header.Set(chttp.IdempotencyKey, "big-body-key")

	resp1, err := app.Test(req1, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	defer resp1.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp1.StatusCode)

	// Second request — same key. The processing marker remains so the mutation
	// cannot execute again without reconciliation.
	req2 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req2.Header.Set(chttp.IdempotencyKey, "big-body-key")

	resp2, err := app.Test(req2, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	body2 := readBody(t, resp2)

	assert.Equal(t, http.StatusConflict, resp2.StatusCode)
	assert.Contains(t, body2, "IDEMPOTENCY_CONFLICT")
	assert.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, retryAfterSeconds, resp2.Header.Get(fiber.HeaderRetryAfter))
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

	seedStoreRecord(t, mr, lockKey, storeRecord{
		State:       keyStateProcessing,
		Fingerprint: requestFingerprint(http.MethodPost, "/test", nil),
		Owner:       "owner-inflight",
	})
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

func TestCheck_CustomDuplicateRejectionHandlers_WriteProblemDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		option     Option
		prepare    func(t *testing.T, mr *miniredis.Miniredis, app *fiber.App)
		request    func(t *testing.T, app *fiber.App) *http.Response
		wantStatus int
		wantDetail string
	}{
		{
			name: "in-flight conflict",
			option: WithConflictHandler(func(c fiber.Ctx) error {
				return c.Status(http.StatusConflict).JSON(fiber.Map{
					"detail": "canonical in-flight conflict",
				})
			}),
			prepare: func(t *testing.T, mr *miniredis.Miniredis, _ *fiber.App) {
				t.Helper()
				seedStoreRecord(t, mr, "idempotency:tenant-custom:custom-key", storeRecord{
					State:       keyStateProcessing,
					Fingerprint: requestFingerprint(http.MethodPost, "/test", nil),
					Owner:       "owner-custom",
				})
			},
			request: func(t *testing.T, app *fiber.App) *http.Response {
				t.Helper()
				return doPost(t, app, "custom-key")
			},
			wantStatus: http.StatusConflict,
			wantDetail: "canonical in-flight conflict",
		},
		{
			name: "same key with different request",
			option: WithKeyReuseHandler(func(c fiber.Ctx) error {
				return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{
					"detail": "canonical key reuse",
				})
			}),
			prepare: func(t *testing.T, _ *miniredis.Miniredis, app *fiber.App) {
				t.Helper()
				first := doSend(t, app, http.MethodPost, "/test", `{"amount":10}`, "custom-key")
				require.Equal(t, http.StatusCreated, first.StatusCode)
				readBody(t, first)
			},
			request: func(t *testing.T, app *fiber.App) *http.Response {
				t.Helper()
				return doSend(t, app, http.MethodPost, "/test", `{"amount":11}`, "custom-key")
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantDetail: "canonical key reuse",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			conn := newRedisClient(t, mr)
			middleware := New(conn, testCase.option)
			app := newEchoApp(middleware.Check(), nil, tenantMiddleware("tenant-custom"))

			testCase.prepare(t, mr, app)
			response := testCase.request(t, app)
			body := readBody(t, response)

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Contains(t, body, testCase.wantDetail)
		})
	}
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

	app := fiber.New()
	app.Use(m.Check())
	app.Head("/test", func(c fiber.Ctx) error {
		handlerCalled.Store(true)
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodHead, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "head-key-should-be-ignored")

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
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

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-put"))
	app.Use(m.Check())
	app.Put("/resource", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"updated": true})
	})

	doPut := func(key string) *http.Response {
		t.Helper()

		req := httptest.NewRequest(http.MethodPut, "/resource", nil)
		if key != "" {
			req.Header.Set(chttp.IdempotencyKey, key)
		}

		resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
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

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-5xx"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		callCount.Add(1)
		// Handler writes 503 but returns nil — a common pattern.
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "temporarily unavailable"})
	})

	// First request — handler returns 503, nil error.
	req1 := httptest.NewRequest(http.MethodPost, "/test", nil)
	req1.Header.Set(chttp.IdempotencyKey, "retry-5xx-key")

	resp1, err := app.Test(req1, fiber.TestConfig{Timeout: 0})
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

	resp2, err := app.Test(req2, fiber.TestConfig{Timeout: 0})
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

// ---------------------------------------------------------------------------
// Payload fingerprint
// ---------------------------------------------------------------------------
//
// The middleware previously decided a duplicate purely by the idempotency key.
// A client reusing one key across two DIFFERENT payloads had the first
// request's response replayed to the second: the second operation never ran and
// the caller was told it succeeded. On a money path that is a phantom success --
// the ledger has no record, the caller's books say settled, and nobody retries
// because the status said 201.

// newEchoApp returns an app whose handler echoes the request body, so a replayed
// response is distinguishable from a freshly produced one.
func newEchoApp(mw fiber.Handler, calls *atomic.Int32, preMiddleware ...fiber.Handler) *fiber.App {
	app := fiber.New()

	for _, pm := range preMiddleware {
		app.Use(pm)
	}

	app.Use(mw)

	handler := func(c fiber.Ctx) error {
		if calls != nil {
			calls.Add(1)
		}

		// Echoed verbatim rather than JSON-wrapped, so an assertion on the body
		// reads the payload directly instead of an escaped copy of it.
		return c.Status(fiber.StatusCreated).SendString(string(c.Body()))
	}

	app.Post("/test", handler)
	app.Post("/other", handler)
	app.Put("/test", handler)

	return app
}

// doSend issues a request with an explicit method, path, body and idempotency key.
func doSend(t *testing.T, app *fiber.App, method, path, body, idempotencyKey string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if idempotencyKey != "" {
		req.Header.Set(chttp.IdempotencyKey, idempotencyKey)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	return resp
}

func TestCheck_SameKey_DifferentPayload_Rejected(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var calls atomic.Int32

	mw := New(conn, WithLogger(libLog.NewNop()))
	app := newEchoApp(mw.Check(), &calls, tenantMiddleware("tenant-a"))

	first := doSend(t, app, http.MethodPost, "/test", `{"amount":10}`, "reused-key")
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.Contains(t, readBody(t, first), `{"amount":10}`)

	second := doSend(t, app, http.MethodPost, "/test", `{"amount":10000}`, "reused-key")
	body := readBody(t, second)

	assert.Equal(t, http.StatusUnprocessableEntity, second.StatusCode,
		"a key reused with a different payload must be refused, not answered with the first payload's response")
	assert.NotContains(t, body, `"amount":10`,
		"the first request's response must not leak to a different payload")
	assert.Equal(t, int32(1), calls.Load(),
		"the handler must not run for the rejected request")
	assert.NotEqual(t, "true", second.Header.Get(chttp.IdempotencyReplayed),
		"a refusal is not a replay and must not be labelled one")
}

func TestCheck_SameKey_SamePayload_StillReplays(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var calls atomic.Int32

	mw := New(conn, WithLogger(libLog.NewNop()))
	app := newEchoApp(mw.Check(), &calls, tenantMiddleware("tenant-a"))

	first := doSend(t, app, http.MethodPost, "/test", `{"amount":10}`, "same-key")
	require.Equal(t, http.StatusCreated, first.StatusCode)

	second := doSend(t, app, http.MethodPost, "/test", `{"amount":10}`, "same-key")

	assert.Equal(t, http.StatusCreated, second.StatusCode)
	assert.Contains(t, readBody(t, second), `{"amount":10}`)
	assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, int32(1), calls.Load(), "a genuine retry must not re-run the handler")
}

func TestCheck_SameKey_DifferentTarget_Rejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		method, path string
	}{
		{"different path", http.MethodPost, "/other"},
		{"different method", http.MethodPut, "/test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			conn := newRedisClient(t, mr)

			mw := New(conn, WithLogger(libLog.NewNop()))
			app := newEchoApp(mw.Check(), nil, tenantMiddleware("tenant-a"))

			first := doSend(t, app, http.MethodPost, "/test", `{"amount":10}`, "cross-target")
			require.Equal(t, http.StatusCreated, first.StatusCode)
			readBody(t, first)

			second := doSend(t, app, tc.method, tc.path, `{"amount":10}`, "cross-target")
			readBody(t, second)

			assert.Equal(t, http.StatusUnprocessableEntity, second.StatusCode,
				"an identical body sent to a different operation is a different request")
		})
	}
}

func TestCheck_FirstRequest_StoresFingerprintWithState(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	mw := New(conn, WithLogger(libLog.NewNop()))
	app := newEchoApp(mw.Check(), nil, tenantMiddleware("tenant-a"))

	resp := doSend(t, app, http.MethodPost, "/test", `{"amount":10}`, "fp-key")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	readBody(t, resp)

	stored, err := mr.Get("idempotency:tenant-a:fp-key")
	require.NoError(t, err)

	var record storeRecord
	require.NoError(t, json.Unmarshal([]byte(stored), &record))
	assert.Equal(t, keyStateComplete, record.State)
	assert.NotEmpty(t, record.Fingerprint)
	assert.Equal(t, requestFingerprint(http.MethodPost, "/test", []byte(`{"amount":10}`)), record.Fingerprint,
		"the fingerprint written on completion must be the one the next request will compare against")
}

func TestCheck_SameKey_QueryVariance_StillReplays(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var calls atomic.Int32

	mw := New(conn, WithLogger(libLog.NewNop()))
	app := newEchoApp(mw.Check(), &calls, tenantMiddleware("tenant-a"))

	// The fingerprint deliberately excludes the query string: clients append
	// cache-busting parameters on retry, and a retry that differs only there is
	// still the same request. Pins that decision, so a change to full-URI
	// fingerprinting fails here instead of silently refusing legitimate retries.
	first := doSend(t, app, http.MethodPost, "/test?attempt=1", `{"amount":10}`, "query-key")
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.Equal(t, `{"amount":10}`, readBody(t, first))

	second := doSend(t, app, http.MethodPost, "/test?_=1764500000000", `{"amount":10}`, "query-key")

	assert.Equal(t, http.StatusCreated, second.StatusCode,
		"a retry differing only in the query string must replay, not be refused as reuse")
	assert.Equal(t, `{"amount":10}`, readBody(t, second))
	assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, int32(1), calls.Load())
}
