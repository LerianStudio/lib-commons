//go:build unit

package idempotency

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/helmet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOrigin is an allowed, non-wildcard origin: cors only emits Vary and a
// concrete Access-Control-Allow-Origin when the allow-list is not "*".
const testOrigin = "https://app.example.com"

// postWithOrigin sends POST /test carrying an Origin, so the globally mounted
// cors middleware treats it as a CORS request on both the live and the replayed
// call — which is the whole point: those headers are on the response BEFORE the
// idempotency middleware runs, and again inside the captured set.
func postWithOrigin(t *testing.T, app *fiber.App, key string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set(fiber.HeaderOrigin, testOrigin)

	if key != "" {
		req.Header.Set(chttp.IdempotencyKey, key)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	return resp
}

// TestReplay_GlobalHeaderMiddleware_NoDuplicatedHeaders pins the shape every
// service that mounts cors and helmet with app.Use actually deploys: those
// headers are already on the response when the idempotency middleware replays,
// and they are also inside the captured set. A browser refuses a CORS response
// whose Access-Control-Allow-Origin "contains multiple values", so a duplicated
// header turns a double-clicked mutation that SUCCEEDED into a reported network
// failure.
func TestReplay_GlobalHeaderMiddleware_NoDuplicatedHeaders(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var called atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-headers"))
	app.Use(cors.New(cors.Config{AllowOrigins: []string{testOrigin}}))
	app.Use(helmet.New())
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		called.Add(1)

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := postWithOrigin(t, app, "cors-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)

	resp2 := postWithOrigin(t, app, "cors-key")
	readBody(t, resp2)

	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed),
		"the second request must be a replay, not a re-execution")
	require.Equal(t, int32(1), called.Load(), "the handler must have run exactly once")

	for name, want := range map[string]string{
		fiber.HeaderAccessControlAllowOrigin: testOrigin,
		fiber.HeaderXFrameOptions:            "SAMEORIGIN",
		fiber.HeaderXContentTypeOptions:      "nosniff",
		fiber.HeaderVary:                     fiber.HeaderOrigin,
	} {
		values := resp2.Header.Values(name)
		assert.Equal(t, []string{want}, values,
			"replay must carry exactly one %s; a duplicate is what browsers reject", name)
	}
}

// TestReplay_MultiValuedCapturedHeader_KeepsEveryValue guards the fix from
// overcorrecting: clearing a captured name before re-applying it must not
// collapse a header the handler legitimately set twice.
func TestReplay_MultiValuedCapturedHeader_KeepsEveryValue(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-multi"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		c.Response().Header.Add("Link", `<https://api.example.com/next>; rel="next"`)
		c.Response().Header.Add("Link", `<https://api.example.com/last>; rel="last"`)

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "link-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Len(t, resp1.Header.Values("Link"), 2, "the live response must carry both Link values")

	resp2 := doPost(t, app, "link-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, []string{
		`<https://api.example.com/next>; rel="next"`,
		`<https://api.example.com/last>; rel="last"`,
	}, resp2.Header.Values("Link"), "a multi-valued captured header must replay whole, in order")
}

// TestReplay_UncapturedLiveHeader_Survives pins the blast radius of the fix: it
// clears only the names the capture owns. A header this request carries that
// was not in the capture — a per-request correlation id, a flag a later release
// started sending — is not the replay's to erase.
func TestReplay_UncapturedLiveHeader_Survives(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var requests atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-live"))
	app.Use(func(c fiber.Ctx) error {
		// Absent from the first response, so absent from the capture; present on
		// the replayed one.
		if requests.Add(1) > 1 {
			c.Set("X-Live-Only", "yes")
		}

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "live-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Empty(t, resp1.Header.Get("X-Live-Only"), "the capture must not contain X-Live-Only")

	resp2 := doPost(t, app, "live-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, "yes", resp2.Header.Get("X-Live-Only"),
		"a header the app set on this request but never captured must survive the replay")
}
