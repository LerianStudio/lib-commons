package outbox

import (
	"context"
	"strings"

	constant "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	// TraceContextTraceparent is the W3C traceparent key of an outbox trace carrier.
	TraceContextTraceparent = constant.MetadataTraceparent
	// TraceContextTracestate is the W3C tracestate key of an outbox trace carrier.
	TraceContextTracestate = constant.MetadataTracestate
)

// traceContextPropagator is an explicit W3C Trace Context propagator, never the
// global one: the global propagator commonly bundles Baggage, and an outbox row
// is durable storage read back in another process. Only traceparent and
// tracestate belong there.
var traceContextPropagator = propagation.TraceContext{}

// CaptureTraceContext returns the W3C trace carrier for ctx, or nil when ctx
// carries no valid span context. The result contains traceparent, and
// tracestate when the caller has one; nothing else.
func CaptureTraceContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}

	if !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}

	carrier := propagation.MapCarrier{}
	traceContextPropagator.Inject(ctx, carrier)

	return SanitizeTraceContext(carrier)
}

// SanitizeTraceContext returns carrier reduced to the W3C keys the outbox
// persists, with keys lowercased, values trimmed, and empty entries dropped.
// It returns nil when nothing survives, so an absent carrier and an empty one
// are indistinguishable downstream.
func SanitizeTraceContext(carrier map[string]string) map[string]string {
	if len(carrier) == 0 {
		return nil
	}

	sanitized := make(map[string]string, len(carrier))

	for key, value := range carrier {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != TraceContextTraceparent && key != TraceContextTracestate {
			continue
		}

		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		sanitized[key] = value
	}

	if len(sanitized) == 0 {
		return nil
	}

	return sanitized
}

// RestoreTraceContext returns ctx extended with the span context encoded in
// carrier, reporting whether a usable remote span context was recovered. It
// returns ctx unchanged when the carrier is empty or malformed, so a corrupt
// row never breaks dispatch.
func RestoreTraceContext(ctx context.Context, carrier map[string]string) (context.Context, bool) {
	if ctx == nil {
		ctx = context.Background()
	}

	sanitized := SanitizeTraceContext(carrier)
	if sanitized == nil {
		return ctx, false
	}

	restored := traceContextPropagator.Extract(ctx, propagation.MapCarrier(sanitized))
	if !trace.SpanContextFromContext(restored).IsValid() {
		return ctx, false
	}

	return restored, true
}
