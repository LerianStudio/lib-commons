//go:build unit

package idempotency

import (
	"fmt"
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

// requestIDHeader is the name a requestid middleware writes. Spelled out rather
// than generated, so a failure names the value that reached the client.
const requestIDHeader = "X-Request-Id"

// postWithOrigin sends POST /test carrying an Origin, so the globally mounted
// cors middleware treats it as a CORS request on both the live and the replayed
// call — which is the whole point: those headers are back on the response, from
// the live middleware, before the replay re-applies anything.
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
// service that mounts cors and helmet with app.Use actually deploys, against
// the capture as it is since the capture narrowed to the handler's delta.
//
// cors and helmet write before c.Next(), so their names are in the pre-handler
// snapshot and NOT in the capture: on the replay they are live, written again by
// the same middleware on the duplicate, and the replay must leave them alone.
// The duplication risk therefore only exists on a name the handler itself
// touched — here the handler overrides helmet's X-Frame-Options — because that
// name IS captured and the live middleware sets it again above. Re-applying it
// without clearing first leaves two values, and a browser refuses a CORS
// response whose Access-Control-Allow-Origin "contains multiple values", so the
// same defect on a cors name turns a double-clicked mutation that SUCCEEDED
// into a reported network failure.
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

		// The one name this route shares with the middleware above it: helmet
		// has already written SAMEORIGIN, the handler overrides it so the
		// receipt can render in a partner iframe. It is the only name in this
		// app that is both captured and live on the replay.
		c.Set(fiber.HeaderXFrameOptions, "ALLOWALL")

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := postWithOrigin(t, app, "cors-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Equal(t, []string{"ALLOWALL"}, resp1.Header.Values(fiber.HeaderXFrameOptions),
		"the live response must carry the handler's override, not helmet's default")

	resp2 := postWithOrigin(t, app, "cors-key")
	readBody(t, resp2)

	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed),
		"the second request must be a replay, not a re-execution")
	require.Equal(t, int32(1), called.Load(), "the handler must have run exactly once")

	assert.Equal(t, []string{"ALLOWALL"}, resp2.Header.Values(fiber.HeaderXFrameOptions),
		"a captured name the middleware above sets again must be REPLACED on the replay, not appended to")

	for name, want := range map[string]string{
		fiber.HeaderAccessControlAllowOrigin: testOrigin,
		fiber.HeaderXContentTypeOptions:      "nosniff",
		fiber.HeaderVary:                     fiber.HeaderOrigin,
	} {
		values := resp2.Header.Values(name)
		assert.Equal(t, []string{want}, values,
			"%s is written above the middleware and never captured; the replay must leave the live value alone", name)
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

// TestReplay_LiveCookieFromOtherMiddleware_Survives pins that a cookie minted
// ABOVE this middleware reaches the client on a replay carrying THIS request's
// value, untouched.
//
// A csrf.New() or a session rotator mounted with app.Use runs on the duplicate
// too and mints a fresh token for it. That cookie is written before c.Next(),
// so it never enters the handler's delta and the capture never holds its name.
// The capture here does hold a Set-Cookie all the same — the handler's own
// session cookie — so the replay DOES clear under that name, and what keeps the
// live token alive is that the clearing is scoped to the cookie names the
// capture is re-applying. fasthttp's ResponseHeader.Del("Set-Cookie") empties
// the whole jar; measured with the DelCookie loop replaced by that Del, this
// test fails with both the csrf and the locale cookie gone from the replay, and
// the user's next mutation is refused as a CSRF failure.
//
// It mints a DIFFERENT token per request, which is what makes it an assertion:
// a version that minted the same bytes twice could not tell a surviving cookie
// from a reinstated one.
//
// What it does NOT pin is that a captured cookie is REPLACED rather than
// duplicated: no live cookie here shares a name with a captured one, so the
// clearing could be dropped entirely and every count below would still be 1.
// That half is TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated.
// Between them the two fence the branch from both sides — drop the clearing and
// the collision test goes red, widen it to the whole jar and this one does.
func TestReplay_LiveCookieFromOtherMiddleware_Survives(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var requests atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-cookies"))
	app.Use(func(c fiber.Ctx) error {
		// A fresh per-request token, exactly as a CSRF middleware mints one, and
		// a cookie that appears only on the replayed request, so the capture
		// cannot hold its name. The token VALUE differs per request: minting the
		// same bytes twice makes the overwrite invisible to the assertion.
		request := requests.Add(1)
		c.Response().Header.Add(fiber.HeaderSetCookie, fmt.Sprintf("csrf=token-%d; Path=/", request))

		if request > 1 {
			c.Response().Header.Add(fiber.HeaderSetCookie, "locale=pt; Path=/")
		}

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		c.Response().Header.Add(fiber.HeaderSetCookie, "session=captured; Path=/")

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "cookie-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Len(t, resp1.Header.Values(fiber.HeaderSetCookie), 2,
		"the first response carries the minted csrf token and the handler's session cookie")

	resp2 := doPost(t, app, "cookie-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))

	counts := make(map[string]int)
	values := make(map[string]string)

	for _, cookie := range resp2.Cookies() {
		counts[cookie.Name]++
		values[cookie.Name] = cookie.Value
	}

	assert.Equal(t, 1, counts["locale"],
		"a cookie this request minted that the capture never held must survive the replay")
	assert.Equal(t, 1, counts["session"], "the captured cookie replays exactly once")
	assert.Equal(t, 1, counts["csrf"],
		"the csrf cookie is minted above this middleware and left alone by the handler, so the delta "+
			"capture never holds it and the replay leaves this request's own token exactly as it is")
	assert.Equal(t, "token-2", values["csrf"],
		"the token minted on THIS request must reach the client; the captured one is stale and its next mutation is refused as a CSRF failure")
	assert.Equal(t, "captured", values["session"], "a cookie the handler minted is part of the receipt")
}

// TestReplay_LiveRequestIDFromAbove_Wins pins the difference between "the
// response" and "the handler's contribution". A requestid middleware mounted
// above mints a fresh correlation id per request; capturing the whole response
// puts the ORIGINAL request's id in the record, and a replay that replaces
// every captured name hands the duplicate an id that belongs to a different
// request — the one thing a correlation id must never do.
func TestReplay_LiveRequestIDFromAbove_Wins(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var requests atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-request-id"))
	app.Use(func(c fiber.Ctx) error {
		c.Set(requestIDHeader, fmt.Sprintf("req-%d", requests.Add(1)))

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "request-id-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Equal(t, "req-1", resp1.Header.Get(requestIDHeader))

	resp2 := doPost(t, app, "request-id-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, "req-2", resp2.Header.Get(requestIDHeader),
		"the replay must carry THIS request's correlation id, not the captured one")
}

// TestReplay_HandlerHeaders_ReplayByteIdentical is the other half: what the
// HANDLER set is the receipt, and a duplicate must receive it unchanged. The
// handler numbers what it sets, so a value from a second execution is visibly
// different from a replayed one.
func TestReplay_HandlerHeaders_ReplayByteIdentical(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var runs atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-handler-headers"))
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		run := runs.Add(1)
		c.Set(fiber.HeaderLocation, fmt.Sprintf("/jobs/%d", run))
		c.Response().Header.Add(fiber.HeaderSetCookie, fmt.Sprintf("session=s%d; Path=/", run))

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "handler-headers-key")
	readBody(t, resp1)
	require.Equal(t, "/jobs/1", resp1.Header.Get(fiber.HeaderLocation))

	resp2 := doPost(t, app, "handler-headers-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	require.Equal(t, int32(1), runs.Load(), "the handler must have run exactly once")
	assert.Equal(t, "/jobs/1", resp2.Header.Get(fiber.HeaderLocation),
		"a Location the handler set must replay byte-identical")

	cookies := map[string]string{}
	for _, cookie := range resp2.Cookies() {
		cookies[cookie.Name] = cookie.Value
	}

	assert.Equal(t, "s1", cookies["session"], "a cookie the handler minted must replay byte-identical")
}

// TestReplay_HandlerOverridesHeaderFromAbove_ReplaysHandlerValue guards the
// delta from the obvious wrong reading of it — "capture only names that were
// absent before the handler". A header helmet sets above and the handler
// deliberately overrides (Cache-Control on a receipt route) IS the handler's
// contribution, and the replay must carry the handler's value over the live one.
func TestReplay_HandlerOverridesHeaderFromAbove_ReplaysHandlerValue(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-override"))
	app.Use(func(c fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "no-cache")

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "no-store")

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "override-key")
	readBody(t, resp1)
	require.Equal(t, "no-store", resp1.Header.Get(fiber.HeaderCacheControl))

	resp2 := doPost(t, app, "override-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, []string{"no-store"}, resp2.Header.Values(fiber.HeaderCacheControl),
		"the handler's override is part of the receipt and must replace the live value exactly once")
}

// TestReplay_FullCaptureFromOlderVersion_ReplacesLiveHeaders pins the mixed
// version case. A record written by the version that captured the WHOLE
// response holds names this version would never capture. This version replays
// every stored name with REPLACE semantics; the version that wrote the record
// re-applied them with a bare Add and could hand the client a live header twice
// (doc.go, "The response capture narrowed"). What this test proves is the new
// behaviour over an old record: the stored value replaces the live value,
// exactly once. Such records self-heal within the retention window.
func TestReplay_FullCaptureFromOlderVersion_ReplacesLiveHeaders(t *testing.T) {
	t.Parallel()

	const tenant = "tenant-full-capture"

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var handlerRuns atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware(tenant))
	app.Use(func(c fiber.Ctx) error {
		c.Set(requestIDHeader, "req-live")

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		handlerRuns.Add(1)

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	seedStoreRecord(t, mr, "idempotency:"+tenant+":full-capture-key", storeRecord{
		State:       keyStateComplete,
		Fingerprint: requestFingerprint(http.MethodPost, "/test", nil),
		Owner:       "owner-old",
		Response: encodeCachedResponse(t, cachedResponse{
			StatusCode:  http.StatusCreated,
			ContentType: fiber.MIMEApplicationJSON,
			Body:        []byte(`{"status":"created"}`),
			Headers: map[string][]string{
				requestIDHeader:       {"req-captured"},
				fiber.HeaderLocation:  {"/jobs/7"},
				fiber.HeaderSetCookie: {"session=captured; Path=/"},
			},
		}),
	})

	resp := doPost(t, app, "full-capture-key")
	readBody(t, resp)

	require.Equal(t, "true", resp.Header.Get(chttp.IdempotencyReplayed))
	require.Equal(t, int32(0), handlerRuns.Load(), "a completed record must never re-execute the handler")
	assert.Equal(t, []string{"req-captured"}, resp.Header.Values(requestIDHeader),
		"the stored value replaces the live value exactly once; the writer's bare Add would have sent both")
	assert.Equal(t, "/jobs/7", resp.Header.Get(fiber.HeaderLocation))
}

// TestReplay_HandlerDeletesHeaderSetAbove_ReplayHasItGoneToo covers the
// authorship case the delta cannot see by looking only at what is live after
// the handler: a name the handler REMOVED.
//
// helmet mounted with app.Use sets X-Frame-Options on every response, and a
// route serving a receipt meant to render inside a partner iframe deletes it.
// The original response therefore carries no X-Frame-Options and the iframe
// renders. On the duplicate, helmet sets the header again above the middleware,
// and unless the capture records the deletion nothing clears it — so the replay
// of a response that ALLOWED framing arrives forbidding it, and the partner's
// page breaks on the retry and not on the first attempt.
func TestReplay_HandlerDeletesHeaderSetAbove_ReplayHasItGoneToo(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var runs atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-deleted-header"))
	app.Use(func(c fiber.Ctx) error {
		c.Set(fiber.HeaderXFrameOptions, "SAMEORIGIN")
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		runs.Add(1)
		c.Response().Header.Del(fiber.HeaderXFrameOptions)

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "deleted-header-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Empty(t, resp1.Header.Get(fiber.HeaderXFrameOptions),
		"the handler deleted it, so the original response never carried it")

	resp2 := doPost(t, app, "deleted-header-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	require.Equal(t, int32(1), runs.Load(), "the handler must have run exactly once")
	assert.Empty(t, resp2.Header.Get(fiber.HeaderXFrameOptions),
		"the replay must reproduce the response the handler wrote, including what it removed")
	assert.Equal(t, []string{"nosniff"}, resp2.Header.Values(fiber.HeaderXContentTypeOptions),
		"a header set above that the handler left alone still belongs to THIS request")
}

// TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated is the
// case the cookie-aware clearing exists for, and the only one that can fail if
// it is removed.
//
// fasthttp appends on Header.Add("Set-Cookie", …) and does not dedupe by cookie
// name, so a session rotator mounted above that mints session=live-N on the
// duplicate, on a route whose handler also mints session, would have the replay
// answer with TWO Set-Cookie headers both named session. Which one the browser
// keeps is unspecified, and one of the two is a session token belonging to a
// different request — a duplicated correlation id is cosmetic, this is not.
//
// Every other cookie assertion in this file mints names that never collide, so
// the DelCookie loop is invisible to them: deleting it leaves them all green.
func TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)
	m := New(conn)

	var requests, runs atomic.Int32

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-cookie-collision"))
	app.Use(func(c fiber.Ctx) error {
		// A session rotator: same cookie NAME the handler below uses, a fresh
		// value per request, mounted above the middleware so it runs on the
		// duplicate too.
		c.Cookie(&fiber.Cookie{Name: "session", Value: fmt.Sprintf("live-%d", requests.Add(1)), Path: "/"})

		return c.Next()
	})
	app.Use(m.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		runs.Add(1)
		c.Cookie(&fiber.Cookie{Name: "session", Value: "captured", Path: "/"})

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	resp1 := doPost(t, app, "cookie-collision-key")
	readBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.Len(t, resp1.Cookies(), 1, "the handler's cookie replaced the rotator's on the original response")
	require.Equal(t, "captured", resp1.Cookies()[0].Value)

	resp2 := doPost(t, app, "cookie-collision-key")
	readBody(t, resp2)

	require.Equal(t, "true", resp2.Header.Get(chttp.IdempotencyReplayed))
	require.Equal(t, int32(1), runs.Load(), "the handler must have run exactly once")

	var session []string

	for _, cookie := range resp2.Cookies() {
		if cookie.Name == "session" {
			session = append(session, cookie.Value)
		}
	}

	assert.Equal(t, []string{"captured"}, session,
		"exactly one session cookie, carrying the captured value: two would let the client keep a token from the other request")
}
