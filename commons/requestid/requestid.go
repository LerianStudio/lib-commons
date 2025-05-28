package requestid

import (
	"context"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// DefaultHeader is the default header name for request ID
const DefaultHeader = "X-Request-ID"

// contextKey is the key type for storing request ID in context
type contextKey struct{}

// requestIDKey is the key for storing request ID in context
var requestIDKey = contextKey{}

// Generator is a function that generates request IDs
type Generator func() string

// generator is the current ID generator
var generator Generator = DefaultGenerator

// DefaultGenerator generates a UUID v4 as request ID
func DefaultGenerator() string {
	return uuid.New().String()
}

// SetGenerator sets a custom request ID generator
func SetGenerator(g Generator) {
	generator = g
}

// Generate generates a new request ID using the current generator
func Generate() string {
	return generator()
}

// NewContext returns a new context with the given request ID
func NewContext(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// FromContext extracts the request ID from context
func FromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// EnsureContext ensures the context has a request ID, generating one if needed
func EnsureContext(ctx context.Context) (context.Context, string) {
	if id := FromContext(ctx); id != "" {
		return ctx, id
	}
	
	id := Generate()
	return NewContext(ctx, id), id
}

// ExtractFromHeaders extracts request ID from headers, checking multiple possible headers
func ExtractFromHeaders(headers http.Header, headerNames ...string) string {
	// If no header names provided, use default
	if len(headerNames) == 0 {
		headerNames = []string{DefaultHeader}
	}
	
	// Check each header in order
	for _, name := range headerNames {
		if id := headers.Get(name); id != "" {
			return id
		}
	}
	
	return ""
}

// HTTPMiddleware is a middleware that ensures request ID propagation
func HTTPMiddleware(next http.Handler) http.Handler {
	return HTTPMiddlewareWithHeader(next, DefaultHeader)
}

// HTTPMiddlewareWithHeader is a middleware with custom header name
func HTTPMiddlewareWithHeader(next http.Handler, headerName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to get ID from header
		requestID := r.Header.Get(headerName)
		
		// Generate if not present
		if requestID == "" {
			requestID = Generate()
		}
		
		// Add to context
		ctx := NewContext(r.Context(), requestID)
		r = r.WithContext(ctx)
		
		// Add to response header
		w.Header().Set(headerName, requestID)
		
		// Continue
		next.ServeHTTP(w, r)
	})
}

// FiberMiddleware is a Fiber middleware that ensures request ID propagation
func FiberMiddleware() fiber.Handler {
	return FiberMiddlewareWithHeader(DefaultHeader)
}

// FiberMiddlewareWithHeader is a Fiber middleware with custom header name
func FiberMiddlewareWithHeader(headerName string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Try to get ID from header
		requestID := c.Get(headerName)
		
		// Generate if not present
		if requestID == "" {
			requestID = Generate()
		}
		
		// Store in locals for access
		c.Locals("requestID", requestID)
		
		// Add to response header
		c.Set(headerName, requestID)
		
		// Continue
		return c.Next()
	}
}

// FromFiberContext extracts request ID from Fiber context
func FromFiberContext(c *fiber.Ctx) string {
	if id, ok := c.Locals("requestID").(string); ok {
		return id
	}
	return ""
}

// HTTPTransport is an http.RoundTripper that adds request ID to outgoing requests
type HTTPTransport struct {
	Base       http.RoundTripper
	HeaderName string
}

// RoundTrip implements http.RoundTripper
func (t *HTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Get request ID from context
	if id := FromContext(req.Context()); id != "" {
		// Clone request to avoid modifying the original
		req = req.Clone(req.Context())
		
		// Set header
		headerName := t.HeaderName
		if headerName == "" {
			headerName = DefaultHeader
		}
		req.Header.Set(headerName, id)
	}
	
	// Use base transport
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	
	return base.RoundTrip(req)
}

// NewHTTPClient creates an HTTP client that propagates request IDs
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &HTTPTransport{
			Base:       http.DefaultTransport,
			HeaderName: DefaultHeader,
		},
	}
}

// NewHTTPClientWithTransport creates an HTTP client with custom transport
func NewHTTPClientWithTransport(base http.RoundTripper, headerName string) *http.Client {
	return &http.Client{
		Transport: &HTTPTransport{
			Base:       base,
			HeaderName: headerName,
		},
	}
}