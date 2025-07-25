package opentelemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestInjectTraceHeadersIntoQueue(t *testing.T) {
	// Setup OpenTelemetry with proper propagator and real tracer
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	tracer := tp.Tracer("inject-trace-test")

	// Create a root span
	rootCtx, rootSpan := tracer.Start(context.Background(), "test-span")
	defer rootSpan.End()

	// Create initial headers map
	headers := map[string]any{
		"X-Request-Id": "test-request-123",
		"Content-Type": "application/json",
	}

	// Test injection into existing headers
	InjectTraceHeadersIntoQueue(rootCtx, &headers)

	// Verify original headers are preserved
	if headers["X-Request-Id"] != "test-request-123" {
		t.Error("Original headers should be preserved")
	}

	if headers["Content-Type"] != "application/json" {
		t.Error("Original headers should be preserved")
	}

	// Verify trace headers were added
	if _, exists := headers["Traceparent"]; !exists {
		t.Errorf("Expected 'Traceparent' header to be added. Got headers: %v", headers)
	}

	// Verify we have more headers than we started with
	if len(headers) <= 2 {
		t.Errorf("Expected headers to be added. Original: 2, Final: %d", len(headers))
	}

	t.Logf("Final headers: %+v", headers)
}

func TestInjectTraceHeadersIntoQueueWithNilPointer(t *testing.T) {
	// Test with nil pointer - should not panic
	InjectTraceHeadersIntoQueue(context.Background(), nil)
	// If we reach here, the function handled nil gracefully
}

func TestInjectTraceHeadersIntoQueueWithEmptyHeaders(t *testing.T) {
	// Setup OpenTelemetry
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	tracer := tp.Tracer("inject-trace-test")

	// Create a root span
	rootCtx, rootSpan := tracer.Start(context.Background(), "test-span")
	defer rootSpan.End()

	// Start with empty headers
	headers := map[string]any{}

	// Test injection
	InjectTraceHeadersIntoQueue(rootCtx, &headers)

	// Verify trace headers were added
	if len(headers) == 0 {
		t.Error("Expected trace headers to be added to empty map")
	}

	if _, exists := headers["Traceparent"]; !exists {
		t.Errorf("Expected 'Traceparent' header to be added. Got headers: %v", headers)
	}

	t.Logf("Headers added to empty map: %+v", headers)
}
