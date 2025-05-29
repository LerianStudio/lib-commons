# Request ID Propagation

The request ID package provides utilities for generating and propagating unique request identifiers across distributed systems, essential for tracing and debugging.

## Overview

Features:
- UUID-based request ID generation
- Context-based propagation
- HTTP middleware for automatic handling
- Fiber framework integration
- Custom header support

## Basic Usage

```go
import (
    "github.com/LerianStudio/lib-commons/commons/requestid"
)

// Generate a new request ID
reqID := requestid.Generate()

// Add to context
ctx := requestid.WithRequestID(context.Background(), reqID)

// Retrieve from context
id := requestid.FromContext(ctx)
```

## HTTP Middleware

### Standard HTTP Middleware

```go
// Create middleware with default settings
middleware := requestid.HTTPMiddleware()

// Or with custom header
middleware := requestid.HTTPMiddlewareWithHeader("X-Correlation-ID")

// Apply to your HTTP server
mux := http.NewServeMux()
mux.HandleFunc("/api/", handleAPI)
handler := middleware(mux)
http.ListenAndServe(":8080", handler)
```

### Complete HTTP Example

```go
func main() {
    // Create middleware
    reqIDMiddleware := requestid.HTTPMiddleware()
    
    // Create handler
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Get request ID from context
        reqID := requestid.FromContext(r.Context())
        
        // Use in logging
        log.Printf("[%s] Processing request to %s", reqID, r.URL.Path)
        
        // Include in response
        w.Header().Set("X-Request-ID", reqID)
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("OK"))
    })
    
    // Apply middleware and start server
    http.ListenAndServe(":8080", reqIDMiddleware(handler))
}
```

## Fiber Integration

```go
import (
    "github.com/gofiber/fiber/v2"
    "github.com/LerianStudio/lib-commons/commons/requestid"
)

app := fiber.New()

// Add request ID middleware
app.Use(requestid.FiberMiddleware())

// Or with custom header
app.Use(requestid.FiberMiddlewareWithHeader("X-Trace-ID"))

// Use in handlers
app.Get("/api/users", func(c *fiber.Ctx) error {
    reqID := requestid.FromContext(c.Context())
    
    log.Printf("[%s] Fetching users", reqID)
    
    // Include in response
    c.Set("X-Request-ID", reqID)
    return c.JSON(users)
})
```

## Propagating to External Services

### HTTP Client

```go
type TracedHTTPClient struct {
    client *http.Client
}

func (c *TracedHTTPClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
    // Get request ID from context
    if reqID := requestid.FromContext(ctx); reqID != "" {
        req.Header.Set("X-Request-ID", reqID)
    }
    
    return c.client.Do(req)
}

// Usage
func callExternalAPI(ctx context.Context) error {
    client := &TracedHTTPClient{client: &http.Client{}}
    
    req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.example.com/data", nil)
    resp, err := client.Do(ctx, req)
    if err != nil {
        reqID := requestid.FromContext(ctx)
        log.Printf("[%s] Failed to call external API: %v", reqID, err)
        return err
    }
    defer resp.Body.Close()
    
    return nil
}
```

### gRPC Client

```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/metadata"
)

// Unary client interceptor
func RequestIDUnaryClientInterceptor() grpc.UnaryClientInterceptor {
    return func(ctx context.Context, method string, req, reply interface{}, 
        cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
        
        // Get request ID from context
        if reqID := requestid.FromContext(ctx); reqID != "" {
            // Add to gRPC metadata
            ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", reqID)
        }
        
        return invoker(ctx, method, req, reply, cc, opts...)
    }
}

// Usage
conn, err := grpc.Dial(address,
    grpc.WithUnaryInterceptor(RequestIDUnaryClientInterceptor()),
)
```

## Logging Integration

### With Structured Logging

```go
func ContextualLogger(ctx context.Context) *log.Logger {
    reqID := requestid.FromContext(ctx)
    if reqID == "" {
        reqID = "no-request-id"
    }
    
    return log.With(
        "request_id", reqID,
    )
}

// Usage
func processOrder(ctx context.Context, orderID string) error {
    logger := ContextualLogger(ctx)
    
    logger.Info("Processing order", "order_id", orderID)
    
    if err := validateOrder(orderID); err != nil {
        logger.Error("Order validation failed", "error", err)
        return err
    }
    
    logger.Info("Order processed successfully")
    return nil
}
```

### With Zap

