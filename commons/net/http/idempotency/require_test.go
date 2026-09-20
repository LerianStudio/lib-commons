//go:build unit

package idempotency

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	libHTTP "github.com/LerianStudio/lib-commons/v7/commons/net/http"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeErrorBody parses a libHTTP.RespondError envelope. The error CODE the
// middleware passes as `title` is asserted from the decoded field, never with a
// substring match on the raw body: the body also carries prose, so Contains
// would pass on a message that merely mentions the code while the machine-
// readable field says something else.
func decodeErrorBody(t *testing.T, resp *http.Response) libHTTP.ErrorResponse {
	t.Helper()

	defer resp.Body.Close()

	var got libHTTP.ErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	return got
}

// TestCheck_BypassesRemainReachableByDefault pins the two shipped bypasses: a
// mutating request with no X-Idempotency header, and a keyed one with no tenant
// in context, both reach the handler when neither option is set.
//
// This is the guard against a later refactor flipping either default silently.
// Both bypasses run a mutation with NO at-most-once protection, so they look
// like defects read in isolation; they are the documented opt-in contract every
// shipped caller depends on, and changing them is a breaking change, not a fix.
func TestCheck_BypassesRemainReachableByDefault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		tenantID string
		// key is the X-Idempotency header value; empty sends no header.
		key string
	}{
		{name: "no_idempotency_key", tenantID: "t-default", key: ""},
		{name: "no_tenant_context", tenantID: "", key: "bypass-tenant"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			conn := newRedisClient(t, miniredis.RunT(t))
			m := New(conn) // no WithRequireKey, no WithRequireTenant

			var called atomic.Bool

			resp := doPost(t, spyApp(m.Check(), testCase.tenantID, &called), testCase.key)
			resp.Body.Close()

			assert.Equal(t, http.StatusCreated, resp.StatusCode)
			assert.True(t, called.Load(), "shipped default must let the request through")
		})
	}
}

// TestCheck_RequireKey covers the missing-header branch under WithRequireKey.
func TestCheck_RequireKey(t *testing.T) {
	t.Parallel()

	const tenant = "t-require-key"

	t.Run("refuses_with_400_before_handler", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireKey())

		var called atomic.Bool

		resp := doPost(t, spyApp(m.Check(), tenant, &called), "")

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.False(t, called.Load(), "handler must NOT run without an idempotency key")

		body := decodeErrorBody(t, resp)
		assert.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", body.Title)
		assert.Equal(t, http.StatusBadRequest, body.Code)
		assert.Contains(t, body.Message, chttp.IdempotencyKey,
			"message must name the header the caller has to send")
	})

	t.Run("custom_handler_replaces_the_response", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireKey(), WithKeyRequiredHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusPreconditionRequired).SendString("bring a key")
		}))

		var called atomic.Bool

		resp := doPost(t, spyApp(m.Check(), tenant, &called), "")
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusPreconditionRequired, resp.StatusCode)
		assert.False(t, called.Load(), "custom handler must still refuse the mutation")
	})

	t.Run("keyed_request_flows_normally_and_replays", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireKey())

		var calls atomic.Int64

		app := countingApp(m.Check(), tenant, &calls)

		first := doPost(t, app, "rk-flows")
		first.Body.Close()

		assert.Equal(t, http.StatusCreated, first.StatusCode)
		assert.Empty(t, first.Header.Get(chttp.IdempotencyReplayed))

		second := doPost(t, app, "rk-flows")
		second.Body.Close()

		assert.Equal(t, http.StatusCreated, second.StatusCode)
		assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed),
			"retry must be answered from the stored record")
		assert.Equal(t, int64(1), calls.Load(), "handler must run exactly once")
	})
}

// TestCheck_RequireTenant covers the missing-tenant branch under
// WithRequireTenant. An opted-in caller refuses the request; it is never keyed
// onto a shared tenant-less namespace, which is what the bypass existed to
// avoid in the first place.
func TestCheck_RequireTenant(t *testing.T) {
	t.Parallel()

	t.Run("refuses_with_400_before_handler", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireTenant())

		var called atomic.Bool

		resp := doPost(t, spyApp(m.Check(), "", &called), "rt-refused")

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.False(t, called.Load(), "handler must NOT run without tenant context")

		body := decodeErrorBody(t, resp)
		assert.Equal(t, "IDEMPOTENCY_TENANT_REQUIRED", body.Title)
		assert.Equal(t, http.StatusBadRequest, body.Code)
	})

	t.Run("custom_handler_replaces_the_response", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireTenant(), WithTenantRequiredHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusForbidden).SendString("no tenant")
		}))

		var called atomic.Bool

		resp := doPost(t, spyApp(m.Check(), "", &called), "rt-custom")
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusForbidden, resp.StatusCode)
		assert.False(t, called.Load(), "custom handler must still refuse the mutation")
	})

	t.Run("tenant_present_flows_normally_and_replays", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireTenant())

		var calls atomic.Int64

		app := countingApp(m.Check(), "t-require-tenant", &calls)

		first := doPost(t, app, "rt-flows")
		first.Body.Close()

		assert.Equal(t, http.StatusCreated, first.StatusCode)

		second := doPost(t, app, "rt-flows")
		second.Body.Close()

		assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed))
		assert.Equal(t, int64(1), calls.Load(), "handler must run exactly once")
	})

	t.Run("unkeyed_request_is_untouched_without_require_key", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))
		m := New(conn, WithRequireTenant())

		var called atomic.Bool

		// No key, no tenant: the key bypass runs FIRST, so WithRequireTenant
		// alone never sees this request. The two options are independent.
		resp := doPost(t, spyApp(m.Check(), "", &called), "")
		resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.True(t, called.Load())
	})
}

// TestCheck_RequireBoth pins the two options composing on one middleware, which
// is how a money route mounts them.
func TestCheck_RequireBoth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		tenantID string
		key      string
		wantCode string
	}{
		{name: "missing_key_wins", tenantID: "t-both", key: "", wantCode: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "missing_tenant", tenantID: "", key: "both-keyed", wantCode: "IDEMPOTENCY_TENANT_REQUIRED"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			conn := newRedisClient(t, miniredis.RunT(t))
			m := New(conn, WithRequireKey(), WithRequireTenant())

			var called atomic.Bool

			resp := doPost(t, spyApp(m.Check(), testCase.tenantID, &called), testCase.key)

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.False(t, called.Load(), "handler must NOT run")
			assert.Equal(t, testCase.wantCode, decodeErrorBody(t, resp).Title)
		})
	}
}

// TestCheck_RequireKey_DoesNotTouchSafeMethods pins that the refusal is scoped
// to mutating methods. A GET carries no idempotency key by design and must not
// start failing when a route group opts in.
func TestCheck_RequireKey_DoesNotTouchSafeMethods(t *testing.T) {
	t.Parallel()

	conn := newRedisClient(t, miniredis.RunT(t))
	m := New(conn, WithRequireKey(), WithRequireTenant())

	app := newPostApp(m.Check(), tenantMiddleware(""))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
