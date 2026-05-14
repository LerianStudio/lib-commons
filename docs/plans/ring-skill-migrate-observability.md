---
name: ring:migrate-observability
description: |
  Migrates a Lerian Go application's direct observability imports from the
  deprecated lib-commons shim packages to lib-observability. Targets exactly
  the packages marked //Deprecated: in lib-commons — no more, no less.
  Adds lib-observability to go.mod and validates the build.
  Does NOT touch commons/opentelemetry (tracing bootstrap, not deprecated),
  commons/net/http, kafka/streaming, or any non-deprecated lib-commons package.
---

# Migrate Deprecated lib-commons Observability Packages to lib-observability

## When to use
- Application imports one or more deprecated lib-commons observability packages
- Team decision to eliminate deprecation warnings from lib-commons shims
- lib-commons deprecation notices appear in IDE or go vet output

## Skip when
- Application already imports lib-observability for all observability concerns
- Application has no imports of the deprecated lib-commons packages listed below
- Application is lib-commons itself
- lib-commons version in go.mod does not yet include the delegation shims (commons/log/doc.go has no Deprecated notice) — upgrade lib-commons first

**Do NOT skip when:**
- "The app only imports log/ from lib-commons" → still migrate; log is deprecated
- "The app uses streaming/kafka" → streaming is NOT deprecated; only the 7 shim packages migrate
- "The app uses commons/opentelemetry for tracing bootstrap" → that package is NOT deprecated; leave it

## Sequence
**Runs before:** (none)
**Runs after:** (none)

## Related
**Complementary:** ring:dev-cycle, ring:codereview, ring:lint, ring:using-lib-commons

---

## Overview

This skill replaces imports of the **deprecated** lib-commons observability shim packages
with their canonical lib-observability equivalents.

**Targeting strategy:** Only packages marked `//Deprecated:` in lib-commons are migrated.
This guarantees precision — every replacement has a known, tested lib-observability counterpart.
Packages that are NOT deprecated in lib-commons (e.g. `commons/opentelemetry` tracing bootstrap,
`commons/net/http`, `commons/streaming`) are explicitly out of scope.

**What changes:** import paths in `.go` files + `go.mod` dependency.
**What stays the same:** all non-deprecated lib-commons packages, including infrastructure
clients (`commons/postgres`, `commons/streaming`, etc.) and the tracing bootstrap
(`commons/opentelemetry`).

---

## CRITICAL: Role Clarification

| Who | Responsibility |
|-----|----------------|
| **This Skill** | Discover imports, plan replacements, validate, report |
| **Agent** | Apply file edits, run go mod, fix compilation errors |

---

## Import Mapping Reference

The 7 deprecated lib-commons packages and their lib-observability replacements:

| Deprecated lib-commons import | lib-observability replacement | Package name change? |
|---|---|---|
| `lib-commons/v5/commons/log` | `lib-observability/log` | No — qualifier stays `log` |
| `lib-commons/v5/commons/zap` | `lib-observability/zap` | No — qualifier stays `zap` |
| `lib-commons/v5/commons/runtime` | `lib-observability/runtime` | No — qualifier stays `runtime` |
| `lib-commons/v5/commons/assert` | `lib-observability/assert` | No — qualifier stays `assert` |
| `lib-commons/v5/commons/opentelemetry/metrics` | `lib-observability/metrics` | No — qualifier stays `metrics` |
| `lib-commons/v5/commons/opentelemetry/constants` | `lib-observability/constants` | No — qualifier stays `constants` |
| `lib-commons/v5/commons/opentelemetry/redaction` | `lib-observability/redaction` | No — qualifier stays `redaction` |

> All 7 replacements share the same package qualifier — import path changes only,
> no call-site renames needed in the file body.

### Do NOT migrate (not deprecated — stays lib-commons)

| Import | Reason |
|---|---|
| `lib-commons/v5/commons/opentelemetry` | Tracing bootstrap — NOT deprecated. API diverges from lib-observability/tracing (security-tier enforcement). See Manual Migration note below. |
| `lib-commons/v5/commons/net/http` | HTTP helpers — NOT deprecated. Telemetry middleware stays lib-commons. |
| `lib-commons/v5/commons/streaming` | Kafka/CloudEvents producer — NOT deprecated; uses lib-observability internally |
| `lib-commons/v5/commons/postgres`, `mongo`, `redis`, `rabbitmq` | Infrastructure clients — NOT deprecated |
| `lib-commons/v5/commons/multitenancy` | Multi-tenant dispatch — NOT deprecated |
| `lib-commons/v5/commons/systemplane` | Runtime config client — NOT deprecated |
| `lib-commons/v5/commons` | Root package (AppConfig, etc.) — NOT deprecated |

### Manual Migration Only (out of scope for this skill)

