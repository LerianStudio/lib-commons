//go:build unit

package idempotency

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamProbe is what the protected handler reports back: whether the request
// body was still a stream when the handler got it, and how many bytes the
// handler itself could read. It is the whole differential of the streamed-body
// test — a middleware that fingerprinted the raw body has already drained and
// closed that stream, so Stream comes back false.
type streamProbe struct {
	Stream bool `json:"stream"`
	Bytes  int  `json:"bytes"`
}

// streamProbeApp routes POST /test through mw on an app configured exactly as a
// large-upload service configures it: StreamRequestBody, so the handler is
// called before the body has been read.
func streamProbeApp(mw fiber.Handler, called *atomic.Int32) *fiber.App {
	app := fiber.New(fiber.Config{StreamRequestBody: true})
	app.Use(tenantMiddleware("t1"))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		called.Add(1)

		probe := streamProbe{Stream: c.Request().IsBodyStream()}

		if probe.Stream {
			n, err := io.Copy(io.Discard, c.Request().BodyStream())
			if err != nil {
				return err
			}

			probe.Bytes = int(n)
		} else {
			probe.Bytes = len(c.Body())
		}

		return c.Status(fiber.StatusCreated).JSON(probe)
	})

	return app
}

// postBody sends POST /test with a body and content type of the caller's
// choosing. doPost sends no body, which is exactly what these tests cannot use.
func postBody(t *testing.T, app *fiber.App, key, contentType string, body []byte) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set(fiber.HeaderContentType, contentType)

	if key != "" {
		req.Header.Set(chttp.IdempotencyKey, key)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	return resp
}

// TestFingerprintProvider_StreamedBody_LeavesTheStreamUntouched is the
// differential that proves the provider path never reads the body.
//
// Without a provider the middleware hashes c.Body(), and fasthttp answers that
// call by draining the request stream into memory and closing it: a service
// running StreamRequestBody for 1 GiB uploads silently buffers every one of
// them, and the handler's streaming branch is dead code. With a provider the
// handler must still find a live stream and read it itself.
func TestFingerprintProvider_StreamedBody_LeavesTheStreamUntouched(t *testing.T) {
	t.Parallel()

	const bodySize = 64 << 10

	body := bytes.Repeat([]byte("x"), bodySize)

	t.Run("without_provider_the_body_is_buffered", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))

		var called atomic.Int32

		resp := postBody(t, streamProbeApp(New(conn).Check(), &called), "k1",
			fiber.MIMEOctetStream, body)

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		probe := decodeStreamProbe(t, resp)
		assert.False(t, probe.Stream,
			"fingerprinting the raw body drains and closes the request stream")
		assert.Equal(t, bodySize, probe.Bytes)
		assert.Equal(t, int32(1), called.Load())
	})

	t.Run("with_provider_the_stream_reaches_the_handler", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))

		var called atomic.Int32

		m := New(conn, WithFingerprintProvider(func(c fiber.Ctx) ([]byte, error) {
			return []byte(c.Get(fiber.HeaderContentLength)), nil
		}))

		resp := postBody(t, streamProbeApp(m.Check(), &called), "k1",
			fiber.MIMEOctetStream, body)

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		probe := decodeStreamProbe(t, resp)
		assert.True(t, probe.Stream,
			"the provider path must never call c.Body(): the handler owns the stream")
		assert.Equal(t, bodySize, probe.Bytes,
			"the handler must still be able to read the whole body from the stream")
		assert.Equal(t, int32(1), called.Load())
	})
}

func decodeStreamProbe(t *testing.T, resp *http.Response) streamProbe {
	t.Helper()

	defer resp.Body.Close()

	var got streamProbe
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	return got
}

// multipartIdentity is the kind of provider a file-upload route writes: the
// declared part names, filenames and sizes, in a stable order. It is stable
// across the fresh boundary every multipart encoder picks per request, which
// the raw body is not.
func multipartIdentity(c fiber.Ctx) ([]byte, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, fmt.Errorf("multipart form: %w", err)
	}

	fields := make([]string, 0, len(form.File))
	for field := range form.File {
		fields = append(fields, field)
	}

	sort.Strings(fields)

	var identity strings.Builder

	for _, field := range fields {
		for _, header := range form.File[field] {
			fmt.Fprintf(&identity, "%s\x00%s\x00%d\n", field, header.Filename, header.Size)
		}
	}

	return []byte(identity.String()), nil
}

