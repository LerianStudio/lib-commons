package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestNewFiberMiddleware(t *testing.T) {
	ctx := context.Background()

	t.Run("with nil provider", func(t *testing.T) {
		middleware, err := NewFiberMiddleware(nil)
		require.NoError(t, err)
		assert.NotNil(t, middleware)

		// Should be a no-op middleware
		app := fiber.New()
		app.Use(middleware)
		app.Get("/test", func(c *fiber.Ctx) error {
			return c.SendString("ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("with enabled provider", func(t *testing.T) {
		provider, err := New(ctx, WithServiceName("test-service"))
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		middleware, err := NewFiberMiddleware(provider)
		require.NoError(t, err)
		assert.NotNil(t, middleware)
	})

	t.Run("with disabled provider", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-service"),
			WithComponentEnabled(false, false, false),
		)
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		middleware, err := NewFiberMiddleware(provider)
		require.NoError(t, err)
		assert.NotNil(t, middleware)
	})
}

func TestFiberMiddlewareOptions(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	t.Run("WithIgnorePathsFiber", func(t *testing.T) {
		middleware, err := NewFiberMiddleware(provider,
			WithIgnorePathsFiber("/health", "/metrics"),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)

		// Test with empty paths (should error)
		_, err = NewFiberMiddleware(provider, WithIgnorePathsFiber())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one path must be provided")
	})

	t.Run("WithIgnoreHeadersFiber", func(t *testing.T) {
		middleware, err := NewFiberMiddleware(provider,
			WithIgnoreHeadersFiber("Authorization", "X-API-Key"),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)

		// Test with empty headers (should error)
		_, err = NewFiberMiddleware(provider, WithIgnoreHeadersFiber())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one header must be provided")
	})

	t.Run("WithMaskedParamsFiber", func(t *testing.T) {
		middleware, err := NewFiberMiddleware(provider,
			WithMaskedParamsFiber("password", "secret"),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)

		// Test with empty params (should error)
		_, err = NewFiberMiddleware(provider, WithMaskedParamsFiber())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one parameter must be provided")
	})

	t.Run("WithUserIDExtractor", func(t *testing.T) {
		extractor := func(c *fiber.Ctx) string {
			return c.Get("X-User-ID")
		}

		middleware, err := NewFiberMiddleware(provider,
			WithUserIDExtractor(extractor),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)

		// Test with nil extractor (should error)
		_, err = NewFiberMiddleware(provider, WithUserIDExtractor(nil))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "user ID extractor cannot be nil")
	})

	t.Run("WithRequestIDExtractor", func(t *testing.T) {
		extractor := func(c *fiber.Ctx) string {
			return c.Get("X-Request-ID")
		}

		middleware, err := NewFiberMiddleware(provider,
			WithRequestIDExtractor(extractor),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)

		// Test with nil extractor (should error)
		_, err = NewFiberMiddleware(provider, WithRequestIDExtractor(nil))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "request ID extractor cannot be nil")
	})

	t.Run("WithSecurityDefaultsFiber", func(t *testing.T) {
		middleware, err := NewFiberMiddleware(provider,
			WithSecurityDefaultsFiber(),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)
	})

	t.Run("multiple options", func(t *testing.T) {
		middleware, err := NewFiberMiddleware(provider,
			WithIgnorePathsFiber("/health"),
			WithIgnoreHeadersFiber("Authorization"),
			WithMaskedParamsFiber("password"),
			WithUserIDExtractor(func(c *fiber.Ctx) string { return "user123" }),
			WithRequestIDExtractor(func(c *fiber.Ctx) string { return "req123" }),
			WithSecurityDefaultsFiber(),
		)
		require.NoError(t, err)
		assert.NotNil(t, middleware)
	})
}

