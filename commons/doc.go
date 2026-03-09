// Package commons provides shared infrastructure helpers used across Lerian services.
//
// The package includes context helpers, validation utilities, error adapters,
// and cross-cutting primitives used by higher-level subpackages.
//
// Typical usage at request ingress:
//
//	ctx = commons.ContextWithLogger(ctx, logger)
//	ctx = commons.ContextWithTracer(ctx, tracer)
//	ctx = commons.ContextWithHeaderID(ctx, requestID)
//
// This package is intentionally dependency-light; specialized integrations live in
// subpackages such as opentelemetry, mongo, redis, rabbitmq, and server.
package commons
