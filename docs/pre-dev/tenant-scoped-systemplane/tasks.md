# Task Breakdown: Tenant-Scoped Systemplane

## Summary
- Total tasks: 9
- Total AI-agent-hours: 74–96 (point estimate: ~85)
- Max per task: 16 hours mandate respected (largest task: Task 8 at 12–14h)
- Tech stack: Go 1.25.7, pgx/v5, mongo-driver/v2, testcontainers-go, testify, gofiber/fiber/v2
- Dependencies count: 11 inter-task dependencies forming a DAG with 5 levels of depth
- Confidence: Medium (overall) — individual tasks range H/M/L; the concurrency + changefeed routing work (Task 5) is the single riskiest chunk

## Estimation Methodology
- **AI-agent-hours**: time for a well-instructed AI agent with full project context to implement + self-verify the task end-to-end. Includes: reading the relevant source, writing the code, running `make lint` / `make vet` / package-level `go test`, iterating on failures, and producing a commit-ready diff. Does NOT include Gate 4 reviewer cycles or Gate 5 human approval.
- **Confidence levels**:
  - **High (H)**: well-understood; direct 1:1 mapping to TRD §10 file plan; patterns already exist elsewhere in the codebase (interface growth, option addition, typed accessor mirror). Low probability of scope surprise.
  - **Medium (M)**: straightforward shape but has a concurrency, integration, or cross-backend subtlety that can extend scope by 20–40% (e.g., MongoDB delete-event semantics, debouncer widening correctness, changefeed routing).
  - **Low (L)**: novel work, cross-cutting verification burden, or a dependency we have not yet validated end-to-end (e.g., Postgres trigger DDL on an existing populated table, concurrency/race/benchmark integration suite).
- **Range interpretation**: lower bound assumes happy path; upper bound assumes one integration surprise per task (e.g., a failed reconnect case, an index-creation race with `IF NOT EXISTS` semantics on MongoDB).

## Tasks

### Task 1: Store interface delta + TestStore mirror + Entry/Event evolution
- **Value delivered:** After this task, the internal `store.Store` interface and the public `TestStore` mirror both carry the five new tenant method signatures and the widened `Entry{TenantID}` / `Event{TenantID}` fields. Consumers reading the code see the intended contract even though no implementation exists. The repo compiles (the concrete `postgres.Store` and `mongodb.Store` temporarily satisfy the interface via stub methods returning `errors.New("not implemented")`), and the compile-time `var _ store.Store = (*Store)(nil)` checks at `postgres.go:29` and `mongodb.go:43` continue to hold. **Testable outcome:** `go build ./commons/systemplane/...` succeeds; a single smoke test verifies `testStoreAdapter` forwards the new methods correctly.
- **AI-agent-hours:** 3–4 (confidence: **H**)
- **Files touched:**
  - New: none
  - Modified:
    - `commons/systemplane/internal/store/store.go` (TRD §2.3) — add 5 methods to `Store`; grow `Entry` and `Event` with `TenantID string`
    - `commons/systemplane/client_testing.go` (TRD §10 modified) — grow `TestStore` with 5 methods; grow `TestEntry`/`TestEvent` with `TenantID`; extend `testStoreAdapter` forwarding
    - `commons/systemplane/internal/postgres/postgres.go` — 5 stub methods returning `errors.New("postgres: tenant method not implemented — task 3")` just to satisfy the interface assertion
    - `commons/systemplane/internal/mongodb/mongodb.go` — same stub shape
- **Scope:**
  - **IN**: interface signatures, struct field additions, stub methods to preserve compile-time interface assertions, one smoke test that verifies `testStoreAdapter` marshals `TenantID` both directions.
  - **OUT**: any real tenant storage logic (Tasks 3–4); contract test additions (Task 8); TestStore-based unit tests of the new semantics (Task 7).
- **Testing strategy:**
  - Unit: one smoke test `TestStoreAdapter_TenantIDRoundtrips` in `client_testing_test.go` — constructs a fake `TestStore`, runs `adapter.Set(ctx, Entry{TenantID: "x"})`, asserts the fake saw the tenantID; same for `List`/`Get`/`Subscribe`.
  - Integration: deferred to Task 8.