```go
func WithRequestID(ctx context.Context, logger *zap.Logger) *zap.Logger {
    if reqID := requestid.FromContext(ctx); reqID != "" {
        return logger.With(zap.String("request_id", reqID))
    }
    return logger
}
```

## Database Query Logging

```go
type TracedDB struct {
    db *sql.DB
}

func (t *TracedDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
    reqID := requestid.FromContext(ctx)
    start := time.Now()
    
    rows, err := t.db.QueryContext(ctx, query, args...)
    
    duration := time.Since(start)
    if err != nil {
        log.Printf("[%s] Query failed (duration: %v): %s", reqID, duration, query)
    } else {
        log.Printf("[%s] Query succeeded (duration: %v): %s", reqID, duration, query)
    }
    
    return rows, err
}
```

## Async Operations

### Goroutine Propagation

```go
func ProcessAsync(ctx context.Context, data []Item) {
    reqID := requestid.FromContext(ctx)
    
    for _, item := range data {
        // Important: capture context value
        go func(item Item, reqID string) {
            // Create new context with request ID
            ctx := requestid.WithRequestID(context.Background(), reqID)
            
            log.Printf("[%s] Processing item %s", reqID, item.ID)
            processItem(ctx, item)
        }(item, reqID)
    }
}
```

### Message Queue Integration

```go
type Message struct {
    ID        string
    RequestID string
    Data      interface{}
}

func PublishMessage(ctx context.Context, data interface{}) error {
    msg := Message{
        ID:        uuid.New().String(),
        RequestID: requestid.FromContext(ctx),
        Data:      data,
    }
    
    return messageQueue.Publish(msg)
}

func ConsumeMessage(msg Message) {
    // Restore request ID in context
    ctx := requestid.WithRequestID(context.Background(), msg.RequestID)
    
    log.Printf("[%s] Processing message %s", msg.RequestID, msg.ID)
    processMessage(ctx, msg.Data)
}
```

## Custom Request ID Generation

```go
// Override default UUID generation
func CustomIDGenerator() string {
    // Example: timestamp-based ID
    return fmt.Sprintf("%d-%s", time.Now().UnixNano(), randString(8))
}

// Use custom generator in middleware
func CustomRequestIDMiddleware(generator func() string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            reqID := r.Header.Get("X-Request-ID")
            if reqID == "" {
                reqID = generator()
            }
            
            ctx := requestid.WithRequestID(r.Context(), reqID)
            w.Header().Set("X-Request-ID", reqID)
            
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

## Tracing Integration

### OpenTelemetry

```go
import (
    "go.opentelemetry.io/otel/trace"
    "go.opentelemetry.io/otel/attribute"
)

func StartSpanWithRequestID(ctx context.Context, name string) (context.Context, trace.Span) {
    tracer := otel.Tracer("service-name")
    ctx, span := tracer.Start(ctx, name)
    
    // Add request ID as span attribute
    if reqID := requestid.FromContext(ctx); reqID != "" {
        span.SetAttributes(attribute.String("request.id", reqID))
    }
    
    return ctx, span
}
```

## Best Practices

1. **Generate at edge**: Create request IDs at system entry points
2. **Propagate everywhere**: Pass through all service calls
3. **Log consistently**: Include in all log entries
4. **Use standard headers**: Stick to X-Request-ID or similar
5. **Handle missing IDs**: Generate if not present
6. **Include in errors**: Add to error responses for debugging

## Testing

```go
func TestRequestIDPropagation(t *testing.T) {
    // Create test handler
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        reqID := requestid.FromContext(r.Context())
        assert.NotEmpty(t, reqID)
        
        // Echo back the request ID
        w.Header().Set("X-Request-ID", reqID)
        w.WriteHeader(http.StatusOK)
    })
    
    // Wrap with middleware
    wrapped := requestid.HTTPMiddleware()(handler)
    
    // Test with provided ID
    req := httptest.NewRequest("GET", "/test", nil)
    req.Header.Set("X-Request-ID", "test-123")
    rec := httptest.NewRecorder()
    
    wrapped.ServeHTTP(rec, req)
    
    assert.Equal(t, "test-123", rec.Header().Get("X-Request-ID"))
    
    // Test without provided ID
    req2 := httptest.NewRequest("GET", "/test", nil)
    rec2 := httptest.NewRecorder()
    
    wrapped.ServeHTTP(rec2, req2)
    
    generatedID := rec2.Header().Get("X-Request-ID")
    assert.NotEmpty(t, generatedID)
    assert.NotEqual(t, "test-123", generatedID)
}
```