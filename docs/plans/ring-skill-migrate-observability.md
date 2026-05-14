---
name: ring:migrate-observability
description: |
  Migrates a Lerian application's direct telemetry/observability imports from
  lib-commons to lib-observability. Scans the codebase for lib-commons
  observability import paths, replaces them with lib-observability equivalents,
  adds lib-observability to go.mod, and validates the build.
  Does NOT touch kafka/streaming instrumentation (internal to lib-commons)
  or non-observability lib-commons packages.

trigger: |
  - Application imports lib-commons observability packages directly
  - Team decision to decouple application from lib-commons for telemetry
  - lib-commons observability packages deprecated in favour of lib-observability

skip_when: |
  - Application already imports lib-observability for observability
  - Application has no direct observability imports from lib-commons
  - Application is lib-commons itself

NOT_skip_when: |
  - "The app only imports log/ or opentelemetry/ from lib-commons" → still migrate; these are the primary targets
  - "The app uses streaming/kafka" → streaming stays lib-commons; only telemetry imports change

sequence:
  before: []
  after: []

related:
  complementary: [ring:dev-cycle, ring:codereview, ring:lint]

input_schema:
  required:
    - name: repo_path
      type: string
      description: "Absolute path to the application repository root"
  optional:
    - name: dry_run
      type: boolean
      default: false
      description: "If true, report what would change without writing files"
    - name: lib_observability_version
      type: string
      default: "latest"
      description: "Version of lib-observability to add to go.mod"

output_schema:
  format: markdown
  required_sections:
    - name: "Discovery"
      pattern: "^## Discovery"
      required: true
    - name: "Migration Plan"
      pattern: "^## Migration Plan"
      required: true
    - name: "Changes Applied"
      pattern: "^## Changes Applied"
      required: true
    - name: "Validation"
      pattern: "^## Validation"
      required: true
  metrics:
    - name: result
      type: enum
      values: [PASS, FAIL, PARTIAL]
    - name: files_changed
      type: integer
    - name: imports_replaced
      type: integer
    - name: imports_skipped
      type: integer

verification:
  automated:
    - command: "go build ./... 2>&1 | grep -c error"
      description: "Build passes after migration"
      success_pattern: "^0$"
    - command: "go test ./... 2>&1 | grep -c FAIL"
      description: "Tests pass after migration"
      success_pattern: "^0$"
  manual:
    - "Verify no lib-commons observability imports remain"
    - "Verify lib-observability appears in go.mod"

---

# Migrate Observability Imports to lib-observability

## Overview

This skill replaces direct lib-commons observability/telemetry imports in a Lerian Go application
with equivalent lib-observability imports. It covers the packages that were extracted from
lib-commons and now live in `github.com/LerianStudio/lib-observability`.

**What changes:** import paths in `.go` files + `go.mod` dependency.
**What stays the same:** kafka/streaming instrumentation (internal to lib-commons methods),
non-observability lib-commons packages (`commons/postgres`, `commons/multitenancy`, etc.).

---

## CRITICAL: Role Clarification

| Who | Responsibility |
|-----|----------------|
| **This Skill** | Discover imports, plan replacements, validate, report |
| **Agent** | Apply file edits, run go mod, fix compilation errors |

---

## Import Mapping Reference

The canonical mapping from lib-commons → lib-observability:

| lib-commons import path | lib-observability import path | Notes |
|---|---|---|
| `lib-commons/v5/commons/opentelemetry` | `lib-observability/tracing` | Main OTEL bootstrap, Telemetry type |
| `lib-commons/v5/commons/opentelemetry/constants` | `lib-observability/constants` | OTEL attribute & metric name constants |
| `lib-commons/v5/commons/opentelemetry/redaction` | `lib-observability/redaction` | IsSensitiveField, DefaultSensitiveFields |
| `lib-commons/v5/commons/log` | `lib-observability/log` | Logger interface, Level, Field helpers |
| `lib-commons/v5/commons/zap` | `lib-observability/zap` | NewZapLogger, OTEL bridge |
| `lib-commons/v5/commons/runtime` | `lib-observability/runtime` | SafeGo, RecoverWithPolicy, PanicMetrics |
| `lib-commons/v5/commons/assert` | `lib-observability/assert` | Asserter, financial predicates |
| `lib-commons/v5/commons/opentelemetry/metrics` | `lib-observability/metrics` | MetricsFactory, Counter/Gauge/Histogram builders |
| `lib-commons/v5/commons/net/http` *(telemetry parts only)* | `lib-observability/middleware` | TelemetryMiddleware, SetHandlerSpanAttributes, context span helpers |

