# Huma OpenAPI Wrapper Promotion to lib-commons — Implementation Plan

> **For implementers:** Use ring:executing-plans (rolling wave: implement the
> detailed phase → user checkpoint → detail the next phase → implement → repeat).
> This document is the living source of truth — task elaboration for later
> phases is written back into it during execution.

**Goal:** Promote the Huma v2 + OpenAPI 3.1 Fiber-binding wrapper and the RFC 9457
`ProblemDetail` error model into `lib-commons/v5` as the single org-wide HTTP/OpenAPI
layer, then migrate `underwriter` and `br-sfn` off their per-repo copies onto it.

**Architecture:** Two new packages under `commons/net/http/`. A heavy
`openapi` package (Fiber v2 binding via `humafiber`, metadata, auto-mount
suppression, Bearer scheme, Scalar spec serving) and a light huma-only `problem`
package (RFC 9457 `Detail` type, `huma.NewError` override with central ≥500
scrub, generic domain-error mapper). The merged wrapper is the SUPERSET of the
two existing copies: br-sfn's richer feature surface + underwriter's safer
central 5xx-scrub. Topology decision (Fred, 2026-06-13): **root module**, not a
submodule — every Lerian service (midaz, reporter, matcher, …) will adopt the
wrapper, so isolating fiber/v3 from non-consumers protects an empty set. The
fiber/v3 module-graph drag is accepted and inherent to `humafiber`'s dual-major
import; Trivy-class scanners will flag future fiber/v3 CVEs on the unused v3 path
(waivable false positive), `govulncheck` stays quiet (v3 unreachable).

**Tech Stack:** Go 1.26.x, Huma v2 (`github.com/danielgtaylor/huma/v2 v2.38.0`),
`humafiber` adapter (Fiber v2 path, `NewV2WithGroup`), `gofiber/fiber/v2
v2.52.13`, `lib-observability/log`, testify, build-tag unit tests.

## Phase Overview

| Phase | Milestone | Epics | Status |
|-------|-----------|-------|--------|
| 1 | lib-commons exposes `openapi` + `problem`; unit-green ≥80%; beta released | 1.1, 1.2, 1.3 | Detailed |
| 2 | underwriter consumes lib-commons wrapper; local copy deleted; CI green | 2.1, 2.2 | Epic-level |
| 3 | br-sfn (SPB + SPI×4 binaries) consumes lib-commons wrapper; copies deleted; CI green | 3.1, 3.2, 3.3 | Epic-level |
| 4 | lib-commons stable `v5.6.0`; both consumers pinned to stable; replaces dropped | 4.1 | Epic-level |

---

## Design Reference (the contract every phase binds to)

### Package `commons/net/http/openapi` (imports fiber/v2, humafiber, huma, lib-observability/log)

```go
type Config struct {
    Title       string
    Version     string
    Description string
    Servers     []string
}

// Metadata + bind. Strips SchemaLinkTransformer (no $schema leak), clears
// OnAddOperation/CreateHooks/SchemasPath, suppresses auto-mount
// (OpenAPIPath="" DocsPath=""), binds via humafiber.NewV2WithGroup. No error
// policy here. Operations registered by callers.
func New(app *fiber.App, group fiber.Router, cfg Config) huma.API

// Registers the BearerAuth (http/bearer/JWT) security scheme. Nil-safe, idempotent.
func DeclareBearerAuth(api huma.API)

// Mounts {prefix}/openapi.json, {prefix}/openapi.yaml, {prefix}/docs (Scalar via
// CDN, per-route relaxed CSP). Snapshots spec once. Caller MUST gate on its
// Swagger.Enabled flag. Nil-safe on api; logs + skips on render failure.
func ServeSpec(app *fiber.App, api huma.API, logger libLog.Logger, prefix, title string)
```

### Package `commons/net/http/problem` (imports huma ONLY — no fiber)

```go
const BaseURI = "https://errors.lerian.studio/v1" // RFC 9457 type URI base, /v1-versioned catalog

// Uniform org-wide RFC 9457 body. Embeds huma.ErrorModel; adds omitempty Code.
// Satisfies huma.StatusError by promotion. Code stays absent for code-less rails.
type Detail struct {
    huma.ErrorModel
    Code string `json:"code,omitempty" doc:"Stable domain error code, e.g. SPB-3002" example:"SPB-3002"`
}

// Install overrides process-global huma.NewError so EVERY huma-constructed error
// is a *Detail. MERGE SEMANTICS (this is the crux):
//   - status >= 500: Detail scrubbed to a static "internal error", NO errs folded
//     (underwriter's central safety — closes the direct-Error5xx-raw-cause leak
//     br-sfn's old override left open).
//   - status  < 500: msg passed through, errs folded into Errors[] in order
//     (preserves Huma's native 422 field errors).
//   - Code/Type stay empty here (framework errors); omitempty drops Code, Type
//     stays at RFC default about:blank.
// sync.Once; deterministic; MUST run before any operation is registered on the
// runtime API or the spec-gen API or schema/runtime bodies diverge.
func Install()

// Generic domain mapper (br-sfn's, verbatim semantics). codeOf extracts
// (code,msg,ok); ok=false or nil err -> canonical sanitized 500 carrying
// fallbackCode. statusOf maps code->status. When code != "", sets Code and
// Type = BaseURI+"/"+code; 5xx detail sanitized to "internal error". Returns a
// concrete *Detail (independent of whether Install ran).
func MapError(err error, codeOf func(error) (code, msg string, ok bool), statusOf func(code string) int, fallbackCode string) error
```

