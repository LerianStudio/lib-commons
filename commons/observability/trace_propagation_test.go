package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/commons/log"
)

func TestDefaultTracePropagationConfig(t *testing.T) {
	config := DefaultTracePropagationConfig()

	// Test propagation settings
	assert.True(t, config.EnableTextMapPropagation)
	assert.True(t, config.EnableBaggagePropagation)
	assert.True(t, config.EnableW3CTraceContext)
	assert.True(t, config.EnableB3Propagation)
	assert.True(t, config.EnableJaegerPropagation)

	// Test context settings
	assert.True(t, config.AutoExtractSpanContext)
	assert.True(t, config.AutoInjectSpanContext)
	assert.Contains(t, config.CustomHeaders, "X-Request-ID")
	assert.Contains(t, config.CustomHeaders, "X-Correlation-ID")
	assert.Contains(t, config.CustomHeaders, "X-User-ID")

	// Test service metadata
	assert.Equal(t, "lib-commons", config.ServiceMetadata["service.name"])
	assert.Equal(t, "1.0.0", config.ServiceMetadata["service.version"])
	assert.Equal(t, "production", config.ServiceMetadata["deployment.env"])

	// Test span settings
	assert.Equal(t, "internal", config.DefaultSpanKind)
	assert.True(t, config.EnableSpanEvents)
	assert.True(t, config.EnableSpanLinks)
	assert.Contains(t, config.SpanAttributeKeys, "http.method")
	assert.Contains(t, config.SpanAttributeKeys, "http.url")
	assert.Contains(t, config.SpanAttributeKeys, "user.id")

	// Test performance settings
	assert.Equal(t, 50, config.MaxBaggageItems)
	assert.Equal(t, 8192, config.MaxBaggageSize)
	assert.True(t, config.EnablePerformanceMetrics)

	// Test error handling
	assert.True(t, config.ContinueOnPropagationError)
	assert.True(t, config.LogPropagationErrors)
}

func TestNewTracePropagator(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	assert.NotNil(t, propagator)
	assert.Equal(t, config, propagator.config)
	assert.NotNil(t, propagator.logger)
	assert.NotNil(t, propagator.tracer)
	assert.NotNil(t, propagator.propagator)

	// Check that metrics are initialized
	metrics := propagator.GetPropagationMetrics()
	assert.Equal(t, int64(0), metrics.TotalExtractions)
	assert.Equal(t, int64(0), metrics.TotalInjections)
}

func TestHTTPHeaderCarrier(t *testing.T) {
	headers := make(http.Header)
	carrier := HTTPHeaderCarrier{headers: headers}

	// Test Set and Get
	carrier.Set("X-Test-Header", "test-value")
	assert.Equal(t, "test-value", carrier.Get("X-Test-Header"))

	// Test case insensitivity
	assert.Equal(t, "test-value", carrier.Get("x-test-header"))

	// Test Keys
	carrier.Set("X-Another-Header", "another-value")
	keys := carrier.Keys()
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, "X-Test-Header")
	assert.Contains(t, keys, "X-Another-Header")

	// Test empty value
	assert.Equal(t, "", carrier.Get("X-Nonexistent"))
}