- **Depends on:** none (this is the foundation task)
- **Blocks:** Tasks 2, 3, 4, 5 all consume the interface shape defined here
- **Acceptance (from PRD):** AC1 partial (non-breaking interface growth verified); AC13 foundation (contract suite has something to extend against)

---

### Task 2: Client-side tenant state, registry, and cache scaffolding
- **Value delivered:** After this task, `Client` carries the new concurrency state (`tenantCache`, `tenantScopedRegistry`, `tenantSubscribers`, `tenantSubsMu`, `tenantLoadMode`), the `WithLazyTenantLoad(max int) Option` is available, and `RegisterTenantScoped(namespace, key, default, opts...)` works with in-memory-only semantics. A consumer can call `RegisterTenantScoped` before `Start`, observe that it succeeds, then call it again and see `ErrDuplicateKey`. After `Start`, `RegisterTenantScoped` returns `ErrRegisterAfterStart`. **Testable outcome:** every PRD AC2 assertion holds without any storage involvement.
- **AI-agent-hours:** 4–6 (confidence: **M** — the LRU wrapper for lazy mode is the one subtle piece; the registry+cache scaffolding mirrors existing patterns at `client.go:58-68`)
- **Files touched:**
  - New:
    - `commons/systemplane/tenant_cache.go` (TRD §10, ~120 LOC) — `tenantCache` type, eager/lazy mode switch, LRU implementation behind a small interface so the eager path stays allocation-free
    - `commons/systemplane/tenant_scoped.go` skeleton (TRD §10, ~80 LOC at this stage) — `RegisterTenantScoped` plus the internal `tenantScopedRegistry` helpers; other methods stubbed returning `errors.New("task 3/5")`
  - Modified:
    - `commons/systemplane/client.go` — `Client` struct gains the four new fields (TRD §2.2); `newClient` initializes them; mutex hierarchy documented in a comment per TRD §5.1
    - `commons/systemplane/options.go` — add `WithLazyTenantLoad(max int) Option` per TRD §2.2
    - `commons/systemplane/errors.go` — add three sentinels (`ErrMissingTenantContext`, `ErrInvalidTenantID`, `ErrTenantScopeNotRegistered`) per TRD §6
- **Scope:**
  - **IN**: client state scaffolding; `RegisterTenantScoped` fully working; the `Option` for lazy mode; the LRU data structure. A single in-memory unit test covers AC2.
  - **OUT**: `SetForTenant` / `GetForTenant` / `DeleteForTenant` / `ListTenantsForKey` (Task 5); `OnTenantChange` (Task 5); storage integration (Tasks 3–4); changefeed routing (Task 5).
- **Testing strategy:**
  - Unit: `TestRegisterTenantScoped_BeforeStart_Succeeds`, `TestRegisterTenantScoped_AfterStart_ReturnsErrRegisterAfterStart`, `TestRegisterTenantScoped_DuplicateReturnsErrDuplicateKey`, `TestWithLazyTenantLoad_ConfiguresLRU`, `TestTenantCache_LRUEvictsOldest`.
  - Integration: deferred to Task 8.
