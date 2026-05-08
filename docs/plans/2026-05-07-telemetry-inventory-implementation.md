# Plan: Telemetry Inventory CLI Implementation

## 1. Header

**Goal:** Build `lib-commons/cmd/telemetry-inventory` — a deterministic Go CLI that statically analyzes a Go service for telemetry primitives (counters, histograms, gauges, spans, log fields, framework auto-instrumentation, cross-cutting consistency) and renders a byte-stable Markdown dictionary, plus a `verify` subcommand that powers a CI drift gate.

**Architecture summary:** `golang.org/x/tools/go/analysis` framework with seven analyzers running through `packages.Load` + a custom checker that captures typed Results (vs. diagnostics). CrossCutAnalyzer declares `Requires` on the five primitive analyzers — framework guarantees ordering. Manual builder with explicit sorts at every emission point yields determinism. `inventory` writes; `verify` regenerates and byte-diffs.

**Tech stack:**
- Go 1.25.9 (lib-commons baseline)
- `golang.org/x/tools/go/{analysis,packages,ast/inspector,ast/astutil}` (new direct dep — already transitive in `go.sum`)
- Stdlib `flag` for CLI parent + manual subcommand routing (decision §3)
- Goreleaser v2 — flip `builds: skip: true` to multi-platform builds for the new binary
- `analysistest` for analyzer fixtures, golden files for renderer/E2E

**Source design:** `/Users/fredamaral/repos/lerianstudio/lib-commons/docs/plans/2026-05-07-telemetry-inventory-design.md` (committed at `72da627`). All design decisions are settled — this plan executes them.

**Branch:** `feat/telemetry-inventory` (already exists, design committed). All work lands here.

---

## 2. Constraints (MANDATORY — copy verbatim)

These six rules apply to every task and every commit. The engineer must enforce them; reviewers must reject violations.

1. **No env vars for behavior toggles.** Use Go flags (`--update-golden`, `--debug`, `--verbose`). Env vars ONLY for genuine environment config (none applicable in this CLI).

2. **Conventional Commits + trailers** for every commit instruction. Format per lib-commons existing history:
   ```bash
   git commit -m "feat(telemetry-inventory): <subject>" \
     --trailer "Generated-by: Claude" --trailer "AI-Model: claude-opus-4-7"
   ```
   Subject types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`. Scope: `telemetry-inventory` for CLI work, `metrics` for registry pre-work.

3. **TDD discipline (RED-GREEN-REFACTOR):** for each analyzer, the test fixture (with `// want "..."` directives) is a SEPARATE TASK that comes BEFORE the analyzer implementation task. The fixture task must explicitly run the test and verify FAILURE before the implementation task begins.

4. **Zero-panic policy:** no `panic()`, no `log.Fatal()`, no `Must*` helpers anywhere. Return `(T, error)`. Bootstrap and init code included. Only exception: `regexp.MustCompile` with compile-time constant patterns.

5. **Verification commands at every task:** each task ends with explicit `go test ./...`, `go build ./...`, or `make <target>` invocations + expected output excerpts. Engineer copy-pastes and verifies.

6. **Executor:** `ring:backend-engineer-golang` for all implementation tasks. Note this in each task's Agent field.

---

## 3. Open Questions Resolutions

### Q1 — CLI framework: stdlib `flag`

**Decision:** stdlib `flag` package + manual subcommand dispatch in `main.go`.

**Rationale:** lib-commons currently has zero CLI binaries (`cmd/` directory does not exist; `go.mod` has no `cobra` or `urfave/cli` dependency). The CLI surface is two subcommands with under a dozen flags total. Adding a new top-level dependency for that surface is unjustified. Stdlib `flag` is sufficient and keeps the binary small for `go install` distribution. Pattern: in `main.go`, switch on `os.Args[1]` to dispatch to either `runInventory(os.Args[2:])` or `runVerify(os.Args[2:])`, each owning its own `flag.NewFlagSet`.

**Decision criteria for revisit:** If a third subcommand or shell completion is requested, re-evaluate Cobra. Not before.

### Q2 — Goreleaser flip: enable per-binary build, keep changelog

**Decision:** Replace `builds: [{ skip: true }]` with a single named build for `cmd/telemetry-inventory` across the four-platform matrix. Library packages still ship via Go module (no change there); only the new binary ships as artifacts. Exact yaml diff in Task B.12.1.

Matrix: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. Strip debug info (`-trimpath`, `-ldflags="-s -w"`). Checksum strategy: `checksum: { name_template: "checksums.txt", algorithm: "sha256" }`. Archives: tar.gz on linux/darwin (no Windows for V1).

**Decision criteria for revisit:** Add Windows + zip archives if anyone files a request — none expected since lib-commons is consumed by Linux services.

### Q3 — Module mode for fixture services: `replace` directives

**Decision:** Each E2E fixture service in `cmd/telemetry-inventory/testdata/services/<name>/` is a real Go module with its own `go.mod`, and uses a `replace github.com/LerianStudio/lib-commons/v5 => ../../../../..` directive pointing at the repo root.

**Rationale:** `replace` directives are the standard testdata-as-module pattern (used by `gopls`, `golangci-lint`, `staticcheck`). They:
- Avoid network fetches in CI (no `GOFLAGS=-mod=mod` chase).
- Pin against the live source so fixtures track lib-commons API changes synchronously.
- Are isolated from the parent module's build graph (Go treats nested `go.mod` as a separate module).
- Need no special tooling — `go test` from the parent module ignores them; the orchestrator's `packages.Load` enters the nested module's directory and reads its `go.mod` directly.

`analysistest` fixtures (`testdata/fixtures/`) are the simpler `analysistest.TestData()` form — synthetic packages, no `go.mod`, no replace.

**Decision criteria for revisit:** If `replace` causes `go.sum` drift in fixture modules, switch to `vendor/` per fixture. Profile during Phase B.11.

### Q4 — Concurrency cap for analyzer goroutines: unbounded for V1

**Decision:** Start with the `go/analysis` framework's default scheduling (one goroutine per analyzer per package, framework manages). 7 analyzers × typical package count = bounded by package count, not analyzer count. No custom worker pool.

**Rationale:** The framework has been load-tested by `staticcheck`, `golangci-lint`, and `gopls` on packages an order of magnitude larger than any Lerian service. Pre-optimizing without a profile is a YAGNI trap.

**Decision criteria for revisit:** If `inventory` exceeds 30s wall-clock or RSS exceeds 2GB on the largest mixed-realistic fixture, add a bounded `errgroup.SetLimit(runtime.GOMAXPROCS(0))`. Profile during Phase B.11.

---

## 4. Stage A — Registry Pre-work

**Branch policy:** Stage A merges to `develop` independently before Stage B starts. This unblocks consumers of the registry and isolates the `metrics` package change from the larger CLI change.

**Estimate total:** ~1 engineer-day (~6 tasks).

---

### Task A.1: Inspect existing helpers and confirm registry seed contents

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 3 min

**Goal:** Read all five helper files and the metrics constants block to produce the exact `HelperSpec` seed list. Output: a written list of (GoFunctionName, MetricName, InstrumentType, Unit, Description, DefaultLabels) tuples to be inlined into Task A.2's code.

**Files (read-only):**
- `commons/opentelemetry/metrics/metrics.go` (lines 47-76 for the `Metric*` constants)
- `commons/opentelemetry/metrics/account.go`
- `commons/opentelemetry/metrics/system.go`
- `commons/opentelemetry/metrics/transaction.go`
- `commons/opentelemetry/metrics/operation_routes.go`
- `commons/opentelemetry/metrics/transaction_routes.go`

**Implementation (no code change):** Confirm this exact 6-entry seed:

| GoFunctionName | MetricName | InstrumentType | Unit | Description | DefaultLabels |
|---|---|---|---|---|---|
| `RecordAccountCreated` | `accounts_created` | `Counter` | `1` | `Measures the number of accounts created by the server.` | `[]` (variadic attrs at call site) |
| `RecordTransactionProcessed` | `transactions_processed` | `Counter` | `1` | `Measures the number of transactions processed by the server.` | `[]` |
| `RecordTransactionRouteCreated` | `transaction_routes_created` | `Counter` | `1` | `Measures the number of transaction routes created by the server.` | `[]` |
| `RecordOperationRouteCreated` | `operation_routes_created` | `Counter` | `1` | `Measures the number of operation routes created by the server.` | `[]` |
| `RecordSystemCPUUsage` | `system.cpu.usage` | `Gauge` | `percentage` | `Current CPU usage percentage of the process host.` | `[]` |
| `RecordSystemMemUsage` | `system.mem.usage` | `Gauge` | `percentage` | `Current memory usage percentage of the process host.` | `[]` |

**Verification:**
```bash
grep -n "^func (f \*MetricsFactory) Record" commons/opentelemetry/metrics/*.go | grep -v _test
```
Expected output (six lines). If grep returns more or fewer, STOP — registry seed is incorrect; re-inspect before proceeding to Task A.2.

**Failure recovery:** If `grep` shows additional `Record*` methods (e.g. someone added one between design and execution), update Task A.2's seed list to include them with values inferred from the same patterns.

---

### Task A.2: Create `commons/opentelemetry/metrics/registry.go`

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 5 min

**Goal:** Add the `MetricType`, `HelperSpec`, and `Helpers` declarations as a new file in the existing `metrics` package.

**Files:**
- `commons/opentelemetry/metrics/registry.go` — create

