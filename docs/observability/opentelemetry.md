# OpenTelemetry Integration

The OpenTelemetry package provides comprehensive observability capabilities including distributed tracing, metrics collection, and structured logging.

## Table of Contents

- [Overview](#overview)
- [Setup and Configuration](#setup-and-configuration)
- [Tracing](#tracing)
- [Metrics](#metrics)
- [Logging](#logging)
- [Middleware Integration](#middleware-integration)
- [Best Practices](#best-practices)
- [Examples](#examples)

## Overview

The observability package integrates OpenTelemetry to provide:

- **Distributed Tracing**: Track requests across multiple services
- **Metrics Collection**: Application and business metrics
- **Structured Logging**: Correlated log entries with trace context
- **Automatic Instrumentation**: HTTP, gRPC, and database instrumentation
- **Custom Instrumentation**: Manual spans and metrics

## Setup and Configuration

### Basic Setup

```go
import (
    "github.com/yourusername/commons-go/commons/observability"
)

// Create observability provider
provider, err := observability.NewProvider(
    observability.WithServiceName("my-service"),
    observability.WithServiceVersion("1.0.0"),
    observability.WithEnvironment("production"),
)
if err != nil {
    log.Fatal(err)
}
defer provider.Shutdown(context.Background())

// Set as global provider
observability.SetGlobalProvider(provider)
```

### Configuration Options

```go
// Full configuration example
provider, err := observability.NewProvider(
    // Service identity
    observability.WithServiceName("payment-service"),
    observability.WithServiceVersion("2.1.0"),
    observability.WithEnvironment("staging"),
    
    // Tracing configuration
    observability.WithTracingEndpoint("http://jaeger:14268/api/traces"),
    observability.WithTracingSampler(0.1), // 10% sampling
    
    // Metrics configuration
    observability.WithMetricsEndpoint("http://prometheus:9090/api/v1/write"),
    observability.WithMetricsInterval(15 * time.Second),
    
    // Resource attributes
    observability.WithResourceAttributes(map[string]string{
        "deployment.environment": "staging",
        "service.namespace":      "payments",
        "service.instance.id":    hostname,
    }),
)
```

## Tracing

### Manual Span Creation

```go
func ProcessPayment(ctx context.Context, request *PaymentRequest) error {
    // Get tracer from context or global
    tracer := observability.TracerFromContext(ctx)
    
    // Start span
    ctx, span := tracer.Start(ctx, "process_payment")
    defer span.End()
    
    // Add attributes
    span.SetAttributes(
        attribute.String("payment.method", request.Method),
        attribute.Float64("payment.amount", request.Amount),
        attribute.String("user.id", request.UserID),
    )
    
    // Validate payment
    if err := validatePayment(ctx, request); err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, "payment validation failed")
        return err
    }
    
    // Process payment
    result, err := chargePayment(ctx, request)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, "payment processing failed")
        return err
    }
    
    // Add result attributes
    span.SetAttributes(
        attribute.String("payment.transaction_id", result.TransactionID),
        attribute.String("payment.status", result.Status),
    )
    
    span.SetStatus(codes.Ok, "payment processed successfully")
    return nil
}
```

### Nested Spans

```go
func validatePayment(ctx context.Context, request *PaymentRequest) error {
    tracer := observability.TracerFromContext(ctx)
    ctx, span := tracer.Start(ctx, "validate_payment")
    defer span.End()
    
    // Validate card
    if err := validateCard(ctx, request.Card); err != nil {
        span.RecordError(err)
        return err
    }
    
    // Check fraud
    if err := checkFraud(ctx, request); err != nil {
        span.RecordError(err)
        return err
    }
    
    return nil
}

func validateCard(ctx context.Context, card *Card) error {
    tracer := observability.TracerFromContext(ctx)
    ctx, span := tracer.Start(ctx, "validate_card")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("card.type", card.Type),
        attribute.String("card.last_four", card.LastFour),
    )
    
    // Validation logic...
    return nil
}
```

## Metrics

### Counter Metrics

```go
// Create counter
paymentCounter, err := observability.NewCounter(
    "payments_total",
    "Total number of payment attempts",
    "unit",
)

// Record metric
observability.RecordMetric(ctx, provider, "payments_total", 1,
    attribute.String("method", "credit_card"),
    attribute.String("status", "success"),
)
```

### Histogram Metrics

```go
// Create histogram
durationHistogram, err := observability.NewHistogram(
    "payment_duration",
    "Payment processing duration",
    "milliseconds",
)

// Record duration
start := time.Now()
// ... process payment ...
duration := time.Since(start)

observability.RecordDuration(ctx, provider, "payment_duration", start,
    attribute.String("method", "credit_card"),
)
```

### Gauge Metrics

```go
// Record gauge value
observability.RecordGauge(ctx, provider, "active_connections", 42,
    attribute.String("service", "database"),
)
```

## Logging

### Structured Logging with Trace Context

```go
func ProcessOrder(ctx context.Context, orderID string) error {
    logger := observability.LoggerFromContext(ctx)
    
    logger.Info("Processing order",
        "order_id", orderID,
        "user_id", getUserID(ctx),
    )
    
    if err := validateOrder(orderID); err != nil {
        logger.Error("Order validation failed",
            "order_id", orderID,
            "error", err,
        )
        return err
    }
    
    logger.Info("Order processed successfully",
        "order_id", orderID,
    )
    
    return nil
}
```

## Middleware Integration

### HTTP Middleware

```go
// Create Fiber middleware
fiberMiddleware, err := observability.NewFiberMiddleware(provider,
    observability.WithIgnorePathsFiber("/health", "/metrics"),
    observability.WithSecurityDefaultsFiber(),
    observability.WithUserIDExtractor(func(c *fiber.Ctx) string {
        return c.Get("X-User-ID")
    }),
)

app := fiber.New()
app.Use(fiberMiddleware)

app.Get("/api/users/:id", func(c *fiber.Ctx) error {
    // Context automatically includes tracing, metrics, and logging
    logger := observability.LoggerFromContext(c.Context())
    logger.Info("Fetching user", "user_id", c.Params("id"))
    
    return c.JSON(user)
})
```

### HTTP Client Middleware

```go
// Create HTTP client with observability
client := &http.Client{
    Transport: observability.NewHTTPClientMiddleware(provider,
        observability.WithIgnorePaths("/health"),
        observability.WithSecurityDefaults(),
    )(http.DefaultTransport),
}

// All requests are automatically traced
resp, err := client.Get("https://api.example.com/users")
```

### gRPC Interceptors

```go
// Server interceptor
server := grpc.NewServer(
    grpc.UnaryInterceptor(observability.UnaryServerInterceptor(provider)),
    grpc.StreamInterceptor(observability.StreamServerInterceptor(provider)),
)

// Client interceptor
conn, err := grpc.Dial(address,
    grpc.WithUnaryInterceptor(observability.UnaryClientInterceptor(provider)),
    grpc.WithStreamInterceptor(observability.StreamClientInterceptor(provider)),
)
```

## Best Practices

### 1. Meaningful Span Names

```go
// Good: Action-oriented names
ctx, span := tracer.Start(ctx, "validate_user_credentials")
ctx, span := tracer.Start(ctx, "charge_credit_card")
ctx, span := tracer.Start(ctx, "send_notification_email")

// Avoid: Generic names
ctx, span := tracer.Start(ctx, "function1")
ctx, span := tracer.Start(ctx, "process")
```

### 2. Rich Attributes

```go
span.SetAttributes(
    // Business context
    attribute.String("user.id", userID),
    attribute.String("order.id", orderID),
    attribute.Float64("order.total", order.Total),
    
    // Technical context
    attribute.String("db.operation", "SELECT"),
    attribute.String("db.table", "orders"),
    attribute.Int("db.rows_affected", rowCount),
    
    // Request context
    attribute.String("http.method", "POST"),
    attribute.String("http.url", request.URL.String()),
    attribute.Int("http.status_code", statusCode),
)
```

### 3. Error Handling

```go
if err != nil {
    // Always record errors
    span.RecordError(err)
    
    // Set appropriate status
    if isUserError(err) {
        span.SetStatus(codes.Error, "user input validation failed")
    } else {
        span.SetStatus(codes.Error, "internal server error")
    }
    
    // Log with context
    logger.Error("Operation failed", "error", err)
    
    return err
}
```

### 4. Resource Cleanup

```go
func ProcessRequest(ctx context.Context) error {
    ctx, span := tracer.Start(ctx, "process_request")
    defer span.End() // Always defer span.End()
    
    // Use defer for cleanup
    defer func() {
        if r := recover(); r != nil {
            span.RecordError(fmt.Errorf("panic: %v", r))
            span.SetStatus(codes.Error, "panic occurred")
            panic(r)
        }
    }()
    
    // Process...
    return nil
}
```

## Examples

### Complete Service Example

```go
package main

import (
    "context"
    "log"
    "net/http"
    
    "github.com/gofiber/fiber/v2"
    "github.com/yourusername/commons-go/commons/observability"
    "go.opentelemetry.io/otel/attribute"
)

func main() {
    // Initialize observability
    provider, err := observability.NewProvider(
        observability.WithServiceName("user-service"),
        observability.WithServiceVersion("1.0.0"),
        observability.WithEnvironment("production"),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer provider.Shutdown(context.Background())
    
    // Create Fiber app with middleware
    app := fiber.New()
    
    middleware, err := observability.NewFiberMiddleware(provider)
    if err != nil {
        log.Fatal(err)
    }
    app.Use(middleware)
    
    // Define routes
    app.Get("/users/:id", getUserHandler)
    app.Post("/users", createUserHandler)
    
    log.Println("Server starting on :8080")
    log.Fatal(app.Listen(":8080"))
}

func getUserHandler(c *fiber.Ctx) error {
    ctx := c.Context()
    userID := c.Params("id")
    
    // Get observability components from context
    tracer := observability.TracerFromContext(ctx)
    logger := observability.LoggerFromContext(ctx)
    
    // Start custom span
    ctx, span := tracer.Start(ctx, "get_user")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("user.id", userID),
        attribute.String("operation", "get_user"),
    )
    
    logger.Info("Fetching user", "user_id", userID)
    
    // Simulate business logic
    user, err := fetchUserFromDB(ctx, userID)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, "failed to fetch user")
        logger.Error("Failed to fetch user", "user_id", userID, "error", err)
        return c.Status(500).JSON(fiber.Map{"error": "internal server error"})
    }
    
    // Record success metrics
    observability.RecordMetric(ctx, provider, "user_fetched_total", 1,
        attribute.String("user.id", userID),
    )
    
    logger.Info("User fetched successfully", "user_id", userID)
    return c.JSON(user)
}

func fetchUserFromDB(ctx context.Context, userID string) (*User, error) {
    tracer := observability.TracerFromContext(ctx)
    ctx, span := tracer.Start(ctx, "db.fetch_user")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("db.operation", "SELECT"),
        attribute.String("db.table", "users"),
        attribute.String("user.id", userID),
    )
    
    // Simulate database call
    start := time.Now()
    
    // ... actual database logic ...
    user := &User{ID: userID, Name: "John Doe"}
    
    // Record database duration
    observability.RecordDuration(ctx, provider, "db_query_duration", start,
        attribute.String("db.table", "users"),
        attribute.String("db.operation", "SELECT"),
    )
    
    return user, nil
}

type User struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}
```

### Testing with Observability

```go
func TestUserService(t *testing.T) {
    // Create test provider
    provider, err := observability.NewProvider(
        observability.WithServiceName("user-service-test"),
        observability.WithTracingEndpoint(""), // Disable external tracing
    )
    require.NoError(t, err)
    defer provider.Shutdown(context.Background())
    
    // Create context with observability
    ctx := context.Background()
    ctx = observability.WithProvider(ctx, provider)
    
    // Test your service
    user, err := userService.GetUser(ctx, "test-user-123")
    assert.NoError(t, err)
    assert.Equal(t, "test-user-123", user.ID)
    
    // Verify traces and metrics were recorded
    // (Implementation depends on your testing setup)
}
``` 