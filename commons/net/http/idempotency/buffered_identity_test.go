//go:build unit

package idempotency

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// streamedCtx builds a request the server has handed over as a stream, which is
// what Fiber's StreamRequestBody produces and what app.Test cannot: the body is
// a reader, not bytes, and nothing has read it yet.
//
// size is the declared Content-Length, or -1 for a chunked request.
func streamedCtx(t *testing.T, app *fiber.App, body []byte, size int) fiber.Ctx {
	t.Helper()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(fiber.MethodPost)
	fctx.Request.SetRequestURI("/test")
	fctx.Request.SetBodyStream(bytes.NewReader(body), size)

	c := app.AcquireCtx(fctx)
	t.Cleanup(func() { app.ReleaseCtx(c) })

	require.True(t, c.Request().IsBodyStream(), "the probe must start from a streamed request")

	return c
}

// sharesArray reports whether a and b are the same bytes in memory rather than
// merely equal ones. It is the only instrument that can see the defect below: a
// slice that was never copied is byte-for-byte correct right up until the
// buffer under it is handed to another request.
func sharesArray(a, b []byte) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

// TestDetachBody is the guard on the copies nothing else in the package pins.
//
// Deleting either one leaves every behavioural test green — the bytes are still
// correct when the test reads them — while a keyed request digests, or hands
// its handler, whatever the next request writes into the buffer SetBodyStream
// returned to fasthttp's pool. Reproducing that through the pool is a race and
// would fail only some of the time, so the copies are pinned where they are
// decided instead.
func TestDetachBody(t *testing.T) {
	t.Parallel()

	raw := bytes.Repeat([]byte("A"), 4<<10)
	decompressed := bytes.Repeat([]byte("B"), 4<<10)

	t.Run("aliased_inputs_cost_one_copy", func(t *testing.T) {
		t.Parallel()

		identity, detached := detachBody(raw, raw)

		assert.Equal(t, raw, identity)
		assert.Equal(t, raw, detached)
		assert.False(t, sharesArray(identity, raw),
			"the digest bytes must not point into the buffer ResetBody returns to the pool")
		assert.False(t, sharesArray(detached, raw),
			"the re-seated reader must not point into the buffer ResetBody returns to the pool")
		assert.True(t, sharesArray(identity, detached),
			"the same bytes in must cost one allocation, not two")
	})

	t.Run("distinct_inputs_are_both_copied", func(t *testing.T) {
		t.Parallel()

		identity, detached := detachBody(decompressed, raw)

		assert.Equal(t, decompressed, identity, "the digest keeps covering the decompressed body")
		assert.Equal(t, raw, detached, "the handler keeps receiving the raw body")
		assert.False(t, sharesArray(identity, decompressed))
		assert.False(t, sharesArray(detached, raw))
	})

	t.Run("nil_and_empty_inputs", func(t *testing.T) {
		t.Parallel()

		identity, detached := detachBody(nil, nil)
		assert.Nil(t, identity)
		assert.Nil(t, detached)

		identity, detached = detachBody([]byte{}, nil)
		assert.Empty(t, identity)
		assert.Empty(t, detached)
	})
}

// TestBufferedIdentity_ReSeatsTheRawBytes pins which of the two bodies goes
// back on the stream. Fiber's c.Body() decompresses under Content-Encoding and
// c.Request().Body() does not, so the choice is load-bearing in both
// directions: the handler must receive the bytes the socket carried, and the
// digest must keep covering the decompressed ones so no stored fingerprint
// moves.
func TestBufferedIdentity_ReSeatsTheRawBytes(t *testing.T) {
	t.Parallel()

	plain := bytes.Repeat([]byte("payload"), 512)

	var compressed bytes.Buffer

	zw := gzip.NewWriter(&compressed)
	_, err := zw.Write(plain)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	encoded := compressed.Bytes()
	require.NotEqual(t, plain, encoded, "the two bodies must differ for this test to discriminate")

	app := fiber.New()
	c := streamedCtx(t, app, encoded, len(encoded))
	c.Request().Header.Set(fiber.HeaderContentEncoding, "gzip")

	identity := bufferedIdentity(c)

	assert.Equal(t, plain, identity,
		"the digest covers the decompressed body, exactly as it did before re-seating existed")

	reSeated, err := io.ReadAll(c.Request().BodyStream())
	require.NoError(t, err)
	assert.Equal(t, encoded, reSeated,
		"the handler declared Content-Encoding and must be handed the encoded bytes back")
}

