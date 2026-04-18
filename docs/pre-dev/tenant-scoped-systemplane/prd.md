# PRD: Tenant-Scoped Systemplane

## 1. Document Metadata
- Feature: tenant-scoped-systemplane
- Track: Small (5 gates)
- Owner: Fred Amaral
- Date: 2026-04-18
- Status: Gate 1 draft

## 2. Problem Statement

`commons/systemplane` today enforces a strict lifecycle: every `Register(namespace, key, default, opts...)` call must happen **before** `Start(ctx)`, after which registration errors with `ErrRegisterAfterStart`. That gate exists for good reason (snapshot-once hydration, bounded subscriber registry), but it has a sharp downstream cost: **dynamic tenant discovery is impossible**.

In multi-tenant SaaS deployments — e.g. `plugin-br-bank-transfer` using Pool Manager for post-boot tenant discovery — tenants are not known at process startup. Consequently `tenant:<id>` namespaces cannot be pre-declared via `Register`. Plugin developers today have **no first-class way** to expose per-tenant runtime config overrides. The only workarounds are (a) restart the service to add new tenants (operationally unacceptable) or (b) roll a parallel config store outside systemplane (violates the single-source-of-truth principle and loses debounced changefeed / validator / redaction behaviors).

The cost of not solving this: tenant admins cannot tune fee policies, limits, or feature toggles per tenant; plugin authors build ad-hoc solutions around systemplane; downstream Pool Manager consumers have zero hot-reload path for per-tenant knobs. See `research.md §Feature Summary` and `LIB-COMMONS-EVOLUTION.md` for the source motivation.

## 3. Users & Stakeholders

### Primary Users
- **Plugin developers** integrating `lib-commons/v5` in multi-tenant services (primary consumer: `plugin-br-bank-transfer`)
- **Tenant administrators** setting per-tenant override values via admin UI or API
- **Library maintainers** custodians of the v5 public API surface

### Secondary Users
- **SREs / Operators** monitoring override propagation latency and change audit
- **Auditors / Compliance** reviewing override history via `updated_by` / `updated_at` metadata
- **Test authors** writing integration/e2e coverage against tenant-aware config paths

## 4. User Stories

- As a **plugin developer**, I want to register an override-able key once at boot without pre-declaring any tenant IDs, so that dynamic tenants discovered post-boot can immediately receive overrides.
- As a **tenant administrator**, I want to set a value that applies only to my tenant, so that my tenant's behavior diverges from the global default without affecting other tenants.
- As a **plugin developer**, I want `GetForTenant(ctx, ns, key)` to fall back to the registered default when my tenant has no override, so that I write one code path and it works for both overridden and non-overridden tenants.
- As a **service operator**, I want existing callers of `Get` / `Set` / `OnChange` to keep working unchanged, so that upgrading `lib-commons` is a zero-touch minor-version bump.
- As a **plugin developer**, I want `SetForTenant` to reject writes when no tenant is in the context, so that I cannot accidentally poison the global value with a tenant-intended write.
- As a **library maintainer**, I want tenant overrides to run through the same registered validator as global writes, so that tenant admins cannot bypass key invariants (`research-best-practices.md §4`).
- As a **subscriber author**, I want `OnTenantChange` to deliver the tenant ID in the callback signature, so that I don't need a side-channel lookup to know which tenant changed.
- As a **tenant admin UI developer**, I want `ListTenantsForKey(ns, key)` to return sorted unique tenant IDs, so that I can populate an admin dropdown without additional DB roundtrips.

## 5. Acceptance Criteria

**AC1 — Backward Compatibility**
- Given: existing code using `Register` / `Get` / `Set` / `OnChange` with current v5 signatures
- When: `lib-commons` is upgraded to the version including tenant-scoped systemplane
- Then: code compiles without changes AND `OnChange` subscribers receive zero extra fires from tenant-scoped writes

**AC2 — Tenant-Scoped Registration**
- Given: a `Client` prior to `Start`
- When: plugin calls `RegisterTenantScoped("global", "fees.fail_closed_default", false)`
- Then: registration succeeds; the key is marked tenant-scoped in the registry; same call after `Start` returns `ErrRegisterAfterStart`

**AC3 — Tenant Get Resolution**
- Given: Tenant-A has set override=true, Tenant-B has no override, default=false
- When: `GetForTenant(ctxWithTenantA, "global", "fees.fail_closed_default")` → `(true, true, nil)`
- When: `GetForTenant(ctxWithTenantB, "global", "fees.fail_closed_default")` → `(false, true, nil)` (falls back to default)
- When: legacy `Get("global", "fees.fail_closed_default")` → `(false, true)` (unchanged)

