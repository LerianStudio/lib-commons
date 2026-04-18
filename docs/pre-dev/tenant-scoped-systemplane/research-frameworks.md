# Framework Research — `tenant-scoped-systemplane`

**Feature:** per-tenant row overrides with change propagation over the existing dual-backend (Postgres LISTEN/NOTIFY + MongoDB change streams) systemplane.
**Module:** `github.com/LerianStudio/lib-commons/v5` (Go 1.25.9)
**Current deps of interest:** `github.com/jackc/pgx/v5 v5.9.1`, `go.mongodb.org/mongo-driver/v2 v2.5.0`, `github.com/testcontainers/testcontainers-go/modules/mongodb v0.41.0`.
**Manifest:** `/Users/fredamaral/repos/lerianstudio/lib-commons-better-systemplane/go.mod`.

---

## 1. pgx v5 LISTEN/NOTIFY

### 1.1 Connection model: `pgxpool.Pool` does not expose `Listen`
`pgxpool.Pool` has **no `Listen` method**. LISTEN binds to a single backend session — the session state dies the moment the connection is returned to the pool, so LISTEN is fundamentally incompatible with pool-return semantics. The canonical path in pgx v5:

1. Open a **dedicated** `*pgx.Conn` via `pgx.Connect(ctx, dsn)` (this is what the current `internal/postgres` already does — see `postgres.go:1-80`, which takes `ListenDSN` separately from the `*sql.DB`), OR
2. `pool.Acquire(ctx)` → `conn.Conn()` → **never call `Release()` while you still want to LISTEN**. If you must share a pool, `AcquireFunc(ctx, fn)` only helps for short-lived borrows; for LISTEN you keep the connection indefinitely. Reference: `https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool`.

The project's existing pattern (dedicated `pgx.Conn` + separate `*sql.DB` for queries) is the **recommended pattern** and should be preserved.

### 1.2 Multi-payload consumer loop
The canonical loop: `conn.Exec(ctx, "LISTEN channel")` once, then repeatedly `notif, err := conn.WaitForNotification(ctx)` — which blocks until a notification arrives or `ctx` is cancelled. `WaitForNotification` returns `*pgconn.Notification{Channel, PID, Payload}` and is the only API pgx exposes for this. Reference: `https://pkg.go.dev/github.com/jackc/pgx/v5#Conn.WaitForNotification`.

For long-running services:
- Each call drains exactly one notification; there is no batch API.
- Multiple channels on one connection are allowed (multiple `LISTEN x; LISTEN y`) — but for the systemplane case a **single channel with a structured JSON payload** is simpler than proliferating channels per tenant.
- `ctx` cancellation is the shutdown signal; callers drain remaining notifications if needed.

### 1.3 NOTIFY payload limit: 8000 bytes, hard error (not truncation)
PostgreSQL's published constraint: payloads **must be shorter than 8000 bytes**, enforced server-side. Exceeding this **errors at `NOTIFY` call time** (`invalid_parameter_value` — "payload string too long"), it does **not** silently truncate. This means pgx will surface an error on the `pg_notify(...)` or `NOTIFY` statement, never deliver a truncated message. Reference: `https://www.postgresql.org/docs/current/sql-notify.html`. Corroborated by multiple real-world failure reports (hatchet-dev#3087, awx#12999, graph-node#768).

**Design implication:** a payload of `{ns, key, tenant_id}` is ~100–200 bytes worst-case (UUID tenant_id + reasonable ns/key lengths). Well within budget. The additional field is safe. If payloads ever approach 1 KB, the standard workaround is "NOTIFY carries the PK only; consumer re-reads the row."

Also note server-wide: a full **8 GB async notify queue** — commits fail globally if the queue fills. Unlikely at systemplane's write volume but worth knowing for chaos testing.

