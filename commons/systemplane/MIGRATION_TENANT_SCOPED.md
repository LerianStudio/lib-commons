# Tenant-Scoped Systemplane — Adoption Guide

This document is the consumer-facing adoption guide for the tenant-scoped
systemplane feature landed in v5. It is additive and non-breaking: every
existing `Register` / `Set` / `Get` / `OnChange` call continues to work
unchanged. If you do not need per-tenant overrides, you do not need to read
this doc.

If you are adopting tenant overrides for at least one key, this guide walks
through:

1. Opting in via `RegisterTenantScoped`.
2. Wiring the tenant ID into `context.Context` at the call site.
3. Migrating your admin authorizer from `WithAuthorizer` to
   `WithTenantAuthorizer`.
4. Two-phase rolling-deploy migration and `WithTenantSchemaEnabled`.
5. Rollback stance and the MongoDB `_id` rewrite migration.
6. Performance characteristics and when to choose eager vs lazy mode.
7. New sentinel errors and `OnTenantChange` semantics.
8. A full lifecycle example.

Primary audience: `plugin-br-bank-transfer` Phase 3, and any other
downstream service consuming `commons/systemplane`.

---

## 1. Opt-in via `RegisterTenantScoped`

`Register` still exists and is unchanged. Legacy keys registered via
`Register` remain **globals-only** — their rows are stored under the
reserved `_global` sentinel and the only reader is `Get` / `OnChange`. The
tenant-scoped methods return `ErrTenantScopeNotRegistered` for those keys.

Keys that should accept per-tenant overrides are declared with
`RegisterTenantScoped` instead. The signature is identical to `Register`
and the same `WithDescription` / `WithValidator` / `WithRedaction` options
apply.

```go
// BEFORE — globals-only key.
_ = client.Register("global", "fees.fail_closed_default", false,
    systemplane.WithDescription("If true, reject transactions on fee-service timeout"),
    systemplane.WithValidator(func(v any) error {
        if _, ok := v.(bool); !ok {
            return errors.New("must be bool")
        }
        return nil
    }),
)

// AFTER — eligible for per-tenant overrides. Same options, same default,
// same validation. The legacy Get/Set/OnChange surface still works exactly
// the same way — those operate on the shared global row.
_ = client.RegisterTenantScoped("global", "fees.fail_closed_default", false,
    systemplane.WithDescription("If true, reject transactions on fee-service timeout"),
    systemplane.WithValidator(func(v any) error {
        if _, ok := v.(bool); !ok {
            return errors.New("must be bool")
        }
        return nil
    }),
)
```

Rules:

- `RegisterTenantScoped` must be called **before** `Start`; after Start it
  returns `ErrRegisterAfterStart`.
- Registering the same `(namespace, key)` pair twice — via any mix of
  `Register` and `RegisterTenantScoped` — returns `ErrDuplicateKey`.
- If a `WithValidator` rejects `defaultValue`, registration fails with
  `ErrValidation`. This is a fail-fast signal; a broken default would
  cause silent misbehavior later.
- The validator runs on the default at registration **and** on every
  per-tenant `SetForTenant`. Redaction applies identically to the global
  row and to every tenant override.

Keep in mind that the tenant-scoped methods (`SetForTenant`,
`GetForTenant`, `DeleteForTenant`, `OnTenantChange`, and the typed
accessor mirrors) reject globals-only keys with
`ErrTenantScopeNotRegistered`. If you call `SetForTenant` against a key
declared by `Register`, the write is rejected without touching the store.

---

## 2. Context setup

All four tenant methods — `SetForTenant`, `GetForTenant`,
`DeleteForTenant`, `GetStringForTenant` / `GetIntForTenant` /
`GetBoolForTenant` / `GetFloat64ForTenant` / `GetDurationForTenant` —
require the tenant ID to be present in `context.Context`. The Client
reads it with `core.GetTenantIDContext`; you set it with
`core.ContextWithTenantID`.

```go
import (
    "context"

    "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

ctx := core.ContextWithTenantID(context.Background(), "tenant-A")
// Now every SetForTenant / GetForTenant / DeleteForTenant / GetXForTenant
// call using ctx resolves to "tenant-A".
```