// newMultipartBody encodes the same logical upload again, with the fresh random
// boundary mime/multipart (and every browser) picks per request.
func newMultipartBody(t *testing.T) (body []byte, contentType string) {
	t.Helper()

	var buf bytes.Buffer

	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("statement", "june.csv")
	require.NoError(t, err)
	_, err = part.Write([]byte("date,amount\n2026-06-01,10.00\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return buf.Bytes(), writer.FormDataContentType()
}

// TestFingerprintProvider_Multipart_RetryUnderTheSameKeyReplays covers the
// second consumer fact: a byte-identical logical retry of a multipart upload
// carries a different boundary, so the raw-body fingerprint never matches its
// own retry and the published "retry with the same key" contract is
// unreachable on any multipart route.
func TestFingerprintProvider_Multipart_RetryUnderTheSameKeyReplays(t *testing.T) {
	t.Parallel()

	first, firstType := newMultipartBody(t)
	second, secondType := newMultipartBody(t)

	require.NotEqual(t, firstType, secondType,
		"the premise of this test is that each encoding picks a fresh boundary")

	t.Run("without_provider_the_retry_is_refused_as_reuse", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))

		var called atomic.Int32

		app := uploadApp(New(conn).Check(), &called)

		postBody(t, app, "k1", firstType, first).Body.Close()

		resp := postBody(t, app, "k1", secondType, second)

		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		assert.Equal(t, "IDEMPOTENCY_KEY_REUSE", decodeErrorBody(t, resp).Title)
		assert.Equal(t, int32(1), called.Load())
	})

	t.Run("with_provider_the_retry_replays", func(t *testing.T) {
		t.Parallel()

		conn := newRedisClient(t, miniredis.RunT(t))

		var called atomic.Int32

		app := uploadApp(New(conn, WithFingerprintProvider(multipartIdentity)).Check(), &called)

		firstResp := postBody(t, app, "k1", firstType, first)
		require.Equal(t, http.StatusCreated, firstResp.StatusCode)
		firstBody := readBody(t, firstResp)

		secondResp := postBody(t, app, "k1", secondType, second)

		assert.Equal(t, http.StatusCreated, secondResp.StatusCode)
		assert.Equal(t, firstBody, readBody(t, secondResp))
		assert.Equal(t, int32(1), called.Load(),
			"the retry must replay, not re-execute the upload")
	})
}

// uploadApp routes POST /test through mw and answers 201 with a body that
// changes per execution, so a replay is distinguishable from a re-execution.
func uploadApp(mw fiber.Handler, called *atomic.Int32) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware("t1"))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).
			JSON(fiber.Map{"execution": called.Add(1)})
	})

	return app
}

// TestFingerprintProvider_Error_RefusesBeforeHandler pins the posture for a
// request whose identity cannot be established: it must not run unprotected.
// The refusal is the pre-handler one — nothing ran, so the caller is told to
// retry, never to reconcile — and it honours WithUnavailableHandler.
func TestFingerprintProvider_Error_RefusesBeforeHandler(t *testing.T) {
	t.Parallel()

	failing := WithFingerprintProvider(func(fiber.Ctx) ([]byte, error) {
		return nil, errors.New("cannot read the request identity")
	})

	t.Run("built_in_refusal", func(t *testing.T) {
		t.Parallel()

		mr := miniredis.RunT(t)

		var called atomic.Int32

		resp := postBody(t, uploadApp(New(newRedisClient(t, mr), failing).Check(), &called),
			"k1", fiber.MIMEApplicationJSON, []byte(`{"amount":10}`))

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, "IDEMPOTENCY_UNAVAILABLE", decodeErrorBody(t, resp).Title)
		assert.Equal(t, int32(0), called.Load(), "the handler must not run")
		assert.Empty(t, mr.Keys(), "a provider error must leave no record behind")
	})

	t.Run("routes_through_unavailable_handler", func(t *testing.T) {
		t.Parallel()

		mr := miniredis.RunT(t)

		var called atomic.Int32

		m := New(newRedisClient(t, mr), failing,
			WithUnavailableHandler(func(c fiber.Ctx) error {
				return c.SendStatus(fiber.StatusTeapot)
			}),
		)

		resp := postBody(t, uploadApp(m.Check(), &called), "k1",
			fiber.MIMEApplicationJSON, []byte(`{"amount":10}`))
		resp.Body.Close()

		assert.Equal(t, http.StatusTeapot, resp.StatusCode)
		assert.Equal(t, int32(0), called.Load())
		assert.Empty(t, mr.Keys())
	})
}

