# lib-commons Documentation

Welcome to the comprehensive documentation for lib-commons, a Go library providing common utilities and components for building robust microservices.

## Table of Contents

### Core Components
- [Context Management](./context-management.md) - Enhanced context utilities for logging, tracing, and request tracking
- [Error Handling](./error-handling.md) - Standardized error handling and business error mapping
- [Logging](./logging.md) - Flexible logging interface with multiple implementations

### Database Connectors
- [PostgreSQL](./database/postgres.md) - Connection management, migrations, and utilities
- [MongoDB](./database/mongodb.md) - Connection management and document operations
- [Redis](./database/redis.md) - Caching and key-value operations

### Messaging
- [RabbitMQ](./messaging/rabbitmq.md) - Message queue operations and patterns

### Distributed Systems
- [Circuit Breaker](./distributed/circuit-breaker.md) - Fault tolerance pattern implementation
- [Retry Logic](./distributed/retry.md) - Configurable retry mechanisms with backoff
- [Rate Limiting](./distributed/rate-limiting.md) - Request rate control algorithms
- [Request ID Propagation](./distributed/request-id.md) - Distributed tracing support
- [Saga Pattern](./distributed/saga.md) - Distributed transaction management
- [Event Sourcing](./event-sourcing.md) - Event-driven architecture support

### Caching
- [Cache Abstractions](./caching.md) - Flexible caching with multiple backends

### Validation
- [Input Validation](./validation.md) - Struct and field validation utilities

### Observability
- [OpenTelemetry](./observability/opentelemetry.md) - Distributed tracing, metrics, and logs
- [Health Checks](./observability/health.md) - Service health monitoring
- [Metrics](./observability/metrics.md) - Application metrics collection

### Utilities
- [String Utilities](./utilities/strings.md) - String manipulation functions
- [Pointer Utilities](./utilities/pointers.md) - Safe pointer operations
- [Time Utilities](./utilities/time.md) - Date and time helpers
- [Transaction Processing](./utilities/transactions.md) - Financial transaction utilities

## Quick Start

```go
import "github.com/lerianstudio/lib-commons/commons"

// Initialize logger
logger := commons.NewLoggerFromContext(ctx)

// Handle errors
err := someOperation()
if err != nil {
    return commons.ValidateBusinessError(err, "EntityType")
}
```

## Architecture Principles

1. **Modularity**: Each component is independent and can be used standalone
2. **Testability**: All components are designed with testing in mind
3. **Performance**: Optimized for high-throughput microservices
4. **Observability**: Built-in support for logging, tracing, and metrics
5. **Resilience**: Fault-tolerant patterns for distributed systems

## Migration Guide

If you're upgrading from a previous version, see our [Migration Guide](./migration-guide.md).