In a real service the tenant ID is typically populated by an earlier
middleware (for example `commons/tenant-manager/middleware`) that
authenticates the request and attaches the tenant ID to the request
context — the handler simply propagates that context downstream.
`ListTenantsForKey` is the one exception: it is an administrative
reflection query and does not require a tenant in ctx.

### Fail-closed resolution

There is **no silent fallback to a shared global when the tenant is
missing**. This is decision D8 in the TRD and is enforced at every
tenant-scoped entry point:

| Condition                                                    | Result                                                                  |
| ------------------------------------------------------------ | ----------------------------------------------------------------------- |
| ctx has no tenant ID                                         | `ErrMissingTenantContext`                                               |
| ctx has a tenant ID but it fails `core.IsValidTenantID`      | `ErrInvalidTenantID`                                                    |
| ctx has `"_global"` as the tenant ID                         | `ErrInvalidTenantID` — `_global` is the reserved shared-row sentinel    |
| ctx has a valid tenant ID                                    | Normal resolution — per-tenant override → global row → registered default |

Callers who want the shared global value must use the legacy `Get`,
`GetString`, etc. family — those methods do not extract a tenant from
ctx and always read the `_global` row.

### Tenant ID rules

Valid tenant IDs match `^[a-zA-Z0-9][a-zA-Z0-9_-]*$` and are at most
`core.MaxTenantIDLength` (256) characters. The regex lives in
`commons/tenant-manager/core/validation.go` and is the canonical rule —
if you can get a value past `core.IsValidTenantID`, the systemplane
Client will accept it.

The `_global` sentinel cannot be used as a tenant ID because its leading
underscore is already outside the regex. The Client rejects it
explicitly too, as defense-in-depth, so the error message is obvious
rather than a generic "regex didn't match".

### Per-request read at the natural point

Because the hit path is sub-microsecond (see §6), downstream code can
call `GetForTenant` (or one of its typed accessor mirrors) at the
natural logical point — for example once per fee computation inside the
transfer handler — without caching the result at the call site. Caching
is redundant and creates staleness the hot-reload subscription cannot
fix.

---

## 3. Admin authorizer migration

The admin package now exposes **six** routes, split across two
authorizer hooks:

```text
# Legacy global routes — use WithAuthorizer.
GET    :prefix/:namespace                         list entries in a namespace
GET    :prefix/:namespace/:key                    read a single global entry
PUT    :prefix/:namespace/:key                    write the global row

# New tenant-scoped routes — use WithTenantAuthorizer.
GET    :prefix/:namespace/:key/tenants            list tenants with an override
PUT    :prefix/:namespace/:key/tenants/:tenantID  write a tenant override
DELETE :prefix/:namespace/:key/tenants/:tenantID  remove a tenant override
```

The default prefix is still `/system`. Override it with
`WithPathPrefix(...)`.

`WithAuthorizer(fn func(c *fiber.Ctx, action string) error)` continues to
work **unchanged** on the legacy global routes. No code changes are
required if you do not expose tenant routes.

### Default-deny escalation

If `WithTenantAuthorizer` is **not** configured, every tenant route
returns 403 Forbidden — regardless of any `WithAuthorizer` hook you
passed. This is a deliberate conservative choice:

1. `WithAuthorizer` predates tenant support. Its signature
   `(*fiber.Ctx, action string)` carries no tenant information, so it
   cannot express policies like "only allow writes to tenants the
   caller owns".
2. Silently falling back to `WithAuthorizer` on tenant routes would
   let a service that configured only `WithAuthorizer` accept tenant
   writes it was never authorized to handle — a silent privilege
   escalation.
3. Forcing consumers to opt in with `WithTenantAuthorizer` makes the
   upgrade conscious and auditable.

See the `WithTenantAuthorizer` docblock in
`commons/systemplane/admin/admin.go:94-125` for the authoritative
rationale.

### Wiring `WithTenantAuthorizer`

`WithTenantAuthorizer(fn func(c *fiber.Ctx, action, tenantID string) error)`
is invoked before every tenant-scoped handler. The arguments:

- `c` — the Fiber context, identical to what `WithAuthorizer` receives.
- `action` — `"read"` for GET, `"write"` for PUT and DELETE.
- `tenantID` — the `:tenantID` URL segment. For the
  `GET :key/tenants` list route (which has no `:tenantID` segment)
  the argument is the empty string, so policies can distinguish the
  list action from per-tenant operations.