- **Depends on:** Task 1 (for the updated `store.Store` interface — even though this task doesn't call the new methods yet, the struct fields it adds reference types that live alongside)
- **Blocks:** Tasks 5, 6 (Task 5 needs the registry; Task 6 needs `RegisterTenantScoped` to exist for admin-side acceptance testing)
- **Acceptance (from PRD):** AC2 (full); AC4 foundation (state exists for error paths to reference)

---

### Task 3: Postgres tenant storage + schema evolution + trigger rewrite
- **Value delivered:** After this task, the Postgres backend fully implements all five new `Store` methods end-to-end. `ensureSchema` performs the four additive DDL statements (add column, idempotent backfill, drop old PK, composite unique index) from TRD §3.1 in the same transaction as the existing `CREATE TABLE`. The NOTIFY trigger now fires on INSERT, UPDATE, **and DELETE**, and the payload carries `tenant_id`. A test using a live Postgres testcontainer can call `store.SetTenantValue(ctx, "tenant-A", entry)`, see the row land with `tenant_id='tenant-A'`, call `store.GetTenantValue(ctx, "tenant-A", ns, key)` and retrieve it, call `ListTenantsForKey(ctx, ns, key)` and see `["tenant-A"]` returned. **Testable outcome:** Postgres-side AC3 (resolution), AC9 (delete), AC10 (list) hold under integration tests.
- **AI-agent-hours:** 10–14 (confidence: **M** — DDL on an existing populated table has one sharp edge: the PK drop must succeed even if downstream restarts race. The `DROP CONSTRAINT IF EXISTS ... ; CREATE UNIQUE INDEX IF NOT EXISTS ...` pair is idempotent, but the transaction boundary needs care)
- **Files touched:**
  - New: none
  - Modified:
    - `commons/systemplane/internal/postgres/postgres.go` — 4 DDL statements added to `ensureSchema` transaction; trigger function rewritten to include `tenant_id` and handle DELETE (reading `OLD.*`); `notifyPayload` struct gains `TenantID`; 5 new methods: `GetTenantValue`, `SetTenantValue`, `DeleteTenantValue`, `ListTenantValues`, `ListTenantsForKey`; existing `Set` unchanged externally but writes `tenant_id='_global'` explicitly
- **Scope:**
  - **IN**: schema evolution (TRD §3.1); trigger rewrite + DELETE support (TRD §4.4); five new method implementations with spans, error wrapping, and the same pattern as existing methods; `notifyPayload` + `parseNotifyPayload` evolution; `_global` sentinel handling in `Set` / `List` so legacy rows Land with `tenant_id='_global'`.
  - **OUT**: MongoDB side (Task 4); changefeed routing inside `Client.onEvent` (Task 5); unit tests at the Client layer (Task 7); non-Postgres contract-suite integration (Task 8 invokes the contract suite against Postgres as part of the parity proof).
- **Testing strategy:**
  - Unit: none at this layer — `internal/postgres` has always been tested via the contract suite against a real container (`systemplanetest.Run`) plus occasional backend-specific unit tests. Add 1–2 backend-specific unit tests for edge cases (e.g., `TestParseNotifyPayload_WithTenantID` for the new payload shape).
  - Integration: deferred to Task 8, where the extended contract suite runs against the Postgres testcontainer.
- **Depends on:** Task 1 (interface shape)
- **Blocks:** Task 5 (changefeed routing needs the widened `Event{TenantID}` surfaced from this backend), Task 8 (integration tests exercise this implementation)
- **Acceptance (from PRD):** AC3 partial (Postgres path), AC9 partial (Postgres delete trigger), AC10 partial (Postgres list), AC13 half (Postgres parity)

---

### Task 4: MongoDB tenant storage + index evolution + delete-event handling
- **Value delivered:** After this task, the MongoDB backend fully implements all five new `Store` methods. The `New` constructor performs the idempotent backfill (set missing `tenant_id` to `_global`), drops the old `namespace_1_key_1` unique index, and creates a new compound `(namespace, key, tenant_id)` unique index. The change-stream `$match` pipeline is extended to include `"delete"` in the operationType set, and the delete-event extractor falls back to `documentKey._id` + re-read (TRD §3.2) because delete events have no `fullDocument`. Polling-mode path (`subscribePoll`) is extended in parallel to emit tenant-aware events. **Testable outcome:** MongoDB-side AC3, AC9, AC10 hold.
- **AI-agent-hours:** 10–14 (confidence: **L** — this is the riskiest per-backend task because: (1) MongoDB delete events are fundamentally different from insert/update and require a re-read or pre/post-images; (2) the old unique index drop during rollout must be handled carefully if the container has pre-existing data; (3) polling-mode tenant events need a separate correctness argument because polling reads full documents already)
- **Files touched:**
  - New: none
  - Modified:
    - `commons/systemplane/internal/mongodb/mongodb.go` — backfill `UpdateMany` + `DropOne("namespace_1_key_1")` + compound index create in `New`; `entryDoc` gains `TenantID string` bson:"tenant_id"; `$match` extended to `["insert","update","replace","delete"]`; `extractEvent` gains a fallback path for delete (re-read via new `GetTenantValue` helper, or use `documentKey` when present); 5 new methods implemented; `subscribePoll` updated to surface tenantID from `entryDoc`
- **Scope:**
  - **IN**: schema/index evolution (TRD §3.2); change-stream pipeline extension; delete-event re-read fallback OR `documentKey` extraction; five new Store methods; polling-mode parity.
  - **OUT**: Postgres side (Task 3); changefeed routing (Task 5); Client-layer tests (Task 7); contract suite orchestration (Task 8).
- **Testing strategy:**
  - Unit: 2–3 backend-specific tests for edge cases (`TestExtractEvent_DeleteUsesDocumentKey`, `TestNew_IdempotentBackfillOnExistingCollection`, `TestSubscribePoll_SurfacesTenantID`).
  - Integration: deferred to Task 8.
- **Depends on:** Task 1 (interface shape)
- **Blocks:** Task 5 (needs widened `Event` emitted from MongoDB), Task 8 (contract parity)
- **Acceptance (from PRD):** AC3 partial (MongoDB path), AC9 partial, AC10 partial, AC13 half

---

### Task 5: Client read/write/delete paths + changefeed routing + debouncer widening + OnTenantChange dispatch
- **Value delivered:** After this task, the full tenant-scoped dataflow works end-to-end on at least one backend. A consumer can: (1) call `SetForTenant(ctxWithTenant, ns, key, value, actor)` and have the value persist + cache update immediately; (2) call `GetForTenant(ctxWithTenant, ns, key)` and get either the override or the global default with correct fall-through semantics; (3) call `DeleteForTenant(ctxWithTenant, ns, key, actor)` and see `OnTenantChange` fire with the restored default value; (4) call `ListTenantsForKey(ns, key)` and receive the sorted unique tenant list; (5) register `OnTenantChange(ns, key, fn)` and observe firing on every tenant write (coalesced within debounce window) while `OnChange` subscribers stay silent for tenant writes. **Testable outcome:** AC3, AC4, AC5, AC6, AC7, AC8, AC9, AC10 all pass at the Client layer with an in-memory `TestStore`.
- **AI-agent-hours:** 10–14 (confidence: **M** — the individual methods mirror existing `set.go` / `get.go` / `onchange.go` patterns, but the changefeed routing logic in `onEvent` + `refreshFromStore` that decides "global vs tenant dispatch based on `evt.TenantID == '_global'`" is genuinely new logic with careful cache-update ordering)
- **Files touched:**
  - New: none (skeletons exist from Task 2)
  - Modified:
    - `commons/systemplane/tenant_scoped.go` — flesh out `SetForTenant`, `GetForTenant`, `DeleteForTenant`, `ListTenantsForKey`, plus the five typed accessor mirrors (`GetStringForTenant`, `GetIntForTenant`, `GetBoolForTenant`, `GetFloat64ForTenant`, `GetDurationForTenant`) per TRD §2.1 — including ctx extract via `core.GetTenantIDContext`, validation via `core.IsValidTenantID`, registry check, validator application (TRD §4.1), JSON marshal + write-through cache under `cacheMu.Lock()`
    - `commons/systemplane/tenant_storage.go` (NEW, ~80 LOC) — thin dispatch layer with span + telemetry wrapping for `store.GetTenantValue` / `SetTenantValue` / `DeleteTenantValue`, mirroring the marshaling discipline at `set.go:70-78`
    - `commons/systemplane/tenant_onchange.go` (NEW, ~100 LOC) — `OnTenantChange`, `fireTenantSubscribers`, `tenantSubscription` struct, unsubscribe logic mirroring `onchange.go:58-73` but with `tenantID` parameter
    - `commons/systemplane/client.go` — `onEvent` composite key widened per TRD §5.2 (`evt.Namespace + "\x1f" + evt.Key + "\x1f" + evt.TenantID`); `refreshFromStore` split into `refreshGlobalFromStore` + `refreshTenantFromStore` with the `_global` sentinel routing decision per TRD §4.3; `Start` eager-hydrates tenant state per TRD §4.5 (calls `store.ListTenantValues`, filters `_global`, populates `tenantCache`, skips unregistered keys with warn log)
    - `commons/systemplane/internal/debounce/debounce.go` — **no changes** per TRD §10 (debouncer is key-agnostic; the widened key is a Client-side concern only)
- **Scope:**
  - **IN**: all Client-layer tenant methods (write, read, delete, list, subscribe); the typed accessor mirrors; the changefeed routing + debouncer key widening; eager-mode hydration at `Start`; lazy-mode miss path (`store.GetTenantValue` with 5s timeout) in `GetForTenant`.
  - **OUT**: admin HTTP routes (Task 6); comprehensive unit tests beyond a happy-path smoke test per method (Task 7 owns the suite); integration + race + benchmarks (Task 8).
- **Testing strategy:**
  - Unit: one happy-path smoke test per new method using `NewForTesting` with an in-memory `TestStore` that echoes events back synchronously — sufficient to catch basic wiring bugs. The comprehensive suite (negative paths, fail-closed ctx handling, cross-tenant isolation, debouncer coalescing) lives in Task 7.
  - Integration: deferred to Task 8.
- **Depends on:** Tasks 1, 2, **and at least one of {3, 4}** — can theoretically start after Task 2 with just Task 1's interface in hand, but the end-to-end demonstration (the "value delivered" claim) requires a real backend from Task 3 or Task 4. Recommend waiting for Task 3 (Postgres lands first; it's the more common backend in downstream services).
- **Blocks:** Task 6 (admin routes call these methods), Task 7 (unit tests pin this behavior), Task 8 (integration tests exercise the full dataflow)
- **Acceptance (from PRD):** AC3, AC4, AC5, AC6, AC7, AC8, AC9, AC10 (smoke-level; Task 7 hardens each); AC12 foundation (debouncer widening is in place but AC12 is verified in Task 7/8)