// TestFingerprintProvider_ScopeStillNamespaces proves the two providers
// compose: the provider supplies the identity bytes, the scope provider still
// namespaces the digest built from them.
func TestFingerprintProvider_ScopeStillNamespaces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		secondUser string
		wantStatus int
		wantRuns   int32
	}{
		{name: "same_scope_replays", secondUser: "alice", wantStatus: http.StatusCreated, wantRuns: 1},
		{name: "different_scope_is_reuse", secondUser: "bob", wantStatus: http.StatusUnprocessableEntity, wantRuns: 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			conn := newRedisClient(t, miniredis.RunT(t))

			var called atomic.Int32

			m := New(conn,
				WithFingerprintProvider(func(fiber.Ctx) ([]byte, error) {
					return []byte("stable-identity"), nil
				}),
				WithFingerprintScopeProvider(func(c fiber.Ctx) string {
					return c.Get("X-User")
				}),
			)

			app := uploadApp(m.Check(), &called)

			first := httptest.NewRequest(http.MethodPost, "/test", nil)
			first.Header.Set(chttp.IdempotencyKey, "k1")
			first.Header.Set("X-User", "alice")
			firstResp, err := app.Test(first, fiber.TestConfig{Timeout: 0})
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, firstResp.StatusCode)
			firstResp.Body.Close()

			second := httptest.NewRequest(http.MethodPost, "/test", nil)
			second.Header.Set(chttp.IdempotencyKey, "k1")
			second.Header.Set("X-User", testCase.secondUser)
			secondResp, err := app.Test(second, fiber.TestConfig{Timeout: 0})
			require.NoError(t, err)
			secondResp.Body.Close()

			assert.Equal(t, testCase.wantStatus, secondResp.StatusCode)
			assert.Equal(t, testCase.wantRuns, called.Load())
		})
	}
}

// TestWithFingerprintProvider_NilIsIgnored matches every other option in the
// package: a nil argument leaves the shipped raw-body fingerprint in place
// rather than installing a provider that would panic on the first request.
func TestWithFingerprintProvider_NilIsIgnored(t *testing.T) {
	t.Parallel()

	conn := newRedisClient(t, miniredis.RunT(t))

	var called atomic.Int32

	app := uploadApp(New(conn, WithFingerprintProvider(nil)).Check(), &called)

	postBody(t, app, "k1", fiber.MIMEApplicationJSON, []byte(`{"amount":10}`)).Body.Close()

	resp := postBody(t, app, "k1", fiber.MIMEApplicationJSON, []byte(`{"amount":99}`))

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"without a provider a different body under the same key is still reuse")
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSE", decodeErrorBody(t, resp).Title)
	assert.Equal(t, int32(1), called.Load())
}

// TestFingerprintProvider_DifferentIdentityIsReuse pins the direction every
// other provider test leaves open: the provider's bytes must actually REACH the
// digest. Without this, a refactor that called the provider for its error and
// then dropped its result would keep the whole suite green, and a client
// uploading june.csv and then july.csv under one key would be answered with the
// first upload's receipt without the handler ever running — a statement file
// silently discarded and reported as ingested.
//
// The raw body is identical in both requests, so the provider's bytes are the
// only thing that can tell them apart.
func TestFingerprintProvider_DifferentIdentityIsReuse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		secondUpload string
		wantStatus   int
	}{
		{name: "same_identity_replays", secondUpload: "june.csv", wantStatus: http.StatusCreated},
		{name: "different_identity_is_reuse", secondUpload: "july.csv", wantStatus: http.StatusUnprocessableEntity},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			conn := newRedisClient(t, miniredis.RunT(t))

			var called atomic.Int32

			m := New(conn, WithFingerprintProvider(func(c fiber.Ctx) ([]byte, error) {
				return []byte(c.Get("X-Upload")), nil
			}))

			app := uploadApp(m.Check(), &called)

			upload := func(filename string) *http.Response {
				req := httptest.NewRequest(http.MethodPost, "/test",
					strings.NewReader(`{"ledger":"main"}`))
				req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
				req.Header.Set(chttp.IdempotencyKey, "k1")
				req.Header.Set("X-Upload", filename)

				resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
				require.NoError(t, err)

				return resp
			}

			first := upload("june.csv")
			require.Equal(t, http.StatusCreated, first.StatusCode)
			require.NoError(t, first.Body.Close())

			second := upload(testCase.secondUpload)
			defer second.Body.Close()

			assert.Equal(t, testCase.wantStatus, second.StatusCode)
			assert.Equal(t, int32(1), called.Load(),
				"the handler runs once whichever way the second request is answered")
		})
	}
}

