package observability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func TestNewMetricsCollector(t *testing.T) {
	ctx := context.Background()

	t.Run("with enabled provider", func(_ *testing.T) {
		provider, err := New(ctx, WithServiceName("test-service"))
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		collector, err := NewMetricsCollector(provider)
		require.NoError(t, err)
		assert.NotNil(t, collector)
		assert.Equal(t, provider, collector.provider)
		assert.NotNil(t, collector.requestCounter)
		assert.NotNil(t, collector.errorCounter)
		assert.NotNil(t, collector.successCounter)
		assert.NotNil(t, collector.retryCounter)
		assert.NotNil(t, collector.requestDuration)
		assert.NotNil(t, collector.requestBatchSize)
		assert.NotNil(t, collector.requestBatchLatency)
	})

	t.Run("with disabled provider", func(_ *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-service"),
			WithComponentEnabled(false, false, false),
		)
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		collector, err := NewMetricsCollector(provider)
		require.NoError(t, err)
		assert.NotNil(t, collector)
		assert.Equal(t, provider, collector.provider)
	})

	t.Run("with nil provider", func(_ *testing.T) {
		// This would panic in the current implementation
		// but we can test the behavior if we modify it
		assert.Panics(t, func() {
			_, _ = NewMetricsCollector(nil)
		})
	})
}

func TestMetricsCollector_RecordRequest(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("successful request", func(_ *testing.T) {
		duration := 100 * time.Millisecond
		attrs := []attribute.KeyValue{
			attribute.String("test", "value"),
		}

		// Should not panic
		collector.RecordRequest(ctx, "test-operation", "test-resource", 200, duration, attrs...)
	})

	t.Run("error request", func(_ *testing.T) {
		duration := 200 * time.Millisecond
		attrs := []attribute.KeyValue{
			attribute.String("error", "test-error"),
		}

		// Should not panic
		collector.RecordRequest(ctx, "test-operation", "test-resource", 500, duration, attrs...)
	})

	t.Run("client error request", func(_ *testing.T) {
		duration := 50 * time.Millisecond

		// Should not panic
		collector.RecordRequest(ctx, "test-operation", "test-resource", 404, duration)
	})

	t.Run("with disabled provider", func(_ *testing.T) {
		disabledProvider, err := New(ctx,
			WithServiceName("test-service"),
			WithComponentEnabled(false, false, false),
		)
		require.NoError(t, err)
		defer func() { _ = disabledProvider.Shutdown(ctx) }()

		disabledCollector, err := NewMetricsCollector(disabledProvider)
		require.NoError(t, err)

		// Should not panic and should do nothing
		disabledCollector.RecordRequest(
			ctx,
			"test-operation",
			"test-resource",
			200,
			time.Millisecond,
		)
	})
}

func TestMetricsCollector_RecordBatchRequest(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("batch request", func(_ *testing.T) {
		duration := 500 * time.Millisecond
		batchSize := 10
		attrs := []attribute.KeyValue{
			attribute.String("batch_id", "test-batch"),
		}

		// Should not panic
		collector.RecordBatchRequest(
			ctx,
			"batch-operation",
			"batch-resource",
			batchSize,
			duration,
			attrs...)
	})

	t.Run("empty batch", func(_ *testing.T) {
		duration := 10 * time.Millisecond
		batchSize := 0

		// Should not panic
		collector.RecordBatchRequest(ctx, "batch-operation", "batch-resource", batchSize, duration)
	})

	t.Run("large batch", func(_ *testing.T) {
		duration := 2 * time.Second
		batchSize := 1000

		// Should not panic
		collector.RecordBatchRequest(ctx, "batch-operation", "batch-resource", batchSize, duration)
	})
}

func TestMetricsCollector_RecordRetry(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("first retry", func(_ *testing.T) {
		attrs := []attribute.KeyValue{
			attribute.String("reason", "timeout"),
		}

		// Should not panic
		collector.RecordRetry(ctx, "retry-operation", "retry-resource", 1, attrs...)
	})

	t.Run("multiple retries", func(_ *testing.T) {
		for attempt := 1; attempt <= 3; attempt++ {
			attrs := []attribute.KeyValue{
				attribute.String("reason", "connection_error"),
				attribute.Int("max_attempts", 3),
			}

			// Should not panic
			collector.RecordRetry(ctx, "retry-operation", "retry-resource", attempt, attrs...)
		}
	})
}

