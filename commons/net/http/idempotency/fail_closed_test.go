//go:build unit

package idempotency

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyStoreFailure_FailsOpenOnlyWhenNonExecutionIsDemonstrable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		recordObserved bool
		want           storeFailureClass
	}{
		{
			name: "dial refused before command reaches Redis",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			want: storeFailureTransientBeforeObservation,
		},
		{name: "closed client cannot execute", err: redis.ErrClosed, want: storeFailureTransientBeforeObservation},
		{name: "EOF may follow command execution", err: io.EOF, want: storeFailureUnsafe},
		{name: "timeout may follow command execution", err: context.DeadlineExceeded, want: storeFailureUnsafe},
		{name: "invalid script response", err: errInvalidStoreResult, want: storeFailureUnsafe},
		{
			name:           "observed claim makes dial error unsafe",
			err:            &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			recordObserved: true,
			want:           storeFailureUnsafe,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, classifyStoreFailure(testCase.err, testCase.recordObserved))
		})
	}
}

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

// TestCheck_TransientRedisError distinguishes a connectivity failure before
// observation from WRONGTYPE failures that prove persisted state exists.
func TestCheck_TransientRedisError(t *testing.T) {
	t.Parallel()

	const tenant = "t-fc"

	cases := []struct {
		name                  string
		wantDefaultStatus     int
		wantDefaultHandlerRun bool
		// seed drives redis into the state that triggers one transient-error branch.
		seed func(t *testing.T, mr *miniredis.Miniredis, key, responseKey string)
	}{
		{
			// Closing a live server can produce EOF after a command was written,
			// so execution is ambiguous and must fail closed.
			name:              "redis_connection_lost_during_acquire",
			wantDefaultStatus: http.StatusServiceUnavailable,
			seed: func(_ *testing.T, mr *miniredis.Miniredis, _, _ string) {
				mr.Close()
			},
		},
		{
			// Branch 3: SET NX sees the key exists (returns false), but the
			// follow-up GET on the wrong-type key returns WRONGTYPE.
			name:              "duplicate_key_state_read_error",
			wantDefaultStatus: http.StatusServiceUnavailable,
			seed: func(t *testing.T, mr *miniredis.Miniredis, key, _ string) {
				_, err := mr.Lpush(key, "x")
				require.NoError(t, err)
			},
		},
		{
			// Branch 4: a valid v6.2 marker is recognized, but the GET on the
			// wrong-type legacy response key returns WRONGTYPE.
			name:              "duplicate_response_read_error",
			wantDefaultStatus: http.StatusServiceUnavailable,
			seed: func(t *testing.T, mr *miniredis.Miniredis, key, responseKey string) {
				fingerprint := requestFingerprint(http.MethodPost, "/test", nil)
				require.NoError(t, mr.Set(key, keyStateComplete+stateSeparator+fingerprint))
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

			t.Run("default_classifies_ambiguous_failures_closed", func(t *testing.T) {
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

				assert.Equal(t, tc.wantDefaultStatus, resp.StatusCode)
				assert.Equal(t, tc.wantDefaultHandlerRun, called.Load(),
					"only connectivity failure before persisted state is observed may run the handler")
			})
		})
	}
}

func TestOnStoreError_ClosedClientBeforeObservationFailsOpen(t *testing.T) {
	t.Parallel()

	middleware := newMiddleware()
	var handlerCalled atomic.Bool
	app := spyApp(func(c fiber.Ctx) error {
		return middleware.onStoreError(c, redis.ErrClosed, false)
	}, "tenant-safe-connectivity", &handlerCalled)

	response := doPost(t, app, "safe-connectivity")
	readBody(t, response)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	assert.True(t, handlerCalled.Load())
}
