//go:build unit

package idempotency

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oversizeBody is the handler's success document. It is deliberately longer
// than the cap the tests below configure, so the receipt cannot be stored.
const oversizeBody = `{"id":"9f1c","status":"settled","amount":"1250.00"}`

// postBodyWithKey sends POST /test with an explicit body, so a resend can
// differ from the original in nothing but its payload — which is what the
// fingerprint gate routes on.
func postBodyWithKey(t *testing.T, app *fiber.App, key, body string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

	if key != "" {
		req.Header.Set(chttp.IdempotencyKey, key)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	return resp
}

// newOversizeApp mounts middleware whose body cap is smaller than the success
// document the handler writes, and counts handler executions.
func newOversizeApp(t *testing.T, tenantID string, opts ...Option) (*fiber.App, *atomic.Int32) {
	t.Helper()

	return newOversizeStatusApp(t, tenantID, fiber.StatusCreated, opts...)
}

// newOversizeStatusApp is newOversizeApp with the handler's status under the
// caller's control. An over-cap document is not always a success: a validation
// route answers a rejection with a per-row report, which is the longest thing
// it ever writes.
func newOversizeStatusApp(t *testing.T, tenantID string, status int, opts ...Option) (*fiber.App, *atomic.Int32) {
	t.Helper()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn, append([]Option{WithMaxBodyCache(len(oversizeBody) - 1)}, opts...)...)

	var calls atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(status).SendString(oversizeBody)
	})

	return app, &calls
}

// smallBody fits every cap these tests configure, so a failure under it can
// only come from the response codec.
const smallBody = `{"id":"tiny"}`

// stubResponseCodec hands captureResponse whatever encoded output the test
// names. The two guards on that output are not the same condition — one is a
// size, the other is a malfunction — and only this seam can tell them apart.
type stubResponseCodec struct{ encoded []byte }

func (s stubResponseCodec) Encode(context.Context, []byte) ([]byte, error) {
	return s.encoded, nil
}

func (s stubResponseCodec) Decode(_ context.Context, encoded []byte) ([]byte, error) {
	return encoded, nil
}

// newCodecApp mounts middleware whose raw body is comfortably under the cap, so
// the codec is the only thing that can send capture down a failure path.
func newCodecApp(t *testing.T, tenantID string, codec ResponseCodec) (*fiber.App, *atomic.Int32) {
	t.Helper()

	conn := newRedisClient(t, miniredis.RunT(t))
	m := New(conn, WithMaxBodyCache(len(oversizeBody)), WithResponseCodec(codec))

	var calls atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(fiber.StatusCreated).SendString(smallBody)
	})

	return app, &calls
}

// TestOversizeResponse_SuccessReachesTheClientUnchanged is the measurement this
// task exists for. A body size is not a fault: the handler ran, its mutation
// committed, and the client must receive that success exactly as written. The
// shipped middleware instead answered 503 "IDEMPOTENCY_UNAVAILABLE" and fenced
// the key, so a route whose success document is larger than the cap reported
// every committed mutation as a store failure.
func TestOversizeResponse_SuccessReachesTheClientUnchanged(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeApp(t, "tenant-oversize-first")

	response := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	body := readBody(t, response)

	assert.Equal(t, http.StatusCreated, response.StatusCode,
		"the handler succeeded; the client must be told so")
	assert.JSONEq(t, oversizeBody, body,
		"the handler's own document must reach the client, not a refusal")
	assert.Equal(t, int32(1), calls.Load())
	assert.Empty(t, response.Header.Get(chttp.IdempotencyFenced),
		"nothing was fenced: the key holds a completed record, not an unrecorded outcome")
}

// TestOversizeResponse_ResendIsRefusedWithoutReExecuting pins the other half of
// the contract. The receipt is gone, so the success cannot be replayed — but the
// mutation must not run a second time under the same key either. The key answers
// the resend itself, and the answer says the operation completed.
func TestOversizeResponse_ResendIsRefusedWithoutReExecuting(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeApp(t, "tenant-oversize-resend")

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	body := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load(),
		"the mutation must never execute twice under one key")
	assert.Equal(t, http.StatusConflict, second.StatusCode)
	assert.Contains(t, body, "IDEMPOTENCY_REPLAY_UNAVAILABLE")
	assert.Empty(t, second.Header.Get(fiber.HeaderRetryAfter),
		"the answer never changes inside the retention window, so it must not advertise a retry")
	assert.Empty(t, second.Header.Get(chttp.IdempotencyReplayed),
		"nothing was replayed; claiming a replay would report a body that was never stored")
}

