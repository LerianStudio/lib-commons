package requestid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID(t *testing.T) {
	t.Run("generate request ID", func(t *testing.T) {
		id := Generate()
		assert.NotEmpty(t, id)
		assert.Len(t, id, 36) // UUID v4 length

		// Should be different each time
		id2 := Generate()
		assert.NotEqual(t, id, id2)
	})

	t.Run("custom generator", func(t *testing.T) {
		SetGenerator(func() string {
			return "custom-id-123"
		})
		defer SetGenerator(DefaultGenerator)

		id := Generate()
		assert.Equal(t, "custom-id-123", id)
	})

	t.Run("context with request ID", func(t *testing.T) {
		ctx := context.Background()

		// No ID initially
		id := FromContext(ctx)
		assert.Empty(t, id)

		// Add ID to context
		expectedID := "test-request-id"
		ctx = NewContext(ctx, expectedID)

		// Retrieve ID
		id = FromContext(ctx)
		assert.Equal(t, expectedID, id)
	})

	t.Run("ensure request ID in context", func(t *testing.T) {
		// Context without ID gets a new one
		ctx := context.Background()
		ctx, id := EnsureContext(ctx)
		assert.NotEmpty(t, id)
		assert.Equal(t, id, FromContext(ctx))

		// Context with ID keeps the existing one
		existingID := "existing-id"
		ctx = NewContext(context.Background(), existingID)
		ctx, id = EnsureContext(ctx)
		assert.Equal(t, existingID, id)
	})

	t.Run("HTTP middleware", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check request ID is in context
			id := FromContext(r.Context())
			assert.NotEmpty(t, id)

			// Check header is set
			assert.Equal(t, id, w.Header().Get(DefaultHeader))

			w.WriteHeader(http.StatusOK)
		})

		// Wrap with middleware
		wrapped := HTTPMiddleware(handler)

		// Test without incoming header
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotEmpty(t, rec.Header().Get(DefaultHeader))

		// Test with incoming header
		existingID := "incoming-request-id"
		req = httptest.NewRequest("GET", "/", nil)
		req.Header.Set(DefaultHeader, existingID)
		rec = httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, existingID, rec.Header().Get(DefaultHeader))
	})

	t.Run("HTTP middleware with custom header", func(t *testing.T) {
		customHeader := "X-Trace-ID"
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		wrapped := HTTPMiddlewareWithHeader(handler, customHeader)

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)

		assert.NotEmpty(t, rec.Header().Get(customHeader))
		assert.Empty(t, rec.Header().Get(DefaultHeader))
	})

	t.Run("Fiber middleware", func(t *testing.T) {
		app := fiber.New()

		// Add middleware
		app.Use(FiberMiddleware())

		// Add test handler
		app.Get("/", func(c *fiber.Ctx) error {
			id := FromFiberContext(c)
			assert.NotEmpty(t, id)
			return c.SendStatus(fiber.StatusOK)
		})

		// Test without header
		req := httptest.NewRequest("GET", "/", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.NotEmpty(t, resp.Header.Get(DefaultHeader))

		// Test with header
		existingID := "fiber-request-id"
		req = httptest.NewRequest("GET", "/", nil)
		req.Header.Set(DefaultHeader, existingID)
		resp, err = app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, existingID, resp.Header.Get(DefaultHeader))
	})

	t.Run("Fiber middleware with custom header", func(t *testing.T) {
		app := fiber.New()
		customHeader := "X-Correlation-ID"

		app.Use(FiberMiddlewareWithHeader(customHeader))
		app.Get("/", func(c *fiber.Ctx) error {
			id := FromFiberContext(c)
			assert.NotEmpty(t, id)
			return c.SendStatus(fiber.StatusOK)
		})

		req := httptest.NewRequest("GET", "/", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.NotEmpty(t, resp.Header.Get(customHeader))
	})

	t.Run("HTTP client with request ID", func(t *testing.T) {
		// Mock server that echoes the request ID
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(DefaultHeader)
			w.Header().Set(DefaultHeader, id)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		// Create client
		client := NewHTTPClient()

		// Make request with context containing ID
		ctx := NewContext(context.Background(), "client-request-id")
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, "client-request-id", resp.Header.Get(DefaultHeader))
	})

	t.Run("extract from headers", func(t *testing.T) {
		headers := http.Header{}

		// No header
		id := ExtractFromHeaders(headers)
		assert.Empty(t, id)

		// With default header
		headers.Set(DefaultHeader, "header-id")
		id = ExtractFromHeaders(headers)
		assert.Equal(t, "header-id", id)

		// With custom headers
		headers.Set("X-Trace-ID", "trace-id")
		id = ExtractFromHeaders(headers, "X-Trace-ID", "X-Other-ID")
		assert.Equal(t, "trace-id", id)

		// First match wins
		headers.Set("X-Other-ID", "other-id")
		id = ExtractFromHeaders(headers, "X-Other-ID", "X-Trace-ID")
		assert.Equal(t, "other-id", id)
	})

	t.Run("concurrent access", func(t *testing.T) {
		// Test that context values are safe for concurrent access
		ctx := NewContext(context.Background(), "concurrent-id")

		done := make(chan bool, 10)
		for i := 0; i < 10; i++ {
			go func() {
				id := FromContext(ctx)
				assert.Equal(t, "concurrent-id", id)
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

func BenchmarkRequestID(b *testing.B) {
	b.Run("Generate", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = Generate()
		}
	})

	b.Run("Context", func(b *testing.B) {
		ctx := context.Background()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			ctx = NewContext(ctx, "bench-id")
			_ = FromContext(ctx)
		}
	})

	b.Run("HTTPMiddleware", func(b *testing.B) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		wrapped := HTTPMiddleware(handler)

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("GET", "/", nil)
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
		}
	})
}
