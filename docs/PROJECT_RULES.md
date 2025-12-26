# Project Rules - lib-commons

This document defines the coding standards, architecture patterns, and development guidelines for the `lib-commons` library.

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

```
lib-commons/
├── commons/                    # All library packages
│   ├── {package}/              # Feature package
│   │   ├── {package}.go        # Main implementation
│   │   ├── {package}_test.go   # Unit tests
│   │   └── doc.go              # Package documentation (optional)
│   ├── context.go              # Root-level utilities
│   ├── errors.go               # Error definitions
│   └── utils.go                # Utility functions
├── docs/                       # Documentation
├── scripts/                    # Build/test scripts
└── go.mod                      # Module definition
```

### Package Design Principles

1. **Single Responsibility**: Each package should have one clear purpose
2. **Minimal Dependencies**: Packages should minimize external dependencies
3. **Interface-Driven**: Define interfaces for testability and flexibility
4. **Zero Business Logic**: This is a utility library - no domain/business logic

### Naming Conventions

| Type | Convention | Example |
|------|------------|---------|
| Package | lowercase, single word preferred | `postgres`, `redis`, `poolmanager` |
| Files | snake_case matching content | `pool_manager_pg.go` |
| Public Functions | PascalCase, descriptive | `NewPostgresConnection` |
| Private Functions | camelCase | `validateConfig` |
| Interfaces | -er suffix or descriptive | `Logger`, `ConnectionPool` |
| Constants | PascalCase or UPPER_SNAKE_CASE | `DefaultTimeout`, `MAX_RETRIES` |

---

## Code Conventions

### Go Version

- **Minimum**: Go 1.24.0
- Keep `go.mod` updated with latest stable Go version

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
    "github.com/LerianStudio/lib-commons/v2/commons/log"
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
- **Run Coverage**: `make cover`

### Test File Naming

| Type | Pattern | Example |
|------|---------|---------|
| Unit Tests | `{file}_test.go` | `config_test.go` |
| Integration | `{file}_integration_test.go` | `postgres_integration_test.go` |
| Benchmarks | In `_test.go` files | `BenchmarkXxx` |

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

---

## Dependencies

### Allowed Dependencies

| Category | Allowed Packages |
|----------|-----------------|
| Database | `pgx/v5`, `mongo-driver`, `go-redis/v9` |
| Messaging | `amqp091-go` |
| Logging | `zap`, internal `log` package |
| Testing | `testify`, `gomock`, `miniredis` |
| Observability | `opentelemetry/*` |
| Utilities | `google/uuid`, `shopspring/decimal` |

### Forbidden Dependencies

- `io/ioutil` - Deprecated, use `io` and `os`
- Direct database drivers without connection pooling
- Logging packages other than `zap` (use internal wrapper)

### Adding Dependencies

1. Check if functionality exists in standard library
2. Check if existing dependency provides the functionality
3. Evaluate package maintenance and security
4. Add to `go.mod` with specific version

---

## Security

### Credential Handling

1. **Never hardcode credentials** - Use environment variables
2. **Never log credentials** - Use obfuscation for sensitive fields
3. **Mask in errors** - Never include credentials in error messages

```go
// Good - mask DSN
func MaskDSN(dsn string) string {
    return regexp.MustCompile(`password=[^\s]+`).ReplaceAllString(dsn, "password=***")
}

// Bad - exposes password
log.Errorf("failed to connect: %s", dsn)
```

### Input Validation

1. Validate all external inputs
2. Use parameterized queries - never string concatenation
3. Sanitize user-provided identifiers

### Environment Variables

- Use `SECURE_LOG_FIELDS` for field obfuscation
- Document required environment variables
- Provide sensible defaults where safe

---

## DevOps

### Linting

- **Tool**: `golangci-lint` v2
- **Config**: `.golangci.yml`
- **Run**: `make lint`

### Formatting

- **Tool**: `gofmt`
- **Run**: `make format`
- All code MUST be formatted before commit

### Testing Commands

```bash
make test      # Run all tests
make cover     # Generate coverage report
make lint      # Run linters
make format    # Format code
make sec       # Security scan with gosec
make tidy      # Clean up go.mod
```

### Git Hooks

- Pre-commit hooks available in `.githooks/`
- Setup: `make setup-git-hooks`
- Verify: `make check-hooks`

### CI/CD

- All PRs must pass linting
- All PRs must pass tests
- Coverage must not decrease
- Security scan must pass

---

## Checklist

Before submitting code:

- [ ] Code follows naming conventions
- [ ] All public APIs are documented
- [ ] Tests achieve 80%+ coverage
- [ ] No panics - all errors handled
- [ ] No hardcoded credentials
- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Dependencies are justified