// TestFingerprintProvider_SlowProviderIsNotChargedToTheStoreDeadline pins whose
// clock the provider runs on.
//
// The provider is the application's code and it reads the request: a multipart
// identity walks the parts, a streamed upload reads its manifest. That work
// belongs to the request, not to the store — but the middleware used to open the
// store's deadline before calling it, so a provider slower than
// [WithRedisTimeout] left nothing of the budget for the first store call. That
// call then failed on a perfectly healthy store, and under the fail-open default
// that did what it exists to do: it ran the mutation UNPROTECTED. Every request
// on the route took that path, so no key was ever held and a duplicate executed
// again.
//
// The assertion is on the budget REMAINING at Acquire, not on the request
// outrunning a timeout. The provider's delay is a guaranteed floor, so a
// provider charged to the budget loses at least that much of it; a version that
// merely hoped to outrun the timeout was measuring machine load instead, and
// went red in a loaded parallel race suite while the code was correct.
func TestFingerprintProvider_SlowProviderIsNotChargedToTheStoreDeadline(t *testing.T) {
	t.Parallel()

	const (
		budget       = time.Second
		providerWork = 200 * time.Millisecond
	)

	base, _ := realRedisStore(t)
	// Two requests, so two acquisitions. The spare slot turns an unexpected
	// third into a failed length assertion instead of a blocked send that would
	// hang the whole package until the test binary times out.
	store := budgetStore{Store: base, remaining: make(chan time.Duration, 3)}

	m := NewWithStore(store,
		WithRedisTimeout(budget),
		WithFingerprintProvider(func(fiber.Ctx) ([]byte, error) {
			// Not CPU work: this stands in for reading the request's identity,
			// which is I/O the store's budget must not be charged for.
			time.Sleep(providerWork)

			return []byte("upload:june.csv:4096"), nil
		}),
	)

	var calls atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("t1"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(fiber.StatusCreated).SendString(`{"id":"9f1c"}`)
	})

	first := postBody(t, app, "slow-provider-key", fiber.MIMEApplicationJSON, []byte(`{"amount":"1250.00"}`))
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBody(t, app, "slow-provider-key", fiber.MIMEApplicationJSON, []byte(`{"amount":"1250.00"}`))
	body := readBody(t, second)

	require.Len(t, store.remaining, 2, "one acquisition per request, and no more")

	for attempt := 1; attempt <= 2; attempt++ {
		assert.Greater(t, <-store.remaining, budget-providerWork/2,
			"the provider's own work must not come out of the store's budget (request %d)", attempt)
	}

	assert.Equal(t, int32(1), calls.Load(),
		"the store was healthy and the key was held, so the duplicate must not reach the handler")
	assert.Equal(t, http.StatusCreated, second.StatusCode)
	assert.JSONEq(t, `{"id":"9f1c"}`, body)
	assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed),
		"the first request left a record, which is what proves its store call was never timed out")
}

// keepAliveConn drives raw HTTP/1.1 over one connection at a time, the way a
// pooled client does: it reuses the connection until the server says
// "Connection: close", and dials again only then.
//
// It is the only instrument that can see the defect below. app.Test is a
// one-shot in-memory conn with no reuse at all, and net/http's Transport
// silently retries a POST with a rewindable body onto a fresh connection — which
// turns a corrupted connection into a green test.
type keepAliveConn struct {
	t     *testing.T
	addr  string
	conn  net.Conn
	br    *bufio.Reader
	dials int
}

func newKeepAliveConn(t *testing.T, addr string) *keepAliveConn {
	t.Helper()

	k := &keepAliveConn{t: t, addr: addr}
	t.Cleanup(k.close)

	return k
}

func (k *keepAliveConn) close() {
	if k.conn != nil {
		_ = k.conn.Close()
		k.conn = nil
	}
}

// post sends one request under a declared Content-Length and reads its
// response. It returns the status, the replayed marker and whether the server
// retired the connection.
func (k *keepAliveConn) post(key string, body []byte) (status int, replayed string, retired bool) {
	k.t.Helper()

	head := fmt.Sprintf("POST /test HTTP/1.1\r\nHost: idempotency.test\r\n%s: %s\r\n"+
		"Content-Type: %s\r\nContent-Length: %d\r\n\r\n",
		chttp.IdempotencyKey, key, fiber.MIMEOctetStream, len(body))

	return k.roundTrip(append([]byte(head), body...))
}

