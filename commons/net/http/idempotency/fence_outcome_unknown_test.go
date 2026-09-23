//go:build unit

package idempotency

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fenceTenant  = "tenant-bridge"
	fenceHeader  = "X-Test-Fence"
	fenceStoreKy = "idempotency:" + fenceTenant + ":bridge-key"
)

var errTestFingerprint = errors.New("fingerprint provider failed")

// bridgeApp mounts, above Check, a consumer that — when the request carries the
// test header — decides the key's outcome is unknowable, fences it through
// FenceOutcomeUnknown and answers 409 itself, exactly as a cutover bridge does.
// fenceErr receives what the seam returned. Every other request reaches Check.
func bridgeApp(mw *Middleware, fenceTTL time.Duration, fenceErr *error, calls *atomic.Int64) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(fenceTenant))
	app.Use(func(c fiber.Ctx) error {
		if c.Get(fenceHeader) == "" {
			return c.Next()
		}

		*fenceErr = mw.FenceOutcomeUnknown(c, fenceTTL)

		return c.SendStatus(fiber.StatusConflict)
	})
	app.Use(mw.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(fiber.StatusCreated).SendString("executed")
	})

	return app
}

func sendBridge(t *testing.T, app *fiber.App, body string, fence bool) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set(chttp.IdempotencyKey, "bridge-key")

	if fence {
		req.Header.Set(fenceHeader, "1")
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	return resp
}

// The defect BUG-36 names: a consumer fence must bind the byte-identical resend
// to the library's own outcome-unknown refusal, and a changed payload to key
// reuse, with the handler never running.
func TestFenceOutcomeUnknown_BindsTheResendToTheOutcomeUnknownRefusal(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)
	mw := NewWithStore(store, WithKeyTTL(fenceRetention))

	var (
		calls    atomic.Int64
		fenceErr error
	)

	app := bridgeApp(mw, fenceRetention, &fenceErr, &calls)

	bridged := sendBridge(t, app, `{"amount":100}`, true)
	bridged.Body.Close()
	require.Equal(t, http.StatusConflict, bridged.StatusCode)
	require.NoError(t, fenceErr)

	resend := sendBridge(t, app, `{"amount":100}`, false)
	resendBody := readBody(t, resend)

	assert.Equal(t, http.StatusUnprocessableEntity, resend.StatusCode)
	assert.Contains(t, resendBody, RefusalCodeOutcomeUnrecorded,
		"the byte-identical resend must read the outcome-unknown refusal, not key reuse")

	changed := sendBridge(t, app, `{"amount":999}`, false)
	changedBody := readBody(t, changed)

	assert.Equal(t, http.StatusUnprocessableEntity, changed.StatusCode)
	assert.Contains(t, changedBody, "IDEMPOTENCY_KEY_REUSE",
		"a different payload under the fenced key is still key reuse")

	assert.Equal(t, int64(0), calls.Load(), "a fenced key must never execute the handler")
}

func TestFenceOutcomeUnknown_HoldsForTheGivenTTLThenLapses(t *testing.T) {
	t.Parallel()

	store, mr := realRedisStore(t)
	mw := NewWithStore(store, WithKeyTTL(fenceRetention))

	var (
		calls    atomic.Int64
		fenceErr error
	)

	const fenceTTL = 10 * time.Minute

	app := bridgeApp(mw, fenceTTL, &fenceErr, &calls)

	sendBridge(t, app, `{}`, true).Body.Close()
	require.NoError(t, fenceErr)

	assert.Equal(t, fenceTTL, mr.TTL(fenceStoreKy))

	mr.FastForward(fenceTTL + time.Second)

	resend := sendBridge(t, app, `{}`, false)
	resend.Body.Close()

	assert.Equal(t, http.StatusCreated, resend.StatusCode, "past its TTL the fence is gone")
	assert.Equal(t, int64(1), calls.Load())
}

func TestFenceOutcomeUnknown_IsIdempotentForTheSameRequest(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)
	mw := NewWithStore(store, WithKeyTTL(fenceRetention))

	var (
		calls    atomic.Int64
		fenceErr error
	)

	app := bridgeApp(mw, fenceRetention, &fenceErr, &calls)

	sendBridge(t, app, `{}`, true).Body.Close()
	require.NoError(t, fenceErr)

	sendBridge(t, app, `{}`, true).Body.Close()
	assert.NoError(t, fenceErr, "re-fencing the same request is a no-op, not a failure")

	sendBridge(t, app, `{"other":1}`, true).Body.Close()
	assert.ErrorIs(t, fenceErr, ErrFenceKeyHeld,
		"a fence for a different request must not replace the first one's fingerprint")
}

