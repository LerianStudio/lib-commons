package integration

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	httpCommons "github.com/LerianStudio/lib-commons/commons/net/http"
	"github.com/LerianStudio/lib-commons/commons/observability"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// ObservabilityIntegrationTestSuite tests observability integration across components
type ObservabilityIntegrationTestSuite struct {
	suite.Suite
	ctx      context.Context
	provider observability.Provider
	app      *fiber.App
}

func TestObservabilityIntegrationSuite(t *testing.T) {
	// Skip integration tests if not in integration test mode
	if os.Getenv("INTEGRATION_TESTS") != "true" {
		t.Skip("Skipping integration tests. Set INTEGRATION_TESTS=true to run.")
	}

	suite.Run(t, new(ObservabilityIntegrationTestSuite))
}

func (suite *ObservabilityIntegrationTestSuite) SetupSuite() {
	suite.ctx = context.Background()

	// Create observability provider configuration for testing
	config := observability.ProviderConfig{
		ServiceName:    "integration-test-service",
		ServiceVersion: "1.0.0",
		Environment:    "test",
		MetricsEnabled: true,
		TracingEnabled: true,
		// Note: No OTLP endpoint for test to avoid external dependencies
	}

	// Initialize observability provider
	var err error
	suite.provider, err = observability.NewProvider(config)
	require.NoError(suite.T(), err, "Failed to create observability provider")

	// Start the provider
	err = suite.provider.Start(suite.ctx)
	require.NoError(suite.T(), err, "Failed to start observability provider")

	// Setup Fiber app with observability middleware
	suite.app = fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
}

func (suite *ObservabilityIntegrationTestSuite) TearDownSuite() {
	// Stop observability provider
	if suite.provider != nil {
		err := suite.provider.Stop(suite.ctx)
		assert.NoError(suite.T(), err, "Failed to stop observability provider")
	}
}

func (suite *ObservabilityIntegrationTestSuite) TestObservabilityProviderIntegration() {
	suite.Run("provider_initialization", func() {
		// Test provider components are initialized
		assert.NotNil(suite.T(), suite.provider)

		tracerProvider := suite.provider.GetTracerProvider()
		assert.NotNil(suite.T(), tracerProvider, "Tracer provider should be initialized")

		meterProvider := suite.provider.GetMeterProvider()
		assert.NotNil(suite.T(), meterProvider, "Meter provider should be initialized")
	})

	suite.Run("provider_lifecycle", func() {
		// Test provider can be started and stopped multiple times
		testConfig := observability.ProviderConfig{
			ServiceName:    "lifecycle-test",
			ServiceVersion: "1.0.0",
			Environment:    "test",
		}

		testProvider, err := observability.NewProvider(testConfig)
		require.NoError(suite.T(), err)

		// Start
		err = testProvider.Start(suite.ctx)
		assert.NoError(suite.T(), err)

		// Stop
		err = testProvider.Stop(suite.ctx)
		assert.NoError(suite.T(), err)

		// Start again
		err = testProvider.Start(suite.ctx)
		assert.NoError(suite.T(), err)

		// Final stop
		err = testProvider.Stop(suite.ctx)
		assert.NoError(suite.T(), err)
	})
}

