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
package http
