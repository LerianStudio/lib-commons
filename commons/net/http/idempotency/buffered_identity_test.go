//go:build unit

package idempotency

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
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

	identity, err := bufferedIdentity(c)
	require.NoError(t, err)

	assert.Equal(t, plain, identity,
		"the digest covers the decompressed body, exactly as it did before re-seating existed")

	reSeated, err := io.ReadAll(c.Request().BodyStream())
	require.NoError(t, err)
	assert.Equal(t, encoded, reSeated,
		"the handler declared Content-Encoding and must be handed the encoded bytes back")
}

// sliceCapture keeps the slice a reader hands to Write, which is the slice the
// reader reads from: bytes.Reader.WriteTo passes its own backing array, where
// io.ReadAll would copy it and hide the aliasing this probe exists to see.
type sliceCapture struct{ got []byte }

func (s *sliceCapture) Write(p []byte) (int, error) {
	s.got = p

	return len(p), nil
}

// TestBufferedIdentity_DetachesFromTheRequestBuffer pins the copy at the call
// site, where TestDetachBody cannot: skipping detachBody inside bufferedIdentity
// leaves the bytes correct and every other test green.
//
// Init2 with reduceMemoryUsage=false is the server's default and makes
// ResetBody keep the request's buffer instead of pooling it, so the buffer the
// body lived in before the call is still observable afterwards and the probe
// is deterministic.
func TestBufferedIdentity_DetachesFromTheRequestBuffer(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("C"), 4<<10)

	app := fiber.New()
	c := streamedCtx(t, app, body, len(body))
	c.RequestCtx().Init2(nil, nil, false)

	// Reading the stream consumes it, so capture the buffer and re-seat a copy:
	// the kept buffer is the request storage neither result may point into.
	before := c.Request().Body()
	require.NotEmpty(t, before)
	c.Request().SetBodyStream(bytes.NewReader(bytes.Clone(before)), len(before))
	require.True(t, c.Request().IsBodyStream())

	returned, err := bufferedIdentity(c)
	require.NoError(t, err)

	assert.Equal(t, body, returned)
	assert.False(t, sharesArray(returned, before),
		"the digest bytes must not point into the request's body buffer")

	reader, ok := c.Request().BodyStream().(io.WriterTo)
	require.True(t, ok, "the re-seated stream must expose its backing slice")

	var reSeated sliceCapture

	_, err = reader.WriteTo(&reSeated)
	require.NoError(t, err)
	assert.Equal(t, body, reSeated.got)
	assert.False(t, sharesArray(reSeated.got, before),
		"the re-seated reader must not point into the request's body buffer")
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

	identity, err := bufferedIdentity(c)
	require.NoError(t, err)
	assert.Equal(t, body, identity)
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

			_, err := bufferedIdentity(c)
			require.NoError(t, err)

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

// TestStreamedBody_ReadFailure_RefusesBeforeHandler pins what the default
// fingerprint does when the body stream fails part-way through.
//
// fasthttp's Request.Body() swallows a stream read error and puts its text in
// the body buffer, so reading the body through c.Body() fingerprinted that text
// and re-seated it as the request body: the handler stored the error string as
// the upload, and a retry carrying the real payload under the same key was then
// answered 422 IDEMPOTENCY_KEY_REUSE. A chunked request whose second chunk size
// is garbage is a stream failure any client can put on the wire, and chunked is
// the framing fasthttp never pre-reads, so the failure surfaces inside the
// middleware's own read. The request must be refused as unavailable with the
// handler never called.
func TestStreamedBody_ReadFailure_RefusesBeforeHandler(t *testing.T) {
	t.Parallel()

	var (
		called atomic.Int32
		seen   atomic.Value
	)

	logger := &recordingLogger{}

	app := fiber.New(fiber.Config{StreamRequestBody: true})
	app.Use(tenantMiddleware("t1"))
	app.Use(New(newRedisClient(t, miniredis.RunT(t)), WithLogger(logger)).Check())
	app.Post("/test", func(c fiber.Ctx) error {
		called.Add(1)

		body, err := io.ReadAll(c.Request().BodyStream())
		if err != nil {
			return err
		}

		seen.Store(string(body))

		return c.SendStatus(fiber.StatusCreated)
	})

	wire := []byte("POST /test HTTP/1.1\r\nHost: idempotency.test\r\n" +
		chttp.IdempotencyKey + ": broken-stream\r\n" +
		"Content-Type: " + fiber.MIMEOctetStream + "\r\nTransfer-Encoding: chunked\r\n\r\n" +
		"5\r\nhello\r\nzz\r\n")

	status, _, retired := newKeepAliveConn(t, serveStreamProbe(t, app)).roundTrip(wire)

	assert.Equal(t, http.StatusServiceUnavailable, status,
		"a body that failed to read must be refused before the handler, as unavailable")
	assert.Equal(t, int32(0), called.Load(), "the handler must never run on a body that failed to read")
	assert.Nil(t, seen.Load(), "the handler must never be handed the read error as the request body")
	assert.True(t, retired, "the rest of a body that failed to read is still on the wire, so the connection must go")

	// No provider is configured, so the operator must not be sent looking for one.
	line := logger.find(t, obs.LevelWarn, "request body unreadable")
	assert.ErrorIs(t, line.kv["error"].(error), errRequestBodyUnreadable)
	assert.False(t, logger.has("fingerprint provider failed"),
		"a body read failure must not be logged as a provider failure")
}