Return a non-nil error to reject with 403 Forbidden. The error message
is surfaced in the JSON response body, so keep it auditable but don't
leak internals.

```go
// Minimal example — a real implementation would consult the caller's
// principal from the Fiber context and cross-check against the tenant
// they're operating on.
admin.Mount(app, systemplaneClient,
    admin.WithAuthorizer(func(c *fiber.Ctx, action string) error {
        // Existing policy for legacy global routes — unchanged.
        return myPolicy.CheckGlobal(c, action)
    }),
    admin.WithTenantAuthorizer(func(c *fiber.Ctx, action, tenantID string) error {
        // New policy for tenant routes. tenantID is "" on the tenants-list route.
        if tenantID == "" {
            return myPolicy.CheckListTenants(c, action)
        }
        return myPolicy.CheckTenant(c, action, tenantID)
    }),
)
```

The admin layer validates `:tenantID` (via `core.IsValidTenantID` and
the `_global` sentinel check) **before** calling your authorizer, so
your hook is guaranteed to receive a well-formed tenant ID or `""`.

---

## 4. Two-phase rolling-deploy migration

The tenant-scoped schema change drops the legacy `(namespace, key)`
primary key (Postgres) / unique index (MongoDB) and replaces it with the
composite `(namespace, key, tenant_id)`. Pre-tenant `lib-commons`
binaries (v5.0.x) upsert via `ON CONFLICT (namespace, key)`; if those
binaries hit the evolved schema they fail with
`no unique or exclusion constraint matching the ON CONFLICT
specification` on the first write. Every rolling deploy therefore needs
a window where the old and new binaries coexist **against the legacy
schema**.

Phase 2 is opt-in via [`WithTenantSchemaEnabled()`](./options.go). The
default is phase 1 — compat mode. **This is deliberate: tenant writes
are rejected with [`ErrTenantSchemaNotEnabled`](./errors.go) until you
explicitly flip the option.**

### Phase 1 — deploy v5.1 everywhere

1. Upgrade the library to v5.1+ across every consumer that shares the
   database.
2. Use the default Client construction (no `WithTenantSchemaEnabled`).
   The backend creates the table / collection with the legacy unique
   shape, adds the `tenant_id` column with the `_global` default, and
   installs the NOTIFY trigger (Postgres) — but does **not** drop the
   legacy PK and does **not** run the MongoDB `_id` rewrite migration.
3. Mixed-version state is safe: v5.0.x binaries continue to upsert via
   `ON CONFLICT (namespace, key)` against the same table/collection;
   v5.1 binaries also upsert against `(namespace, key)` in phase 1.
4. Tenant writes (`SetForTenant`, `DeleteForTenant`, and the admin
   `PUT /:key/tenants/:tenantID` / `DELETE /:key/tenants/:tenantID`
   routes) return `ErrTenantSchemaNotEnabled`. This is expected. Tenant
   reads (`GetForTenant`, `ListTenantsForKey`) fall through to the
   registered default because no tenant rows exist yet.

### Phase transition — verify every consumer is on v5.1+

Before flipping phase 2, confirm that **no** v5.0.x binaries are still
running against this database. The inventory check is deployment-
specific — a combination of Kubernetes image tags, service dashboards,
and `go.mod` audits of every consumer. Any missed binary will start
failing its first upsert the moment phase 2 commits.

### Phase 2 — opt in to the composite unique

```go
c, err := systemplane.NewPostgres(db, dsn,
    systemplane.WithLogger(logger),
    systemplane.WithTenantSchemaEnabled(), // enable phase 2
)
```

On the next `Start()` call the backend runs the schema evolution:

- **Postgres:** `DROP CONSTRAINT IF EXISTS <table>_pkey`, then
  `CREATE UNIQUE INDEX IF NOT EXISTS <table>_pkey_v2 ON ... (namespace,
  key, tenant_id)`. Before the drop, the backend runs a
  duplicate-detection query (see H8 below); the migration aborts with a
  clear error if the pre-migration state is ambiguous.
- **MongoDB:** drops the `namespace_1_key_1` unique index and runs the
  `ObjectId _id` → compound-`_id` rewrite migration. Before the rewrite,
  the backend runs an aggregation that aborts if the same
  `(namespace, key)` has both a missing-`tenant_id` document and a
  `_global` sibling.

