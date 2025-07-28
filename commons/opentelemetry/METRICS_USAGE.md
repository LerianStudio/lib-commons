# OpenTelemetry Metrics Usage Guide

This guide demonstrates how to use the OpenTelemetry MetricsFactory for creating and managing metrics in your applications.

## Table of Contents

- [Overview](#overview)
- [Metric Types](#metric-types)
- [Basic Usage](#basic-usage)
- [Financial Transaction Examples](#financial-transaction-examples)
- [High-Throughput Scenarios](#high-throughput-scenarios)
- [Error Handling](#error-handling)
- [Custom Histogram Buckets](#custom-histogram-buckets)
- [Best Practices](#best-practices)
- [Naming Conventions](#naming-conventions)

## Overview

The MetricsFactory provides a thread-safe, high-performance way to create and manage OpenTelemetry metrics with:

- **Lazy initialization** using `sync.Map` for concurrent access
- **Fluent API** with method chaining
- **Automatic aggregation** (no trace correlation to avoid high cardinality)
- **Graceful error handling** with no-op fallbacks

## Metric Types

### Counter
Monotonically increasing values (e.g., requests processed, errors occurred)

### Gauge  
Current values that can go up or down (e.g., memory usage, queue size, account balance)

### Histogram
Distribution of values with configurable buckets (e.g., response times, transaction amounts)

## Basic Usage

```go
import (
    "context"
    "github.com/LerianStudio/lib-commons/commons/opentelemetry"
)

func basicMetricsExample(telemetry *opentelemetry.Telemetry, ctx context.Context) {
    factory := telemetry.MetricsFactory

    // Counter - increment by 1
    factory.Counter("requests.processed.count", 
        opentelemetry.MetricOption{
            Description: "Total requests processed",
            Unit:        "1", // dimensionless counter
        }).
        WithLabels(map[string]string{
            "service": "api-gateway",
            "status":  "success",
        }).
        Add(ctx, 1)

    // Gauge - set current value
    factory.Gauge("memory.usage.bytes", 
        opentelemetry.MetricOption{
            Description: "Current memory usage",
            Unit:        "By", // bytes
        }).
        WithLabels(map[string]string{
            "service": "api-gateway",
        }).
        Set(ctx, 1024000) // 1MB

    // Histogram - record a value
    factory.Histogram("request.duration.histogram", 
        opentelemetry.MetricOption{
            Description: "Request duration distribution",
            Unit:        "ms", // milliseconds
        }).
        WithLabels(map[string]string{
            "endpoint": "/api/users",
            "method":   "GET",
        }).
        Record(ctx, 150) // 150ms
}
```

## Financial Transaction Examples

### Transaction Processing Counter

```go
// Pattern: "domain.component.action.metric"
factory.Counter("transaction.payment.processed.count",
    opentelemetry.MetricOption{
        Description: "Total number of payment transactions processed",
        Unit:        "1", // dimensionless counter
    }).
    WithLabels(map[string]string{
        "ledger_id":        "ledger-123",
        "transaction_type": "payment",
        "status":           "success",
        "currency":         "USD",
    }).
    Add(ctx, 1)
```

### Account Creation Counter

```go
factory.Counter("onboarding.account.created.count",
    opentelemetry.MetricOption{
        Description: "Number of accounts created",
        Unit:        "1",
    }).
    WithLabels(map[string]string{
        "ledger_id":    "ledger-123",
        "account_type": "checking",
        "branch":       "main",
    }).
    Add(ctx, 1)
```

### Transaction Amount Distribution

```go
factory.Histogram("transaction.transfer.amount.histogram",
    opentelemetry.MetricOption{
        Description: "Distribution of transfer amounts",
        Unit:        "USD",
        Buckets:     []float64{1, 10, 100, 1000, 10000, 100000, 1000000}, // Custom buckets for financial amounts
    }).
    WithLabels(map[string]string{
        "ledger_id": "ledger-123",
        "currency":  "USD",
        "region":    "us-east-1",
    }).
    Record(ctx, 15000) // $150.00 (in cents)
```

### Account Balance Gauge

```go
factory.Gauge("account.balance.current.gauge",
    opentelemetry.MetricOption{
        Description: "Current account balance",
        Unit:        "USD",
    }).
    WithLabels(map[string]string{
        "account_id": "acc-456",
        "ledger_id":  "ledger-123",
        "currency":   "USD",
    }).
    Set(ctx, 250000) // $2,500.00 (in cents)
```

### Transaction Processing Latency

```go
start := time.Now()
// ... transaction processing logic ...
duration := time.Since(start)

factory.Histogram("transaction.processing.latency.histogram",
    opentelemetry.MetricOption{
        Description: "Transaction processing latency",
        Unit:        "s", // seconds
        // Uses DefaultLatencyBuckets automatically
    }).
    WithLabels(map[string]string{
        "ledger_id":        "ledger-123",
        "transaction_type": "transfer",
        "complexity":       "simple",
    }).
    Record(ctx, duration.Milliseconds())
```

## High-Throughput Scenarios

For high-throughput applications (e.g., 8000+ TPS), pre-create builders to avoid repeated map lookups:

```go
func highThroughputExample(telemetry *opentelemetry.Telemetry, ctx context.Context) {
    factory := telemetry.MetricsFactory

    // Pre-create builders for high-performance scenarios
    transactionCounter := factory.Counter("transaction.processed.total.count",
        opentelemetry.MetricOption{
            Description: "High-throughput transaction counter",
            Unit:        "1",
        }).
        WithLabels(map[string]string{
            "service": "transaction-processor",
            "version": "v1.2.3",
        })

    // Simulate high-throughput processing
    for i := 0; i < 1000; i++ {
        // Each transaction gets recorded with specific labels
        transactionCounter.
            WithLabels(map[string]string{
                "ledger_id": "ledger-456",
                "status":    "success",
            }).
            Add(ctx, 1)
    }

    // Transaction throughput gauge (transactions per second)
    factory.Gauge("transaction.throughput.current.gauge",
        opentelemetry.MetricOption{
            Description: "Current transaction throughput",
            Unit:        "1/s", // transactions per second
        }).
        WithLabels(map[string]string{
            "service":   "transaction-processor",
            "ledger_id": "ledger-456",
        }).
        Set(ctx, 7500) // 7,500 TPS
}
```

## Error Handling

The MetricsFactory handles errors gracefully with no-op fallbacks:

```go
func errorHandlingExample(telemetry *opentelemetry.Telemetry, ctx context.Context) {
    if telemetry == nil {
        // Telemetry not initialized - operations will be no-ops
        return
    }

    if telemetry.MetricsFactory == nil {
        // MetricsFactory not available - operations will be no-ops
        return
    }

    factory := telemetry.MetricsFactory

    // Even if metric creation fails internally, these operations won't panic
    // They will log errors and continue gracefully
    factory.Counter("invalid-metric-name-that-might-fail").
        WithLabels(map[string]string{
            "error_case": "demonstration",
        }).
        Add(ctx, 1) // This won't panic even if counter creation failed

    // The factory handles errors gracefully:
    // 1. Logs error via the provided logger
    // 2. Returns nil metric (handled by builders with nil checks)
    // 3. Application continues running without interruption
}
```

## Custom Histogram Buckets

### API Response Time Buckets

```go
factory.Histogram("api.response.time.histogram",
    opentelemetry.MetricOption{
        Description: "API response time distribution",
        Unit:        "ms",
        Buckets:     []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}, // Custom latency buckets
    }).
    WithLabels(map[string]string{
        "endpoint": "/api/v1/transactions",
        "method":   "POST",
    }).
    Record(ctx, 45) // 45ms response time
```

### Batch Processing Size Buckets

```go
factory.Histogram("batch.processing.size.histogram",
    opentelemetry.MetricOption{
        Description: "Batch processing size distribution",
        Unit:        "1",
        Buckets:     []float64{1, 10, 50, 100, 500, 1000, 5000, 10000}, // Batch size buckets
    }).
    WithLabels(map[string]string{
        "processor": "transaction-batch",
        "ledger_id": "ledger-123",
    }).
    Record(ctx, 250) // 250 transactions in batch
```

## Best Practices

### 1. Metric Naming Convention
Use the pattern: `domain.component.action.metric`

**Examples:**
- `transaction.payment.processed.count`
- `onboarding.account.created.count`
- `api.request.duration.histogram`

### 2. Units
Always specify appropriate units following OpenTelemetry semantic conventions:

- **Dimensionless counters**: `"1"`
- **Bytes**: `"By"`
- **Seconds**: `"s"`
- **Milliseconds**: `"ms"`
- **Currency**: `"USD"`, `"EUR"`, etc.
- **Rate**: `"1/s"` (per second)

### 3. Labels vs Attributes

**Use `WithLabels()` for:**
- String-only labels (convenience method)
- Simple key-value pairs

```go
.WithLabels(map[string]string{
    "service": "api-gateway",
    "status":  "success",
})
```

**Use `WithAttributes()` for:**
- Mixed data types (string, int, bool, float)
- More explicit control

```go
.WithAttributes(
    attribute.String("service", "api-gateway"),
    attribute.Int("retry_count", 3),
    attribute.Bool("cached", true),
)
```

### 4. Avoid High Cardinality
**❌ Don't include unique identifiers:**
```go
// BAD - creates separate metric series for each transaction
.WithLabels(map[string]string{
    "transaction_id": "tx-12345", // Unique per request
})
```

**✅ Use aggregatable labels:**
```go
// GOOD - allows proper aggregation
.WithLabels(map[string]string{
    "ledger_id":        "ledger-123",
    "transaction_type": "payment",
    "status":           "success",
})
```

### 5. Pre-create Builders for High Throughput
For performance-critical paths, pre-create metric builders:

```go
// Pre-create once
var transactionCounter = factory.Counter("transaction.count", option).
    WithLabels(commonLabels)

// Use many times
transactionCounter.Add(ctx, 1)
```

### 6. Graceful Degradation
Always check for nil telemetry and factory:

```go
if telemetry == nil || telemetry.MetricsFactory == nil {
    return // Graceful no-op
}
```

## Naming Conventions

### Metric Names
- Use lowercase with dots as separators
- Follow pattern: `domain.component.action.type`
- End with metric type: `.count`, `.gauge`, `.histogram`

### Label Keys
- Use snake_case
- Be descriptive but concise
- Avoid high cardinality values

### Common Labels
- `service`: Service name
- `version`: Service version
- `ledger_id`: Ledger identifier
- `organization_id`: Organization identifier
- `status`: Operation status (success, error, timeout)
- `method`: HTTP method or operation type
- `endpoint`: API endpoint or operation name

## Integration with Observability Stack

These metrics work seamlessly with:
- **Grafana**: For visualization and dashboards
- **Prometheus**: For storage and querying
- **OpenTelemetry Collector**: For processing and export
- **Jaeger/Zipkin**: Traces are separate but correlated via logs

The metrics are automatically exported asynchronously using batch processors for optimal performance.