func (suite *ObservabilityIntegrationTestSuite) TestHTTPMiddlewareIntegration() {
	suite.Run("telemetry_middleware_integration", func() {
		// Setup routes with telemetry middleware
		app := fiber.New(fiber.Config{DisableStartupMessage: true})

		// Add telemetry middleware
		app.Use(httpCommons.WithTelemetry(suite.provider))

		// Add test routes
		app.Get("/test", func(c *fiber.Ctx) error {
			return httpCommons.OK(c, map[string]string{"message": "test"})
		})

		app.Get("/error", func(c *fiber.Ctx) error {
			return httpCommons.InternalServerError(
				c,
				"TEST_001",
				"Test Error",
				"This is a test error",
			)
		})

		// Test successful request
		req := httptest.NewRequest("GET", "/test", nil)
		resp, err := app.Test(req)
		require.NoError(suite.T(), err)
		assert.Equal(suite.T(), 200, resp.StatusCode)

		// Test error request
		req = httptest.NewRequest("GET", "/error", nil)
		resp, err = app.Test(req)
		require.NoError(suite.T(), err)
		assert.Equal(suite.T(), 500, resp.StatusCode)
	})

	suite.Run("logging_middleware_integration", func() {
		// Setup routes with logging middleware
		app := fiber.New(fiber.Config{DisableStartupMessage: true})

		// Add logging middleware
		app.Use(httpCommons.WithHTTPLogging())

		// Add telemetry for correlation
		app.Use(httpCommons.WithTelemetry(suite.provider))

		app.Get("/logged", func(c *fiber.Ctx) error {
			return httpCommons.OK(c, map[string]string{"message": "logged request"})
		})

		// Test request with logging
		req := httptest.NewRequest("GET", "/logged", nil)
		resp, err := app.Test(req)
		require.NoError(suite.T(), err)
		assert.Equal(suite.T(), 200, resp.StatusCode)

		// Check for correlation headers
		assert.NotEmpty(suite.T(), resp.Header.Get("X-Request-ID"))
	})

	suite.Run("middleware_stack_integration", func() {
		// Test full middleware stack integration
		app := fiber.New(fiber.Config{DisableStartupMessage: true})

		// Add middleware in correct order
		app.Use(httpCommons.WithCORS())
		app.Use(httpCommons.WithHTTPLogging())
		app.Use(httpCommons.WithTelemetry(suite.provider))

		app.Get("/stack", func(c *fiber.Ctx) error {
			// Verify context has observability data
			ctx := c.Context()
			assert.NotNil(suite.T(), ctx)

			return httpCommons.OK(c, map[string]string{
				"message": "middleware stack test",
				"status":  "success",
			})
		})

		// Test request through full stack
		req := httptest.NewRequest("GET", "/stack", nil)
		req.Header.Set("Origin", "https://example.com")

		resp, err := app.Test(req)
		require.NoError(suite.T(), err)
		assert.Equal(suite.T(), 200, resp.StatusCode)

		// Verify CORS headers
		assert.NotEmpty(suite.T(), resp.Header.Get("Access-Control-Allow-Origin"))
	})
}

func (suite *ObservabilityIntegrationTestSuite) TestContextPropagationIntegration() {
	suite.Run("context_with_request_id", func() {
		// Test request ID context propagation
		ctx := suite.ctx
		requestID := "test-request-id-123"

		// Add request ID to context
		ctxWithID := observability.ContextWithHeaderID(ctx, requestID)

		// Retrieve request ID from context
		retrievedID := observability.NewHeaderIDFromContext(ctxWithID)
		assert.Equal(suite.T(), requestID, retrievedID)

		// Original context should not be modified
		originalID := observability.NewHeaderIDFromContext(ctx)
		assert.Empty(suite.T(), originalID)
	})

	suite.Run("context_with_tracer", func() {
		// Test tracer context propagation
		ctx := suite.ctx

		// Get tracer from provider
		tracerProvider := suite.provider.GetTracerProvider()
		tracer := tracerProvider.Tracer("test-tracer")

		// Add tracer to context
		ctxWithTracer := observability.ContextWithTracer(ctx, tracer)

		// Retrieve tracer from context
		retrievedTracer := observability.NewTracerFromContext(ctxWithTracer)
		assert.NotNil(suite.T(), retrievedTracer)

		// Create span to test tracer functionality
		spanCtx, span := retrievedTracer.Start(ctxWithTracer, "test-span")
		assert.NotNil(suite.T(), spanCtx)
		assert.NotNil(suite.T(), span)
		span.End()
	})

	suite.Run("context_with_logger", func() {
		// Test logger context propagation
		ctx := suite.ctx

		// Add logger to context (nil for test)
		ctxWithLogger := observability.ContextWithLogger(ctx, nil)

		// Retrieve logger from context
		logger := observability.NewLoggerFromContext(ctxWithLogger)
		// Logger may be nil in test environment
		_ = logger // Just test that retrieval doesn't panic
	})
}

