package opentelemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestQueueTraceContextPropagation(t *testing.T) {
	// Setup OpenTelemetry with proper propagator and real tracer
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	tracer := tp.Tracer("queue-trace-test")

	// Create a root span to simulate an HTTP request
	rootCtx, rootSpan := tracer.Start(context.Background(), "http-request")
	defer rootSpan.End()

	// Test injection
	headers := InjectQueueTraceContext(rootCtx)
	t.Logf("Injected headers: %+v", headers)
	if len(headers) == 0 {
		t.Error("Expected trace headers to be injected, got empty map")
		return
	}

	// Verify Traceparent header exists (OpenTelemetry uses canonical case)
	if _, exists := headers["Traceparent"]; !exists {
		t.Errorf("Expected 'Traceparent' header to be present in injected headers. Available headers: %v", headers)
		return
	}

	// Test extraction
	extractedCtx := ExtractQueueTraceContext(context.Background(), headers)
	if extractedCtx == nil {
		t.Error("Expected extracted context to be non-nil")
	}

	// Verify trace ID propagation
	originalTraceID := GetTraceIDFromContext(rootCtx)
	extractedTraceID := GetTraceIDFromContext(extractedCtx)

	if originalTraceID == "" {
		t.Error("Expected original trace ID to be non-empty")
	}

	if extractedTraceID == "" {
		t.Error("Expected extracted trace ID to be non-empty")
	}

	if originalTraceID != extractedTraceID {
		t.Errorf("Expected trace IDs to match: original=%s, extracted=%s", originalTraceID, extractedTraceID)
	}
}

func TestQueueTraceContextWithNilHeaders(t *testing.T) {
	ctx := context.Background()
	
	// Test extraction with nil headers
	extractedCtx := ExtractQueueTraceContext(ctx, nil)
	if extractedCtx != ctx {
		t.Error("Expected extracted context to be the same as input when headers are nil")
	}

	// Test extraction with empty headers
	extractedCtx = ExtractQueueTraceContext(ctx, map[string]string{})
	if extractedCtx == nil {
		t.Error("Expected extracted context to be non-nil even with empty headers")
	}
}

func TestGetTraceIDAndStateFromContext(t *testing.T) {
	// Test with empty context
	emptyCtx := context.Background()
	traceID := GetTraceIDFromContext(emptyCtx)
	traceState := GetTraceStateFromContext(emptyCtx)

	if traceID != "" {
		t.Errorf("Expected empty trace ID for empty context, got: %s", traceID)
	}

	if traceState != "" {
		t.Errorf("Expected empty trace state for empty context, got: %s", traceState)
	}

	// Test with span context
	tp := noop.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	traceID = GetTraceIDFromContext(ctx)
	traceState = GetTraceStateFromContext(ctx)

	// Note: With noop tracer, these might still be empty, but functions should not panic
	if traceID == "" {
		t.Log("Trace ID is empty with noop tracer (expected)")
	}

	if traceState == "" {
		t.Log("Trace state is empty with noop tracer (expected)")
	}
}
