# Plan: Simplify `commons/systemplane`

## Context

### Why we're doing this

`commons/systemplane` is lib-commons's runtime-configuration plane — database-backed
hot-reload of knobs, with audit history, optimistic concurrency, change feeds, and
component-granular bundle rebuilds across two backends. It is **~37,000 LOC across
198 files in 10 subpackages with 15 interfaces**. Production LOC (~12K) is half the
test LOC (~25K) — a strong signal that the abstractions generate test surface without
paying back product surface.

The team finds the current design "quite complicated" and cannot derive intuitions
for the "matrix of possibilities" the design opens up. Concretely, every setting is
modeled across **four orthogonal identity axes** (Kind × Scope × Subject × Key), writes
are classified into **five ApplyBehavior escalation levels** that cascade to the
strongest level in a batch, reconcilers run through **three ordered phases**
(StateSync → Validation → SideEffect), and the supervisor maintains an atomic
`(snapshot, bundle)` pair with an **incremental bundle rebuild** subsystem that diffs
components via `reflect.DeepEqual`. Only **two downstream apps** use the package.

The core diagnostic: the library tried to be correct for every imaginable knob
(connection strings included) instead of narrowing to the specific knobs that actually
benefit from hot-reload. That missing upstream decision compounded outward into per-key
apply behaviors, per-key mutability, per-key scope, per-key component, per-key
redaction — a big generic lattice instead of a small opinionated system.

### What we're building

A **tight, opinionated, dual-backend runtime-config library** focused on the actual
business problem: *let ops change a small set of specific knobs at runtime without
a pod restart.* Everything else (DB DSNs, secrets, TLS paths, identity, telemetry
init) moves back to env-vars where the pod lifecycle enforces correctness.

### The env-var vs. hot-reload rule

A knob belongs in **env-vars (bootstrap-only)** if any of these are true:

1. Changing it implies tearing down a connection pool (DB DSNs, Redis URLs, broker hosts).
2. It is a secret (belongs in a secret manager, not a config DB).
3. It is cryptographic material or a cert path (`commons/certificate` already hot-reloads PEMs).
4. It is identity (service name, env name, region, cluster).
5. It is bound at telemetry SDK init (OTEL exporter endpoint, sampling rate).
6. It affects HTTP server identity (listen address, TLS policy, body limit).
7. Blast radius of a wrong value is a service outage rather than a single failed request.

A knob belongs in the **hot-reload plane** if all of these are true:

1. It is re-read per request / per operation, not held in a long-lived resource.
2. A wrong value fails *that call only*, not the service.
3. It is something ops actually want to poke during incidents without a deploy.

Concrete hot-reload inventory: `log.level`, feature flags, rate limits, circuit-breaker
thresholds, retry knobs, per-request timeouts, business thresholds, telemetry
sampling rates, CORS allow-lists, and `database/sql` pool sizing (which `SetMaxOpenConns`
supports as a runtime call).

### Intended outcome

- Public API of **~10 methods, 1 type, 2 option constructors, 2 option types** — teachable in a 5-minute Loom video.
- **~2K LOC production**, ~3.5K LOC test. Down from 12K/25K (~83% reduction).
- Dual-backend preserved (Postgres via LISTEN/NOTIFY, MongoDB via change-streams with polling fallback).
- Breaking changes acceptable; two downstream apps will be migrated in lockstep.

---

## Proposed Architecture

> **Non-normative pseudocode.** The Go snippets in this section are the pre-implementation
> proposal and are NOT guaranteed to compile. For the actual shipped API surface (method
> names, option constructors, tenant-scoped methods added post-plan), see
> `CLAUDE.md` §"API invariants to respect" → **Runtime configuration (`commons/systemplane`)**
> and `MIGRATION_MAP.md`.

### Public package (`commons/systemplane/`)

