# TRD: Tenant-Scoped Systemplane

## 1. Architecture Overview

The existing systemplane architecture is preserved in its entirety. The Client's snapshot-once lifecycle (`Register*`-before-`Start`, `client.go:162-247`), write-through cache + changefeed-echo pattern (`set.go:82-84`), debouncer coalescing (`client.go:293-299`), and sequential subscriber dispatch under `cacheMu`/`subsMu` (`client.go:365-380`) all stay in place. No existing public method signature changes; no existing row on-disk format changes. The feature layers on top through six additive Client methods, five new `Store` interface methods, one internal state struct (`tenantState`), three new sentinel errors, three new admin routes, and a widened debouncer composite key.

Store backends continue to own their own changefeed — Postgres LISTEN/NOTIFY via `internal/postgres` (`postgres.go:319-422`) and MongoDB change streams / polling via `internal/mongodb` (`mongodb.go:285-299`). Both gain an additive `tenant_id TEXT NOT NULL DEFAULT '_global'` column / BSON field (locked decision D2), an additive NOTIFY payload / change-stream `$match` field, and five new `TenantStore`-equivalent methods on the existing `Store` interface (not a new sub-interface — avoids an extra adapter layer and keeps `client_testing.go`'s mirror shape trivially symmetrical). The single `_global` sentinel string removes NULL-semantics ambiguity between backends (`research-frameworks.md §5.2`) and enables one compound unique index per backend.

## 2. Component Design

### 2.1 Public API Surface (additive)

All six primary methods plus five typed accessor mirrors. Signatures fixed per locked decision D1.

```go
func (c *Client) RegisterTenantScoped(namespace, key string, defaultValue any, opts ...KeyOption) error
func (c *Client) GetForTenant(ctx context.Context, namespace, key string) (value any, found bool, err error)
func (c *Client) SetForTenant(ctx context.Context, namespace, key string, value any, actor string) error
func (c *Client) DeleteForTenant(ctx context.Context, namespace, key, actor string) error
func (c *Client) ListTenantsForKey(namespace, key string) []string
func (c *Client) OnTenantChange(namespace, key string, fn func(ns, key, tenantID string, newValue any)) UnsubscribeFunc
```

Typed accessor mirrors (parallel to `get.go:149-232`):
```go
func (c *Client) GetStringForTenant(ctx context.Context, ns, key string) (string, error)
func (c *Client) GetIntForTenant(ctx context.Context, ns, key string) (int, error)
func (c *Client) GetBoolForTenant(ctx context.Context, ns, key string) (bool, error)
func (c *Client) GetFloat64ForTenant(ctx context.Context, ns, key string) (float64, error)
func (c *Client) GetDurationForTenant(ctx context.Context, ns, key string) (time.Duration, error)
```

All typed accessors return `(zero, err)` on missing ctx tenant / invalid tenant — they cannot silently collapse to a shared global as the legacy typed accessors do, because "missing tenant" is not a valid read state for a tenant-scoped key (see D8). Legacy accessors remain strictly global-reading.

### 2.2 New Internal Components

- **`tenantCache`** — `map[string]map[nskey]any` (tenantID → (ns, key) → value), protected by existing `cacheMu sync.RWMutex` (locked decision D7). Reads at `Get`-hit path take `RLock`; writes (hydrate, write-through, changefeed refresh) take `Lock`.
- **`tenantScopedRegistry`** — `map[nskey]struct{}` under `registryMu`. Identifies which registered keys accept tenant overrides. Lookup on `SetForTenant`/`GetForTenant`/`DeleteForTenant` returns `ErrTenantScopeNotRegistered` for globals-only keys.
- **`tenantSubscribers`** — `map[nskey][]tenantSubscription` under a new `tenantSubsMu sync.RWMutex`. Each entry mirrors the existing `subscription` pattern (`client.go:44-47`) but the callback signature carries `tenantID`. Dispatch pattern identical to `fireSubscribers` (`client.go:365-380`) — `RLock → copy → RUnlock → for range → defer RecoverAndLog`.
- **`tenantLoadMode`** — `{mode: eager|lazy, maxCached: int}`. Default eager (locked D3). `WithLazyTenantLoad(max)` swaps to a bounded LRU implementation (`tenant_cache.go`) that wraps the map with eviction-on-insert.

### 2.3 Store Interface Delta

Five additive methods on the existing `Store` interface at `internal/store/store.go:36-53`:

```go
type Store interface {
    // existing methods unchanged: List, Get, Set, Subscribe, Close

    GetTenantValue(ctx context.Context, tenantID, namespace, key string) (Entry, bool, error)
    SetTenantValue(ctx context.Context, tenantID string, e Entry) error
    DeleteTenantValue(ctx context.Context, tenantID, namespace, key, actor string) error
    ListTenantValues(ctx context.Context) ([]Entry, error)            // all rows including _global
    ListTenantsForKey(ctx context.Context, namespace, key string) ([]string, error)
}

// Entry gains a TenantID field (additive) populated by backends; defaults to "_global".
type Entry struct {
    Namespace, Key, TenantID string
    Value                    []byte
    UpdatedAt                time.Time
    UpdatedBy                string
}

// Event gains a TenantID field populated by the changefeed payload.
type Event struct {
    Namespace, Key, TenantID string
}
```

Why not a separate `TenantStore` sub-interface: `client_testing.go:17-128` already hand-mirrors the `Store` shape with a `testStoreAdapter`. A sub-interface would require a second adapter, and every production call-site (`client.go:96-108`, `newClient`) already treats `store.Store` as the single backend type. Keeping everything on one interface preserves the single-mirror discipline (research-codebase.md §5). `TestStore` in `client_testing.go` grows the same five mirror methods.

## 3. Data Architecture

### 3.1 Postgres Schema Evolution

Existing table at `postgres.go:138-145`:
```sql
CREATE TABLE IF NOT EXISTS systemplane_entries (
    namespace TEXT NOT NULL, key TEXT NOT NULL, value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (namespace, key)
);
```

`ensureSchema` (`postgres.go:128-183`) grows four additive statements, run in the same transaction immediately after the existing `CREATE TABLE IF NOT EXISTS`:

```sql
-- 1. Additive column with sentinel default.
ALTER TABLE systemplane_entries
    ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '_global';

-- 2. Idempotent backfill (locked decision D2).
UPDATE systemplane_entries SET tenant_id = '_global'
    WHERE tenant_id IS NULL OR tenant_id = '';

-- 3. Drop old (namespace, key) PK; replace with composite unique index.
ALTER TABLE systemplane_entries DROP CONSTRAINT IF EXISTS systemplane_entries_pkey;
CREATE UNIQUE INDEX IF NOT EXISTS systemplane_entries_pkey_v2
    ON systemplane_entries (namespace, key, tenant_id);

-- 4. Tenant ID identifier: cannot begin with underscore per commons/tenant-manager/core/validation.go:11
-- regex ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ — '_global' is safe sentinel, no collision.
```

`CREATE OR REPLACE FUNCTION systemplane_notify()` (`postgres.go:154-159`) is rewritten to include `tenant_id`:
```sql
PERFORM pg_notify('<channel>',
    json_build_object('namespace', NEW.namespace,
                      'key', NEW.key,
                      'tenant_id', NEW.tenant_id)::text);
```

Payload size: ~150 bytes worst case with 256-char max tenant IDs — well under the 8000-byte hard ceiling (`research-frameworks.md §1.3`).

### 3.2 MongoDB Collection Evolution

`mongodb.go:111-154` `New` currently creates a compound unique index on `(namespace, key)`. Additive steps at store construction:

```go
// 1. Idempotent backfill: treat missing tenant_id as '_global'.
coll.UpdateMany(ctx,
    bson.D{{"tenant_id", bson.D{{"$exists", false}}}},
    bson.D{{"$set", bson.D{{"tenant_id", "_global"}}}})

// 2. Drop old unique index, create new composite.
coll.Indexes().DropOne(ctx, "namespace_1_key_1") // existing index name from mongodb.go:126-133
coll.Indexes().CreateOne(ctx, mongo.IndexModel{
    Keys: bson.D{{"namespace", 1}, {"key", 1}, {"tenant_id", 1}},
    Options: options.Index().SetUnique(true),
})
```

`entryDoc` (`mongodb.go:70-76`) gains `TenantID string` bson:"tenant_id".

Change-stream `$match` (`mongodb.go:364-370`) gains delete handling:
```go
pipeline := mongo.Pipeline{
    {{"$match", bson.D{
        {"operationType", bson.D{{"$in", bson.A{"insert","update","replace","delete"}}}},
    }}},
}
```
Delete events have no `fullDocument` (research-frameworks.md §2.1); the extractor falls back to `documentKey._id` and re-reads via `GetTenantValue` to determine `tenant_id`. This preserves DeleteForTenant → changefeed correctness without requiring pre/post-images.

### 3.3 NOTIFY / Change Stream Payload

Postgres: `{"namespace":"global","key":"fees.fail_closed_default","tenant_id":"_global"}` — ~80 bytes typical, ~350 bytes with worst-case 256-char tenant ID. Well under 8000-byte ceiling.

MongoDB: `fullDocument.tenant_id` surfaces directly via `SetFullDocument(UpdateLookup)` (existing setting at `mongodb.go:372-373`, unchanged). Delete events — see §3.2.

## 4. Integration Patterns

### 4.1 Write Path (SetForTenant)

1. `ctx` extract via `core.GetTenantIDContext(ctx)` (`commons/tenant-manager/core/context.go:42-48`); return `ErrMissingTenantContext` if empty (locked D8).
2. `core.IsValidTenantID(id)` (`validation.go:16-22`); return `ErrInvalidTenantID` if false. Tenant IDs start with alphanumeric, cannot collide with `_global` sentinel (D2).
3. Registry lookup under `registryMu.RLock()`. Missing → `ErrUnknownKey`. Not tenant-scoped → `ErrTenantScopeNotRegistered`.
4. Run `def.validator(value)` — same validator chain as `Set` (locked D6), matching `set.go:46-50`.
5. `json.Marshal(value)` — same as `set.go:53`.
6. `store.SetTenantValue(ctx, tenantID, Entry{...})`.
7. Write-through cache update under `cacheMu.Lock()` — `tenantCache[tenantID][nskey]` (canonical form via `json.Unmarshal` round-trip per `set.go:70-78`).
8. Return. Subscribers fire only from the changefeed echo (set.go:18-21 invariant preserved).

### 4.2 Read Path (GetForTenant)

**Eager mode (default, D3):**
1. Extract tenant from ctx. Missing/invalid → error.
2. Registry lookup. Missing → `(nil, false, ErrUnknownKey)`. Not tenant-scoped → `ErrTenantScopeNotRegistered`.
3. `cacheMu.RLock()`. Read `tenantCache[tenantID][nskey]` — hit returns `(v, true, nil)`.
4. Miss → read `cache[nskey]` (legacy global cache at `client.go:63`) — hit returns `(v, true, nil)` falling through to global.
5. Miss → return registered default `(def.defaultValue, true, nil)`. Never errors on "no override."

**Lazy mode (opt-in via `WithLazyTenantLoad(max)`):**
1–3. Same through cache read.
4. LRU miss → single-flight `store.GetTenantValue(ctx, tenantID, ns, key)` with 5s timeout (mirroring `client.go:321-323`). On success, populate LRU under `cacheMu.Lock()`. On error, log, fall through.
5. Same fallback chain.

Performance target: hit path sub-microsecond (one `RLock` + two map reads). Lazy miss path: ~5ms (Postgres) / ~10ms (MongoDB) — single round-trip.

### 4.3 Changefeed Routing

1. NOTIFY / change-stream event arrives at `onEvent` handler. Event now carries `TenantID`.
2. Debouncer key widens from `evt.Namespace + "\x1f" + evt.Key` (`client.go:294`) to `evt.Namespace + "\x1f" + evt.Key + "\x1f" + evt.TenantID`. Global writes emit `_global`; per-tenant writes emit actual tenant. Coalescing remains per-tuple (D4). No cross-tenant collapse possible.
3. After 100ms window (unchanged default), `refreshFromStore(tenantID, ns, key)` fires.
4. For `tenantID == "_global"`: update legacy `cache[nskey]` and fire `fireSubscribers(nk, newValue)` — existing OnChange path.
5. For `tenantID != "_global"`: update `tenantCache[tenantID][nk]` and fire `fireTenantSubscribers(nk, tenantID, newValue)` — new path.

This routing implements locked decision D5 precisely: `OnChange` and `OnTenantChange` are mutually exclusive dispatch targets based on the single `tenant_id` field on the refreshed row.

### 4.4 Delete Path

`DeleteForTenant(ctx, ns, key, actor)`:
1. Ctx extract + tenant validate (same as write path).
2. `store.DeleteTenantValue(ctx, tenantID, ns, key, actor)` — Postgres `DELETE WHERE namespace=$1 AND key=$2 AND tenant_id=$3`; MongoDB `coll.DeleteOne(ctx, {namespace, key, tenant_id})`.
3. Trigger/change-stream emits event. On refresh, `store.GetTenantValue` returns `(_, false, nil)` — missing row.
4. Cache update under `cacheMu.Lock()`: `delete(tenantCache[tenantID], nk)`.
5. Fire `OnTenantChange` with `newValue = def.defaultValue` — per PRD AC9 contract (the tenant has reverted to global/default).

Postgres: triggers on DELETE are not currently wired (`postgres.go:170-172` uses `AFTER INSERT OR UPDATE`). `CREATE TRIGGER` is extended to `AFTER INSERT OR UPDATE OR DELETE` and the trigger function reads `OLD.namespace, OLD.key, OLD.tenant_id` on DELETE (NEW is null on DELETE).

### 4.5 Start() Sequence

Existing hydration at `client.go:178-226` preserved for globals. Two additions:

- **Eager mode:** after global hydrate, call `store.ListTenantValues(ctx)` (returns every row including `_global`). Filter rows where `tenant_id != '_global'`. Populate `tenantCache[tenantID][nk]`. Skip rows for unregistered keys (log warn, same as current loop at `client.go:201-208`).
- **Lazy mode:** skip tenant hydrate; initialize empty LRU of size `maxCached`.

Subscribe goroutine (`client.go:229-242`) unchanged — the backend-side changefeed plumbing now carries `tenant_id` and the `onEvent` dispatcher routes by it.

## 5. Concurrency Model

### 5.1 Mutex Hierarchy

Ordered to prevent deadlock (enforced by convention; no nested two-RWMutex holds):

1. `startMu sync.Mutex` — serializes `Start()` (`client.go:70`).
2. `registryMu sync.RWMutex` — keyDef + tenantScopedRegistry (`client.go:58`).
3. `cacheMu sync.RWMutex` — both `cache` and `tenantCache` (`client.go:62`).
4. `subsMu sync.RWMutex` — OnChange subscribers (`client.go:66`).
5. `tenantSubsMu sync.RWMutex` — new; OnTenantChange subscribers.
6. `atomic.Bool` flags (`started`, `closed`) — lock-free.

No code path holds two `RWMutex` simultaneously for write. Reads follow the existing `client.go:307-309` pattern: RLock → read-and-copy → RUnlock → compute. Dispatch follows `fireSubscribers`: RLock → slice-copy → RUnlock → invoke under panic shield.

### 5.2 Debouncer Widening

Composite key shape at `client.go:294` changes from:
```go
compositeKey := evt.Namespace + "\x1f" + evt.Key
```
to:
```go
compositeKey := evt.Namespace + "\x1f" + evt.Key + "\x1f" + evt.TenantID
```

`evt.TenantID` is always populated — `_global` for legacy global writes, actual tenant ID otherwise. `debounce/debounce.go` is key-agnostic (string); no changes to the debouncer internals. This is mandatory from day 1 (D4) — no silent dropping of per-tenant echoes possible.

### 5.3 Race Guarantees

- **AC11 (100 concurrent SetForTenant on distinct tenants):** Each write takes `cacheMu.Lock()` independently. `tenantCache[tenantID]` is created on first write per tenant; creation must be under the same write lock. No two tenants share a map node. Dispatch via changefeed echo enters under `cacheMu.Lock()` for the refresh, then `tenantSubsMu.RLock()` for subscriber copy — serialization guarantees exactly-once fire per changefeed event.
- **AC12 (debounce correctness under burst):** Widened key ensures `tenant-A`/`tenant-B` events never collide on the same timer slot. `Debouncer.Submit` under `d.mu.Lock()` (`debounce.go:75-90`) keyed by full triple. Timer coalesces only intra-tenant bursts.
- **AC13 (backend parity):** `systemplanetest.Run(t, factory)` contract suite gets tenant-specific contracts. Both `internal/postgres` and `internal/mongodb` implementations plus `client_testing.go`'s `TestStore` must satisfy identical observable behavior (research-codebase.md §5 additive-interface pattern).

## 6. Error Model

New sentinels in `errors.go` (additive):
```go
var (
    ErrMissingTenantContext     = errors.New("systemplane: missing tenant ID in context")
    ErrInvalidTenantID          = errors.New("systemplane: invalid tenant ID")
    ErrTenantScopeNotRegistered = errors.New("systemplane: key was not registered via RegisterTenantScoped")
)
```

Existing sentinels preserved verbatim: `ErrClosed`, `ErrNotStarted`, `ErrRegisterAfterStart`, `ErrUnknownKey`, `ErrValidation`, `ErrDuplicateKey` (`errors.go:6-25`). All existing wrapping (`fmt.Errorf("%w: ...", ...)`) maintained.

## 7. Admin HTTP Surface

Three additive routes mounted at existing prefix (default `/system`):
```
GET    :prefix/:namespace/:key/tenants               — ListTenantsForKey
PUT    :prefix/:namespace/:key/tenants/:tenantID     — SetForTenant  (body: {"value": ...})
DELETE :prefix/:namespace/:key/tenants/:tenantID     — DeleteForTenant
```

Routes registered after the three existing routes at `admin.go:110-113`. Tenant ID path param validated via `core.IsValidTenantID` at handler entry; 400 on invalid. Response redaction applied per existing `KeyRedaction` policy — same call path as handlers at `admin.go:185-208`.

### 7.1 Authorizer Evolution (Tension 3 — RESOLVED: Option A)

**Recommendation: Option A — additive `WithTenantAuthorizer`.**

```go
func WithTenantAuthorizer(fn func(c *fiber.Ctx, action, tenantID string) error) MountOption
```

- Existing `WithAuthorizer(fn func(*fiber.Ctx, string) error)` signature at `admin.go:60-66` stays byte-compatible. Every current consumer continues to compile.
- Tenant routes first try `WithTenantAuthorizer` if configured. If not configured, the tenant routes fall back to the existing `WithAuthorizer` with action `"write"` or `"read"` — the tenantID is not passed, and the caller must audit policy on `:tenantID` path param themselves. For the additive-first discipline of lib-commons this is the only safe choice.
- Option B (changing `WithAuthorizer` signature to `func(c *fiber.Ctx, AuthContext)`) is cleaner long-term but breaks every v5 consumer that already wired an authorizer — violates the zero-breaking-change mandate.

**Decision: Option A, shipped.** Dissent possible in Gate 3 only if a maintainer argues the fallback semantics are footgunny enough to warrant the deny-all-by-default escalation for tenant routes when no tenant authorizer is set. Recommend the default-deny escalation as a conservative fallback — tenant routes return 403 unless `WithTenantAuthorizer` is explicitly configured, mirroring `admin.go:32-38`'s deny-all default.

## 8. Concurrency + Timing Constraints

- Startup hydration: `O(rows)` linear. At 10k rows a scan completes in ~200ms (Postgres `ORDER BY namespace, key` on a covering index; MongoDB `Find({})` cursor). Budget 1s before warning — well above expected cardinality in practice (PRD §8: ~1,200 entries for 100 tenants × 12 keys).
- `GetForTenant` hit path: sub-microsecond (one `RLock`, two map reads, one unlock). Benchmarked in `TestGetForTenant_Benchmark` per PRD AC15.
- `GetForTenant` miss path in lazy mode: 5ms Postgres (`QueryRowContext` → scan → unmarshal), 10ms MongoDB (`FindOne` + BSON decode). Acceptable for rare first-touch.
- Debounce window: 100ms unchanged. Widened key guarantees intra-tuple coalescing only.

## 9. Backward Compatibility Matrix

| Existing Method          | Behavior After                                                                          | Proof                                         |
|--------------------------|-----------------------------------------------------------------------------------------|-----------------------------------------------|
| `Register()`             | Unchanged. Keys registered here are not tenant-scoped.                                  | `api_compat_test.go: TestRegister_*`          |
| `Get(ns, key)`           | Unchanged. Returns `cache[nk]` (global only); tenant rows invisible.                   | `TestGet_IgnoresTenantOverrides`              |
| `Set(ns, key, v, actor)` | Unchanged. Writes row with `tenant_id='_global'` (backfill-transparent).                | Backfill + `TestSet_WritesGlobalRow`          |
| `OnChange(ns, key, fn)`  | Unchanged. Fires only for `tenant_id='_global'` changefeed events.                     | `TestOnChange_DoesNotFireOnTenantWrites` (D5) |
| Admin GET/PUT            | Unchanged. Tenant sub-routes are additive paths under same prefix; no conflict.         | Existing route tests untouched                |
| `NewForTesting(s, ...)`  | Unchanged. `TestStore` grows five methods; older callers fail to compile (intentional — internal test helper per `client_testing.go:1-3` explicit "API stability is not promised"). | `TestStore_ContractCompat` |

Non-goal clarification: `NewForTesting`'s `TestStore` interface is explicitly marked "DO NOT USE IN PRODUCTION. API stability is not promised" (`client_testing.go:15-16`). Growing it is by design and doesn't violate the v5 compat promise.

## 10. File-by-File Implementation Plan

### New Files
- `commons/systemplane/tenant_scoped.go` (~250 LOC) — `RegisterTenantScoped`, `GetForTenant`, `SetForTenant`, `DeleteForTenant`, `ListTenantsForKey`, typed accessor mirrors, `extractTenantID` helper.
- `commons/systemplane/tenant_storage.go` (~80 LOC) — thin dispatch layer wrapping `store.GetTenantValue` / `SetTenantValue` / `DeleteTenantValue` with span + telemetry + canonical marshaling.
- `commons/systemplane/tenant_onchange.go` (~100 LOC) — `OnTenantChange`, `fireTenantSubscribers`, `tenantSubscription` struct, unsubscribe logic mirroring `onchange.go:58-73`.
- `commons/systemplane/tenant_cache.go` (~120 LOC) — LRU wrapper for lazy mode. Eager mode uses bare `map[string]map[nskey]any`.
- `commons/systemplane/api_compat_test.go` (~200 LOC) — pin every v5.0 method signature. Fails to compile on any signature change.

### Modified Files
- `commons/systemplane/client.go` — `Client` struct gains `tenantCache`, `tenantScopedRegistry`, `tenantSubscribers`, `tenantSubsMu`, `tenantLoadMode`; `newClient` initializes; `Start` hydrates; `onEvent` widens composite key; `refreshFromStore` routes global vs tenant.
- `commons/systemplane/register.go` — add `RegisterTenantScoped` that calls internal `register` with `tenantScoped=true` marker in `tenantScopedRegistry`.
- `commons/systemplane/options.go` — add `WithLazyTenantLoad(max int) Option`.
- `commons/systemplane/errors.go` — add three sentinels.
- `commons/systemplane/internal/store/store.go` — add five methods, grow `Entry` with `TenantID`, grow `Event` with `TenantID`.
- `commons/systemplane/internal/postgres/postgres.go` — four-statement DDL in `ensureSchema`, trigger payload rewrite, trigger extended to DELETE, implement five new Store methods.
- `commons/systemplane/internal/mongodb/mongodb.go` — backfill + index swap in `New`, `$match` adds delete, five new Store methods.
- `commons/systemplane/internal/debounce/debounce.go` — no code changes; the widened key is a pure Client-side concern.
- `commons/systemplane/admin/admin.go` — three routes, `WithTenantAuthorizer` option, default-deny escalation for tenant routes.
- `commons/systemplane/client_testing.go` — `TestStore` grows five methods, `testStoreAdapter` forwards them, `TestEntry`/`TestEvent` grow `TenantID`.
- `commons/systemplane/systemplanetest/` — add tenant contract tests invoked from both backends.

### Documentation Changes
- `commons/systemplane/MIGRATION_TENANT_SCOPED.md` (new) — opt-in guide, context setup, admin authorizer migration.
- `CLAUDE.md` — Systemplane section grows API summary for new methods.
- `README.md` — changelog entry; "Adds tenant-scoped systemplane keys (additive, non-breaking)."

## 11. Worked Example

Setup: `plugin-br-bank-transfer` boots against Postgres backend.

```go
// boot
c, _ := systemplane.NewPostgres(db, dsn, systemplane.WithLogger(l))
_ = c.RegisterTenantScoped("global", "fees.fail_closed_default", false,
    systemplane.WithValidator(func(v any) error {
        if _, ok := v.(bool); !ok { return errors.New("must be bool") }
        return nil
    }))
_ = c.Start(ctx)
// hydrate: List() returns no rows; cache["global:fees.fail_closed_default"] = false (default);
// tenantCache is empty.
```

Admin sets tenant-A override:
```go
ctxA := core.ContextWithTenantID(ctx, "tenant-A")
_ = c.SetForTenant(ctxA, "global", "fees.fail_closed_default", true, "admin-tool")
```

Internally:
1. `extractTenantID(ctxA)` → `"tenant-A"` (valid per `IsValidTenantID`).
2. Registry check: tenant-scoped, validator passes on `true`.
3. `store.SetTenantValue(ctx, "tenant-A", Entry{Namespace: "global", Key: "fees.fail_closed_default", Value: []byte("true"), UpdatedBy: "admin-tool"})`.
4. Postgres: `INSERT ... ON CONFLICT (namespace, key, tenant_id) DO UPDATE`.
5. Write-through: `tenantCache["tenant-A"]["global/fees.fail_closed_default"] = true`.
6. Trigger fires NOTIFY `{"namespace":"global","key":"fees.fail_closed_default","tenant_id":"tenant-A"}`.
7. `onEvent` debounce keys on `"global\x1ffees.fail_closed_default\x1ftenant-A"`.
8. After 100ms, `refreshFromStore("tenant-A", "global", "fees.fail_closed_default")` → cache idempotent update → fire `OnTenantChange` subscribers with `(ns="global", key="fees.fail_closed_default", tenantID="tenant-A", newValue=true)`.

Three resolution observations:
- `c.GetForTenant(ctxA, "global", "fees.fail_closed_default")` → `(true, true, nil)`.
- `c.GetForTenant(ctxB, "global", "fees.fail_closed_default")` where ctxB has `tenant-B` → `(false, true, nil)` (falls to global default).
- `c.Get("global", "fees.fail_closed_default")` → `(false, true)` (legacy path unchanged, reads global only).

Subscribers:
- `OnChange("global", "fees.fail_closed_default", fn)` subscribers: **not fired** (tenant_id != '_global').
- `OnTenantChange("global", "fees.fail_closed_default", fn)` subscribers: **fired once** with `("global", "fees.fail_closed_default", "tenant-A", true)`.

## 12. Open Questions for Gate 3

- Typed accessor variants: TRD proposes all five (`GetStringForTenant`, `GetIntForTenant`, `GetBoolForTenant`, `GetFloat64ForTenant`, `GetDurationForTenant`). Dissent possible if maintainers prefer only the three hot paths (`String`, `Int`, `Bool`) — but symmetry with `get.go:149-232` argues for all five.
- Migration doc rollback section: roll-forward is idempotent (sentinel backfill is safe to re-run). Rollback — dropping `tenant_id` column + new unique index — IS destructive and would drop tenant override data silently. Recommendation: document rollback as explicitly unsupported; require data export if a downgrade is needed.
- Delete trigger extension on Postgres adds one more DDL line to `ensureSchema`. Any concern about unexpected DELETE-fires on existing install upgrades? (There aren't any DELETEs in current Set path, so no observable behavior change for existing callers.)

## 13. Approval

- [ ] Gate 2 approved by: Fred Amaral
- [ ] Date: ___
