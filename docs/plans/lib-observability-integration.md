# Plan: lib-commons → lib-observability Integration

## Context

`lib-observability` (`github.com/LerianStudio/lib-observability`) is a new standalone library
extracted from lib-commons that owns all observability and telemetry concerns: tracing, metrics,
logging, panic recovery, assertions, HTTP/gRPC middleware instrumentation, and sensitive-field
redaction.

**Dependency direction (non-negotiable):**

```
lib-observability  ←  lib-commons  ←  applications
```

lib-observability must NEVER import lib-commons. lib-commons will import lib-observability and
delegate its own observability packages to it, keeping its public API unchanged so consumers
don't break.

---

## Current State

lib-observability already contains full ports of the following lib-commons packages:

| lib-commons package | lib-observability package | Status |
|---|---|---|
| `commons/opentelemetry/` | `tracing/` | ✅ Ported (PR #2) |
| `commons/log/` | `log/` | ✅ Ported (PR #2) |
| `commons/zap/` | `zap/` | ✅ Ported (PR #2) |
| `commons/runtime/` | `runtime/` | ✅ Ported (PR #2) |
| `commons/assert/` | `assert/` | ✅ Ported (PR #2) |
| `commons/opentelemetry/metrics/` | `metrics/` | ✅ Ported (PR #2) |
| `commons/net/http/` (telemetry parts) | `middleware/` | ✅ Ported (PR #2) |
| `commons/opentelemetry/redaction/` | `redaction/` | ✅ Ported (PR #2) |
| `commons/opentelemetry/constants/` | `constants/` | ✅ Ported (PR #2) |
| Root context helpers | root `package observability` | ✅ Ported (PR #2) |

lib-commons does **not** yet depend on lib-observability — the packages above are duplicated.
This plan removes that duplication.

---

## What Does NOT Move to lib-observability

These lib-commons packages use observability internally but are domain-specific to lib-commons
and do not belong in lib-observability:

| Package | Reason stays in lib-commons |
|---|---|
| `commons/streaming/` | kafka/franz-go producer — calls lib-observability primitives internally |
| `commons/postgres/`, `commons/mongo/`, `commons/redis/`, `commons/rabbitmq/` | infrastructure clients — use tracing/metrics internally |
| `commons/circuitbreaker/` | resilience pattern — emits metrics internally |
| `commons/outbox/` | transactional pattern — emits metrics internally |
| `commons/net/http/` (routing, non-telemetry) | request routing logic stays lib-commons |

---

## Migration Phases

### Phase 1 — Add lib-observability Dependency

```bash
cd lib-commons
go get github.com/LerianStudio/lib-observability@latest
go mod tidy
```

Verify no circular import is introduced:
```bash
go mod graph | grep lib-observability
# Should show: lib-commons → lib-observability (never the reverse)
```

---

### Phase 2 — Delegate Observability Packages to lib-observability

For each mirrored package, the strategy is:

1. Keep the lib-commons package path and public types **unchanged** (backward compat)
2. Replace the implementation with delegation to lib-observability
3. Use type aliases where the type is directly re-exported

#### 2.1 `commons/log/`

Replace the `Logger` interface, `Level`, and `Field` helpers with type aliases from
`lib-observability/log`:

```go
// commons/log/log.go — keep package, replace body
package log

import libobs "github.com/LerianStudio/lib-observability/log"

type Logger = libobs.Logger
type Level = libobs.Level
type Field = libobs.Field

// Helpers re-exported
var (
    String  = libobs.String
    Int     = libobs.Int
    Bool    = libobs.Bool
    Any     = libobs.Any
    Err     = libobs.Err
    LevelInfo  = libobs.LevelInfo
    LevelWarn  = libobs.LevelWarn
    LevelError = libobs.LevelError
    LevelDebug = libobs.LevelDebug
)

func NewNop() Logger { return libobs.NewNop() }
func ParseLevel(s string) (Level, error) { return libobs.ParseLevel(s) }
```

#### 2.2 `commons/opentelemetry/` (tracing)

```go
// commons/opentelemetry/otel.go — keep package, delegate
package opentelemetry

import "github.com/LerianStudio/lib-observability/tracing"

type Telemetry = tracing.Telemetry
type TelemetryConfig = tracing.TelemetryConfig

func NewTelemetry(cfg TelemetryConfig) (*Telemetry, error) {
    return tracing.NewTelemetry(cfg)
}
// ... re-export all public functions as thin wrappers or aliases
```

#### 2.3 `commons/zap/`

```go
package zap

import (
    libzap "github.com/LerianStudio/lib-observability/zap"
    "github.com/LerianStudio/lib-observability/log"
)

func NewZapLogger(zl *zap.Logger, tl *tracing.Telemetry) log.Logger {
    return libzap.NewZapLogger(zl, tl)
}
```

#### 2.4 `commons/runtime/`

```go
package runtime

import "github.com/LerianStudio/lib-observability/runtime"

type PanicPolicy = runtime.PanicPolicy

var (
    KeepRunning   = runtime.KeepRunning
    CrashProcess  = runtime.CrashProcess
    SafeGo        = runtime.SafeGo
    RecoverWithPolicy = runtime.RecoverWithPolicy
    // ... all public symbols
)
```

#### 2.5 `commons/assert/`

```go
package assert

import "github.com/LerianStudio/lib-observability/assert"

type Asserter = assert.Asserter

var New = assert.New
// predicates, metrics helpers, etc.
```

#### 2.6 `commons/opentelemetry/metrics/`

```go
package metrics

import "github.com/LerianStudio/lib-observability/metrics"

type MetricsFactory = metrics.MetricsFactory
// ... re-export builders, recorders
```

#### 2.7 `commons/net/http/` (telemetry parts)

```go
// TelemetryMiddleware and helpers delegate to lib-observability/middleware
package http

import "github.com/LerianStudio/lib-observability/middleware"

type TelemetryMiddleware = middleware.TelemetryMiddleware

func NewTelemetryMiddleware(tl *tracing.Telemetry) *TelemetryMiddleware {
    return middleware.NewTelemetryMiddleware(tl)
}
```

---

### Phase 3 — Update Internal Consumers in lib-commons

Packages that use observability internally (not re-exported, just consumed):

| Package | Change needed |
|---|---|
| `commons/streaming/emit_span.go` | Replace `commons/opentelemetry` imports → `lib-observability/tracing` |
| `commons/streaming/metrics.go` | Replace `commons/opentelemetry/metrics` imports → `lib-observability/metrics` |
| `commons/streaming/metrics_recorders.go` | Same as above |
| `commons/postgres/`, `commons/mongo/`, etc. | Replace internal tracing calls → `lib-observability/tracing` |
| `commons/circuitbreaker/` | Replace metrics/tracing → lib-observability equivalents |
| `commons/outbox/` | Replace metrics/tracing → lib-observability equivalents |

---

### Phase 4 — Remove Duplicated Implementations

Once Phase 2 and 3 are complete and all tests pass, delete the implementation bodies from the
lib-commons packages that have been fully delegated. Keep only the thin re-export layer.

Order of deletion (reverse dependency order):
1. Delete implementations in `commons/opentelemetry/constants/`
2. Delete implementations in `commons/opentelemetry/redaction/`
3. Delete implementations in `commons/log/`
4. Delete implementations in `commons/zap/`
5. Delete implementations in `commons/runtime/`
6. Delete implementations in `commons/assert/`
7. Delete implementations in `commons/opentelemetry/metrics/`
8. Delete implementations in `commons/opentelemetry/` (tracing)
9. Delete implementations in `commons/net/http/` (telemetry parts only)

Validate after each deletion:
```bash
go build ./... && go test ./...
```

---

### Phase 5 — Validate & Release

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run ./...
go mod tidy
```

Bump lib-commons to next minor version (e.g., v5.X.0) since this is an internal refactor with
no public API changes.

---

## Things Still to Annotate: Remaining Migration from lib-commons → lib-observability

These items were identified during lib-observability extraction as candidates for a future
migration pass. They remain in lib-commons today and should be evaluated in a follow-up:

### A. `commons/opentelemetry/` — Obfuscation engine
- `commons/opentelemetry/obfuscation.go` — `Redactor`, `RedactionRule`, `actionFor()`
- Currently mirrored in `lib-observability/tracing/obfuscation.go`
- **Action**: After Phase 2, verify API parity and remove from lib-commons

### B. Context carrier helpers in root `commons` package
- `commons/observability.go` or equivalent: `ContextWithSpanAttributes()`,
  `NewLoggerFromContext()`, `NewTrackingFromContext()`, `ContextWithHeaderID()`
- Mirrored in `lib-observability/observability.go` and `lib-observability/context_helpers.go`
- **Action**: Re-export from lib-observability; delete lib-commons implementations

### C. `commons/net/http/context_span.go`
- `SetHandlerSpanAttributes()`, `SetTenantSpanAttribute()`, `SetExceptionSpanAttributes()`,
  `SetDisputeSpanAttributes()`
- Mirrored in `lib-observability/middleware/context_span.go`
- **Action**: Re-export; delete lib-commons implementations

### D. `commons/opentelemetry/system.go`
- `GetCPUUsage()`, `GetMemUsage()` — system metrics helpers
- Mirrored in `lib-observability/system.go`
- **Action**: Re-export; delete lib-commons implementations

---

## Cyclic Dependency Prevention

A CI check should be added to lib-observability to ensure it never imports lib-commons:

```bash
# In lib-observability CI
go mod graph | grep -E "lib-observability.*lib-commons"
# Must be empty — any match fails the build
```

Add to `lib-observability/Makefile`:
```makefile
check-no-circular:
	@go mod graph | grep -E "lib-observability.*lib-commons" && \
	  (echo "FATAL: circular dependency detected" && exit 1) || \
	  echo "OK: no circular dependency"
```
