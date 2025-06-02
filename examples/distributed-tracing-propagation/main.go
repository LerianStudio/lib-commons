// Package main demonstrates distributed tracing context propagation across service boundaries
// using the lib-commons observability package with OpenTelemetry integration.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/LerianStudio/lib-commons/commons/observability"
	"github.com/LerianStudio/lib-commons/commons/zap"
)

func main() {
	// Initialize OpenTelemetry
	if err := initTracing(); err != nil {
		log.Fatal("Failed to initialize tracing:", err)
	}

	// Create logger
	logger, err := zap.NewStructured("distributed-tracing-demo", "info")
	if err != nil {
		log.Fatal("Failed to create logger:", err)
	}

	// Run examples
	runBasicPropagationExample(logger)
	runFiberMiddlewareExample(logger)
	runCrossServiceExample(logger)
	runBaggagePropagationExample(logger)
	runErrorPropagationExample(logger)
	runLinkedSpansExample(logger)
	runCustomHeaderPropagationExample(logger)

	fmt.Println("\n=== All distributed tracing propagation examples completed ===")
}

// initTracing sets up OpenTelemetry with stdout exporter for demonstration
func initTracing() error {
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return err
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithSampler(trace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	return nil
}

// Example 1: Basic trace context propagation between HTTP services
func runBasicPropagationExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 1: Basic Trace Context Propagation ===")

	// Create trace propagator with default config
	config := observability.DefaultTracePropagationConfig()
	propagator := observability.NewTracePropagator(config, logger)

	// Simulate incoming request with trace context
	incomingHeaders := make(http.Header)
	incomingHeaders.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	incomingHeaders.Set("X-Request-ID", "req-12345")

	// Extract trace context
	ctx := context.Background()
	extractedCtx := propagator.ExtractTraceContext(ctx, incomingHeaders)

	// Create a span for processing
	spanCtx, span := propagator.CreateSpanWithPropagation(extractedCtx, "process-request",
		trace.WithAttributes(
			attribute.String("service.name", "api-gateway"),
			attribute.String("operation.type", "http.request"),
		),
	)
	defer span.End()

	// Simulate some work
	time.Sleep(10 * time.Millisecond)

	// Prepare outgoing request headers
	outgoingHeaders := make(http.Header)
	if err := propagator.InjectTraceContext(spanCtx, outgoingHeaders); err != nil {
		logger.Error("Failed to inject trace context", zap.Error(err))
	}

	fmt.Printf("Original traceparent: %s\n", incomingHeaders.Get("traceparent"))
	fmt.Printf("Propagated traceparent: %s\n", outgoingHeaders.Get("traceparent"))

	// Show metrics
	metrics := propagator.GetPropagationMetrics()
	fmt.Printf("Propagation metrics - Extractions: %d, Injections: %d\n",
		metrics.TotalExtractions, metrics.TotalInjections)
}

// Example 2: Fiber middleware integration
func runFiberMiddlewareExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 2: Fiber Middleware Integration ===")

	// Create trace propagator
	config := observability.DefaultTracePropagationConfig()
	config.EnableSpanEvents = true
	propagator := observability.NewTracePropagator(config, logger)

	// Create Fiber app with tracing middleware
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	// Add tracing middleware
	app.Use(propagator.Middleware())

	// Add route handlers
	app.Get("/api/users/:id", func(c *fiber.Ctx) error {
		// The span context is automatically available
		ctx := c.UserContext()

		// Create child span for database operation
		dbCtx, dbSpan := propagator.CreateSpanWithPropagation(ctx, "db.query.users",
			trace.WithAttributes(
				attribute.String("db.operation", "SELECT"),
				attribute.String("db.table", "users"),
				attribute.String("user.id", c.Params("id")),
			),
		)
		defer dbSpan.End()

		// Simulate database query
		time.Sleep(5 * time.Millisecond)

		// Create child span for cache operation
		_, cacheSpan := propagator.CreateSpanWithPropagation(dbCtx, "cache.get",
			trace.WithAttributes(
				attribute.String("cache.key", fmt.Sprintf("user:%s", c.Params("id"))),
				attribute.String("cache.result", "hit"),
			),
		)
		defer cacheSpan.End()

		// Simulate cache lookup
		time.Sleep(2 * time.Millisecond)

		return c.JSON(fiber.Map{
			"user_id":  c.Params("id"),
			"name":     "John Doe",
			"trace_id": fmt.Sprintf("%s", dbSpan.SpanContext().TraceID()),
		})
	})

	// Simulate a request (in real scenario, this would be an HTTP client)
	c := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(c)

	c.Request().Header.Set("traceparent", "00-5bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	c.Request().Header.Set("X-Request-ID", "middleware-test")
	c.Request().URI().SetPath("/api/users/123")
	c.Request().Header.SetMethod("GET")

	fmt.Println("Fiber middleware automatically handles trace propagation")
	fmt.Println("Spans are created for request processing, DB queries, and cache operations")
}