// TestOversizeResponse_DifferentFingerprintIsStillKeyReuse guards the gate order.
// A completed-but-unreplayable record must not soften the reuse refusal: a
// different payload under a spent key is still reuse, and answering it with the
// replay-unavailable document would report an outcome for an operation that
// never ran.
func TestOversizeResponse_DifferentFingerprintIsStillKeyReuse(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeApp(t, "tenant-oversize-reuse")

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"9999.00"}`)
	body := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, http.StatusUnprocessableEntity, second.StatusCode)
	assert.Contains(t, body, "IDEMPOTENCY_KEY_REUSE")
}

// TestOversizeResponse_RefusalIsOverridable proves the seam: a service that
// wants its own document for this refusal takes the option, and the built-in
// 409 gets out of the way.
func TestOversizeResponse_RefusalIsOverridable(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeApp(t, "tenant-oversize-seam",
		WithReplayUnavailableHandler(func(c fiber.Ctx) error {
			return c.Status(http.StatusGone).SendString("reconcile 9f1c")
		}))

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	body := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, http.StatusGone, second.StatusCode)
	assert.Equal(t, "reconcile 9f1c", body)
}

// TestOversizeResponse_UnderCapStillReplays is the regression guard: everything
// above must change nothing for a response that fits the cap.
func TestOversizeResponse_UnderCapStillReplays(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var calls atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-under-cap"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(fiber.StatusCreated).SendString(oversizeBody)
	})

	first := postBodyWithKey(t, app, "under-cap-key", `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBodyWithKey(t, app, "under-cap-key", `{"amount":"1250.00"}`)
	body := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, http.StatusCreated, second.StatusCode)
	assert.JSONEq(t, oversizeBody, body)
	assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed))
}

// TestOversizeResponse_ClientErrorNeverClaimsSuccess pins the half of the
// over-cap contract a status code decides. A 4xx is a REJECTION: the mutation
// committed nothing, so a resend must never be answered with the refusal that
// reports a KNOWN success. A client told "already completed successfully" for a
// payment its own service refused books one that does not exist, and the
// validation report that would explain the refusal is unreachable for the whole
// retention window.
func TestOversizeResponse_ClientErrorNeverClaimsSuccess(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeStatusApp(t, "tenant-oversize-4xx", fiber.StatusUnprocessableEntity)

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	firstBody := readBody(t, first)

	require.Equal(t, http.StatusUnprocessableEntity, first.StatusCode,
		"the handler rejected the request; the client is owed that rejection unchanged")
	require.JSONEq(t, oversizeBody, firstBody)

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	secondBody := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load(), "the handler must not run a second time")
	assert.NotContains(t, secondBody, "IDEMPOTENCY_REPLAY_UNAVAILABLE",
		"that refusal reports a known success; this request committed nothing")
	assert.NotContains(t, secondBody, "already completed successfully")
	assert.Contains(t, secondBody, RefusalCodeOutcomeUnrecorded,
		"with no receipt for a rejection the honest answer is that the outcome was not recorded")
}

// TestOversizeResponse_EmptyEncodingIsAFaultNotASize separates the two
// conditions that share the size guard. A codec that encodes nothing is broken,
// and absorbing it as a size turns every response on the route into a
// non-replayable one while the log and the client document blame a cap the
// operator can raise forever without moving the symptom.
func TestOversizeResponse_EmptyEncodingIsAFaultNotASize(t *testing.T) {
	t.Parallel()

	app, calls := newCodecApp(t, "tenant-codec-empty", stubResponseCodec{encoded: nil})

	response := postBodyWithKey(t, app, "codec-key", `{"amount":"1250.00"}`)
	body := readBody(t, response)

	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode,
		"a codec that produced no bytes is a malfunction, and the loud answer is the one that pages someone")
	assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
	assert.Equal(t, "true", response.Header.Get(chttp.IdempotencyFenced),
		"the receipt failed for an unknown reason, so the key is fenced as before")
}

// TestOversizeResponse_EncodedOutputOverTheBoundCompletes covers the second size
// guard: a codec that inflates a small body past the encoded bound IS a size,
// so it takes the same completion path as an over-cap raw body.
func TestOversizeResponse_EncodedOutputOverTheBoundCompletes(t *testing.T) {
	t.Parallel()

	app, calls := newCodecApp(t, "tenant-codec-inflated",
		stubResponseCodec{encoded: bytes.Repeat([]byte("z"), 4*len(oversizeBody))})

	first := postBodyWithKey(t, app, "codec-key", `{"amount":"1250.00"}`)
	firstBody := readBody(t, first)

	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.JSONEq(t, smallBody, firstBody)

	second := postBodyWithKey(t, app, "codec-key", `{"amount":"1250.00"}`)
	secondBody := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, http.StatusConflict, second.StatusCode)
	assert.Contains(t, secondBody, "IDEMPOTENCY_REPLAY_UNAVAILABLE")
}