// postChunked sends the same request with no Content-Length at all, framing the
// body as a single HTTP/1.1 chunk.
//
// That is the framing the declared-length pre-read can never produce: fasthttp
// reports ContentLength() == -1 and readBodyWithStreaming refuses chunked
// outright, so NOTHING is lifted out of the connection before the stream is
// handed over. Size cannot make such a body harmless the way a small declared
// length does.
func (k *keepAliveConn) postChunked(key string, body []byte) (status int, replayed string, retired bool) {
	k.t.Helper()

	wire := []byte(fmt.Sprintf("POST /test HTTP/1.1\r\nHost: idempotency.test\r\n%s: %s\r\n"+
		"Content-Type: %s\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n",
		chttp.IdempotencyKey, key, fiber.MIMEOctetStream, len(body)))
	wire = append(wire, body...)
	wire = append(wire, "\r\n0\r\n\r\n"...)

	return k.roundTrip(wire)
}

// roundTrip writes one already-framed request on the pooled connection, dialling
// again only if the previous answer retired it, and reads the response back.
func (k *keepAliveConn) roundTrip(wire []byte) (status int, replayed string, retired bool) {
	k.t.Helper()

	if k.conn == nil {
		conn, err := net.Dial("tcp", k.addr)
		require.NoError(k.t, err)

		k.conn, k.br, k.dials = conn, bufio.NewReader(conn), k.dials+1
	}

	require.NoError(k.t, k.conn.SetDeadline(time.Now().Add(10*time.Second)))

	_, err := k.conn.Write(wire)
	require.NoError(k.t, err, "the connection died before this request was even sent")

	resp, err := http.ReadResponse(k.br, nil)
	require.NoError(k.t, err, "the connection died before this request was answered")

	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(k.t, err)
	require.NoError(k.t, resp.Body.Close())

	if resp.Close {
		k.close()
	}

	return resp.StatusCode, resp.Header.Get(chttp.IdempotencyReplayed), resp.Close
}

// serveStreamProbe starts app on a real TCP listener, which app.Test cannot
// stand in for: the defect below lives in how fasthttp parses the NEXT request
// off a connection this one left unread.
func serveStreamProbe(t *testing.T, app *fiber.App) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	listenerErr := make(chan error, 1)
	go func() { listenerErr <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()

	t.Cleanup(func() {
		require.NoError(t, app.Shutdown())
		require.NoError(t, <-listenerErr)
	})

	return ln.Addr().String()
}