### 1.4 Reconnect strategy after connection loss
pgx provides **no automatic reconnect** for a raw `*pgx.Conn`. The idiomatic pattern (already used in `internal/postgres/postgres.go` via `backoff.ExponentialWithJitter`, constants `backoffBase = 500ms`, `backoffCap = 30s`):

```
for {
    select { case <-ctx.Done(): return }
    conn, err := pgx.Connect(listenCtx, dsn)
    if err != nil { backoff.WaitContext(ctx); continue }
    if _, err := conn.Exec(ctx, "LISTEN systemplane_changes"); err != nil { conn.Close(ctx); continue }
    // IMPORTANT: after reconnect we've missed notifications between t_disconnect and t_reconnect.
    //            Must hydrate state from the store to catch up.
    for { n, err := conn.WaitForNotification(ctx); if err != nil { break } ; dispatch(n) }
}
```

The **hydration-on-reconnect** step is non-negotiable because NOTIFY is strictly fire-and-forget and delivered only to currently connected listeners. Postgres does not retain a backlog for disconnected sessions.

### 1.5 Switching payload `{ns,key}` → `{ns,key,tenant_id}`: pitfalls
- **Payload size:** fine (see 1.3).
- **Backward compatibility:** older consumers parsing only `{ns, key}` will silently ignore the `tenant_id` field if the JSON decoder uses default permissive behavior — safe additive change.
- **Empty / nil tenant:** must be represented unambiguously. Convention: omit the field (use `omitempty`) to mean "global/untenanted write", or emit an empty string explicitly. Choose one and stick to it — mixing leads to ambiguous filtering.
- **SQL trigger:** if NOTIFY is emitted from a trigger, `json_build_object('ns', NEW.namespace, 'key', NEW.key, 'tenant_id', NEW.tenant_id)::text` is the canonical shape.
- **Coalesced debounce:** the project already debounces (`options.go`, default 100 ms). Debounce keys must now include tenant_id — otherwise a burst of `(ns, key, tenantA)` + `(ns, key, tenantB)` collapses to a single fire and one tenant loses its echo.

### 1.6 Server-side payload filtering: **NOT SUPPORTED** (confirmed)
Postgres LISTEN filters **only by channel name**. There is no `WHERE` clause, no payload predicate, no content-based subscription. Every listener on `systemplane_changes` sees every notification — filtering happens in Go. Reference: `https://www.postgresql.org/docs/current/sql-notify.html` ("the payload string could be used to differentiate various cases" — explicitly client-side).

**Design implication:** if you want "only tenant X's subscriber fires on a tenant-X write", the filter lives in the Client's dispatch code (after JSON parse of the payload), not in Postgres.

---

## 2. MongoDB change streams (mongo-driver/v2)

Reference: `https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/mongo` and MongoDB change-streams manual.

### 2.1 `$match` on `fullDocument.tenant_id`
Pipelines are `mongo.Pipeline` = `[]bson.D`. Example:

```go
pipeline := mongo.Pipeline{
    bson.D{{"$match", bson.D{
        {"operationType", bson.D{{"$in", bson.A{"insert","update","replace","delete"}}}},
        {"fullDocument.tenant_id", tenantID}, // only present when fullDocument populated
    }}},
}
cs, err := coll.Watch(ctx, pipeline, options.ChangeStream().
    SetFullDocument(options.UpdateLookup))
```

**Critical gotcha:** `fullDocument` is populated **only for insert events by default**. Update events require `options.UpdateLookup` to materialize the post-image; **delete events never have a fullDocument** because the row is gone. For deletes, you must filter on `documentKey._id` or pre-image (`fullDocumentBeforeChange`, requires `changeStreamPreAndPostImages: true` on the collection — MongoDB 6.0+). Reference: MongoDB manual on change streams.

Mongo docs explicitly warn against `$match` on `fullDocument` combined with `updateLookup` under high-churn workloads: race between the update event and the post-image lookup causes `ResumeTokenNotFound` errors. Mitigation: use **pre/post-images** instead (server-side, atomic with the oplog entry) when high churn is expected.