// postTruncated sends a keyed POST that declares declared bytes of body, writes
// only sent of them and then closes the client's side of the connection, the
// way a client that died mid-upload leaves it. keepAliveConn cannot stand in:
// it has no point between writing and reading where the write half can close.
// It returns the status, or 0 when the server closed without answering.
func postTruncated(t *testing.T, addr string, declared, sent int) int {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	head := fmt.Sprintf("POST /test HTTP/1.1\r\nHost: idempotency.test\r\n%s: truncated\r\n"+
		"Content-Type: %s\r\nContent-Length: %d\r\n\r\n",
		chttp.IdempotencyKey, fiber.MIMEOctetStream, declared)

	_, err = conn.Write(append([]byte(head), bytes.Repeat([]byte("x"), sent)...))
	require.NoError(t, err)

	tcp, ok := conn.(*net.TCPConn)
	require.True(t, ok)
	require.NoError(t, tcp.CloseWrite())

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0
	}

	require.NoError(t, resp.Body.Close())

	return resp.StatusCode
}

// TestStreamedBody_TruncatedDeclaredLength_RefusesBeforeHandler pins the
// other way a streamed body fails to arrive: the client declared a length and
// died before sending it.
//
// fasthttp reports that two different ways. A declared length inside its 8 KiB
// pre-read is read in full before the request exists, and a short one is
// refused there with no handler and no middleware. Past the pre-read the rest
// comes from the connection-backed stream, which returns the socket's io.EOF
// unchanged on a declared-length body, so a read to EOF sees a clean, short
// body. Re-seating that would hand the handler a truncated upload under a
// Content-Length it measured itself, and its fingerprint would make the retry
// carrying the whole payload a key reuse.
func TestStreamedBody_TruncatedDeclaredLength_RefusesBeforeHandler(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		declared, sent int
		wantStatus     int
	}{
		// fasthttp's own refusal, before any middleware runs.
		{name: "inside_the_pre_read", declared: 10, sent: 5, wantStatus: http.StatusBadRequest},
		{
			name:       "past_the_pre_read",
			declared:   3 * fasthttpStreamPreRead,
			sent:       2 * fasthttpStreamPreRead,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var (
				called atomic.Int32
				seen   atomic.Int64
			)

			app := fiber.New(fiber.Config{StreamRequestBody: true})
			app.Use(tenantMiddleware("t1"))
			app.Use(New(newRedisClient(t, miniredis.RunT(t))).Check())
			app.Post("/test", func(c fiber.Ctx) error {
				called.Add(1)

				body, err := io.ReadAll(c.Request().BodyStream())
				if err != nil {
					return err
				}

				seen.Store(int64(len(body)))

				return c.SendStatus(fiber.StatusCreated)
			})

			status := postTruncated(t, serveStreamProbe(t, app), testCase.declared, testCase.sent)

			assert.Equal(t, testCase.wantStatus, status, "a truncated body must be refused before the handler")
			assert.Equal(t, int32(0), called.Load(), "the handler must never run on a truncated body")
			assert.Zero(t, seen.Load(), "the handler must never be handed a truncated body")
		})
	}
}
