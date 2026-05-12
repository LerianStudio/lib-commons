# Design: `lib-commons/cmd/telemetry-inventory`

**Date:** 2026-05-07
**Status:** Approved (brainstorm-validated, ready for `ring:write-plan`)
**Origin:** Counterpart to `ring:creating-grafana-dashboards` skill in [ring repo]

---

## 1. Context & Motivation

The `ring:creating-grafana-dashboards` skill (in `LerianStudio/ring`, `pm-team/skills/`) orchestrates an 8-gate workflow for Lerian PM teams to produce Grafana dashboards grounded in real telemetry shipped by Go services. Its Phase 1 (Sweep) inventories every metric, span, and structured log emission, producing a canonical `docs/dashboards/telemetry-dictionary.md` artifact. Its Gate 7 installs a **blocking CI gate** that fails any PR which introduces drift between the committed dictionary and the telemetry actually emitted by code.

The blocking gate is the design's load-bearing column. Without enforcement, the dictionary rots within sprints. **The CI gate cannot invoke the skill** — skills run inside the Claude harness; CI runs in GitHub Actions standalone. CI needs a **deterministic, fast, headless** binary that performs Gates 0-3 of the skill (recon → sweep → assembly → render) without LLM involvement.

This document specifies that binary: **`lib-commons/cmd/telemetry-inventory`**.

---

## 2. Goals & Non-Goals

### Goals

1. **Phase 1-3 of the skill, headless.** Walk a target Go service, detect every telemetry primitive, render a deterministic Markdown dictionary.
2. **Drift detection.** `verify` subcommand regenerates and byte-diffs against a committed dictionary; exits non-zero on drift.
3. **Determinism.** Same code → byte-identical dictionary across runs and machines.
4. **Performance.** <10s for a medium service (~500 Go files); <5s typical for `make telemetry-dictionary` local DX.
5. **Coverage.** Detect all three telemetry tiers (see §3) plus spans, log fields, cross-cutting consistency, framework instrumentation. Seven angles total, mirroring `sweep-angles.md`.
6. **Reusability.** Binary distributable via `go install github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory@latest` and via goreleaser-built release artifacts.

### Non-Goals

1. **Dashboard generation.** Phases 4-6 of the skill (theme proposal, PM iteration, Grafonnet authoring) remain LLM-driven. CLI does not opine on dashboards.
2. **Semantic analysis of metric quality.** "Is this metric well-named?" / "Should this be a counter or histogram?" are PM concerns.
3. **Runtime telemetry observation.** This is a static analyzer over source code, not a runtime collector.
4. **Multi-language support.** Go only. TypeScript services (if any emit telemetry) are out of scope for V1.

---

## 3. Three-Tier Telemetry Landscape

Recon of `lib-commons/commons/opentelemetry/` revealed that Lerian services emit telemetry through **three distinct layers**, all of which must be detected:

| Tier | Pattern | Where the metric name lives |
|------|---------|-----------------------------|
| **Tier 1** — OTel SDK direct | `meter.Int64Counter("ledger_tx_total", metric.WithDescription("..."))` | String literal at constructor call site |
| **Tier 2** — `MetricsFactory` builder | `factory.Counter(metrics.Metric{Name: "ledger_tx_total", ...})` | Struct literal in argument; resolved via `go/types` to confirm `(*MetricsFactory).Counter` |
| **Tier 3** — Domain helper | `factory.RecordAccountCreated(ctx, attrs...)` | **Inside lib-commons**, not at the call site. Resolved via the `metrics.Helpers` registry (see §11) |

