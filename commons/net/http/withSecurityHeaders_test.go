//go:build unit

package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultHSTSValue = "max-age=63072000; includeSubDomains"
	// testPeerAddr is the peer address fiber's app.Test reports for every
	// in-memory request (its test connection answers RemoteAddr with 0.0.0.0).
	testPeerAddr = "0.0.0.0"
)

func newSecurityHeadersApp(cfg fiber.Config, opts ...SecurityHeadersOption) *fiber.App {
	app := fiber.New(cfg)
	app.Use(WithSecurityHeaders(opts...))
	app.Get("/ok", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})
	app.Get("/fail", func(_ fiber.Ctx) error {
		return fiber.NewError(fiber.StatusInternalServerError, "boom")
	})

	return app
}

func doSecurityHeadersRequest(t *testing.T, app *fiber.App, path string, headers map[string]string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

func TestWithSecurityHeaders_DefaultsOnEveryResponse(t *testing.T) {
	t.Parallel()

	app := newSecurityHeadersApp(fiber.Config{})

	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/ok", fiber.StatusOK},
		{"/missing", fiber.StatusNotFound},
		{"/fail", fiber.StatusInternalServerError},
	} {
		resp := doSecurityHeadersRequest(t, app, tc.path, nil)

		assert.Equal(t, tc.status, resp.StatusCode, tc.path)
		assert.Equal(t, "nosniff", resp.Header.Get(fiber.HeaderXContentTypeOptions), tc.path)
		assert.Equal(t, "DENY", resp.Header.Get(fiber.HeaderXFrameOptions), tc.path)
		assert.Equal(t, "no-referrer", resp.Header.Get(fiber.HeaderReferrerPolicy), tc.path)
		assert.Equal(t, "default-src 'none'; frame-ancestors 'none'",
			resp.Header.Get(fiber.HeaderContentSecurityPolicy), tc.path)
		assert.Empty(t, resp.Header.Get(fiber.HeaderStrictTransportSecurity),
			"plain HTTP never receives HSTS: %s", tc.path)
	}
}

func TestWithSecurityHeaders_RouteOverrideWins(t *testing.T) {
	t.Parallel()

	const docsCSP = "default-src 'self'; script-src 'self' https://cdn.example.com"

	app := fiber.New()
	app.Use(WithSecurityHeaders())
	app.Get("/docs", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentSecurityPolicy, docsCSP)
		c.Set(fiber.HeaderXFrameOptions, "SAMEORIGIN")

		return c.SendString("docs")
	})

	resp := doSecurityHeadersRequest(t, app, "/docs", nil)

	assert.Equal(t, docsCSP, resp.Header.Get(fiber.HeaderContentSecurityPolicy))
	assert.Equal(t, "SAMEORIGIN", resp.Header.Get(fiber.HeaderXFrameOptions))
	assert.Equal(t, "nosniff", resp.Header.Get(fiber.HeaderXContentTypeOptions))
}

func TestWithSecurityHeaders_EmptyOptionOmitsHeader(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		opt    SecurityHeadersOption
		header string
	}{
		{"csp", WithContentSecurityPolicy(""), fiber.HeaderContentSecurityPolicy},
		{"frame options", WithFrameOptions(""), fiber.HeaderXFrameOptions},
		{"referrer policy", WithReferrerPolicy(""), fiber.HeaderReferrerPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := doSecurityHeadersRequest(t, newSecurityHeadersApp(fiber.Config{}, tc.opt), "/ok", nil)

			_, present := resp.Header[tc.header]
			assert.False(t, present, "%s must be omitted", tc.header)
			assert.Equal(t, "nosniff", resp.Header.Get(fiber.HeaderXContentTypeOptions),
				"nosniff is always sent")
		})
	}
}

func TestWithSecurityHeaders_CustomValues(t *testing.T) {
	t.Parallel()

	app := newSecurityHeadersApp(fiber.Config{},
		WithContentSecurityPolicy("default-src 'self'"),
		WithFrameOptions("SAMEORIGIN"),
		WithReferrerPolicy("strict-origin-when-cross-origin"),
	)

	resp := doSecurityHeadersRequest(t, app, "/ok", nil)

	assert.Equal(t, "default-src 'self'", resp.Header.Get(fiber.HeaderContentSecurityPolicy))
	assert.Equal(t, "SAMEORIGIN", resp.Header.Get(fiber.HeaderXFrameOptions))
	assert.Equal(t, "strict-origin-when-cross-origin", resp.Header.Get(fiber.HeaderReferrerPolicy))
}

