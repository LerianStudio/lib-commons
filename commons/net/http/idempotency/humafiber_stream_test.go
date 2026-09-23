//go:build unit

package idempotency

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHumaFiber_StreamedKeyedRequest_ReachesTheHandler is the consumer end of
// the same defect, measured through the adapter that found it rather than
// through a hand-written probe.
//
// humafiber's BodyReader() branches on the SERVER's StreamRequestBody setting,
// not on whether this particular request still has a stream: with streaming on
// it returns Request().BodyStream() and never falls back to c.Body(). So a
// middleware that read the body to fingerprint it handed huma a nil reader,
// huma read it as an empty body, and every KEYED request carrying a payload was
// answered 400 "request body is required" while the same request without a key
// succeeded — idempotency turning a working route off.
func TestHumaFiber_StreamedKeyedRequest_ReachesTheHandler(t *testing.T) {
	t.Parallel()

	type payload struct {
		Body struct {
			Name string `json:"name"`
		}
	}

	type created struct {
		Status int `json:"-"`
		Body   struct {
			Name string `json:"name"`
		}
	}

	var called atomic.Int32

	app := fiber.New(fiber.Config{StreamRequestBody: true})
	app.Use(tenantMiddleware("t1"))
	app.Use(New(newRedisClient(t, miniredis.RunT(t))).Check())

	api := humafiber.New(app, huma.DefaultConfig("test", "1.0.0"))
	huma.Post(api, "/test", func(_ context.Context, in *payload) (*created, error) {
		called.Add(1)

		out := &created{Status: http.StatusCreated}
		out.Body.Name = in.Body.Name

		return out, nil
	})

	post := func(t *testing.T, key string) *http.Response {
		t.Helper()

		req, err := http.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"upload"}`))
		require.NoError(t, err)
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

		if key != "" {
			req.Header.Set(chttp.IdempotencyKey, key)
		}

		resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
		require.NoError(t, err)

		t.Cleanup(func() { _ = resp.Body.Close() })

		return resp
	}

	require.Equal(t, http.StatusCreated, post(t, "").StatusCode,
		"the unkeyed request is the control: this route works without idempotency")

	assert.Equal(t, http.StatusCreated, post(t, "huma-key").StatusCode,
		"a keyed request carrying a body must reach the handler, not be refused as bodiless")

	assert.Equal(t, int32(2), called.Load())
}
