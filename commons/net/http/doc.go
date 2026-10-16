// Package http provides Fiber-oriented HTTP helpers, middleware, and error handling.
//
// Core entry points include response helpers (Respond, RespondError, RenderError),
// middleware builders, and FiberErrorHandler for consistent request failure handling.
//
// # Error envelopes
//
// ErrorResponse + RespondError provide the historical flat
// {code,title,message} envelope for simple services. ErrorEnvelope +
// RespondErrorEnvelope are the richer sibling contract for services whose
// clients pattern-match on stable application-level error codes. The richer
// helper intentionally uses the RespondErrorEnvelope name instead of changing
// RespondError, preserving the existing v5 RespondError(c,status,title,message)
// API and making the wire contract explicit at each call site.
//
// # Security headers
//
// WithSecurityHeaders is the opt-in API security-header profile (nosniff, a
// restrictive CSP, frame denial, no referrer, and HSTS on https only). HSTS
// trusts a forwarded scheme only through Fiber's TrustProxy configuration:
//
//	app := fiber.New(fiber.Config{
//	    TrustProxy:       true,
//	    TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{"10.0.0.10"}},
//	})
//	app.Use(http.WithSecurityHeaders())
package http
