# Project Rules - lib-commons

This document defines the coding standards, architecture patterns, and development guidelines for `lib-commons` in the v5 split-library minor line.

## Table of Contents

| # | Section | Description |
|---|---------|-------------|
| 1 | [Architecture Patterns](#architecture-patterns) | Package structure and organization |
| 2 | [Code Conventions](#code-conventions) | Go coding standards |
| 3 | [Error Handling](#error-handling) | Error handling patterns |
| 4 | [Testing Requirements](#testing-requirements) | Test coverage and patterns |
| 5 | [Documentation Standards](#documentation-standards) | Code documentation requirements |
| 6 | [Dependencies](#dependencies) | Dependency management rules |
| 7 | [Security](#security) | Security requirements |
| 8 | [DevOps](#devops) | CI/CD and tooling |

---

## Architecture Patterns

### Package Structure

```text
lib-commons/
├── commons/                      # All library packages
│   ├── backoff/                    # Exponential backoff with jitter
│   ├── certificate/                # Thread-safe TLS certificate manager with hot-reload
│   ├── circuitbreaker/             # Circuit breaker manager and health checker
│   ├── constants/                  # Shared constants (headers, errors, pagination)
│   ├── cron/                       # Cron expression parsing and scheduling
│   ├── crypto/                     # Hashing and symmetric encryption
│   ├── dlq/                        # Redis-backed dead letter queue with consumer and retry
│   ├── errgroup/                   # Goroutine coordination with panic recovery
│   ├── internal/                   # Internal packages (not part of public API)
│   │   └── nilcheck/               # Nil interface detection helpers
│   ├── jwt/                        # HMAC-based JWT signing and verification
│   ├── license/                    # License validation and enforcement
│   ├── mongo/                      # MongoDB connector
│   ├── net/http/                   # Fiber-oriented HTTP helpers and middleware
│   │   ├── idempotency/            # Fiber idempotency middleware (Redis-backed, tenant-scoped)
│   │   └── ratelimit/              # Redis-backed rate limit storage
│   ├── outbox/                     # Transactional outbox primitives
│   │   ├── mongo/                  # MongoDB outbox adapter with row-scoped tenants and tenant DB resolver
│   │   ├── outboxtest/             # Backend-agnostic outbox repository contract tests
│   │   └── postgres/               # PostgreSQL outbox adapter with schema/column tenant strategies
│   ├── pointers/                   # Pointer conversion helpers
│   ├── postgres/                   # PostgreSQL connector with migrations
│   ├── rabbitmq/                   # RabbitMQ connector
│   ├── redis/                      # Redis connector (standalone/sentinel/cluster)
│   ├── safe/                       # Panic-free math/regex/slice operations
│   ├── secretsmanager/             # AWS Secrets Manager M2M credential retrieval
│   ├── security/                   # Sensitive field detection and handling
│   ├── server/                     # Graceful shutdown and lifecycle (ServerManager)
│   ├── shell/                      # Makefile includes and shell utilities
│   ├── tenant-manager/             # Multi-tenant database-per-tenant isolation
│   │   ├── cache/                  # In-memory tenant cache with LRU eviction
│   │   ├── client/                 # HTTP client for tenant-manager API
│   │   ├── consumer/               # Multi-tenant consumer with lazy loading and retry
│   │   ├── core/                   # Core types, context, errors, validation
│   │   ├── event/                  # Event listener, dispatcher, payloads (Redis Pub/Sub)
│   │   ├── log/                    # Tenant-scoped logger
│   │   ├── middleware/             # Fiber middleware (TenantMiddleware with WithPG/WithMB)
│   │   ├── mongo/                  # MongoDB tenant manager
│   │   ├── postgres/               # PostgreSQL tenant manager
│   │   ├── rabbitmq/               # RabbitMQ tenant manager
│   │   ├── redis/                  # Redis tenant client
│   │   ├── s3/                     # S3 object storage for tenant provisioning scripts
│   │   ├── tenantcache/            # Tenant cache and loader
│   │   └── valkey/                 # Valkey/Redis key patterns
│   ├── transaction/                # Typed transaction validation/posting primitives
│   ├── webhook/                    # Webhook delivery with HMAC-SHA256 signing and SSRF protection
│   ├── app.go                      # Application bootstrap helpers
│   ├── context.go                  # Context utilities
│   ├── environment.go              # Environment detection and security tier mapping
│   ├── errors.go                   # Error definitions
│   ├── os.go                       # OS utilities and env var helpers
│   ├── security_override.go        # ALLOW_* security policy override mechanism
│   ├── stringUtils.go              # String utilities
│   ├── time.go                     # Time utilities
│   └── utils.go                    # General utility functions
├── docs/                           # Documentation
├── reports/                        # Test and coverage reports
└── go.mod                          # Module definition (v5)
```

### Lerian Library Boundaries

`lib-commons` is no longer the owner for every shared capability. Treat these boundaries as architectural contracts:

| Library | Ownership |
|---------|-----------|
| `github.com/LerianStudio/lib-commons` | Core helpers, connectors, HTTP/server utilities, security, resilience, tenant-manager primitives, outbox, DLQ, certificate, JWT, transaction helpers |
| `github.com/LerianStudio/lib-observability` | Logging, zap adapter, tracing, metrics, redaction, panic instrumentation, assertions, observability constants |
| `github.com/LerianStudio/lib-systemplane` | Runtime configuration, hot reload, systemplane admin routes, tenant-scoped runtime knobs, systemplane contract tests |
| `github.com/LerianStudio/lib-streaming` | CloudEvents/Kafka streaming, event emitters, streaming DLQs, outbox replay for streaming events |

The former lib-commons packages that mirrored extracted observability APIs were removed. Code must import `github.com/LerianStudio/lib-observability` packages directly for logging, zap adapters, tracing, metrics, redaction, panic instrumentation, assertions, and observability constants.

### Package Design Principles

1. **Single Responsibility**: Each package should have one clear purpose
2. **Minimal Dependencies**: Packages should minimize external dependencies
3. **Interface-Driven**: Define interfaces for testability and flexibility
4. **Zero Business Logic**: This is a utility library - no domain/business logic
5. **Nil-Safe and Concurrency-Safe**: Keep behavior safe by default
6. **Explicit Error Returns**: Prefer error returns over panic paths

### Naming Conventions

| Type | Convention | Example |
|------|------------|---------|
| Package | lowercase, single word preferred | `postgres`, `redis`, `circuitbreaker` |
| Files | snake_case or camelCase matching content | `pool_manager_pg.go`, `stringUtils.go` |
| Public Functions | PascalCase, descriptive | `NewClient`, `ServeReverseProxy` |
| Private Functions | camelCase | `validateConfig` |
| Interfaces | -er suffix or descriptive | `Logger`, `Manager`, `LockManager` |
| Constants | PascalCase | `DefaultTimeout`, `LevelInfo` |

---

## Code Conventions

### Go Version

- **Minimum**: Go 1.26.3
- Keep `go.mod` updated with latest stable Go version
- Module path: `github.com/LerianStudio/lib-commons/v5`

### Build Tags

- Unit test files **MUST** have `//go:build unit` as the first line
- Integration test files **MUST** have `//go:build integration` as the first line

```go
//go:build unit

package mypackage

import "testing"

func TestMyFunc(t *testing.T) { ... }
```

### Imports Organization

```go
import (
    // Standard library
    "context"
    "fmt"
    "time"

    // Third-party packages
    "github.com/jackc/pgx/v5"
    "go.uber.org/zap"

    // Internal packages
    "github.com/LerianStudio/lib-observability/log"
    "github.com/LerianStudio/lib-commons/v5/commons/postgres"
)
```

### Function Design

1. **Context First**: Functions that may block should accept `context.Context` as first parameter
2. **Options Pattern**: Use functional options for configurable constructors
3. **Error Last**: Return errors as the last return value
4. **Named Returns**: Avoid named returns except for documentation

```go
// Good
func NewClient(ctx context.Context, opts ...Option) (*Client, error)

// Avoid
func NewClient(opts ...Option) (client *Client, err error)
```

### Struct Design

```go
type Config struct {
    Host     string        `json:"host"`
    Port     int           `json:"port"`
    Timeout  time.Duration `json:"timeout"`
    MaxConns int           `json:"max_conns"`
}

func (c *Config) Validate() error {
    if c.Host == "" {
        return ErrEmptyHost
    }
    return nil
}
```

### Constants and Variables

```go
const (
    DefaultTimeout  = 30 * time.Second
    DefaultMaxConns = 10
)

var (
    ErrNotFound     = errors.New("not found")
    ErrInvalidInput = errors.New("invalid input")
)
```

---

## Error Handling

### Error Definition

1. **Sentinel Errors**: Define package-level errors for expected conditions
2. **Error Wrapping**: Use `fmt.Errorf` with `%w` for context
3. **Custom Types**: Use custom error types when additional context is needed

```go
var (
    ErrConnectionFailed = errors.New("connection failed")
    ErrTenantNotFound   = errors.New("tenant not found")
)

// Wrapping
return fmt.Errorf("failed to connect to %s: %w", host, err)

// Custom type
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation failed for %s: %s", e.Field, e.Message)
}
```

### Error Handling Rules

1. **NEVER use panic()** - Always return errors
2. **NEVER ignore errors** - Handle or propagate all errors
3. **Log at boundaries** - Log errors at service boundaries, not in library code
4. **Provide context** - Wrap errors with meaningful context

```go
// Good
if err != nil {
    return fmt.Errorf("failed to execute query: %w", err)
}

// Bad - panics
if err != nil {
    panic(err)
}

// Bad - ignores error
result, _ := doSomething()
```

---

## Testing Requirements

### Coverage Requirements

- **Minimum Coverage**: 80% for new packages
- **Critical Paths**: 100% coverage for error handling paths
- **Run Coverage**: `make coverage-unit` or `make coverage-integration`
- **Coverage Exclusions**: Defined in `.ignorecoverunit` (e.g., `*_mock.go`)

### Build Tags

All test files **MUST** include the appropriate build tag as the first line:

| Type | Build Tag | Example |
|------|-----------|---------|
| Unit Tests | `//go:build unit` | All `_test.go` files |
| Integration Tests | `//go:build integration` | All `_integration_test.go` files |

### Test File Naming

| Type | Pattern | Example |
|------|---------|---------|
| Unit Tests | `{file}_test.go` | `config_test.go` |
| Integration | `{file}_integration_test.go` | `postgres_integration_test.go` |
| Examples | `{feature}_example_test.go` | `cursor_example_test.go` |
| Benchmarks | In `_test.go` or `benchmark_test.go` | `BenchmarkXxx` |

### Integration Test Conventions

- Test function names **MUST** start with `TestIntegration_` (e.g., `TestIntegration_MyFeature_Works`)
- Integration tests use `testcontainers-go` to spin up ephemeral containers
- Docker is required to run integration tests
- Integration tests run sequentially (`-p=1`) to avoid Docker container conflicts

### Test Patterns

```go
func TestConfig_Validate(t *testing.T) {
    tests := []struct {
        name    string
        config  Config
        wantErr bool
    }{
        {
            name:    "valid config",
            config:  Config{Host: "localhost", Port: 5432},
            wantErr: false,
        },
        {
            name:    "empty host",
            config:  Config{Host: "", Port: 5432},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.config.Validate()
            if (err != nil) != tt.wantErr {
                t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

### Test Data

- Use realistic but fake data (e.g., `"pass"`, `"secret"` for passwords in tests)
- Never use real credentials in tests
- Use test fixtures for complex data structures

### Mocking

- Use `go.uber.org/mock` for interface mocking
- Define interfaces at point of use for testability
- Prefer dependency injection over global state
- Mock files follow the `{type}_mock.go` pattern

---

## Documentation Standards

### Package Documentation

Every package MUST have a `doc.go` file or package comment:

```go
// Package postgres provides PostgreSQL connection management utilities.
//
// It supports connection pooling, migrations, and read-replica configurations
// for high-availability deployments.
package postgres
```

### Function Documentation

Public functions MUST have documentation:

```go
// Connect establishes a connection to the PostgreSQL database.
// It validates the configuration before attempting to connect.
//
// Returns an error if the configuration is invalid or connection fails.
func (c *Client) Connect(ctx context.Context) error {
```

### README Updates

- Update `README.md` API Reference when adding public APIs
- Include usage examples for new packages

### Migration Awareness

- If a task touches renamed, removed, redesigned, or extracted APIs in the v5 split-library minor line, update live package documentation and README guidance so consumers know the owning library or replacement API.
- If a task changes package-level behavior or API expectations, update `README.md`

---

## Dependencies

### Allowed Dependencies

| Category | Allowed Packages |
|----------|-----------------|
| Database | `pgx/v5`, `mongo-driver`, `mongo-driver/v2`, `go-redis/v9`, `dbresolver/v2`, `golang-migrate/v4` |
| Messaging | `amqp091-go` |
| HTTP | `gofiber/fiber/v2` |
| Logging | `github.com/LerianStudio/lib-observability/log`, `github.com/LerianStudio/lib-observability/zap` |
| Testing | `testify`, `go.uber.org/mock`, `miniredis/v2`, `testcontainers-go`, `goleak` |
| Observability | `github.com/LerianStudio/lib-observability`, `opentelemetry/*`, `otelzap`, `grpc`, `protobuf` |
| Utilities | `google/uuid`, `shopspring/decimal`, `go-playground/validator/v10`, `golang.org/x/sync`, `golang.org/x/text` |
| Resilience | `sony/gobreaker`, `go-redsync/v4` |
| Security | `golang.org/x/oauth2`, `google.golang.org/api`, `golang-jwt/jwt/v5`, `aws-sdk-go-v2` (secretsmanager) |
| System | `shirou/gopsutil`, `joho/godotenv` |

### Forbidden Dependencies

- `io/ioutil` - Deprecated, use `io` and `os` (enforced by `depguard` linter)
- Direct database drivers without connection pooling
- Logging packages outside `github.com/LerianStudio/lib-observability/log` and its zap adapter

### Adding Dependencies

1. Check if functionality exists in standard library
2. Check if existing dependency provides the functionality
3. Evaluate package maintenance and security
4. Add to `go.mod` with specific version

---

## Security

### Credential Handling

1. **Never hardcode credentials** - Use environment variables
2. **Never log credentials** - Use the `Redactor` for sensitive fields
3. **Mask in errors** - Never include credentials in error messages

```go
// Use lib-observability redaction helpers to classify sensitive field names.
if redaction.IsSensitiveField(fieldName) {
    // Mask, omit, or otherwise avoid logging the value.
}

// For structured span/payload redaction, use tracing.NewDefaultRedactor()
// with tracing.ObfuscateStruct() or the redacting span processor.
```

### Sensitive Field Detection

- Use `commons/security` for sensitive field detection and handling
- Use `github.com/LerianStudio/lib-observability/redaction` for field classification (`IsSensitiveField`, `DefaultSensitiveFields`)
- Do not add observability redaction shims back into lib-commons; use `github.com/LerianStudio/lib-observability/redaction` directly

### Input Validation

1. Validate all external inputs
2. Use parameterized queries - never string concatenation
3. Sanitize user-provided identifiers
4. Use `go-playground/validator/v10` for struct validation

### Log Injection Prevention

- Use `github.com/LerianStudio/lib-observability/log` sanitizer helpers for log-injection prevention
- Never interpolate untrusted input into log messages without sanitization

### Environment Variables

- Use `LOG_OBFUSCATION_DISABLED` to control HTTP body obfuscation (default: disabled)
- Sensitive field detection uses `commons/security.IsSensitiveField()` with a hardcoded set
- Document required environment variables
- Provide sensible defaults where safe

---

## DevOps

### Linting

- **Tool**: `golangci-lint` v2
- **Config**: `.golangci.yml`
- **Run**: `make lint` (read-only check) or `make lint-fix` (auto-fix)
- **Performance**: Optional `perfsprint` checks (install separately)

### Enabled Linters

**Existing linters:**
`bodyclose`, `depguard`, `dogsled`, `dupword`, `errchkjson`, `gocognit`, `gocyclo`, `loggercheck`, `misspell`, `nakedret`, `nilerr`, `nolintlint`, `prealloc`, `predeclared`, `reassign`, `revive`, `staticcheck`, `unconvert`, `unparam`, `usestdlibvars`, `wastedassign`, `wsl_v5`

**Tier 1 — Safety & Correctness:**
`errorlint`, `exhaustive`, `fatcontext`, `forcetypeassert`, `gosec`, `nilnil`, `noctx`

**Tier 2 — Code Quality & Modernization:**
`goconst`, `gocritic`, `inamedparam`, `intrange`, `mirror`, `modernize`, `perfsprint`

**Tier 3 — Zero-Issue Guards:**
`asasalint`, `copyloopvar`, `durationcheck`, `exptostd`, `gocheckcompilerdirectives`, `makezero`, `musttag`, `nilnesserr`, `recvcheck`, `rowserrcheck`, `spancheck`, `sqlclosecheck`, `testifylint`

### Formatting

- **Tool**: `gofmt`
- **Run**: `make format`
- All code MUST be formatted before commit

### Testing Commands

```bash
make ci                    # Local fix + verify pipeline
make test                  # Run unit tests (with -tags=unit)
make test-unit             # Run unit tests (excluding integration)
make test-integration      # Run integration tests with testcontainers (requires Docker)
make test-all              # Run all tests (unit + integration)
make coverage-unit         # Unit tests with coverage report
make coverage-integration  # Integration tests with coverage report
make coverage              # All coverage targets
```

### Testing Options

| Option | Description | Example |
|--------|-------------|---------|
| `RUN` | Specific test name pattern | `make test-integration RUN=TestIntegration_MyFeature` |
| `PKG` | Specific package to test | `make test-integration PKG=./commons/postgres/...` |
| `LOW_RESOURCE` | Low-resource mode (no race, -p=1) | `make test LOW_RESOURCE=1` |
| `RETRY_ON_FAIL` | Retry failed tests once | `make test RETRY_ON_FAIL=1` |

### Code Quality Commands

```bash
make lint                  # Run linters (read-only)
make lint-fix              # Run linters with auto-fix
make format                # Format code
make tidy                  # Clean dependencies
make check-tests           # Verify test coverage for packages
make vet                   # Run go vet on all packages
make sec                   # Security scan with gosec
make sec SARIF=1           # Security scan with SARIF output
make build                 # Build all packages
make clean                 # Clean all build artifacts
```

### Git Hooks

- Pre-commit hooks available in `.githooks/`
- Setup: `make setup-git-hooks`
- Verify: `make check-hooks`
- Environment check: `make check-envs`

### CI/CD

- All PRs must pass linting
- All PRs must pass tests
- Coverage must not decrease
- Security scan must pass

---

## API Invariants

Key v5 minor-line API contracts that must be preserved:

Extracted observability, systemplane, and streaming packages are not lib-commons API invariants. Their canonical APIs live in `lib-observability`, `lib-systemplane`, and `lib-streaming` respectively. The lib-commons observability/logging/runtime/assertion shim packages have been removed from the v5 split-library minor line.

| Package | Invariant |
|---------|-----------|
| `net/http` | `Respond`, `RespondStatus`, `RespondError`, `RenderError`, `FiberErrorHandler` |
| `net/http` | `ServeReverseProxy(target, policy, res, req)` with `ReverseProxyPolicy` |
| `server` | `ServerManager` exclusively (no `GracefulShutdown`) |
| `circuitbreaker` | `NewManager(logger) (Manager, error)`; `GetOrCreate` returns `(CircuitBreaker, error)` |
| `safe` | Explicit error returns for division, slice access, regex operations |
| `jwt` | `jwt.Parse()` / `jwt.Sign()` with `AlgHS256`, `AlgHS384`, `AlgHS512` |
| `backoff` | `ExponentialWithJitter()` and `WaitContext()` |
| `redis` | `New(ctx, cfg)` with topology-based `Config` (standalone/sentinel/cluster) |
| `redis` | `NewRedisLockManager()` and `LockManager` interface |
| `postgres` | `New(cfg Config)`; `Resolver(ctx)` (not `GetDB()`); `NewMigrator(cfg)` |
| `mongo` | `NewClient(ctx, cfg, opts...)` constructor |
| `transaction` | `BuildIntentPlan()` + `ValidateBalanceEligibility()` + `ApplyPosting()` |
| `rabbitmq` | `*Context()` variants for lifecycle; `HealthCheck()` returns `(bool, error)` |
| `certificate` | `NewManager(certPath, keyPath)` — empty paths return unconfigured manager (TLS optional). `Rotate(cert, key)` for zero-downtime hot-reload. `GetCertificateFunc()` for `tls.Config.GetCertificate`. All methods nil-safe. |
| `certificate` | Private key parsing order: PKCS#8 → PKCS#1 → EC. Key file must have mode 0600 or stricter (enforced at load time). Public key match validated against cert at load and rotate. |
| `dlq` | `New(conn, keyPrefix, maxRetries, opts...)` returns `*Handler`; `NewConsumer(handler, retryFn, opts...)` returns `(*Consumer, error)`. |
| `dlq` | `Handler.Enqueue(ctx, msg)`, `Dequeue(ctx, source)`, `QueueLength(ctx, source)`, `ScanQueues(ctx, source)`, `PruneExhaustedMessages(ctx, source, limit)`, `ExtractTenantFromKey(key, source)`. |
| `dlq` | `Consumer.Run(ctx)` blocks until ctx cancelled or `Stop()` called. `ProcessOnce(ctx)` exported for testing. Tenant isolation via `tmcore.GetTenantIDContext`. |
| `dlq` | `DLQMetrics` interface: `RecordRetried(ctx, source)`, `RecordExhausted(ctx, source)`. Nil metrics are silently skipped. |
| `net/http/idempotency` | `New(conn, opts...)` returns `*Middleware` (nil when conn is nil). `(*Middleware).Check()` returns a Fiber handler; nil receiver returns pass-through. |
| `net/http/idempotency` | Redis key pattern: `<prefix><tenantID>:<idempotencyKey>` with companion response key `…:response`. Default prefix `"idempotency:"`, TTL 7 days, max key length 256. |
| `net/http/idempotency` | Fail-open on Redis errors. GET/OPTIONS/HEAD requests pass through. Handler error deletes key (client may retry). In-flight duplicate returns 409 Conflict. |
| `webhook` | `NewDeliverer(lister, opts...)` returns `*Deliverer` (nil when lister is nil). `Deliver(ctx, event)` and `DeliverWithResults(ctx, event)` are the delivery entry points. |
| `webhook` | SSRF protection: `resolveAndValidateIP` performs a single DNS lookup, validates all resolved IPs against private/loopback/CGNAT/RFC-reserved ranges, and pins the URL to the first IP to eliminate TOCTOU. Redirects are blocked at transport layer. |
| `webhook` | HMAC-SHA256 signature sent in `X-Webhook-Signature: sha256=HEX` header over raw payload bytes only (timestamp excluded by design). Encrypted secrets use `enc:` prefix and require `WithSecretDecryptor`. |
| `webhook` | `EndpointLister` interface: `ListActiveEndpoints(ctx) ([]Endpoint, error)`. `DeliveryMetrics` interface: `RecordDelivery(ctx, endpointID, success, statusCode, attempts)`. |
| `outbox` | `OutboxRepository` contract: `Create`, `CreateWithTx`, `ListPending`, `ListPendingByType`, `ListTenants`, `GetByID`, `MarkPublished`, `MarkFailed`, `ListFailedForRetry`, `ResetForRetry`, `ResetStuckProcessing`, `MarkInvalid`. |
| `outbox` | Tenant-aware repositories validate tenant IDs with `tenant-manager/core.IsValidTenantID` and return `ErrInvalidTenantID` for invalid IDs; tenant discovery must not return malformed tenant IDs. |
| `outbox/mongo` | Mongo repository supports row-scoped tenant field storage by default and optional tenant Mongo database dispatch via `WithModule` plus `WithTenantDatabaseResolver`; module-scoped repositories fail closed with `ErrTenantDatabaseRequired` when no tenant database can be resolved. |
| `outbox/postgres` | Postgres repository supports schema-per-tenant and column-per-tenant strategies; column tenant isolation uses parameterized tenant filters and composite `(tenant_id, id)` semantics. |

---

## Checklist

Before submitting code:

- [ ] Code follows naming conventions
- [ ] All public APIs are documented
- [ ] Tests achieve 80%+ coverage
- [ ] Test files have correct build tag (`//go:build unit` or `//go:build integration`)
- [ ] No panics - all errors handled
- [ ] No hardcoded credentials
- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] `make build` passes
- [ ] Dependencies are justified
- [ ] Live README/package documentation updated if renamed, removed, redesigned, or extracted APIs affect consumers
- [ ] `README.md` updated if public API changed
