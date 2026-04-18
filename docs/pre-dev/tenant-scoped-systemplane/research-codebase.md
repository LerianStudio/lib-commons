# Research: Adjacent Patterns for Tenant-Scoped Systemplane

**Research mode:** MODIFICATION. Target: additive extension of `commons/systemplane`.
**Evolution doc read:** `/Users/fredamaral/repos/lerianstudio/plugin-br-bank-transfer/LIB-COMMONS-EVOLUTION.md`

---

## 1. Tenant-Scoping Patterns Across lib-commons

lib-commons has **three** distinct storage patterns for tenant scoping and **no canonical DB-column pattern** shared across packages. Each storage layer invented its own — the design of tenant-scoped systemplane is a greenfield choice.

| Pattern | Storage shape | Example | File:Line |
|---|---|---|---|
| **Colon-delimited key prefix** | `"<prefix><tenantID>:<resource>"` | DLQ Redis keys `"dlq:tenant-abc:outbound"` | `commons/dlq/keys.go:19-42` |
| **`tenant:` well-known prefix** | `"tenant:<tenantID>:<key>"` (valkey helper) | Valkey/Redis GetKey helper | `commons/tenant-manager/valkey/keys.go:15-31` |
| **DB column `tenant_id`** | Compound PK `(tenant_id, id)`, all indices tenant-prefixed | outbox column-per-tenant | `commons/outbox/postgres/migrations/column/000001_outbox_events_column.up.sql:10-32`, `commons/outbox/postgres/column_resolver.go:69,87` |
| **Schema-per-tenant** | `SET LOCAL search_path` per tx | outbox schema resolver | `commons/outbox/postgres/schema_resolver.go:65-97` |
| **DB-per-tenant** | Connection manager fetches per-tenant DSN | tenant-manager postgres | `commons/tenant-manager/postgres/manager.go:84-120` |

**Canonical context extraction** lives in one place: `core.GetTenantIDContext(ctx) string` at `commons/tenant-manager/core/context.go:42-48`. Every tenant-aware package consumes it (DLQ `keys.go:20`; idempotency `idempotency.go:194`; valkey `keys.go:42`; DLQ consumer `consumer.go:295,427`). `core.ContextWithTenantID` is the setter (`context.go:36-38`); the key is an unexported struct (`contextKey{name:"tenantID"}`, `context.go:22-33`) to prevent collisions. **Systemplane should use this exact helper, not invent its own.**

**Canonical validation** is `core.IsValidTenantID(id) bool` at `commons/tenant-manager/core/validation.go:16-22`, with `MaxTenantIDLength = 256` (line 6) and regex `^[a-zA-Z0-9][a-zA-Z0-9_-]*$` (line 11). Used by the JWT-decoding tenant middleware (`commons/tenant-manager/middleware/tenant.go:170-177`). **Outbox does not use it** — its `ColumnResolver` only trims whitespace (`column_resolver.go:180-184`). **DLQ has its own stricter per-segment validator** (`keys.go:74-82`) that additionally forbids `:*?[]\\` to prevent Redis SCAN-pattern injection — this is domain-specific to key segments.

**Fail-open vs fail-closed on missing tenant:**
- **Fail-closed (reject op):** DLQ when tenantID is present but invalid (`commons/dlq/keys.go:28-36` — returns empty key so caller skips).
- **Fail-open degrade-to-global:** DLQ when tenantID is empty — writes to unscoped `"dlq:<source>"` (`keys.go:41`); outbox ColumnResolver when `tenant == ""` — skips append to tenant list (`column_resolver.go:182`).
- **Fail-open bypass:** Idempotency middleware when no tenant ctx — skips idempotency entirely (`commons/net/http/idempotency/idempotency.go:194-200` with explicit comment: "to avoid collapsing all tenant-less requests onto a shared key").
- **Fail-closed reject request:** Tenant middleware JWT path — 401 `MISSING_TENANT` when claim absent (`middleware/tenant.go:162-168`).

The rule is clearly **per-domain**: shared-state packages (idempotency) bypass, enforcement packages (JWT middleware) reject, opinionated queue packages (DLQ) degrade-to-global on absence but fail-closed on invalidity.

## 2. Validator Composition

