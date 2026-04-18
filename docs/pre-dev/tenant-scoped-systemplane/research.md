# Research — Tenant-Scoped Systemplane

## Feature Summary

The current `commons/systemplane` package enforces a strict lifecycle: `Register(namespace, key, default, opts...)` must be called for every known key **before** `Start(ctx)`, after which registration errors with `ErrRegisterAfterStart`. That gate makes dynamic per-tenant overrides impossible — tenants aren't known at process boot, so no Register call can be issued for them. The goal is **additive per-tenant overrides**: the same global key can be selectively overridden by a tenant without pre-declaration, while preserving the registered validator, redaction policy, and v5 public-API contracts. No breaking changes to existing consumers (`Get`, `Set`, `OnChange`, admin HTTP surface) are permitted. See `LIB-COMMONS-EVOLUTION.md` for the source spec.

## Research Mode

**MODIFICATION** — extending `commons/systemplane` package, zero breaking changes mandated. Storage schema, subscriber dispatch, admin routes, debouncer, and typed accessors must all gain a tenant dimension without mutating v5 call-sites that target the global surface.

## Key Findings (cross-source synthesis)

### 1. Storage Decision

All three reports converge on **extending the existing `systemplane_entries` table with a nullable `tenant_id` column plus composite uniqueness** rather than a second table, sentinel marker row, or schema-per-tenant split.

- **Codebase evidence:** systemplane already uses inline idempotent DDL (`CREATE TABLE IF NOT EXISTS` + triggers in one tx on construction — `research-codebase.md` §6, citing `internal/postgres/postgres.go:128-183`). `ADD COLUMN IF NOT EXISTS` composes with that style; outbox's column-per-tenant precedent (`commons/outbox/postgres/migrations/column/…:10-32`) demonstrates the compound-PK pattern already has an in-tree reference.
- **Best-practices evidence:** Postgres multi-tenant guidance (Crunchy Data, Bytebase) favors denormalizing `tenant_id` onto every override row with a composite key — partition-friendly, single SQL path, no dual-table drift (`research-best-practices.md` §1, §RECOMMENDATIONS item 1).
- **Frameworks evidence:** MongoDB side, adding `tenant_id` to existing docs is trivially backward-compatible; existing compound index `(namespace, key)` must be replaced with a partial-unique on `(namespace, key, tenant_id)` using a single representation of "global" (`research-frameworks.md` §5.2). pgx's JSONB codec unchanged — the value column stays `json.RawMessage` (§4.1).

**Winning shape:** single extended table + composite uniqueness `(namespace, key, tenant_id)` on both backends. Postgres inline DDL via `ADD COLUMN IF NOT EXISTS`; MongoDB via additional compound index. One storage path, one validator path, one SQL/aggregation.

### 2. API Shape

Two candidates remain for Gate 2:

- **Explicit tenantID parameter** (`GetForTenantID(tenantID, ns, key)`, `SetForTenant(ctx, tenantID, ns, key, value, actor)`) — matches Go's "no magic" culture, hard to misuse, ergonomic for tests/admin tooling (`research-best-practices.md` §3). Evolution doc leans here.
- **Context-carried tenantID** (`GetForTenant(ctx, ns, key)` reading `systemplane.WithTenant(ctx, id)`) — matches OpenFeature's ambient evaluation context spec, composable with future axes (region/env), transport-friendly across async boundaries (`research-best-practices.md` §3; `research-frameworks.md` §3.2 confirms unexported-struct context-key as canonical Go idiom).

**Best-practices middle ground:** expose **both** — a context-aware `GetForTenant(ctx, ns, key)` as the ergonomic path plus `GetForTenantID(tenantID, ns, key)` as the explicit escape hatch. The typed context helper is cheap to add now and expensive to retrofit when region/env appear (`research-best-practices.md` §RECOMMENDATIONS item 5).

### 3. Subscriber Semantics

**`OnChange` stays global-only; new `OnTenantChange(ns, key, fn)` carries `tenantID` in the callback signature** — `fn(ns, key, tenantID string, newValue any)`.

