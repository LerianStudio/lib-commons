//go:build unit

package ratelimit

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	tmcore "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	observability "github.com/LerianStudio/lib-observability/v4"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusalCase builds an app whose only answer is one of the limiter's non-429 refusals.
type refusalCase struct {
	name     string
	status   int
	flatBody string
	build    func(t *testing.T, opts ...Option) *fiber.App
}

var refusalCases = []refusalCase{
	{
		name:     "503 redis down while fail-closed",
		status:   http.StatusServiceUnavailable,
		flatBody: `{"code":503,"title":"service_unavailable","message":"rate limiter temporarily unavailable"}`,
		build: func(t *testing.T, opts ...Option) *fiber.App {
			mr := miniredis.RunT(t)
			rl := New(newTestMiddlewareRedisConnection(t, mr), append(opts, WithFailOpen(false))...)
			mr.Close()

			return refusalApp(nil, rl.WithRateLimit(Tier{Name: "down", Max: 10, Window: time.Minute}))
		},
	},
	{
		name:     "500 misconfigured tier",
		status:   http.StatusInternalServerError,
		flatBody: `{"code":500,"title":"misconfigured_rate_limiter","message":"rate limiter tier max is not positive; contact the service operator"}`,
		build: func(t *testing.T, opts ...Option) *fiber.App {
			rl := New(newTestMiddlewareRedisConnection(t, miniredis.RunT(t)), opts...)

			return refusalApp(nil, rl.WithRateLimit(Tier{Name: "zero", Max: 0, Window: time.Minute}))
		},
	},
	{
		name:     "400 invalid tenant context",
		status:   http.StatusBadRequest,
		flatBody: `{"code":400,"title":"invalid_tenant_context","message":"tenant context is invalid"}`,
		build: func(t *testing.T, opts ...Option) *fiber.App {
			rl := New(newTestMiddlewareRedisConnection(t, miniredis.RunT(t)), opts...)
			badTenant := func(c fiber.Ctx) error {
				c.SetContext(tmcore.ContextWithTenantID(c.Context(), "tenant:raw-secret"))
				return c.Next()
			}

			return refusalApp(badTenant, rl.WithRateLimit(Tier{Name: "tenant", Max: 10, Window: time.Minute}))
		},
	},
}

// refusalApp mounts the cause slot WithHTTPLogging installs, then the limiter. The recorded
// cause travels back on a response header so each request is checked on its own.
func refusalApp(before, limiter fiber.Handler) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		ctx, cause := observability.ContextWithErrorCause(c.Context())
		c.SetContext(ctx)

		err := c.Next()
		if recorded := cause(); recorded != nil {
			c.Set("X-Recorded-Cause", recorded.Error())
		}

		return err
	})

	if before != nil {
		app.Use(before)
	}

	app.Use(limiter)
	app.Get("/", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })

	return app
}

func doRefusal(t *testing.T, app *fiber.App) (*http.Response, string) {
	t.Helper()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp, string(body)
}

func TestRefusal_UnconfiguredKeepsFlatEnvelopeByteForByte(t *testing.T) {
	t.Parallel()

	for _, tc := range refusalCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp, body := doRefusal(t, tc.build(t))

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, fiber.MIMEApplicationJSONCharsetUTF8, resp.Header.Get(fiber.HeaderContentType))
			assert.Equal(t, tc.flatBody, body)
			assert.Equal(t, tc.status >= http.StatusInternalServerError, resp.Header.Get("X-Recorded-Cause") != "",
				"a 5xx cause reaches the access line; a 4xx records none")
		})
	}
}

func TestRefusal_HandlerWritesProblemJSONWithCause(t *testing.T) {
	t.Parallel()

	for _, tc := range refusalCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotStatus int

			var gotCause error

			app := tc.build(t, WithRefusalHandler(func(c fiber.Ctx, status int, cause error) error {
				gotStatus, gotCause = status, cause
				c.Set(fiber.HeaderContentType, "application/problem+json")

				return c.Status(status).SendString(`{"status":` + strconv.Itoa(status) + `}`)
			}))

			resp, body := doRefusal(t, app)

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, "application/problem+json", resp.Header.Get(fiber.HeaderContentType))
			assert.Equal(t, `{"status":`+strconv.Itoa(tc.status)+`}`, body)
			assert.Equal(t, tc.status, gotStatus)
			require.Error(t, gotCause)

			if tc.status >= http.StatusInternalServerError {
				assert.Equal(t, gotCause.Error(), resp.Header.Get("X-Recorded-Cause"))
			} else {
				assert.True(t, errors.Is(gotCause, ErrInvalidTenantContext))
				assert.Empty(t, resp.Header.Get("X-Recorded-Cause"))
			}
		})
	}
}