The CLI must handle all three. Treating only Tier 1 (the original skill's assumption) would miss services that follow lib-commons recommended patterns — i.e., most services.

Same three-tier distinction applies semantically to Histograms and Gauges. Spans/logs are simpler — only the OTel-direct surface (no MetricsFactory equivalent for those today).

---

## 4. Architecture: `go/analysis` Framework

The CLI uses **`golang.org/x/tools/go/analysis`** — the same framework that powers `go vet`, `golangci-lint`, and `staticcheck`.

### Why this framework

1. **Cross-cutting (Angle 6) becomes trivial.** The cross-cutting analyzer declares `Requires: [Counter, Histogram, Gauge, Span, LogField]`. The framework guarantees those analyzers' results are available in `pass.ResultOf[A]` when the cross-cutting analyzer runs. Zero shared mutable state, zero coordination code.
2. **Test isolation is a solved problem.** `analysistest.Run` is the gold standard for testing static analyzers in Go. Fixtures use inline `// want "..."` directives.
3. **Inspector pattern shares AST traversal.** `golang.org/x/tools/go/ast/inspector` is registered as a dependency once; subsequent analyzers reuse the cached traversal. We get single-walk performance without single-visitor coupling.
4. **Standard pattern.** Any Go engineer joining lib-commons later recognizes the structure.

### Why a custom orchestrator instead of `singlechecker.Main`

The framework's default entry points (`singlechecker.Main`, `multichecker.Main`) report findings as **diagnostics** — formatted text suitable for terminal output (line/column references, `// want` matching). We need **typed `Result` objects** to feed a renderer. We use the `analysis.Pass.ResultOf` mechanism with a custom checker (~80 lines) that mimics `multichecker` but captures Results.

This pattern is documented in `golang.org/x/tools/go/analysis/internal/checker` and is what `gopls` and similar tools do.

---

## 5. Module Layout

```
lib-commons/
├── cmd/
│   └── telemetry-inventory/
│       ├── main.go                          # CLI entry, cobra-shaped subcommands
│       ├── internal/
│       │   ├── analyzers/
│       │   │   ├── counter.go               # CounterAnalyzer (Tier 1+2+3 unified)
│       │   │   ├── counter_test.go
│       │   │   ├── histogram.go             # HistogramAnalyzer
│       │   │   ├── histogram_test.go
│       │   │   ├── gauge.go                 # GaugeAnalyzer
│       │   │   ├── gauge_test.go
│       │   │   ├── span.go                  # SpanAnalyzer (also detects unbounded_span)
│       │   │   ├── span_test.go
│       │   │   ├── logfield.go              # LogFieldAnalyzer
│       │   │   ├── logfield_test.go
│       │   │   ├── crosscut.go              # CrossCutAnalyzer (Requires: 5 above)
│       │   │   ├── crosscut_test.go
│       │   │   ├── framework.go             # FrameworkAnalyzer (HTTP/gRPC/AMQP middleware)
│       │   │   └── framework_test.go
│       │   ├── orchestrator/
│       │   │   ├── orchestrator.go          # packages.Load + checker + result aggregation
│       │   │   └── orchestrator_test.go
│       │   ├── renderer/
│       │   │   ├── markdown.go              # Deterministic Markdown writer
│       │   │   ├── markdown_test.go         # Idempotency + golden tests
│       │   │   ├── json.go                  # JSON intermediate writer
│       │   │   └── yaml_block.go            # Sorted YAML block helper
│       │   ├── schema/
│       │   │   ├── dictionary.go            # Versioned Dictionary types
│       │   │   ├── primitives.go            # CounterPrimitive, HistogramPrimitive, etc.
│       │   │   └── version.go               # SchemaVersion = "1.0.0"
│       │   └── verify/
│       │       └── verify.go                # `verify` subcommand: regen + diff
│       └── testdata/
│           ├── fixtures/                    # analysistest fixtures (per-angle minimal pkgs)
│           │   ├── counter-tier1/
│           │   ├── counter-tier2/
│           │   ├── counter-tier3/
│           │   ├── ... (similar for histogram, gauge, span, logfield, crosscut, framework)
│           ├── services/                    # End-to-end fixture services (full Go modules)
│           │   ├── pure-tier1/
│           │   ├── pure-tier2/
│           │   ├── mixed-realistic/
│           │   ├── broken-crosscut/
│           │   └── leak-spans/
│           └── golden/                      # Expected dictionary outputs
│               ├── pure-tier1.md
│               ├── pure-tier2.md
│               └── ...
└── commons/opentelemetry/metrics/
    ├── registry.go                          # NEW (pre-work PR)
    └── registry_test.go                     # NEW (pre-work PR)
```

---

## 6. Per-Angle Analyzer Specs

Each analyzer respects the `*analysis.Analyzer` contract:

```go
var CounterAnalyzer = &analysis.Analyzer{
    Name:       "counter",
    Doc:        "Detects counter metric primitives across Tier 1, 2, 3.",
    Run:        runCounter,
    Requires:   []*analysis.Analyzer{inspect.Analyzer},
    ResultType: reflect.TypeOf((*Findings)(nil)),
}
```

Findings types live in `internal/analyzers/findings.go`:

```go
type Findings struct {
    Primitives []schema.Primitive  // generic over CounterPrimitive | HistogramPrimitive | ...
}
```

### Per-tier detection inside CounterAnalyzer (illustrative)

```go
func runCounter(pass *analysis.Pass) (any, error) {
    insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
    findings := &Findings{}

    insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
        call := n.(*ast.CallExpr)
        if p := matchTier1Counter(pass, call); p != nil {        // meter.Int64Counter("name")
            findings.Primitives = append(findings.Primitives, *p)
            return
        }
        if p := matchTier2Counter(pass, call); p != nil {        // factory.Counter(Metric{Name: "..."})
            findings.Primitives = append(findings.Primitives, *p)
            return
        }
        if p := matchTier3Counter(pass, call); p != nil {        // factory.RecordX(...) ∈ Helpers
            findings.Primitives = append(findings.Primitives, *p)
        }
    })

    return findings, nil
}
```

Each `matchTierN` uses `pass.TypesInfo.Selections[selExpr]` to resolve method targets (`*MetricsFactory`-bound vs OTel SDK `Meter`-bound vs user-defined). Type-aware matching = robust against false positives.

### Other analyzers

- **HistogramAnalyzer / GaugeAnalyzer**: same pattern, different signatures
- **SpanAnalyzer**: `tracer.Start` detection + paired `defer span.End()` check via block walk; flags `unbounded_span: true` when Start has no matching End in the same function scope
- **LogFieldAnalyzer**: walks `(log.Logger).With/.Info/.Warn/.Error/.Debug` chains, extracts field names from `zap.String/Int/Bool/Any/...` arguments, computes per-field level distribution + PII risk substring check
- **FrameworkAnalyzer**: detects `otelfiber.Middleware`, `otelhttp.NewHandler`, `otelgrpc.UnaryServerInterceptor`, lib-commons RabbitMQ consumer wrappers, `otelsql`/pgx OTel tracer wiring; outputs auto-emitted metric/span names so downstream analyzers can flag `redundant_with_framework: true` when manual instrumentation duplicates auto

---

## 7. Cross-Cutting Algorithm (Angle 6)

The most algorithmically interesting analyzer. Declares dependencies on all five primitive analyzers; framework guarantees results available before run.

### Algorithm: function-scoped bundling

```go
func runCrossCut(pass *analysis.Pass) (any, error) {
    counters  := pass.ResultOf[CounterAnalyzer].(*Findings)
    histograms := pass.ResultOf[HistogramAnalyzer].(*Findings)
    gauges    := pass.ResultOf[GaugeAnalyzer].(*Findings)
    spans     := pass.ResultOf[SpanAnalyzer].(*Findings)
    logFields := pass.ResultOf[LogFieldAnalyzer].(*Findings)

    // Bundle every primitive emission_site by its enclosing FuncDecl.
    // astutil.PathEnclosingInterval gives O(log n) lookup of enclosing function
    // for any (file, line) — and emission_sites already carry file:line.
    bundles := groupByEnclosingFunction(pass, counters, histograms, gauges, spans, logFields)

    findings := &CrossCutFindings{}
    for fnKey, bundle := range bundles {
        checkTenantConsistency(fnKey, bundle, findings)
        checkErrorAttribution(fnKey, bundle, findings)
        checkTraceCorrelation(fnKey, bundle, findings)
    }

    return findings, nil
}
```

### Three concrete checks

| Check | Rule | Output |
|-------|------|--------|
| `checkTenantConsistency` | If a function emits a metric labeled `tenant_id`, every span/log emission in the same function must carry `tenant_id` too | Per-site inconsistency record (file:line, missing primitive type) |
| `checkErrorAttribution` | For each `if err != nil { ... }` block within a function: presence of (a) `span.RecordError(err)`, (b) `span.SetStatus(codes.Error, ...)`, (c) `logger.Error(...)`, (d) counter increment with `result="error"` label | Classify each error site as complete / partial (with missing list) / no_attribution |
| `checkTraceCorrelation` | Each log emission and each histogram `Record` site has an active span context in the enclosing function chain (i.e., a `tracer.Start` exists in scope) | Coverage % overall, list of broken propagation sites |

### Closure handling (known limitation)

Closures (`go func() { logger.Info(...) }()`) have their emission_site inside the `*ast.FuncLit`, but bundling assigns them to the enclosing `*ast.FuncDecl`. This is correct for tenant consistency (closure inherits scope's `tenant_id`) but may produce false positives for trace correlation when the closure runs detached (post-function-return). Documented as known limitation; revisit if false-positive volume warrants.

---

## 8. Renderer Determinism

**Bug class avoided:** non-deterministic output that passes locally and fails intermittently in CI.

### Builder, not template

`text/template` over Go maps iterates in random order. Manual builder with explicit sort hooks at every emission point:

```go
type Renderer struct {
    sb *strings.Builder
}

func (r *Renderer) writeCounter(c schema.CounterPrimitive) {
    r.writeHeading("### " + c.Name)
    r.writeYAMLBlock(map[string]any{
        "description":                c.Description,
        "emission_sites":             sortSites(c.EmissionSites),
        "instrument_type":            c.InstrumentType,
        "label_cardinality_estimate": c.LabelCardinality,
        "labels":                     sortStrings(c.Labels),
        "tenant_scoped":              c.TenantScoped,
        "unit":                       c.Unit,
    })
}
```

`writeYAMLBlock` sorts keys alphabetically before emit. `sortStrings`, `sortSites` use `sort.SliceStable` (not `sort.Slice` — instability is a non-determinism source).

### Determinism guards

| Source | Mitigation |
|--------|-----------|
| Map iteration order | `writeYAMLBlock` sorts keys before emit |
| `sort.Slice` instability | Use `sort.SliceStable` everywhere |
| Float formatting (`1.0` vs `1`) | `strconv.FormatFloat(v, 'g', -1, 64)` — minimal canonical |
| Path separators (cross-platform) | `filepath.ToSlash` on all emission_site paths |
| Line endings | Force `\n`; `strings.Builder` doesn't introduce CRLF |
| Trailing whitespace | Final pass: regex strip ` +\n` → `\n` |
| Timestamp non-determinism | `_meta.generated_at` is the ONLY non-deterministic field; verify CI redacts it before diff (already in `ci-drift-check.md`) |

### Tests

- **Idempotency**: render same Dictionary twice, byte-compare. Catches any non-determinism source. **More valuable than golden test** because it catches the dangerous bug class (intermittent CI failure).
- **Golden**: render against committed expected output. Catches semantic drift.
- **Update mechanism**: `flag.Bool("update-golden", ...)` + `make update-goldens` Makefile target. **No env vars** — env-driven test behavior hides state.

---

## 9. Testing Strategy

Three layers of confidence.

### Layer 1: analyzer-level via `analysistest`

Standard pattern:

```go
func TestCounterAnalyzer(t *testing.T) {
    testdata := analysistest.TestData()
    analysistest.Run(t, testdata, analyzers.CounterAnalyzer,
        "./fixtures/counter-tier1",
        "./fixtures/counter-tier2",
        "./fixtures/counter-tier3",
    )
}
```

Fixtures use inline `// want "regex"` directives:

```go
// testdata/fixtures/counter-tier1/main.go
counter, _ := meter.Int64Counter("ledger_transactions_total")  // want `counter "ledger_transactions_total" tier=1`
counter.Add(ctx, 1, attribute.String("tenant_id", tid))         // want `counter increment site labels=\[tenant_id\]`
```

`analysistest.Run` loads the fixture as a synthetic Go package, runs the analyzer, validates that `// want` patterns match emitted diagnostics. Because we use `Result` channels rather than diagnostics for the dictionary, we'll emit **dual-output** during test mode: diagnostics for `// want` matching + Results for orchestrator. This is a small wrapper around the analyzer; documented pattern.

### Layer 2: renderer determinism

Already covered §8.

### Layer 3: end-to-end fixture services

```
testdata/services/
├── pure-tier1/         # Only OTel SDK direct calls — exercises Tier 1 in isolation
├── pure-tier2/         # Only MetricsFactory builders — exercises Tier 2 in isolation
├── mixed-realistic/    # Tier 1+2+3 + framework instrumentation + good cross-cutting — production-shaped
├── broken-crosscut/    # Intentional inconsistencies (metric labels tenant_id but span doesn't) — verifies Angle 6 detection
└── leak-spans/         # tracer.Start without defer span.End() — verifies Angle 4 unbounded_span detection
```

Each service is a real Go module (own `go.mod`), imports lib-commons at a pinned version. E2E test:

```go
func TestE2E_MixedRealistic(t *testing.T) {
    dict, err := orchestrator.Inventory("./testdata/services/mixed-realistic")
    require.NoError(t, err)

    rendered := renderer.Markdown(dict)
    expected := loadGolden(t, "./testdata/golden/mixed-realistic.md")

    assert.Equal(t, expected, rendered)
}
```

Fixtures exist at module level (separate `go.mod` per fixture) so they don't pollute the CLI's own dependency graph and so they can pin specific lib-commons versions for compatibility testing.

---

## 10. CLI Surface

Single binary, two subcommands. Cobra or urfave/cli — implementation detail.

```
telemetry-inventory inventory [flags] [target]
  Sweep + render dictionary. Used locally and in CI regen step.
  Flags:
    --output-md PATH       Write Markdown dictionary
                           (default: ./docs/dashboards/telemetry-dictionary.md)
    --output-json PATH     Also write JSON intermediate (default: skip, JSON not written)
    --schema-version V     Pin schema version (default: latest, currently 1.0.0)
    --json                 Emit JSON to stdout instead of writing files
                           (for CI pipelines that want to consume programmatically)

telemetry-inventory verify [flags] [target]
  Regen + byte-diff against committed dictionary. Exits 1 on drift.
  Used by CI gate (.github/workflows/telemetry-drift.yml).
  Flags:
    --committed PATH       Path to committed dictionary
                           (default: ./docs/dashboards/telemetry-dictionary.md)
    --quiet                Suppress diff output (CI exits with code only)
```

`[target]` is the directory rooted at a `go.mod` file. Defaults to CWD if omitted.

### Why two subcommands instead of `inventory --check-drift`

1. **Different intent.** `inventory` writes; `verify` asserts. Conflating them invites mistakes (running with `--check-drift` thinking it'll write fails silently).
2. **Different exit codes.** `verify` exit 1 = drift; `inventory` exit 1 = parse error. Conflated, the meaning of exit 1 becomes ambiguous.
3. **Cleaner flag sets.** `inventory` flags pertain to output destinations; `verify` flags pertain to comparison.

### No env vars

Per the no-env-vars rule, all behavior toggles are flags. `--debug`, `--verbose` exist as standard flags. No `TELEMETRY_INVENTORY_DEBUG=1` style switches.

---

## 11. Schema & Versioning

### Schema location

`internal/schema/dictionary.go` defines the canonical types. `version.go` declares:

```go
const SchemaVersion = "1.0.0"
```

Bumped only when Dictionary shape changes. Independent of lib-commons module version (v5.x) — they evolve on different cadences.

### Drift CI version handling

The committed dictionary's `_meta.schema_version` is compared against the CLI's `SchemaVersion` constant at `verify` time. Mismatch surfaces "regenerate against current CLI version" instead of crashing on schema interpretation.

### lib-commons release coupling

CLI lives at `cmd/telemetry-inventory` of `github.com/LerianStudio/lib-commons/v5`. When lib-commons cuts a release (v5.6 → v5.7), the CLI binary in that release embeds the corresponding `metrics.Helpers` registry. Services pinned to v5.6 use v5.6's CLI; v5.7's new helpers don't appear until the service upgrades.

This is the **release coupling** that makes the registry approach (§12) sustainable.

---

## 12. Pre-Work: lib-commons Registry Seed

**Separate PR before CLI implementation begins.** ~1 day of `ring:backend-engineer-golang` work.

### Files

**`commons/opentelemetry/metrics/registry.go`** (new):

```go
package metrics

type MetricType int

const (
    Counter MetricType = iota
    Histogram
    Gauge
)

type HelperSpec struct {
    GoFunctionName string
    MetricName     string
    InstrumentType MetricType
    Unit           string
    Description    string
    DefaultLabels  []string
}

// Helpers is the canonical registry of MetricsFactory.Record* helpers.
// Adding a Record* method to MetricsFactory MUST add an entry here.
// Enforced by TestHelpersMatchRegistry.
var Helpers = []HelperSpec{
    // Seed entries — exhaustive coverage of currently-existing helpers.
    // To be populated from inspection of:
    //   - account.go            (RecordAccountCreated, ...)
    //   - system.go             (RecordSystemCPUUsage, ...)
    //   - transaction.go        (RecordTransactionPosted?, ...)
    //   - operation_routes.go
    //   - transaction_routes.go
}
```

**`commons/opentelemetry/metrics/registry_test.go`** (new):

```go
package metrics_test

import (
    "reflect"
    "strings"
    "testing"

    "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// TestHelpersMatchRegistry enforces bidirectional consistency between
// MetricsFactory.Record* methods and the metrics.Helpers registry.
//
// Failure modes:
//   - Method exists but no registry entry → CLI cannot detect Tier 3 emission
//   - Registry entry exists but method removed → CLI looks for nonexistent method
//
// Both are caught at lib-commons CI time, before release.
func TestHelpersMatchRegistry(t *testing.T) {
    factoryType := reflect.TypeOf((*metrics.MetricsFactory)(nil))

    methodsInCode := map[string]bool{}
    for i := 0; i < factoryType.NumMethod(); i++ {
        name := factoryType.Method(i).Name
        if strings.HasPrefix(name, "Record") {
            methodsInCode[name] = true
        }
    }

    methodsInRegistry := map[string]bool{}
    for _, h := range metrics.Helpers {
        methodsInRegistry[h.GoFunctionName] = true
    }

    for name := range methodsInCode {
        if !methodsInRegistry[name] {
            t.Errorf("MetricsFactory.%s exists but is not in metrics.Helpers — add an entry to registry.go", name)
        }
    }
    for name := range methodsInRegistry {
        if !methodsInCode[name] {
            t.Errorf("metrics.Helpers references %s but no method exists on MetricsFactory — remove from registry.go", name)
        }
    }
}
```

### Why this enforcement matters

Without `TestHelpersMatchRegistry`, the registry degrades to "another map someone forgets to update." With it, lib-commons CI blocks merge when `MetricsFactory` methods and `Helpers` diverge. **This test is what makes the registry approach sustainable for years**, not for one quarter.

---

## 13. Performance & DX Budget

### Performance

| Workload | Target | Approach |
|----------|--------|----------|
| Type-checked package load | <5s for ~500 files | `packages.Load` is single-threaded by design (type-checker); accept this |
| Analyzer execution (7 analyzers) | <3s | Goroutine-per-analyzer; framework resolves Requires ordering |
| Render Markdown | <500ms | `strings.Builder` is fast; sorts dominate |
| Total `inventory` command | <10s typical, <30s worst case | Sum of above |
| Total `verify` command | <12s | inventory + diff |
| Local `make telemetry-dictionary` | <5s | Smaller services; same analyzers |

### DX

- `make telemetry-dictionary` Makefile target invokes `telemetry-inventory inventory .`
- `make verify-telemetry` invokes `telemetry-inventory verify .`
- `make update-goldens` (in CLI's own test suite) invokes `go test ./... -update-golden`
- Failure messages on `verify` include actionable next step: "run `make telemetry-dictionary` and stage the result"

---

## 14. Sequencing & Open Questions

### Sequencing for `ring:write-plan`

Suggested phasing in the implementation plan:

1. **Pre-work PR**: lib-commons registry seed (registry.go + registry_test.go) — merged independently
2. **CLI scaffolding**: `cmd/telemetry-inventory/main.go`, schema types, orchestrator skeleton, no analyzers yet
3. **Analyzer 1: CounterAnalyzer** (Tier 1 only) — first end-to-end vertical slice; exercises framework setup, fixtures, golden tests
4. **Analyzer 1.5: CounterAnalyzer Tier 2 + Tier 3** — adds layered detection on top of Tier 1
5. **Analyzers 2-3: Histogram, Gauge** — pattern-replicate from Counter
6. **Analyzers 4-5: Span, LogField** — different shape from metrics; spans need block walk for End() pairing
7. **Analyzer 7: Framework** — detection of auto-instrumentation
8. **Analyzer 6: CrossCut** — depends on 1-5 + 7; lands last among analyzers
9. **Renderer Markdown + JSON** — consumes orchestrator output
10. **Verify subcommand** — diff logic, exit codes, CI integration documentation
11. **End-to-end fixture services + golden tests** — validates full pipeline
12. **Goreleaser config update + first release**

Slice 3 ships value early: a working CLI that detects only counters via Tier 1 is enough to demonstrate the pipeline end-to-end and validate the architecture before the full surface area lands.

### Open questions for `ring:write-plan` to resolve

1. **Cobra vs urfave/cli vs stdlib `flag`** — implementation detail; preference for what lib-commons's existing CLI tools (if any) use. Investigate before plan.
2. **Goreleaser flip from `builds: skip: true` to actual build** — exact yaml change, multi-platform matrix (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64), checksum strategy.
3. **Module-mode for fixtures** — fixtures-as-modules require `go.mod` per fixture; should the test runner use `GOFLAGS=-mod=mod` or `replace` directives to avoid network fetches?
4. **Concurrency cap for analyzer goroutines** — 7 analyzers in parallel is fine; if memory pressure on large services, may need bounded pool. Profile during slice 3-4.

These don't block the plan — they get answered during implementation slices.

---

## 15. Decisions Log

| Decision | Resolution | Rationale |
|----------|-----------|-----------|
| Home | `lib-commons/cmd/telemetry-inventory` + goreleaser update | Co-located with the primitives it parses; release cadence aligns naturally |
| Scope V1 | Full 7 angles + tests | Cold-start with no installed base; partial coverage would surface visible gaps for first users |
| Architecture | `golang.org/x/tools/go/analysis` framework | Cross-cutting via `Requires` declarative; community-standard; analysistest gold standard |
| Parser | `golang.org/x/tools/go/packages` + `go/types` | Mandatory for Tier 2 type resolution (`(*MetricsFactory).Counter` vs user-defined methods) |
| Tier 3 detection | Self-describing registry in lib-commons + bidirectional reflection test | Single source of truth; compiler-enforced; degrades gracefully if test enforcement holds |
| Cross-cutting algorithm | Function-scoped bundling via `astutil.PathEnclosingInterval` | O(log n) enclosing-function lookup; framework guarantees Requires ordering |
| Renderer | Manual builder + sort hooks; idempotency tests + golden tests | `text/template` map iteration is random order — fragile for byte-stable output |
| CLI surface | `inventory` + `verify` subcommands | Different intent → different commands; cleaner exit codes and flag sets |
| Behavior toggles | Flags only (`--update-golden`, `--debug`, `--verbose`) | Env vars hide state; explicit flags discoverable via `--help` |
| Schema versioning | Independent of lib-commons module version; bumped only on Dictionary shape change | Decoupled cadence; `verify` checks version match before semantic comparison |
| Pre-work | Separate lib-commons PR for `metrics.Helpers` registry + test | ~1 day of work; unblocks CLI and provides standalone value (auto-described lib-commons) |
| Closure handling in cross-cutting | Bundle FuncLit emissions into enclosing FuncDecl | Correct for tenant consistency; potential FP for trace correlation in detached closures — accepted as known limitation |

---

## Appendix A: Cross-references

- **Origin skill:** `LerianStudio/ring:pm-team/skills/creating-grafana-dashboards/SKILL.md`
- **Sweep angle specs (input contract):** `LerianStudio/ring:pm-team/skills/creating-grafana-dashboards/sub-files/sweep-angles.md`
- **Dictionary schema (output contract):** `LerianStudio/ring:pm-team/skills/creating-grafana-dashboards/sub-files/dictionary-schema.md`
- **CI drift gate spec (consumer):** `LerianStudio/ring:pm-team/skills/creating-grafana-dashboards/sub-files/ci-drift-check.md`