### Do NOT migrate (stays lib-commons)

| Import | Reason |
|---|---|
| `lib-commons/v5/commons/net/http` (routing, non-telemetry) | Request routing stays lib-commons |
| `lib-commons/v5/commons/streaming` | kafka/franz-go producer; telemetry is internal |
| `lib-commons/v5/commons/postgres`, `mongo`, `redis`, `rabbitmq` | infrastructure clients |
| `lib-commons/v5/commons/multitenancy` | multi-tenant dispatch layer |
| `lib-commons/v5/commons/systemplane` | runtime config client |
| `lib-commons/v5/commons` (root package, app config) | AppConfig, EffectiveSecurityTier, etc. |

---

## Step 1: Validate Input

<verify_before_proceed>
- repo_path exists and contains a go.mod file
- go.mod declares module path (to identify lib-commons import prefix)
</verify_before_proceed>

```text
1. Verify repo_path/go.mod exists
2. Extract module name from go.mod
3. Confirm lib-commons is a dependency: grep "lib-commons" go.mod
   if not found → report "No lib-commons dependency found. Nothing to migrate." and exit PASS
```

---

## Step 2: Discover Observability Imports

Scan all `.go` files in the repository for lib-commons observability import paths.

```text
Search patterns (grep -r across *.go files):

MIGRATE targets:
  lib-commons/v5/commons/opentelemetry"
  lib-commons/v5/commons/opentelemetry/constants"
  lib-commons/v5/commons/opentelemetry/redaction"
  lib-commons/v5/commons/opentelemetry/metrics"
  lib-commons/v5/commons/log"
  lib-commons/v5/commons/zap"
  lib-commons/v5/commons/runtime"
  lib-commons/v5/commons/assert"
  lib-commons/v5/commons/net/http"   ← telemetry parts only; see Step 3

DO NOT MIGRATE targets (skip, report):
  lib-commons/v5/commons/streaming"
  lib-commons/v5/commons/postgres"
  lib-commons/v5/commons/mongo"
  lib-commons/v5/commons/redis"
  lib-commons/v5/commons/rabbitmq"
  lib-commons/v5/commons/multitenancy"
  lib-commons/v5/commons/systemplane"
  lib-commons/v5/commons"            ← root package (AppConfig etc.)

For each found import, record:
  - file path
  - line number
  - import alias (if any)
  - full import path
```

**Output discovery report:**
```
## Discovery

### Imports to Migrate
| File | Line | Current Import | Target Import |
|------|------|---------------|---------------|
| cmd/main.go | 5 | lib-commons/v5/commons/opentelemetry | lib-observability/tracing |
| ...

### Imports NOT Migrated (kept in lib-commons)
| File | Line | Import | Reason |
|------|------|--------|--------|
| ...

### Summary
- Total files to change: X
- Total imports to replace: Y
- Total imports left as-is: Z
```

---

## Step 3: Handle `commons/net/http` Carefully

`commons/net/http` has both telemetry and non-telemetry exports. Identify what the file actually
uses from this import:

```text
For each file importing lib-commons/v5/commons/net/http:
  1. Read the file
  2. Identify which symbols are used from this import
  3. Classify each symbol:

  TELEMETRY (migrate to lib-observability/middleware):
    - TelemetryMiddleware
    - NewTelemetryMiddleware
    - WithTelemetry (the fiber middleware constructor)
    - SetHandlerSpanAttributes
    - SetTenantSpanAttribute
    - SetExceptionSpanAttributes
    - SetDisputeSpanAttributes

  NON-TELEMETRY (keep lib-commons/v5/commons/net/http):
    - Any routing, request parsing, or non-span helpers

  Decision:
    - If file uses ONLY telemetry symbols → replace import entirely
    - If file uses BOTH → split into two imports (lib-commons + lib-observability)
    - If file uses ONLY non-telemetry → no change
```

---

## Step 4: Present Migration Plan and Confirm

<dispatch_required agent="ring:backend-engineer-golang">
Do not proceed with edits until user approves.
</dispatch_required>

Present the full plan using AskUserQuestion:
```
Options:
  1. Apply all migrations as planned
  2. Dry run — show diffs without writing
  3. Select specific packages only
```