// A completed record is a KNOWN outcome with a receipt. Overwriting it would
// turn a replayable answer into "unknown" and destroy the receipt, so the seam
// refuses and the resend still replays.
func TestFenceOutcomeUnknown_RefusesToOverwriteACompletedRecord(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)
	mw := NewWithStore(store, WithKeyTTL(fenceRetention))

	var (
		calls    atomic.Int64
		fenceErr error
	)

	app := bridgeApp(mw, fenceRetention, &fenceErr, &calls)

	first := sendBridge(t, app, `{}`, false)
	first.Body.Close()
	require.Equal(t, http.StatusCreated, first.StatusCode)

	sendBridge(t, app, `{}`, true).Body.Close()
	assert.ErrorIs(t, fenceErr, ErrFenceKeyHeld)

	replay := sendBridge(t, app, `{}`, false)

	assert.Equal(t, http.StatusCreated, replay.StatusCode)
	assert.Equal(t, "executed", readBody(t, replay))
	assert.Equal(t, "true", replay.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, int64(1), calls.Load())
}

func TestFenceOutcomeUnknown_RefusesWhatHandleWouldNotRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mw     func(t *testing.T) *Middleware
		tenant string
		key    string
		ttl    time.Duration
		want   string
	}{
		{
			name:   "missing key",
			mw:     func(t *testing.T) *Middleware { s, _ := realRedisStore(t); return NewWithStore(s) },
			tenant: fenceTenant, ttl: time.Hour, want: "key",
		},
		{
			name: "missing tenant",
			mw:   func(t *testing.T) *Middleware { s, _ := realRedisStore(t); return NewWithStore(s) },
			key:  "k", ttl: time.Hour, want: "tenant",
		},
		{
			name:   "non-positive ttl",
			mw:     func(t *testing.T) *Middleware { s, _ := realRedisStore(t); return NewWithStore(s) },
			tenant: fenceTenant, key: "k", want: "TTL",
		},
		{
			name: "key over the length bound",
			mw: func(t *testing.T) *Middleware {
				s, _ := realRedisStore(t)
				return NewWithStore(s, WithMaxKeyLength(4))
			},
			tenant: fenceTenant, key: "too-long", ttl: time.Hour, want: "exceeds",
		},
		{
			name:   "nil store",
			mw:     func(*testing.T) *Middleware { return NewWithStore(nil) },
			tenant: fenceTenant, key: "k", ttl: time.Hour, want: "store",
		},
		{
			name: "fingerprint provider failure",
			mw: func(t *testing.T) *Middleware {
				s, _ := realRedisStore(t)
				return NewWithStore(s, WithFingerprintProvider(func(fiber.Ctx) ([]byte, error) {
					return nil, errTestFingerprint
				}))
			},
			tenant: fenceTenant, key: "k", ttl: time.Hour, want: errTestFingerprint.Error(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mw := tc.mw(t)

			var got error

			app := fiber.New()
			if tc.tenant != "" {
				app.Use(tenantMiddleware(tc.tenant))
			}

			app.Post("/test", func(c fiber.Ctx) error {
				got = mw.FenceOutcomeUnknown(c, tc.ttl)

				return c.SendStatus(fiber.StatusConflict)
			})

			req := httptest.NewRequest(http.MethodPost, "/test", nil)
			if tc.key != "" {
				req.Header.Set(chttp.IdempotencyKey, tc.key)
			}

			resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			require.NoError(t, err)
			resp.Body.Close()

			require.Error(t, got)
			assert.Contains(t, got.Error(), tc.want)
		})
	}
}

func TestFenceOutcomeUnknown_NilReceiver(t *testing.T) {
	t.Parallel()

	var mw *Middleware

	app := fiber.New()

	var got error

	app.Post("/test", func(c fiber.Ctx) error {
		got = mw.FenceOutcomeUnknown(c, time.Hour)

		return nil
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/test", nil), fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)
	resp.Body.Close()

	assert.Error(t, got)
}

// With a TenantProvider the fence must land where Check reads — the provider's
// tenant — not at the tenant-manager context's, or the resend finds its key free.
func TestFenceOutcomeUnknown_RootsTheFenceAtTheProviderTenant(t *testing.T) {
	t.Parallel()

	store, mr := realRedisStore(t)
	mw := NewWithStore(store,
		WithKeyTTL(fenceRetention),
		WithTenantProvider(func(fiber.Ctx) (string, error) { return "tenant-from-provider", nil }),
	)

	var calls atomic.Int64

	var fenceErr error

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-from-tmcore"))
	app.Use(func(c fiber.Ctx) error {
		if c.Get(fenceHeader) == "" {
			return c.Next()
		}

		fenceErr = mw.FenceOutcomeUnknown(c, fenceRetention)

		return c.SendStatus(fiber.StatusConflict)
	})
	app.Use(mw.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.SendStatus(fiber.StatusCreated)
	})

	sendBridge(t, app, `{}`, true).Body.Close()
	require.NoError(t, fenceErr)

	assert.Equal(t, []string{"idempotency:tenant-from-provider:bridge-key"}, mr.Keys(),
		"exactly one fence, rooted at the provider tenant and none at the tmcore one")

	resend := sendBridge(t, app, `{}`, false)

	assert.Equal(t, http.StatusUnprocessableEntity, resend.StatusCode)
	assert.Contains(t, readBody(t, resend), RefusalCodeOutcomeUnrecorded)
	assert.Equal(t, int64(0), calls.Load())
}
