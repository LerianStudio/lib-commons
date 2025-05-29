# Context Management

The context management utilities provide a standardized way to pass request-scoped values through your application, including loggers, tracers, and request identifiers.

## Overview

Context management is crucial for:
- Distributed tracing across services
- Structured logging with request correlation
- Passing request-scoped values safely

## Basic Usage

### Adding Logger to Context

```go
import (
    "context"
    "github.com/LerianStudio/lib-commons/commons"
)

// Create a logger
logger := commons.NewLogger()

// Add logger to context
ctx := commons.ContextWithLogger(context.Background(), logger)

// Retrieve logger from context
logger = commons.NewLoggerFromContext(ctx)
logger.Info("Processing request")
```

### Adding Tracer to Context

```go
import (
    "github.com/LerianStudio/lib-commons/commons"
    "go.opentelemetry.io/otel"
)

// Get a tracer
tracer := otel.Tracer("service-name")

// Add tracer to context
ctx := commons.ContextWithTracer(context.Background(), tracer)

// Retrieve tracer from context
tracer = commons.NewTracerFromContext(ctx)
_, span := tracer.Start(ctx, "operation-name")
defer span.End()
```

### Adding Header ID for Request Tracking

```go
// Add header ID (request ID) to context
headerID := "req-123456"
ctx := commons.ContextWithHeaderID(context.Background(), headerID)

// Retrieve header ID from context
retrievedID := commons.NewHeaderIDFromContext(ctx)
logger.WithFields("request_id", retrievedID).Info("Processing request")
```

## Advanced Usage

### Custom Context Values

```go
// Store custom values using the CustomContextKey
customData := commons.CustomContextKeyValue{
    HeaderID: "req-123456",
    Tracer:   tracer,
    Logger:   logger,
}

ctx := context.WithValue(context.Background(), commons.CustomContextKey, customData)

// Retrieve custom values
if data, ok := ctx.Value(commons.CustomContextKey).(commons.CustomContextKeyValue); ok {
    // Use the custom data
    logger := data.Logger
    tracer := data.Tracer
}
```

### HTTP Middleware Example

```go
func RequestIDMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Extract or generate request ID
        requestID := r.Header.Get("X-Request-ID")
        if requestID == "" {
            requestID = uuid.New().String()
        }
        
        // Add to context
        ctx := commons.ContextWithHeaderID(r.Context(), requestID)
        ctx = commons.ContextWithLogger(ctx, logger.WithFields("request_id", requestID))
        
        // Pass context to next handler
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### Service Layer Example

```go
type UserService struct {
    db *sql.DB
}

func (s *UserService) GetUser(ctx context.Context, userID string) (*User, error) {
    logger := commons.NewLoggerFromContext(ctx)
    tracer := commons.NewTracerFromContext(ctx)
    
    // Start a span
    ctx, span := tracer.Start(ctx, "UserService.GetUser")
    defer span.End()
    
    logger.Info("Fetching user", "user_id", userID)
    
    // Database query with context
    var user User
    err := s.db.QueryRowContext(ctx, "SELECT * FROM users WHERE id = $1", userID).Scan(&user)
    if err != nil {
        logger.Error("Failed to fetch user", "error", err)
        span.RecordError(err)
        return nil, err
    }
    
    return &user, nil
}
```

## Best Practices

1. **Always pass context**: Pass context as the first parameter to functions
2. **Don't store context**: Never store context in structs
3. **Context cancellation**: Respect context cancellation in long-running operations
4. **Minimal context values**: Only store request-scoped values in context
5. **Type-safe access**: Use typed functions to access context values

## Thread Safety

All context operations are thread-safe. The context package uses immutable values, creating new contexts rather than modifying existing ones.

## Performance Considerations

- Context values are stored in a linked list, so lookup is O(n)
- Keep the number of context values minimal
- Use context for request-scoped data only
- For application-wide configuration, use dependency injection instead

## Integration with OpenTelemetry

The context utilities are designed to work seamlessly with OpenTelemetry:

```go
import (
    "github.com/LerianStudio/lib-commons/commons"
    "go.opentelemetry.io/otel"
)

func ProcessRequest(ctx context.Context) error {
    // Extract tracer from context
    tracer := commons.NewTracerFromContext(ctx)
    if tracer == nil {
        tracer = otel.Tracer("default")
    }
    
    // Start a span
    ctx, span := tracer.Start(ctx, "ProcessRequest")
    defer span.End()
    
    // Logger automatically includes trace ID
    logger := commons.NewLoggerFromContext(ctx)
    logger.Info("Processing request")
    
    return nil
}
```