**Implementation:**
```go
// Package metrics — registry of MetricsFactory.Record* helpers.
//
// This file is the canonical source of truth for telemetry-inventory CLI
// Tier 3 detection. Every Record* method on MetricsFactory MUST have a
// matching entry below. Drift is enforced by TestHelpersMatchRegistry.
package metrics

// MetricType identifies an OpenTelemetry instrument category.
type MetricType int

const (
	// Counter is a monotonically increasing instrument.
	Counter MetricType = iota
	// Histogram records a distribution of values.
	Histogram
	// Gauge records a current value at a point in time.
	Gauge
)

// String returns the lowercase canonical name used in dictionary output.
func (m MetricType) String() string {
	switch m {
	case Counter:
		return "counter"
	case Histogram:
		return "histogram"
	case Gauge:
		return "gauge"
	default:
		return "unknown"
	}
}

// HelperSpec describes a single MetricsFactory.Record* helper in machine-
// readable form. Consumed by the telemetry-inventory CLI to detect Tier 3
// emission sites without re-parsing helper source code.
type HelperSpec struct {
	// GoFunctionName is the exact Go method name on *MetricsFactory.
	GoFunctionName string
	// MetricName is the Prometheus/OTel metric name the helper emits.
	MetricName string
	// InstrumentType is the OTel instrument category.
	InstrumentType MetricType
	// Unit is the OTel unit string (e.g. "1", "percentage", "ms").
	Unit string
	// Description matches the Metric.Description used by the helper.
	Description string
	// DefaultLabels are labels that the helper always attaches. Empty
	// when the helper accepts variadic attributes from the caller.
	DefaultLabels []string
}

// Helpers is the canonical registry of MetricsFactory.Record* helpers.
//
// Adding a Record* method to MetricsFactory MUST add an entry here.
// Removing one MUST remove the entry. Enforced by TestHelpersMatchRegistry.
var Helpers = []HelperSpec{
	{
		GoFunctionName: "RecordAccountCreated",
		MetricName:     "accounts_created",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of accounts created by the server.",
		DefaultLabels:  nil,
	},
	{
		GoFunctionName: "RecordTransactionProcessed",
		MetricName:     "transactions_processed",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of transactions processed by the server.",
		DefaultLabels:  nil,
	},
	{
		GoFunctionName: "RecordTransactionRouteCreated",
		MetricName:     "transaction_routes_created",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of transaction routes created by the server.",
		DefaultLabels:  nil,
	},
	{
		GoFunctionName: "RecordOperationRouteCreated",
		MetricName:     "operation_routes_created",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of operation routes created by the server.",
		DefaultLabels:  nil,
	},
	{
		GoFunctionName: "RecordSystemCPUUsage",
		MetricName:     "system.cpu.usage",
		InstrumentType: Gauge,
		Unit:           "percentage",
		Description:    "Current CPU usage percentage of the process host.",
		DefaultLabels:  nil,
	},
	{
		GoFunctionName: "RecordSystemMemUsage",
		MetricName:     "system.mem.usage",
		InstrumentType: Gauge,
		Unit:           "percentage",
		Description:    "Current memory usage percentage of the process host.",
		DefaultLabels:  nil,
	},
}
```

**Verification:**
```bash
go build ./commons/opentelemetry/metrics/... && go vet ./commons/opentelemetry/metrics/...
```
Expected output: empty (no errors, no warnings).

**Failure recovery:** Compile error usually indicates a typo or a missing import. The file uses no imports — if Go complains, re-paste verbatim. Lint complaint about exported-without-comment: every type and field already has a doc comment; if a new linter rule fires, add the missing comment without changing semantics.

---

### Task A.3: Write `TestHelpersMatchRegistry` (RED)

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 4 min

**Goal:** Add the bidirectional reflection test. Run it now to confirm it currently passes (because Task A.2 already seeded all 6 entries). RED-GREEN doesn't apply cleanly here because the test is the contract guard — its purpose is to fail when registry drifts from code. Verify by temporarily commenting out one helper entry.

**Files:**
- `commons/opentelemetry/metrics/registry_test.go` — create

**Implementation:**
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
//   - Method exists but no registry entry → telemetry-inventory CLI
//     cannot detect Tier 3 emission for that helper.
//   - Registry entry exists but method removed → CLI looks for a
//     nonexistent method.
//
// Both are caught here, before a release ships.
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

**Verification:**
```bash
go test -run TestHelpersMatchRegistry -v ./commons/opentelemetry/metrics/...
```
Expected output (excerpt):
```
=== RUN   TestHelpersMatchRegistry
--- PASS: TestHelpersMatchRegistry (0.00s)
PASS
```

**Failure recovery:** If a `Record*` method shows up that you missed in Task A.1, add the matching `HelperSpec` to `registry.go` with values inferred from the helper source. If a registry entry references a method that doesn't exist, you typoed in A.2 — fix the entry.

---

### Task A.4: Verify `TestHelpersMatchRegistry` actually catches drift

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 3 min

**Goal:** Sanity-check the contract. Temporarily mutate one input, confirm test fails, then revert.

**Files:**
- `commons/opentelemetry/metrics/registry.go` — temporary local edit (revert before commit)

**Implementation:**
1. Open `commons/opentelemetry/metrics/registry.go`.
2. Comment out the entire `RecordAccountCreated` entry (lines roughly 50-57 in the file written by Task A.2).
3. Run the test.
4. Confirm it FAILS with the expected error message.
5. Uncomment to restore.
6. Re-run the test to confirm it PASSES again.

**Verification (step 3):**
```bash
go test -run TestHelpersMatchRegistry -v ./commons/opentelemetry/metrics/...
```
Expected output (excerpt):
```
=== RUN   TestHelpersMatchRegistry
    registry_test.go:NN: MetricsFactory.RecordAccountCreated exists but is not in metrics.Helpers — add an entry to registry.go
--- FAIL: TestHelpersMatchRegistry (0.00s)
FAIL
```

**Verification (step 6):**
```bash
go test -run TestHelpersMatchRegistry -v ./commons/opentelemetry/metrics/...
```
Expected output: `PASS`.

**Failure recovery:** If the test passes when you comment out the entry, the test logic is broken — re-read Task A.3's implementation and confirm both forward and reverse loops fire. If the test fails after you uncomment, you didn't restore correctly — check git diff is empty.

---

### Task A.5: Run full lib-commons test suite to confirm no regressions

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 3 min

**Goal:** Verify the new file and test don't break anything in the existing metrics package.

**Files:** none (no edits)

**Implementation:** Run unit tests for the metrics package only.

**Verification:**
```bash
go test -tags=unit -count=1 ./commons/opentelemetry/metrics/...
```
Expected output (excerpt):
```
ok  	github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics	X.XXXs
```

Then linter:
```bash
make lint 2>&1 | tail -10
```
Expected: `[ok] Lint and performance checks passed successfully` or no output beyond ok marker. If perfsprint or golangci-lint flags new issues in `registry.go`, fix per their suggestions (typically: missing comment on exported, or use `iota` correctly — already done).

**Failure recovery:** Pre-existing failing tests in the metrics package are NOT yours to fix in this task — log them as a separate concern and proceed. Failures in `registry_test.go` itself: re-run Tasks A.3 / A.4.

---

### Task A.6: Commit Stage A

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 2 min

**Goal:** Commit registry pre-work as a single feat commit.

**Files:** none new (just stage the two files written above)

**Implementation:**
```bash
git add commons/opentelemetry/metrics/registry.go commons/opentelemetry/metrics/registry_test.go
git commit -m "$(cat <<'EOF'
feat(metrics): add Helpers registry with bidirectional drift test

Introduces metrics.Helpers — a self-describing registry of every
MetricsFactory.Record* helper. Each entry carries the Go method name,
emitted metric name, OTel instrument type, unit, description, and
default labels.

TestHelpersMatchRegistry enforces that every Record* method has a
registry entry and vice versa, via reflect over *MetricsFactory.
Catches drift at lib-commons CI before release.

Seed covers all 6 currently-existing helpers:
- RecordAccountCreated (counter, accounts_created)
- RecordTransactionProcessed (counter, transactions_processed)
- RecordTransactionRouteCreated (counter, transaction_routes_created)
- RecordOperationRouteCreated (counter, operation_routes_created)
- RecordSystemCPUUsage (gauge, system.cpu.usage)
- RecordSystemMemUsage (gauge, system.mem.usage)

Pre-work for cmd/telemetry-inventory CLI Tier 3 detection (see
docs/plans/2026-05-07-telemetry-inventory-design.md §12).
EOF
)" --trailer "Generated-by: Claude" --trailer "AI-Model: claude-opus-4-7"
```

**Verification:**
```bash
git log -1 --format='%H %s' && git show --stat HEAD
```
Expected output (excerpt): one commit with subject `feat(metrics): add Helpers registry with bidirectional drift test`, two files added (`registry.go`, `registry_test.go`).

**Failure recovery:** If `git add` complains the path doesn't exist, check Task A.2 / A.3 actually created the files. If pre-commit hooks fire and fail, fix the issue surfaced and create a NEW commit — do not amend.

---

## 5. Review Checkpoint A — gate before Stage B

Before starting Stage B, the reviewer must confirm:

- [ ] `go test ./commons/opentelemetry/metrics/...` passes on a clean checkout of `feat/telemetry-inventory`.
- [ ] `TestHelpersMatchRegistry` was demonstrated to fail when an entry is removed (Task A.4 evidence in PR description).
- [ ] Registry seed contains exactly 6 entries — no more, no fewer.
- [ ] `make lint` passes for the metrics package.
- [ ] Commit message uses Conventional Commits with `metrics` scope and includes both trailers.
- [ ] No changes outside `commons/opentelemetry/metrics/`.
- [ ] Stage A merges to `develop` BEFORE Stage B starts.

If any check fails: block the PR, fix, re-verify, do not proceed.

---

## 6. Stage B — CLI Implementation

**Branch:** `feat/telemetry-inventory` (Stage A already merged from develop, or rebased onto it).

**Estimate total:** ~17-20 engineer-days across 12 phases.

**Phase summary:**
- Phase B.1 — Module scaffold (4 tasks, 1 day)
- Phase B.2 — CounterAnalyzer Tier 1 vertical slice (6 tasks, 2 days)
- Phase B.3 — CounterAnalyzer Tier 2 + Tier 3 (4 tasks, 1 day)
- Phase B.4 — HistogramAnalyzer (3 tasks, 0.75 day)
- Phase B.5 — GaugeAnalyzer (3 tasks, 0.75 day)
- Phase B.6 — SpanAnalyzer (5 tasks, 1.5 days)
- Phase B.7 — LogFieldAnalyzer (4 tasks, 1.25 days)
- Phase B.8 — FrameworkAnalyzer (5 tasks, 1.5 days)
- Phase B.9 — CrossCutAnalyzer (6 tasks, 2 days)
- Phase B.10 — Renderer Markdown + JSON (5 tasks, 1.5 days)
- Phase B.11 — Verify subcommand + Makefile targets (4 tasks, 1 day)
- Phase B.12 — E2E fixture services + goreleaser + final polish (6 tasks, 2 days)

---

### Phase B.1: Module scaffold

#### Task B.1.1: Add `golang.org/x/tools` direct dependency

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 2 min

**Goal:** Promote `golang.org/x/tools` from transitive to direct dep so analyzer code can import `go/analysis`.