func TestFiberMiddlewareRequestHandling(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("successful request", func(t *testing.T) {
		app.Get("/test", func(c *fiber.Ctx) error {
			// Verify context has provider
			provider := GetProvider(c.UserContext())
			assert.NotNil(t, provider)

			// Verify span is in context
			span := trace.SpanFromContext(c.UserContext())
			assert.NotNil(t, span)

			return c.JSON(fiber.Map{"message": "success"})
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("User-Agent", "test-agent")
		req.Header.Set("X-Request-ID", "test-request-id")

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("request with error", func(t *testing.T) {
		app.Get("/error", func(c *fiber.Ctx) error {
			return fiber.NewError(500, "internal server error")
		})

		req := httptest.NewRequest(http.MethodGet, "/error", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 500, resp.StatusCode)
	})

	t.Run("request with custom headers", func(t *testing.T) {
		app.Post("/custom", func(c *fiber.Ctx) error {
			return c.SendString("custom response")
		})

		body := strings.NewReader(`{"test": "data"}`)
		req := httptest.NewRequest(http.MethodPost, "/custom", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer token123")
		req.Header.Set("X-Custom-Header", "custom-value")

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestFiberMiddlewareIgnorePaths(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider,
		WithIgnorePathsFiber("/health", "/metrics"),
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("ignored paths", func(t *testing.T) {
		app.Get("/health", func(c *fiber.Ctx) error {
			// Should not have observability context
			provider := GetProvider(c.UserContext())
			assert.Nil(t, provider)
			return c.SendString("healthy")
		})

		app.Get("/metrics", func(c *fiber.Ctx) error {
			provider := GetProvider(c.UserContext())
			assert.Nil(t, provider)
			return c.SendString("metrics")
		})

		// Test health endpoint
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Test metrics endpoint
		req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("non-ignored paths", func(t *testing.T) {
		app.Get("/api/test", func(c *fiber.Ctx) error {
			// Should have observability context
			provider := GetProvider(c.UserContext())
			assert.NotNil(t, provider)
			return c.SendString("api response")
		})

		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestFiberMiddlewareExtractors(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	userIDExtractor := func(c *fiber.Ctx) string {
		return c.Get("X-User-ID")
	}

	requestIDExtractor := func(c *fiber.Ctx) string {
		return c.Get("X-Request-ID")
	}

	middleware, err := NewFiberMiddleware(provider,
		WithUserIDExtractor(userIDExtractor),
		WithRequestIDExtractor(requestIDExtractor),
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("with extractors", func(t *testing.T) {
		app.Get("/extract", func(c *fiber.Ctx) error {
			return c.SendString("extracted")
		})

		req := httptest.NewRequest(http.MethodGet, "/extract", nil)
		req.Header.Set("X-User-ID", "user123")
		req.Header.Set("X-Request-ID", "req456")

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("without extractors", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/extract", nil)
		// No headers set

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestFiberMiddlewareSecurityDefaults(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider,
		WithSecurityDefaultsFiber(),
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("with sensitive headers", func(t *testing.T) {
		app.Post("/secure", func(c *fiber.Ctx) error {
			return c.SendString("secure response")
		})

		body := strings.NewReader(`{"password": "secret123"}`)
		req := httptest.NewRequest(http.MethodPost, "/secure", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sensitive-token")
		req.Header.Set("Cookie", "session=secret")
		req.Header.Set("X-API-Key", "api-key-123")

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestFiberMiddlewareErrorHandling(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("404 error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 404, resp.StatusCode)
	})

	t.Run("500 error", func(t *testing.T) {
		app.Get("/server-error", func(c *fiber.Ctx) error {
			return fiber.NewError(500, "internal server error")
		})

		req := httptest.NewRequest(http.MethodGet, "/server-error", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 500, resp.StatusCode)
	})

	t.Run("400 error", func(t *testing.T) {
		app.Post("/bad-request", func(c *fiber.Ctx) error {
			return fiber.NewError(400, "bad request")
		})

		req := httptest.NewRequest(http.MethodPost, "/bad-request", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode)
	})

	t.Run("panic recovery", func(t *testing.T) {
		app.Get("/panic", func(c *fiber.Ctx) error {
			panic("test panic")
		})

		req := httptest.NewRequest(http.MethodGet, "/panic", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		// Fiber should handle the panic and return 500
		assert.Equal(t, 500, resp.StatusCode)
	})
}

func TestFiberMiddlewareMetrics(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("request with body", func(t *testing.T) {
		app.Post("/with-body", func(c *fiber.Ctx) error {
			body := c.Body()
			return c.JSON(fiber.Map{"received": len(body)})
		})

		requestBody := `{"test": "data", "number": 123}`
		req := httptest.NewRequest(http.MethodPost, "/with-body", strings.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Read response body
		respBody, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(respBody), "received")
	})

	t.Run("request without body", func(t *testing.T) {
		app.Get("/no-body", func(c *fiber.Ctx) error {
			return c.SendString("no body response")
		})

		req := httptest.NewRequest(http.MethodGet, "/no-body", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestFiberMiddlewareTracing(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("test-service"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	t.Run("trace context propagation", func(t *testing.T) {
		var capturedSpan trace.Span

		app.Get("/trace", func(c *fiber.Ctx) error {
			span := trace.SpanFromContext(c.UserContext())
			capturedSpan = span
			assert.NotNil(t, span)
			assert.True(t, span.SpanContext().IsValid())

			// Add custom span attributes
			span.SetAttributes(attribute.String("custom.attribute", "test-value"))

			return c.SendString("traced")
		})

		req := httptest.NewRequest(http.MethodGet, "/trace", nil)
		req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.NotNil(t, capturedSpan)
	})

	t.Run("nested spans", func(t *testing.T) {
		app.Get("/nested", func(c *fiber.Ctx) error {
			parentSpan := trace.SpanFromContext(c.UserContext())
			assert.NotNil(t, parentSpan)

			// Create a child span
			tracer := provider.Tracer()
			childCtx, childSpan := tracer.Start(c.UserContext(), "child-operation")
			defer childSpan.End()

			// Verify parent-child relationship
			assert.NotEqual(t, parentSpan.SpanContext().SpanID(), childSpan.SpanContext().SpanID())
			assert.Equal(t, parentSpan.SpanContext().TraceID(), childSpan.SpanContext().TraceID())

			// Use child context
			_ = childCtx

			return c.SendString("nested")
		})

		req := httptest.NewRequest(http.MethodGet, "/nested", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestFiberMiddlewareIntegration(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("integration-test"))
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider,
		WithIgnorePathsFiber("/health"),
		WithIgnoreHeadersFiber("authorization"),
		WithMaskedParamsFiber("password"),
		WithUserIDExtractor(func(c *fiber.Ctx) string {
			return c.Get("X-User-ID")
		}),
		WithRequestIDExtractor(func(c *fiber.Ctx) string {
			return c.Get("X-Request-ID")
		}),
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(middleware)

	// Add routes
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("healthy")
	})

	app.Get("/api/users/:id", func(c *fiber.Ctx) error {
		userID := c.Params("id")
		return c.JSON(fiber.Map{"user_id": userID})
	})

	app.Post("/api/login", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"token": "jwt-token"})
	})

	app.Get("/api/error", func(c *fiber.Ctx) error {
		return fiber.NewError(500, "simulated error")
	})

	t.Run("complete integration test", func(t *testing.T) {
		// Test health endpoint (should be ignored)
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Test API endpoint with tracing
		req = httptest.NewRequest(http.MethodGet, "/api/users/123", nil)
		req.Header.Set("X-User-ID", "user456")
		req.Header.Set("X-Request-ID", "req789")
		req.Header.Set("Authorization", "Bearer secret-token")
		resp, err = app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Test POST with body
		body := strings.NewReader(`{"username": "test", "password": "secret123"}`)
		req = httptest.NewRequest(http.MethodPost, "/api/login", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "login-req")
		resp, err = app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Test error endpoint
		req = httptest.NewRequest(http.MethodGet, "/api/error", nil)
		req.Header.Set("X-Request-ID", "error-req")
		resp, err = app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 500, resp.StatusCode)
	})
}

func BenchmarkFiberMiddleware(b *testing.B) {
	ctx := context.Background()
	provider, err := New(ctx, WithServiceName("benchmark-test"))
	require.NoError(b, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	middleware, err := NewFiberMiddleware(provider)
	require.NoError(b, err)

	app := fiber.New()
	app.Use(middleware)
	app.Get("/benchmark", func(c *fiber.Ctx) error {
		return c.SendString("benchmark")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodGet, "/benchmark", nil)
			resp, err := app.Test(req)
			if err != nil {
				b.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				b.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
			}
		}
	})
}
