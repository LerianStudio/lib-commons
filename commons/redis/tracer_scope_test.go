//go:build unit

package redis

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTracerScope pins FC-7: every span this package emits carries the
// lib-commons module path as instrumentation scope name and the version of
// that module as instrumentation version.
//
// Mutates a process-global, the OpenTelemetry TracerProvider: no t.Parallel(),
// and the previous provider is put back in Cleanup so sibling tests keep
// whatever they were given. Replacing it rather than shutting it down also
// keeps the global usable: the SDK delegates an early tracer exactly once, so
// a shut-down provider left installed would silently swallow every later span.
func TestTracerScope(t *testing.T) {
	const (
		wantName = "github.com/LerianStudio/lib-commons/v7/commons/redis"
		// lib-commons is the main module of its own test binary and the
		// toolchain stamps no version on it.
		wantVersion = "(devel)"
		probeName   = "commons/redis.scope_probe"
	)

	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	_, span := tracer.Start(context.Background(), probeName)
	span.End()

	var scope struct{ Name, Version string }

	for _, s := range recorder.Ended() {
		if s.Name() == probeName {
			scope.Name, scope.Version = s.InstrumentationScope().Name, s.InstrumentationScope().Version
		}
	}

	if scope.Name != wantName {
		t.Errorf("instrumentation scope name = %q, want %q", scope.Name, wantName)
	}

	if scope.Version != wantVersion {
		t.Errorf("instrumentation scope version = %q, want %q", scope.Version, wantVersion)
	}
}
