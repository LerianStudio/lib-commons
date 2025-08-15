package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons"
	"github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// setupTestTracer sets up a test tracer provider and returns it along with a span recorder
func setupTestTracer() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(spanRecorder),
	)
	
	// Set the global propagator to TraceContext
	otel.SetTextMapPropagator(propagation.TraceContext{})
	
	return tracerProvider, spanRecorder
}

// TestWithTelemetry tests the WithTelemetry middleware function
func TestWithTelemetry(t *testing.T) {
	tests := []struct {
		name               string
		path               string
		method             string
		setupHandler       func(c *fiber.Ctx) error
		nilTelemetry       bool
		traceparent        string
		expectedStatusCode int
		expectSpan         bool
		swaggerPath        bool
	}{
		{
			name:               "Basic middleware functionality",
			path:               "/api/resource",
			method:             "GET",
			setupHandler:       func(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) },
			expectedStatusCode: http.StatusOK,
			expectSpan:         true,
		},
		{
			name:               "Handler returns error",
			path:               "/api/resource",
			method:             "POST",
			setupHandler:       func(c *fiber.Ctx) error { return errors.New("handler error") },
			expectedStatusCode: http.StatusInternalServerError,
			expectSpan:         true,
		},
		{
			name:               "Nil telemetry",
			path:               "/api/resource",
			method:             "GET",
			setupHandler:       func(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) },
			nilTelemetry:       true,
			expectedStatusCode: http.StatusOK,
			expectSpan:         false,
		},
		{
			name:               "With trace context",
			path:               "/api/resource",
			method:             "GET",
			setupHandler:       func(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) },
			traceparent:        "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			expectedStatusCode: http.StatusOK,
			expectSpan:         true,
		},
		{
			name:               "UUID in path",
			path:               "/api/users/123e4567-e89b-12d3-a456-426614174000/profile",
			method:             "GET",
			setupHandler:       func(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) },
			expectedStatusCode: http.StatusOK,
			expectSpan:         true,
		},
		{
			name:               "Swagger path bypass",
			path:               "/swagger/api-docs",
			method:             "GET",
			setupHandler:       func(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) },
			expectedStatusCode: http.StatusOK,
			expectSpan:         false,
			swaggerPath:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			
			// Setup test tracer
			tp, spanRecorder := setupTestTracer()
			defer func() {
				_ = tp.Shutdown(ctx)
			}()
			
			// Replace the global tracer provider for this test
			oldTracerProvider := otel.GetTracerProvider()
			otel.SetTracerProvider(tp)
			defer otel.SetTracerProvider(oldTracerProvider)
			
			// Setup telemetry
			var telemetry *opentelemetry.Telemetry
			if !tt.nilTelemetry {
				telemetry = &opentelemetry.Telemetry{
					TelemetryConfig: opentelemetry.TelemetryConfig{
						LibraryName:     "test-library",
						EnableTelemetry: true,
					},
					TracerProvider: tp,
				}
			}

			// Create middleware
			middleware := NewTelemetryMiddleware(telemetry)

			// Create fiber app with error handler
			app := fiber.New(fiber.Config{
				ErrorHandler: func(c *fiber.Ctx, err error) error {
					return c.Status(http.StatusInternalServerError).SendString(err.Error())
				},
			})

			// Add middleware
			if !tt.nilTelemetry {
				if tt.swaggerPath {
					// For swagger paths, add them to excluded routes
					app.Use(middleware.WithTelemetry(telemetry, "/swagger"))
				} else {
					// For regular paths, no excluded routes
					app.Use(middleware.WithTelemetry(telemetry))
				}
			}

			// Add test route
			app.All(tt.path, func(c *fiber.Ctx) error {
				return tt.setupHandler(c)
			})

			// Create test request
			req, err := http.NewRequest(tt.method, tt.path, nil)
			require.NoError(t, err)

			// Add trace context if specified
			if tt.traceparent != "" {
				req.Header.Set("traceparent", tt.traceparent)
			}

			// Execute request
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Check status code
			assert.Equal(t, tt.expectedStatusCode, resp.StatusCode)
			
			// Check spans
			spans := spanRecorder.Ended()
			
			if tt.expectSpan && !tt.nilTelemetry && !tt.swaggerPath {
				// Should have created a span
				require.GreaterOrEqual(t, len(spans), 1, "Expected at least one span to be created")
				
				// Check span name
				expectedPath := tt.path
				if strings.Contains(tt.path, "123e4567-e89b-12d3-a456-426614174000") {
					expectedPath = commons.ReplaceUUIDWithPlaceholder(tt.path)
				}
				
				spanFound := false
				for _, span := range spans {
					if span.Name() == tt.method+" "+expectedPath {
						spanFound = true
						break
					}
				}
				assert.True(t, spanFound, "Expected span with name %s not found", tt.method+" "+expectedPath)
			} else if tt.swaggerPath || tt.nilTelemetry {
				// Should not have created a span for swagger paths or nil telemetry
				for _, span := range spans {
					assert.NotEqual(t, tt.method+" "+tt.path, span.Name(), "Should not have created a span for swagger path or nil telemetry")
				}
			}
		})
	}
}