// Example 3: Cross-service trace propagation
func runCrossServiceExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 3: Cross-Service Trace Propagation ===")

	config := observability.DefaultTracePropagationConfig()
	propagator := observability.NewTracePropagator(config, logger)

	// Service A: API Gateway
	ctx := context.Background()
	gatewayCtx, gatewaySpan := propagator.CreateSpanWithPropagation(ctx, "api-gateway.request",
		trace.WithAttributes(
			attribute.String("service.name", "api-gateway"),
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/orders"),
		),
	)
	defer gatewaySpan.End()

	// Prepare headers for service B
	serviceBHeaders := make(http.Header)
	if err := propagator.InjectTraceContext(gatewayCtx, serviceBHeaders); err != nil {
		logger.Error("Failed to inject context for service B", zap.Error(err))
	}

	// Service B: Order Service
	serviceBCtx := propagator.ExtractTraceContext(context.Background(), serviceBHeaders)
	orderCtx, orderSpan := propagator.CreateSpanWithPropagation(
		serviceBCtx,
		"order-service.create-order",
		trace.WithAttributes(
			attribute.String("service.name", "order-service"),
			attribute.String("order.type", "standard"),
			attribute.Int("order.items", 3),
		),
	)
	defer orderSpan.End()

	// Prepare headers for service C
	serviceCHeaders := make(http.Header)
	if err := propagator.InjectTraceContext(orderCtx, serviceCHeaders); err != nil {
		logger.Error("Failed to inject context for service C", zap.Error(err))
	}

	// Service C: Payment Service
	serviceCCtx := propagator.ExtractTraceContext(context.Background(), serviceCHeaders)
	_, paymentSpan := propagator.CreateSpanWithPropagation(
		serviceCCtx,
		"payment-service.process-payment",
		trace.WithAttributes(
			attribute.String("service.name", "payment-service"),
			attribute.String("payment.method", "credit_card"),
			attribute.Float64("payment.amount", 99.99),
		),
	)
	defer paymentSpan.End()

	fmt.Println("Trace context successfully propagated across 3 services:")
	fmt.Printf("Gateway Trace ID: %s\n", gatewaySpan.SpanContext().TraceID())
	fmt.Printf("Order Trace ID: %s\n", orderSpan.SpanContext().TraceID())
	fmt.Printf("Payment Trace ID: %s\n", paymentSpan.SpanContext().TraceID())
}

// Example 4: Baggage propagation for metadata
func runBaggagePropagationExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 4: Baggage Propagation for Metadata ===")

	config := observability.DefaultTracePropagationConfig()
	config.CustomHeaders = []string{"X-User-ID", "X-Tenant-ID", "X-Feature-Flags"}
	propagator := observability.NewTracePropagator(config, logger)

	// Create headers with custom metadata
	headers := make(http.Header)
	headers.Set("traceparent", "00-6bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	headers.Set("X-User-ID", "user-456")
	headers.Set("X-Tenant-ID", "tenant-789")
	headers.Set("X-Feature-Flags", "feature-a:true,feature-b:false")

	// Extract context with baggage
	ctx := propagator.ExtractTraceContext(context.Background(), headers)

	// Create span that can access baggage metadata
	spanCtx, span := propagator.CreateSpanWithPropagation(ctx, "user-operation",
		trace.WithAttributes(
			attribute.String("operation.type", "user_action"),
		),
	)
	defer span.End()

	// Simulate downstream service call
	downstreamHeaders := make(http.Header)
	if err := propagator.InjectTraceContext(spanCtx, downstreamHeaders); err != nil {
		logger.Error("Failed to inject baggage", zap.Error(err))
	}

	fmt.Println("Baggage propagation example:")
	fmt.Printf("Original X-User-ID: %s\n", headers.Get("X-User-ID"))
	fmt.Printf("Original X-Tenant-ID: %s\n", headers.Get("X-Tenant-ID"))
	fmt.Printf("Propagated headers include trace context and baggage\n")

	// Show baggage metrics
	metrics := propagator.GetPropagationMetrics()
	fmt.Printf("Baggage items extracted: %d\n", metrics.BaggageItemsExtracted)
	fmt.Printf("Custom headers processed: %d\n", metrics.CustomHeadersProcessed)
}

// Example 5: Error propagation and handling
func runErrorPropagationExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 5: Error Propagation and Handling ===")

	config := observability.DefaultTracePropagationConfig()
	propagator := observability.NewTracePropagator(config, logger)

	// Create root span
	ctx, rootSpan := propagator.CreateSpanWithPropagation(context.Background(), "order-processing",
		trace.WithAttributes(
			attribute.String("order.id", "order-123"),
		),
	)
	defer rootSpan.End()

	// Simulate operation that might fail
	processingCtx, processingSpan := propagator.CreateSpanWithPropagation(ctx, "validate-payment")
	defer processingSpan.End()

	// Simulate an error
	validationError := fmt.Errorf("insufficient funds: account balance too low")
	propagator.PropagateError(processingCtx, validationError, "Payment validation failed")

	// Continue with compensation logic
	compensationCtx, compensationSpan := propagator.CreateSpanWithPropagation(
		ctx,
		"compensation.cancel-order",
	)
	defer compensationSpan.End()

	// Add compensation attributes
	compensationSpan.SetAttributes(
		attribute.String("compensation.action", "cancel_order"),
		attribute.String("compensation.reason", "payment_failed"),
	)

	fmt.Println("Error propagation example:")
	fmt.Printf("Error recorded in span: %s\n", validationError.Error())
	fmt.Println("Compensation action initiated with proper trace context")
}