| Import | Target | Why manual |
|---|---|---|
| `lib-commons/v5/commons/opentelemetry` | `lib-observability/tracing` | Package qualifier changes (`opentelemetry.X` → `tracing.X`). Constructor API differs — lib-commons enforces `EffectiveSecurityTier`; lib-observability uses a simpler env-var check. Requires human review per call site. |

---

## Step 1: Validate Input

<verify_before_proceed>
- repo_path exists and contains a go.mod file
- go.mod declares module path (to identify lib-commons import prefix)
- lib-commons version in go.mod includes the delegation shims (HARD GATE — see below)
</verify_before_proceed>

```text
1. Verify repo_path/go.mod exists
2. Extract module name from go.mod
3. Confirm lib-commons is a dependency: grep "lib-commons" go.mod
   if not found → report "No lib-commons dependency found. Nothing to migrate." and exit PASS
4. HARD GATE — Verify lib-commons has the delegation shims:

   Resolve the effective lib-commons module directory (honours replace directives, workspaces, and custom GOMODCACHE):

   LIB_COMMONS_DIR=$(go list -m -f '{{.Dir}}' github.com/LerianStudio/lib-commons/v5)
   DOC_PATH="${LIB_COMMONS_DIR}/commons/log/doc.go"

   Guard — abort immediately if the file cannot be located:
     if [ ! -f "$DOC_PATH" ]; then
       echo "HARD BLOCK: unable to locate $DOC_PATH for github.com/LerianStudio/lib-commons/v5"
       exit 2
     fi

   Check if doc.go contains "Deprecated":
     grep -c "Deprecated" "$DOC_PATH"

   If result is 0 → STOP. Report:
     "HARD BLOCK: lib-commons does not include the
      delegation shims. commons/log.Logger is still the original type, not a
      type alias to lib-observability/log.Logger. Migrating import paths would
      cause type incompatibility at every lib-commons boundary that returns or
      accepts Logger (e.g. commons.NewTrackingFromContext, mongo.Config.Logger).

      Required action: Upgrade lib-commons to a version that includes the
      delegation shims (the refactor/lib-observability-integration PR or later).
      Once lib-commons is released with the shims, re-run this skill."
     Exit PARTIAL.

   If result is ≥ 1 → shims are present, proceed.
```

---

## Step 2: Discover Deprecated Observability Imports

Scan all `.go` files in the repository for the 7 deprecated import paths.

```text
Search patterns (grep -r across *.go files):

MIGRATE targets (all deprecated in lib-commons):
  lib-commons/v5/commons/log"
  lib-commons/v5/commons/zap"
  lib-commons/v5/commons/runtime"
  lib-commons/v5/commons/assert"
  lib-commons/v5/commons/opentelemetry/metrics"
  lib-commons/v5/commons/opentelemetry/constants"
  lib-commons/v5/commons/opentelemetry/redaction"

DO NOT MIGRATE targets (not deprecated — skip silently):
  lib-commons/v5/commons/opentelemetry"   ← root tracing bootstrap, NOT deprecated
  lib-commons/v5/commons/net/http"        ← HTTP helpers, NOT deprecated
  lib-commons/v5/commons/streaming"
  lib-commons/v5/commons/postgres"
  lib-commons/v5/commons/mongo"
  lib-commons/v5/commons/redis"
  lib-commons/v5/commons/rabbitmq"
  lib-commons/v5/commons/multitenancy"
  lib-commons/v5/commons/systemplane"
  lib-commons/v5/commons"                 ← root package

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
| cmd/main.go | 5 | lib-commons/v5/commons/log | lib-observability/log |
| ...

### Imports NOT Migrated (not deprecated — kept in lib-commons)
| File | Line | Import | Reason |
|------|------|--------|--------|
| ...

### Summary
- Total files to change: X
- Total imports to replace: Y
- Total imports left as-is: Z
```

---

## Step 3: Present Migration Plan and Confirm

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

## Step 4: Add lib-observability to go.mod

```bash
# lib-observability may not yet be indexed by the Go sum database.
# Always use GONOSUMDB + GOPRIVATE to avoid sum.golang.org 404 errors.
GONOSUMDB="github.com/LerianStudio/lib-observability" \
GOPRIVATE="github.com/LerianStudio/lib-observability" \
  go get github.com/LerianStudio/lib-observability@{lib_observability_version}
```

Verify it appears in go.mod as a direct dependency:
```bash
grep "lib-observability" go.mod
# Expected: github.com/LerianStudio/lib-observability v... (no "// indirect")
```

---

## Step 5: Apply Import Replacements

<dispatch_required agent="ring:backend-engineer-golang">
For each file identified in Step 2:
1. Read the file
2. Replace the import path using the mapping table
3. Preserve any import aliases the file was using
4. No package qualifier changes needed — all 7 replacements keep the same qualifier
   (log stays log, assert stays assert, metrics stays metrics, etc.)