// TestWithTelemetryExcludedRoutes tests the WithTelemetry middleware with excluded routes
func TestWithTelemetryExcludedRoutes(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		method         string
		excludedRoutes []string
		expectSpan     bool
	}{
		{
			name:           "Route not excluded",
			path:           "/api/users",
			method:         "GET",
			excludedRoutes: []string{"/swagger", "/health"},
			expectSpan:     true,
		},
		{
			name:           "Route excluded by exact match",
			path:           "/swagger/api-docs",
			method:         "GET",
			excludedRoutes: []string{"/swagger"},
			expectSpan:     false,
		},
		{
			name:           "Route excluded by partial match",
			path:           "/health/check",
			method:         "GET",
			excludedRoutes: []string{"/health"},
			expectSpan:     false,
		},
		{
			name:           "Multiple excluded routes",
			path:           "/metrics/prometheus",
			method:         "GET",
			excludedRoutes: []string{"/swagger", "/health", "/metrics"},
			expectSpan:     false,
		},
		{
			name:           "No excluded routes",
			path:           "/api/users",
			method:         "GET",
			excludedRoutes: []string{},
			expectSpan:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			
			// Setup test tracer
			tp, spanRecorder := setupTestTracer()
			defer func() {
				_ = tp.Shutdown(ctx)
			}()
			
			// Replace the global tracer provider for this test
			oldTracerProvider := otel.GetTracerProvider()
			otel.SetTracerProvider(tp)
			defer otel.SetTracerProvider(oldTracerProvider)
			
			// Setup telemetry
			telemetry := &opentelemetry.Telemetry{
				TelemetryConfig: opentelemetry.TelemetryConfig{
					LibraryName:     "test-library",
					EnableTelemetry: true,
				},
				TracerProvider: tp,
			}

			// Create middleware
			middleware := NewTelemetryMiddleware(telemetry)

			// Create fiber app
			app := fiber.New()

			// Add middleware with excluded routes
			app.Use(middleware.WithTelemetry(telemetry, tt.excludedRoutes...))

			// Add test route
			app.All(tt.path, func(c *fiber.Ctx) error {
				return c.SendStatus(http.StatusOK)
			})

			// Create test request
			req, err := http.NewRequest(tt.method, tt.path, nil)
			require.NoError(t, err)

			// Execute request
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Check status code
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			
			// Check spans
			spans := spanRecorder.Ended()
			
			if tt.expectSpan {
				// Should have created a span
				require.GreaterOrEqual(t, len(spans), 1, "Expected at least one span to be created")
				
				// Check span name
				expectedSpanName := tt.method + " " + commons.ReplaceUUIDWithPlaceholder(tt.path)
				spanFound := false
				for _, span := range spans {
					if span.Name() == expectedSpanName {
						spanFound = true
						break
					}
				}
				assert.True(t, spanFound, "Expected span with name %s not found", expectedSpanName)
			} else {
				// Should not have created a span for excluded routes
				expectedSpanName := tt.method + " " + commons.ReplaceUUIDWithPlaceholder(tt.path)
				for _, span := range spans {
					assert.NotEqual(t, expectedSpanName, span.Name(), "Should not have created a span for excluded route")
				}
			}
		})
	}
}

