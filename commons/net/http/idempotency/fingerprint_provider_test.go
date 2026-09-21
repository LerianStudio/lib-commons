//go:build unit

package idempotency

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

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
