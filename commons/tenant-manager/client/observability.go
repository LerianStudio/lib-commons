package client

import (
	"context"
	"net/http"

	"github.com/LerianStudio/lib-observability/v4/httpobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// instrumentedTransport applies the platform's default outbound HTTP telemetry to
// a transport: http.client.request.duration plus a CLIENT span per request, with
// W3C trace context injected on the outbound headers.
//
// Centralizing this here is the point of the whole wiring: a service gets the
// same series, with the same names and labels, by bumping lib-commons — nothing
// to wire per service (lib-observability docs/metrics-contract.md). The scope is
// deliberately narrow: only the outbound traffic lib-commons ITSELF originates.
// A client handed in via WithHTTPClient stays exactly as the caller built it.
//
// Providers are resolved from the process-global OpenTelemetry providers, which
// Telemetry.ApplyGlobals installs at bootstrap. A service that never calls
// ApplyGlobals gets no-op instrumentation, never an error. Both providers are
// passed explicitly so the wiring does not depend on httpobs defaults
// (lib-observability ADR-005).
//
// The CLIENT span is only produced under a parent span: see spanOnlyUnderParent.
//
// Instrumentation is never allowed to break connectivity: httpobs construction
// cannot fail, and a nil base is valid — it falls back to http.DefaultTransport.
func instrumentedTransport(base http.RoundTripper) http.RoundTripper {
	return spanOnlyUnderParent{
		traced: httpobs.NewTransport(base,
			httpobs.WithMeterProvider(otel.GetMeterProvider()),
			httpobs.WithTracerProvider(otel.GetTracerProvider()),
		),
		untraced: httpobs.NewTransport(base,
			httpobs.WithMeterProvider(otel.GetMeterProvider()),
			httpobs.WithTracerProvider(noop.NewTracerProvider()),
		),
	}
}

// spanOnlyUnderParent sends a request through the traced transport only when
// its context already carries a span. A request made outside any request
// (pool settings revalidation runs on context.Background) goes through the
// untraced one: it still records http.client.request.duration, but it does not
// start a root trace of its own. The untraced side gets an explicit no-op
// TracerProvider because otelhttp falls back to the global provider when none
// is given. Both sides wrap the same base, so they share one connection pool.
type spanOnlyUnderParent struct {
	traced   http.RoundTripper
	untraced http.RoundTripper
}

func (t spanOnlyUnderParent) RoundTrip(req *http.Request) (*http.Response, error) {
	if trace.SpanContextFromContext(req.Context()).IsValid() {
		return t.traced.RoundTrip(req)
	}

	return t.untraced.RoundTrip(req)
}

// startChildSpan opens a span only when ctx already carries one. A call made
// outside any request (pool settings revalidation runs on context.Background,
// once per check interval, per tenant, per manager) would otherwise start a
// root trace of its own on every run. Without a parent it returns ctx and the
// span already in it, which is non-recording: End and the span error helpers
// do nothing on it.
func startChildSpan(ctx context.Context, tracer trace.Tracer, name string) (context.Context, trace.Span) {
	parent := trace.SpanFromContext(ctx)
	if !parent.SpanContext().IsValid() {
		return ctx, parent
	}

	return tracer.Start(ctx, name) //nolint:spancheck // callers defer span.End() right after startChildSpan
}