---

### Task 6: Admin HTTP tenant routes + WithTenantAuthorizer option + default-deny escalation
- **Value delivered:** After this task, a service that mounts the admin surface gains three new routes: `GET /:prefix/:namespace/:key/tenants` (list), `PUT /:prefix/:namespace/:key/tenants/:tenantID` (set), and `DELETE /:prefix/:namespace/:key/tenants/:tenantID` (delete). An operator can POST a tenant override from a curl command and see `OnTenantChange` fire downstream. The `WithTenantAuthorizer` option lets consumers wire policy that receives `tenantID`; absent that option, tenant routes default-deny (return 403) per TRD §7.1 Option A. **Testable outcome:** AC14 passes under a Fiber app-test harness.
- **AI-agent-hours:** 5–7 (confidence: **H** — admin routes are a well-understood pattern in the codebase; the only subtle piece is the authorizer fallback semantics)
- **Files touched:**
  - New: none
  - Modified:
    - `commons/systemplane/admin/admin.go` — three new routes registered after existing three (TRD §7); new `WithTenantAuthorizer` option; `mountConfig` gains a `tenantAuthorizer` field with default-deny function mirroring `admin.go:32-38`; three new handlers (`handleListTenants`, `handlePutTenant`, `handleDeleteTenant`) that validate `:tenantID` path param via `core.IsValidTenantID`, extract ctx tenant, call `client.SetForTenant` / `client.DeleteForTenant` / `client.ListTenantsForKey`; response redaction applied via existing `KeyRedaction` pathway; ctx injected with tenantID via `core.ContextWithTenantID` before calling the Client
