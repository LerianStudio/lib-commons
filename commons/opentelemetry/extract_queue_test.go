package opentelemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestExtractTraceContextFromQueueHeaders(t *testing.T) {
	// Setup OpenTelemetry with proper propagator and real tracer
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	tracer := tp.Tracer("extract-queue-test")

	// Create a root span and inject headers (simulating producer)
	rootCtx, rootSpan := tracer.Start(context.Background(), "producer-span")
	defer rootSpan.End()

	// Inject trace headers (what producer would do)
	traceHeaders := InjectQueueTraceContext(rootCtx)
	
	// Convert to amqp.Table format (simulating RabbitMQ headers)
	amqpHeaders := make(map[string]any)
	for k, v := range traceHeaders {
		amqpHeaders[k] = v
	}
	// Add some non-trace headers
	amqpHeaders["X-Request-Id"] = "test-123"
	amqpHeaders["Content-Type"] = "application/json"

	// Test extraction (what consumer would do)
	baseCtx := context.Background()
	extractedCtx := ExtractTraceContextFromQueueHeaders(baseCtx, amqpHeaders)

	// Verify trace context was extracted correctly
	originalTraceID := GetTraceIDFromContext(rootCtx)
	extractedTraceID := GetTraceIDFromContext(extractedCtx)

	if originalTraceID == "" {
		t.Error("Expected original trace ID to be non-empty")
	}

	if extractedTraceID == "" {
		t.Error("Expected extracted trace ID to be non-empty")
	}

	if originalTraceID != extractedTraceID {
		t.Errorf("Trace ID mismatch: original=%s, extracted=%s", originalTraceID, extractedTraceID)
	}

	t.Logf("✅ Trace ID successfully propagated: %s", extractedTraceID)
}

func TestExtractTraceContextFromQueueHeadersWithEmptyHeaders(t *testing.T) {
	baseCtx := context.Background()
	
	// Test with nil headers
	extractedCtx := ExtractTraceContextFromQueueHeaders(baseCtx, nil)
	if extractedCtx != baseCtx {
		t.Error("Expected same context when headers are nil")
	}

	// Test with empty headers
	extractedCtx = ExtractTraceContextFromQueueHeaders(baseCtx, map[string]any{})
	if extractedCtx != baseCtx {
		t.Error("Expected same context when headers are empty")
	}
}

func TestExtractTraceContextFromQueueHeadersWithNonStringValues(t *testing.T) {
	baseCtx := context.Background()
	
	// Test with headers containing non-string values
	amqpHeaders := map[string]any{
		"X-Request-Id": "test-123",
		"Retry-Count":  42,           // int
		"Timestamp":    1234567890.5, // float
		"Enabled":      true,         // bool
	}

	extractedCtx := ExtractTraceContextFromQueueHeaders(baseCtx, amqpHeaders)
	
	// Should return base context since no valid trace headers
	if extractedCtx != baseCtx {
		t.Error("Expected same context when no valid trace headers present")
	}

	// Verify no trace ID extracted
	traceID := GetTraceIDFromContext(extractedCtx)
	if traceID != "" {
		t.Errorf("Expected empty trace ID, got: %s", traceID)
	}
}

func TestExtractTraceContextFromQueueHeadersWithMixedTypes(t *testing.T) {
	// Setup OpenTelemetry
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	tracer := tp.Tracer("mixed-types-test")

	// Create span and get trace headers
	rootCtx, rootSpan := tracer.Start(context.Background(), "test-span")
	defer rootSpan.End()
	
	traceHeaders := InjectQueueTraceContext(rootCtx)

	// Create mixed-type headers (simulating real RabbitMQ scenario)
	amqpHeaders := map[string]any{
		"X-Request-Id": "test-123",
		"Retry-Count":  42,
		"Enabled":      true,
	}
	
	// Add trace headers as strings
	for k, v := range traceHeaders {
		amqpHeaders[k] = v
	}

	// Test extraction
	baseCtx := context.Background()
	extractedCtx := ExtractTraceContextFromQueueHeaders(baseCtx, amqpHeaders)

	// Verify trace context was extracted despite mixed types
	originalTraceID := GetTraceIDFromContext(rootCtx)
	extractedTraceID := GetTraceIDFromContext(extractedCtx)

	if originalTraceID != extractedTraceID {
		t.Errorf("Trace ID mismatch with mixed types: original=%s, extracted=%s", originalTraceID, extractedTraceID)
	}

	t.Logf("✅ Trace extraction works with mixed header types: %s", extractedTraceID)
}