**Convergence consequences (declared, greenfield — no external consumers):**
- underwriter's error SCHEMA gains an optional `code` property (never populated → omitted on the wire). Uniform org-wide shape is the goal, costs underwriter nothing.
- br-sfn's framework-error 5xx path now scrubs centrally (strictly safer; was a latent leak).
- Both repos delete their local copies entirely (underwriter `internal/shared/adapters/openapi`; br-sfn `shared/openapi` + `shared/humaerr`).

**Local dev workflow (de-risks the release gate):** Phases 2 & 3 develop against
the unreleased wrapper via `replace github.com/LerianStudio/lib-commons/v5 =>
../lib-commons` in each consumer's go.mod. The replace is DROPPED and the real
version pinned before each consumer PR merges (Phase 4 pins stable).

---

## Phase 1 — lib-commons wrapper (foundation)

**Milestone:** `go test -tags=unit ./commons/net/http/openapi/... ./commons/net/http/problem/...`
green at ≥80% coverage; `make lint sec vulncheck` green; merged to `develop`;
beta `v5.6.0-beta.N` released.

### Epic 1.1: `problem` package — RFC 9457 error model

**Goal:** `commons/net/http/problem` exposes `Detail`, `BaseURI`, `Install`, `MapError` with the merged 5xx-scrub semantics, huma-only deps.
**Scope:** `commons/net/http/problem/` (new dir).
**Dependencies:** none (huma is added to go.mod in Epic 1.3, but problem is huma-only so go.mod edit can lead here).
**Done when:** unit tests prove (a) ≥500 scrubbed + no errs folded; (b) <500 msg passthrough + errs folded preserving order; (c) `Detail` JSON omits empty Code, includes it when set; (d) `MapError` nil/unrecognized → sanitized 500 w/ fallbackCode; (e) coded path sets Code+Type, 5xx detail sanitized; (f) `*Detail` satisfies `huma.StatusError`.
**Status:** Pending

#### Task 1.1.1: Author the `problem` package

- [ ] Done

**Context:** Port and MERGE two existing sources. br-sfn `shared/humaerr/{problemdetail,install,humaerr}.go` supplies `ProblemDetail`+`Code`, `InstallProblemDetail`/`newProblemError`, `MapDomainError`, `ProblemBaseURI`. underwriter `internal/shared/adapters/openapi/openapi.go:46-57` supplies the central ≥500 scrubber `installServerErrorScrubber`. The merge: the override (`Install`) must do BOTH — build a `*Detail` (br-sfn) AND scrub ≥500 centrally (underwriter). br-sfn's `newProblemError` does NOT scrub 5xx today; the promoted version MUST.

**Implementation vision:** New package `problem` (package name `problem`). Files: `problem.go` (`Detail`, `BaseURI`), `install.go` (`Install`, unexported `newError` override), `maperror.go` (`MapError`, unexported `newProblem`). Rename br-sfn symbols to drop stutter: `ProblemDetail`→`Detail`, `ProblemBaseURI`→`BaseURI`, `InstallProblemDetail`→`Install`, `MapDomainError`→`MapError`. The `newError` override (installed over `huma.NewError`): for `status >= http.StatusInternalServerError` return a `*Detail` with `Detail:"internal error"` and NO folded errs; else fold errs[] (skip nil, honor `huma.ErrorDetailer`) and pass msg through. `sync.Once` guard. `MapError` keeps br-sfn semantics verbatim (returns concrete `*Detail`, scrubs 5xx detail, sets Code+Type when code != "").

**Files:**
- Create: `commons/net/http/problem/problem.go`
- Create: `commons/net/http/problem/install.go`
- Create: `commons/net/http/problem/maperror.go`
- Test: `commons/net/http/problem/problem_test.go`, `install_test.go`, `maperror_test.go` (`//go:build unit`)

**Verification:** `go test -tags=unit ./commons/net/http/problem/... -count=1 -race` — all pass; `go test -tags=unit -cover` ≥80%.