**AC4 — Fail-Closed on Missing Tenant Context**
- Given: a `ctx` with no tenant ID populated
- When: code calls `GetForTenant(ctx, ns, key)` or `SetForTenant(ctx, ns, key, value, actor)` or `DeleteForTenant(ctx, ns, key, actor)`
- Then: returns `ErrMissingTenantContext`; storage is NOT touched; no subscriber fires

**AC5 — Tenant ID Validation**
- Given: a `ctx` carrying an invalid tenant ID (e.g. contains `:`, `/`, violates `core.IsValidTenantID`)
- When: code calls `SetForTenant(ctx, ns, key, value, actor)`
- Then: returns `ErrInvalidTenantID`; storage NOT touched; validator NOT invoked

**AC6 — Validator Application to Tenant Writes**
- Given: a key registered with validator rejecting negative numbers
- When: code calls `SetForTenant(ctxWithValidTenant, ns, key, -1, actor)`
- Then: returns `ErrValidation`; no storage write; no subscriber fires (`research-best-practices.md §4`)

**AC7 — OnTenantChange Subscriber**
- Given: a subscriber registered via `OnTenantChange(ns, key, fn)`
- When: any tenant sets, updates, or deletes the key
- Then: `fn(ns, key, tenantID, newValue)` fires exactly once per write; panic inside `fn` does not terminate the process (`runtime.RecoverAndLog`)

**AC8 — OnChange Non-Fire on Tenant Writes**
- Given: subscriber registered via legacy `OnChange(ns, key, fn)`
- When: any tenant writes via `SetForTenant`
- Then: `fn` does NOT fire. `OnChange` remains strictly global-path (`research.md §3`)

**AC9 — DeleteForTenant Reverts to Default**
- Given: tenant-A has override=true, registered default=false
- When: `DeleteForTenant(ctxWithTenantA, ns, key, actor)`
- Then: `OnTenantChange` fires with `newValue = default = false`; subsequent `GetForTenant(ctxWithTenantA, ns, key)` → `(false, true, nil)`

**AC10 — ListTenantsForKey Returns Sorted Unique IDs**
- Given: tenants A, C, B have overrides; tenant D does not
- When: `ListTenantsForKey(ns, key)`
- Then: returns `[A, B, C]` sorted lexicographically, no duplicates, tenant D absent

**AC11 — Concurrency Safety**
- Given: 100 goroutines each calling `SetForTenant` on distinct tenants against the same key
- When: test runs with `-race`
- Then: no data races; in-memory state consistent; all `OnTenantChange` fires arrive exactly once per write

**AC12 — Debouncer Correctness**
- Given: tenant-A sets a key twice within 50ms; tenant-B sets the same key once in the same window; default `WithDebounce(100ms)`
- When: debounce elapses
- Then: `OnTenantChange` fires once for tenant-A (coalesced) and once for tenant-B (distinct tenant). Events are NEVER collapsed across tenants (`research.md §5`, `research-frameworks.md §FRAMEWORK CONSTRAINTS item 5`)

**AC13 — Integration Backend Parity**
- Given: the same test suite run against Postgres and MongoDB backends via testcontainers (`make test-integration`)
- When: executed under `integration` build tag
- Then: both backends produce identical observable behavior for AC1–AC12

**AC14 — Admin HTTP Tenant Routes**
- Given: admin mounted at `/system` with an authorizer permitting `write` on tenant-scoped paths
- When: `PUT /system/global/fees.fail_closed_default/tenants/tenant-a` with body `{"value": true}`
- Then: writes override for `tenant-a`; returns `200`; response body redacts per registered `RedactPolicy`; authorizer receives `tenantID` parameter (`research-codebase.md §7`)

**AC15 — Performance**
- Given: a warm in-memory cache after hydration
- When: `GetForTenant` hit path benchmark
- Then: p99 < 1 µs; miss path (fall through to default) < 2 µs

## 6. Success Metrics

**Primary**
- Zero breaking-change regressions: `api_compat_test.go` compiles against a captured v5.0 snapshot
- `GetForTenant` hit path < 1 µs (benchmark)
- `GetForTenant` miss path < 2 µs (benchmark)
- All 15 acceptance criteria pass on both Postgres and MongoDB backends

