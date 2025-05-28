package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestNewObservabilityMiddleware(t *testing.T) {
	ctx := context.Background()

	t.Run("successful creation", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-middleware"),
			WithComponentEnabled(true, true, true),
		)
		require.NoError(t, err)
		defer provider.Shutdown(ctx)

		buf := &bytes.Buffer{}
		baseLogger := &log.GoLogger{Level: log.InfoLevel}
		logger := log.NewStructuredLogger(baseLogger)

		om, err := NewObservabilityMiddleware(
			"test-service",
			provider.(*ObservabilityProvider).tracerProvider,
			provider.(*ObservabilityProvider).meterProvider,
			logger,
		)

		require.NoError(t, err)
		require.NotNil(t, om)
		assert.Equal(t, "test-service", om.serviceName)
		assert.NotNil(t, om.tracer)
		assert.NotNil(t, om.meter)
		assert.NotNil(t, om.logger)
		assert.NotNil(t, om.requestCounter)
		assert.NotNil(t, om.requestDuration)
		assert.NotNil(t, om.requestSize)
		assert.NotNil(t, om.responseSize)
		assert.NotNil(t, om.activeRequests)
	})

	t.Run("with nil providers", func(t *testing.T) {
		buf := &bytes.Buffer{}
		logger := log.NewStructuredLogger(log.InfoLevel, buf)

		_, err := NewObservabilityMiddleware(
			"test-service",
			nil,
			nil,
			logger,
		)

		assert.Error(t, err)
	})

	t.Run("with nil logger", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-middleware"),
			WithComponentEnabled(true, true, true),
		)
		require.NoError(t, err)
		defer provider.Shutdown(ctx)

		om, err := NewObservabilityMiddleware(
			"test-service",
			provider.TracerProvider(),
			provider.MeterProvider(),
			nil,
		)

		require.NoError(t, err)
		require.NotNil(t, om)
	})
}

func TestObservabilityMiddlewareHandler(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	t.Run("successful GET request", func(t *testing.T) {
		app.Get("/api/users", func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"users": []string{"john", "jane"}})
		})

		req := httptest.NewRequest("GET", "/api/users", nil)
		req.Header.Set("User-Agent", "test-client")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "users")

		logOutput := buf.String()
		assert.Contains(t, logOutput, "Request completed successfully")
		assert.Contains(t, logOutput, "GET")
		assert.Contains(t, logOutput, "/api/users")
		assert.Contains(t, logOutput, "200")
	})

	t.Run("successful POST request", func(t *testing.T) {
		buf.Reset()

		app.Post("/api/users", func(c *fiber.Ctx) error {
			return c.Status(201).JSON(fiber.Map{"id": 1, "name": "john"})
		})

		reqBody := `{"name": "john", "email": "john@example.com"}`
		req := httptest.NewRequest("POST", "/api/users", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "test-client")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		logOutput := buf.String()
		assert.Contains(t, logOutput, "Request completed successfully")
		assert.Contains(t, logOutput, "POST")
		assert.Contains(t, logOutput, "201")
	})

	t.Run("error response", func(t *testing.T) {
		buf.Reset()

		app.Get("/api/error", func(c *fiber.Ctx) error {
			return c.Status(500).JSON(fiber.Map{"error": "internal server error"})
		})

		req := httptest.NewRequest("GET", "/api/error", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

		logOutput := buf.String()
		assert.Contains(t, logOutput, "Request completed with error status")
		assert.Contains(t, logOutput, "500")
	})

	t.Run("client error response", func(t *testing.T) {
		buf.Reset()

		app.Get("/api/notfound", func(c *fiber.Ctx) error {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		})

		req := httptest.NewRequest("GET", "/api/notfound", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)

		logOutput := buf.String()
		assert.Contains(t, logOutput, "Request completed with error status")
		assert.Contains(t, logOutput, "404")
	})

	t.Run("request with error", func(t *testing.T) {
		buf.Reset()

		app.Get("/api/panic", func(c *fiber.Ctx) error {
			return fmt.Errorf("something went wrong")
		})

		req := httptest.NewRequest("GET", "/api/panic", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

		logOutput := buf.String()
		assert.Contains(t, logOutput, "Request failed")
		assert.Contains(t, logOutput, "something went wrong")
	})
}

func TestObservabilityMiddlewareMetrics(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/api/test", func(c *fiber.Ctx) error {
		time.Sleep(10 * time.Millisecond)
		return c.JSON(fiber.Map{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Content-Length", "100")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestObservabilityMiddlewareTracing(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	var spanContext trace.SpanContext
	app.Get("/api/trace", func(c *fiber.Ctx) error {
		span := trace.SpanFromContext(c.UserContext())
		spanContext = span.SpanContext()
		return c.JSON(fiber.Map{"trace_id": spanContext.TraceID().String()})
	})

	req := httptest.NewRequest("GET", "/api/trace", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, spanContext.IsValid())
}

func TestObservabilityMiddlewareHeaders(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/api/headers", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"user_agent": c.Get("User-Agent"),
			"host":       c.Get("Host"),
		})
	})

	req := httptest.NewRequest("GET", "/api/headers", nil)
	req.Header.Set("User-Agent", "custom-client/1.0")
	req.Header.Set("Host", "api.example.com")
	req.Header.Set("X-Forwarded-For", "192.168.1.1")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "custom-client/1.0")
}