**Files:** `go.mod` — modify (`go get` will update)

**Implementation:**
```bash
go get golang.org/x/tools@latest
go mod tidy
```

**Verification:**
```bash
grep "golang.org/x/tools" go.mod
go build ./...
```
Expected: one direct require line in go.mod with `v0.X.Y`; empty build output.

**Failure recovery:** If `go get` fails due to proxy or auth issues, retry with `GOPROXY=https://proxy.golang.org,direct`. If `go mod tidy` removes the entry because nothing imports it yet, that's expected — it'll be re-added when Task B.1.2 imports it.

---

#### Task B.1.2: Create `cmd/telemetry-inventory/main.go` skeleton

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 5 min

**Goal:** Establish the binary entrypoint with subcommand dispatch and `--help`. No real work yet — just `inventory` and `verify` stubs that print "not implemented" and exit 0.

**Files:**
- `cmd/telemetry-inventory/main.go` — create
- `cmd/telemetry-inventory/inventory.go` — create
- `cmd/telemetry-inventory/verify.go` — create

**Implementation (main.go):**
```go
// Command telemetry-inventory inventories OpenTelemetry primitives in a
// Go service and renders a deterministic Markdown dictionary.
//
// Subcommands:
//   inventory  Sweep + render dictionary.
//   verify     Regen + diff against committed dictionary; exits 1 on drift.
//
// All behavior toggles are flags — no env-var switches.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const usage = `telemetry-inventory — OpenTelemetry primitive inventory for Go services.

Usage:
  telemetry-inventory <subcommand> [flags] [target]

Subcommands:
  inventory   Sweep target and render Markdown dictionary.
  verify      Regenerate and byte-diff against committed dictionary.

Run 'telemetry-inventory <subcommand> -h' for subcommand-specific flags.
`

var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "telemetry-inventory:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "inventory":
		return runInventory(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown subcommand: %q\n\n", args[0])
		return errUsage
	}
}
```

**Implementation (inventory.go):**
```go
package main

import (
	"flag"
	"fmt"
	"io"
)

func runInventory(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		outputMD       = fs.String("output-md", "./docs/dashboards/telemetry-dictionary.md", "Markdown dictionary output path")
		outputJSON     = fs.String("output-json", "", "Optional JSON intermediate output path (default: skip)")
		schemaVersion  = fs.String("schema-version", "", "Pin schema version (default: latest)")
		emitJSONStdout = fs.Bool("json", false, "Emit JSON to stdout instead of writing files")
		debug          = fs.Bool("debug", false, "Verbose internal diagnostics")
		verbose        = fs.Bool("verbose", false, "Verbose progress output")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := "."
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}

	// Phase B.1: stub. Real impl arrives in Phase B.10.
	fmt.Fprintf(stdout, "telemetry-inventory inventory: not yet implemented\n")
	fmt.Fprintf(stdout, "  target=%s output-md=%s output-json=%s schema=%s json=%v debug=%v verbose=%v\n",
		target, *outputMD, *outputJSON, *schemaVersion, *emitJSONStdout, *debug, *verbose)
	return nil
}
```

**Implementation (verify.go):**
```go
package main

import (
	"flag"
	"fmt"
	"io"
)

func runVerify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		committed = fs.String("committed", "./docs/dashboards/telemetry-dictionary.md", "Path to committed dictionary")
		quiet     = fs.Bool("quiet", false, "Suppress diff output")
		debug     = fs.Bool("debug", false, "Verbose internal diagnostics")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := "."
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}

	// Phase B.1: stub. Real impl arrives in Phase B.11.
	fmt.Fprintf(stdout, "telemetry-inventory verify: not yet implemented\n")
	fmt.Fprintf(stdout, "  target=%s committed=%s quiet=%v debug=%v\n",
		target, *committed, *quiet, *debug)
	return nil
}
```

**Verification:**
```bash
go build ./cmd/telemetry-inventory/...
go run ./cmd/telemetry-inventory/ inventory --help 2>&1 || true
go run ./cmd/telemetry-inventory/ inventory ./testdata 2>&1
```
Expected (last command):
```
telemetry-inventory inventory: not yet implemented
  target=./testdata output-md=./docs/dashboards/telemetry-dictionary.md ...
```

**Failure recovery:** If `go build` fails, double-check all three files exist and compile. If `flag.NewFlagSet(... ContinueOnError)` swallows `-h` differently than expected, that's fine for now — the goal is the command runs without panicking.

---

#### Task B.1.3: Create `internal/schema/` types

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 4 min

**Goal:** Define the canonical Dictionary type tree. Three files: `version.go`, `primitives.go`, `dictionary.go`.

**Files:**
- `cmd/telemetry-inventory/internal/schema/version.go` — create
- `cmd/telemetry-inventory/internal/schema/primitives.go` — create
- `cmd/telemetry-inventory/internal/schema/dictionary.go` — create

**Implementation (version.go):**
```go
// Package schema defines the canonical Dictionary types emitted by the
// telemetry-inventory CLI. Schema version is independent of lib-commons
// module version and bumps only when Dictionary shape changes.
package schema

// SchemaVersion is the current Dictionary schema. Incremented on shape
// change. Verify subcommand fails fast on mismatch.
const SchemaVersion = "1.0.0"
```

**Implementation (primitives.go):**
```go
package schema

// EmissionSite is a (file, line) reference to where a primitive is
// constructed or recorded. Paths are slash-normalized.
type EmissionSite struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// Tier identifies how the primitive surfaces (1=OTel direct,
	// 2=MetricsFactory builder, 3=domain helper). 0 for non-metric
	// primitives (spans, log fields).
	Tier int `json:"tier,omitempty"`
}

// CounterPrimitive describes one observed counter metric.
type CounterPrimitive struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Unit             string         `json:"unit,omitempty"`
	InstrumentType   string         `json:"instrument_type"`
	EmissionSites    []EmissionSite `json:"emission_sites"`
	Labels           []string       `json:"labels"`
	LabelCardinality int            `json:"label_cardinality_estimate,omitempty"`
	TenantScoped     bool           `json:"tenant_scoped,omitempty"`
}

// HistogramPrimitive describes one observed histogram metric.
type HistogramPrimitive struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Unit             string         `json:"unit,omitempty"`
	InstrumentType   string         `json:"instrument_type"`
	EmissionSites    []EmissionSite `json:"emission_sites"`
	Labels           []string       `json:"labels"`
	LabelCardinality int            `json:"label_cardinality_estimate,omitempty"`
	Buckets          []float64      `json:"buckets,omitempty"`
	TenantScoped     bool           `json:"tenant_scoped,omitempty"`
}

// GaugePrimitive describes one observed gauge metric.
type GaugePrimitive struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Unit             string         `json:"unit,omitempty"`
	InstrumentType   string         `json:"instrument_type"`
	EmissionSites    []EmissionSite `json:"emission_sites"`
	Labels           []string       `json:"labels"`
	LabelCardinality int            `json:"label_cardinality_estimate,omitempty"`
	TenantScoped     bool           `json:"tenant_scoped,omitempty"`
}

// SpanPrimitive describes one observed tracer.Start invocation.
type SpanPrimitive struct {
	Name          string         `json:"name"`
	EmissionSites []EmissionSite `json:"emission_sites"`
	Attributes    []string       `json:"attributes"`
	UnboundedSpan bool           `json:"unbounded_span,omitempty"`
	StatusOnError bool           `json:"status_on_error_observed"`
	RecordOnError bool           `json:"record_on_error_observed"`
}

// LogFieldPrimitive describes one structured log field observed across
// log.With/.Info/.Warn/.Error/.Debug call sites.
type LogFieldPrimitive struct {
	Name              string         `json:"name"`
	EmissionSites     []EmissionSite `json:"emission_sites"`
	LevelDistribution map[string]int `json:"level_distribution"`
	PIIRiskFlag       bool           `json:"pii_risk_flag,omitempty"`
}

// FrameworkInstrumentation describes a detected auto-instrumentation
// integration (otelfiber, otelhttp, otelgrpc, etc).
type FrameworkInstrumentation struct {
	Framework     string         `json:"framework"`
	EmissionSites []EmissionSite `json:"emission_sites"`
	AutoMetrics   []string       `json:"auto_metrics,omitempty"`
	AutoSpans     []string       `json:"auto_spans,omitempty"`
}

// CrossCutFinding is a per-site cross-cutting consistency issue.
type CrossCutFinding struct {
	Kind     string       `json:"kind"`
	Site     EmissionSite `json:"site"`
	Function string       `json:"function"`
	Detail   string       `json:"detail"`
}
```

**Implementation (dictionary.go):**
```go
package schema

// Meta carries non-deterministic-by-design metadata. Verify subcommand
// must redact GeneratedAt before byte-comparing.
type Meta struct {
	SchemaVersion string `json:"schema_version"`
	GeneratedAt   string `json:"generated_at,omitempty"`
	Target        string `json:"target,omitempty"`
}

// Dictionary is the canonical telemetry inventory output.
type Dictionary struct {
	Meta       Meta                       `json:"_meta"`
	Counters   []CounterPrimitive         `json:"counters"`
	Histograms []HistogramPrimitive       `json:"histograms"`
	Gauges     []GaugePrimitive           `json:"gauges"`
	Spans      []SpanPrimitive            `json:"spans"`
	LogFields  []LogFieldPrimitive        `json:"log_fields"`
	Frameworks []FrameworkInstrumentation `json:"frameworks"`
	CrossCut   []CrossCutFinding          `json:"cross_cutting"`
}
```

**Verification:**
```bash
go build ./cmd/telemetry-inventory/internal/schema/...
go vet ./cmd/telemetry-inventory/internal/schema/...
```
Expected: empty.

**Failure recovery:** If a JSON tag is malformed, build fails — verify the backtick syntax. Re-paste verbatim.

---

#### Task B.1.4: Create `internal/orchestrator/` skeleton

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 5 min

**Goal:** Create the orchestrator entry function that loads a target Go module via `packages.Load`. No analyzers wired yet — returns an empty Dictionary with populated Meta.

**Files:**
- `cmd/telemetry-inventory/internal/orchestrator/orchestrator.go` — create
- `cmd/telemetry-inventory/internal/orchestrator/orchestrator_test.go` — create