func TestFiberHeaderCarrier(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		carrier := FiberHeaderCarrier{ctx: c}

		// Test Get from request headers
		assert.Equal(t, "test-value", carrier.Get("X-Test-Header"))
		assert.Equal(t, "another-value", carrier.Get("X-Another-Header"))

		// Test Set (for response headers)
		carrier.Set("X-Response-Header", "response-value")
		assert.Equal(t, "response-value", c.Get("X-Response-Header"))

		// Test Keys
		keys := carrier.Keys()
		assert.Greater(t, len(keys), 0)

		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Test-Header", "test-value")
	req.Header.Set("X-Another-Header", "another-value")

	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestExtractTraceContext(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	// Create headers with trace context
	headers := make(http.Header)
	headers.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	headers.Set("X-Request-ID", "req-123")

	ctx := context.Background()
	extractedCtx := propagator.ExtractTraceContext(ctx, headers)

	// Verify context is different (contains trace info)
	assert.NotEqual(t, ctx, extractedCtx)

	// Check metrics
	metrics := propagator.GetPropagationMetrics()
	assert.Equal(t, int64(1), metrics.TotalExtractions)
	assert.Equal(t, int64(1), metrics.SuccessfulExtractions)
	assert.Equal(t, int64(0), metrics.FailedExtractions)
}

func TestExtractTraceContextFromFiber(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	var extractedCtx context.Context
	var bag baggage.Baggage

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		ctx := context.Background()
		extractedCtx = propagator.ExtractTraceContextFromFiber(ctx, c)

		// Check baggage for custom headers
		bag = baggage.FromContext(extractedCtx)

		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("X-Request-ID", "req-456")
	req.Header.Set("X-User-ID", "user-789")

	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify context is different
	assert.NotEqual(t, context.Background(), extractedCtx)

	// Check baggage for custom headers
	assert.NotNil(t, bag)

	// Check metrics
	metrics := propagator.GetPropagationMetrics()
	assert.Equal(t, int64(1), metrics.TotalExtractions)
	assert.Equal(t, int64(1), metrics.SuccessfulExtractions)
	assert.Greater(t, metrics.BaggageItemsExtracted, int64(0))
	assert.Greater(t, metrics.CustomHeadersProcessed, int64(0))
}

func TestInjectTraceContext(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	// Create context with trace
	ctx, span := propagator.CreateSpanWithPropagation(context.Background(), "test-span")
	defer span.End()

	headers := make(http.Header)
	err := propagator.InjectTraceContext(ctx, headers)
	assert.NoError(t, err)

	// Check that trace headers were injected
	assert.NotEmpty(t, headers.Get("traceparent"))

	// Check metrics
	metrics := propagator.GetPropagationMetrics()
	assert.Equal(t, int64(1), metrics.TotalInjections)
	assert.Equal(t, int64(1), metrics.SuccessfulInjections)
}

func TestInjectTraceContextToFiber(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	var traceparentHeader string

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		// Create context with trace
		ctx, span := propagator.CreateSpanWithPropagation(context.Background(), "test-span")
		defer span.End()

		err := propagator.InjectTraceContextToFiber(ctx, c)
		require.NoError(t, err)

		// Check that trace headers were injected
		traceparentHeader = c.Get("traceparent")

		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Check that trace headers were injected
	assert.NotEmpty(t, traceparentHeader)

	// Check metrics
	metrics := propagator.GetPropagationMetrics()
	assert.Equal(t, int64(1), metrics.TotalInjections)
	assert.Equal(t, int64(1), metrics.SuccessfulInjections)
}

func TestCreateSpanWithPropagation(t *testing.T) {
	config := DefaultTracePropagationConfig()
	config.DefaultSpanKind = "server"
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	ctx := context.Background()
	spanCtx, span := propagator.CreateSpanWithPropagation(ctx, "test-operation")
	defer span.End()

	// Verify span context is different
	assert.NotEqual(t, ctx, spanCtx)

	// Verify span is recording
	assert.True(t, span.IsRecording())

	// Verify span context contains the span
	extractedSpan := trace.SpanFromContext(spanCtx)
	assert.Equal(t, span, extractedSpan)
}

func TestCreateSpanWithPropagationDifferentKinds(t *testing.T) {
	logger := &log.GoLogger{}

	testCases := []struct {
		name         string
		spanKind     string
		expectedKind trace.SpanKind
	}{
		{"server_kind", "server", trace.SpanKindServer},
		{"client_kind", "client", trace.SpanKindClient},
		{"producer_kind", "producer", trace.SpanKindProducer},
		{"consumer_kind", "consumer", trace.SpanKindConsumer},
		{"internal_kind", "internal", trace.SpanKindInternal},
		{"unknown_kind", "unknown", trace.SpanKindInternal}, // defaults to internal
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultTracePropagationConfig()
			config.DefaultSpanKind = tc.spanKind
			propagator := NewTracePropagator(config, logger)

			ctx := context.Background()
			_, span := propagator.CreateSpanWithPropagation(ctx, "test-operation")
			defer span.End()

			// Note: We can't directly verify span kind from the public API,
			// but we can verify the span was created successfully
			assert.True(t, span.IsRecording())
		})
	}
}

func TestCreateLinkedSpan(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	// Create a parent span first
	ctx, parentSpan := propagator.CreateSpanWithPropagation(context.Background(), "parent-span")
	defer parentSpan.End()

	// Create links
	links := []trace.Link{
		{SpanContext: parentSpan.SpanContext()},
	}

	// Create linked span
	_, linkedSpan := propagator.CreateLinkedSpan(ctx, "linked-span", links)
	defer linkedSpan.End()

	assert.True(t, linkedSpan.IsRecording())
}

func TestPropagateError(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	ctx, span := propagator.CreateSpanWithPropagation(context.Background(), "test-span")
	defer span.End()

	testError := assert.AnError
	propagator.PropagateError(ctx, testError, "Test error occurred")

	// The span should still be recording
	assert.True(t, span.IsRecording())
}

func TestCustomHeaderPropagation(t *testing.T) {
	config := DefaultTracePropagationConfig()
	config.CustomHeaders = []string{"X-Custom-Header", "X-Another-Header"}
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	// Test extraction
	headers := make(http.Header)
	headers.Set("X-Custom-Header", "custom-value")
	headers.Set("X-Another-Header", "another-value")

	ctx := propagator.ExtractTraceContext(context.Background(), headers)

	// Check baggage
	bag := baggage.FromContext(ctx)
	assert.NotNil(t, bag)

	// Test injection
	outHeaders := make(http.Header)
	err := propagator.InjectTraceContext(ctx, outHeaders)
	assert.NoError(t, err)

	// Custom headers should be propagated through baggage
	// Note: The exact behavior depends on baggage implementation
}