// TestEndTracingSpans tests the EndTracingSpans middleware function
func TestEndTracingSpans(t *testing.T) {
	tests := []struct {
		name       string
		setupCtx   bool
		handlerErr error
	}{
		{
			name:       "With context",
			setupCtx:   true,
			handlerErr: nil,
		},
		{
			name:       "Without context",
			setupCtx:   false,
			handlerErr: nil,
		},
		{
			name:       "With context and handler error",
			setupCtx:   true,
			handlerErr: errors.New("handler error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test tracer with span recorder
			spanRecorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithSpanProcessor(spanRecorder),
			)
			defer func() {
				_ = tp.Shutdown(context.Background())
			}()

			// Create telemetry
			telemetry := &opentelemetry.Telemetry{
				TelemetryConfig: opentelemetry.TelemetryConfig{
					LibraryName:     "test-library",
					EnableTelemetry: true,
				},
				TracerProvider: tp,
			}

			// Create middleware
			middleware := NewTelemetryMiddleware(telemetry)

			// Create a fiber app
			app := fiber.New()

			// Create a middleware that sets up the context before the handler runs
			setupMiddleware := func(c *fiber.Ctx) error {
				ctx := c.UserContext()
				if ctx == nil {
					ctx = context.Background()
				}

				// Create a span if the test requires it
				if tt.setupCtx {
					tracer := tp.Tracer("test")
					ctx, _ = tracer.Start(ctx, "test-span")
					c.SetUserContext(ctx)
				}

				return c.Next()
			}

			// Create a simple handler that returns the test error
			handler := func(c *fiber.Ctx) error {
				return tt.handlerErr
			}

			// Register the route with setup middleware, then EndTracingSpans middleware, then handler
			app.Get("/test", setupMiddleware, middleware.EndTracingSpans, handler)

			// Create and execute the request
			req := httptest.NewRequest("GET", "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Verify error propagation via status code
			if tt.handlerErr != nil {
				assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
			} else {
				assert.Equal(t, fiber.StatusOK, resp.StatusCode)
			}

			// To test the async behavior of EndTracingSpans, we poll for the result.
			if tt.setupCtx {
				// Check if the span started in the handler was ended by the middleware
				assert.Eventually(t, func() bool {
					return len(spanRecorder.Ended()) == 1
				}, time.Second, 10*time.Millisecond, "Expected middleware to end one span")

				spans := spanRecorder.Ended()
				if assert.Len(t, spans, 1) {
					assert.Equal(t, "test-span", spans[0].Name())
				}
			} else {
				// Assert that no spans are ended by polling for a short duration
				assert.Never(t, func() bool {
					return len(spanRecorder.Ended()) > 0
				}, 100*time.Millisecond, 10*time.Millisecond, "Expected no spans to be ended")
			}
		})
	}
}

// TestExtractHTTPContext tests the ExtractHTTPContext function
func TestExtractHTTPContext(t *testing.T) {
	ctx := context.Background()
	
	// Setup test tracer
	tp, _ := setupTestTracer()
	defer func() {
		_ = tp.Shutdown(ctx)
	}()
	
	// Replace the global tracer provider for this test
	oldTracerProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(oldTracerProvider)
	
	// Create a valid traceparent header
	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	
	// Create fiber app
	app := fiber.New()
	
	// Add test route
	app.Get("/test", func(c *fiber.Ctx) error {
		// Extract context
		ctx := opentelemetry.ExtractHTTPContext(c)
		
		// Check if span info was extracted
		spanCtx := trace.SpanContextFromContext(ctx)
		
		// If traceparent header is present, span context should be valid
		if c.Get("traceparent") != "" {
			assert.True(t, spanCtx.IsValid(), "Span context should be valid with traceparent header")
			assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spanCtx.TraceID().String())
			assert.Equal(t, "00f067aa0ba902b7", spanCtx.SpanID().String())
		} else {
			assert.False(t, spanCtx.IsValid(), "Span context should not be valid without traceparent header")
		}
		
		return c.SendStatus(http.StatusOK)
	})
	
	// Test with traceparent header
	req1, err := http.NewRequest("GET", "/test", nil)
	require.NoError(t, err)
	req1.Header.Set("traceparent", traceparent)
	
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusOK, resp1.StatusCode)
	
	// Test without traceparent header
	req2, err := http.NewRequest("GET", "/test", nil)
	require.NoError(t, err)
	
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}