- **Scope:**
  - **IN**: three routes; new option; default-deny escalation; path param validation; ctx injection with tenantID; redaction on response bodies; sentinel-error HTTP mapping mirroring the `handlePut` pattern at `admin.go:247-277`.
  - **OUT**: unit tests of the handlers (Task 7); integration tests (Task 8).
- **Testing strategy:**
  - Unit: 4–5 handler tests using Fiber's `app.Test()` harness in `admin_test.go` — covers happy paths + authorizer default-deny + invalid tenantID returns 400 + unknown key returns 400 + missing tenant authorizer escalates to deny-all.
  - Integration: deferred to Task 8.
- **Depends on:** Task 5 (handlers call `SetForTenant` / `DeleteForTenant` / `ListTenantsForKey`)
- **Blocks:** Task 7 (admin handler unit tests live in the Task 7 suite), Task 9 (migration doc references admin authorizer migration)
- **Acceptance (from PRD):** AC14

---

### Task 7: api_compat_test.go pin + comprehensive Client-layer unit test suite
- **Value delivered:** After this task, the v5 public-API surface is pinned against regression via a compile-only `api_compat_test.go`. The comprehensive unit-test suite covers every PRD AC at the Client layer (using `NewForTesting` + in-memory `TestStore`) including fail-closed ctx handling (AC4), invalid tenant ID rejection (AC5), validator rejection on tenant writes (AC6), subscriber semantics including the critical AC8 non-fire on `OnChange`, delete revert-to-default (AC9), list sort/dedup (AC10), and debouncer coalescing correctness (AC12). **Testable outcome:** `go test ./commons/systemplane/... -run 'TestTenant|TestRegisterTenantScoped|TestOnTenantChange|TestAPICompat'` passes; the suite serves as the PRD AC verification artifact.
- **AI-agent-hours:** 8–10 (confidence: **M** — the test count is high (~15+ tests) and the debouncer-correctness tests need careful timing construction; the api_compat pin is a well-known pattern but needs every new signature captured)
- **Files touched:**
  - New:
    - `commons/systemplane/api_compat_test.go` (TRD §10, ~200 LOC) — compile-time pin of every v5.0 method signature PLUS every new tenant method signature from TRD §2.1. Uses `var _ = SomeType{}` and `var _ func(...) = (*Client).SomeMethod` style assertions. Fails to compile on any signature drift.
    - `commons/systemplane/tenant_scoped_test.go` (~400 LOC) — happy path + all negative paths for `RegisterTenantScoped`/`SetForTenant`/`GetForTenant`/`DeleteForTenant`/`ListTenantsForKey`/typed accessors
    - `commons/systemplane/tenant_onchange_test.go` (~200 LOC) — `OnTenantChange` firing + coalescing + AC8 `OnChange` non-fire pin
    - `commons/systemplane/admin/admin_tenant_test.go` (~250 LOC) — Fiber app-test harness for Task 6 handlers
  - Modified:
    - `commons/systemplane/admin/admin_test.go` (or similar existing file) — if there's an existing admin test file, extend the helper setup to include a `RegisterTenantScoped` call; otherwise keep the tenant cases in the new file