`commons/systemplane/register.go:52-56` defines the single validator slot: `keyDef.validator func(any) error`. `WithValidator(fn)` (`options.go:116-122`) **replaces** rather than chains — each new call overwrites. The default is run at registration time against `defaultValue` (`register.go:52-56`) and at every `Set` (`set.go:46-50`).

**No other lib-commons package uses composable validators.** Circuit-breaker config validation is a single `Validate() error` on the Config struct itself (`commons/circuitbreaker/manager.go:128-130`); Redis topology validation is a single `validateTopology(cfg)` imperative function (`commons/redis/redis.go:1009-1056`) with one-topology-only invariant enforced by a `count` counter; Postgres migrator is `NewMigrator(cfg).Validate()` (`commons/postgres/postgres.go:636-643`). Idempotency middleware has zero validators — it only length-checks the key (`commons/net/http/idempotency/idempotency.go:182-190`).

**Implication:** the registered validator is already the *composed* contract. Tenant overrides should run the **same** validator chain — no per-tenant layer, matching the evolution-doc constraint. Do NOT introduce a second validator slot; reuse `keyDef.validator`.

## 3. Subscriber Dispatch Patterns

`commons/systemplane/client.go:365-380` implements sequential dispatch with snapshot: `RLock → copy slice → RUnlock → for range; defer runtime.RecoverAndLog(...) per callback`. Subscriptions are keyed by `(namespace,key)` (`client.go:67`) with monotonic `id` (`client.go:44-47`), and unsub removes via linear scan under write-lock (`onchange.go:60-73`). Slice-copy-under-RLock prevents callback-during-mutation races.

Comparable dispatch models:
- **DLQ consumer (`commons/dlq/consumer.go:167-221`):** single goroutine, ticker-driven, `safeProcessOnce` wraps each cycle with `runtime.RecoverWithPolicyAndContext(..., KeepRunning)`; panic in one cycle does not stop the loop. Uses `stopCh` channel + `sync.Once`-like guard (`consumer.go:173-196`) to serialize Run.
- **Webhook fanout (`commons/webhook/deliverer.go:277-290+`):** bounded-concurrency fanout via `sem := make(chan struct{}, d.maxConc)` + `sync.WaitGroup`. Detaches from parent ctx so in-flight deliveries survive graceful shutdown.
- **SafeGo (`commons/runtime/goroutine.go:26-93`):** single-shot goroutine with panic policy; no fanout.
- **Postgres LISTEN invoke (`commons/systemplane/internal/postgres/postgres.go:418-422`):** per-event callback with `runtime.RecoverAndLogWithContext` wrapper.

**Race pitfalls already solved in the codebase:**
- **Slice-mutation during iteration:** systemplane copies the slice before releasing the RLock (`client.go:366-369`). Webhook indexes by `i` and copies `ep := endpoints[i]` before spawning (`deliverer.go:282-284`).
- **Panic-in-handler blows up dispatcher:** all packages defer `runtime.RecoverAndLog*` per call.
- **Goroutine leaks on shutdown:** systemplane uses `wg.Wait()` with 10s deadline (`client.go:264-277`); tenant-manager postgres uses `revalidateWG.Wait()` outside the lock to avoid deadlock (`manager.go:986-991`).
- **Changefeed echo double-fire:** systemplane `Set` deliberately does NOT fire subscribers — only the changefeed roundtrip fires them (`set.go:18-21,82-84`). This is the reason `OnChange` observes *backend* state, not same-process state.

**Implication for tenant-scoped:** sequential dispatch with `RLock+copy+unlock+defer recover` is the idiomatic Ring pattern and scales to low hundreds of subscribers. For higher fanout (if ever needed), the webhook semaphore pattern is the established escape hatch. `OnTenantChange` should follow the exact same shape.

## 4. Options Builder Conventions

Systemplane uses two distinct option types: package-level `Option func(*clientConfig)` (`options.go:33`) and per-key `KeyOption func(*keyDef)` (`options.go:103`). This dual-constructor-with-dual-option pattern is unique to systemplane.

