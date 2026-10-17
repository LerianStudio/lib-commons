//go:build unit

package idempotency

import (
	"net/http"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSingleTenant_KeyShape pins where a keyed request's record lives in each
// mode. Single-tenant roots it at no tenant and never reads the tenant-manager
// context; the default rows are the shipped shapes, which must not move.
func TestSingleTenant_KeyShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []Option
		ctxTenant string
		wantKeys  []string
	}{
		{"single-tenant, no tenant on the context", []Option{WithSingleTenant()}, "", []string{"idem:k1"}},
		{"single-tenant never reads a tenant-manager tenant", []Option{WithSingleTenant()}, "t1", []string{"idem:k1"}},
		{"default, tenant on the context", nil, "t1", []string{"idem:t1:k1"}},
		{"default, no tenant bypasses", nil, "", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)

			var calls atomic.Int32

			m := New(newRedisClient(t, mr), append([]Option{WithKeyPrefix("idem:")}, tc.opts...)...)
			require.NoError(t, m.Err())

			resp := doPost(t, tenantEchoApp(m.Check(), tc.ctxTenant, &calls), "k1")
			resp.Body.Close()

			require.Equal(t, http.StatusCreated, resp.StatusCode)
			assert.Equal(t, int32(1), calls.Load())

			if tc.wantKeys == nil {
				assert.Empty(t, mr.Keys())
			} else {
				assert.Equal(t, tc.wantKeys, mr.Keys())
			}
		})
	}
}

// TestSingleTenant_ReplaysAndRefusesDuplicates walks the three duplicate
// answers with no tenant anywhere: a resend replays, a different payload is key
// reuse, and a key still in flight is a conflict. The handler runs once.
func TestSingleTenant_ReplaysAndRefusesDuplicates(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)

	var calls atomic.Int32

	m := New(newRedisClient(t, mr), WithSingleTenant())
	app := newEchoApp(m.Check(), &calls)

	first := doSend(t, app, http.MethodPost, "/test", `{"amount":100}`, "k1")
	require.Equal(t, http.StatusCreated, first.StatusCode)
	assert.JSONEq(t, `{"amount":100}`, readBody(t, first))

	replay := doSend(t, app, http.MethodPost, "/test", `{"amount":100}`, "k1")
	assert.Equal(t, http.StatusCreated, replay.StatusCode)
	assert.Equal(t, "true", replay.Header.Get(chttp.IdempotencyReplayed))
	assert.JSONEq(t, `{"amount":100}`, readBody(t, replay))

	reuse := doSend(t, app, http.MethodPost, "/test", `{"amount":999}`, "k1")
	assert.Equal(t, http.StatusUnprocessableEntity, reuse.StatusCode)
	assert.Contains(t, readBody(t, reuse), "IDEMPOTENCY_KEY_REUSE")

	seedStoreRecord(t, mr, "idempotency:k2", storeRecord{
		State:       keyStateProcessing,
		Fingerprint: requestFingerprint(http.MethodPost, "/test", []byte(`{}`)),
		Owner:       "owner-in-flight",
	})

	conflict := doSend(t, app, http.MethodPost, "/test", `{}`, "k2")
	assert.Equal(t, http.StatusConflict, conflict.StatusCode)
	assert.Contains(t, readBody(t, conflict), "IDEMPOTENCY_CONFLICT")

	assert.Equal(t, int32(1), calls.Load(), "only the first request may run the handler")
}

// TestSingleTenant_FenceOutcomeUnknown proves the consumer fence works with no
// tenant: it lands where Check reads, binds the resend, and its log line names
// no tenant. bridgeApp stamps a tenant on the context, which this mode ignores.
func TestSingleTenant_FenceOutcomeUnknown(t *testing.T) {
	t.Parallel()

	store, mr := realRedisStore(t)
	logger := &recordingLogger{}
	mw := NewWithStore(store, WithKeyTTL(fenceRetention), WithSingleTenant(), WithLogger(logger))

	var (
		calls    atomic.Int64
		fenceErr error
	)

	app := bridgeApp(mw, fenceRetention, &fenceErr, &calls)

	bridged := sendBridge(t, app, `{"amount":100}`, true)
	bridged.Body.Close()
	require.NoError(t, fenceErr)
	assert.Equal(t, []string{"idempotency:bridge-key"}, mr.Keys())

	resend := sendBridge(t, app, `{"amount":100}`, false)
	assert.Equal(t, http.StatusUnprocessableEntity, resend.StatusCode)
	assert.Contains(t, readBody(t, resend), RefusalCodeOutcomeUnrecorded)
	assert.Equal(t, int64(0), calls.Load(), "a fenced key must never execute the handler")

	line := logger.find(t, obs.LevelWarn, "fenced with an unrecorded outcome by the application")
	assert.NotContains(t, line.kv, "tenant_id", "a record rooted at no tenant names none")
}

// TestSingleTenant_ConflictingOptionsFailClosed pins that asking for no tenant
// and for a tenant at once is a configuration error, whatever the option order,
// and that the middleware refuses keyed mutations rather than guess.
func TestSingleTenant_ConflictingOptionsFailClosed(t *testing.T) {
	t.Parallel()

	provider := WithTenantProvider(func(fiber.Ctx) (string, error) { return "p1", nil })

	tests := map[string][]Option{
		"with WithRequireTenant":               {WithSingleTenant(), WithRequireTenant()},
		"with WithTenantProvider, given first": {provider, WithSingleTenant()},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)

			var calls atomic.Int32

			m := New(newRedisClient(t, mr), opts...)
			require.ErrorIs(t, m.Err(), ErrSingleTenantMisconfigured)

			resp := doPost(t, tenantEchoApp(m.Check(), "t1", &calls), "k1")

			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Equal(t, "IDEMPOTENCY_UNAVAILABLE", decodeErrorBody(t, resp).Title)
			assert.Equal(t, int32(0), calls.Load(), "the handler must not run")
			assert.Empty(t, mr.Keys())
		})
	}
}