- **Backward-compat pin:** existing `OnChange` subscribers must NOT see extra fires from tenant writes. `research-codebase.md` §3 notes systemplane already hardened the Set/changefeed-echo separation at `set.go:18-21` (Set does not fire subscribers directly — only the changefeed roundtrip does). Tenant writes must echo through a distinct dispatch path.
- **Rationale:** splitting callbacks is clearer for bounded subscriber counts typical of a library consumer set; carrying `tenantID` in the signature avoids forcing subscribers onto a side-channel lookup (`research-best-practices.md` §5).
- **Dispatch shape:** copy the proven systemplane pattern — `RLock → slice-copy → RUnlock → for range; defer runtime.RecoverAndLog per callback` (`research-codebase.md` §3, citing `client.go:365-380`). Same panic-recovery shield.

### 4. Validator Application

**Universal agreement across all three research sources:** the **same** registered validator chain (`keyDef.validator`) applies unconditionally to tenant overrides. No second validator slot, no per-tenant hook.

- `research-codebase.md` §2: systemplane has a single validator slot (`register.go:52-56`); no other lib-commons package composes validators. The registered validator *is* the composed contract.
- `research-best-practices.md` §4: "Skipping validation on the tenant path is the canonical 'how a tenant admin nukes production' incident pattern." LaunchDarkly, ConfigCat, Flipt all enforce validation identically on every write path.
- `research-best-practices.md` §RECOMMENDATIONS item 2: reject `SetForTenant` on unknown keys with `ErrUnknownKey` (same sentinel as `Set`) — validation is a property of the key, not of the setter's path.

### 5. Concurrency & Debounce

Two hard requirements surface from the framework research:

- **Debouncer key MUST widen to include `tenant_id`.** The existing 100 ms debouncer coalesces per `(ns, key)`; leaving it unchanged causes bursts of `(ns, key, tenantA)` + `(ns, key, tenantB)` writes to collapse into one fire and one tenant loses its echo (`research-frameworks.md` §1.5, bulleted under "Coalesced debounce"; also §FRAMEWORK CONSTRAINTS item 5).
- **Stay with `map + sync.RWMutex`; do NOT introduce `sync.Map`.** The official `sync` package docs spell out that `sync.Map` is advantageous only for write-once/read-many or disjoint-key workloads. Systemplane is write-through on `Set` with frequent value mutation across shared keys — textbook `sync.Map` mismatch. Low-thousands entries at scale; `map[compositeKey]Entry + RWMutex` gives sub-microsecond O(1) reads and preserves type safety for multi-step RMW (`research-frameworks.md` §3.1).

### 6. Pattern Reuse

- **Additive layering mirrors DLQ `Handler → Consumer`:** when DLQ needed a background retry loop, `NewConsumer(handler, fn, opts...)` was layered on top of `NewHandler(...)` without modifying Handler. Tenant-scoped systemplane should follow the same template — new methods and optionally a new sub-interface (`TenantStore`) on the internal `Store`, rather than rewriting existing signatures (`research-codebase.md` §5).
- **Tenant identity helpers already canonical:** reuse `tenant-manager/core.GetTenantIDContext(ctx) string` (`commons/tenant-manager/core/context.go:42-48`) and `core.IsValidTenantID(id) bool` (`…/validation.go:16-22`). Every tenant-aware package in lib-commons consumes these — DLQ, idempotency, valkey, middleware — systemplane deviating would be an unforced error (`research-codebase.md` §1).
- **Admin routes extend path-param style:** existing admin mounts `GET/PUT :prefix/:namespace/:key`. Natural extensions are `PUT/DELETE :prefix/:namespace/:key/tenants/:tenantID` with `core.IsValidTenantID` gate and authorizer hook passing `tenantID` through (`research-codebase.md` §7).

## Risks & Incidents Informing Design

