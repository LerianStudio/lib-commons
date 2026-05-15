// Client telemetry helpers: span creation and logger-level shortcuts.
//
// This file is a surgical extraction from client.go. The intent is to keep
// client.go focused on lifecycle (construction, Start, Close, onEvent,
// fireSubscribers) and collect the telemetry plumbing — which is purely
// infrastructural and rarely changes shape — in one place.
//
// Nothing here is behavior-bearing: the exported surface of the Client is
// unchanged, and every helper preserves its existing contract (nil-safe
// logger guards, no-op span fallback when telemetry is unconfigured). Tests
// continue to reference these methods by name.
package systemplane

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// startSpan creates a child span if telemetry is configured, otherwise returns
// a no-op span. Callers MUST defer finish() to end the span.
func (c *Client) startSpan(ctx context.Context, name string) (context.Context, trace.Span, func()) {
	return c.startSpanWithAttrs(ctx, name)
}

// startSpanWithAttrs creates a child span and (when telemetry is configured)
// sets the provided attributes on it before returning. Callers MUST defer
// finish() to end the span. The attributes argument is variadic so zero
// attributes is a valid — and common — call shape, used by legacy callers
// that do not need span attributes.
func (c *Client) startSpanWithAttrs(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span, func()) {
	noop := func() {}

	if c.telemetry == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	tracer, err := c.telemetry.Tracer(tracerName)
	if err != nil || tracer == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	ctx, span := tracer.Start(ctx, name)

	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}

	return ctx, span, func() { span.End() }
}

// logWarn emits a warning-level log via the configured logger.
func (c *Client) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if !nilcheck.Interface(c.logger) {
		c.logger.Log(ctx, log.LevelWarn, msg, fields...)
	}
}

// logDebug emits a debug-level log via the configured logger. Nil-safe on
// the logger field for symmetry with logWarn — newClient always wires a
// non-nil logger today, but the guard future-proofs against construction
// paths that might bypass it.
func (c *Client) logDebug(ctx context.Context, msg string, fields ...log.Field) {
	if !nilcheck.Interface(c.logger) {
		c.logger.Log(ctx, log.LevelDebug, msg, fields...)
	}
}