### 2.2 Change streams vs polling
| Aspect | Change streams | Polling |
|---|---|---|
| Latency | Sub-second | `WithPollInterval` tick (project allows this as fallback) |
| Replica-set required | **Yes** | No — works on standalone |
| Resumability | Built-in via resume tokens | Requires caller-maintained `_id`/`updatedAt` cursor |
| Tenant filter | `$match` in pipeline (server-side) | `WHERE tenant_id = ?` in query (server-side) |
| Reconnect safety | Automatic via `StartAfter`/`ResumeAfter` | Caller must persist cursor |

The current project already supports both (`WithPollInterval` in `options.go` switches MongoDB to polling — explicitly for standalone instances without a replica set). For tenant-filtered events, change streams are strictly better — server applies the `$match`, network carries only relevant events.

### 2.3 Single-node replica set with testcontainers
`github.com/testcontainers/testcontainers-go/modules/mongodb` **v0.31.0+** exposes `mongodb.WithReplicaSet("rs0")`, which the project already pins at v0.41.0. Canonical pattern:

```go
ctr, err := mongodb.Run(ctx, "mongo:6", mongodb.WithReplicaSet("rs0"))
```

The module auto-initiates the replica set. Reference: `https://pkg.go.dev/github.com/testcontainers/testcontainers-go/modules/mongodb`. Confirmed applicable: check `internal/mongodb/mongodb_integration_test.go` in this repo — if it already uses `WithReplicaSet`, the tenant-filter tests simply extend that setup.

### 2.4 Resume tokens: `ResumeAfter` vs `StartAfter` vs `StartAtOperationTime`
| Option | Semantics | Use case |
|---|---|---|
| `ResumeAfter(token)` | Resume from token — fails if oplog has aged past that token | Normal reconnect after brief outage |
| `StartAfter(token)` | Like `ResumeAfter` but **safer after invalidation** (e.g., collection drop/rename); can start from an invalidate event | Resilient production use |
| `StartAtOperationTime(ts)` | Start from a cluster timestamp | Cold start / fresh subscription with lower bound; no prior token |

For reconnect-safe tenant filtering, **`StartAfter`** is the correct choice. Persist the resume token each time a notification is dispatched (or periodically — balance durability vs write amplification). The `$match` pipeline is re-applied server-side after resume, so tenant filter survives reconnects cleanly.

---

## 3. Go 1.25.9 idioms

Reference: `https://go.dev/doc/go1.25`.

### 3.1 `sync.Map` vs `map + sync.RWMutex`
Per official `sync` docs, `sync.Map` is **only** advantageous for:
- Write-once / read-many keys (caches that only grow).
- Disjoint key sets across goroutines.

Systemplane's tenant-scoped cache is write-through on `Set` (so keys with frequent value mutation) and reads from potentially many goroutines across shared keys — **textbook mismatch for `sync.Map`**. Stay with the existing `map + sync.RWMutex` pattern used by the current Client. Bonus: `RWMutex` preserves type safety (`map[key]typed`) and lets you hold the lock across multi-step read-modify-write safely.

Scale sanity check: "100s of tenants × 10s of keys" = low thousands of entries. A `map[compositeKey]Entry` with `sync.RWMutex` handles this trivially — contention only matters if writes are hot, which is not systemplane's workload.

Reference: `https://pkg.go.dev/sync#Map` (explicit guidance: "Most code should use a plain Go map instead").

### 3.2 Context-key type
Best practice (pre-dates Go 1.25, still canonical): **unexported struct type** as key, never `string`.

```go
type tenantKey struct{}
func WithTenant(ctx context.Context, t string) context.Context {
    return context.WithValue(ctx, tenantKey{}, t)
}
func TenantFromContext(ctx context.Context) (string, bool) {
    t, ok := ctx.Value(tenantKey{}).(string)
    return t, ok
}
```