// TestBufferedIdentity_NonStreamedRequestIsUntouched pins the guard that scopes
// re-seating to requests the server streamed. Handing a stream to a handler
// that was never given one is the same defect in the other direction, and it
// would charge every buffered request a copy of its own body.
func TestBufferedIdentity_NonStreamedRequestIsUntouched(t *testing.T) {
	t.Parallel()

	body := []byte(`{"name":"upload"}`)

	app := fiber.New()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(fiber.MethodPost)
	fctx.Request.SetRequestURI("/test")
	fctx.Request.SetBody(body)

	c := app.AcquireCtx(fctx)
	defer app.ReleaseCtx(c)

	require.False(t, c.Request().IsBodyStream())

	assert.Equal(t, body, bufferedIdentity(c))
	assert.False(t, c.Request().IsBodyStream(),
		"a request the server buffered must not reach the handler as a stream")
}

// TestBufferedIdentity_KeepsTheClientsFraming pins that the re-seat is
// invisible in the request headers.
//
// SetBodyStream with a length declares a Content-Length and deletes
// Transfer-Encoding, so re-seating a chunked upload at its measured size would
// hand the handler a framing the client never sent.
func TestBufferedIdentity_KeepsTheClientsFraming(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("x"), 2048)

	testCases := []struct {
		name              string
		size              int
		wantContentLength int
		wantChunked       bool
	}{
		{
			name:              "declared_length_stays_declared",
			size:              len(body),
			wantContentLength: len(body),
		},
		{
			name:              "chunked_stays_chunked",
			size:              -1,
			wantContentLength: -1,
			wantChunked:       true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			c := streamedCtx(t, app, body, testCase.size)

			bufferedIdentity(c)

			assert.Equal(t, testCase.wantContentLength, c.Request().Header.ContentLength(),
				"the handler must observe the length the client declared")

			chunked := bytes.Equal(c.Request().Header.Peek(fiber.HeaderTransferEncoding), []byte("chunked"))
			assert.Equal(t, testCase.wantChunked, chunked,
				"the handler must observe the transfer encoding the client sent")

			reSeated, err := io.ReadAll(c.Request().BodyStream())
			require.NoError(t, err)
			assert.Equal(t, body, reSeated, "either framing must still carry the whole body")
		})
	}
}

// framingProbe is what the protected handler reports back about the request it
// was handed: whether it is still a stream, how many bytes it carries, and the
// framing the client chose.
type framingProbe struct {
	Stream        bool   `json:"stream"`
	Bytes         int    `json:"bytes"`
	ContentLength int    `json:"contentLength"`
	Transfer      string `json:"transfer"`
}

// TestStoredFingerprint_IsFramingIndependent is the deploy guard.
//
// The digest covers the body bytes and nothing about how they arrived, so a
// record written by one process must match a retry parsed by another under a
// different framing or a different StreamRequestBody setting. If re-seating had
// moved it, a rolling deploy would answer 422 IDEMPOTENCY_KEY_REUSE to every
// legitimate retry that crossed the version boundary.
func TestStoredFingerprint_IsFramingIndependent(t *testing.T) {
	t.Parallel()

	body := []byte(`{"amount":10}`)
	want := requestFingerprint(http.MethodPost, "/test", body)

	testCases := []struct {
		name     string
		streamed bool
		chunked  bool
	}{
		{name: "buffered_route"},
		{name: "streamed_declared_length", streamed: true},
		{name: "streamed_chunked", streamed: true, chunked: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)

			app := fiber.New(fiber.Config{StreamRequestBody: testCase.streamed})
			app.Use(tenantMiddleware("tenant-a"))
			app.Use(New(newRedisClient(t, mr)).Check())
			app.Post("/test", func(c fiber.Ctx) error {
				if r := c.Request().BodyStream(); r != nil {
					if _, err := io.Copy(io.Discard, r); err != nil {
						return err
					}
				}

				return c.SendStatus(fiber.StatusCreated)
			})

			req, err := http.NewRequest(http.MethodPost, "http://"+serveStreamProbe(t, app)+"/test",
				bytes.NewReader(body))
			require.NoError(t, err)

			if testCase.chunked {
				req.ContentLength = -1
			}

			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			req.Header.Set(chttp.IdempotencyKey, "fp-key")

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusCreated, resp.StatusCode)

			stored, err := mr.Get("idempotency:tenant-a:fp-key")
			require.NoError(t, err)

			var record storeRecord
			require.NoError(t, json.Unmarshal([]byte(stored), &record))
			assert.Equal(t, want, record.Fingerprint,
				"every framing must store the digest a record written before re-seating existed carries")
		})
	}
}