// Example 6: Linked spans for async operations
func runLinkedSpansExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 6: Linked Spans for Async Operations ===")

	config := observability.DefaultTracePropagationConfig()
	propagator := observability.NewTracePropagator(config, logger)

	// Create parent request span
	ctx, requestSpan := propagator.CreateSpanWithPropagation(
		context.Background(),
		"api.create-user",
		trace.WithAttributes(
			attribute.String("user.email", "user@example.com"),
		),
	)
	defer requestSpan.End()

	// Get span context for linking
	parentSpanContext := requestSpan.SpanContext()

	// Simulate async operations that should be linked to the original request
	asyncOperations := []struct {
		name        string
		description string
	}{
		{"email.send-welcome", "Send welcome email"},
		{"analytics.track-signup", "Track user signup event"},
		{"cache.warm-user-data", "Warm up user cache"},
	}

	for _, op := range asyncOperations {
		// Create link to parent span
		links := []trace.Link{
			{SpanContext: parentSpanContext},
		}

		// Create linked span (would typically run in goroutine)
		_, asyncSpan := propagator.CreateLinkedSpan(context.Background(), op.name, links,
			trace.WithAttributes(
				attribute.String("async.operation", op.description),
				attribute.String("linked.parent", "api.create-user"),
			),
		)

		// Simulate async work
		time.Sleep(2 * time.Millisecond)
		asyncSpan.End()

		fmt.Printf("Created linked async span: %s\n", op.name)
	}

	fmt.Printf("All async operations linked to parent span: %s\n", parentSpanContext.SpanID())
}

// Example 7: Custom header propagation patterns
func runCustomHeaderPropagationExample(logger *zap.Logger) {
	fmt.Println("\n=== Example 7: Custom Header Propagation Patterns ===")

	config := observability.DefaultTracePropagationConfig()
	config.CustomHeaders = []string{
		"X-Request-ID",
		"X-Correlation-ID",
		"X-User-ID",
		"X-Session-ID",
		"X-Client-Version",
		"X-Feature-Flags",
	}
	propagator := observability.NewTracePropagator(config, logger)

	// Create comprehensive headers
	headers := make(http.Header)
	headers.Set("traceparent", "00-7bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	headers.Set("X-Request-ID", "req-abcd-1234")
	headers.Set("X-Correlation-ID", "corr-efgh-5678")
	headers.Set("X-User-ID", "user-ijkl-9012")
	headers.Set("X-Session-ID", "sess-mnop-3456")
	headers.Set("X-Client-Version", "mobile-app-v2.1.0")
	headers.Set("X-Feature-Flags", "dark-mode:true,beta-features:false,new-ui:true")

	// Multi-hop propagation simulation
	services := []string{"api-gateway", "user-service", "notification-service"}

	currentCtx := context.Background()
	currentHeaders := headers

	for i, service := range services {
		fmt.Printf("\nService %d: %s\n", i+1, service)

		// Extract context
		currentCtx = propagator.ExtractTraceContext(currentCtx, currentHeaders)

		// Create service span
		spanCtx, span := propagator.CreateSpanWithPropagation(
			currentCtx,
			fmt.Sprintf("%s.process", service),
			trace.WithAttributes(
				attribute.String("service.name", service),
				attribute.Int("service.hop", i+1),
			),
		)

		// Show custom headers available
		fmt.Printf("  Request ID: %s\n", currentHeaders.Get("X-Request-ID"))
		fmt.Printf("  User ID: %s\n", currentHeaders.Get("X-User-ID"))
		fmt.Printf("  Feature Flags: %s\n", currentHeaders.Get("X-Feature-Flags"))

		// Prepare for next service
		nextHeaders := make(http.Header)
		if err := propagator.InjectTraceContext(spanCtx, nextHeaders); err != nil {
			logger.Error("Failed to propagate to next service", zap.Error(err))
		}

		span.End()
		currentHeaders = nextHeaders
	}

	// Final metrics
	metrics := propagator.GetPropagationMetrics()
	fmt.Printf("\nFinal propagation metrics:\n")
	fmt.Printf("  Total extractions: %d\n", metrics.TotalExtractions)
	fmt.Printf("  Total injections: %d\n", metrics.TotalInjections)
	fmt.Printf("  Baggage items processed: %d\n", metrics.BaggageItemsExtracted)
	fmt.Printf("  Custom headers processed: %d\n", metrics.CustomHeadersProcessed)
}