If dry_run=true from input, skip this step and show diffs only.

---

## Step 5: Add lib-observability to go.mod

```bash
go get github.com/LerianStudio/lib-observability@{lib_observability_version}
```

Verify it appears in go.mod:
```bash
grep "lib-observability" go.mod
```

---

## Step 6: Apply Import Replacements

<dispatch_required agent="ring:backend-engineer-golang">
For each file identified in Step 2:
1. Read the file
2. Replace the import path(s) using the mapping table
3. Preserve any import aliases the file was using
4. If the package name changed (e.g., `opentelemetry` → `tracing`), update all usages of the
   alias in the file body
5. Write the updated file

Important:
- `opentelemetry.NewTelemetry(...)` becomes `tracing.NewTelemetry(...)`
  (if imported without alias, the package qualifier changes)
- If file used an alias `otel "lib-commons/.../opentelemetry"`, preserve as
  `otel "lib-observability/tracing"` — no body changes needed
- For `commons/log` → `log` package name is the same; import path changes only
- For `commons/zap` → `zap` package name is the same; import path changes only
</dispatch_required>

---

## Step 7: Run go mod tidy

```bash
go mod tidy
```

Check that lib-commons is still present (needed for non-observability packages):
```bash
grep "lib-commons" go.mod
# Should still be there unless ALL lib-commons imports were replaced
```

---

## Step 8: Validate Build and Tests

```bash
go build ./...
go vet ./...
go test ./...
```

<block_condition>
- `go build ./...` exits non-zero
</block_condition>

If build fails:
1. Read each compilation error
2. Identify type mismatches (API differences between lib-commons and lib-observability ports)
3. Fix them — common causes:
   - `opentelemetry.Telemetry` → `tracing.Telemetry` (package qualifier changed without alias)
   - Struct field names that differ between ports
   - Method signatures that differ (check lib-observability source)
4. Re-run until `go build ./...` passes

---

## Step 9: Verify No Remaining Observability Imports from lib-commons

```bash
grep -r "lib-commons/v5/commons/opentelemetry" . --include="*.go"
grep -r "lib-commons/v5/commons/log" . --include="*.go"
grep -r "lib-commons/v5/commons/zap" . --include="*.go"
grep -r "lib-commons/v5/commons/runtime" . --include="*.go"
grep -r "lib-commons/v5/commons/assert" . --include="*.go"
```

Each should return zero results.

---

## Step 10: Produce Final Report

```markdown
## Changes Applied

| File | Imports Replaced | Before | After |
|------|-----------------|--------|-------|
| cmd/main.go | 2 | commons/opentelemetry, commons/log | tracing, log |
| ...

## Validation

| Check | Result |
|-------|--------|
| go build ./... | ✅ PASS |
| go vet ./... | ✅ PASS |
| go test ./... | ✅ PASS |
| No remaining lib-commons observability imports | ✅ PASS |

## Summary

- Files changed: X
- Imports replaced: Y
- Build status: PASS
- Result: PASS
```

---

## Severity Calibration

| Severity | Criteria |
|----------|----------|
| CRITICAL | Build fails after migration and cannot be fixed by import path changes alone |
| HIGH | Type mismatch between lib-commons and lib-observability APIs for a given package |
| MEDIUM | Import alias collision requiring manual rename |
| LOW | Unused import warning after migration |

---

## Pressure Resistance

| Pushback | Response |
|---|---|
| "The app only uses one log import, not worth migrating" | Consistency across the codebase matters. Migrate now to avoid two sources of truth. |
| "lib-commons still has these packages, why bother?" | lib-commons observability packages will be deprecated and removed once all consumers migrate. Starting now reduces future blast radius. |
| "What if lib-observability API differs slightly?" | Step 8 handles this explicitly. Fix compilation errors after replacement — they are usually just package qualifier changes. |
| "Streaming imports will break" | Streaming stays lib-commons. The skill explicitly skips `commons/streaming`. |

---

## Anti-Rationalization Table

| Rationalization | Why Wrong | Action |
|---|---|---|
| "I'll skip commons/net/http because it's complex" | Step 3 handles the mixed case — split import if needed | Follow Step 3 classification |
| "The tests pass even with mixed imports" | Mixed imports create a two-source-of-truth problem | Replace all observability imports |
| "I'll do it later when lib-commons officially deprecates" | Migration is harder with more files in flight | Migrate now, reduce future risk |
