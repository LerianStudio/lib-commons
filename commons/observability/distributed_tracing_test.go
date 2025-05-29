package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestDistributedTracingHelper(t *testing.T) {
	helper := NewDistributedTracingHelper()
	require.NotNil(t, helper)

	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	tracer := provider.Tracer()

	t.Run("PropagateServiceCall", func(t *testing.T) {
		called := false
		err := helper.PropagateServiceCall(ctx, tracer, "test-service", "test-operation", func(ctx context.Context) error {
			called = true
			span := trace.SpanFromContext(ctx)
			assert.NotNil(t, span)
			return nil
		})

		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("PropagateServiceCall with error", func(t *testing.T) {
		testErr := assert.AnError
		err := helper.PropagateServiceCall(ctx, tracer, "test-service", "test-operation", func(ctx context.Context) error {
			return testErr
		})

		assert.Equal(t, testErr, err)
	})

	t.Run("InjectContext and ExtractContext", func(t *testing.T) {
		// Create a span context
		ctx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		// Create a carrier
		carrier := make(MapCarrier)

		// Inject context
		helper.InjectContext(ctx, carrier)
		assert.NotEmpty(t, carrier)

		// Extract context
		newCtx := helper.ExtractContext(context.Background(), carrier)
		assert.NotNil(t, newCtx)
	})

	t.Run("CreateChildSpan", func(t *testing.T) {
		parentCtx, parentSpan := tracer.Start(ctx, "parent-span")
		defer parentSpan.End()

		childCtx, childSpan := helper.CreateChildSpan(parentCtx, tracer, "child-span")
		defer childSpan.End()

		assert.NotNil(t, childCtx)
		assert.NotNil(t, childSpan)
		assert.NotEqual(t, parentCtx, childCtx)
	})
}

func TestMapCarrier(t *testing.T) {
	carrier := make(MapCarrier)

	t.Run("Set and Get", func(t *testing.T) {
		carrier.Set("test-key", "test-value")
		value := carrier.Get("test-key")
		assert.Equal(t, "test-value", value)
	})

	t.Run("Get non-existent key", func(t *testing.T) {
		value := carrier.Get("non-existent")
		assert.Empty(t, value)
	})

	t.Run("Keys", func(t *testing.T) {
		carrier.Set("key1", "value1")
		carrier.Set("key2", "value2")

		keys := carrier.Keys()
		assert.Contains(t, keys, "key1")
		assert.Contains(t, keys, "key2")
		assert.Len(t, keys, 3) // Including "test-key" from previous test
	})
}

func TestTraceContextCarrier(t *testing.T) {
	carrier := NewTraceContextCarrier()
	require.NotNil(t, carrier)

	t.Run("Set and Get", func(t *testing.T) {
		carrier.Set("test-header", "test-value")
		value := carrier.Get("test-header")
		assert.Equal(t, "test-value", value)
	})

	t.Run("Get non-existent header", func(t *testing.T) {
		value := carrier.Get("non-existent")
		assert.Empty(t, value)
	})

	t.Run("Keys", func(t *testing.T) {
		carrier.Set("header1", "value1")
		carrier.Set("header2", "value2")

		keys := carrier.Keys()
		assert.Contains(t, keys, "header1")
		assert.Contains(t, keys, "header2")
	})

	t.Run("GetHeaders", func(t *testing.T) {
		headers := carrier.GetHeaders()
		assert.NotNil(t, headers)
		assert.Contains(t, headers, "test-header")
		assert.Contains(t, headers, "header1")
		assert.Contains(t, headers, "header2")
	})
}

func TestSpanProcessor(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	processor := NewSpanProcessor(provider)
	require.NotNil(t, processor)

	t.Run("ProcessWithSpan success", func(t *testing.T) {
		called := false
		err := processor.ProcessWithSpan(ctx, "test-span", func(ctx context.Context) error {
			called = true
			span := trace.SpanFromContext(ctx)
			assert.NotNil(t, span)
			return nil
		})

		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("ProcessWithSpan error", func(t *testing.T) {
		testErr := assert.AnError
		err := processor.ProcessWithSpan(ctx, "test-span", func(ctx context.Context) error {
			return testErr
		})

		assert.Equal(t, testErr, err)
	})

	t.Run("ProcessWithSpanAndResult success", func(t *testing.T) {
		result, err := processor.ProcessWithSpanAndResult(ctx, "test-span", func(ctx context.Context) (interface{}, error) {
			span := trace.SpanFromContext(ctx)
			assert.NotNil(t, span)
			return "test-result", nil
		})

		assert.NoError(t, err)
		assert.Equal(t, "test-result", result)
	})

	t.Run("ProcessWithSpanAndResult error", func(t *testing.T) {
		testErr := assert.AnError
		result, err := processor.ProcessWithSpanAndResult(ctx, "test-span", func(ctx context.Context) (interface{}, error) {
			return nil, testErr
		})

		assert.Equal(t, testErr, err)
		assert.Nil(t, result)
	})
}

func TestAsyncSpanProcessor(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	processor := NewAsyncSpanProcessor(provider)
	require.NotNil(t, processor)

	t.Run("ProcessAsync success", func(t *testing.T) {
		called := false
		errChan := processor.ProcessAsync(ctx, "test-span", func(ctx context.Context) error {
			called = true
			span := trace.SpanFromContext(ctx)
			assert.NotNil(t, span)
			return nil
		})

		err := <-errChan
		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("ProcessAsync error", func(t *testing.T) {
		testErr := assert.AnError
		errChan := processor.ProcessAsync(ctx, "test-span", func(ctx context.Context) error {
			return testErr
		})

		err := <-errChan
		assert.Equal(t, testErr, err)
	})
}

func TestTracingUtilities(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	tracer := provider.Tracer()

	t.Run("Link", func(t *testing.T) {
		_, span := tracer.Start(ctx, "test-span")
		spanContext := span.SpanContext()
		span.End()

		link := Link(ctx, spanContext, attribute.String("test", "value"))
		assert.Equal(t, spanContext, link.SpanContext)
		assert.Len(t, link.Attributes, 1)
	})

	t.Run("GetCurrentSpan", func(t *testing.T) {
		ctx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		currentSpan := GetCurrentSpan(ctx)
		assert.NotNil(t, currentSpan)
		assert.Equal(t, span.SpanContext().SpanID(), currentSpan.SpanContext().SpanID())
	})

	t.Run("SetSpanError", func(t *testing.T) {
		ctx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		testErr := assert.AnError
		SetSpanError(ctx, testErr)
		// No assertion needed, just ensure it doesn't panic
	})

	t.Run("SetSpanSuccess", func(t *testing.T) {
		ctx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		SetSpanSuccess(ctx)
		// No assertion needed, just ensure it doesn't panic
	})
}