Reasons: unexported type prevents accidental collision across packages; struct zero-value has no heap allocation; `go vet`'s `contextcheck` warns on `string` keys. Go 1.25 adds no new context helpers for this.

### 3.3 Sorted iteration: `slices.Sorted`, `maps.Keys`
Already available **since Go 1.23** (stable in 1.25.9): `maps.Keys(m)` returns an iterator (`iter.Seq[K]`), and `slices.Sorted(seq)` materializes a sorted slice. Canonical idiom:

```go
for _, k := range slices.Sorted(maps.Keys(m)) { ... }
```

Useful for a future `ListTenantsForKey(ns, key) []string` helper. Go 1.25 did not add further helpers here (per release notes). Reference: `https://go.dev/doc/go1.25` — the maps/slices packages have no new functions in 1.25.

### 3.4 Generics: `Get[T any](...)` vs typed accessors
The project already has untyped `Get` + typed accessors (`GetString`, `GetInt`, etc.). Given the **additive-only API constraint**, introducing `GetT[T any](c *Client, ns, key string) (T, bool)` as a **package-level generic helper** (methods cannot be generic in Go) is:
- **Non-breaking** — typed accessors remain.
- **Type-safe** — callers write `GetT[time.Duration](client, ns, key)`.
- **Slightly verbose at call sites** — package-level function, not a method.

Trade-off: the existing `GetDuration` etc. remain the ergonomic choice for known types. `GetT` fits custom/generic domain types without growing the accessor surface. Recommend **adding** `GetT[T any]` alongside existing methods — it composes with tenant-scoped variants (`GetTForTenant[T any]`) without explosion of typed×tenanted accessors.

---

## 4. pgx v5 JSONB

Reference: `https://pkg.go.dev/github.com/jackc/pgx/v5/pgtype`.

### 4.1 `json.RawMessage` vs `pgtype.JSONB`
pgx v5 ships a **`JSONBCodec`** (not a user-facing `pgtype.JSONB` concrete type — the codec is transparent). You scan directly into:
- A **typed struct** if the JSON shape is fixed: `conn.QueryRow(...).Scan(&MyStruct{})`.
- **`json.RawMessage`** for deferred parsing: `var raw json.RawMessage; row.Scan(&raw)`.
- A **custom type implementing `sql.Scanner` + `driver.Valuer`** for domain-specific encoding.

For systemplane's value field — arbitrary `string | int | bool | float64 | time.Duration` — **`json.RawMessage` is canonical**: store as JSONB, scan as raw bytes, `json.Unmarshal` into `any`, then type-assert. This is exactly the current pattern in `internal/postgres` and should carry forward for the tenant-scoped row.

### 4.2 Scan into `any`
pgx v5 does **not** auto-decode JSONB into `any` via a bare `interface{}` Scan target — you get `[]byte` representing the raw JSON. The idiomatic path is `json.RawMessage` + explicit `json.Unmarshal(raw, &target)`. This is deliberate — avoids surprises from pgx reinterpreting JSONB as `map[string]any` with type-lossy float64 coercions.

---

## 5. mongo-driver/v2 BSON

Reference: `https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/bson`.

### 5.1 Flexible value field
For a value column holding `string | int | bool | float64 | time.Duration`:
- **`interface{}` / `any`** — simplest, works, loses BSON-specific type info on round-trip.
- **`bson.RawValue`** — keeps type tag + raw bytes; defers decoding; best when you want lazy parsing.
- **Typed struct** — impossible for truly polymorphic values.

**Recommendation:** store the value under a field typed as `bson.RawValue` in the document (or wrap it in a `bson.M` envelope: `{"value": rawValue, "type": "duration"}` for self-describing payloads). `bson.RawValue.UnmarshalValue(&typedTarget)` decodes on demand. Avoids the classic `int32`-vs-`int64` ambiguity you get with bare `any`.

