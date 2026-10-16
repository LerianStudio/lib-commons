package http

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

const (
	// DefaultContentSecurityPolicy is the API profile CSP WithSecurityHeaders
	// sends by default: a JSON API loads no subresources and is never framed.
	DefaultContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"
	// DefaultFrameOptions is the default X-Frame-Options value, the legacy
	// counterpart of frame-ancestors 'none' for clients that ignore CSP.
	DefaultFrameOptions = "DENY"
	// DefaultReferrerPolicy is the default Referrer-Policy value.
	DefaultReferrerPolicy = "no-referrer"
	// DefaultHSTSMaxAge is the default Strict-Transport-Security max-age (two years).
	DefaultHSTSMaxAge = 2 * 365 * 24 * time.Hour
)

// SecurityHeadersOption configures WithSecurityHeaders. A nil option is ignored.
type SecurityHeadersOption func(*securityHeadersConfig)

type securityHeadersConfig struct {
	contentSecurityPolicy string
	frameOptions          string
	referrerPolicy        string
	hstsMaxAge            time.Duration
	hstsIncludeSubDomains bool
	hstsPreload           bool
}

// WithContentSecurityPolicy sets the Content-Security-Policy value. An empty
// policy omits the header. Default: DefaultContentSecurityPolicy.
func WithContentSecurityPolicy(policy string) SecurityHeadersOption {
	return func(c *securityHeadersConfig) {
		c.contentSecurityPolicy = policy
	}
}

// WithFrameOptions sets the X-Frame-Options value. An empty value omits the
// header. Default: DefaultFrameOptions.
func WithFrameOptions(value string) SecurityHeadersOption {
	return func(c *securityHeadersConfig) {
		c.frameOptions = value
	}
}

// WithReferrerPolicy sets the Referrer-Policy value. An empty value omits the
// header. Default: DefaultReferrerPolicy.
func WithReferrerPolicy(value string) SecurityHeadersOption {
	return func(c *securityHeadersConfig) {
		c.referrerPolicy = value
	}
}

// WithHSTS sets the Strict-Transport-Security policy. maxAge is truncated to
// whole seconds; a result of zero or less disables HSTS, since "max-age=0"
// tells browsers to forget the host's policy rather than enforce it. preload
// only takes effect in browsers when maxAge is at least one year and
// includeSubDomains is set (hstspreload.org submission rules); it is emitted
// as configured either way. Default: DefaultHSTSMaxAge, includeSubDomains, no
// preload.
func WithHSTS(maxAge time.Duration, includeSubDomains, preload bool) SecurityHeadersOption {
	return func(c *securityHeadersConfig) {
		c.hstsMaxAge = maxAge
		c.hstsIncludeSubDomains = includeSubDomains
		c.hstsPreload = preload
	}
}

// WithSecurityHeaders returns a middleware that sets the API security-header
// profile on every response it sees, including errors and 404s produced below
// it. It is opt-in (mount it with app.Use) and reads no environment variables.
//
// Always sent: X-Content-Type-Options: nosniff. Sent by default and
// individually configurable (an empty value omits the header):
// Content-Security-Policy (DefaultContentSecurityPolicy), X-Frame-Options
// (DefaultFrameOptions) and Referrer-Policy (DefaultReferrerPolicy).
//
// Strict-Transport-Security is sent only when fiber.Ctx.Secure() reports
// https: either the connection itself is TLS, or X-Forwarded-Proto /
// X-Forwarded-Ssl / X-Url-Scheme came from a peer the application trusts
// through fiber.Config.TrustProxy and fiber.Config.TrustProxyConfig. That
// Fiber configuration IS the trusted TLS terminator setting; there is
// deliberately no second trust list here, because two lists drift. With
// TrustProxy off (Fiber's default) a forwarded scheme header is ignored, so a
// client cannot spoof HSTS onto a plaintext response.
//
// Headers are set before the rest of the chain runs, so a route or a later
// middleware that sets the same header wins. The OpenAPI docs page mounted by
// commons/net/http/openapi relies on this to relax the CSP for that one
// route; any HTML route needs the same kind of override, since the default
// CSP blocks every script, style and image.
//
// Mount order relative to commons/net/http/idempotency: mount this middleware
// ABOVE it. A replayed response then carries the headers set on the replaying
// request, because the idempotency middleware stores and replays only the
// headers the handler itself added.
func WithSecurityHeaders(opts ...SecurityHeadersOption) fiber.Handler {
	cfg := &securityHeadersConfig{
		contentSecurityPolicy: DefaultContentSecurityPolicy,
		frameOptions:          DefaultFrameOptions,
		referrerPolicy:        DefaultReferrerPolicy,
		hstsMaxAge:            DefaultHSTSMaxAge,
		hstsIncludeSubDomains: true,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	hsts := cfg.hstsValue()

	return func(c fiber.Ctx) error {
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")

		if cfg.contentSecurityPolicy != "" {
			c.Set(fiber.HeaderContentSecurityPolicy, cfg.contentSecurityPolicy)
		}

		if cfg.frameOptions != "" {
			c.Set(fiber.HeaderXFrameOptions, cfg.frameOptions)
		}

		if cfg.referrerPolicy != "" {
			c.Set(fiber.HeaderReferrerPolicy, cfg.referrerPolicy)
		}

		if hsts != "" && c.Secure() {
			c.Set(fiber.HeaderStrictTransportSecurity, hsts)
		}

		return c.Next()
	}
}

// hstsValue renders the Strict-Transport-Security value, or "" when HSTS is
// disabled.
func (c *securityHeadersConfig) hstsValue() string {
	seconds := int64(c.hstsMaxAge / time.Second)
	if seconds <= 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("max-age=")
	b.WriteString(strconv.FormatInt(seconds, 10))

	if c.hstsIncludeSubDomains {
		b.WriteString("; includeSubDomains")
	}

	if c.hstsPreload {
		b.WriteString("; preload")
	}

	return b.String()
}