- **Scope:**
  - **IN**: compile-time signature pin; full PRD AC coverage at Client + admin layer using in-memory store; the 12+ test functions noted in research.md § Open Design Questions and TRD §9 Backward Compatibility Matrix. Explicitly includes `TestOnChange_DoesNotFireOnTenantWrites` (AC8) and `TestGet_IgnoresTenantOverrides` (AC1) as critical regression pins.
  - **OUT**: testcontainers-based integration tests (Task 8); race detector runs (Task 8); benchmarks (Task 8); backend-specific DDL tests (owned by Tasks 3 and 4).
- **Testing strategy:**
  - Unit: this IS the unit test task.
  - Integration: explicitly deferred to Task 8.
- **Depends on:** Tasks 5, 6 (need the full Client API + admin routes to test)
- **Blocks:** Task 8 (integration suite reuses helpers from Task 7); Task 9 (migration doc references the regression tests as evidence of stability)
- **Acceptance (from PRD):** AC1 (full — signature pin + `OnChange` regression test), AC2 (hardened), AC3–AC10 (hardened at Client layer), AC12 (hardened via time-scaled subtests), AC14 (hardened at admin layer)

---

### Task 8: Integration + race + benchmark tests (Postgres + MongoDB)
- **Value delivered:** After this task, the `systemplanetest.Run` contract suite is extended with a full tenant-aware contract set (`TenantListOnEmpty`, `SetTenantThenGetRoundtrip`, `SetTenantTwiceLastWriteWins`, `DeleteTenantRevertsToDefault`, `ListTenantsForKeySorted`, `SubscribeReceivesTenantEvent`, `DebouncerDoesNotCollapseAcrossTenants`) that runs against BOTH Postgres and MongoDB testcontainers. The `-race` flag passes on a parallel 100-goroutines × distinct-tenants stress test. Benchmarks confirm the PRD AC15 targets: `BenchmarkGetForTenant_Hit` < 1µs and `BenchmarkGetForTenant_Miss` < 2µs. **Testable outcome:** `make test-integration` green on both backends; `make test-unit` green with `-race`; `go test -bench=BenchmarkGetForTenant ./commons/systemplane/...` meets perf budget.
- **AI-agent-hours:** 12–14 (confidence: **L** — testcontainers orchestration, MongoDB replica-set bootstrapping, change-stream timing, and the 100-goroutine race test are each independently delicate; achieving the sub-microsecond Get target likely requires profile-guided tuning on the LRU path)
- **Files touched:**
  - New:
    - `commons/systemplane/internal/postgres/postgres_tenant_integration_test.go` — wraps the existing `postgres_integration_test.go` factory, runs extended contract suite
    - `commons/systemplane/internal/mongodb/mongodb_tenant_integration_test.go` — mirror for MongoDB including replica-set container setup
    - `commons/systemplane/bench_tenant_test.go` — `BenchmarkGetForTenant_Hit`, `BenchmarkGetForTenant_Miss` (eager + lazy), `BenchmarkSetForTenant`
    - `commons/systemplane/tenant_race_test.go` — 100 goroutines × distinct tenants × shared key, asserts no races + exactly-once `OnTenantChange` per write
  - Modified:
    - `commons/systemplane/systemplanetest/contract.go` — add 7+ new `testTenant*` functions to the `Run` suite so both backends exercise identical tenant semantics (AC13 proof)