**Scope expression elsewhere:**
- **Redis topology (`commons/redis/redis.go:97-118`):** uses a `Topology` struct with three nullable pointer sub-structs (`Standalone`, `Sentinel`, `Cluster`). Validated by counter: exactly one must be non-nil (`redis.go:1052-1054`). This is the **sum-type-via-nullable-fields** idiom — no enum.
- **Circuit-breaker (`commons/circuitbreaker/config.go:1-66`):** *preset constructors* return full `Config` structs (`DefaultConfig()`, `AggressiveConfig()`, `HTTPServiceConfig()`, `DatabaseConfig()`). No scope enum.
- **Outbox SchemaResolver (`schema_resolver.go:27-54`):** **paired mutually-exclusive options** `WithRequireTenant()` / `WithAllowEmptyTenant()` flip a single bool.
- **Tenant-manager middleware (`middleware/tenant.go:46-103`):** functional options with module-keyed maps (`pgModules map[string]*tmpostgres.Manager`) — "multi-module mode" is signaled by whether the variadic module arg is passed.

**Implication:** for tenant-scoped behavior, idiomatic choices are (a) **separate `RegisterTenantScoped` constructor** matching SchemaResolver's pairing, OR (b) a `KeyOption` like `WithTenantScoped()` that flips a bool on `keyDef`. Option (a) is the cleaner call site: `c.RegisterTenantScoped("global", "fee.pct", 0.02)` reads the intent at the call site. Option (b) reads as `c.Register("global", "fee.pct", 0.02, WithTenantScoped())` — verbose but composable with existing options. The Redis topology precedent suggests a dedicated method when the modes have materially different semantics (which tenant-scoped does — it adds new storage dimension, new methods). **Lean (a).**

## 5. Store Interface Extensions (additive multi-backend evolution)

The internal `store.Store` (`commons/systemplane/internal/store/store.go:36-53`) is 5 methods: `List/Get/Set/Subscribe/Close`. Both implementations sit under ~500 LOC: `internal/postgres/postgres.go` (LISTEN/NOTIFY) and `internal/mongodb/mongodb.go` (change-streams with poll fallback). Public `TestStore` is a hand-mirrored interface with `TestEntry`/`TestEvent` type-duplicates, bridged by a manual adapter (`commons/systemplane/client_testing.go:17-128`).

**Additive-interface precedent in lib-commons:**
- **Logger (`commons/log/log.go:11-18`):** 5 methods. Zap adapter added alongside `GoLogger` — both satisfy the interface; no deprecation. `zap.Logger` extends further via `Raw()`, `Level()` escape hatches but stays compatible.
- **Metrics factory (`commons/opentelemetry/metrics`):** `NewMetricsFactory(meter, logger)` plus `NewNopFactory()` for tests/disabled. Both satisfy same shape; the nop never errors. Pattern: pair every real constructor with an explicit nop.
- **Postgres `Resolver` (`commons/postgres/postgres.go:30-58`):** replaced v1's `GetDB()`. `Primary()` kept for raw access. The renamed methods are the evolution — v1 was broken and *replaced*, not extended. This is the *un*-additive lane.
- **DLQ `Handler` → `Consumer` (`commons/dlq`):** when the need for a background retry loop emerged, `NewConsumer(handler, fn, opts...)` was layered on top of `NewHandler(...)`. **The Handler was not modified; a new type was added that composes it.** This is the template.

**Implication:** for tenant-scoped storage the additive move is **new methods on `Store`** (e.g. `GetTenant`, `SetTenant`, `DeleteTenant`, `ListTenants`) or — cleaner — a **new sub-interface `TenantStore`** satisfied optionally via a type-assertion at Client construction. DLQ's Handler+Consumer split is the precedent. The `TestStore` shim must be mirror-updated when Store grows; the existing manual adapter at `client_testing.go:40-101` is the template.

## 6. Systemplane Schema Creation

Schema creation in systemplane is **ad-hoc, idempotent, inline** — NOT using `commons/postgres.NewMigrator`. `commons/systemplane/internal/postgres/postgres.go:128-183` runs one transaction on `New(cfg)`: `CREATE TABLE IF NOT EXISTS <table>`, `CREATE OR REPLACE FUNCTION systemplane_notify()`, `DROP TRIGGER IF EXISTS … / CREATE TRIGGER`. Table name is validated against `safeIdentifierRe = ^[a-zA-Z_][a-zA-Z0-9_]*$` (line 34) before interpolation. No versioned migration files.