Both the Postgres and MongoDB migrations are **idempotent**. Crashes
mid-migration are recoverable: restart the Client and the remaining
steps run to completion.

### Duplicate-detection errors (H6/H8)

If the pre-migration data is ambiguous, the migration aborts with one of:

- **Postgres:** `ambiguous pre-migration state: multiple rows for (ns,
  key) with inconsistent tenant_id — consolidate before enabling phase
  2` (carries an affected-row count).
- **MongoDB:** `ambiguous pre-migration state: documents exist with
  missing tenant_id AND a '_global' sibling for the same (namespace,
  key) — consolidate before enabling phase 2`.

The fix is to manually consolidate the rows (pick which one is
authoritative, delete the other) **before** re-running with
`WithTenantSchemaEnabled()`. The backend refuses to silently backfill
and lose data.

### DO NOT flip to phase 2 until every consumer is upgraded

Flipping `WithTenantSchemaEnabled()` with any v5.0.x binaries still
running against the database will break those binaries' upserts the
next time they write. The legacy `ON CONFLICT (namespace, key)` clause
has no matching unique constraint once phase 2 drops it, and the
Postgres driver returns
`no unique or exclusion constraint matching the ON CONFLICT
specification`. MongoDB fails the upsert with a duplicate-key error on
the compound `_id` because the v5.0.x binary still tries to allocate an
`ObjectId _id`.

### Cross-references

- [`ErrTenantSchemaNotEnabled`](./errors.go) — the sentinel returned
  during phase 1; match with `errors.Is`.
- [`WithTenantSchemaEnabled()`](./options.go) — the opt-in option.
- Postgres phase-2 pre-flight and DDL: `internal/postgres/postgres_schema.go`.
- MongoDB phase-2 pre-flight and migration: `internal/mongodb/mongodb_migration.go`.

---

## 5. Rollback and forward-only migration

The feature is **additive**. Opting back out — switching a key from
`RegisterTenantScoped` to plain `Register` — is supported by the code
without change: any tenant override rows already in the store remain,
but the Client will read exclusively through the globals-only surface
so they become inert.

DDL rollback (dropping the Postgres `tenant_id` column or the MongoDB
compound `_id` index / document shape) is **not supported by the
library**. Running an older binary against the evolved schema would
silently drop every tenant override row on first write. If a service
must downgrade, it must:

1. Export every tenant override via `ListTenantsForKey` +
   `GetForTenant` with `core.ContextWithTenantID` before running the
   older binary.
2. Restore those overrides after upgrading again (or discard them if
   the downgrade is intended to be permanent).

We intentionally did **not** ship an automatic downgrade path: the
library cannot tell the difference between "I want to erase tenant
overrides" and "I want to preserve them and migrate later", and
silently dropping rows is unacceptable.

### MongoDB `_id` rewrite migration

The MongoDB backend stores entries with a compound BSON document `_id`
`{namespace, key, tenant_id}`. On first boot against a collection that
was populated by a previous v5 release (which used `ObjectId _id`
plus application-level unique indexes), the Client detects the legacy
shape and rewrites every document's `_id` into the compound form.

Important properties:

- The migration is **idempotent**. Crashes mid-migration are
  recoverable: restarting the Client resumes from where it left off
  (the detection is per-document).
- Reads concurrent with the rewrite are safe — the rewrite uses a
  temporary marker field so an interrupted document is never lost.
- The migration runs once, synchronously, before `Start` returns.
  Expect a one-time cold-start delay proportional to the number of
  existing entries.

For fresh MongoDB collections, the rewrite path is a no-op.

> **Transient duplicate window during rewrite.** The rewrite is not a
> single atomic transaction — it processes one document at a time
> (insert-new-id → delete-old-id). A long-running read session that
> begins before `rewriteObjectIDDocuments` starts and is still active
> while a specific document is mid-migration can observe BOTH the
> legacy `ObjectId _id` document AND the compound-`_id` document for
> the same `(namespace, key)`. The window is bounded per row
> (typically sub-millisecond), closes on the next read, and does not
> affect `Get` / `GetForTenant` correctness (both filter by the
> compound fields, not `_id`).
>
> Caller implication: if a consumer holds a long-lived cursor over
> `List(namespace)` during `Start()`, it may see transient duplicates
> for the same `(namespace, key)` pair while the rewrite is running.
> The recommendation is to avoid running the first-boot migration
> concurrent with heavy read traffic; either schedule the upgrade in
> a maintenance window or accept the bounded duplicate window on the
> read side.

