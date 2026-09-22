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
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
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
	assert.Contains(t, body, RefusalCodeReplayUnavailable)
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

// TestOversizeResponse_ClientErrorHonoursTheCachePolicy pins the half of the
// over-cap contract a configured POLICY decides, not a byte count.
//
// [ClientErrorPolicyCache] is the default, and it is the route owner saying "a
// rejection under this key is answered from the record, never re-executed" —
// because only the route owner knows whether its 4xx path writes anything (a
// declined-attempt audit row, a quota decrement, a fraud counter). Letting the
// SIZE of the rejection document decide that instead means the same route
// re-executes or does not depending on how many rows a validation report
// happens to carry, and the shorter report is the one that behaves as
// configured. So an over-cap rejection completes exactly like an over-cap
// success: delivered unchanged, key held, receipt missing.
func TestOversizeResponse_ClientErrorHonoursTheCachePolicy(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeStatusApp(t, "tenant-oversize-4xx", fiber.StatusUnprocessableEntity)

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	firstBody := readBody(t, first)

	require.Equal(t, http.StatusUnprocessableEntity, first.StatusCode,
		"the handler rejected the request; the client is owed that rejection unchanged")
	require.JSONEq(t, oversizeBody, firstBody)
	require.Empty(t, first.Header.Get(chttp.IdempotencyFenced),
		"a size is not a fault, so nothing is fenced")

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	secondBody := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load(),
		"the configured policy caches 4xx, so the resend must not reach the handler")
	assert.Equal(t, http.StatusConflict, second.StatusCode)
	assert.Contains(t, secondBody, RefusalCodeReplayUnavailable)
	assert.NotContains(t, secondBody, "successfully",
		"this request was REJECTED; the refusal must not book a success that never happened")
	assert.NotContains(t, secondBody, RefusalCodeOutcomeUnrecorded,
		"the outcome is recorded: the handler answered and the client received it")
	assert.Empty(t, second.Header.Get(chttp.IdempotencyFenced))
	assert.Empty(t, second.Header.Get(chttp.IdempotencyReplayed),
		"nothing was replayed; claiming otherwise tells the client it holds the original response")
}

// TestOversizeResponse_ClientErrorReleasePolicyStillReleases is the other half
// of the same contract. A route that wants a corrected resend to re-run its
// rejection path already has the option for it, and an over-cap document must
// not change that answer either.
func TestOversizeResponse_ClientErrorReleasePolicyStillReleases(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeStatusApp(t, "tenant-oversize-4xx-release",
		fiber.StatusUnprocessableEntity, WithClientErrorPolicy(ClientErrorPolicyRelease))

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusUnprocessableEntity, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	secondBody := readBody(t, second)

	assert.Equal(t, int32(2), calls.Load(),
		"the route released the key on 4xx, so the resend re-runs the handler")
	assert.Equal(t, http.StatusUnprocessableEntity, second.StatusCode)
	assert.JSONEq(t, oversizeBody, secondBody,
		"the resend collects the same rejection, not a refusal about it")
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
	assert.Contains(t, secondBody, RefusalCodeReplayUnavailable)
}

// TestOversizeResponse_BothWarningsNameTheRecord pins the only per-key trace an
// operator has of a key that will refuse every resend for its whole retention.
//
// This branch completes the record with no receipt, so once it fires every
// resend under that key is answered 409 IDEMPOTENCY_REPLAY_UNAVAILABLE until the
// record expires. Someone paged because a settlement route is answering a stream
// of those has to find the one record behind them, and "a response was too
// large" with neither a tenant nor a key is not an answer. Both WARN lines
// therefore carry the pair the fence logs already use, and the pair is what this
// test holds: measured, deleting it from either line left the whole package
// green.
func TestOversizeResponse_BothWarningsNameTheRecord(t *testing.T) {
	t.Parallel()

	const (
		tenantID  = "tenant-oversize-warnings"
		clientKey = "oversize-key"
	)

	logger := &recordingLogger{}

	app, calls := newOversizeApp(t, tenantID, WithLogger(logger))

	response := postBodyWithKey(t, app, clientKey, `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Equal(t, int32(1), calls.Load())

	digest := keyDigest("idempotency:" + tenantID + ":" + clientKey)

	for _, substring := range []string{
		// captureResponse, where the size is measured.
		"response body exceeds maxBodyCache",
		// handleStoreAcquired, where the key is completed without a receipt.
		"completing the key without a replayable receipt",
	} {
		line := logger.find(t, obs.LevelWarn, substring)

		assert.Equal(t, digest, line.kv["idempotency_key_digest"],
			"%q must name the record it made unreplayable, as a digest and never the raw key", substring)
		assert.Equal(t, tenantID, line.kv["tenant_id"],
			"%q must name the tenant whose key it spent", substring)
	}
}