- **Scope:**
  - **IN**: extended contract suite; per-backend integration test wiring; race test; benchmarks meeting PRD AC15; replica-set container bootstrapping for MongoDB change streams.
  - **OUT**: unit tests without `-race` or without testcontainers (Task 7); documentation (Task 9); DDL backfill tests on pre-existing data (covered backend-internally in Tasks 3, 4).
- **Testing strategy:**
  - Unit: N/A — this task's output IS integration + race + benchmark tests. The existing Task 7 unit suite stays as the baseline.
  - Integration: the testcontainers-go setup follows the existing pattern in `commons/systemplane/internal/postgres/` and the MongoDB side needs `mongodb.WithReplicaSet("rs0")` (available at testcontainers-go v0.41.0+) per research.md § Risks.
- **Depends on:** Tasks 3, 4, 5 (both backends complete + Client routing in place), Task 7 (reuses unit-test helpers for `TestStore` setup where applicable)
- **Blocks:** Task 9 (migration doc cites performance benchmarks + the contract parity proof)
- **Acceptance (from PRD):** AC11 (race), AC12 (debouncer under burst against real backend timing), AC13 (backend parity), AC15 (performance)

---

### Task 9: Migration doc + CLAUDE.md systemplane-section update + README changelog
- **Value delivered:** After this task, downstream consumers (primary: `plugin-br-bank-transfer`) have clear adoption guidance. `MIGRATION_TENANT_SCOPED.md` covers: opt-in via `RegisterTenantScoped`; context setup using `core.ContextWithTenantID`; admin authorizer migration path from `WithAuthorizer` → `WithTenantAuthorizer` including the default-deny fallback rationale; rollback stance (forward-only, export-before-upgrade). `CLAUDE.md`'s systemplane section carries a concise summary of the six new methods + three new routes + three new sentinels. README changelog gets the "Adds tenant-scoped systemplane keys (additive, non-breaking)" entry. **Testable outcome:** documentation review gate passes; `plugin-br-bank-transfer` Phase 3 has enough information to migrate without a synchronous question loop.
- **AI-agent-hours:** 4–5 (confidence: **H** — straight documentation work; the content is 100% determined by Tasks 1–8 output; no code changes)
- **Files touched:**
  - New:
    - `commons/systemplane/MIGRATION_TENANT_SCOPED.md` (~300 lines) — opt-in guide, context setup, admin authorizer migration, rollback stance, example transformation from `Register` to `RegisterTenantScoped` for a representative key
  - Modified:
    - `CLAUDE.md` — the `### Runtime configuration (commons/systemplane)` section grows ~25 lines with tenant methods, sentinels, options, and admin routes
    - `README.md` — changelog entry in the appropriate version section
    - `.env.reference` — if any new env var is introduced (expected: **none** for this feature; verify during the task)
- **Scope:**
  - **IN**: migration doc; CLAUDE.md section update; README changelog entry; a sanity pass on .env.reference to confirm no env additions.
  - **OUT**: any code changes (all prior tasks); API reference doc generation (not used by this repo); Go doc comments (maintained inline during Tasks 1–8).