**Done when:** the six "Done when" assertions of Epic 1.1 hold; `golangci-lint run` clean on the package (errorlint, nilnil, gosec).

#### Task 1.1.2: Schema-shape parity test

- [ ] Done

**Context:** br-sfn has `problemdetail_schema_test.go` + `crossrail_parity_test.go` asserting the generated OpenAPI error schema carries `code`. Promote an equivalent so the org-wide shape is locked.

**Implementation vision:** Register a trivial throwaway operation on a Huma API after `problem.Install()`, render `api.OpenAPI()`, assert the error component schema includes the `code` property and the `Detail` shape (status/title/detail/errors/code). Keep it huma-only (no fiber) by using `humatest` or a minimal config — but per the Huma migration memory, prefer the real adapter path; here a schema-only assertion via `huma.OpenAPI()` on a config-built API avoids needing Fiber.

**Files:**
- Test: `commons/net/http/problem/schema_test.go` (`//go:build unit`)

**Verification:** `go test -tags=unit ./commons/net/http/problem/... -run Schema -count=1`.

**Done when:** the generated error schema exposes `code` as optional; test fails if `Detail` loses the field.

### Epic 1.2: `openapi` package — Fiber binding + spec serving

**Goal:** `commons/net/http/openapi` exposes `Config`, `New`, `DeclareBearerAuth`, `ServeSpec`.
**Scope:** `commons/net/http/openapi/` (new dir).
**Dependencies:** go.mod must gain huma + humafiber + (already present) fiber/v2 — sequence with Epic 1.3 or do the go.mod add first.
**Done when:** unit tests prove (a) `New` suppresses auto-mount (no /openapi or /docs route registered by the wrapper); (b) transformers/hooks cleared (response body serializes without `$schema`); (c) servers populated when supplied; (d) `DeclareBearerAuth` adds the scheme, nil-safe; (e) `ServeSpec` mounts the three routes under prefix, nil-safe on api, skips+logs on render failure.
**Status:** Pending

#### Task 1.2.1: Author the `openapi` package

- [ ] Done

**Context:** Port br-sfn `shared/openapi/openapi.go` (the richer copy: it already has `New`, `DeclareBearerAuth`, `ServeSpec`, Scalar template, CSP middleware) verbatim-ish. underwriter's `New` is identical minus the serve/bearer helpers, so br-sfn's file is the base. The ONLY behavioral note: `New` applies no error policy — error policy lives entirely in `problem.Install()`, called by the consumer's bootstrap.

**Implementation vision:** New package `openapi`. `openapi.go`: `Config`, `New` (verbatim from br-sfn), `DeclareBearerAuth`, `ServeSpec`, `scalarCSP`/`docsHTMLTemplate`/`scalarCSPMiddleware`/`docsHTML`. Keep the doc comment's "no error policy" contract. Logger param is `libLog.Logger` from lib-observability (already a lib-commons dep). Do NOT import `problem` here (layering: binding must not depend on error model).

**Files:**
- Create: `commons/net/http/openapi/openapi.go`
- Test: `commons/net/http/openapi/openapi_test.go` (`//go:build unit`)