**Implementation (orchestrator.go):**
```go
// Package orchestrator runs analyzers against a target Go module and
// aggregates their typed Results into a schema.Dictionary.
package orchestrator

import (
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/packages"
)

// ErrNoPackages is returned when packages.Load yields zero packages.
var ErrNoPackages = errors.New("no Go packages found at target")

// Inventory loads target as a Go module and runs all registered
// analyzers. Returns a schema.Dictionary; caller is responsible for
// rendering.
func Inventory(target string) (*schema.Dictionary, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedDeps | packages.NeedModule,
		Dir: target,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages at %q: %w", target, err)
	}

	if len(pkgs) == 0 {
		return nil, ErrNoPackages
	}

	if errs := collectPackageErrors(pkgs); len(errs) > 0 {
		return nil, fmt.Errorf("package load errors: %w", errors.Join(errs...))
	}

	dict := &schema.Dictionary{
		Meta: schema.Meta{
			SchemaVersion: schema.SchemaVersion,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Target:        target,
		},
	}

	// Phase B.1: scaffold returns empty Dictionary. Analyzers wired in
	// subsequent phases (B.2 onward).
	_ = pkgs

	return dict, nil
}

func collectPackageErrors(pkgs []*packages.Package) []error {
	var errs []error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			errs = append(errs, fmt.Errorf("%s: %s", p.PkgPath, e.Msg))
		}
	})
	return errs
}
```

**Implementation (orchestrator_test.go):**
```go
package orchestrator_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/orchestrator"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

func TestInventory_NoPackages_ReturnsError(t *testing.T) {
	dict, err := orchestrator.Inventory("/tmp/definitely-not-a-go-module-path-xyz123")
	if err == nil {
		t.Fatalf("expected error, got dict=%+v", dict)
	}
}

func TestInventory_OnLibCommonsItself_ReturnsDict(t *testing.T) {
	dict, err := orchestrator.Inventory("../../../..")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dict == nil {
		t.Fatal("dict is nil")
	}
	if dict.Meta.SchemaVersion != schema.SchemaVersion {
		t.Errorf("schema_version = %q, want %q", dict.Meta.SchemaVersion, schema.SchemaVersion)
	}
}
```

**Verification:**
```bash
go test -count=1 ./cmd/telemetry-inventory/internal/orchestrator/...
```

**Failure recovery:** `TestInventory_OnLibCommonsItself_ReturnsDict` may surface package-load errors from the broader repo — if so, narrow the test to a smaller subdirectory and document why. Do not loosen the error check in `Inventory` itself.

---

### Phase B.2: CounterAnalyzer Tier 1 vertical slice

This is the load-bearing first end-to-end slice. It exercises framework setup, fixture format, golden tests, and orchestrator wiring — once it works, Phases B.3-B.8 are pattern replication.

#### Task B.2.1: Create `internal/analyzers/findings.go` shared types

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 3 min

**Goal:** Define the per-analyzer `*Findings` result types. One file shared across all primitive analyzers.

**Files:**
- `cmd/telemetry-inventory/internal/analyzers/findings.go` — create

**Implementation:**
```go
// Package analyzers contains go/analysis Analyzers for each telemetry
// primitive type plus a CrossCut analyzer that depends on the others.
package analyzers

import "github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"

type CounterFindings struct {
	Counters []schema.CounterPrimitive
}

type HistogramFindings struct {
	Histograms []schema.HistogramPrimitive
}

type GaugeFindings struct {
	Gauges []schema.GaugePrimitive
}

type SpanFindings struct {
	Spans []schema.SpanPrimitive
}

type LogFieldFindings struct {
	Fields []schema.LogFieldPrimitive
}

type FrameworkFindings struct {
	Frameworks []schema.FrameworkInstrumentation
}

type CrossCutFindings struct {
	Issues []schema.CrossCutFinding
}
```

**Verification:** `go build ./cmd/telemetry-inventory/internal/analyzers/...` — empty output.

---

#### Task B.2.2: Create CounterAnalyzer Tier 1 fixture (RED)

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 4 min

**Goal:** Write the analysistest fixture file for Tier 1 (OTel direct). Run the test EXPECTING failure because the analyzer doesn't exist yet.

**Files:**
- `cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier1/main.go` — create
- `cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier1/go.mod` — create
- `cmd/telemetry-inventory/internal/analyzers/counter_test.go` — create

**Implementation (testdata/src/counter-tier1/main.go):**
```go
package countertier1

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func emit(meter metric.Meter, ctx context.Context, tenantID string) {
	counter, _ := meter.Int64Counter("ledger_transactions_total") // want `counter "ledger_transactions_total" tier=1`
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant_id", tenantID))) // want `counter increment site labels=\[tenant_id\]`
}
```

**Implementation (testdata/src/counter-tier1/go.mod):**
```
module countertier1

go 1.25
```

**Implementation (counter_test.go):**
```go
package analyzers_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestCounterAnalyzer_Tier1(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.CounterAnalyzer, "counter-tier1")
}
```

**Verification:**
```bash
go test -run TestCounterAnalyzer_Tier1 -count=1 ./cmd/telemetry-inventory/internal/analyzers/...
```
Expected output (RED — analyzer not yet defined):
```
./counter_test.go:N:NN: undefined: analyzers.CounterAnalyzer
FAIL    github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers [build failed]
```

**Failure recovery:** If the test surprisingly PASSES, you accidentally wrote the analyzer too — that's RED-GREEN inversion; revert and reattempt. If `analysistest.TestData()` cannot find the fixture, the fixture path is wrong — `testdata/src/counter-tier1/main.go` is the canonical layout.

---

#### Task B.2.3: Implement CounterAnalyzer Tier 1 (GREEN)

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 5 min

**Goal:** Implement the minimum CounterAnalyzer that detects OTel direct counter constructors and call sites, matching the fixture's `// want` directives.

**Files:**
- `cmd/telemetry-inventory/internal/analyzers/counter.go` — create

**Implementation:**
```go
package analyzers

import (
	"go/ast"
	"go/types"
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// CounterAnalyzer detects counter primitives across all three tiers.
//
// Tier 1: meter.Int64Counter("name", ...) at constructor site.
// Tier 2: factory.Counter(metrics.Metric{Name: "name", ...}) (added in B.3).
// Tier 3: factory.RecordX(...) where RecordX is in metrics.Helpers (added in B.3).
var CounterAnalyzer = &analysis.Analyzer{
	Name:       "counter",
	Doc:        "Detects counter metric primitives across Tier 1, 2, 3.",
	Run:        runCounter,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeOf((*CounterFindings)(nil)),
}

func runCounter(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return &CounterFindings{}, nil
	}

	out := &CounterFindings{}

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call, _ := n.(*ast.CallExpr)
		if call == nil {
			return
		}
		if p := matchTier1Counter(pass, call); p != nil {
			out.Counters = append(out.Counters, *p)
			pass.Reportf(call.Pos(), "counter %q tier=1", p.Name)
			return
		}
		if l := matchTier1CounterAdd(pass, call); l != nil {
			pass.Reportf(call.Pos(), "counter increment site labels=%v", l)
		}
	})

	return out, nil
}

func matchTier1Counter(pass *analysis.Pass, call *ast.CallExpr) *schema.CounterPrimitive {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	if sel.Sel.Name != "Int64Counter" {
		return nil
	}
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok {
		return nil
	}
	if !isOTelMeter(tv.Type) {
		return nil
	}
	if len(call.Args) == 0 {
		return nil
	}
	name := stringLitValue(call.Args[0])
	if name == "" {
		return nil
	}
	pos := pass.Fset.Position(call.Pos())
	return &schema.CounterPrimitive{
		Name:           name,
		InstrumentType: "counter",
		EmissionSites: []schema.EmissionSite{{
			File: pos.Filename,
			Line: pos.Line,
			Tier: 1,
		}},
		Labels: nil,
	}
}

func matchTier1CounterAdd(pass *analysis.Pass, call *ast.CallExpr) []string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" {
		return nil
	}
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok {
		return nil
	}
	if !isOTelInt64Counter(tv.Type) {
		return nil
	}
	return extractAttributeKeys(pass, call.Args)
}

func isOTelMeter(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		if iface, ok := t.(*types.Interface); ok {
			return iface != nil && hasIntCounterMethod(iface)
		}
		return false
	}
	return named.Obj() != nil &&
		named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "go.opentelemetry.io/otel/metric" &&
		named.Obj().Name() == "Meter"
}

func isOTelInt64Counter(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj() != nil &&
		named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "go.opentelemetry.io/otel/metric" &&
		named.Obj().Name() == "Int64Counter"
}

func hasIntCounterMethod(iface *types.Interface) bool {
	for i := 0; i < iface.NumMethods(); i++ {
		if iface.Method(i).Name() == "Int64Counter" {
			return true
		}
	}
	return false
}

func stringLitValue(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok {
		return ""
	}
	if len(lit.Value) < 2 {
		return ""
	}
	if lit.Value[0] != '"' {
		return ""
	}
	return lit.Value[1 : len(lit.Value)-1]
}

func extractAttributeKeys(pass *analysis.Pass, args []ast.Expr) []string {
	var keys []string
	for _, a := range args {
		call, ok := a.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if sel.Sel.Name == "WithAttributes" {
			keys = append(keys, extractAttributeKeys(pass, call.Args)...)
			continue
		}
		if isAttributeConstructor(pass, sel) && len(call.Args) >= 1 {
			if k := stringLitValue(call.Args[0]); k != "" {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

func isAttributeConstructor(pass *analysis.Pass, sel *ast.SelectorExpr) bool {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	obj := pass.TypesInfo.Uses[id]
	if obj == nil {
		return false
	}
	pkg, ok := obj.(*types.PkgName)
	if !ok {
		return false
	}
	return pkg.Imported().Path() == "go.opentelemetry.io/otel/attribute"
}
```

**Verification:**
```bash
go test -run TestCounterAnalyzer_Tier1 -count=1 -v ./cmd/telemetry-inventory/internal/analyzers/...
```
Expected: PASS.

**Failure recovery:** If `// want` regex doesn't match, the most common cause is the diagnostic message format diverging from what the fixture expects. Either change the fixture comment OR adjust the `pass.Reportf` format. Keep them in sync. If type resolution fails, run `go mod tidy` inside `testdata/src/counter-tier1/` once.

---

#### Task B.2.4: Wire CounterAnalyzer into orchestrator

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 5 min