The Postgres backend's DDL evolution is a simpler ALTER TABLE adding
`tenant_id TEXT NOT NULL DEFAULT '_global'` plus a composite unique
index; existing rows are backfilled with `_global` automatically by
the column default.

---

## 6. Performance notes and load modes

The Client tracks two modes for tenant value caching:

| Mode  | Behavior                                                                              | Option                                    |
| ----- | ------------------------------------------------------------------------------------- | ----------------------------------------- |
| Eager | Hydrate every tenant row at `Start`; keep the full tenant cache in memory forever.    | Default — no option required.             |
| Lazy  | Populate a bounded LRU on first read per `(tenant, key)` tuple; evict LRU when full.  | `systemplane.WithLazyTenantLoad(maxEntries)` |

Observed benchmark numbers on an Apple M5 Max against an in-memory
`TestStore` (see `commons/systemplane/bench_tenant_test.go`):

| Benchmark                        | Observed                          | PRD AC15 target          |
| -------------------------------- | --------------------------------- | ------------------------ |
| `GetForTenant_Hit` (eager)       | ~133 ns/op                         | < 1 µs/op                |
| `GetForTenant_Miss_Eager`        | ~130 ns/op (cascade to global)    | < 2 µs/op                |
| `GetForTenant_Miss_Lazy`         | ~400 ns/op (in-memory store)       | informational, no target |

Both hit and miss paths clear the AC15 target with 5–10× headroom
against the in-memory test store. Against a real database the lazy
miss path inflates to 5–10 ms on the first touch per `(tenant, key)`;
the eager hit path is unaffected because it never touches the
backend.

### When to choose which

- **Eager (default)** — small to medium tenant populations (up to a
  few thousand tenants × dozens of keys). Memory cost is `O(tenants
  × keys)`; each entry is a JSON-round-tripped `any`. Reads are
  sub-microsecond with no backend touch.
- **Lazy (`WithLazyTenantLoad(N)`)** — high-cardinality deployments
  (>10 k tenants) where the active set is small. Trade first-touch
  latency (~5–10 ms) for a bounded memory footprint. The LRU capacity
  `N` should be sized to the realistic active-tenant count, not the
  total population.

Switching between modes requires a Client restart (the option is
applied at construction time). Both modes present the same public
API; only the cache implementation differs.

### What you should **not** cache at the call site

Because the hit path is ~133 ns/op, caching the result of
`GetForTenant` in your own handler creates staleness the
`OnTenantChange` subscription cannot fix. The recommended pattern is:
**call `GetForTenant` at the natural logical point and let the
Client's cache be the only cache**. For hot inner loops that would
benefit from zero-allocation reads, register an `OnTenantChange`
subscriber that writes the decoded value into a local atomic and
read that instead — this is the same pattern the Client itself uses
internally.

---

## 7. New sentinel errors

| Sentinel                             | Meaning                                                                                       |
| ------------------------------------ | --------------------------------------------------------------------------------------------- |
| `systemplane.ErrMissingTenantContext` | The ctx carries no tenant ID. Wrap-compatible with `errors.Is`.                              |
| `systemplane.ErrInvalidTenantID`      | The tenant ID failed `core.IsValidTenantID` or equals the `_global` sentinel.                 |
| `systemplane.ErrTenantScopeNotRegistered` | The key was registered via `Register`, not `RegisterTenantScoped`; tenant overrides are not permitted. |

All three are returned from `SetForTenant`, `GetForTenant`,
`DeleteForTenant`, and each of the typed accessor mirrors
(`GetStringForTenant`, etc.). They are `errors.Is` / `errors.As`
compatible — the admin package, for example, uses
`errors.Is(err, systemplane.ErrTenantScopeNotRegistered)` to map to
400 with a specific title string.

The existing sentinels (`ErrClosed`, `ErrNotStarted`, `ErrUnknownKey`,
`ErrValidation`, `ErrRegisterAfterStart`, `ErrDuplicateKey`) continue
to work unchanged and are also surfaced through the tenant methods as
appropriate.

---

## 8. `OnTenantChange` semantics

`OnTenantChange(namespace, key, fn)` is the tenant-aware sibling of
`OnChange`. Two invariants matter for reconcilers:

- **The split is on the row's tenant_id, not on whether the key was
  registered as tenant-scoped.** `OnChange` fires exclusively for
  changes to the shared global row. `OnTenantChange` fires
  exclusively for changes to per-tenant rows. A tenant-scoped key's
  global row changing fires `OnChange`, not `OnTenantChange` — this
  is PRD AC8.
- **Delete fires with the registered default.** When a tenant
  override is removed via `DeleteForTenant`, the subscriber receives
  `newValue = def.defaultValue` (not the pre-delete override value).
  The semantics are "this tenant has reverted to global/default", so
  reconcilers see the same value they would see on first startup
  before any override was written.

The callback signature carries the tenant ID so a single subscription
observes every tenant:

```go
unsubscribe := client.OnTenantChange("global", "fees.fail_closed_default",
    func(ns, key, tenantID string, newValue any) {
        log.Info("tenant config changed",
            zap.String("tenant", tenantID),
            zap.Any("new", newValue))
    })
defer unsubscribe()
```

Subscribers fire from the **changefeed echo**, not synchronously from
`SetForTenant` / `DeleteForTenant`. This preserves the
`set.go:18-21` invariant: subscribers observe backend state changes,
not in-process writes. In practice this means a single-process test
sees the callback fire after a short round-trip through the store's
change-stream / LISTEN-NOTIFY path (bounded by `WithDebounce`, default
100 ms).

Callbacks are invoked sequentially under panic recovery via
`commons/runtime.RecoverAndLog`; a panicking callback does not block
subsequent subscribers. Unsubscribe is safe to call multiple times
(guarded by `sync.Once` internally).

Registering `OnTenantChange` against a key declared by `Register`
(globals-only) returns a no-op unsubscribe — silent tolerance
mirroring `OnChange` for unknown keys. Use `OnChange` for those keys.

---

## 8.1 Operational caveats

Two backend-specific behaviors are worth calling out explicitly because
they can surprise operators during or after a rolling upgrade.

### MongoDB polling-mode consumers miss DELETE events

The MongoDB backend defaults to change streams for the subscribe path,
which requires a replica set (or a sharded cluster with config
replica sets). Standalone MongoDB deployments must opt into polling
mode via `systemplane.WithPollInterval(d)` — the Client then runs a
periodic `find + watermark` loop instead of a persistent change
stream.

**Polling mode cannot observe DELETE events.** A document that was
inserted and then deleted between two polling ticks leaves no trace
for the poller to find — the watermark advances past it and the
delete is silently skipped. In the tenant-scoped world this is a
hard violation of PRD AC9: `DeleteForTenant` is supposed to fire
`OnTenantChange` with the registered default as the "reverted to
global" signal, and polling consumers will not see that signal.

Recommendation: if your service uses `RegisterTenantScoped` +
`DeleteForTenant`, run MongoDB as a replica set and keep change
streams. If polling is a hard constraint (development-only
standalone, air-gapped single-node deployments), treat
`OnTenantChange` subscriptions as best-effort for INSERT/UPDATE only
and reconcile deletions through a separate mechanism (e.g. a
periodic `ListTenantsForKey` diff).

The backend-agnostic contract suite skips
`TenantSubscribeReceivesDeleteEvent` for polling-mode integration
tests via `SkipSubtest`, so this gap is explicitly known and
documented rather than silently accepted.

### Postgres NOTIFY now fires on DELETE

Prior to tenant-scoped routing, the Postgres LISTEN/NOTIFY trigger
fired only on `INSERT` and `UPDATE` of rows in
`systemplane_entries`. Delete-event routing (PRD AC9) required
extending the trigger to fire on `DELETE` as well so subscribers
can observe `DeleteForTenant`.