5. Write the updated file
</dispatch_required>

---

## Step 6: Run go mod tidy

```bash
GONOSUMDB="github.com/LerianStudio/lib-observability" \
GOPRIVATE="github.com/LerianStudio/lib-observability" \
  go mod tidy
```

Check that lib-commons is still present if the app uses non-deprecated packages:
```bash
grep "lib-commons" go.mod
# Should still be there unless the app used ONLY deprecated packages
```

---

## Step 7: Validate Build and Tests

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
2. Since all 7 packages share the same qualifier, errors are unlikely — check for:
   - Indirect API differences between shim and lib-observability (rare)
   - Missing symbols that the shim exposed but lib-observability doesn't (report as PARTIAL)
3. Fix compilation errors
4. Re-run until `go build ./...` passes

---

## Step 8: Verify No Remaining Deprecated Imports

```bash
grep -r "lib-commons/v5/commons/log\"" . --include="*.go" | wc -l
grep -r "lib-commons/v5/commons/zap\"" . --include="*.go" | wc -l
grep -r "lib-commons/v5/commons/runtime\"" . --include="*.go" | wc -l
grep -r "lib-commons/v5/commons/assert\"" . --include="*.go" | wc -l
grep -r "lib-commons/v5/commons/opentelemetry/metrics\"" . --include="*.go" | wc -l
grep -r "lib-commons/v5/commons/opentelemetry/constants\"" . --include="*.go" | wc -l
grep -r "lib-commons/v5/commons/opentelemetry/redaction\"" . --include="*.go" | wc -l
```

Each should print `0`.

---

## Step 9: Produce Final Report

```markdown
## Changes Applied

| File | Imports Replaced | Before | After |
|------|-----------------|--------|-------|
| cmd/main.go | 2 | commons/log, commons/runtime | log, runtime |
| ...

## Validation

| Check | Result |
|-------|--------|
| go build ./... | ✅ PASS |
| go vet ./... | ✅ PASS |
| go test ./... | ✅ PASS |
| No remaining deprecated lib-commons observability imports | ✅ PASS |

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
| HIGH | Symbol exists in lib-commons shim but is missing from lib-observability |
| MEDIUM | Import alias collision requiring manual rename |
| LOW | Unused import warning after migration |

---

## Pressure Resistance

| Pushback | Response |
|---|---|
| "Only one or two deprecated imports, not worth the effort" | Deprecation warnings accumulate. Migrate now to keep the codebase clean. |
| "lib-commons shims still work, why change?" | They are deprecated and will be removed in a future major. Earlier migration = smaller blast radius. |
| "commons/opentelemetry is similar, migrate it too" | It is NOT deprecated in lib-commons. Its API diverges from lib-observability/tracing. Manual migration only — this skill does not touch it. |
| "What about commons/net/http telemetry middleware?" | Not deprecated in lib-commons. Out of scope for this skill. |
| "Streaming imports will break" | commons/streaming is NOT deprecated. The skill explicitly skips it. |
| "The import paths are simple, just replace them regardless of lib-commons version" | CANNOT proceed. Without the delegation shims, commons/log.Logger and lib-observability/log.Logger are distinct named types. Every lib-commons boundary (NewTrackingFromContext, mongo.Config.Logger, etc.) returns the old type. Replacing import paths causes compile errors at all those boundaries. Upgrade lib-commons first. |

---

## Anti-Rationalization Table

| Rationalization | Why Wrong | Action |
|---|---|---|
| "I'll also replace commons/opentelemetry while I'm at it" | It is NOT deprecated. Its constructor differs from lib-observability/tracing. This is a manual migration requiring human review. | Stop. Only migrate the 7 deprecated packages. |
| "The tests pass even with mixed imports" | Deprecated imports create future breakage risk | Replace all 7 deprecated packages |
| "I'll do it later when lib-commons removes the shims" | Migration is harder with more files in flight | Migrate now, reduce future blast radius |
| "go get failed — the module must not be public" | The sum DB may not have indexed it yet | Use GONOSUMDB + GOPRIVATE as shown in Step 4 |
| "The import path is the only thing changing, types are compatible" | Only true AFTER lib-commons is on a version with delegation shims. On older versions, commons/log.Logger and lib-observability/log.Logger are distinct named types — not aliases. Compile errors will occur at every lib-commons boundary. | STOP. Run the Step 1 shim check first. If it fails, upgrade lib-commons before running this skill. |
| "I can fix the type errors after replacing imports" | The type errors are systemic — they appear at every lib-commons API boundary (NewTrackingFromContext, infrastructure Config.Logger fields, etc.). Fixing them ad-hoc defeats the purpose of the skill and risks introducing bugs. | STOP. Upgrade lib-commons to a shim-enabled version first. |