**Verification:** `go test -tags=unit ./commons/net/http/openapi/... -count=1 -race`; test through a real `*fiber.App` via `app.Test` (NOT humatest — Huma issue #935).

**Done when:** Epic 1.2 "Done when" assertions hold; lint clean (bodyclose, noctx, gosec on the CDN CSP string).

### Epic 1.3: go.mod, CI scope, release

**Goal:** lib-commons builds with the new deps, passes all gates, lands on develop, cuts a beta.
**Scope:** `go.mod`, `go.sum`, `.github/workflows/pr-validation.yml` (PR-title scope), CHANGELOG via semantic-release.
**Dependencies:** Epics 1.1, 1.2 code complete.
**Done when:** `go mod tidy` adds `huma/v2`, `humafiber` (and fiber/v3 indirect); `make test-unit lint sec vulncheck` green; coverage ≥80%; PR title scope accepted; merged to develop; `v5.6.0-beta.N` tag exists.
**Status:** Pending

#### Task 1.3.1: go.mod + PR-title scope + verify

- [ ] Done

**Context:** lib-commons root go.mod has fiber/v2 but no huma. PR-title validation (`.github/workflows/pr-validation.yml`) gates scope; `openapi`/`problem`/`net` must be accepted. Coverage gate is 80% unit (`go-combined-analysis.yml`).

**Implementation vision:** `go get github.com/danielgtaylor/huma/v2@v2.38.0` (match the consumers' pinned version to avoid graph skew), `go mod tidy`. Confirm fiber/v3 lands as indirect. Add `openapi` (and/or `problem`/`net`) to the allowed PR scope list if not already present; if `net` is the umbrella scope, title `feat(net): add Huma OpenAPI wrapper + RFC 9457 problem model`. Run the full local gate set. Open PR → develop.

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `.github/workflows/pr-validation.yml` (only if scope not already allowed)

**Verification:** `make test-unit && make lint && make sec && make vulncheck` all green; `go list -m github.com/gofiber/fiber/v3` shows it as indirect.

**Done when:** PR green on all gates; merged; beta released.

---

## Phase 2 — underwriter adoption

**Milestone:** underwriter serves the same endpoints through the lib-commons wrapper; `internal/shared/adapters/openapi` deleted; CI green.

### Epic 2.1: Swap underwriter onto lib-commons openapi + problem

**Goal:** bootstrap uses `openapi.New`/`ServeSpec`/`DeclareBearerAuth` + `problem.Install`; local wrapper + scrubber removed.
**Scope:** `internal/bootstrap/` (routes, openapi mount, error handler wiring), `internal/shared/adapters/openapi/` (delete), go.mod.
**Dependencies:** Phase 1 beta (or local `replace ../lib-commons`).
**Done when:** the bootstrap spec-serving (Swagger.Enabled gate) routes through `openapi.ServeSpec`; `problem.Install()` runs before operation registration; the deleted package leaves no references; `make test-unit` green; the committed-artifact drift gate + oneOf union guard still pass.
**Status:** Pending

### Epic 2.2: Reconcile underwriter error-model + drift tests

**Goal:** underwriter's RFC 9457 assertions account for the optional `code` field; spec drift gate regenerates clean.
**Scope:** bootstrap spec tests (`huma_spec_test.go`, `routes_br_test.go`), any error-shape assertions, committed `docs/openapi/*.yaml`.
**Dependencies:** Epic 2.1.
**Done when:** regenerated spec committed; drift gate green; error-shape tests assert title/status/detail (NOT type — Huma omitempty) and tolerate optional `code`.
**Status:** Pending

---

## Phase 3 — br-sfn adoption (SPB + SPI×4)

**Milestone:** SPB and all SPI binaries serve through the lib-commons wrapper; `shared/openapi` + `shared/humaerr` deleted; CI green across services.

### Epic 3.1: Swap SPB onto lib-commons

**Goal:** SPB bootstrap/server uses `openapi.*` + `problem.Install`; SPB's `codeOf`/`statusOf`/`fallbackCode` ("SPB-NNNN" taxonomy) wired into `problem.MapError`.
**Scope:** `services/spb/internal/adapters/http/` (server, routes, error_handler, huma_operations), `services/spb/go.mod`.
**Dependencies:** Phase 1 beta (or local replace).
**Done when:** SPB runtime + spec-gen call `problem.Install()` once; domain errors route through `problem.MapError(err, spbCodeOf, spbStatusOf, "SPB-…")`; `shared/openapi`+`shared/humaerr` references removed for SPB; SPB tests green.
**Status:** Pending

### Epic 3.2: Swap SPI (4 binaries) onto lib-commons

**Goal:** spi/core/brcode/dict binaries each construct their own `openapi.New` API + call `problem.Install`; SPI's empty-code path preserved (`fallbackCode=""`).
**Scope:** `services/spi/internal/shared/bootstrap/`, the four `*/app/huma_spec.go`, per-binary `cmd/*/main.go`, `services/spi/go.mod`.
**Dependencies:** Phase 1 beta (or local replace).
**Done when:** all four specs regenerate; per-binary `DocsPrefix`/`OpenAPIConfig` preserved; SPI integration suites green (the 4 modified test files in the working tree reconciled).
**Status:** Pending

### Epic 3.3: Delete br-sfn shared copies + cross-rail parity

**Goal:** `shared/openapi` + `shared/humaerr` removed; the crossrail parity test promoted/retargeted at the lib-commons types.
**Scope:** `services/*/...` references, `shared/` deletion, parity test relocation.
**Dependencies:** Epics 3.1, 3.2.
**Done when:** no `shared/openapi`/`shared/humaerr` imports remain; parity coverage exists against lib-commons `problem.Detail`; full br-sfn CI green.
**Status:** Pending

---

## Phase 4 — stable release + pin

### Epic 4.1: Promote stable, pin both consumers

**Goal:** lib-commons `v5.6.0` stable; underwriter + br-sfn drop `replace`, pin the stable version.
**Scope:** lib-commons main release; both consumers' go.mod.
**Dependencies:** Phases 2 & 3 merged.
**Done when:** `v5.6.0` on main; both consumers pinned to `v5.6.0` (no replace, no beta); both CIs green on the pinned version.
**Status:** Pending
