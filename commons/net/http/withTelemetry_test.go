// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

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
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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

// TestGetMetricsCollectionInterval tests the getMetricsCollectionInterval function
func TestGetMetricsCollectionInterval(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{
			name:     "default when not set",
			envValue: "",
			expected: DefaultMetricsCollectionInterval,
		},
		{
			name:     "valid duration in seconds",
			envValue: "10s",
			expected: 10 * time.Second,
		},
		{
			name:     "valid duration in milliseconds",
			envValue: "500ms",
			expected: 500 * time.Millisecond,
		},
		{
			name:     "valid duration in minutes",
			envValue: "1m",
			expected: 1 * time.Minute,
		},
		{
			name:     "invalid format falls back to default",
			envValue: "invalid",
			expected: DefaultMetricsCollectionInterval,
		},
		{
			name:     "zero value falls back to default",
			envValue: "0s",
			expected: DefaultMetricsCollectionInterval,
		},
		{
			name:     "negative value falls back to default",
			envValue: "-5s",
			expected: DefaultMetricsCollectionInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv("METRICS_COLLECTION_INTERVAL", tt.envValue)
			} else {
				t.Setenv("METRICS_COLLECTION_INTERVAL", "")
			}

			result := getMetricsCollectionInterval()
			assert.Equal(t, tt.expected, result)
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

// TestWithTelemetryConditionalTracePropagation tests the conditional trace propagation based on UserAgent
func TestWithTelemetryConditionalTracePropagation(t *testing.T) {
	tests := []struct {
		name               string
		userAgent          string
		traceparent        string
		shouldPropagateTrace bool
		description        string
	}{
		{
			name:                 "Internal Lerian service - should propagate trace",
			userAgent:            "midaz/1.0.0 LerianStudio",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: true,
			description:          "Internal service with valid UserAgent pattern should propagate trace context",
		},
		{
			name:                 "Internal Lerian service with different version",
			userAgent:            "transaction-api/2.3.5 LerianStudio",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: true,
			description:          "Different service name and version but valid LerianStudio pattern",
		},
		{
			name:                 "External service - should NOT propagate trace",
			userAgent:            "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "External browser UserAgent should create new root span",
		},
		{
			name:                 "External API client - should NOT propagate trace",
			userAgent:            "PostmanRuntime/7.26.8",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "External API testing tool should create new root span",
		},
		{
			name:                 "No UserAgent - should NOT propagate trace",
			userAgent:            "",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "Missing UserAgent should create new root span",
		},
		{
			name:                 "Invalid Lerian pattern - should NOT propagate trace",
			userAgent:            "midaz/1.0.0 NotLerianStudio",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "Similar but invalid pattern should not propagate trace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Setup test tracer with span recorder
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
			app.Use(middleware.WithTelemetry(telemetry))

			// Add test route that captures span context
			var capturedSpanContext trace.SpanContext
			app.Get("/test", func(c *fiber.Ctx) error {
				capturedSpanContext = trace.SpanContextFromContext(c.UserContext())
				return c.SendStatus(http.StatusOK)
			})

			// Create test request
			req, err := http.NewRequest("GET", "/test", nil)
			require.NoError(t, err)

			// Set UserAgent header if provided
			if tt.userAgent != "" {
				req.Header.Set("User-Agent", tt.userAgent)
			}

			// Set traceparent header
			req.Header.Set("traceparent", tt.traceparent)

			// Execute request
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Check status code
			assert.Equal(t, http.StatusOK, resp.StatusCode)

			// Verify spans
			spans := spanRecorder.Ended()
			require.GreaterOrEqual(t, len(spans), 1, "Expected at least one span to be created")

			// Check if trace was propagated correctly
			if tt.shouldPropagateTrace {
				// Should have propagated the trace ID from traceparent header
				assert.True(t, capturedSpanContext.IsValid(), "Span context should be valid for internal services")
				assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", capturedSpanContext.TraceID().String(),
					"Trace ID should match the traceparent header for internal services")
			} else {
				// Should have created a new root span with different trace ID
				// The span context will be valid, but the trace ID should be different
				if capturedSpanContext.IsValid() {
					assert.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", capturedSpanContext.TraceID().String(),
						"Trace ID should be different from traceparent header for external services (new root)")
				}
			}
		})
	}
}

