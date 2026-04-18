# Research — Tenant-Scoped Systemplane: Industry Best Practices

**Scope:** survey how mature multi-tenant config/feature-flag systems model per-tenant overrides, propagate changes, expose evaluation APIs, enforce validation, and structure subscribers — so the design for `commons/systemplane` per-tenant overrides is informed by prior art.

---

## Section 1 — Storage architecture

The industry is split between **targeting-rule trees** (evaluated inside the SDK against an ambient context) and **table-column override rows** (pre-resolved per subject). Understanding which one a project chose — and why — matters because our `GetForTenant` semantic is closer to the latter.

- **LaunchDarkly** — every flag document carries an `environments` map; inside each environment the flag has `targets[]` (list-based, one entry per variation with `contextKind`, `variationId`, `values[]`), `contextTargets[]` (same shape for non-user kinds), and `rules[]` (richer, with clauses that include segment-match operators). Overrides are thus not a separate table — they are arrays embedded in the flag document, keyed by `contextKind` ([JSON targeting](https://launchdarkly.com/docs/home/flags/json-targeting); [Targeting rules](https://launchdarkly.com/docs/home/flags/target-rules)). Segments are a reusable, separately-persisted list-or-rule object referenced from flag rules ([Segments](https://launchdarkly.com/docs/home/flags/segments)).
- **Unleash** — flags are a single logical entity with per-environment *activation strategies*; each strategy carries *constraints* (a list of AND-ed predicates over context attributes). There is no dedicated "override row" — narrowing happens via constraint lists, and "project" is a grouping/ACL boundary, not an override axis ([Core concepts](https://docs.getunleash.io/understanding-unleash/the-anatomy-of-unleash); [Managing constraints](https://docs.getunleash.io/understanding-unleash/managing-constraints); [Organize by env/project](https://docs.getunleash.io/guides/organize-feature-flags)).
- **ConfigCat** — targeting rules live on the flag; *segments* are reusable named user-condition groups that a rule can reference with `IS IN SEGMENT` ([Segment condition](https://configcat.com/docs/targeting/targeting-rule/segment-condition/); [Segments](https://configcat.com/docs/advanced/segments/); [Targeting overview](https://configcat.com/docs/targeting/targeting-overview/)). Rules within a flag are OR-ordered (first match wins); conditions within a rule are AND-ed ([Evaluation](https://configcat.com/docs/targeting/feature-flag-evaluation/)).
- **Flipt** — separates *segments* (named groups with constraints), *rules* (`IF segment THEN variant`, ordered by `rank 1..N`, first match wins), and *rollouts* (threshold/segment rollouts for boolean flags). Constraints are stored as strings and coerced per-type at evaluation time; *namespaces* provide hard isolation — a flag/segment in one namespace is invisible from another ([Concepts](https://docs.flipt.io/v1/concepts); [Boolean rollouts](https://blog.flipt.io/boolean-flags-and-rollouts)).
- **GrowthBook** — one feature per key with a default value, plus **per-environment** rule stacks (Forced Value, Percentage Rollout, Safe Rollout, Experiment). Each environment has its own SDK connection and its own rule list ([Environments](https://docs.growthbook.io/features/environments); [Rules](https://docs.growthbook.io/features/rules)). Per-environment rules are the closest industry analogue to our proposed "tenant as an override axis."

**Takeaway for our design.** None of these vendors models "tenant" as a *column on an override row*; all of them model narrower audiences as either (a) a rule inside the flag document (LD, Unleash, ConfigCat, Flipt) or (b) a separate rule-stack keyed by environment (GrowthBook). Our proposal to add a dedicated tenant override row is closer to the Postgres multi-tenant-schema guidance — denormalize `tenant_id` on every override table, with a composite key `(namespace, key, tenant_id)` ([Crunchy Data — multi-tenant schema](https://www.crunchydata.com/blog/designing-your-postgres-database-for-multi-tenancy); [Bytebase — multi-tenant patterns](https://www.bytebase.com/blog/multi-tenant-database-architecture-patterns-explained/)). That is an architecturally defensible choice *because* we want any tenant to override without pre-declaration — a rule-tree model would force a catalog write per tenant.

---

## Section 2 — Change propagation

- **LaunchDarkly** — a permanent **SSE** streaming connection from the SDK to LD's edge (Fastly-backed Flag Delivery Network); initial payload plus delta events, sub-200ms global propagation ([Platform architecture](https://launchdarkly.com/features/platform-architecture/); [Evolution from polling to streaming](https://launchdarkly.com/blog/launchdarklys-evolution-from-polling-to-streaming/)). Flags are served from SDK memory (sub-1ms).
- **Unleash** — **polling** by default (interval configurable, "a few seconds"); **Enterprise Edge** adds a push channel between Edge and the Unleash server ([Architecture](https://docs.getunleash.io/get-started/unleash-overview); [Unleash Edge](https://docs.getunleash.io/unleash-edge)). Unleash has actively argued streaming is marketing theatre for most users ([Streaming flags is a paper tiger](https://www.getunleash.io/blog/streaming-flags-is-a-paper-tiger)).
- **ConfigCat** — SDKs store downloaded config in a local cache; propagation is polling with a configurable refresh interval, with a pluggable `IConfigCatCache` for external stores ([.NET SDK](https://configcat.com/docs/sdk-reference/dotnet/)).

**Our LISTEN/NOTIFY + change-stream setup is closer in latency characteristics to LaunchDarkly's SSE than to Unleash's polling** — Postgres `NOTIFY` wakes waiting `LISTEN` connections within a single round-trip, and MongoDB change streams are continuous cursors on the oplog. Both give sub-second propagation to every process that holds a subscription, which is what OnChange subscribers expect. Importantly, neither mechanism *natively* fans out by tenant — the current design debounces and re-reads, so a tenant write will naturally replay through the same code path as a global write, but subscribers will need to learn the distinction (see Section 5).

---

## Section 3 — API shape for per-tenant reads

The dominant industry pattern is the **ambient evaluation context**, not an explicit parameter. OpenFeature's spec is explicit: *"The evaluation context structure MUST define an optional `targeting key` field of type string, identifying the subject of the flag evaluation"* ([OpenFeature — Evaluation context](https://openfeature.dev/specification/sections/evaluation-context/)). Context is hierarchically merged (API → transaction → client → invocation → hooks) and custom fields are first-class. LaunchDarkly's SDK takes the same route — you build a `Context` object (possibly multi-kind: user + tenant + device) and pass it to `variation(...)` ([Context configuration](https://launchdarkly.com/docs/sdk/features/context-config)).

**Tradeoffs:**
- *Context object (their approach):* composable, transport-friendly across async boundaries, supports multi-tenant evaluation (e.g. "user X on behalf of tenant Y"), and is the only workable shape once you add org/device/region axes. Cost: more ceremony, tenant identity can go missing silently.
- *Explicit parameter (our proposal):* simple, hard to misuse (you *must* pass `tenantID`), ergonomic for Go's "pass context explicitly" culture. Cost: doesn't scale past a second axis — if we later add region/env, signature churn is painful.

The pragmatic middle ground, adopted by OpenFeature's dynamic-context paradigm, is to let tenant identity ride in `context.Context` via a typed helper (`systemplane.WithTenant(ctx, "t-123")`), with `GetForTenant(tenantID, ns, key)` as an explicit escape hatch. That keeps Go idioms intact while leaving room for future axes.

---

## Section 4 — Validator application to overrides

This is the area with the **least explicit prior art** — most vendors conflate "validator" with "schema" (e.g. a boolean flag can only be true/false) and enforce via the flag's declared `variations` list. LaunchDarkly constrains overrides to pre-declared `variations` ([JSON targeting](https://launchdarkly.com/docs/home/flags/json-targeting)); ConfigCat enforces setting-type when editing an override ([Targeting rules](https://configcat.com/docs/targeting/targeting-rule/targeting-rule-overview/)); Flipt flags have a declared type and variants. None let tenant admins freely inject an unchecked value.

**Universal lesson:** validation is a property of the *key*, not the *path* to set the key. Our `Register` accepts a `validator` option — `SetForTenant` must run *the same* validator, and `RegisterTenantScoped` must reject writes for unknown keys unless the caller opts into a permissive mode. Skipping validation on the tenant path is the canonical "how a tenant admin nukes production" incident pattern ([Feature flag anti-patterns](https://shahbhat.medium.com/feature-flag-anti-paterns-learnings-from-outages-e1b805f23725)).

---

## Section 5 — Subscriber semantics

The OpenFeature and LaunchDarkly SDKs fire a **single callback with evaluation-context payload**; subscribers filter themselves. Unleash's SDK emits `"changed"` events at the flag level without tenant specificity (consumers re-evaluate as needed). ConfigCat emits `onConfigChanged` on the whole config snapshot.

Our proposal splits `OnChange` (global) from `OnTenantChange` (tenant-writes-only). The **ergonomic winner depends on subscriber count**:
- Few subscribers, each cares about one axis → split callbacks are clearer.
- Many subscribers, cross-cutting concerns → a single callback with a `ChangeEvent{Scope: Global|Tenant, TenantID: ...}` payload avoids callback-table explosion.

Given lib-commons is a library (not a SaaS backend), subscriber count is bounded by consumer packages. **Splitting is defensible but the tenant callback should carry `tenantID` in its signature** — `fn(ns, key, tenantID, newValue)` — so subscribers that care about "which tenant" don't need a side channel.

---

## Section 6 — Anti-patterns to avoid

1. **Unbounded override memory growth.** PostHog's Feb-2026 incident traced to a cache worker that "loads all data into memory at once — flag definitions, cohorts, serialized representations, and the final JSON payload." An outlier tenant blew past the 8GB worker cap, cascading to a 116k task backlog ([PostHog post-mortem](https://posthog.com/handbook/company/post-mortems/2026-02-06-feature-flags-cache-degradation)). **Lesson:** bound the per-tenant cache; consider lazy load + LRU eviction rather than eager hydration of every tenant's overrides at `Start()`.
2. **Validator bypass on the tenant path.** See Section 4. Every write path must go through the same validator — if `Register` has a validator and `SetForTenant` doesn't run it, tenant admins can poison the cache ([Feature flag anti-patterns — Learnings from outages](https://shahbhat.medium.com/feature-flag-anti-paterns-learnings-from-outages-e1b805f23725)).
3. **Cache-stampede on wildcard invalidation.** Facebook's 2010 config outage: a bad config update invalidated caches, every client queried the backend simultaneously, hundreds of thousands of QPS hit the DB ([Shahzad Bhatti — Feature flag anti-patterns](https://shahbhat.medium.com/feature-flag-anti-paterns-learnings-from-outages-e1b805f23725)). **Lesson:** debounce (we already do, 100ms) + single-flight re-read per (ns, key) on NOTIFY, never "invalidate all and refetch."
4. **Active invalidation via broadcast.** [Maurits van der Schee](https://tqdev.com/2016-active-cache-invalidation-anti-pattern) argues active invalidation is inherently racy under partition; prefer write-through (we already do in `Set`) plus changefeed *echo* rather than broadcast-then-delete.
5. **100% flip on tenant rollout.** Google's 2025 quota incident shows the danger of propagating a config change immediately globally. Even for tenant-scoped config, if a default is changed globally, every tenant without an override adopts it instantly — consider staged rollout helpers or tenant-pinning for critical defaults ([Shahzad Bhatti — Feature flag anti-patterns](https://shahbhat.medium.com/feature-flag-anti-paterns-learnings-from-outages-e1b805f23725)).
6. **Unbounded subscriber fanout.** The same PostHog incident notes OOM visibility without queue-backlog visibility delayed RCA by days. If each tenant write fans out to N `OnTenantChange` callbacks, bound the callback set and expose a registry length metric.

---

## RECOMMENDATIONS FOR OUR DESIGN

1. **Composite primary key `(namespace, key, tenant_id)` with `tenant_id = '_global'` sentinel**, not nullable — keeps `NULL` semantics unambiguous, lets the same table serve global + tenant rows, and aligns with Postgres multi-tenant guidance (denormalize tenant_id everywhere; partition-friendly later). Route both writes through one SQL path with a `tenant_id` parameter to eliminate dual-path validator drift.
2. **Run the registered validator on `SetForTenant` unconditionally, even for keys registered via `RegisterTenantScoped`.** If a key has no validator, skip; otherwise, validation is a key-level invariant, not a path-level one. Reject `SetForTenant` on unknown keys with `ErrUnknownKey` (same sentinel as `Set`).
3. **Lazy-load tenant overrides, LRU-bound the cache per (namespace, key).** Do not hydrate all tenants' overrides at `Start()` — hydrate globals, then lazy-pull on first `GetForTenant`. Cap the tenant-override LRU (e.g. 10k entries) and emit a metric when eviction kicks in. Directly addresses the PostHog failure mode.
4. **Keep `OnChange` global-only; add `OnTenantChange(ns, key, fn)` with signature `fn(ns, key, tenantID string, newValue any)`.** Carry tenant identity in the callback — do not force subscribers to re-lookup. Fire serially under the same panic-recovery shield as `OnChange` (`commons/runtime.RecoverAndLog`). Expose a registry-length metric to detect callback leaks.
5. **Expose `GetForTenant(ctx, ns, key)` that reads tenant from `context.Context` (via `systemplane.WithTenant`) AND keep `GetForTenantID(tenantID, ns, key)` as the explicit escape hatch.** Gives us OpenFeature's ergonomics for the common path (one arg: `ctx`) while preserving Go's "no magic" ethos for tests and admin tooling. The typed context helper is cheap to add now and expensive to retrofit later when region/env appear.