func TestMetricsCollector_RecordError(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("network error", func(_ *testing.T) {
		attrs := []attribute.KeyValue{
			attribute.String("error_code", "NETWORK_TIMEOUT"),
		}

		// Should not panic
		collector.RecordError(ctx, "network-operation", "network-resource", "timeout", attrs...)
	})

	t.Run("validation error", func(_ *testing.T) {
		attrs := []attribute.KeyValue{
			attribute.String("field", "email"),
		}

		// Should not panic
		collector.RecordError(
			ctx,
			"validation-operation",
			"validation-resource",
			"validation_error",
			attrs...)
	})

	t.Run("unknown error", func(_ *testing.T) {
		// Should not panic
		collector.RecordError(ctx, "unknown-operation", "unknown-resource", "unknown")
	})
}

func TestTimer(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("successful timer", func(_ *testing.T) {
		timer := collector.NewTimer(ctx, "timer-operation", "timer-resource",
			attribute.String("test", "value"),
		)
		assert.NotNil(t, timer)
		assert.Equal(t, collector, timer.collector)
		assert.Equal(t, ctx, timer.ctx)
		assert.Equal(t, "timer-operation", timer.operation)
		assert.Equal(t, "timer-resource", timer.resourceType)

		// Simulate some work
		time.Sleep(10 * time.Millisecond)

		// Should not panic
		timer.Stop(200, attribute.String("additional", "attr"))
	})

	t.Run("error timer", func(_ *testing.T) {
		timer := collector.NewTimer(ctx, "error-operation", "error-resource")

		// Simulate some work
		time.Sleep(5 * time.Millisecond)

		// Should not panic
		timer.StopWithError("test_error", attribute.String("error_detail", "test"))
	})

	t.Run("batch timer", func(_ *testing.T) {
		timer := collector.NewTimer(ctx, "batch-operation", "batch-resource")

		// Simulate some work
		time.Sleep(15 * time.Millisecond)

		// Should not panic
		timer.StopBatch(5, attribute.String("batch_type", "test"))
	})
}

func TestBatchTimer(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("batch timer with items", func(_ *testing.T) {
		batchTimer := collector.NewBatchTimer(ctx, "batch-operation", "batch-resource",
			attribute.String("batch_id", "test-batch"),
		)
		assert.NotNil(t, batchTimer)
		assert.Equal(t, 0, len(batchTimer.items))

		// Add items
		batchTimer.AddItem("item1")
		batchTimer.AddItem("item2")
		batchTimer.AddItems("item3", "item4", "item5")

		assert.Equal(t, 5, len(batchTimer.items))

		// Simulate some work
		time.Sleep(20 * time.Millisecond)

		// Should not panic
		batchTimer.Stop(attribute.String("result", "success"))
	})

	t.Run("empty batch timer", func(_ *testing.T) {
		batchTimer := collector.NewBatchTimer(ctx, "empty-batch", "empty-resource")

		// Don't add any items
		time.Sleep(5 * time.Millisecond)

		// Should not panic
		batchTimer.Stop()
	})

	t.Run("batch timer with complex items", func(_ *testing.T) {
		batchTimer := collector.NewBatchTimer(ctx, "complex-batch", "complex-resource")

		// Add different types of items
		batchTimer.AddItem(map[string]any{"id": 1, "name": "test"})
		batchTimer.AddItem([]int{1, 2, 3})
		batchTimer.AddItem(struct{ Value string }{Value: "test"})

		assert.Equal(t, 3, len(batchTimer.items))

		time.Sleep(10 * time.Millisecond)
		batchTimer.Stop()
	})
}