**Goal:** Build a custom checker that runs analyzers and captures their typed Results into the Dictionary. This is the "custom orchestrator instead of singlechecker.Main" pattern from design §4.

**Files:**
- `cmd/telemetry-inventory/internal/orchestrator/checker.go` — create
- `cmd/telemetry-inventory/internal/orchestrator/orchestrator.go` — modify

**Implementation (checker.go):**
```go
package orchestrator

import (
	"fmt"
	"go/token"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

func runAnalyzers(pkgs []*packages.Package, analyzers []*analysis.Analyzer) (map[string]map[*analysis.Analyzer]any, error) {
	out := make(map[string]map[*analysis.Analyzer]any)

	order, err := topoSort(analyzers)
	if err != nil {
		return nil, err
	}

	for _, pkg := range pkgs {
		if pkg.IllTyped {
			continue
		}
		results := make(map[*analysis.Analyzer]any)
		out[pkg.PkgPath] = results

		for _, a := range order {
			res, runErr := runOne(pkg, a, results)
			if runErr != nil {
				return nil, fmt.Errorf("analyzer %s on %s: %w", a.Name, pkg.PkgPath, runErr)
			}
			results[a] = res
		}
	}

	return out, nil
}

func runOne(pkg *packages.Package, a *analysis.Analyzer, prior map[*analysis.Analyzer]any) (any, error) {
	pass := &analysis.Pass{
		Analyzer:         a,
		Fset:             pkg.Fset,
		Files:            pkg.Syntax,
		Pkg:              pkg.Types,
		TypesInfo:        pkg.TypesInfo,
		TypesSizes:       pkg.TypesSizes,
		ResultOf:         prior,
		Report:           func(d analysis.Diagnostic) { /* discard; we capture via Result */ },
		ImportObjectFact: func(_ token.Pos, _ analysis.Fact) bool { return false },
	}
	return a.Run(pass)
}

func topoSort(roots []*analysis.Analyzer) ([]*analysis.Analyzer, error) {
	visited := map[*analysis.Analyzer]bool{}
	stack := map[*analysis.Analyzer]bool{}
	var order []*analysis.Analyzer
	var visit func(a *analysis.Analyzer) error

	visit = func(a *analysis.Analyzer) error {
		if stack[a] {
			return fmt.Errorf("analyzer cycle through %s", a.Name)
		}
		if visited[a] {
			return nil
		}
		stack[a] = true
		for _, r := range a.Requires {
			if err := visit(r); err != nil {
				return err
			}
		}
		stack[a] = false
		visited[a] = true
		order = append(order, a)
		return nil
	}

	for _, r := range roots {
		if err := visit(r); err != nil {
			return nil, err
		}
	}
	return order, nil
}
```

**Implementation (orchestrator.go modify):** Replace the `_ = pkgs` placeholder with analyzer wiring + `mergeCounters` call. Add `analyzers.CounterAnalyzer` to `allAnalyzers` slice. (Full diff follows the pattern; see design §6 sample code.)

**Verification:**
```bash
go build ./cmd/telemetry-inventory/...
go test -count=1 ./cmd/telemetry-inventory/internal/orchestrator/...
```

**Failure recovery:** If `runAnalyzers` panics inside `runOne` (most likely cause: nil `pass.ResultOf` map), the topo sort is mis-ordered. Print the order and confirm `inspect.Analyzer` precedes `CounterAnalyzer`.

---

#### Task B.2.5: Add Tier-1 E2E smoke test against a tiny fixture

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 5 min

**Goal:** Confirm orchestrator + analyzer wiring reads a fixture-as-module and returns at least one CounterPrimitive with the expected name.

**Files:**
- `cmd/telemetry-inventory/testdata/services/pure-tier1/go.mod` — create
- `cmd/telemetry-inventory/testdata/services/pure-tier1/main.go` — create
- `cmd/telemetry-inventory/internal/orchestrator/orchestrator_test.go` — modify (append test)

**Implementation (testdata/services/pure-tier1/go.mod):**
```
module example.com/puretier1

go 1.25

require go.opentelemetry.io/otel/metric v1.42.0

require (
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.42.0 // indirect
	go.opentelemetry.io/otel/trace v1.42.0 // indirect
)

replace github.com/LerianStudio/lib-commons/v5 => ../../../../..
```

**Implementation (testdata/services/pure-tier1/main.go):**
```go
package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func emit(meter metric.Meter, ctx context.Context) {
	c, _ := meter.Int64Counter("ledger_transactions_total")
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant_id", "t-1")))
}

func main() {}
```

**Implementation (append to orchestrator_test.go):**
```go
func TestInventory_PureTier1Fixture_DetectsCounter(t *testing.T) {
	dict, err := orchestrator.Inventory("../../testdata/services/pure-tier1")
	if err != nil {
		t.Fatalf("inventory failed: %v", err)
	}
	if len(dict.Counters) == 0 {
		t.Fatal("no counters detected")
	}
	var found bool
	for _, c := range dict.Counters {
		if c.Name == "ledger_transactions_total" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ledger_transactions_total in counters, got %+v", dict.Counters)
	}
}
```

**Verification:**
```bash
( cd cmd/telemetry-inventory/testdata/services/pure-tier1 && go mod tidy )
go test -run TestInventory_PureTier1Fixture_DetectsCounter -count=1 -v ./cmd/telemetry-inventory/internal/orchestrator/...
```

**Failure recovery:** If `go mod tidy` inside the fixture pulls a different OTel version that conflicts, pin in the fixture's go.mod to match parent. If `replace` directive fails because the relative path is wrong, count `..` carefully: from `cmd/telemetry-inventory/testdata/services/pure-tier1/` to repo root is `../../../../..` (5 levels up).

---

#### Task B.2.6: Commit Phase B.1 + B.2 vertical slice

**Target:** backend
**Working Directory:** /Users/fredamaral/repos/lerianstudio/lib-commons
**Agent:** ring:backend-engineer-golang
**Estimate:** 2 min

**Implementation:**
```bash
git add cmd/telemetry-inventory/ go.mod go.sum
git commit -m "$(cat <<'EOF'
feat(telemetry-inventory): scaffold CLI with Tier 1 counter detection

Establishes cmd/telemetry-inventory module with:
- main.go: stdlib-flag-based subcommand dispatch (inventory, verify)
- internal/schema: versioned Dictionary types (SchemaVersion 1.0.0)
- internal/orchestrator: packages.Load + custom checker capturing
  typed analyzer Results
- internal/analyzers/counter.go: Tier 1 (OTel direct) detection of
  meter.Int64Counter via go/types-aware matching
- analysistest fixture (testdata/src/counter-tier1) with // want
  directives
- E2E fixture service (testdata/services/pure-tier1) with go.mod
  + replace directive

Subsequent commits add Tier 2/3 detection, additional primitives,
renderer, and verify subcommand.
EOF
)" --trailer "Generated-by: Claude" --trailer "AI-Model: claude-opus-4-7"
```

**Verification:** `git show --stat HEAD | head -30` and `go test -count=1 ./cmd/telemetry-inventory/...`

---

### Review Checkpoint B.2

Before proceeding to B.3:

- [ ] `go test ./cmd/telemetry-inventory/...` passes.
- [ ] `analysistest` Tier 1 fixture green.
- [ ] E2E fixture (`pure-tier1/`) detects `ledger_transactions_total`.
- [ ] Orchestrator code has no `panic`, no `log.Fatal`. (Constraint #4.)
- [ ] No env-var reads anywhere. (Constraint #1.)
- [ ] `make lint` passes.

---

### Phase B.3: CounterAnalyzer Tier 2 + Tier 3

#### Task B.3.1: Tier 2 fixture (RED)

**Target:** backend | **Agent:** ring:backend-engineer-golang | **Estimate:** 4 min

**Files:**
- `cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier2/go.mod` — create
- `cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier2/main.go` — create
- `cmd/telemetry-inventory/internal/analyzers/counter_test.go` — modify (add subtest)

**Implementation (counter-tier2/go.mod):**
```
module countertier2

go 1.25

require github.com/LerianStudio/lib-commons/v5 v5.0.0

replace github.com/LerianStudio/lib-commons/v5 => ../../../../../..
```

**Implementation (counter-tier2/main.go):**
```go
package countertier2

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
)

func emit(f *metrics.MetricsFactory, ctx context.Context, tid string) error {
	b, err := f.Counter(metrics.Metric{Name: "ledger_tx_total", Unit: "1", Description: "demo"}) // want `counter "ledger_tx_total" tier=2`
	if err != nil {
		return err
	}
	return b.WithAttributes(attribute.String("tenant_id", tid)).AddOne(ctx)
}
```

**Add subtest to counter_test.go:**
```go
func TestCounterAnalyzer_Tier2(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.CounterAnalyzer, "counter-tier2")
}
```

**Verification:** RED expected — no Tier 2 detection yet.

```bash
( cd cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier2 && go mod tidy )
go test -run TestCounterAnalyzer_Tier2 -count=1 -v ./cmd/telemetry-inventory/internal/analyzers/...
```

---

#### Task B.3.2: Implement Tier 2 detection (GREEN)

**Target:** backend | **Agent:** ring:backend-engineer-golang | **Estimate:** 5 min

**Files:** `cmd/telemetry-inventory/internal/analyzers/counter.go` — modify

**Implementation:** Inside `runCounter`, after the Tier 1 branch, add Tier 2 dispatch and helper functions `matchTier2Counter`, `isMetricsFactory`, `extractMetricStructFields`.

Key logic:
- `matchTier2Counter` resolves `factory.Counter` to `(*MetricsFactory).Counter` via `pass.TypesInfo.Types[sel.X]`.
- `extractMetricStructFields` walks `*ast.CompositeLit.Elts` looking for `KeyValueExpr` with `.Key.(*ast.Ident).Name in {"Name", "Unit", "Description"}` and extracts string literal values.
- `pass.Reportf(call.Pos(), "counter %q tier=2", p.Name)` for fixture matching.

**Verification:** `go test -run TestCounterAnalyzer_Tier2 -count=1 -v ./cmd/telemetry-inventory/internal/analyzers/...` → PASS.

---

#### Task B.3.3: Tier 3 fixture + helper-registry-driven detection (RED→GREEN)

**Target:** backend | **Agent:** ring:backend-engineer-golang | **Estimate:** 5 min

**Files:**
- `cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier3/{go.mod,main.go}` — create
- `cmd/telemetry-inventory/internal/analyzers/counter.go` — modify
- `cmd/telemetry-inventory/internal/analyzers/counter_test.go` — modify

**Implementation (counter-tier3/main.go):**
```go
package countertier3

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

func emit(f *metrics.MetricsFactory, ctx context.Context) error {
	if err := f.RecordAccountCreated(ctx); err != nil { // want `counter "accounts_created" tier=3`
		return err
	}
	return f.RecordTransactionProcessed(ctx) // want `counter "transactions_processed" tier=3`
}
```

**In counter.go**, add Tier 3 branch + package-level lookup map:
```go
import libmetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"

var helperByMethod = func() map[string]libmetrics.HelperSpec {
	m := make(map[string]libmetrics.HelperSpec, len(libmetrics.Helpers))
	for _, h := range libmetrics.Helpers {
		m[h.GoFunctionName] = h
	}
	return m
}()

func matchTier3Counter(pass *analysis.Pass, call *ast.CallExpr) *schema.CounterPrimitive {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	spec, ok := helperByMethod[sel.Sel.Name]
	if !ok || spec.InstrumentType != libmetrics.Counter {
		return nil
	}
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok || !isMetricsFactory(tv.Type) {
		return nil
	}
	pos := pass.Fset.Position(call.Pos())
	return &schema.CounterPrimitive{
		Name:           spec.MetricName,
		Description:    spec.Description,
		Unit:           spec.Unit,
		InstrumentType: "counter",
		EmissionSites: []schema.EmissionSite{{
			File: pos.Filename,
			Line: pos.Line,
			Tier: 3,
		}},
		Labels: append([]string(nil), spec.DefaultLabels...),
	}
}
```

Add subtest. Run all three Counter subtests:

```bash
( cd cmd/telemetry-inventory/internal/analyzers/testdata/src/counter-tier3 && go mod tidy )
go test -run TestCounterAnalyzer -count=1 -v ./cmd/telemetry-inventory/internal/analyzers/...
```
Expected: All three Counter subtests PASS.

---

#### Task B.3.4: Commit Phase B.3

**Target:** backend | **Agent:** ring:backend-engineer-golang | **Estimate:** 2 min

```bash
git add cmd/telemetry-inventory/internal/analyzers/ cmd/telemetry-inventory/internal/analyzers/testdata/
git commit -m "feat(telemetry-inventory): add Tier 2 and Tier 3 counter detection" \
  --trailer "Generated-by: Claude" --trailer "AI-Model: claude-opus-4-7"
```

---

### Review Checkpoint B.3

- [ ] All three Counter subtests pass.
- [ ] `helperByMethod` lookup map covers all 6 Stage-A registry entries.
- [ ] No new env-var reads.
- [ ] `make lint` passes.

---

### Phase B.4: HistogramAnalyzer

Pattern-replicates Counter. Three tasks: B.4.1 (fixtures Tier 1+2 RED), B.4.2 (implement GREEN, wire orchestrator), B.4.3 (commit).

Tier 1 method: `Int64Histogram`. Tier 2 method: `Histogram`. Tier 3: filter `libmetrics.Helpers` by `InstrumentType == libmetrics.Histogram` (currently empty — code path exists but no matches in V1).

Move shared helpers (`stringLitValue`, `isMetricsFactory`, `extractMetricStructFields`) into `cmd/telemetry-inventory/internal/analyzers/helpers.go` to avoid duplication.

Commit: `feat(telemetry-inventory): add HistogramAnalyzer with Tier 1+2 detection`

---

### Phase B.5: GaugeAnalyzer

Identical shape to Histogram. Tier 1 method: `Int64Gauge`. Tier 2 method: `Gauge`. Tier 3: filter by `InstrumentType == libmetrics.Gauge` — fires for `RecordSystemCPUUsage`/`RecordSystemMemUsage`.

Three tasks (fixture + impl + commit). Pattern from Phase B.4.

Commit: `feat(telemetry-inventory): add GaugeAnalyzer with Tier 1+2+3 detection`

---

### Phase B.6: SpanAnalyzer

Span detection differs from metrics: walks function bodies block-by-block to pair `tracer.Start` with `defer span.End()`. The `unbounded_span` flag fires when no matching `End` exists in the same function.

#### Task B.6.1: Span fixture for happy path (RED) — 4 min

`testdata/src/span-balanced/main.go`:
```go
package spanbalanced

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

func ok(ctx context.Context, tr trace.Tracer) {
	ctx, span := tr.Start(ctx, "Balance.Transfer") // want `span "Balance.Transfer" balanced=true`
	defer span.End()
	_ = ctx
}
```

#### Task B.6.2: Span fixture for unbounded leak (RED) — 3 min

`testdata/src/span-leak/main.go`:
```go
package spanleak

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

func leak(ctx context.Context, tr trace.Tracer) {
	_, span := tr.Start(ctx, "Leaky.Op") // want `span "Leaky.Op" balanced=false`
	_ = span
	// no defer span.End() — unbounded
}
```

#### Task B.6.3: Implement SpanAnalyzer with block walk (GREEN) — 5 min

`cmd/telemetry-inventory/internal/analyzers/span.go`:

Algorithm:
- Walk every `*ast.FuncDecl` and `*ast.FuncLit`.
- For each function, find all `tracer.Start(...)` calls and bind the second return value name.
- For each function, find all `defer <expr>.End()` calls.
- Span is balanced if any defer's receiver names the same identifier as a `Start` second return.
- Emit `SpanPrimitive{Name, EmissionSites, UnboundedSpan}`.
- Type-resolve `tr` to `go.opentelemetry.io/otel/trace.Tracer`.

Also recognize `span.RecordError(err)` and `span.SetStatus(codes.Error, ...)` to populate `RecordOnError`/`StatusOnError` (used later by CrossCut). For Phase B.6 these can be inferred from any occurrence of those method names with span-typed receiver in the same function.

#### Task B.6.4: Add E2E fixture service `leak-spans` — 3 min

`testdata/services/leak-spans/{go.mod,main.go}` — service that has both balanced and unbounded spans. Smoke test in orchestrator confirms `dict.Spans[i].UnboundedSpan == true` for the leaky function.

#### Task B.6.5: Commit Phase B.6 — 2 min

`feat(telemetry-inventory): add SpanAnalyzer with End() pairing detection`

---

### Phase B.7: LogFieldAnalyzer

#### Task B.7.1: LogField fixture (RED) — 4 min

`testdata/src/logfield/main.go` exercises `commons/log` calls with `log.String("tenant_id", t)`, `log.Int("balance", n)`, and `log.With(log.String("trace_id", id))`. Each `// want` directive lists the field name and the level distribution.

#### Task B.7.2: Implement LogFieldAnalyzer (GREEN) — 5 min

`cmd/telemetry-inventory/internal/analyzers/logfield.go`:

- Recognize `Logger.Log(ctx, level, msg, fields...)`, `Logger.With(fields...)`, plus shorthand `.Info/.Warn/.Error/.Debug` (check the Logger interface in `commons/log`).
- Each field arg is typically `log.String(key, value)`, `log.Int(...)`, `log.Bool(...)`, `log.Err(...)`, `log.Any(...)`.
- Extract the literal key, count by level.
- PII risk: case-insensitive substring match on `password`, `token`, `secret`, `apikey`, `email`, `ssn`, `phone`, `address`, `cpf`, `cnpj`, `card`. Compile-time constant pattern → `regexp.MustCompile` allowed (Constraint #4 exception).

#### Task B.7.3: Wire into orchestrator + smoke test — 4 min

#### Task B.7.4: Commit Phase B.7 — 2 min

`feat(telemetry-inventory): add LogFieldAnalyzer with PII risk flag`

---

### Phase B.8: FrameworkAnalyzer

Detects auto-instrumentation: `otelfiber.Middleware`, `otelhttp.NewHandler`, `otelgrpc.UnaryServerInterceptor`, lib-commons RabbitMQ wrappers, `otelsql`/pgx OTel tracer wiring.

#### Task B.8.1: Framework fixtures (one per framework) — 5 min

5 minimal fixtures: fiber, http, grpc, rabbitmq (lib-commons consumer wrapper — read existing wrapper to identify the symbol), pgx-otel.

#### Task B.8.2: Implement FrameworkAnalyzer (GREEN) — 5 min

`cmd/telemetry-inventory/internal/analyzers/framework.go`:

For each known framework, define the (package path, symbol) pair. Walk all `*ast.SelectorExpr` and emit a `FrameworkInstrumentation` finding when one matches. Embed the auto-emitted metric/span names from the framework's documented telemetry surface (hardcoded list — they don't change often).

#### Task B.8.3: Wire into orchestrator + smoke test — 3 min

#### Task B.8.4: Add `mixed-realistic` E2E fixture stub — 5 min

Larger fixture mixing all framework integrations + Tier 1/2/3 metrics + spans + logs. Will be exercised in Phase B.12.

#### Task B.8.5: Commit Phase B.8 — 2 min

`feat(telemetry-inventory): add FrameworkAnalyzer for OTel auto-instrumentation`

---

### Review Checkpoint B.8

- [ ] Five primitive analyzers + Framework analyzer pass all subtests.
- [ ] Mixed-realistic fixture builds.
- [ ] Orchestrator returns populated Dictionary fields for all five primitive types + frameworks.
- [ ] No analyzer takes more than ~1s on the mixed-realistic fixture.

---

### Phase B.9: CrossCutAnalyzer

Algorithmically interesting. Declares `Requires: [Counter, Histogram, Gauge, Span, LogField]`.

#### Task B.9.1: CrossCut fixture for tenant consistency (RED) — 5 min

`testdata/src/crosscut-tenant/main.go`: function emits a counter labeled `tenant_id` but a span without `tenant_id` attribute. `// want` flags the span site as inconsistent.

#### Task B.9.2: CrossCut fixture for error attribution (RED) — 5 min

Function with `if err != nil` block; one branch has `span.RecordError(err)` + counter increment with `result="error"`; another lacks both. `// want` flags the partial branch.

#### Task B.9.3: CrossCut fixture for trace correlation (RED) — 4 min

Function emits a histogram in code path that has no `tracer.Start` upstream in the call chain. `// want` flags broken propagation.

#### Task B.9.4: Implement CrossCutAnalyzer with function-scoped bundling (GREEN) — 5 min

`cmd/telemetry-inventory/internal/analyzers/crosscut.go`:

- Read `Findings` from each prior analyzer via `pass.ResultOf[...]`.
- For each EmissionSite, compute enclosing FuncDecl via `astutil.PathEnclosingInterval(file, pos)`.
- Bundle by `(file, funcName)` key.
- Implement `checkTenantConsistency`, `checkErrorAttribution`, `checkTraceCorrelation` per design §7.
- Append `CrossCutFinding` per inconsistency.

Closure handling caveat from design §7 — bundle `*ast.FuncLit` emissions into enclosing `*ast.FuncDecl`. Document as known limitation.

#### Task B.9.5: Wire into orchestrator + add `broken-crosscut` E2E fixture service — 5 min

#### Task B.9.6: Commit Phase B.9 — 2 min

`feat(telemetry-inventory): add CrossCutAnalyzer for tenant/error/trace consistency`

---

### Review Checkpoint B.9

- [ ] CrossCut analyzer reads from all five `Requires` analyzers without nil panics.
- [ ] All three check kinds fire correctly on broken fixtures.
- [ ] Closure-in-FuncDecl bundling matches design §7.
- [ ] No false positives on the happy-path balanced span fixture from Phase B.6.

---

### Phase B.10: Renderer Markdown + JSON

#### Task B.10.1: Implement renderer determinism helpers — 5 min

`cmd/telemetry-inventory/internal/renderer/yaml_block.go`:

```go
package renderer

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

func writeYAMLBlock(sb *strings.Builder, indent int, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	prefix := strings.Repeat(" ", indent)
	for _, k := range keys {
		sb.WriteString(prefix)
		sb.WriteString(k)
		sb.WriteString(": ")
		writeYAMLValue(sb, indent+2, m[k])
		sb.WriteByte('\n')
	}
}

func writeYAMLValue(sb *strings.Builder, indent int, v any) {
	switch x := v.(type) {
	case string:
		sb.WriteString(strconv.Quote(x))
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case int:
		sb.WriteString(strconv.Itoa(x))
	case float64:
		sb.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case []string:
		sorted := make([]string, len(x))
		copy(sorted, x)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		sb.WriteByte('[')
		for i, s := range sorted {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(strconv.Quote(s))
		}
		sb.WriteByte(']')
	case []schema.EmissionSite:
		sites := make([]schema.EmissionSite, len(x))
		copy(sites, x)
		sort.SliceStable(sites, func(i, j int) bool {
			if sites[i].File != sites[j].File {
				return sites[i].File < sites[j].File
			}
			return sites[i].Line < sites[j].Line
		})
		sb.WriteByte('\n')
		for _, s := range sites {
			sb.WriteString(strings.Repeat(" ", indent))
			sb.WriteString("- file: ")
			sb.WriteString(strconv.Quote(filepath.ToSlash(s.File)))
			sb.WriteByte('\n')
			sb.WriteString(strings.Repeat(" ", indent))
			sb.WriteString("  line: ")
			sb.WriteString(strconv.Itoa(s.Line))
			if s.Tier > 0 {
				sb.WriteByte('\n')
				sb.WriteString(strings.Repeat(" ", indent))
				sb.WriteString("  tier: ")
				sb.WriteString(strconv.Itoa(s.Tier))
			}
			sb.WriteByte('\n')
		}
	default:
		sb.WriteString("null")
	}
}
```

#### Task B.10.2: Implement Markdown renderer — 5 min

`cmd/telemetry-inventory/internal/renderer/markdown.go`:

Per design §8: manual builder, sort hooks at every emission point. Render order: `_meta`, then `## Counters`, `## Histograms`, `## Gauges`, `## Spans`, `## Log Fields`, `## Frameworks`, `## Cross-Cutting`. Within each section, primitives sorted by name.

Final pass: regex-strip trailing whitespace (` +\n` → `\n`) using `regexp.MustCompile` (Constraint #4 exception).

Function: `Render(d *schema.Dictionary) string`.

#### Task B.10.3: Idempotency test (most valuable test) — 4 min

`cmd/telemetry-inventory/internal/renderer/markdown_test.go`:

```go
func TestRender_Idempotent(t *testing.T) {
	d := buildSampleDictionary() // helper that constructs a non-trivial Dictionary
	a := renderer.Render(d)
	b := renderer.Render(d)
	if a != b {
		t.Fatalf("non-deterministic: render produced different output on second call\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}
```

`buildSampleDictionary` includes unsorted slices, mixed types, at least 5 primitives per category.

#### Task B.10.4: Golden test + `--update-golden` flag — 5 min

```go
var updateGolden = flag.Bool("update-golden", false, "Rewrite renderer golden files")

func TestRender_Golden(t *testing.T) {
	d := buildSampleDictionary()
	got := renderer.Render(d)
	goldenPath := "testdata/golden/sample.md"
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("renderer output diverged from golden.\nRun: go test -run TestRender_Golden ./cmd/telemetry-inventory/internal/renderer -update-golden\n\n--- want ---\n%s\n--- got ---\n%s", string(want), got)
	}
}
```

Add Makefile target `update-goldens` (Phase B.11) that runs `go test -run Golden ... -update-golden` across all golden tests.

#### Task B.10.5: Wire renderer into `inventory` subcommand + commit — 5 min

Replace `inventory.go` stub with real impl:
- Call `orchestrator.Inventory(target)`, then pick output mode based on flags.
- `--json` flag: emit JSON to stdout, no file writes.
- `--output-md`: write Markdown to that path.
- `--output-json`: also write JSON to that path.

Create `cmd/telemetry-inventory/internal/renderer/json.go`: `encoding/json` `MarshalIndent` over the Dictionary.

Commit: `feat(telemetry-inventory): add deterministic Markdown + JSON renderer`

---

### Phase B.11: Verify subcommand + Makefile targets

#### Task B.11.1: Implement `verify` subcommand — 5 min

Replace `verify.go` stub. Create `cmd/telemetry-inventory/internal/verify/verify.go`:

- Re-run `orchestrator.Inventory(target)` and `renderer.Render(dict)`.
- Read committed file at `--committed` path.
- Redact `_meta.generated_at` line in both before comparing (regex strip).
- Schema-version mismatch → exit 2 with explicit "regenerate against current CLI version" message.
- Byte-diff. On drift, print unified diff (use `internal/verify` helper) and exit 1. `--quiet` suppresses diff body, keeps exit code.
- No drift → exit 0, no output.

#### Task B.11.2: Verify subcommand tests — 4 min

Test cases:
1. Identical inventory → exit 0, no output.
2. Drift in counter description → exit 1, prints diff.
3. Schema version mismatch → exit 2.
4. `--quiet` on drift → exit 1, no diff body.

Use harness: create temp dir with fixture service, run `verify` programmatically, capture stdout/stderr/exit.

#### Task B.11.3: Add Makefile targets — 4 min

Append to `Makefile`:
```makefile
.PHONY: telemetry-dictionary
telemetry-dictionary:
	$(call print_title,Generating telemetry dictionary)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go run ./cmd/telemetry-inventory inventory .
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Telemetry dictionary generated$(GREEN) ✔️$(NC)"

.PHONY: verify-telemetry
verify-telemetry:
	$(call print_title,Verifying telemetry dictionary against committed version)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go run ./cmd/telemetry-inventory verify .
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Telemetry dictionary in sync$(GREEN) ✔️$(NC)"

.PHONY: update-goldens
update-goldens:
	$(call print_title,Updating telemetry-inventory golden files)
	$(call check_command,go,"Install Go from https://golang.org/doc/install")
	go test -count=1 -run Golden ./cmd/telemetry-inventory/... -update-golden
	@echo "$(GREEN)$(BOLD)[ok]$(NC) Goldens updated — review with git diff before committing$(GREEN) ✔️$(NC)"
```

Update `make help` lines accordingly.

**Verification:** `make telemetry-dictionary && make verify-telemetry`

#### Task B.11.4: Commit Phase B.11 — 2 min

`feat(telemetry-inventory): add verify subcommand + Makefile targets`

---

### Phase B.12: E2E fixture services + goreleaser + final polish

#### Task B.12.1: Flip `.goreleaser.yml` to build the binary — 5 min

`.goreleaser.yml` — replace lines 6-7 (the `builds: - skip: true` block):

```yaml
version: 2

# lib-commons/v5 is primarily a Go library; the new
# cmd/telemetry-inventory binary ships as release artifacts.

builds:
  - id: telemetry-inventory
    main: ./cmd/telemetry-inventory
    binary: telemetry-inventory
    env:
      - CGO_ENABLED=0
    flags:
      - -trimpath
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}
    goos:
      - linux
      - darwin
    goarch:
      - amd64
      - arm64

archives:
  - id: telemetry-inventory
    builds:
      - telemetry-inventory
    name_template: >-
      telemetry-inventory_
      {{- .Version }}_
      {{- .Os }}_
      {{- .Arch }}
    formats: [tar.gz]

checksum:
  name_template: "checksums.txt"
  algorithm: sha256

# (existing changelog block unchanged below)
changelog:
  # … keep existing content …
```

If `main.version`/`main.commit`/`main.date` not yet declared in main.go, add `var (version, commit, date = "dev", "none", "unknown")` to silence the ldflags warning.

**Verification:** `make goreleaser && ls dist/`. Expected: 4 binaries (linux_amd64, linux_arm64, darwin_amd64, darwin_arm64) + checksums.txt + tar.gz archives.

#### Task B.12.2: Build out `mixed-realistic` E2E service fully — 5 min

Expand the stub from B.8.4 into a production-shaped service: HTTP handlers (otelhttp + Fiber), gRPC server, RabbitMQ consumer (lib-commons wrapper), pgx repository with otel tracing, plus all three metric tiers + spans + logs with consistent tenant_id labeling.

#### Task B.12.3: Add golden Markdown for each E2E service — 5 min

Generate via `make telemetry-dictionary` against each fixture, commit as `cmd/telemetry-inventory/testdata/golden/<service>.md`. E2E test in orchestrator runs `Inventory + Render + diff against golden`.

#### Task B.12.4: Run idempotency check across all E2E goldens — 3 min

For each E2E service, run `Inventory + Render` twice, byte-compare. Most valuable cross-machine determinism guard.

#### Task B.12.5: Documentation pass — README for cmd/telemetry-inventory — 5 min

`cmd/telemetry-inventory/README.md` sections: Goal, Install (`go install ...@latest`), Usage (inventory + verify), Schema versioning, How to add a new analyzer, Troubleshooting (common drift causes).

#### Task B.12.6: Final commit + Stage B PR-ready state — 3 min

```bash
git add .goreleaser.yml cmd/telemetry-inventory/
git commit -m "$(cat <<'EOF'
feat(telemetry-inventory): wire goreleaser cross-platform builds

Flips .goreleaser.yml from `builds: skip: true` to a single
telemetry-inventory build matrix: linux/amd64, linux/arm64,
darwin/amd64, darwin/arm64. CGO disabled for portable static binaries.
sha256 checksums.txt summarizes archive integrity.

Adds mixed-realistic E2E fixture, golden snapshots for all five
fixture services, and cmd/telemetry-inventory/README.md with install
+ usage instructions.

Closes the V1 surface; downstream services consume via:
  go install github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory@latest
EOF
)" --trailer "Generated-by: Claude" --trailer "AI-Model: claude-opus-4-7"
```

---

## 7. Final Validation Stage

### Task F.1: Full E2E run on all fixture services — 5 min

```bash
for svc in pure-tier1 pure-tier2 mixed-realistic broken-crosscut leak-spans; do
  echo "=== $svc ===";
  go run ./cmd/telemetry-inventory inventory \
    --output-md /tmp/$svc.md \
    cmd/telemetry-inventory/testdata/services/$svc;
  diff -u cmd/telemetry-inventory/testdata/golden/$svc.md /tmp/$svc.md && echo "OK $svc" || echo "DRIFT $svc";
done
```
Expected: `OK` for all 5.

### Task F.2: Goreleaser snapshot dry run — 3 min

```bash
make goreleaser
ls dist/ | sort
```
Expected: 4 binaries + 4 archives + checksums.txt.

### Task F.3: Lint + vet + tests across the new module — 3 min

```bash
make lint
go vet ./cmd/telemetry-inventory/...
go test -count=1 ./cmd/telemetry-inventory/...
```
Expected: all green.

### Task F.4: Constraint audit — 3 min

```bash
# Constraint #1 — no env vars for behavior toggles in CLI code
grep -rn "os.Getenv\|os.LookupEnv" cmd/telemetry-inventory/ || echo "OK: no env reads"

# Constraint #4 — zero-panic
grep -rn "panic(\|log.Fatal\|^\\s*Must" cmd/telemetry-inventory/ \
  | grep -v "regexp.MustCompile" \
  | grep -v "_test.go" \
  || echo "OK: zero-panic"
```
Expected: both checks emit `OK`.

### Task F.5: Documentation consistency check — 3 min

Confirm:
- `docs/plans/2026-05-07-telemetry-inventory-design.md` § references match implemented surface.
- `cmd/telemetry-inventory/README.md` documents install command, both subcommands, exit codes, schema version.
- Makefile help text mentions the three new targets.

### Task F.6: Open PR for Stage B — 5 min

```bash
gh pr create --title "feat(telemetry-inventory): introduce CLI for telemetry dictionary generation and CI drift gate" --body "$(cat <<'EOF'
## Summary
- Adds cmd/telemetry-inventory with seven analyzers (Counter/Histogram/Gauge across Tier 1+2+3, Span with End() pairing, LogField with PII risk, Framework auto-instrumentation, CrossCut for tenant/error/trace consistency).
- Deterministic Markdown + JSON renderer (idempotency + golden tests).
- inventory + verify subcommands; --update-golden / --debug / --verbose flags only (no env vars).
- Goreleaser flipped to build cross-platform binaries (linux/darwin × amd64/arm64).
- Five E2E fixture services + golden dictionaries.

## Test plan
- [ ] `make lint` clean
- [ ] `go test ./cmd/telemetry-inventory/...` clean
- [ ] `make goreleaser` produces 4 binaries + checksums.txt
- [ ] `make telemetry-dictionary` runs in <10s on mixed-realistic fixture
- [ ] `make verify-telemetry` returns exit 0 against committed golden
- [ ] `make verify-telemetry` returns exit 1 when a fixture is mutated
- [ ] Constraint audit: no env reads, no panic/log.Fatal outside MustCompile

Design: docs/plans/2026-05-07-telemetry-inventory-design.md
Pre-work: Stage A (commons/opentelemetry/metrics registry) merged separately.

Generated-by: Claude
AI-Model: claude-opus-4-7
EOF
)"
```

---

## 8. Decisions Log

Inherited from design §15 plus planning-specific:

| Decision | Resolution | Rationale |
|---|---|---|
| Home | `lib-commons/cmd/telemetry-inventory` + goreleaser update | Co-located with the primitives it parses; release cadence aligns naturally |
| Scope V1 | Full 7 angles + tests | Cold-start with no installed base; partial coverage would surface visible gaps for first users |
| Architecture | `golang.org/x/tools/go/analysis` framework | Cross-cutting via `Requires` declarative; community-standard; `analysistest` gold standard |
| Parser | `golang.org/x/tools/go/packages` + `go/types` | Mandatory for Tier 2 type resolution |
| Tier 3 detection | Self-describing registry in lib-commons + bidirectional reflection test | Single source of truth; compiler-enforced |
| Cross-cutting algorithm | Function-scoped bundling via `astutil.PathEnclosingInterval` | O(log n) enclosing-function lookup; framework guarantees Requires ordering |
| Renderer | Manual builder + sort hooks; idempotency tests + golden tests | `text/template` map iteration is random — fragile for byte-stable output |
| CLI surface | `inventory` + `verify` subcommands | Different intent → different commands; cleaner exit codes and flag sets |
| Behavior toggles | Flags only (`--update-golden`, `--debug`, `--verbose`) | Env vars hide state; explicit flags discoverable via `--help` |
| Schema versioning | Independent of lib-commons module version | Decoupled cadence; `verify` checks version match before semantic comparison |
| Pre-work | Separate lib-commons PR for `metrics.Helpers` registry + test | ~1 day; unblocks CLI and provides standalone value |
| Closure handling in cross-cutting | Bundle FuncLit emissions into enclosing FuncDecl | Correct for tenant consistency; potential FP for trace correlation in detached closures — accepted as known limitation |
| **Planning: CLI framework** | **stdlib `flag` + manual subcommand dispatch** | **lib-commons has zero existing CLI binaries; surface is small; minimal new dependency.** |
| **Planning: Goreleaser flip** | **Single `telemetry-inventory` build × {linux,darwin}×{amd64,arm64}, CGO_ENABLED=0, sha256 checksums** | **No Windows users; portable static binaries simplify install across CI runners.** |
| **Planning: Fixture module mode** | **Per-fixture `go.mod` with `replace` directive to repo root** | **Standard Go testdata-as-module pattern (gopls, golangci-lint); avoids network fetches; pins live source.** |
| **Planning: Analyzer concurrency** | **Default framework scheduling, no custom worker pool** | **YAGNI; framework load-tested by staticcheck/golangci-lint at scale; revisit only if profiling shows pressure.** |

---

## 9. Open Concerns surfaced during planning

1. **Fixture `go mod tidy` chicken-and-egg.** Tasks B.2.5, B.3.1, B.3.3, etc. call for `go mod tidy` inside fixture modules. The `replace` directive points back to the parent repo, so the fixture's tidy will resolve OTel via the parent's pin. This works in practice but means fixtures' `go.sum` files get committed and may drift with parent dep updates. Mitigation: add a Makefile target `tidy-fixtures` that re-runs `go mod tidy` across all fixtures, run it whenever lib-commons OTel deps update. Consider adding a CI check for fixture-go.sum drift in a follow-up.

2. **`analysistest` and external imports.** `analysistest` historically had trouble with fixtures that import non-stdlib packages. The `go.mod` + `replace` pattern is the documented workaround but adds friction. If a fixture import problem surfaces in B.2.3, fall back to defining minimal interface stubs inside the fixture rather than importing real OTel. The `// want` regexes still match because diagnostic output is text-only.

3. **Tier 3 helper registry coverage gap.** Currently 6 helpers, all metrics. If lib-commons adds Tier 3 helpers for spans or log fields in future, the design's `metrics.Helpers` shape doesn't accommodate them — we'd need a parallel `tracing.Helpers` and `log.Helpers`. Out of scope for V1 but worth flagging at design-doc revision time.

4. **CrossCut closure false-positives unmeasured.** Design §7 calls out the closure FP risk for trace correlation. Plan trusts the design — but if the `mixed-realistic` fixture surfaces FPs during B.12, there's no contingency in the plan. Suggest adding an extra task at B.9.5: "Audit closure FPs against mixed-realistic; if rate > 0, add per-finding `closure_detached: true` flag and require it to suppress tenant/correlation checks."

5. **`flag.NewFlagSet(... ContinueOnError)` and `-h` behavior.** Stdlib `flag` with `ContinueOnError` returns `flag.ErrHelp` on `-h`/`--help`. The current `runInventory`/`runVerify` stubs don't distinguish that from real errors — top-level `main` will exit 1. Minor UX papercut to fix in B.10.5 when wiring real impl: check `errors.Is(err, flag.ErrHelp)` and treat as exit 0.

---

## 10. Critical Files for Implementation

- `commons/opentelemetry/metrics/registry.go` (Stage A — registry seed; load-bearing for Tier 3 detection)
- `cmd/telemetry-inventory/main.go` (Stage B — entrypoint and subcommand dispatch)
- `cmd/telemetry-inventory/internal/orchestrator/orchestrator.go` (Stage B — packages.Load + custom checker; Dictionary aggregator)
- `cmd/telemetry-inventory/internal/analyzers/counter.go` (Stage B — vertical-slice template all other analyzers replicate)
- `cmd/telemetry-inventory/internal/renderer/markdown.go` (Stage B — determinism kernel; idempotency test catches the dangerous bug class)

---

## 11. Plan summary

- **Total tasks:** 6 (Stage A) + 50 (Stage B across 12 phases) + 6 (Final Validation) = **62 tasks**
- **Total estimated engineer-days:** ~1 (Stage A) + ~17–20 (Stage B) + ~0.5 (Final) = **~18.5–21.5 eng-days**
- **Concerns surfaced:** 5 (see §9) — all are non-blocking but worth tracking; concern #4 (CrossCut closure FP audit) and #1 (fixture `go.sum` drift) are most likely to require follow-up work post-merge.
- **No design issues found:** the 586-line design is internally consistent and all four open §14 questions are resolved in §3 with clear revisit criteria.
