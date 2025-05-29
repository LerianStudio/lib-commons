package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

func TestContextUtilities(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	t.Run("WithProvider and GetProvider", func(t *testing.T) {
		ctxWithProvider := WithProvider(ctx, provider)
		retrievedProvider := GetProvider(ctxWithProvider)

		assert.NotNil(t, retrievedProvider)
		assert.Equal(t, provider, retrievedProvider)
	})

	t.Run("GetProvider from context without provider", func(t *testing.T) {
		retrievedProvider := GetProvider(ctx)
		assert.Nil(t, retrievedProvider)
	})

	t.Run("WithSpanAttributes", func(t *testing.T) {
		// Create a span context first
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		attrs := []attribute.KeyValue{
			attribute.String("test", "value"),
			attribute.Int("count", 42),
		}

		ctxWithAttrs := WithSpanAttributes(spanCtx, attrs...)
		assert.NotNil(t, ctxWithAttrs)
		// WithSpanAttributes modifies the span and returns the same context
		assert.Equal(t, spanCtx, ctxWithAttrs)
	})

	t.Run("AddSpanAttributes", func(t *testing.T) {
		// Create a span context first
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		attrs := []attribute.KeyValue{
			attribute.String("test", "value"),
			attribute.Int("count", 42),
		}

		// Should not panic
		AddSpanAttributes(spanCtx, attrs...)
	})

	t.Run("AddSpanEvent", func(t *testing.T) {
		// Create a span context first
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		attrs := []attribute.KeyValue{
			attribute.String("event", "test-event"),
		}

		// Should not panic
		AddSpanEvent(spanCtx, "test-event", attrs...)
	})

	t.Run("WithBaggageItem and GetBaggageItem", func(t *testing.T) {
		ctxWithBaggage, err := WithBaggageItem(ctx, "test-key", "test-value")
		require.NoError(t, err)

		value := GetBaggageItem(ctxWithBaggage, "test-key")
		assert.Equal(t, "test-value", value)
	})

	t.Run("GetBaggageItem non-existent key", func(t *testing.T) {
		value := GetBaggageItem(ctx, "non-existent")
		assert.Empty(t, value)
	})

	t.Run("WithBaggageItem invalid key", func(t *testing.T) {
		// Test with invalid baggage key (contains invalid characters)
		_, err := WithBaggageItem(ctx, "invalid key with spaces", "value")
		assert.Error(t, err)
	})

	t.Run("Start span", func(t *testing.T) {
		spanCtx, span := Start(ctx, "test-span")
		defer span.End()

		assert.NotNil(t, spanCtx)
		assert.NotNil(t, span)
		assert.NotEqual(t, ctx, spanCtx)

		// Verify span is in context
		retrievedSpan := trace.SpanFromContext(spanCtx)
		assert.Equal(t, span, retrievedSpan)
	})

	t.Run("Log", func(t *testing.T) {
		logger := Log(ctx)
		assert.NotNil(t, logger)

		// Should not panic
		logger.Info("test message")
	})

	t.Run("TraceID", func(t *testing.T) {
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		traceID := TraceID(spanCtx)
		assert.NotEmpty(t, traceID)

		// Should be valid trace ID format
		assert.Len(t, traceID, 32) // 32 hex characters
	})

	t.Run("TraceID from context without span", func(t *testing.T) {
		traceID := TraceID(ctx)
		assert.Empty(t, traceID)
	})

	t.Run("SpanID", func(t *testing.T) {
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		spanID := SpanID(spanCtx)
		assert.NotEmpty(t, spanID)

		// Should be valid span ID format
		assert.Len(t, spanID, 16) // 16 hex characters
	})

	t.Run("SpanID from context without span", func(t *testing.T) {
		spanID := SpanID(ctx)
		assert.Empty(t, spanID)
	})

	t.Run("ExtractSpanContext", func(t *testing.T) {
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		extractedSpanContext := ExtractSpanContext(spanCtx)
		assert.True(t, extractedSpanContext.IsValid())
		assert.Equal(t, span.SpanContext(), extractedSpanContext)
	})

	t.Run("ExtractSpanContext from context without span", func(t *testing.T) {
		extractedSpanContext := ExtractSpanContext(ctx)
		assert.False(t, extractedSpanContext.IsValid())
	})

	t.Run("IsRecording", func(t *testing.T) {
		tracer := provider.Tracer()
		spanCtx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		recording := IsRecording(spanCtx)
		// May be true or false depending on sampling, but should not panic
		assert.IsType(t, true, recording)
	})

	t.Run("IsRecording from context without span", func(t *testing.T) {
		recording := IsRecording(ctx)
		assert.False(t, recording)
	})
}

func TestBaggageIntegration(t *testing.T) {
	ctx := context.Background()

	t.Run("Multiple baggage items", func(t *testing.T) {
		ctx1, err := WithBaggageItem(ctx, "key1", "value1")
		require.NoError(t, err)

		ctx2, err := WithBaggageItem(ctx1, "key2", "value2")
		require.NoError(t, err)

		// Both values should be accessible
		assert.Equal(t, "value1", GetBaggageItem(ctx2, "key1"))
		assert.Equal(t, "value2", GetBaggageItem(ctx2, "key2"))
	})

	t.Run("Overwrite baggage item", func(t *testing.T) {
		ctx1, err := WithBaggageItem(ctx, "key", "value1")
		require.NoError(t, err)

		ctx2, err := WithBaggageItem(ctx1, "key", "value2")
		require.NoError(t, err)

		// Should have the new value
		assert.Equal(t, "value2", GetBaggageItem(ctx2, "key"))
	})

	t.Run("Baggage with existing baggage in context", func(t *testing.T) {
		// Create initial baggage
		member, err := baggage.NewMember("existing", "value")
		require.NoError(t, err)

		bag, err := baggage.New(member)
		require.NoError(t, err)

		ctxWithBaggage := baggage.ContextWithBaggage(ctx, bag)

		// Add new item
		newCtx, err := WithBaggageItem(ctxWithBaggage, "new-key", "new-value")
		require.NoError(t, err)

		// Both should be accessible
		assert.Equal(t, "value", GetBaggageItem(newCtx, "existing"))
		assert.Equal(t, "new-value", GetBaggageItem(newCtx, "new-key"))
	})
}