// TestFingerprintProvider_StreamedDuplicate_LeavesTheConnectionUsable is the
// other half of the streamed-body contract, and the one the provider broke.
//
// A provider exists so the middleware never calls c.Body(). But a duplicate is
// answered WITHOUT running the handler, so on that request nobody reads the
// upload at all — and fasthttp recycles the stream struct without draining the
// connection reader. The next request on a keep-alive connection is then parsed
// from the middle of this one's 70 KiB body, and the server resets it: measured
// before the fix, the client's third POST failed with "connection reset by peer"
// while the same three requests without a provider all answered. A client
// retrying a large upload under the same key — the published contract — got its
// replay and lost the pooled connection, and whatever request the pool
// multiplexed onto it next died as a network error.
//
// The middleware retires the connection instead: "Connection: close" is what
// net/http sends when a handler leaves a body unread, and what a client's pool
// understands. Draining would mean reading a gigabyte to answer a 409.
func TestFingerprintProvider_StreamedDuplicate_LeavesTheConnectionUsable(t *testing.T) {
	t.Parallel()

	const (
		bodySize = 70 << 10
		key      = "streamed-upload-key"
	)

	body := bytes.Repeat([]byte("x"), bodySize)

	newApp := func(t *testing.T, opts ...Option) (*fiber.App, *atomic.Int32) {
		t.Helper()

		m := New(newRedisClient(t, miniredis.RunT(t)), opts...)

		var called atomic.Int32

		app := fiber.New(fiber.Config{StreamRequestBody: true})
		app.Use(tenantMiddleware("t1"))
		app.Use(m.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			called.Add(1)

			if c.Request().IsBodyStream() {
				if _, err := io.Copy(io.Discard, c.Request().BodyStream()); err != nil {
					return err
				}
			}

			return c.Status(fiber.StatusCreated).SendString("ok")
		})

		return app, &called
	}

	t.Run("with_provider_every_duplicate_is_still_answered", func(t *testing.T) {
		t.Parallel()

		app, called := newApp(t, WithFingerprintProvider(func(c fiber.Ctx) ([]byte, error) {
			return []byte(c.Get(fiber.HeaderContentLength)), nil
		}))

		client := newKeepAliveConn(t, serveStreamProbe(t, app))

		status, replayed, retired := client.post(key, body)
		require.Equal(t, http.StatusCreated, status)
		assert.Empty(t, replayed, "the first request runs the handler")
		assert.False(t, retired,
			"the handler ran and owns the body: a healthy connection must not be thrown away")

		for i := 2; i <= 3; i++ {
			status, replayed, retired = client.post(key, body)

			require.Equal(t, http.StatusCreated, status, "duplicate %d must be answered", i)
			assert.Equal(t, "true", replayed, "duplicate %d must be a replay", i)
			assert.True(t, retired,
				"duplicate %d answered without running the handler, so its body was never read; "+
					"keeping the connection leaves the next request parsed from the middle of it", i)
		}

		assert.Equal(t, int32(1), called.Load(), "the upload must be handled exactly once")
		assert.Equal(t, 2, client.dials,
			"requests 1 and 2 share the first connection; only the one request 2 retired is redialled, "+
				"and no request is lost to it")
	})

	t.Run("a_re_seated_provider_keeps_the_connection", func(t *testing.T) {
		t.Parallel()

		// The provider that drains the wire and hands the bytes back is the
		// shape a service reaches for when the fingerprint must cover the
		// payload: read the stream once, re-seat it so the handler still finds
		// one. Nothing is left in the connection after it runs — every byte is
		// in memory — so retiring the connection on the duplicates buys
		// nothing and costs a handshake per retry.
		app, called := newApp(t, WithFingerprintProvider(func(c fiber.Ctx) ([]byte, error) {
			raw, err := io.ReadAll(c.Request().BodyStream())
			if err != nil {
				return nil, err
			}

			c.Request().SetBodyStream(bytes.NewReader(raw), len(raw))

			return raw, nil
		}))

		client := newKeepAliveConn(t, serveStreamProbe(t, app))

		for i := 1; i <= 3; i++ {
			status, _, retired := client.post(key, body)

			require.Equal(t, http.StatusCreated, status, "request %d must be answered", i)
			assert.False(t, retired,
				"the provider read the whole body into memory and re-seated it as a rewindable "+
					"reader, so request %d holds nothing in the connection", i)
		}

		assert.Equal(t, int32(1), called.Load(), "the upload must be handled exactly once")
		assert.Equal(t, 1, client.dials, "one connection must carry all three requests")
	})

	t.Run("without_provider_the_connection_is_reused", func(t *testing.T) {
		t.Parallel()

		app, called := newApp(t)

		client := newKeepAliveConn(t, serveStreamProbe(t, app))

		for i := 1; i <= 3; i++ {
			status, _, retired := client.post(key, body)

			require.Equal(t, http.StatusCreated, status, "request %d must be answered", i)
			assert.False(t, retired,
				"without a provider the middleware reads the body itself, so nothing is left "+
					"unread and request %d must not cost the connection", i)
		}

		assert.Equal(t, int32(1), called.Load())
		assert.Equal(t, 1, client.dials, "one connection must carry all three requests")
	})
}