```go
package systemplane

// Client is the runtime-config handle. Reads are nil-receiver safe.
type Client struct { /* unexported */ }

// NewPostgres wires a Client backed by a Postgres connection + LISTEN/NOTIFY.
func NewPostgres(db *sql.DB, opts ...Option) (*Client, error)

// NewMongoDB wires a Client backed by a MongoDB database + change streams
// (or polling if WithPollInterval is set). Requires a replica set for change streams.
func NewMongoDB(client *mongo.Client, database string, opts ...Option) (*Client, error)

// Register declares a key, its default, and optional validator.
// Namespace is a free-text label: "global", "tenant:acme", "feature-flags".
// Must be called before Start(); returns ErrAlreadyStarted otherwise.
func (c *Client) Register(namespace, key string, defaultValue any, opts ...KeyOption) error

// Start hydrates initial values and begins listening for changes. Idempotent.
func (c *Client) Start(ctx context.Context) error

// Close unsubscribes and releases resources. Idempotent.
func (c *Client) Close() error

// Get returns the current value. Nil-safe; returns (zero, false) if client/key unset.
func (c *Client) Get(namespace, key string) (any, bool)

// Typed accessors — all nil-safe, all return zero values on miss.
func (c *Client) GetString(namespace, key string) string
func (c *Client) GetInt(namespace, key string) int
func (c *Client) GetBool(namespace, key string) bool
func (c *Client) GetFloat64(namespace, key string) float64
func (c *Client) GetDuration(namespace, key string) time.Duration

// Set writes a new value. Last-write-wins. Notifies subscribers after commit.
func (c *Client) Set(ctx context.Context, namespace, key string, value any, actor string) error

// OnChange registers a callback. Returns an unsubscribe func.
// Callbacks are invoked one-at-a-time with panic recovery; slow callbacks backpressure.
func (c *Client) OnChange(namespace, key string, fn func(newValue any)) (unsubscribe func())

// Options.
type Option func(*clientConfig)
func WithLogger(l log.Logger) Option
func WithTelemetry(t *opentelemetry.Telemetry) Option
func WithListenChannel(name string) Option        // Postgres only; default "systemplane_changes"
func WithPollInterval(d time.Duration) Option     // MongoDB only; enables polling mode
func WithDebounce(d time.Duration) Option         // default 100ms
func WithCollection(name string) Option           // MongoDB collection name; default "systemplane_entries"
func WithTable(name string) Option                // Postgres table name; default "systemplane_entries"

type KeyOption func(*keyDef)
func WithDescription(s string) KeyOption
func WithValidator(fn func(any) error) KeyOption
func WithRedaction(policy RedactPolicy) KeyOption // RedactNone | RedactMask | RedactFull
```

### Optional admin subpackage (`commons/systemplane/admin/`)

```go
package admin

// Mount registers HTTP admin routes on a Fiber router.
// Default path prefix is "/system"; routes are:
//   GET    /system/:namespace          — list namespace entries
//   GET    /system/:namespace/:key     — read one entry
//   PUT    /system/:namespace/:key     — write one entry
func Mount(router fiber.Router, c *systemplane.Client, opts ...MountOption)

type MountOption func(*mountConfig)
func WithPathPrefix(p string) MountOption
func WithAuthorizer(fn func(fiber.Ctx, action string) error) MountOption
func WithActorExtractor(fn func(fiber.Ctx) string) MountOption
```

### Internal store abstraction (`commons/systemplane/internal/store/`)

The only interface we keep — a tiny one that lets Postgres and MongoDB coexist
without leaking into the public API.

```go
package store

// Entry is the persisted shape of a single key.
type Entry struct {
    Namespace string
    Key       string
    Value     []byte    // JSON-encoded
    UpdatedAt time.Time
    UpdatedBy string
}

// Event is what a changefeed delivers.
type Event struct {
    Namespace string
    Key       string
}

// Store is implemented by internal/postgres and internal/mongodb.
type Store interface {
    List(ctx context.Context) ([]Entry, error)            // hydrate at Start()
    Get(ctx context.Context, ns, key string) (Entry, bool, error)
    Set(ctx context.Context, e Entry) error               // last-write-wins; notifies via changefeed
    Subscribe(ctx context.Context, handler func(Event)) error // blocks until ctx done
    Close() error
}
```

### File layout (target)

```
commons/systemplane/
  doc.go                      -- package documentation
  client.go                   -- Client type + lifecycle (New*, Start, Close)
  register.go                 -- Register + keyDef + validation
  get.go                      -- Get + typed accessors
  set.go                      -- Set (validates, persists, notifies)
  onchange.go                 -- OnChange + notification loop + debounce + panic recovery
  options.go                  -- all Option and KeyOption constructors
  redact.go                   -- RedactPolicy + apply
  errors.go                   -- sentinel errors
  client_test.go              -- unit tests against in-memory store
  contract_test.go            -- invokes systemplanetest against both backends

  admin/
    admin.go                  -- Mount + handlers + DTOs
    admin_test.go

  internal/
    store/
      store.go                -- Store interface + Entry + Event
    postgres/
      postgres.go              -- Postgres Store impl (~300 LOC)
      ddl.sql                  -- embedded DDL
      postgres_integration_test.go
    mongodb/
      mongodb.go               -- MongoDB Store impl (~350 LOC, change-stream + optional poll)
      mongodb_integration_test.go
    debounce/
      debounce.go              -- shared trailing-edge debouncer (~80 LOC)
      debounce_test.go

  systemplanetest/
    contract.go                -- shared contract tests runnable against any Store
```

