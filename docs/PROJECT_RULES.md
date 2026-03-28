# Project Rules - lib-commons

This document defines the coding standards, architecture patterns, and development guidelines for the unified `lib-commons` library.

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
│   ├── assert/                     # Production-safe assertions with telemetry
│   ├── backoff/                    # Exponential backoff with jitter
│   ├── circuitbreaker/             # Circuit breaker manager and health checker
│   ├── constants/                  # Shared constants (headers, errors, pagination)
│   ├── cron/                       # Cron expression parsing and scheduling
│   ├── crypto/                     # Hashing and symmetric encryption
│   ├── errgroup/                   # Goroutine coordination with panic recovery
│   ├── internal/                   # Internal packages (not part of public API)
│   │   └── nilcheck/               # Nil interface detection helpers
│   ├── jwt/                        # HMAC-based JWT signing and verification
│   ├── license/                    # License validation and enforcement
│   ├── log/                        # Logging abstraction (Logger interface)
│   ├── mongo/                      # MongoDB connector
│   ├── net/http/                   # Fiber-oriented HTTP helpers and middleware
│   │   └── ratelimit/              # Redis-backed rate limit storage
│   ├── opentelemetry/              # Telemetry bootstrap, propagation, redaction
│   │   └── metrics/                # Metric factory and fluent builders
│   ├── outbox/                     # Transactional outbox primitives
│   │   └── postgres/               # PostgreSQL outbox adapter with migrations
│   ├── pointers/                   # Pointer conversion helpers
│   ├── postgres/                   # PostgreSQL connector with migrations
│   ├── rabbitmq/                   # RabbitMQ connector
│   ├── redis/                      # Redis connector (standalone/sentinel/cluster)
│   ├── runtime/                    # Panic recovery, metrics, safe goroutine wrappers
│   ├── safe/                       # Panic-free math/regex/slice operations
│   ├── secretsmanager/             # AWS Secrets Manager M2M credential retrieval
│   ├── security/                   # Sensitive field detection and handling
│   ├── server/                     # Graceful shutdown and lifecycle (ServerManager)
│   ├── shell/                      # Makefile includes and shell utilities
│   ├── systemplane/                # Runtime configuration plane (hot-reloadable settings)
│   │   ├── adapters/               # Store (postgres, mongodb) and changefeed adapters
│   │   ├── bootstrap/              # Environment-based config loading
│   │   ├── domain/                 # Domain types, entries, revisions, snapshots
│   │   ├── ports/                  # Port interfaces (store, changefeed, history, reconciler)
│   │   ├── registry/               # Configuration key registry and validation
│   │   ├── service/                # Service manager, supervisor, escalation
│   │   └── testutil/               # Test fakes for systemplane contracts
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
│   ├── zap/                        # Zap logging adapter
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
└── go.mod                          # Module definition (v4)
```

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

- **Minimum**: Go 1.25.7
- Keep `go.mod` updated with latest stable Go version
- Module path: `github.com/LerianStudio/lib-commons/v4`

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
    "github.com/LerianStudio/lib-commons/v4/commons/log"
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

- If a task touches renamed/removed v1 symbols, update `MIGRATION_MAP.md`
- If a task changes package-level behavior or API expectations, update `README.md`

---

## Dependencies

### Allowed Dependencies

| Category | Allowed Packages |
|----------|-----------------|
| Database | `pgx/v5`, `mongo-driver`, `mongo-driver/v2`, `go-redis/v9`, `dbresolver/v2`, `golang-migrate/v4` |
| Messaging | `amqp091-go` |
| HTTP | `gofiber/fiber/v2` |
| Logging | `zap`, internal `log` package |
| Testing | `testify`, `go.uber.org/mock`, `miniredis/v2`, `testcontainers-go`, `go-sqlmock`, `goleak` |
| Observability | `opentelemetry/*`, `otelzap`, `grpc`, `protobuf` |
| Utilities | `google/uuid`, `shopspring/decimal`, `go-playground/validator/v10`, `golang.org/x/sync`, `golang.org/x/text` |
| Resilience | `sony/gobreaker`, `go-redsync/v4` |
| Security | `golang.org/x/oauth2`, `google.golang.org/api`, `golang-jwt/jwt/v5`, `aws-sdk-go-v2` (secretsmanager) |
| System | `shirou/gopsutil`, `joho/godotenv` |

### Forbidden Dependencies

- `io/ioutil` - Deprecated, use `io` and `os` (enforced by `depguard` linter)
- Direct database drivers without connection pooling
- Logging packages other than `zap` (use internal `log` wrapper)

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
// Use the built-in Redactor for sensitive data
redactor := opentelemetry.NewDefaultRedactor()
safeValue := redactor.Redact(sensitiveField)
```

### Sensitive Field Detection

- Use `commons/security` for sensitive field detection and handling
- Use `commons/opentelemetry.Redactor` with `RedactionRule` patterns
- Constructors: `NewDefaultRedactor()` and `NewRedactor(rules, mask)`

### Input Validation

1. Validate all external inputs
2. Use parameterized queries - never string concatenation
3. Sanitize user-provided identifiers
4. Use `go-playground/validator/v10` for struct validation

### Log Injection Prevention

- Use `commons/log/sanitizer.go` for log-injection prevention
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

Key v4 API contracts that must be preserved:

| Package | Invariant |
|---------|-----------|
| `opentelemetry` | `NewTelemetry(...)` for init; `ApplyGlobals()` opt-in for global providers |
| `log` | `Logger` 5-method interface: `Log`, `With`, `WithGroup`, `Enabled`, `Sync` |
| `log` | Level constants: `LevelError`, `LevelWarn`, `LevelInfo`, `LevelDebug` |
| `log` | Field constructors: `String()`, `Int()`, `Bool()`, `Err()` |
| `zap` | `zap.New(cfg Config)` constructor; `Logger.Raw()` for underlying access |
| `net/http` | `Respond`, `RespondStatus`, `RespondError`, `RenderError`, `FiberErrorHandler` |
| `net/http` | `ServeReverseProxy(target, policy, res, req)` with `ReverseProxyPolicy` |
| `server` | `ServerManager` exclusively (no `GracefulShutdown`) |
| `circuitbreaker` | `NewManager(logger) (Manager, error)`; `GetOrCreate` returns `(CircuitBreaker, error)` |
| `assert` | `assert.New(ctx, logger, component, operation)` returns errors, no panics |
| `safe` | Explicit error returns for division, slice access, regex operations |
| `jwt` | `jwt.Parse()` / `jwt.Sign()` with `AlgHS256`, `AlgHS384`, `AlgHS512` |
| `backoff` | `ExponentialWithJitter()` and `WaitContext()` |
| `redis` | `New(ctx, cfg)` with topology-based `Config` (standalone/sentinel/cluster) |
| `redis` | `NewRedisLockManager()` and `LockManager` interface |
| `postgres` | `New(cfg Config)`; `Resolver(ctx)` (not `GetDB()`); `NewMigrator(cfg)` |
| `mongo` | `NewClient(ctx, cfg, opts...)` constructor |
| `transaction` | `BuildIntentPlan()` + `ValidateBalanceEligibility()` + `ApplyPosting()` |
| `rabbitmq` | `*Context()` variants for lifecycle; `HealthCheck()` returns `(bool, error)` |
| `opentelemetry` | `Redactor` with `RedactionRule`; `NewDefaultRedactor()` / `NewRedactor(rules, mask)` |

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
- [ ] `MIGRATION_MAP.md` updated if v1 symbols changed
- [ ] `README.md` updated if public API changed
