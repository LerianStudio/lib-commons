//go:build unit

package otelscope

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// install swaps the global TracerProvider for a recording one and returns the
// recorder. Cleanup puts back whatever was there before.
func install(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	return recorder
}

// TestTracerScope pins FC-7 for the shared helper: the scope name is the
// lib-commons module path joined with the package, the version is the version
// of that module in the binary.
//
// Mutates a process-global, the OpenTelemetry TracerProvider: no t.Parallel().
func TestTracerScope(t *testing.T) {
	recorder := install(t)

	_, span := Tracer("commons/postgres").Start(context.Background(), "probe")
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}

	scope := ended[0].InstrumentationScope()
	if want := "github.com/LerianStudio/lib-commons/v7/commons/postgres"; scope.Name != want {
		t.Errorf("scope name = %q, want %q", scope.Name, want)
	}

	// lib-commons is the main module of its own test binary and the toolchain
	// stamps no version on it.
	if scope.Version != "(devel)" {
		t.Errorf("scope version = %q, want %q", scope.Version, "(devel)")
	}
}

// TestTracerFollowsAReplacedProvider is the regression this helper exists for.
// A tracer built at package initialisation is delegated by the OTel SDK
// exactly once, so a tracer captured in a package variable keeps writing into
// the first provider forever. Resolving the provider per span fixes that: the
// second provider must see the second span, and the first must not.
//
// Mutates a process-global, the OpenTelemetry TracerProvider: no t.Parallel().
func TestTracerFollowsAReplacedProvider(t *testing.T) {
	tracer := Tracer("commons/redis")

	first := install(t)

	_, span := tracer.Start(context.Background(), "before")
	span.End()

	second := install(t)

	_, span = tracer.Start(context.Background(), "after")
	span.End()

	if got := len(first.Ended()); got != 1 {
		t.Fatalf("first provider recorded %d spans, want 1", got)
	}

	ended := second.Ended()
	if len(ended) != 1 {
		t.Fatalf("second provider recorded %d spans, want 1 -- the tracer is stuck on the first provider", len(ended))
	}

	if ended[0].Name() != "after" {
		t.Errorf("second provider recorded %q, want %q", ended[0].Name(), "after")
	}
}

// countingProvider counts how many tracers it hands out.
type countingProvider struct {
	noop.TracerProvider

	calls int
}

func (p *countingProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	p.calls++

	return p.TracerProvider.Tracer(name, opts...)
}

// uncomparableProvider has a dynamic type that panics under ==.
type uncomparableProvider struct {
	noop.TracerProvider

	calls []string
}

// TestTracerCachesPerProvider pins the hot-path contract: consecutive spans on
// the same provider reuse one resolved tracer, and a provider swap resolves
// again exactly once.
//
// Mutates a process-global, the OpenTelemetry TracerProvider: no t.Parallel().
func TestTracerCachesPerProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	tracer := Tracer("commons/net/http/ratelimit")

	first := &countingProvider{}
	otel.SetTracerProvider(first)

	for range 3 {
		_, span := tracer.Start(context.Background(), "probe")
		span.End()
	}

	if first.calls != 1 {
		t.Fatalf("first provider resolved %d tracers over 3 spans, want 1", first.calls)
	}

	second := &countingProvider{}
	otel.SetTracerProvider(second)

	for range 3 {
		_, span := tracer.Start(context.Background(), "probe")
		span.End()
	}

	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("after the swap: first=%d second=%d resolutions, want 1 and 1", first.calls, second.calls)
	}
}

// TestTracerSurvivesAnUncomparableProvider guards the cache against a provider
// whose dynamic type cannot be compared: it must still produce spans instead of
// panicking on the identity check.
//
// Mutates a process-global, the OpenTelemetry TracerProvider: no t.Parallel().
func TestTracerSurvivesAnUncomparableProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	otel.SetTracerProvider(uncomparableProvider{})

	tracer := Tracer("commons/redis")

	for range 2 {
		_, span := tracer.Start(context.Background(), "probe")
		span.End()
	}
}