Total target production LOC: **~2,000**. Test LOC: **~3,500**.

---

## Scope — Delete List

Everything below is **removed** in the new package. For each item, a one-line
justification (evidence already documented in the prior analysis).

| Path | Size | Why it goes |
|---|---|---|
| `commons/systemplane/domain/kind.go` | 46 LOC | Kind/Scope/Subject replaced by free-text `namespace` |
| `commons/systemplane/domain/scope.go` | 46 LOC | same |
| `commons/systemplane/domain/target.go` | 56 LOC | same |
| `commons/systemplane/domain/apply_behavior.go` | 84 LOC | Everything registered is hot-reloadable; no escalation |
| `commons/systemplane/domain/reconciler_phase.go` | 86 LOC | Reconciler phases gone; `OnChange` is the only hook |
| `commons/systemplane/domain/revision.go` | 17 LOC | Last-write-wins; no optimistic concurrency |
| `commons/systemplane/domain/snapshot.go` | 106 LOC | Replaced by `map[string]any` under RWMutex inside Client |
| `commons/systemplane/domain/bundle.go` | 13 LOC | Bundle abstraction gone |
| `commons/systemplane/domain/snapshot_from_keydefs.go` | 69 LOC | gone |
| `commons/systemplane/domain/coercion_helpers.go` | 366 LOC | Much simpler typed accessors (~50 LOC) |
| `commons/systemplane/domain/backend_kind.go` | 46 LOC | No backend factory registry |
| `commons/systemplane/service/*` | ~2,100 LOC | Manager/Supervisor/SnapshotBuilder collapse into `Client` |
| `commons/systemplane/service/component_diff.go` | 215 LOC | No bundles to diff |
| `commons/systemplane/service/escalation.go` | 70 LOC | No escalation |
| `commons/systemplane/service/shutdown.go` | 96 LOC | `Client.Close` replaces |
| `commons/systemplane/registry/*` | 275 LOC | Registry becomes `map[string]keyDef` field |
| `commons/systemplane/catalog/*` | 544 LOC | Consumer apps own their key list |
| `commons/systemplane/bootstrap/*` | ~700 LOC | Replaced by direct constructors `NewPostgres` / `NewMongoDB` |
| `commons/systemplane/bootstrap/builtin/*` | ~160 LOC | No factory registry |
| `commons/systemplane/adapters/store/secretcodec/*` | 236 LOC | Secrets live in secret managers |
| `commons/systemplane/adapters/store/storetest/*` | 781 LOC | Replaced by slimmer `systemplanetest/contract.go` (~400 LOC) |
| `commons/systemplane/adapters/changefeed/feedtest/*` | 1,034 LOC | same |
| `commons/systemplane/adapters/store/postgres/*` | ~1,100 LOC | Rewritten at ~300 LOC in `internal/postgres/` |
| `commons/systemplane/adapters/store/mongodb/*` | ~1,000 LOC | Rewritten at ~350 LOC in `internal/mongodb/` |
| `commons/systemplane/adapters/changefeed/postgres/*` | 508 LOC | Merged into `internal/postgres/postgres.go` |
| `commons/systemplane/adapters/changefeed/mongodb/*` | 555 LOC | Merged into `internal/mongodb/mongodb.go` |
| `commons/systemplane/adapters/changefeed/debounce*.go` | ~380 LOC | Reimplemented at ~80 LOC in `internal/debounce/` |
| `commons/systemplane/adapters/http/fiber/*` | ~978 LOC | Rewritten at ~250 LOC in `admin/admin.go` |
| `commons/systemplane/swagger/*` | 175 LOC | No bespoke swagger merge; regenerate from handler tags if ever needed |
| `commons/systemplane/ports/history.go` | 38 LOC | Audit via telemetry/logs, not a bespoke history table |
| `commons/systemplane/ports/bundle_factory.go` | 51 LOC | No bundles |
| `commons/systemplane/ports/reconciler.go` | 31 LOC | `OnChange` replaces |
| `commons/systemplane/ports/authorizer*.go` | 114 LOC | Authorizer becomes a `MountOption` on admin router |
| `commons/systemplane/ports/identity*.go` | 111 LOC | Actor extraction via `MountOption` |
| `commons/systemplane/testutil/*` | 494 LOC | Replaced by thin in-memory Store for tests |

