package client

import (
	"net/http"

	"github.com/LerianStudio/lib-observability/v3/httpobs"
	"go.opentelemetry.io/otel"
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
// passed explicitly because httpobs defaults the MeterProvider to the global one
// but NOT the TracerProvider — without WithTracerProvider it emits the metric and
// no span at all (lib-observability ADR-005).
//
// Instrumentation is never allowed to break connectivity: httpobs construction
// cannot fail, and a nil base is valid — it falls back to http.DefaultTransport.
func instrumentedTransport(base http.RoundTripper) http.RoundTripper {
	return httpobs.NewTransport(base,
		httpobs.WithMeterProvider(otel.GetMeterProvider()),
		httpobs.WithTracerProvider(otel.GetTracerProvider()),
	)
}