func TestBaggageKeyToHeader(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	testCases := []struct {
		baggageKey string
		expected   string
	}{
		{"x-request-id", "X-Request-Id"},
		{"x-correlation-id", "X-Correlation-Id"},
		{"user-id", "User-Id"},
		{"simple", "Simple"},
	}

	for _, tc := range testCases {
		t.Run(tc.baggageKey, func(t *testing.T) {
			result := propagator.baggageKeyToHeader(tc.baggageKey)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsCustomHeader(t *testing.T) {
	config := DefaultTracePropagationConfig()
	config.CustomHeaders = []string{"X-Request-ID", "X-User-ID", "X-Correlation-ID"}
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	testCases := []struct {
		header   string
		expected bool
	}{
		{"X-Request-ID", true},
		{"x-request-id", true}, // case insensitive
		{"X-USER-ID", true},
		{"X-Unknown-Header", false},
		{"Content-Type", false},
	}

	for _, tc := range testCases {
		t.Run(tc.header, func(t *testing.T) {
			result := propagator.isCustomHeader(tc.header)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestPropagationMetrics(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	// Initial metrics should be zero
	metrics := propagator.GetPropagationMetrics()
	assert.Equal(t, int64(0), metrics.TotalExtractions)
	assert.Equal(t, int64(0), metrics.SuccessfulExtractions)
	assert.Equal(t, int64(0), metrics.TotalInjections)
	assert.Equal(t, int64(0), metrics.SuccessfulInjections)

	// Perform extraction
	headers := make(http.Header)
	headers.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := propagator.ExtractTraceContext(context.Background(), headers)

	// Check extraction metrics
	metrics = propagator.GetPropagationMetrics()
	assert.Equal(t, int64(1), metrics.TotalExtractions)
	assert.Equal(t, int64(1), metrics.SuccessfulExtractions)

	// Perform injection
	outHeaders := make(http.Header)
	err := propagator.InjectTraceContext(ctx, outHeaders)
	assert.NoError(t, err)

	// Check injection metrics
	metrics = propagator.GetPropagationMetrics()
	assert.Equal(t, int64(1), metrics.TotalInjections)
	assert.Equal(t, int64(1), metrics.SuccessfulInjections)
}

func TestMiddleware(t *testing.T) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	app := fiber.New()
	app.Use(propagator.Middleware())

	app.Get("/test", func(c *fiber.Ctx) error {
		// Verify that span context is available
		span := trace.SpanFromContext(c.UserContext())
		assert.True(t, span.IsRecording())
		return c.SendString("OK")
	})

	// This would require HTTP test setup to fully test
	// For now, we just verify the middleware can be created
	assert.NotNil(t, propagator.Middleware())
}

func TestAutoExtractionDisabled(t *testing.T) {
	config := DefaultTracePropagationConfig()
	config.AutoExtractSpanContext = false
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	headers := make(http.Header)
	headers.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	ctx := context.Background()
	extractedCtx := propagator.ExtractTraceContext(ctx, headers)

	// Context should be unchanged when auto-extraction is disabled
	assert.Equal(t, ctx, extractedCtx)
}

func TestAutoInjectionDisabled(t *testing.T) {
	config := DefaultTracePropagationConfig()
	config.AutoInjectSpanContext = false
	logger := &log.GoLogger{}

	propagator := NewTracePropagator(config, logger)

	ctx, span := propagator.CreateSpanWithPropagation(context.Background(), "test-span")
	defer span.End()

	headers := make(http.Header)
	err := propagator.InjectTraceContext(ctx, headers)
	assert.NoError(t, err)

	// Headers should be empty when auto-injection is disabled
	assert.Empty(t, headers.Get("traceparent"))
}

// Benchmark tests
func BenchmarkExtractTraceContext(b *testing.B) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}
	propagator := NewTracePropagator(config, logger)

	headers := make(http.Header)
	headers.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	headers.Set("X-Request-ID", "req-123")

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = propagator.ExtractTraceContext(ctx, headers)
	}
}

func BenchmarkInjectTraceContext(b *testing.B) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}
	propagator := NewTracePropagator(config, logger)

	ctx, span := propagator.CreateSpanWithPropagation(context.Background(), "test-span")
	defer span.End()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		headers := make(http.Header)
		_ = propagator.InjectTraceContext(ctx, headers)
	}
}

func BenchmarkCreateSpanWithPropagation(b *testing.B) {
	config := DefaultTracePropagationConfig()
	logger := &log.GoLogger{}
	propagator := NewTracePropagator(config, logger)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, span := propagator.CreateSpanWithPropagation(ctx, "benchmark-span")
		span.End()
	}
}