**Deletes ~9,000 LOC production + ~22,000 LOC tests.**

---

## Reuse Existing Utilities

No new primitives get invented. Every foundational concern maps to an existing
lib-commons package:

| Concern | Reused package | Path |
|---|---|---|
| Structured logging | `commons/log` | `/commons/log/` |
| Spans / redaction / attributes | `commons/opentelemetry` | `/commons/opentelemetry/` |
| Exponential backoff with jitter | `commons/backoff.ExponentialWithJitter` | `/commons/backoff/backoff.go` |
| Panic recovery in goroutines | `commons/runtime.SafeGoWithContext`, `RecoverAndLog` | `/commons/runtime/` |
| Sensitive field detection | `commons/security.IsSensitiveField` | `/commons/security/security.go` |
| Nil-safety on Get paths | existing convention in `commons/dlq`, `commons/webhook`, `commons/idempotency` | — |
| Fiber route mounting + auth | existing convention in `commons/net/http` | `/commons/net/http/` |
| Package constants (OTEL labels, redaction strings) | `commons/constants.SanitizeMetricLabel` | `/commons/constants/` |

Two of the current `systemplane` adapters already use `backoff.ExponentialWithJitter`
(`commons/systemplane/adapters/changefeed/postgres/feed_subscribe.go`) — we keep that
pattern in the new `internal/postgres/postgres.go` for LISTEN reconnect.

---

## Critical Files — Concrete Modifications

### Created (15 files)

- `commons/systemplane/doc.go`
- `commons/systemplane/client.go` — `Client`, `NewPostgres`, `NewMongoDB`, `Start`, `Close`
- `commons/systemplane/register.go` — `Register`, `keyDef`, in-memory registry
- `commons/systemplane/get.go` — `Get`, `GetString`, `GetInt`, `GetBool`, `GetFloat64`, `GetDuration`
- `commons/systemplane/set.go` — `Set` + validation + persistence + notification
- `commons/systemplane/onchange.go` — `OnChange` + dispatch loop + panic recovery via `commons/runtime`
- `commons/systemplane/options.go` — all `Option` / `KeyOption` constructors
- `commons/systemplane/redact.go` — `RedactPolicy` + `applyRedaction`
- `commons/systemplane/errors.go` — sentinel errors
- `commons/systemplane/admin/admin.go` — `Mount`, handlers, DTOs
- `commons/systemplane/internal/store/store.go` — `Store` interface, `Entry`, `Event`
- `commons/systemplane/internal/postgres/postgres.go` — PG impl, LISTEN/NOTIFY, embedded DDL
- `commons/systemplane/internal/mongodb/mongodb.go` — Mongo impl, change streams + optional polling
- `commons/systemplane/internal/debounce/debounce.go` — trailing-edge debouncer
- `commons/systemplane/systemplanetest/contract.go` — backend-agnostic contract tests

### Deleted (entire directories)

- `commons/systemplane/adapters/` (all subtrees)
- `commons/systemplane/bootstrap/`
- `commons/systemplane/catalog/`
- `commons/systemplane/domain/`
- `commons/systemplane/ports/`
- `commons/systemplane/registry/`
- `commons/systemplane/service/`
- `commons/systemplane/swagger/`
- `commons/systemplane/testutil/`

### Docs