- **Testing strategy:**
  - Unit: N/A
  - Integration: N/A
  - Validation: `make check-envs` passes (no hooks regression); a human docs reviewer confirms accuracy.
- **Depends on:** Tasks 1, 2, 3, 4, 5, 6, 7, 8 (all technical work must be settled so the docs reflect shipped behavior)
- **Blocks:** none (final task)
- **Acceptance (from PRD):** Release & Rollout §10 (migration doc + changelog requirement fulfilled)

---

## Dependency DAG

```
Task 1 (interface)
   ├──> Task 2 (client scaffolding)
   │       ├──> Task 5 (client dataflow)
   │       │       ├──> Task 6 (admin routes)
   │       │       │       └──> Task 7 (unit suite)
   │       │       ├──> Task 7 (unit suite)
   │       │       └──> Task 8 (integration)
   │       └──> Task 6 (admin routes)
   ├──> Task 3 (postgres)
   │       ├──> Task 5
   │       └──> Task 8
   └──> Task 4 (mongodb)
           ├──> Task 5
           └──> Task 8

Task 7 ──> Task 8 ──> Task 9
Task 6 ──> Task 7
```

## Critical path

**Task 1 → Task 2 → Task 3 → Task 5 → Task 6 → Task 7 → Task 8 → Task 9**

- Length: 8 tasks
- Cumulative hours along critical path: 3 + 4 + 10 + 10 + 5 + 8 + 12 + 4 = **~56 hours** (point estimate with lower ends), or ~68h with upper-end estimates
- This is the longest chain; no task in this chain can start before its predecessor completes

## Parallelization opportunities

- **Tasks 3 and 4 run in parallel** after Task 1 lands. Different engineers/agents can own Postgres and MongoDB independently; they touch disjoint files and share only the interface from Task 1. **Savings: ~10 hours** shaved off critical path if run in parallel.
- **Task 2 can run in parallel with Tasks 3 and 4** — it depends on Task 1 but touches different files (client scaffolding vs backend implementations). **Savings: ~4 hours.**
- **Task 6 (admin) can start as soon as Task 5 has `SetForTenant` / `DeleteForTenant` / `ListTenantsForKey` wired up** — a partial dependency, but realistic to parallelize the last hour of Task 5 with the first hour of Task 6.
- **Task 7 can be partially developed alongside Tasks 5 and 6**, writing the unit tests against expected signatures before implementation fully lands (TDD-style). This is risky for estimation confidence — default schedule assumes Task 7 runs sequentially after Task 6.

With maximum parallelization of Tasks 2–3–4, the critical path becomes: Task 1 → (max of Tasks 2, 3, 4) → Task 5 → Task 6 → Task 7 → Task 8 → Task 9 ≈ 3 + 10 + 10 + 5 + 8 + 12 + 4 = **~52 hours** point estimate.

## Verdict: Track fit

**Small Track mandate: <2 days ≈ 16 hours total.**

This feature totals **74–96 AI-agent-hours** (point estimate ~85h).

**This does NOT fit Small Track by a factor of ~5×.**

Recommendations:
1. **Flag to the user that this feature warrants a Large Track / Full Track pre-dev workflow re-classification.** The 9-gate Full Track adds API design, data model, dependency map, and subtask creation gates — all of which would meaningfully de-risk the concurrency-heavy Task 5 and the integration-heavy Task 8.
2. If the team elects to stay on Small Track despite the size, **consider splitting delivery into two PRs**: PR1 = Tasks 1–5 (Postgres-only minimum viable) + Tasks 7, 9 scoped to Postgres-only acceptance; PR2 = Tasks 4, 6, 8 (MongoDB parity + admin routes + full integration suite). This would make each PR review-sized (~40–50 AI-agent-hours each) while keeping the forward-only compatibility promise intact (PR1 ships a working tenant API for Postgres consumers; PR2 adds MongoDB parity + admin routes additively).
3. If the team sticks with single-PR delivery under Small Track, **expect review latency to dominate elapsed calendar time** and plan a 3-day calendar window rather than the 2-day mandate.

No task exceeded the 16-hour cap (largest was Task 8 at 12–14h). No splits were needed at the 16h threshold, though Task 8 is close to the ceiling and is the strongest candidate for a defensive split if calendar pressure mounts.