Legacy v5.0.x consumers that subscribed to the same Postgres channel
with their own LISTEN handlers will now receive more notification
fanout than before. The payload shape is unchanged (JSON with
namespace, key, tenant_id); older clients that are JSON-tolerant of
the extra `tenant_id` field will decode the envelope successfully
and their downstream `Get("global", "<key>")` call returns the
registered default for the deleted row. No behavioral regression,
just more events on the wire.

If a consumer specifically needs to filter out DELETE events at the
listen side, examine the `tenant_id` on the NOTIFY payload rather
than relying on the event's presence/absence.

---

## 9. Full lifecycle example

A condensed end-to-end example showing registration, boot, an admin
override, a request-time read, and a subscriber.

```go
import (
    "context"
    "errors"
    "log"

    "github.com/LerianStudio/lib-commons/v5/commons/systemplane"
    "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// --- Boot path ----------------------------------------------------------

c, err := systemplane.NewPostgres(db, dsn, systemplane.WithLogger(logger))
if err != nil {
    return fmt.Errorf("systemplane init: %w", err)
}

if err := c.RegisterTenantScoped("global", "fees.fail_closed_default", false,
    systemplane.WithDescription("If true, reject transactions on fee-service timeout"),
    systemplane.WithValidator(func(v any) error {
        if _, ok := v.(bool); !ok {
            return errors.New("must be bool")
        }
        return nil
    }),
); err != nil {
    return err
}

if err := c.Start(ctx); err != nil {
    return err
}
defer c.Close()

// --- Admin path (writing a tenant override) -----------------------------
//
// Most services will expose this through the admin HTTP routes under
// /system (see §3). For a programmatic example, wire the tenant ID
// into ctx explicitly:

ctxA := core.ContextWithTenantID(ctx, "tenant-A")
if err := c.SetForTenant(ctxA, "global", "fees.fail_closed_default", true, "admin-tool"); err != nil {
    return err
}

// --- Request path (reading in the transfer handler) ---------------------

func handleTransfer(ctx context.Context) error {
    // ctx has been populated by upstream middleware with the request's tenantID.
    // Nothing else to do — the Client extracts it from ctx.
    failClosed, err := c.GetBoolForTenant(ctx, "global", "fees.fail_closed_default")
    if err != nil {
        return fmt.Errorf("resolve fail_closed: %w", err)
    }

    if failClosed {
        // policy branch A
    } else {
        // policy branch B
    }
    return nil
}

// --- Reconciler path (reacting to config changes) -----------------------

unsubscribe := c.OnTenantChange("global", "fees.fail_closed_default",
    func(ns, key, tenantID string, newValue any) {
        logger.Info(ctx, "tenant config changed",
            log.String("tenant", tenantID),
            log.Any("new_value", newValue))

        // Update any local caches / feature-flag mirrors / etc. The callback
        // fires for every tenant-scoped change to this (ns, key); multi-tenant
        // services typically index their local state by tenantID.
    })
defer unsubscribe()
```

---

## 10. Migration checklist

For a service adopting at least one tenant-scoped key:

- [ ] Identify keys that need per-tenant overrides. Leave everything
      else on `Register`.
- [ ] Replace `Register` with `RegisterTenantScoped` for those keys.
      All options (`WithDescription`, `WithValidator`, `WithRedaction`)
      carry over unchanged.
- [ ] In request-handling code, replace `Get` / `GetString` / etc.
      with `GetForTenant` / `GetStringForTenant` / etc. at the call
      sites that should honor per-tenant overrides. Leave
      truly-global reads (e.g. bootstrap metadata) on the legacy
      accessors.
- [ ] Confirm every call site has a populated tenant ID in ctx. If
      there is no middleware doing this today, add
      `commons/tenant-manager/middleware` or an equivalent upstream
      extractor.
- [ ] If exposing admin routes, wire `WithTenantAuthorizer`. Confirm
      the default-deny behavior matches your intent.
- [ ] Register `OnTenantChange` subscribers for any reconciler that
      previously listened via `OnChange` and needs to see per-tenant
      changes. Keep `OnChange` subscriptions for the global-row
      signal path (AC8).
- [ ] For deployments with very high tenant cardinality (>10 k), add
      `systemplane.WithLazyTenantLoad(N)` where `N` is the active
      tenant count.
- [ ] Review the rollback stance (§5) with operations before first
      production rollout.
- [ ] No new environment variables are introduced by this feature —
      `.env.reference` is unchanged.