- `CLAUDE.md` — add `systemplane` under **API invariants** (not currently listed; the absence is itself a signal that it wasn't a first-class package). New entry describes the 10-method public surface.
- `README.md` — mention the package once with link to `doc.go`.
- Update `MIGRATION_MAP.md` with the v4 → v4.next symbol deletions.
- `docs/plans/pure-crunching-noodle.md` — this file; kept as historical record.

---

## Status

**Shipped.** The simplification landed in `commons/systemplane/` and its subpackages.
For the landed public API (including post-plan additions such as `RegisterTenantScoped`,
`SetForTenant`, `WithLazyTenantLoad`, and the admin tenant routes), see
`CLAUDE.md` §"Runtime configuration (`commons/systemplane`)". Deleted symbols are
recorded in `MIGRATION_MAP.md`. Downstream app migration is tracked outside this
repository.

---

## Risks & Open Questions

1. **Tenant enforcement.** Collapsing `Scope=Tenant + SubjectID` into a free-text
   `namespace` string removes library-level enforcement. Mitigation: the admin router
   accepts a `WithAuthorizer` hook; consumers can reject unauthorized namespaces there.
   **This is the single highest-risk change.** Accepted as a known trade-off; if a
   downstream app relied on library-level tenant enforcement, we reintroduce it when
   it surfaces during Phase 8 migration.
2. **Audit history.** If either downstream app uses the current history endpoints for
   compliance evidence, deleting the history table is a regression. Mitigation: route
   writes to a standard audit stream (Kafka / log pipeline / dedicated audit service)
   if needed; do not rebuild it inside `systemplane`.
3. **Optimistic concurrency removal.** If a downstream app has an automated writer
   racing with manual admin writes, last-write-wins can silently overwrite. Accepted
   as unlikely; if it surfaces in migration, the mitigation is application-side
   coordination (e.g., single-writer ownership) rather than re-adding revisions.
4. **MongoDB replica-set requirement.** Change streams need a replica set. Preserving
   polling mode (`WithPollInterval`) keeps standalone deployments supported; consumers
   opt in at `NewMongoDB(...)` time.
5. **Env-var migration.** Downstream apps must update their deployment manifests to
   read connection-level settings from standard env-vars (already consumed by
   `commons/postgres.Config` etc.) rather than `SYSTEMPLANE_*`. Pure config-map change,
   no code change, but coordination required.
6. **No deprecation window.** Because this is a breaking rewrite and only two
   consumers exist, we ship as a minor-version break. No `Deprecated:` shims.

---

## Verification

### Unit / static checks

- `make lint` — clean.
- `make vet` — clean.
- `make sec` — clean (gosec; SARIF output saved to CI artifacts).
- `make test-unit PKG=./commons/systemplane/...` — >=90% coverage on the
  `systemplane` package proper.
- `make format && make tidy` — no diffs.

### Integration checks

- `make test-integration PKG=./commons/systemplane/internal/postgres/...` —
  Postgres-backed contract tests via testcontainers.
- `make test-integration PKG=./commons/systemplane/internal/mongodb/...` —
  MongoDB-backed contract tests via testcontainers; one run with change streams,
  one with polling (env flag `SYSTEMPLANE_MONGO_POLL=1`).

### End-to-end smoke test (scripted, in `systemplanetest/e2e_test.go`)

Build tag `e2e`. For each backend:

1. `NewPostgres(db)` or `NewMongoDB(client, db)`.
2. `Register("global", "log.level", "info")`.
3. `Register("global", "rate_limit.rps", 100)`.
4. `OnChange("global", "log.level", ...)` registers a callback whose side effect
   is writing to a channel.
5. `Start(ctx)`.
6. `Set(ctx, "global", "log.level", "debug", "actor-test")`.
7. Assert callback fires within 500ms with `newValue == "debug"`.
8. Assert `GetString("global", "log.level") == "debug"` immediately after.
9. Spin up a second `Client` against the same backend, call `Start`, and assert
   it sees the current value `"debug"` after hydration.
10. Write via the second client, assert the first client's `OnChange` fires.

### CI pipeline

- `make ci` must pass locally before PR open.
- PR CI must run both backend integration suites (Docker available on CI runners).
- Manual verification against a staging replica of one downstream app before merging.

### Manual admin smoke test

- Mount `admin.Mount(router, client)` in a throwaway `main.go`.
- `curl -X PUT http://localhost:8080/system/global/log.level -d '{"value":"debug"}'` — 200.
- `curl http://localhost:8080/system/global/log.level` — returns `"debug"`.
- `curl http://localhost:8080/system/global` — lists entries.

---

## Non-goals

- We are **not** rebuilding audit history inside `systemplane`.
- We are **not** preserving the four-axis (Kind × Scope × Subject × Key) identity model.
- We are **not** keeping `ApplyBehavior`, reconciler phases, or bundle supervision.
- We are **not** providing a deprecation shim for v4 consumers — the two downstream
  apps will be migrated directly.
- We are **not** writing a migration tool for existing rows — the schema is new and
  the prior tables will be dropped by the downstream teams as part of their migration.