func TestWithSecurityHeaders_NilOptionIgnored(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		resp := doSecurityHeadersRequest(t, newSecurityHeadersApp(fiber.Config{}, nil), "/ok", nil)
		assert.Equal(t, "DENY", resp.Header.Get(fiber.HeaderXFrameOptions))
	})
}

func TestWithSecurityHeaders_ForwardedProtoIgnoredWithoutTrustProxy(t *testing.T) {
	t.Parallel()

	app := newSecurityHeadersApp(fiber.Config{})

	for _, spoof := range []map[string]string{
		{fiber.HeaderXForwardedProto: "https"},
		{fiber.HeaderXForwardedSsl: "on"},
	} {
		resp := doSecurityHeadersRequest(t, app, "/ok", spoof)
		assert.Empty(t, resp.Header.Get(fiber.HeaderStrictTransportSecurity),
			"an untrusted peer cannot claim https: %v", spoof)
	}
}

func TestWithSecurityHeaders_ForwardedProtoIgnoredFromUntrustedPeer(t *testing.T) {
	t.Parallel()

	app := newSecurityHeadersApp(fiber.Config{
		TrustProxy:       true,
		TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{"10.0.0.1"}},
	})

	resp := doSecurityHeadersRequest(t, app, "/ok", map[string]string{fiber.HeaderXForwardedProto: "https"})
	assert.Empty(t, resp.Header.Get(fiber.HeaderStrictTransportSecurity))
}

func TestWithSecurityHeaders_HSTSFromTrustedTerminator(t *testing.T) {
	t.Parallel()

	app := newSecurityHeadersApp(fiber.Config{
		TrustProxy:       true,
		TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{testPeerAddr}},
	})

	resp := doSecurityHeadersRequest(t, app, "/ok", map[string]string{fiber.HeaderXForwardedProto: "https"})
	assert.Equal(t, defaultHSTSValue, resp.Header.Get(fiber.HeaderStrictTransportSecurity))

	resp = doSecurityHeadersRequest(t, app, "/missing", map[string]string{fiber.HeaderXForwardedSsl: "on"})
	assert.Equal(t, defaultHSTSValue, resp.Header.Get(fiber.HeaderStrictTransportSecurity))

	resp = doSecurityHeadersRequest(t, app, "/ok", map[string]string{fiber.HeaderXForwardedProto: "http"})
	assert.Empty(t, resp.Header.Get(fiber.HeaderStrictTransportSecurity),
		"a trusted terminator reporting plain http gets no HSTS")
}

func TestWithSecurityHeaders_HSTSFormatting(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name              string
		maxAge            time.Duration
		includeSubDomains bool
		preload           bool
		want              string
	}{
		{"max-age only", 24 * time.Hour, false, false, "max-age=86400"},
		{"subdomains", 24 * time.Hour, true, false, "max-age=86400; includeSubDomains"},
		{"preload", 365 * 24 * time.Hour, false, true, "max-age=31536000; preload"},
		{"subdomains and preload", 365 * 24 * time.Hour, true, true, "max-age=31536000; includeSubDomains; preload"},
		{"zero disables", 0, true, true, ""},
		{"negative disables", -time.Hour, true, false, ""},
		{"sub-second disables", 500 * time.Millisecond, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := newSecurityHeadersApp(fiber.Config{
				TrustProxy:       true,
				TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{testPeerAddr}},
			}, WithHSTS(tc.maxAge, tc.includeSubDomains, tc.preload))

			resp := doSecurityHeadersRequest(t, app, "/ok", map[string]string{fiber.HeaderXForwardedProto: "https"})

			assert.Equal(t, tc.want, resp.Header.Get(fiber.HeaderStrictTransportSecurity))
			_, present := resp.Header[fiber.HeaderStrictTransportSecurity]
			assert.Equal(t, tc.want != "", present)
		})
	}
}