// TestRefusal_StreamedBodyUnread_RetiresTheConnection is the other half of the
// same guard, and the half that fires with no provider configured anywhere.
//
// The deferred retirement is registered at the TOP of handle(), before the
// refusals that return without ever reaching resolveFingerprint — the only site
// that calls c.Body() and so the only thing that drains a streamed request. An
// over-length key is one of those refusals: on a StreamRequestBody route the
// 400 leaves 70 KiB sitting unread in the connection, and the next request on it
// is parsed from the middle of that body. The guard is therefore right to fire
// with no provider in sight, and narrowing it to the provider would put the same
// reset back on every refused upload.
//
// The chunked subtests pin the other framing. The guard decides on
// Content-Length, and a chunked upload declares none: fasthttp reports -1 and
// pre-reads nothing, so ten bytes are as unread as 200 KB. Read as a number
// rather than as "no declared length", that -1 sorts below every threshold and
// the guard would keep exactly the connections it exists to retire.
func TestRefusal_StreamedBodyUnread_RetiresTheConnection(t *testing.T) {
	t.Parallel()

	newProbe := func(t *testing.T) (*keepAliveConn, *atomic.Int32) {
		t.Helper()

		m := New(newRedisClient(t, miniredis.RunT(t)), WithMaxKeyLength(8))

		var called atomic.Int32

		app := fiber.New(fiber.Config{StreamRequestBody: true})
		app.Use(tenantMiddleware("t1"))
		app.Use(m.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			called.Add(1)

			if c.Request().IsBodyStream() {
				if _, err := io.Copy(io.Discard, c.Request().BodyStream()); err != nil {
					return err
				}
			}

			return c.Status(fiber.StatusCreated).SendString("ok")
		})

		return newKeepAliveConn(t, serveStreamProbe(t, app)), &called
	}

	t.Run("declared_length_past_the_pre_read", func(t *testing.T) {
		t.Parallel()

		client, called := newProbe(t)
		body := bytes.Repeat([]byte("x"), 70<<10)

		status, replayed, retired := client.post("this-key-is-far-too-long", body)
		require.Equal(t, http.StatusBadRequest, status, "the key is longer than WithMaxKeyLength(8)")
		assert.Empty(t, replayed, "nothing was replayed: the request was refused before any record was read")
		assert.True(t, retired,
			"the refusal answered without running the handler, so the upload was never read; "+
				"keeping the connection leaves the next request parsed from the middle of it")

		status, replayed, retired = client.post("short", body)
		require.Equal(t, http.StatusCreated, status,
			"the client reconnects and its next request is answered: the refusal costs a connection, never a request")
		assert.Empty(t, replayed, "a fresh key on a fresh connection is not a duplicate")
		assert.False(t, retired, "the handler ran and drained the body, so this connection stays usable")

		assert.Equal(t, int32(1), called.Load(), "the refused upload must never reach the handler")
		assert.Equal(t, 2, client.dials,
			"one dial for the refused request and one for the request that follows it")
	})

	// A chunked upload — what a client streaming an export or a multipart file
	// of unknown length actually sends — declares no length at all, and fasthttp
	// pre-reads none of it: the whole body is still in the connection when the
	// refusal answers, at ten bytes exactly as much as at 200 KB. The guard
	// reads Content-Length to decide, so it MUST treat the -1 that chunked
	// reports as "nothing was buffered" and never as "small enough to keep":
	// held by size alone, a ten-byte chunked body looks like the harmless case
	// and is the opposite of it.
	for _, bodySize := range []int{10, 200 << 10} {
		t.Run(fmt.Sprintf("chunked_%d_bytes", bodySize), func(t *testing.T) {
			t.Parallel()

			client, called := newProbe(t)

			status, replayed, retired := client.postChunked(
				"this-key-is-far-too-long", bytes.Repeat([]byte("x"), bodySize))
			require.Equal(t, http.StatusBadRequest, status, "the key is longer than WithMaxKeyLength(8)")
			assert.Empty(t, replayed, "nothing was replayed: the request was refused before any record was read")
			assert.True(t, retired,
				"no byte of a chunked body is pre-read, so this %d-byte refusal left all of it in the "+
					"connection: the next request on it is parsed from the chunk framing", bodySize)

			status, replayed, retired = client.post("short", []byte("small"))
			require.Equal(t, http.StatusCreated, status,
				"the client reconnects and its next request is answered by the app: the refusal costs a "+
					"connection, never a request")
			assert.Empty(t, replayed, "a fresh key on a fresh connection is not a duplicate")
			assert.False(t, retired, "the handler ran and drained the body, so this connection stays usable")

			assert.Equal(t, int32(1), called.Load(), "the refused upload must never reach the handler")
			assert.Equal(t, 2, client.dials,
				"one dial for the refused chunked request and one for the request that follows it")
		})
	}
}