func (suite *ObservabilityIntegrationTestSuite) TestDistributedTracingIntegration() {
	suite.Run("span_creation_and_attributes", func() {
		// Get tracer from provider
		tracerProvider := suite.provider.GetTracerProvider()
		tracer := tracerProvider.Tracer("integration-test")

		// Create parent span
		ctx, parentSpan := tracer.Start(suite.ctx, "parent-operation")
		defer parentSpan.End()

		// Add attributes to parent span
		parentSpan.SetAttributes(
			observability.KeyServiceName.String("integration-test-service"),
			observability.KeyServiceVersion.String("1.0.0"),
			observability.KeyEnvironment.String("test"),
		)

		// Create child span
		_, childSpan := tracer.Start(ctx, "child-operation")
		defer childSpan.End()

		// Add attributes to child span
		childSpan.SetAttributes(
			observability.KeyHTTPMethod.String("GET"),
			observability.KeyHTTPPath.String("/test"),
			observability.KeyHTTPStatusCode.Int(200),
		)

		// Test spans are created without errors
		assert.NotNil(suite.T(), parentSpan)
		assert.NotNil(suite.T(), childSpan)
	})

	suite.Run("error_recording", func() {
		// Get tracer from provider
		tracerProvider := suite.provider.GetTracerProvider()
		tracer := tracerProvider.Tracer("error-test")

		// Create span
		_, span := tracer.Start(suite.ctx, "error-operation")
		defer span.End()

		// Record error
		testError := assert.AnError
		span.RecordError(testError)

		// Set error status
		span.SetStatus(observability.StatusError, "operation failed")

		// Test span handles error recording
		assert.NotNil(suite.T(), span)
	})
}

func (suite *ObservabilityIntegrationTestSuite) TestMetricsIntegration() {
	suite.Run("meter_provider_access", func() {
		// Get meter provider
		meterProvider := suite.provider.GetMeterProvider()
		assert.NotNil(suite.T(), meterProvider)

		// Create meter
		meter := meterProvider.Meter("integration-test")
		assert.NotNil(suite.T(), meter)
	})

	suite.Run("counter_metrics", func() {
		// Get meter
		meterProvider := suite.provider.GetMeterProvider()
		meter := meterProvider.Meter("counter-test")

		// Create counter
		counter, err := meter.Int64Counter(
			"test_requests_total",
			// metric.WithDescription("Total test requests"),
		)
		require.NoError(suite.T(), err)

		// Record counter increment
		counter.Add(suite.ctx, 1)

		// Test counter creation and usage
		assert.NotNil(suite.T(), counter)
	})

	suite.Run("histogram_metrics", func() {
		// Get meter
		meterProvider := suite.provider.GetMeterProvider()
		meter := meterProvider.Meter("histogram-test")

		// Create histogram
		histogram, err := meter.Float64Histogram(
			"test_request_duration_seconds",
			// metric.WithDescription("Test request duration"),
		)
		require.NoError(suite.T(), err)

		// Record histogram value
		histogram.Record(suite.ctx, 0.025) // 25ms

		// Test histogram creation and usage
		assert.NotNil(suite.T(), histogram)
	})
}

func (suite *ObservabilityIntegrationTestSuite) TestObservabilityErrorHandling() {
	suite.Run("provider_with_invalid_config", func() {
		// Test provider creation with invalid configuration
		invalidConfig := observability.ProviderConfig{
			// Missing required fields
		}

		provider, err := observability.NewProvider(invalidConfig)
		if err != nil {
			// Provider should handle invalid config gracefully
			assert.Error(suite.T(), err)
			assert.Nil(suite.T(), provider)
		} else {
			// Or provide defaults
			assert.NotNil(suite.T(), provider)
			// Clean up if created
			provider.Stop(suite.ctx)
		}
	})

	suite.Run("middleware_with_nil_provider", func() {
		// Test middleware behavior with nil provider
		app := fiber.New(fiber.Config{DisableStartupMessage: true})

		// Add telemetry middleware with nil provider
		app.Use(httpCommons.WithTelemetry(nil))

		app.Get("/nil-provider", func(c *fiber.Ctx) error {
			return httpCommons.OK(c, map[string]string{"message": "test"})
		})

		// Request should still work (middleware should handle nil gracefully)
		req := httptest.NewRequest("GET", "/nil-provider", nil)
		resp, err := app.Test(req)
		require.NoError(suite.T(), err)
		assert.Equal(suite.T(), 200, resp.StatusCode)
	})
}