Duration specifically: BSON has no native duration — store as `int64` nanoseconds with an explicit `"type": "duration"` sibling, decode with `time.Duration(v)`. This matches the existing systemplane convention.

### 5.2 Schema evolution: adding `tenant_id` to existing docs
- **Field addition is trivially backward-compatible** — the Go driver ignores unknown fields in structs by default, and missing fields unmarshal as zero values.
- **No migration required** for pre-existing docs: `tenant_id: null` (or field absent) means "global/untenanted". `$match` filters with `{tenant_id: {$exists: false}}` or `{tenant_id: null}` catch legacy rows.
- **Index strategy:** add a compound index on `(namespace, key, tenant_id)` to keep lookup O(log n). Existing `(namespace, key)` unique constraint must be **replaced** with a partial unique: `unique on (namespace, key, tenant_id)` where global rows use `tenant_id: null`. MongoDB treats `null` and absent as distinct for uniqueness — pick one representation and enforce it at write time.
- **Existing change-stream subscribers** filtering on `fullDocument.ns`/`fullDocument.key` continue to work; new tenant filter composes as an additional `$match` stage.

---

## FRAMEWORK CONSTRAINTS

1. **pgx LISTEN cannot server-side filter by payload.** All tenant filtering for Postgres-backed notifications happens in Go after JSON-decoding the payload. The existing single-channel design is correct; do not proliferate `systemplane_changes_tenant_X` channels.
2. **Postgres NOTIFY payload limit is 8000 bytes, hard error on overflow.** `{ns, key, tenant_id}` stays safely under this. Any future expansion of the payload (e.g., carrying the value) risks hitting the ceiling — if needed, carry a PK and let the consumer re-read.
3. **MongoDB change streams require a replica set; `$match` on `fullDocument.tenant_id` requires `SetFullDocument(UpdateLookup)` for updates and is unavailable for deletes.** Use `documentKey._id` for delete filtering, or enable pre/post-images (MongoDB 6.0+) for complete before/after filtering. Test containers must use `mongodb.WithReplicaSet("rs0")`.
4. **Reconnect is the caller's responsibility and MUST trigger hydration.** Both backends lose events during disconnection — Postgres has no backlog; MongoDB resume tokens can expire if the oplog rolls past. On reconnect, re-read the full tenant-scoped state from the store before resuming subscriptions.
5. **Debounce keys must include `tenant_id`.** The existing 100 ms debouncer (`options.go WithDebounce`) must now coalesce per `(ns, key, tenant_id)` tuple, not `(ns, key)` — otherwise bursts of writes across tenants silently drop per-tenant echoes.

**Additive-API corollary:** generics-based helpers (`GetT[T any]`, `GetTForTenant[T any]`) may be added alongside the existing typed accessors without breaking v5 contracts — this is the cleanest way to extend the read surface for tenant-scoped access without doubling the method count.

---

## Sources

- pgxpool.Pool API: https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool
- pgx.Conn LISTEN / WaitForNotification: https://pkg.go.dev/github.com/jackc/pgx/v5
- PostgreSQL NOTIFY payload limit: https://www.postgresql.org/docs/current/sql-notify.html
- pgtype JSONB codec: https://pkg.go.dev/github.com/jackc/pgx/v5/pgtype
- mongo-driver/v2 change streams: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/mongo
- mongo-driver/v2 BSON types: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/bson
- MongoDB change-streams manual (fullDocument semantics): https://www.mongodb.com/docs/manual/changeStreams/
- testcontainers MongoDB module: https://pkg.go.dev/github.com/testcontainers/testcontainers-go/modules/mongodb
- Go 1.25 release notes: https://go.dev/doc/go1.25
- sync.Map guidance: https://pkg.go.dev/sync
- Known 8000-byte NOTIFY failure reports: https://github.com/hatchet-dev/hatchet/issues/3087, https://github.com/ansible/awx/issues/12999, https://github.com/graphprotocol/graph-node/issues/768