// TestChunkedKeyedDuplicate_KeepsTheConnection walks the chunked half of the
// connection contract, which no test reached before: every chunked subtest in
// the package is a refusal that returns before the fingerprint is read, so the
// re-seat never ran on one.
//
// A duplicate is answered by the guard with the handler never called, and the
// retirement rule asks whether anything is left in the connection. Nothing is:
// the fingerprint read the whole chunked body off the wire and put it back as a
// rewindable reader. Retiring here would charge a pooled client a fresh
// handshake per duplicate to protect an empty socket.
func TestChunkedKeyedDuplicate_KeepsTheConnection(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("x"), 70<<10)

	var called atomic.Int32

	app := fiber.New(fiber.Config{StreamRequestBody: true})
	app.Use(tenantMiddleware("t1"))
	app.Use(New(newRedisClient(t, miniredis.RunT(t))).Check())
	app.Post("/test", func(c fiber.Ctx) error {
		called.Add(1)

		if r := c.Request().BodyStream(); r != nil {
			if _, err := io.Copy(io.Discard, r); err != nil {
				return err
			}
		}

		return c.SendStatus(fiber.StatusCreated)
	})

	client := newKeepAliveConn(t, serveStreamProbe(t, app))

	for i := 1; i <= 3; i++ {
		status, _, retired := client.postChunked("chunked-dup", body)

		require.Equal(t, http.StatusCreated, status, "request %d must be answered", i)
		assert.False(t, retired,
			"the fingerprint took every byte off the wire and put it back, so request %d "+
				"must not cost the connection", i)
	}

	assert.Equal(t, int32(1), called.Load(), "only the first request may reach the handler")
	assert.Equal(t, 1, client.dials, "one connection must carry all three requests")
}

// TestChunkedKeyedRequest_ReachesTheHandlerChunked is the end-to-end half of
// the framing contract, over a real socket because chunked is the framing
// app.Test cannot produce.
//
// A handler that branches on "no declared length, so stream this to object
// storage" must take the same branch keyed and unkeyed. Before the re-seat kept
// the framing it took the sized branch on every keyed request, which is this
// middleware quietly changing what the handler below sees — the defect the
// re-seat exists to fix, one layer down.
func TestChunkedKeyedRequest_ReachesTheHandlerChunked(t *testing.T) {
	t.Parallel()

	const bodySize = 70 << 10

	body := bytes.Repeat([]byte("x"), bodySize)

	var called atomic.Int32

	app := fiber.New(fiber.Config{StreamRequestBody: true})
	app.Use(tenantMiddleware("t1"))
	app.Use(New(newRedisClient(t, miniredis.RunT(t))).Check())
	app.Post("/test", func(c fiber.Ctx) error {
		called.Add(1)

		probe := framingProbe{
			ContentLength: c.Request().Header.ContentLength(),
			Transfer:      string(c.Request().Header.Peek(fiber.HeaderTransferEncoding)),
		}

		// Read the body the way humafiber does and nothing else: it returns
		// Request().BodyStream() whatever that is and never falls back to
		// c.Body(), so a probe that falls back reports a body the real adapter
		// would not have seen.
		if r := c.Request().BodyStream(); r != nil {
			probe.Stream = true

			n, err := io.Copy(io.Discard, r)
			if err != nil {
				return err
			}

			probe.Bytes = int(n)
		}

		return c.Status(fiber.StatusCreated).JSON(probe)
	})

	addr := serveStreamProbe(t, app)

	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/test", bytes.NewReader(body))
	require.NoError(t, err)

	// -1 is what makes net/http frame this chunked: no Content-Length reaches
	// the server, so fasthttp pre-reads none of it and hands the whole body
	// over as a live stream.
	req.ContentLength = -1
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEOctetStream)
	req.Header.Set(chttp.IdempotencyKey, "chunked-key")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"a keyed chunked request carrying a body must reach the handler")
	assert.Equal(t, int32(1), called.Load())

	var got framingProbe
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	assert.True(t, got.Stream, "the handler must still be handed a readable stream")
	assert.Equal(t, bodySize, got.Bytes, "the stream must carry the whole body")
	assert.Equal(t, -1, got.ContentLength,
		"the client declared no length and the handler must not see one invented")
	assert.Equal(t, "chunked", got.Transfer,
		"the client framed this chunked and the handler must see it that way")
}