func TestObservabilityMiddlewareRequestSize(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Post("/api/upload", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"received": len(c.Body())})
	})

	reqBody := strings.Repeat("a", 1000)
	req := httptest.NewRequest("POST", "/api/upload", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "text/plain")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "request_size")
	assert.Contains(t, logOutput, "1000")
}

func TestObservabilityMiddlewareResponseSize(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/api/large", func(c *fiber.Ctx) error {
		largeData := strings.Repeat("x", 2000)
		return c.SendString(largeData)
	})

	req := httptest.NewRequest("GET", "/api/large", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Len(t, body, 2000)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "response_size")
	assert.Contains(t, logOutput, "2000")
}

func TestObservabilityMiddlewareDuration(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/api/slow", func(c *fiber.Ctx) error {
		time.Sleep(50 * time.Millisecond)
		return c.JSON(fiber.Map{"status": "completed"})
	})

	req := httptest.NewRequest("GET", "/api/slow", nil)

	start := time.Now()
	resp, err := app.Test(req)
	duration := time.Since(start)

	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.GreaterOrEqual(t, duration, 50*time.Millisecond)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "duration_ms")
}

func TestObservabilityMiddlewareStatusClasses(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	tests := []struct {
		name       string
		path       string
		statusCode int
		handler    func(c *fiber.Ctx) error
	}{
		{
			name:       "2xx success",
			path:       "/api/success",
			statusCode: 200,
			handler: func(c *fiber.Ctx) error {
				return c.JSON(fiber.Map{"status": "ok"})
			},
		},
		{
			name:       "3xx redirect",
			path:       "/api/redirect",
			statusCode: 302,
			handler: func(c *fiber.Ctx) error {
				return c.Redirect("/api/success")
			},
		},
		{
			name:       "4xx client error",
			path:       "/api/badrequest",
			statusCode: 400,
			handler: func(c *fiber.Ctx) error {
				return c.Status(400).JSON(fiber.Map{"error": "bad request"})
			},
		},
		{
			name:       "5xx server error",
			path:       "/api/servererror",
			statusCode: 500,
			handler: func(c *fiber.Ctx) error {
				return c.Status(500).JSON(fiber.Map{"error": "server error"})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()

			app.Get(tt.path, tt.handler)

			req := httptest.NewRequest("GET", tt.path, nil)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.statusCode, resp.StatusCode)

			logOutput := buf.String()
			assert.Contains(t, logOutput, fmt.Sprintf("%d", tt.statusCode))
		})
	}
}

func TestObservabilityMiddlewareIPExtraction(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"test-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/api/ip", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ip": c.IP()})
	})

	req := httptest.NewRequest("GET", "/api/ip", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.100")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "ip")
}

func TestObservabilityMiddlewareIntegration(t *testing.T) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("test-integration"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(t, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"integration-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(t, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	app.Get("/api/users/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		if id == "999" {
			return c.Status(404).JSON(fiber.Map{"error": "user not found"})
		}
		return c.JSON(fiber.Map{"id": id, "name": "John Doe"})
	})

	app.Post("/api/users", func(c *fiber.Ctx) error {
		var user map[string]interface{}
		if err := c.BodyParser(&user); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "invalid JSON"})
		}
		user["id"] = "123"
		return c.Status(201).JSON(user)
	})

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedLog    string
	}{
		{
			name:           "health check",
			method:         "GET",
			path:           "/health",
			expectedStatus: 200,
			expectedLog:    "Request completed successfully",
		},
		{
			name:           "get user success",
			method:         "GET",
			path:           "/api/users/123",
			expectedStatus: 200,
			expectedLog:    "Request completed successfully",
		},
		{
			name:           "get user not found",
			method:         "GET",
			path:           "/api/users/999",
			expectedStatus: 404,
			expectedLog:    "Request completed with error status",
		},
		{
			name:           "create user success",
			method:         "POST",
			path:           "/api/users",
			body:           `{"name": "Jane Doe", "email": "jane@example.com"}`,
			expectedStatus: 201,
			expectedLog:    "Request completed successfully",
		},
		{
			name:           "create user invalid JSON",
			method:         "POST",
			path:           "/api/users",
			body:           `{"name": "Jane Doe", "email":}`,
			expectedStatus: 400,
			expectedLog:    "Request completed with error status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()

			var req *http.Request
			if tt.body != "" {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			req.Header.Set("User-Agent", "integration-test")

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			logOutput := buf.String()
			assert.Contains(t, logOutput, tt.expectedLog)
			assert.Contains(t, logOutput, tt.method)
			assert.Contains(t, logOutput, fmt.Sprintf("%d", tt.expectedStatus))
		})
	}
}

func BenchmarkObservabilityMiddleware(b *testing.B) {
	ctx := context.Background()
	provider, err := New(ctx,
		WithServiceName("bench-middleware"),
		WithComponentEnabled(true, true, true),
	)
	require.NoError(b, err)
	defer provider.Shutdown(ctx)

	buf := &bytes.Buffer{}
	logger := log.NewStructuredLogger(log.InfoLevel, buf)

	om, err := NewObservabilityMiddleware(
		"bench-service",
		provider.TracerProvider(),
		provider.MeterProvider(),
		logger,
	)
	require.NoError(b, err)

	app := fiber.New()
	app.Use(om.Middleware())

	app.Get("/api/bench", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/api/bench", nil)
			resp, err := app.Test(req)
			if err != nil {
				b.Fatal(err)
			}
			resp.Body.Close()
		}
	})
}
