# Commons-Go Library

A comprehensive Go library providing common utilities and components for building robust microservices and applications in the Lerian Studio ecosystem.

## 📋 Table of Contents

### 🚀 Quick Start
- [Installation](#installation)
- [Basic Usage](#basic-usage)
- [Full Documentation](#full-documentation)

### 🏗️ Core Components
- [Application Management](#application-management)
- [Context Utilities](#context-utilities) 
- [Error Handling](#error-handling)
- [String Utilities](#string-utilities)
- [Core Utilities](#core-utilities)
- [OS Utilities](#os-utilities)
- [Time Utilities](#time-utilities)
- [Pointer Utilities](#pointer-utilities)

### 🗄️ Data & Storage
- [PostgreSQL](#postgresql)
- [MongoDB](#mongodb)
- [Redis](#redis)
- [Cache System](#cache-system)

### 📡 Messaging & Communication
- [RabbitMQ](#rabbitmq)
- [HTTP Client & Middleware](#http-client--middleware)

### 🔧 Distributed Systems
- [Circuit Breaker](#circuit-breaker)
- [Retry Logic](#retry-logic)
- [Rate Limiting](#rate-limiting)
- [Request ID Propagation](#request-id-propagation)
- [Saga Pattern](#saga-pattern)
- [Event Sourcing](#event-sourcing)

### 📊 Observability
- [Logging](#logging)
- [OpenTelemetry](#opentelemetry)
- [Zap Integration](#zap-integration)
- [Observability Provider](#observability-provider)

### ✅ Validation & Processing
- [Validation Framework](#validation-framework)
- [Transaction Processing](#transaction-processing)

---

## Installation

```bash
go get github.com/LerianStudio/lib-commons
```

**Prerequisites:** Go 1.23.2 or higher

## Basic Usage

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

## Full Documentation

📖 **For comprehensive guides, examples, and architectural patterns:** [./docs/README.md](./docs/README.md)

---

# API Reference

## Application Management

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewLauncher` | `NewLauncher(opts ...LauncherOption) *Launcher` | Create application launcher with options. Use for managing multiple services in one binary. |
| `WithLogger` | `WithLogger(logger log.Logger) LauncherOption` | Add logger to launcher. Use when you need centralized logging for all apps. |
| `RunApp` | `RunApp(name string, app App) LauncherOption` | Register app to run. Use to add services to the launcher. |
| `Launcher.Add` | `Add(appName string, a App) *Launcher` | Register application manually. Use when building launcher programmatically. |
| `Launcher.Run` | `Run()` | Start all registered apps in goroutines. Use to launch entire system. |

**When to use:** Multi-service applications, microservice orchestration, service discovery patterns.

---

## Context Utilities

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `ContextWithLogger` | `ContextWithLogger(ctx context.Context, logger log.Logger) context.Context` | Add logger to context. Use for request-scoped logging. |
| `NewLoggerFromContext` | `NewLoggerFromContext(ctx context.Context) log.Logger` | Extract logger from context. Use in handlers/services to get request logger. |
| `ContextWithTracer` | `ContextWithTracer(ctx context.Context, tracer trace.Tracer) context.Context` | Add tracer to context. Use for distributed tracing setup. |
| `NewTracerFromContext` | `NewTracerFromContext(ctx context.Context) trace.Tracer` | Extract tracer from context. Use to create spans in services. |
| `ContextWithHeaderID` | `ContextWithHeaderID(ctx context.Context, headerID string) context.Context` | Add header ID to context. Use for request correlation. |
| `NewHeaderIDFromContext` | `NewHeaderIDFromContext(ctx context.Context) string` | Extract header ID from context. Use for request tracking across services. |

**When to use:** Request tracing, distributed logging, correlation IDs, microservice communication.

---

## Error Handling

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `ValidateBusinessError` | `ValidateBusinessError(err error, entityType string, ...any) error` | Map domain errors to business responses. Use for consistent API error responses. |
| `Response.Error` | `Error() string` | Get error message from Response. Use when implementing error interface. |

**When to use:** API error handling, business rule validation, consistent error responses across microservices.

---

## String Utilities

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `IsNilOrEmpty` | `IsNilOrEmpty(s *string) bool` | Check if string pointer is nil or empty. Use for safe string validation. |
| `TruncateString` | `TruncateString(s string, maxLen int) string` | Truncate string to max length. Use for display/logging limits. |
| `MaskEmail` | `MaskEmail(email string) string` | Mask email for privacy. Use in logs/audit trails. |
| `MaskLastDigits` | `MaskLastDigits(value string, digitsToShow int) string` | Mask all but last digits. Use for credit cards/sensitive data. |
| `StringToObject` | `StringToObject(s string, obj interface{}) error` | Convert JSON string to object. Use for config/API parsing. |
| `ObjectToString` | `ObjectToString(obj interface{}) (string, error)` | Convert object to JSON string. Use for serialization. |
| `RemoveAccents` | `RemoveAccents(word string) (string, error)` | Remove accents from text. Use for search normalization. |
| `RemoveSpaces` | `RemoveSpaces(word string) string` | Remove all spaces. Use for identifier cleanup. |
| `CamelToSnakeCase` | `CamelToSnakeCase(str string) string` | Convert camelCase to snake_case. Use for database field mapping. |
| `RegexIgnoreAccents` | `RegexIgnoreAccents(regex string) string` | Create accent-insensitive regex. Use for flexible search. |
| `RemoveChars` | `RemoveChars(str string, chars map[string]bool) string` | Remove specific characters. Use for input sanitization. |
| `ReplaceUUIDWithPlaceholder` | `ReplaceUUIDWithPlaceholder(path string) string` | Replace UUIDs in paths with placeholders. Use for URL pattern matching. |
| `ValidateServerAddress` | `ValidateServerAddress(value string) string` | Validate and normalize server address. Use for connection strings. |
| `HashSHA256` | `HashSHA256(input string) string` | Create SHA256 hash. Use for data integrity/passwords. |
| `StringToInt` | `StringToInt(s string) int` | Convert string to int safely. Use for type conversion with defaults. |

**When to use:** Data processing, input validation, search functionality, security, API data transformation.

---

## Core Utilities

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `Contains` | `Contains[T comparable](slice []T, item T) bool` | Check if slice contains item. Use for membership testing. |
| `CheckMetadataKeyAndValueLength` | `CheckMetadataKeyAndValueLength(limit int, metadata map[string]any) error` | Validate metadata size limits. Use for API input validation. |
| `ValidateCountryAddress` | `ValidateCountryAddress(country string) error` | Validate country code. Use for address validation. |
| `ValidateAccountType` | `ValidateAccountType(t string) error` | Validate account type enum. Use for business rule validation. |
| `ValidateType` | `ValidateType(t string) error` | Validate generic type field. Use for entity validation. |
| `ValidateCode` | `ValidateCode(code string) error` | Validate code format. Use for identifier validation. |
| `ValidateCurrency` | `ValidateCurrency(code string) error` | Validate currency code. Use for financial data validation. |
| `SafeIntToUint64` | `SafeIntToUint64(val int) uint64` | Safely convert int to uint64. Use to prevent overflow. |
| `SafeInt64ToInt` | `SafeInt64ToInt(val int64) int` | Safely convert int64 to int. Use to prevent overflow. |
| `SafeUintToInt` | `SafeUintToInt(val uint) int` | Safely convert uint to int. Use to prevent overflow. |
| `IsUUID` | `IsUUID(s string) bool` | Check if string is valid UUID. Use for ID validation. |
| `GenerateUUIDv7` | `GenerateUUIDv7() uuid.UUID` | Generate UUID v7 (time-ordered). Use for sortable IDs. |
| `StructToJSONString` | `StructToJSONString(s any) (string, error)` | Convert struct to JSON string. Use for logging/debugging. |
| `MergeMaps` | `MergeMaps(source, target map[string]any) map[string]any` | Merge two maps. Use for configuration merging. |
| `GetCPUUsage` | `GetCPUUsage(ctx context.Context, cpuGauge metric.Int64Gauge)` | Record CPU usage metrics. Use for system monitoring. |
| `GetMemUsage` | `GetMemUsage(ctx context.Context, memGauge metric.Int64Gauge)` | Record memory usage metrics. Use for system monitoring. |
| `GetMapNumKinds` | `GetMapNumKinds() map[reflect.Kind]bool` | Get numeric types map. Use for type checking. |
| `Reverse` | `Reverse[T any](s []T) []T` | Reverse slice order. Use for data manipulation. |
| `InternalKey` | `InternalKey(organizationID, ledgerID uuid.UUID, key string) string` | Generate internal cache key. Use for multi-tenant caching. |
| `LockInternalKey` | `LockInternalKey(organizationID, ledgerID uuid.UUID, key string) string` | Generate lock key. Use for distributed locking. |

**When to use:** Business logic validation, data transformation, system monitoring, multi-tenant applications.

---

## OS Utilities

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `GetEnv` | `GetEnv(key, fallback string) string` | Get env var with fallback. Use for optional configuration. |
| `MustGetEnv` | `MustGetEnv(key string) string` | Get required env var or panic. Use for critical configuration. |
| `LoadEnvFile` | `LoadEnvFile(file string) error` | Load env vars from file. Use for development setup. |
| `GetenvBoolOrDefault` | `GetenvBoolOrDefault(key string, defaultValue bool) bool` | Get bool env var with default. Use for feature flags. |
| `GetenvIntOrDefault` | `GetenvIntOrDefault(key string, defaultValue int64) int64` | Get int env var with default. Use for numeric configuration. |
| `SetConfigFromEnvVars` | `SetConfigFromEnvVars(s any) error` | Set struct fields from env vars. Use for config binding. |
| `EnsureConfigFromEnvVars` | `EnsureConfigFromEnvVars(s any) any` | Ensure config is set from env. Use for initialization. |
| `InitLocalEnvConfig` | `InitLocalEnvConfig() *LocalEnvConfig` | Initialize local env config. Use for development setup. |

**When to use:** Configuration management, environment setup, feature toggles, development workflows.

---

## Time Utilities

**Package:** `commons`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `FormatTime` | `FormatTime(t time.Time, layout string) string` | Format time with layout. Use for consistent time display. |
| `ParseTime` | `ParseTime(s, layout string) (time.Time, error)` | Parse time from string. Use for API input parsing. |
| `GetCurrentTime` | `GetCurrentTime() time.Time` | Get current UTC time. Use for consistent timestamps. |
| `TimeBetween` | `TimeBetween(t, start, end time.Time) bool` | Check if time is in range. Use for time validation. |
| `IsValidDate` | `IsValidDate(date string) bool` | Check if date string is valid. Use for input validation. |
| `IsInitialDateBeforeFinalDate` | `IsInitialDateBeforeFinalDate(initial, final time.Time) bool` | Check date order. Use for range validation. |
| `IsDateRangeWithinMonthLimit` | `IsDateRangeWithinMonthLimit(initial, final time.Time, limit int) bool` | Check if range within month limit. Use for business rules. |
| `NormalizeDate` | `NormalizeDate(date time.Time, days *int) string` | Normalize date with optional offset. Use for date calculations. |

**When to use:** API input validation, business rule enforcement, reporting, audit trails.

---

## Pointer Utilities

**Package:** `commons/pointers`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `String` | `String(s string) *string` | Create string pointer. Use for optional fields in structs. |
| `Bool` | `Bool(b bool) *bool` | Create bool pointer. Use for optional boolean fields. |
| `Time` | `Time(t time.Time) *time.Time` | Create time pointer. Use for optional time fields. |
| `Int64` | `Int64(t int64) *int64` | Create int64 pointer. Use for optional numeric fields. |
| `Float64` | `Float64(t float64) *float64` | Create float64 pointer. Use for optional decimal fields. |
| `Int` | `Int(t int) *int` | Create int pointer. Use for optional integer fields. |

**When to use:** Working with optional fields, JSON marshaling, database models, API responses.

---

## PostgreSQL

**Package:** `commons/postgres`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `PostgresConnection.Connect` | `Connect(ctx context.Context) error` | Establish PostgreSQL connections. Use for database initialization. |
| `PostgresConnection.GetDB` | `GetDB() *sql.DB` | Get database connection. Use for query execution. |
| `PostgresConnection.MigrateUp` | `MigrateUp(sourceDir string) error` | Run database migrations up. Use for schema updates. |
| `PostgresConnection.MigrateDown` | `MigrateDown(sourceDir string) error` | Run database migrations down. Use for rollbacks. |
| `GetPagination` | `GetPagination(page, pageSize int) (int, int)` | Get pagination offset/limit. Use for SQL pagination. |

**When to use:** Database connectivity, migrations, pagination, multi-replica setups.

---

## MongoDB

**Package:** `commons/mongo`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `MongoConnection.Connect` | `Connect(ctx context.Context) error` | Establish MongoDB connection. Use for database initialization. |
| `MongoConnection.GetClient` | `GetClient(ctx context.Context) *mongo.Client` | Get MongoDB client. Use for database operations. |
| `MongoConnection.GetDatabase` | `GetDatabase(ctx context.Context) *mongo.Database` | Get MongoDB database. Use for collection access. |
| `MongoConnection.GetCollection` | `GetCollection(ctx context.Context, name string) *mongo.Collection` | Get MongoDB collection. Use for document operations. |

**When to use:** NoSQL document storage, flexible schema applications, event storage.

---

## Redis

**Package:** `commons/redis`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `RedisConnection.Connect` | `Connect(ctx context.Context) error` | Establish Redis connection. Use for cache initialization. |
| `RedisConnection.GetClient` | `GetClient(ctx context.Context) *redis.Client` | Get Redis client. Use for cache operations. |
| `RedisConnection.Set` | `Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error` | Set key-value with TTL. Use for caching data. |
| `RedisConnection.Get` | `Get(ctx context.Context, key string) (string, error)` | Get value by key. Use for cache retrieval. |
| `RedisConnection.Del` | `Del(ctx context.Context, keys ...string) error` | Delete keys. Use for cache invalidation. |

**When to use:** Caching, session storage, rate limiting, real-time features.

---

## Cache System

**Package:** `commons/cache`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewMemoryCache` | `NewMemoryCache(opts ...MemoryCacheOption) *MemoryCache` | Create in-memory cache. Use for local caching without external dependencies. |
| `NewLoadingCache` | `NewLoadingCache(cache Cache, loadFunc LoadFunc) *LoadingCache` | Create cache with auto-loading. Use for expensive computations with fallback. |
| `NewNamespaceCache` | `NewNamespaceCache(cache Cache, namespace string) *NamespaceCache` | Create namespaced cache. Use for multi-tenant caching. |
| `Cache.Get` | `Get(ctx context.Context, key string) (interface{}, error)` | Get cached value. Use for cache retrieval. |
| `Cache.Set` | `Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error` | Set cached value with TTL. Use for caching data. |
| `Cache.Delete` | `Delete(ctx context.Context, key string) error` | Delete cached value. Use for cache invalidation. |
| `Cache.Exists` | `Exists(ctx context.Context, key string) (bool, error)` | Check if key exists. Use for cache hit detection. |
| `Cache.Clear` | `Clear(ctx context.Context) error` | Clear all cached values. Use for cache reset. |

**Cache Options:**
- `WithMaxSize(size int)` - Set maximum cache size
- `WithEvictionPolicy(policy EvictionPolicy)` - Set eviction strategy (LRU, LFU, FIFO)
- `WithMetrics(enabled bool)` - Enable cache metrics
- `WithCleanupInterval(interval time.Duration)` - Set cleanup frequency

**When to use:** Performance optimization, reducing database load, expensive computation caching.

---

## RabbitMQ

**Package:** `commons/rabbitmq`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `RabbitMQConnection.Connect` | `Connect() error` | Establish RabbitMQ connection. Use for message broker setup. |
| `RabbitMQConnection.GetChannel` | `GetChannel() (*amqp.Channel, error)` | Get RabbitMQ channel. Use for message operations. |
| `RabbitMQConnection.DeclareQueue` | `DeclareQueue(name string) (amqp.Queue, error)` | Declare message queue. Use for queue setup. |
| `RabbitMQConnection.DeclareExchange` | `DeclareExchange(name, kind string) error` | Declare exchange. Use for routing setup. |
| `RabbitMQConnection.QueueBind` | `QueueBind(queue, exchange, routingKey string) error` | Bind queue to exchange. Use for message routing. |
| `RabbitMQConnection.Publish` | `Publish(exchange, routingKey string, body []byte) error` | Publish message. Use for async communication. |
| `RabbitMQConnection.Consume` | `Consume(queue string, consumer string) (<-chan amqp.Delivery, error)` | Consume messages. Use for message processing. |

**When to use:** Asynchronous messaging, event-driven architecture, microservice communication, background jobs.

---

## Circuit Breaker

**Package:** `commons/circuitbreaker`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `New` | `New(name string, opts ...Option) *CircuitBreaker` | Create circuit breaker. Use for fault tolerance around external services. |
| `Execute` | `Execute(fn func() error) error` | Execute function with circuit breaker. Use for protected operations. |
| `ExecuteWithContext` | `ExecuteWithContext(ctx context.Context, fn func() error) error` | Execute with context and circuit breaker. Use for cancellable protected operations. |
| `State` | `State() State` | Get current state (Closed/Open/HalfOpen). Use for monitoring. |
| `Reset` | `Reset()` | Reset circuit breaker. Use for manual recovery. |
| `Metrics` | `Metrics() Metrics` | Get failure metrics. Use for observability. |

**Circuit Breaker Options:**
- `WithThreshold(count int)` - Failure threshold before opening
- `WithSuccessThreshold(count int)` - Success threshold for closing
- `WithTimeout(duration time.Duration)` - Open state timeout
- `WithFailureCondition(fn func(error) bool)` - Custom failure detection
- `WithOnStateChange(fn func(State))` - State change callback

**When to use:** External API calls, database connections, preventing cascade failures, system resilience.

---

## Retry Logic

**Package:** `commons/retry`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `Do` | `Do(ctx context.Context, operation Operation, opts ...Option) error` | Retry operation with backoff. Use for unreliable operations. |
| `DoWithResult` | `DoWithResult[T any](ctx context.Context, operation OperationWithResult[T], opts ...Option) (T, error)` | Retry operation returning result. Use for operations with return values. |
| `MarkPermanent` | `MarkPermanent(err error) error` | Mark error as permanent (non-retryable). Use to stop retries immediately. |
| `IsPermanent` | `IsPermanent(err error) bool` | Check if error is permanent. Use in retry conditions. |

**Retry Options:**
- `WithMaxRetries(count int)` - Maximum retry attempts
- `WithDelay(duration time.Duration)` - Fixed delay between retries
- `WithExponentialBackoff(base time.Duration, multiplier float64)` - Exponential backoff
- `WithJitter(enabled bool)` - Add randomization to backoff
- `WithRetryIf(fn func(error) bool)` - Custom retry condition
- `WithOnRetry(fn func(int, error))` - Retry callback

**When to use:** Network operations, external API calls, transient failure recovery, improving reliability.

---

## Rate Limiting

**Package:** `commons/ratelimit`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewTokenBucket` | `NewTokenBucket(capacity, refillRate float64) *TokenBucket` | Create token bucket limiter. Use for burst-friendly rate limiting. |
| `NewSlidingWindow` | `NewSlidingWindow(limit int, window time.Duration) *SlidingWindow` | Create sliding window limiter. Use for smooth rate limiting. |
| `NewFixedWindow` | `NewFixedWindow(limit int, window time.Duration) *FixedWindow` | Create fixed window limiter. Use for simple rate limiting. |
| `NewKeyedLimiter` | `NewKeyedLimiter(factory func(string) Limiter, opts ...KeyedOption) *KeyedLimiter` | Create per-key limiter. Use for user-specific rate limiting. |
| `Limiter.Allow` | `Allow() bool` | Check if operation is allowed. Use for immediate rate limit checks. |
| `Limiter.Wait` | `Wait(ctx context.Context) error` | Wait until operation is allowed. Use for blocking rate limiting. |
| `Limiter.Reserve` | `Reserve(n int) Reservation` | Reserve n tokens. Use for advanced rate limiting patterns. |

**When to use:** API rate limiting, preventing abuse, resource protection, traffic shaping.

---

## Request ID Propagation

**Package:** `commons/requestid`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewRequestID` | `NewRequestID() string` | Generate new request ID. Use for tracing request flows. |
| `FromContext` | `FromContext(ctx context.Context) string` | Extract request ID from context. Use in services for logging. |
| `WithRequestID` | `WithRequestID(ctx context.Context, id string) context.Context` | Add request ID to context. Use at request entry points. |

**When to use:** Distributed tracing, request correlation, debugging microservices, audit logging.

---

## Saga Pattern

**Package:** `commons/saga`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewSaga` | `NewSaga(name string) *Saga` | Create new saga. Use for distributed transaction orchestration. |
| `NewCoordinator` | `NewCoordinator() *Coordinator` | Create saga coordinator. Use for managing multiple sagas. |
| `NewDistributedSaga` | `NewDistributedSaga(name string, eventStore EventStore) *DistributedSaga` | Create distributed saga with event store. Use for persistent saga state. |
| `SagaBuilder` | `SagaBuilder` | Fluent API for building sagas. Use for complex workflow definition. |

**When to use:** Distributed transactions, workflow orchestration, microservice coordination, compensating actions.

---

## Event Sourcing

**Package:** `commons/eventsourcing`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewRepository` | `NewRepository(store EventStore, factory func() Aggregate, opts ...RepositoryOption) *Repository` | Create event sourced repository. Use for aggregate persistence. |
| `Repository.Get` | `Get(ctx context.Context, id string) (Aggregate, error)` | Load aggregate from events. Use for aggregate retrieval. |
| `Repository.Save` | `Save(ctx context.Context, aggregate Aggregate, events []Event) error` | Save aggregate events. Use for persisting changes. |
| `EventBus.Subscribe` | `Subscribe(eventType string, handler EventHandler)` | Subscribe to events. Use for event processing. |
| `EventBus.Publish` | `Publish(ctx context.Context, events ...Event) error` | Publish events. Use for event distribution. |

**When to use:** Audit trails, complex domain models, temporal queries, event-driven architecture.

---

## Logging

**Package:** `commons/log`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `Info` | `Info(args ...interface{})` | Log info message. Use for general information. |
| `Infof` | `Infof(format string, args ...interface{})` | Log formatted info message. Use for structured info logging. |
| `Error` | `Error(args ...interface{})` | Log error message. Use for error reporting. |
| `Errorf` | `Errorf(format string, args ...interface{})` | Log formatted error message. Use for structured error logging. |
| `Warn` | `Warn(args ...interface{})` | Log warning message. Use for non-critical issues. |
| `Warnf` | `Warnf(format string, args ...interface{})` | Log formatted warning message. Use for structured warnings. |
| `Debug` | `Debug(args ...interface{})` | Log debug message. Use for development debugging. |
| `Debugf` | `Debugf(format string, args ...interface{})` | Log formatted debug message. Use for structured debugging. |
| `Fatal` | `Fatal(args ...interface{})` | Log fatal message and exit. Use for unrecoverable errors. |
| `Fatalf` | `Fatalf(format string, args ...interface{})` | Log formatted fatal message and exit. Use for critical failures. |
| `WithFields` | `WithFields(fields ...interface{}) Logger` | Add fields to logger. Use for contextual logging. |
| `Sync` | `Sync() error` | Flush buffered logs. Use before application exit. |

**When to use:** Application monitoring, debugging, audit trails, structured logging.

---

## OpenTelemetry

**Package:** `commons/opentelemetry`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `InitializeTelemetry` | `InitializeTelemetry(logger log.Logger) error` | Initialize OpenTelemetry providers. Use for observability setup. |
| `ShutdownTelemetry` | `ShutdownTelemetry() error` | Shutdown OpenTelemetry providers. Use for graceful shutdown. |
| `GetTracer` | `GetTracer() trace.Tracer` | Get tracer instance. Use for creating spans. |
| `GetMeter` | `GetMeter() metric.Meter` | Get meter instance. Use for recording metrics. |
| `GetLogger` | `GetLogger() log.Logger` | Get logger instance. Use for structured logging. |
| `StartSpan` | `StartSpan(ctx context.Context, name string) (context.Context, trace.Span)` | Start trace span. Use for operation tracing. |
| `EndSpan` | `EndSpan(span trace.Span, err error)` | End trace span with optional error. Use to complete tracing. |

**When to use:** Distributed tracing, performance monitoring, observability, microservice debugging.

---

## Zap Integration

**Package:** `commons/zap`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `NewZapLogger` | `NewZapLogger(config zap.Config) (*ZapLoggerAdapter, error)` | Create Zap logger adapter. Use for high-performance logging. |
| `ZapLoggerAdapter.Info` | `Info(args ...interface{})` | Log info with Zap. Use for fast structured logging. |
| `ZapLoggerAdapter.Error` | `Error(args ...interface{})` | Log error with Zap. Use for fast error logging. |
| `ZapLoggerAdapter.WithFields` | `WithFields(fields ...interface{}) log.Logger` | Add fields to Zap logger. Use for contextual logging. |

**When to use:** High-performance logging, production applications, structured JSON logs.

---

## Observability Provider

**Package:** `commons/observability`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `New` | `New(ctx context.Context, opts ...Option) (Provider, error)` | Create observability provider. Use for comprehensive observability setup. |
| `Provider.Tracer` | `Tracer() trace.Tracer` | Get tracer from provider. Use for distributed tracing. |
| `Provider.Meter` | `Meter() metric.Meter` | Get meter from provider. Use for metrics collection. |
| `Provider.Logger` | `Logger() log.Logger` | Get logger from provider. Use for structured logging. |
| `Provider.Shutdown` | `Shutdown(ctx context.Context) error` | Shutdown provider gracefully. Use for cleanup. |
| `WithSpan` | `WithSpan(ctx context.Context, name string, fn func(context.Context) error) error` | Execute function with span. Use for automatic span management. |
| `RecordMetric` | `RecordMetric(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue)` | Record metric value. Use for business metrics. |

**Provider Options:**
- `WithServiceName(name string)` - Set service name
- `WithEnvironment(env string)` - Set environment
- `WithTraceSampleRate(rate float64)` - Set trace sampling rate
- `WithMetricsEnabled(enabled bool)` - Enable/disable metrics
- `WithLoggingEnabled(enabled bool)` - Enable/disable logging

**When to use:** Complete observability setup, microservice monitoring, performance tracking.

---

## Validation Framework

**Package:** `commons/validation`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `Required` | `Required(value interface{}, fieldName string) error` | Validate required field. Use for mandatory field validation. |
| `MinLength` | `MinLength(value string, min int, fieldName string) error` | Validate minimum length. Use for input length validation. |
| `MaxLength` | `MaxLength(value string, max int, fieldName string) error` | Validate maximum length. Use for input length limits. |
| `Email` | `Email(value string, fieldName string) error` | Validate email format. Use for email field validation. |
| `URL` | `URL(value string, fieldName string) error` | Validate URL format. Use for URL field validation. |
| `UUID` | `UUID(value string, fieldName string) error` | Validate UUID format. Use for ID field validation. |
| `InRange` | `InRange(value, min, max int, fieldName string) error` | Validate numeric range. Use for range validation. |
| `Matches` | `Matches(value, pattern string, fieldName string) error` | Validate regex pattern. Use for format validation. |
| `OneOf` | `OneOf(value string, options []string, fieldName string) error` | Validate enum values. Use for choice validation. |
| `ValidateStruct` | `ValidateStruct(s interface{}) []ValidationError` | Validate entire struct. Use for comprehensive validation. |
| `RegisterCustomValidator` | `RegisterCustomValidator(name string, fn func(interface{}) error) error` | Register custom validator. Use for business rule validation. |

**When to use:** API input validation, data integrity, business rule enforcement, form validation.

---

## Transaction Processing

**Package:** `commons/transaction`

| Function | Signature | What & When to Use |
|----------|-----------|-------------------|
| `ValidateTransactionRequest` | `ValidateTransactionRequest(req *TransactionRequest) error` | Validate transaction request. Use for financial transaction validation. |
| `ValidateAccountBalances` | `ValidateAccountBalances(accounts []*Account) error` | Validate account balances. Use for sufficient funds validation. |
| `ValidateAssetCode` | `ValidateAssetCode(code string) error` | Validate asset code. Use for currency/asset validation. |
| `ValidateAccountStatuses` | `ValidateAccountStatuses(accounts []*Account) error` | Validate account statuses. Use for account eligibility validation. |

**Domain Models:**
- `Transaction` - Complete transaction structure
- `Balance` - Account balance information
- `Amount` - Monetary amount with currency
- `Source` - Transaction source information
- `Send` - Send operation details
- `Distribute` - Distribution operation details

**When to use:** Financial applications, accounting systems, transaction processing, ledger systems.

---

---

## Contributing

Please read the contributing guidelines before submitting pull requests.

## License

This project is licensed under the terms found in the LICENSE file in the root directory.