func (suite *ObservabilityIntegrationTestSuite) TestPerformanceIntegration() {
	suite.Run("middleware_performance_overhead", func() {
		// Test performance overhead of observability middleware
		app := fiber.New(fiber.Config{DisableStartupMessage: true})

		// Route without middleware
		app.Get("/no-middleware", func(c *fiber.Ctx) error {
			return httpCommons.OK(c, map[string]string{"message": "no middleware"})
		})

		// Route with full observability stack
		observabilityApp := fiber.New(fiber.Config{DisableStartupMessage: true})
		observabilityApp.Use(httpCommons.WithHTTPLogging())
		observabilityApp.Use(httpCommons.WithTelemetry(suite.provider))
		observabilityApp.Get("/with-middleware", func(c *fiber.Ctx) error {
			return httpCommons.OK(c, map[string]string{"message": "with middleware"})
		})

		// Benchmark basic request
		start := time.Now()
		req := httptest.NewRequest("GET", "/no-middleware", nil)
		_, err := app.Test(req)
		baselineDuration := time.Since(start)
		require.NoError(suite.T(), err)

		// Benchmark request with middleware
		start = time.Now()
		req = httptest.NewRequest("GET", "/with-middleware", nil)
		_, err = observabilityApp.Test(req)
		middlewareDuration := time.Since(start)
		require.NoError(suite.T(), err)

		// Log performance comparison
		suite.T().Logf("Baseline request: %v", baselineDuration)
		suite.T().Logf("Middleware request: %v", middlewareDuration)
		suite.T().Logf("Overhead: %v", middlewareDuration-baselineDuration)

		// Overhead should be reasonable (less than 10x baseline)
		assert.Less(suite.T(), middlewareDuration.Nanoseconds(),
			baselineDuration.Nanoseconds()*10,
			"Middleware overhead should be reasonable")
	})
}

// BenchmarkObservabilityIntegration benchmarks observability components
func BenchmarkObservabilityIntegration(b *testing.B) {
	if os.Getenv("INTEGRATION_TESTS") != "true" {
		b.Skip("Skipping integration benchmarks. Set INTEGRATION_TESTS=true to run.")
	}

	ctx := context.Background()

	// Setup provider for benchmarks
	config := observability.ProviderConfig{
		ServiceName:    "benchmark-test",
		ServiceVersion: "1.0.0",
		Environment:    "test",
		MetricsEnabled: true,
		TracingEnabled: true,
	}

	provider, err := observability.NewProvider(config)
	if err != nil {
		b.Fatal(err)
	}

	err = provider.Start(ctx)
	if err != nil {
		b.Fatal(err)
	}
	defer provider.Stop(ctx)

	b.Run("span_creation", func(b *testing.B) {
		tracerProvider := provider.GetTracerProvider()
		tracer := tracerProvider.Tracer("benchmark")

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, span := tracer.Start(ctx, "benchmark-span")
			span.End()
		}
	})

	b.Run("metric_recording", func(b *testing.B) {
		meterProvider := provider.GetMeterProvider()
		meter := meterProvider.Meter("benchmark")

		counter, _ := meter.Int64Counter("benchmark_counter")

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			counter.Add(ctx, 1)
		}
	})

	b.Run("context_propagation", func(b *testing.B) {
		requestID := "benchmark-request-id"

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ctxWithID := observability.ContextWithHeaderID(ctx, requestID)
			_ = observability.NewHeaderIDFromContext(ctxWithID)
		}
	})
}
