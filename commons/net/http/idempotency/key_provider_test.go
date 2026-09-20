//go:build unit

package idempotency

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyEchoApp routes POST /test through mw and answers 201 with the
// X-Idempotency header value the HANDLER sees, so a test can assert what
// survived the middleware rather than what was sent.
func keyEchoApp(mw fiber.Handler, tenantID string, called *atomic.Bool) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		called.Store(true)

		return c.Status(fiber.StatusCreated).SendString(c.Get(chttp.IdempotencyKey))
	})

	return app
}

// TestKeyProvider_Default_ReadsHeader pins the shipped behaviour: with no
// provider configured the storage key is built from the X-Idempotency header,
// byte for byte.
func TestKeyProvider_Default_ReadsHeader(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var called atomic.Bool

	m := New(conn, WithKeyPrefix("idem:"))

	resp := doPost(t, keyEchoApp(m.Check(), "t1", &called), "header-key")
	resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.True(t, called.Load())
	assert.Equal(t, []string{"idem:t1:header-key"}, mr.Keys())
}

// TestKeyProvider_SuppliesStoreKey proves the provider's value — not the
// header's — is what the middleware stores under, and that the header reaching
// the handler is still the caller's own.
//
// The second half is the whole point of the option. The only way to scope a key
// before it existed was to overwrite the published header, which forced any
// route binding the raw key (an upstream correlation id, a persisted column) to
// stash and restore it around the middleware. The middleware must never write
// that header.
func TestKeyProvider_SuppliesStoreKey(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var called atomic.Bool

	m := New(conn,
		WithKeyPrefix("idem:"),
		WithKeyProvider(func(c fiber.Ctx) (string, error) {
			return "principal-alice:" + c.Get(chttp.IdempotencyKey), nil
		}),
	)

	got := doPost(t, keyEchoApp(m.Check(), "t1", &called), "caller-key")

	require.Equal(t, http.StatusCreated, got.StatusCode)
	assert.True(t, called.Load())
	assert.Equal(t, []string{"idem:t1:principal-alice:caller-key"}, mr.Keys(),
		"the store key must come from the provider, not the header")
	assert.Equal(t, "caller-key", readBody(t, got),
		"the middleware must leave the request header exactly as the caller sent it")
}

// TestKeyProvider_Error_RefusesBeforeHandler covers the provider failure. It
// takes the pre-handler refusal a WithTTLProvider error already takes — 503
// IDEMPOTENCY_UNAVAILABLE, routed through WithUnavailableHandler when set —
// because nothing has run and the caller must be told to retry, not to
// reconcile.
func TestKeyProvider_Error_RefusesBeforeHandler(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var called atomic.Bool

	m := New(conn, WithKeyProvider(func(fiber.Ctx) (string, error) {
		return "", errors.New("no principal on this request")
	}))

	resp := doPost(t, keyEchoApp(m.Check(), "t1", &called), "caller-key")

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "IDEMPOTENCY_UNAVAILABLE", decodeErrorBody(t, resp).Title)
	assert.False(t, called.Load(), "the handler must not run on a provider error")
	assert.Empty(t, mr.Keys(), "a provider error must write nothing")
}

// TestKeyProvider_Error_RoutesThroughUnavailableHandler pins that the provider
// failure honours the caller's own refusal document rather than inventing one.
func TestKeyProvider_Error_RoutesThroughUnavailableHandler(t *testing.T) {
	t.Parallel()

	conn := newRedisClient(t, miniredis.RunT(t))

	var called atomic.Bool

	m := New(conn,
		WithKeyProvider(func(fiber.Ctx) (string, error) {
			return "", errors.New("boom")
		}),
		WithUnavailableHandler(func(c fiber.Ctx) error {
			return c.SendStatus(fiber.StatusTeapot)
		}),
	)

	resp := doPost(t, keyEchoApp(m.Check(), "t1", &called), "caller-key")
	resp.Body.Close()

	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
	assert.False(t, called.Load())
}

// TestKeyProvider_EmptyValue_TakesUnkeyedBranch pins that an empty provider
// return is the same statement as a missing header: pass through by default,
// refuse under WithRequireKey. A request the provider cannot key is not a
// request with a blank key.
func TestKeyProvider_EmptyValue_TakesUnkeyedBranch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		requireKey bool
		wantStatus int
		wantCalled bool
	}{
		{name: "default_passes_through", requireKey: false, wantStatus: http.StatusCreated, wantCalled: true},
		{name: "require_key_refuses", requireKey: true, wantStatus: http.StatusBadRequest, wantCalled: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			conn := newRedisClient(t, mr)

			opts := []Option{WithKeyProvider(func(fiber.Ctx) (string, error) {
				return "", nil
			})}
			if testCase.requireKey {
				opts = append(opts, WithRequireKey())
			}

			var called atomic.Bool

			resp := doPost(t, keyEchoApp(New(conn, opts...).Check(), "t1", &called), "caller-key")
			resp.Body.Close()

			assert.Equal(t, testCase.wantStatus, resp.StatusCode)
			assert.Equal(t, testCase.wantCalled, called.Load())
			assert.Empty(t, mr.Keys(), "an unkeyed request must write nothing")
		})
	}
}

// TestKeyProvider_RespectsMaxKeyLength pins that the length cap follows the key
// wherever it comes from. The cap bounds the STORAGE key, so a provider that
// derives an oversized value is refused exactly as an oversized header is.
func TestKeyProvider_RespectsMaxKeyLength(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var called atomic.Bool

	m := New(conn,
		WithMaxKeyLength(8),
		WithKeyProvider(func(fiber.Ctx) (string, error) {
			return "0123456789", nil
		}),
	)

	resp := doPost(t, keyEchoApp(m.Check(), "t1", &called), "ok")

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "VALIDATION_ERROR", decodeErrorBody(t, resp).Title)
	assert.False(t, called.Load())
	assert.Empty(t, mr.Keys())
}

// TestWithKeyProvider_NilIsIgnored matches every other option in the package: a
// nil argument leaves the shipped behaviour in place rather than installing a
// provider that would panic on the first mutating request.
func TestWithKeyProvider_NilIsIgnored(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	var called atomic.Bool

	m := New(conn, WithKeyPrefix("idem:"), WithKeyProvider(nil))

	resp := doPost(t, keyEchoApp(m.Check(), "t1", &called), "header-key")
	resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, []string{"idem:t1:header-key"}, mr.Keys())
}