// TestGetGRPCUserAgent tests the getGRPCUserAgent helper function
func TestGetGRPCUserAgent(t *testing.T) {
	tests := []struct {
		name           string
		setupMetadata  func() context.Context
		expectedUA     string
		description    string
	}{
		{
			name: "Valid user-agent in metadata",
			setupMetadata: func() context.Context {
				md := metadata.Pairs("user-agent", "midaz/1.0.0 LerianStudio")
				return metadata.NewIncomingContext(context.Background(), md)
			},
			expectedUA:  "midaz/1.0.0 LerianStudio",
			description: "Should extract user-agent from gRPC metadata",
		},
		{
			name: "Multiple user-agent values",
			setupMetadata: func() context.Context {
				md := metadata.New(map[string]string{
					"user-agent": "midaz/1.0.0 LerianStudio",
				})
				md.Append("user-agent", "secondary-value")
				return metadata.NewIncomingContext(context.Background(), md)
			},
			expectedUA:  "midaz/1.0.0 LerianStudio",
			description: "Should return first user-agent value when multiple are present",
		},
		{
			name: "No metadata in context",
			setupMetadata: func() context.Context {
				return context.Background()
			},
			expectedUA:  "",
			description: "Should return empty string when no metadata present",
		},
		{
			name: "Metadata without user-agent",
			setupMetadata: func() context.Context {
				md := metadata.Pairs("authorization", "Bearer token")
				return metadata.NewIncomingContext(context.Background(), md)
			},
			expectedUA:  "",
			description: "Should return empty string when user-agent key not present",
		},
		{
			name: "Empty user-agent value",
			setupMetadata: func() context.Context {
				md := metadata.Pairs("user-agent", "")
				return metadata.NewIncomingContext(context.Background(), md)
			},
			expectedUA:  "",
			description: "Should return empty string for empty user-agent value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupMetadata()
			result := getGRPCUserAgent(ctx)
			assert.Equal(t, tt.expectedUA, result, tt.description)
		})
	}
}

// TestWithTelemetryInterceptorConditionalTracePropagation tests conditional trace propagation in gRPC interceptor
func TestWithTelemetryInterceptorConditionalTracePropagation(t *testing.T) {
	tests := []struct {
		name                 string
		userAgent            string
		traceparent          string
		tracestate           string
		shouldPropagateTrace bool
		description          string
	}{
		{
			name:                 "Internal Lerian service via gRPC - should propagate trace",
			userAgent:            "midaz/1.0.0 LerianStudio",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			tracestate:           "congo=t61rcWkgMzE",
			shouldPropagateTrace: true,
			description:          "Internal gRPC service should propagate trace context",
		},
		{
			name:                 "Internal service with hyphenated name",
			userAgent:            "transaction-router/3.2.1 LerianStudio",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: true,
			description:          "Service with hyphenated name should propagate trace",
		},
		{
			name:                 "External gRPC client - should NOT propagate trace",
			userAgent:            "grpc-go/1.50.0",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "External gRPC client should create new root span",
		},
		{
			name:                 "No UserAgent in gRPC metadata - should NOT propagate trace",
			userAgent:            "",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "Missing UserAgent in metadata should create new root span",
		},
		{
			name:                 "Invalid Lerian pattern in gRPC - should NOT propagate trace",
			userAgent:            "myservice/1.0.0 NotLerian",
			traceparent:          "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			shouldPropagateTrace: false,
			description:          "Invalid pattern should not propagate trace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Setup test tracer with span recorder
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

			// Create gRPC interceptor
			interceptor := middleware.WithTelemetryInterceptor(telemetry)

			// Prepare incoming context with metadata
			md := metadata.New(map[string]string{})
			if tt.userAgent != "" {
				md.Set("user-agent", tt.userAgent)
			}
			if tt.traceparent != "" {
				md.Set("traceparent", tt.traceparent)
			}
			if tt.tracestate != "" {
				md.Set("tracestate", tt.tracestate)
			}
			ctx = metadata.NewIncomingContext(ctx, md)

			// Mock handler that captures the span context
			var capturedSpanContext trace.SpanContext
			handler := func(ctx context.Context, req any) (any, error) {
				capturedSpanContext = trace.SpanContextFromContext(ctx)
				return "response", nil
			}

			// Mock UnaryServerInfo
			info := &grpc.UnaryServerInfo{
				FullMethod: "/test.Service/Method",
			}

			// Execute interceptor
			_, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)

			// Verify spans
			spans := spanRecorder.Ended()
			require.GreaterOrEqual(t, len(spans), 1, "Expected at least one span to be created")

			// Check if trace was propagated correctly
			if tt.shouldPropagateTrace {
				// Should have propagated the trace ID from metadata
				assert.True(t, capturedSpanContext.IsValid(), "Span context should be valid for internal services")
				assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", capturedSpanContext.TraceID().String(),
					"Trace ID should match the traceparent for internal gRPC services")
			} else {
				// Should have created a new root span with different trace ID
				if capturedSpanContext.IsValid() {
					assert.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", capturedSpanContext.TraceID().String(),
						"Trace ID should be different from traceparent for external services (new root)")
				}
			}
		})
	}
}