**Secondary**
- `plugin-br-bank-transfer`'s 12 overridable keys migrate from `Register` → `RegisterTenantScoped` with <20 lines of diff per bootstrap call site
- `TestTenantSettingsResolver_RespectsPerTenantOverrides` passes in `plugin-br-bank-transfer`

**Adoption signals (post-ship)**
- ≥1 downstream service uses the API within 30 days (`plugin-br-bank-transfer` Phase 3)
- Zero GitHub issues reporting breakage of existing systemplane consumers in the first release cycle

## 7. Out of Scope

- Per-tenant validator overrides (validator is a property of the key, not the tenant — `research-best-practices.md §4`)
- Cross-tenant aggregation APIs (callers compose `ListTenantsForKey` + loop)
- Redis-backed systemplane (only Postgres + MongoDB implementations extended)
- `lib-auth` integration at the systemplane layer (tenant ID comes from context, populated by auth middleware)
- Breaking changes to existing `Register` / `Get` / `Set` / `OnChange` signatures
- Schema migration framework adoption (systemplane continues using inline idempotent DDL per `research-codebase.md §6`)
- Per-key-per-tenant audit log (rely on existing `updated_at` / `updated_by` columns + `OnTenantChange` callback for audit wiring)
- Explicit `GetForTenantID` / `SetForTenantID` variant (locked API decision: ctx-carried only)
- Scoped sub-client API (locked API decision: no sub-client)

## 8. Dependencies & Assumptions

**Dependencies**
- `commons/tenant-manager/core.GetTenantIDContext(ctx) (string, bool)` — canonical ctx extractor (`research-codebase.md §1`)
- `commons/tenant-manager/core.IsValidTenantID(id) bool` — canonical validator
- `pgx/v5` for Postgres LISTEN/NOTIFY (already a v5 dep)
- `mongo-driver/v2` for change streams (already a v5 dep)
- `commons/runtime.RecoverAndLog` for subscriber panic recovery (existing systemplane pattern)

**Assumptions**
- ~100 tenants × ~12 overridable keys ≈ 1,200 in-memory entries (trivial footprint; `research.md §Risks`)
- Tenant ID format conforms to `core.IsValidTenantID` regex
- Auth middleware has populated `ctx` with the tenant ID before reaching systemplane call sites in request paths
- Postgres NOTIFY payload `{ns, key, tenant_id}` stays well under the 8000-byte hard ceiling (`research-frameworks.md §1.3`)
- MongoDB change streams run against a replica set (testcontainers `mongodb.WithReplicaSet("rs0")`)

## 9. Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Debouncer collapses per-tenant echoes | Med | High | Widen debounce key to include `tenant_id` — mandatory per `research-frameworks.md §1.5` |
| PostHog-style OOM from eager tenant load | Low | High | Default eager for small cardinality; expose `WithLazyTenantLoad(maxEntries)` functional option + eviction metric (`research-best-practices.md §6 item 1`) |
| Silent `OnChange` regression (subscriber starts seeing tenant fires) | Low | High | Pin via `TestOnChange_DoesNotFireOnTenantWrites` (AC8) |
| MongoDB unique index change during rollout | Med | Med | Additive index create + drop-old sequence; documented in `MIGRATION_TENANT_SCOPED.md` |
| Admin HTTP authorization gap on tenant routes | Med | High | Extend authorizer signature to receive `tenantID`; default deny-all preserved |
| Reconnect drops tenant events before hydrate | Med | High | Mandatory hydration-on-reconnect on both backends (`research-frameworks.md §1.4, §2.4`) |
| Mongo delete events have no `fullDocument` for `$match` | Med | Med | Filter on `documentKey._id`, or enable pre/post-images (MongoDB 6.0+) |

## 10. Release & Rollout

- **Versioning:** `v5.X.0` minor bump (SemVer: new additive feature, no breaks)
- **Changelog:** "Adds tenant-scoped systemplane keys for multi-tenant services (additive, non-breaking)."
- **Migration doc:** `commons/systemplane/MIGRATION_TENANT_SCOPED.md` — covers `Register → RegisterTenantScoped` opt-in, context-based tenant extraction, admin route extension
- **Feature flag needed?** No — additive; opt-in happens at the `RegisterTenantScoped` call site
- **Downstream uptake:** follow-up PR in `plugin-br-bank-transfer` converting 12 overridable keys and switching the tenant settings resolver

## 11. Approval
- [ ] Gate 1 approved by: Fred Amaral
- [ ] Date:
