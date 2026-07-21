//go:build unit

package idempotency

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spyApp builds a POST /test app whose handler records whether it ran and
// returns 201. Tenant context is injected ahead of the idempotency middleware.
func spyApp(mw fiber.Handler, tenantID string, called *atomic.Bool) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		called.Store(true)
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	return app
}

// TestCheck_TransientRedisError covers all four fail-open branches: the
// pre-duplicate GetClient/SetNX errors (redis down) and the two
// duplicate/replay read errors (wrong-type key forces a WRONGTYPE GET while
// SET NX still reports the key exists). With WithFailClosed(true) each returns
// 503 without running the handler; with the default each falls through to the
// handler (unchanged behavior).
func TestCheck_TransientRedisError(t *testing.T) {
	t.Parallel()

	const tenant = "t-fc"

	cases := []struct {
		name string
		// seed drives redis into the state that triggers one transient-error branch.
		seed func(t *testing.T, mr *miniredis.Miniredis, key, responseKey string)
	}{
		{
			// Branches 1 & 2: GetClient / SetNX fail because Redis is down.
			name: "redis_down_before_setnx",
			seed: func(_ *testing.T, mr *miniredis.Miniredis, _, _ string) {
				mr.Close()
			},
		},
		{
			// Branch 3: SET NX sees the key exists (returns false), but the
			// follow-up GET on the wrong-type key returns WRONGTYPE.
			name: "duplicate_key_state_read_error",
			seed: func(t *testing.T, mr *miniredis.Miniredis, key, _ string) {
				_, err := mr.Lpush(key, "x")
				require.NoError(t, err)
			},
		},
		{
			// Branch 4: key-state GET succeeds ("complete"), but the GET on the
			// wrong-type response key returns WRONGTYPE.
			name: "duplicate_response_read_error",
			seed: func(t *testing.T, mr *miniredis.Miniredis, key, responseKey string) {
				require.NoError(t, mr.Set(key, keyStateComplete))
				_, err := mr.Lpush(responseKey, "x")
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("fail_closed_returns_503", func(t *testing.T) {
				t.Parallel()

				mr := miniredis.RunT(t)
				conn := newRedisClient(t, mr)
				m := New(conn, WithFailClosed(true))

				idemKey := "fc-" + tc.name
				key := fmt.Sprintf("idempotency:%s:%s", tenant, idemKey)
				tc.seed(t, mr, key, key+":response")

				var called atomic.Bool

				resp := doPost(t, spyApp(m.Check(), tenant, &called), idemKey)
				resp.Body.Close()

				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
				assert.False(t, called.Load(), "handler must NOT run when failing closed")
			})

			t.Run("default_fails_open", func(t *testing.T) {
				t.Parallel()

				mr := miniredis.RunT(t)
				conn := newRedisClient(t, mr)
				m := New(conn) // default: fail open

				idemKey := "fo-" + tc.name
				key := fmt.Sprintf("idempotency:%s:%s", tenant, idemKey)
				tc.seed(t, mr, key, key+":response")

				var called atomic.Bool

				resp := doPost(t, spyApp(m.Check(), tenant, &called), idemKey)
				resp.Body.Close()

				assert.Equal(t, http.StatusCreated, resp.StatusCode)
				assert.True(t, called.Load(), "handler must run when failing open")
			})
		})
	}
}
