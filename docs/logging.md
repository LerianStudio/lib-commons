# Logging

The logging utilities provide a flexible, pluggable logging interface that supports multiple implementations including Zap, structured logging, and log obfuscation.

## Overview

Features:
- Pluggable logging interface
- Structured logging with fields
- Log level support (Debug, Info, Warn, Error, Fatal)
- Context integration
- Field obfuscation for sensitive data
- Multiple backend implementations

## Logger Interface

```go
type Logger interface {
    Info(args ...any)
    Infof(format string, args ...any)
    Error(args ...any)
    Errorf(format string, args ...any)
    Warn(args ...any)
    Warnf(format string, args ...any)
    Debug(args ...any)
    Debugf(format string, args ...any)
    Fatal(args ...any)
    Fatalf(format string, args ...any)
    WithFields(fields ...any) Logger
    WithDefaultMessageTemplate(message string) Logger
    Sync() error
}
```

## Basic Usage

### Creating a Logger

```go
import (
    "github.com/LerianStudio/lib-commons/commons/log"
    "github.com/LerianStudio/lib-commons/commons/zap"
)

// Create a Zap logger
config := zap.Configuration{
    Level:    "info",
    Encoding: "json",
}
logger := zap.NewZapLogger(config)

// Or use a simple structured logger
logger := log.NewStructuredLogger()

// Or use a no-op logger for testing
logger := log.NewNilLogger()
```

### Basic Logging

```go
// Simple messages
logger.Info("Server started")
logger.Error("Failed to connect to database")

// Formatted messages
logger.Infof("Processing %d items", count)
logger.Errorf("Failed to process item %s: %v", itemID, err)

// With fields
logger.WithFields(
    "user_id", userID,
    "action", "login",
    "ip", request.RemoteAddr,
).Info("User logged in")
```

### Structured Logging

```go
// Chain multiple field additions
logger.
    WithFields("service", "payment").
    WithFields("transaction_id", txID).
    WithFields("amount", 100.50).
    Info("Payment processed")

// Using with default message template
logger.
    WithDefaultMessageTemplate("Database query executed").
    WithFields(
        "query", "SELECT * FROM users",
        "duration_ms", 42,
        "rows_returned", 10,
    ).
    Info()
```

## Advanced Usage

### Context Integration

```go
import (
    "context"
    "github.com/LerianStudio/lib-commons/commons"
)

func ProcessRequest(ctx context.Context, requestID string) {
    // Get logger from context
    logger := commons.NewLoggerFromContext(ctx)
    
    // Add request ID to all logs
    logger = logger.WithFields("request_id", requestID)
    
    // Update context with new logger
    ctx = commons.ContextWithLogger(ctx, logger)
    
    // All subsequent logs will include request_id
    logger.Info("Processing request")
    
    // Pass context to other functions
    processPayment(ctx)
}

func processPayment(ctx context.Context) {
    logger := commons.NewLoggerFromContext(ctx)
    // This will automatically include the request_id
    logger.Info("Processing payment")
}
```

### Log Obfuscation

Sensitive fields can be automatically obfuscated:

```go
// Set environment variable
// SECURE_LOG_FIELDS=password,apiKey,creditCard

logger.WithFields(
    "user", "john@example.com",
    "password", "secret123",      // Will be obfuscated
    "apiKey", "sk_live_abc123",   // Will be obfuscated
    "action", "login",
).Info("User authentication")

// Output: {"user":"john@example.com","password":"***","apiKey":"***","action":"login"}
```

### Error Logging with Stack Traces

```go
func ProcessOrder(orderID string) error {
    logger := log.NewStructuredLogger()
    
    err := validateOrder(orderID)
    if err != nil {
        logger.WithFields(
            "order_id", orderID,
            "error", err.Error(),
            "stack_trace", fmt.Sprintf("%+v", err),
        ).Error("Order validation failed")
        return err
    }
    
    return nil
}
```

### Log Levels

```go
// Debug - Detailed information for debugging
logger.Debug("Entering function ProcessPayment", "payment_id", paymentID)

// Info - General informational messages
logger.Info("Payment processed successfully", "amount", 100.50)

// Warn - Warning messages for potentially harmful situations
logger.Warn("Payment retry attempt", "attempt", 3, "max_attempts", 5)

// Error - Error messages for failures
logger.Error("Payment failed", "error", err, "payment_id", paymentID)

// Fatal - Critical errors that require program termination
logger.Fatal("Unable to connect to payment gateway", "error", err)
```

## Zap Logger Configuration

### JSON Encoding

```go
config := zap.Configuration{
    Level:        "info",
    Encoding:     "json",
    OutputPaths:  []string{"stdout"},
    Development:  false,
}
logger := zap.NewZapLogger(config)
```

### Console Encoding

```go
config := zap.Configuration{
    Level:        "debug",
    Encoding:     "console",
    OutputPaths:  []string{"stdout", "/var/log/app.log"},
    Development:  true,
}
logger := zap.NewZapLogger(config)
```

### Custom Fields

```go
// Add fields that appear in every log
logger := zap.NewZapLogger(config).WithFields(
    "service", "payment-api",
    "version", "1.0.0",
    "environment", os.Getenv("ENV"),
)
```

## Best Practices

### 1. Use Structured Logging

```go
// Good
logger.WithFields(
    "user_id", userID,
    "order_id", orderID,
    "amount", amount,
).Info("Order placed")

// Avoid
logger.Infof("Order %s placed by user %s for amount %f", orderID, userID, amount)
```

### 2. Log at the Right Level

```go
// Debug: Detailed information for debugging
logger.Debug("Calculated tax", "subtotal", 100, "tax_rate", 0.08, "tax", 8)

// Info: Important business events
logger.Info("Order completed", "order_id", orderID)

// Warn: Potentially problematic situations
logger.Warn("Slow database query", "duration_ms", 5000)

// Error: Errors that need attention
logger.Error("Payment failed", "error", err)
```

### 3. Include Context

```go
logger.WithFields(
    "correlation_id", correlationID,
    "user_id", userID,
    "session_id", sessionID,
    "request_path", r.URL.Path,
    "method", r.Method,
).Info("HTTP request received")
```

### 4. Avoid Logging Sensitive Data

```go
// Configure obfuscation
os.Setenv("SECURE_LOG_FIELDS", "password,ssn,creditCard,apiKey")

// These fields will be automatically obfuscated
logger.WithFields(
    "user", email,
    "password", password,  // Will show as ***
).Info("Login attempt")
```

### 5. Use Defer for Cleanup

```go
logger := zap.NewZapLogger(config)
defer logger.Sync() // Ensure all logs are flushed
```

## Performance Considerations

1. **Use lazy evaluation**: Don't construct expensive log messages if they won't be logged
2. **Batch field additions**: Add multiple fields in one call
3. **Reuse loggers**: Create logger once and reuse with `WithFields`
4. **Async logging**: Zap logger uses async writing by default
5. **Sampling**: For high-volume logs, consider sampling

```go
// Example of conditional logging for performance
if logger.Level() <= log.DebugLevel {
    expensiveData := computeExpensiveDebugInfo()
    logger.Debug("Detailed debug info", "data", expensiveData)
}
```