In contrast, **outbox ships proper golang-migrate migration files** at `commons/outbox/postgres/migrations/000001_outbox_events_schema.up.sql` and `commons/outbox/postgres/migrations/column/000001_outbox_events_column.up.sql`, consumed by `commons/postgres.NewMigrator(MigrationConfig)` at `commons/postgres/postgres.go:636-643`. This is the more formal pattern but outbox is the only first-party package using it — dlq, systemplane, idempotency, redis all roll their own.

**Implication:** for adding a `tenant_id` column or a separate `systemplane_tenant_entries` table, the established systemplane style is **inline `ensureSchema` additive DDL** — add another `CREATE TABLE IF NOT EXISTS` + trigger in the same tx, keeping ops-zero-migration-friction. The alternative of introducing golang-migrate for one package is out-of-pattern relative to what's already there. Caveat: inline `CREATE TABLE` can't remove columns or rename safely — if the storage decision is "extend existing table with NULLable `tenant_id`" (option c in the evolution doc), that's still safe because `ADD COLUMN IF NOT EXISTS` is idempotent in Postgres 9.6+.

## 7. Admin HTTP Surface Parameterization

`commons/systemplane/admin/admin.go:110-113` registers three routes with namespace and key as **path parameters**: `GET :prefix/:namespace`, `GET :prefix/:namespace/:key`, `PUT :prefix/:namespace/:key`. Body is JSON `{"value": <raw>}` (`admin.go:152-154`). Path-prefix, authorizer (`action string` — "read" or "write") and actor-extractor are injected via `MountOption` functions (`admin.go:42-77`). Default authorizer is **deny-all** (`admin.go:34-39`) — a crucial security default.

**Other admin-ish surfaces in lib-commons are sparse.** The tenant middleware (`commons/tenant-manager/middleware/tenant.go`) is inbound only. No other package mounts config-mutation routes. Fiber is the standard: `commons/net/http` provides the shared `ErrorResponse` DTO (`admin.go:119-123, 217-229`) and helpers like `RespondError`. Idempotency also mounts a Fiber middleware but for cross-cutting enforcement, not admin.

**Implication:** tenant-scoped admin should extend this same path-param style. `GET :prefix/:namespace/:key/tenants` for list and `PUT/DELETE :prefix/:namespace/:key/tenants/:tenantID` for per-tenant writes/deletes are the natural extensions. Keeping `tenantID` in the path (not query or body) matches the established convention and makes the authorizer hook trivially able to diff `"read"` / `"write"` × per-tenant scopes.

---

## DESIGN IMPLICATIONS

- **Reuse `tenantmanager/core.GetTenantIDContext` and `core.IsValidTenantID`** rather than adding systemplane-local helpers. These are the *single* canonical choices already in the tree; systemplane deviating would be an unforced error.
- **Tenant-scoped registration should be a dedicated constructor (`RegisterTenantScoped`), not an option flag**, mirroring the DLQ Handler→Consumer additive layering and Redis Topology clarity. Shares the same validator chain (`keyDef.validator`) — no new hook.
- **Storage: add a nullable `tenant_id TEXT` column to the existing `systemplane_entries` table** via additive DDL in `ensureSchema`, with composite PK `(namespace, key, COALESCE(tenant_id, ''))` or a partial unique index. Matches the outbox column-per-tenant precedent and avoids a second table/migration framework. Inline `ADD COLUMN IF NOT EXISTS` keeps systemplane's ops-friction-zero ethos.
- **Subscriber dispatch for `OnTenantChange` should copy the systemplane sequential model** (`RLock+slice-copy+unlock+defer RecoverAndLog`) — same pattern already proven against panics and concurrent mutation. `OnChange` should NOT fire on tenant writes (backward-compat pin), matching the Set/changefeed-echo separation already hardened in `set.go:18-21`.
- **Fail behavior: mirror DLQ's matrix.** Tenant ID present but invalid → fail-closed (reject Set). Tenant ID missing for `GetForTenant` → fail-open to default (semantically correct: "no override means default"). Admin HTTP layer: validate `tenantID` path param with `core.IsValidTenantID` before touching the Client, return 400 on invalid.
- **Admin routes should use path params** — `PUT :prefix/:namespace/:key/tenants/:tenantID` — and reuse the existing authorizer hook, passing `"write"` as the action. The per-tenant route needs the tenantID in the authorizer callback so policies can gate by tenant; plan to extend `WithAuthorizer` to a variadic-args signature or pass the tenantID through Fiber params to the authorizer.