// TestRefusal_StreamedBodyBuffered_KeepsTheConnection walks the boundary that
// decides whether an unread stream has actually left anything in the connection.
//
// "IsBodyStream" is not that question. fasthttp copies min(bodyLimit,
// Content-Length, 8 KiB) of a declared-length body out of the connection BEFORE
// it hands over a stream (readBodyWithStreaming), and then hands over a stream
// whatever it copied — so a body at or under that bound reports as a stream with
// an empty reader behind it. Retiring there threw away a connection that was
// provably clean: since Fiber's StreamRequestBody is an APP-WIDE setting, a
// service that turns it on for its one large-upload route charged a mobile
// client a fresh TCP (and TLS) handshake for every duplicate of a 200-byte JSON
// mutation, and turned a retry storm into connection churn at its pool.
//
// The oracle here is the requests that FOLLOW a kept connection, not the header:
// if fasthttp stopped pre-reading the whole of an 8192-byte body, the next
// request on the socket would be parsed from the leftovers and this test fails
// loudly — which is the point of walking the boundary rather than trusting a
// constant nobody exported.
func TestRefusal_StreamedBodyBuffered_KeepsTheConnection(t *testing.T) {
	t.Parallel()

	newProbe := func(t *testing.T, bodyLimit int) (*keepAliveConn, *atomic.Int32) {
		t.Helper()

		m := New(newRedisClient(t, miniredis.RunT(t)), WithMaxKeyLength(8))

		var called atomic.Int32

		app := fiber.New(fiber.Config{StreamRequestBody: true, BodyLimit: bodyLimit})
		app.Use(tenantMiddleware("t1"))
		app.Use(m.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			called.Add(1)

			if c.Request().IsBodyStream() {
				if _, err := io.Copy(io.Discard, c.Request().BodyStream()); err != nil {
					return err
				}
			}

			return c.Status(fiber.StatusCreated).SendString("ok")
		})

		return newKeepAliveConn(t, serveStreamProbe(t, app)), &called
	}

	// With the default body limit (4 MiB) the 8 KiB ceiling is the binding one,
	// so the boundary sits where fasthttp stops copying.
	t.Run("bounded_by_the_8k_pre_read", func(t *testing.T) {
		t.Parallel()

		client, called := newProbe(t, fiber.DefaultBodyLimit)

		// Every one of these is fully buffered out of the connection before the
		// handler chain even starts, so the refusal leaves nothing behind.
		for _, bodySize := range []int{0, 200, 8 << 10} {
			status, _, retired := client.post("this-key-is-far-too-long", bytes.Repeat([]byte("x"), bodySize))

			require.Equal(t, http.StatusBadRequest, status,
				"the %d-byte request is refused for its over-length key", bodySize)
			assert.False(t, retired,
				"fasthttp already copied the whole %d-byte body out of the connection, so this refusal "+
					"read nothing only because there was nothing left to read: closing here costs the "+
					"client a handshake per duplicate and protects nothing", bodySize)
		}

		// One byte past the pre-read: the remainder really is sitting in the
		// connection, and the next request would be parsed from the middle of it.
		status, _, retired := client.post("this-key-is-far-too-long", bytes.Repeat([]byte("x"), (8<<10)+1))
		require.Equal(t, http.StatusBadRequest, status)
		assert.True(t, retired,
			"one byte past what fasthttp buffers, the refusal leaves an unread remainder in the connection")

		status, _, retired = client.post("short", []byte("small"))
		require.Equal(t, http.StatusCreated, status,
			"the client reconnects and its next request is answered: a retirement costs a connection, never a request")
		assert.False(t, retired, "the handler ran and owns the body")

		assert.Equal(t, int32(1), called.Load(), "only the well-formed request reaches the handler")
		assert.Equal(t, 2, client.dials,
			"one connection carries all four refusals; only the one that left a remainder is redialled")
	})

	// An app whose body limit is BELOW the 8 KiB ceiling moves the boundary down
	// to the limit: fasthttp copies min(bodyLimit, Content-Length, 8 KiB), so on
	// a 4 KiB route a 4097-byte body is a stream with exactly one byte still in
	// the connection. A guard that stopped at the 8 KiB ceiling would read 4097
	// as "buffered", keep the socket, and hand the next request that leftover
	// byte — which is why the limit is a clause of its own and not a comment.
	t.Run("bounded_by_a_smaller_body_limit", func(t *testing.T) {
		t.Parallel()

		const bodyLimit = 4096

		client, called := newProbe(t, bodyLimit)

		status, _, retired := client.post("this-key-is-far-too-long", bytes.Repeat([]byte("x"), bodyLimit))
		require.Equal(t, http.StatusBadRequest, status,
			"the %d-byte request is refused for its over-length key", bodyLimit)
		assert.False(t, retired,
			"a body exactly at the route's limit is copied out whole, so this refusal left nothing behind")

		status, _, retired = client.post("this-key-is-far-too-long", bytes.Repeat([]byte("x"), bodyLimit+1))
		require.Equal(t, http.StatusBadRequest, status,
			"the %d-byte request is refused for its over-length key, not for its size", bodyLimit+1)
		assert.True(t, retired,
			"one byte past the route's limit, fasthttp stopped copying at %d and that byte is still in "+
				"the connection: the next request on it is parsed starting from it", bodyLimit)

		status, _, retired = client.post("short", []byte("small"))
		require.Equal(t, http.StatusCreated, status,
			"the client reconnects and its next request is answered by the app: a retirement costs a "+
				"connection, never a request")
		assert.False(t, retired, "the handler ran and owns the body")

		assert.Equal(t, int32(1), called.Load(), "only the well-formed request reaches the handler")
		assert.Equal(t, 2, client.dials,
			"both refusals share the first connection; only the one that left a remainder is redialled")
	})
}