func TestCounter(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("counter increment", func(_ *testing.T) {
		counter := collector.NewCounter("test-counter", "counter-resource",
			attribute.String("counter_type", "test"),
		)
		assert.NotNil(t, counter)
		assert.Equal(t, collector, counter.collector)
		assert.Equal(t, "test-counter", counter.name)
		assert.Equal(t, "counter-resource", counter.resourceType)

		// Should not panic
		counter.Inc(ctx, attribute.String("increment", "test"))
	})

	t.Run("counter add", func(_ *testing.T) {
		counter := collector.NewCounter("add-counter", "add-resource")

		// Should not panic
		counter.Add(ctx, 5.5, attribute.String("add_value", "5.5"))
		counter.Add(ctx, 10, attribute.String("add_value", "10"))
		counter.Add(ctx, 0, attribute.String("add_value", "0"))
	})

	t.Run("counter with disabled provider", func(_ *testing.T) {
		disabledProvider, err := New(ctx,
			WithServiceName("test-service"),
			WithComponentEnabled(false, false, false),
		)
		require.NoError(t, err)
		defer func() { _ = disabledProvider.Shutdown(ctx) }()

		disabledCollector, err := NewMetricsCollector(disabledProvider)
		require.NoError(t, err)

		counter := disabledCollector.NewCounter("disabled-counter", "disabled-resource")

		// Should not panic and should do nothing
		counter.Inc(ctx)
		counter.Add(ctx, 100)
	})
}

func TestMetricsCollectorIntegration(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("integration-test"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(t, err)

	t.Run("complete workflow", func(_ *testing.T) {
		// Start a timer
		timer := collector.NewTimer(ctx, "integration-operation", "integration-resource",
			attribute.String("workflow", "complete"),
		)

		// Record some retries
		collector.RecordRetry(ctx, "integration-operation", "integration-resource", 1,
			attribute.String("reason", "temporary_failure"),
		)
		collector.RecordRetry(ctx, "integration-operation", "integration-resource", 2,
			attribute.String("reason", "temporary_failure"),
		)

		// Simulate work
		time.Sleep(25 * time.Millisecond)

		// Complete successfully
		timer.Stop(200, attribute.String("final_result", "success"))

		// Record additional metrics
		counter := collector.NewCounter("integration-counter", "integration-resource")
		counter.Inc(ctx, attribute.String("event", "workflow_completed"))

		// Record a batch operation
		batchTimer := collector.NewBatchTimer(ctx, "integration-batch", "integration-resource")
		batchTimer.AddItems("result1", "result2", "result3")
		time.Sleep(10 * time.Millisecond)
		batchTimer.Stop(attribute.String("batch_result", "success"))
	})

	t.Run("error workflow", func(_ *testing.T) {
		timer := collector.NewTimer(ctx, "error-workflow", "error-resource")

		// Record multiple retries
		for attempt := 1; attempt <= 3; attempt++ {
			collector.RecordRetry(ctx, "error-workflow", "error-resource", attempt,
				attribute.String("reason", "persistent_error"),
			)
		}

		// Simulate work
		time.Sleep(15 * time.Millisecond)

		// Fail with error
		timer.StopWithError("persistent_error",
			attribute.String("final_attempt", "3"),
			attribute.Bool("exhausted_retries", true),
		)

		// Record the specific error
		collector.RecordError(ctx, "error-workflow", "error-resource", "persistent_error",
			attribute.String("error_category", "external_service"),
		)
	})
}

func BenchmarkMetricsCollector(b *testing.B) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("benchmark-test"))
	require.NoError(b, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	collector, err := NewMetricsCollector(provider)
	require.NoError(b, err)

	b.Run("RecordRequest", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			collector.RecordRequest(
				ctx,
				"benchmark-operation",
				"benchmark-resource",
				200,
				time.Millisecond,
			)
		}
	})

	b.Run("Timer", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			timer := collector.NewTimer(ctx, "benchmark-timer", "benchmark-resource")
			timer.Stop(200)
		}
	})

	b.Run("Counter", func(b *testing.B) {
		counter := collector.NewCounter("benchmark-counter", "benchmark-resource")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			counter.Inc(ctx)
		}
	})

	b.Run("BatchTimer", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			batchTimer := collector.NewBatchTimer(ctx, "benchmark-batch", "benchmark-resource")
			batchTimer.AddItems("item1", "item2", "item3")
			batchTimer.Stop()
		}
	})
}