- **PostHog Feb-2026 OOM incident** (`research-best-practices.md` §6 item 1): a cache worker eager-loaded all flag definitions + cohorts + serialized payloads into memory; an outlier tenant blew past the 8GB cap and cascaded into a 116k-task backlog. **Recommendation:** default eager-load tenant overrides at `Start()` is acceptable for expected small cardinality, but offer a **lazy-load + LRU eviction** functional option (e.g. `WithTenantCacheMode(Eager|Lazy), WithTenantCacheMax(10_000)`) for consumers that anticipate tenant-heavy workloads. Emit an eviction metric.
- **Facebook 2010 cache-stampede on wildcard invalidation** and **Google 2025 quota propagation** (`research-best-practices.md` §6 items 3, 5): existing debounce + write-through + changefeed-echo pattern already mitigates both; widening the debounce key to include tenant_id preserves this.
- **Postgres NOTIFY payload 8000-byte hard error** (`research-frameworks.md` §1.3): `{ns, key, tenant_id}` sits well under the ceiling. If payload is ever expanded to carry the value, use the established "NOTIFY carries PK; consumer re-reads" fallback.
- **MongoDB change-stream `$match` on `fullDocument.tenant_id` does not cover deletes** (`research-frameworks.md` §2.1): delete events have no `fullDocument`. Filter on `documentKey._id` for deletes, or enable pre/post-images on MongoDB 6.0+. Test containers use `mongodb.WithReplicaSet("rs0")`, already available at v0.41.0.
- **Reconnect hydration is mandatory on both backends** (`research-frameworks.md` §1.4, §2.4, §FRAMEWORK CONSTRAINTS item 4): Postgres NOTIFY has no backlog; MongoDB resume tokens may age out. On reconnect, re-read full tenant state before resuming subscriptions. `StartAfter(token)` is the correct Mongo resume mode.

## Open Design Questions for Gate 2 (TRD)

- **Q1: Storage shape (separate table vs. extend existing vs. schema-per-tenant)?** → **Preferred: (c) extend existing `systemplane_entries` with nullable `tenant_id` + composite uniqueness on `(namespace, key, tenant_id)`** — unanimous across all three reports.
- **Q2: API shape (ctx-carried tenant vs. explicit tenantID parameter)?** → **Preferred: dual-API — `GetForTenant(ctx, ns, key)` reading `systemplane.WithTenant(ctx, id)` AND `GetForTenantID(tenantID, ns, key)` explicit.** Evolution doc leans explicit; best-practices leans dual. TRD must resolve which Set-side variant is primary.
- **Q3: Should `OnChange` fire on tenant writes?** → **Consensus: NO.** `OnChange` stays global-only; `OnTenantChange(ns, key, fn)` is the new surface, callback carries `tenantID` in signature.
- **Q4: Must the debouncer key widen to include `tenant_id`?** → **Consensus: YES (mandatory).** Framework research lists this as a hard requirement; not doing so drops per-tenant echoes under burst.
- **Q5: Admin HTTP tenant-route shape?** → **Preferred: extend path-param style — `PUT/DELETE :prefix/:namespace/:key/tenants/:tenantID`** with `core.IsValidTenantID` validation at the HTTP boundary and `tenantID` threaded through the authorizer hook.
- **Q6: Eager vs. lazy load of tenant overrides at `Start()`?** → **Default eager** (expected small cardinality, matches current hydrate-on-Start ergonomic); **offer lazy + LRU option** (`WithTenantCacheMode`, `WithTenantCacheMax`, eviction metric) for consumers with high tenant cardinality. Informed by PostHog incident.
- **Q7: Global sentinel convention — NULL `tenant_id` or `'_global'` marker?** → **Split:** codebase §6 leans NULL (additive inline DDL + `COALESCE` in unique expression); best-practices §RECOMMENDATIONS item 1 leans `'_global'` sentinel (no NULL-semantic ambiguity, single SQL path). Frameworks §5.2 flags that Mongo treats NULL and absent as distinct for uniqueness — pick one and enforce. **TRD must pick; preferred is a non-null `_global` sentinel for cross-backend consistency.**

## Sources

- `research-codebase.md` — lib-commons internal patterns (tenant scoping §1, validators §2, subscribers §3, options §4, Store extensions §5, schema §6, admin §7, DESIGN IMPLICATIONS).
- `research-best-practices.md` — LaunchDarkly, Unleash, ConfigCat, Flipt, GrowthBook, OpenFeature (storage §1, propagation §2, API §3, validators §4, subscribers §5, anti-patterns §6, RECOMMENDATIONS).
- `research-frameworks.md` — pgx v5 (§1), mongo-driver/v2 change streams (§2), Go 1.25 idioms (§3), pgx JSONB (§4), BSON schema evolution (§5), FRAMEWORK CONSTRAINTS.

## Verdict

**GATE 0 COMPLETE** — sufficient cross-source evidence to proceed to Gate 1 (PRD). Storage, validator, subscriber, concurrency, and pattern-reuse answers converge; API shape and sentinel convention remain open but bounded with preferred answers. No blocking unknowns.
