// Package outbox provides transactional outbox primitives.
//
// It includes an event model, repository contracts, a generic dispatcher with
// retry controls, and persistence adapters under the postgres and mongo
// subpackages.
//
// Repositories with one physical outbox database per tenant implement
// OutboxRepository and ListTenants as before. A repository that exposes several
// physical outbox databases for the same real tenant may additionally implement
// TenantDispatchScopeRepository. TenantDispatchScope.TenantID remains the only
// tenant identity visible to handlers and telemetry; PoolKey is opaque routing
// metadata used only by the repository. The dispatcher trims and deduplicates
// exact (TenantID, PoolKey) scopes before scanning them.
//
// Dispatch scopes with work, or work observed within ColdDispatchInterval,
// retain the normal DispatchInterval cadence. Empty scopes poll at the bounded
// cold interval (one minute by default), while tenant topology discovery still
// runs on every normal dispatcher tick. This avoids keeping idle tenant pools
// hot and bounds discovery latency for newly committed, retryable, or stuck
// rows. Scope removal also evicts its activity state.
//
// Retention is opt-in through WithRetentionPublished and needs a repository
// implementing the optional PublishedPurger capability (the postgres and mongo
// adapters do); NewDispatcher returns ErrOutboxRetentionUnsupported otherwise.
// When enabled, a dispatch scope is swept at most once per
// RetentionSweepInterval: one call to DeletePublishedBefore removes up to
// RetentionBatchSize PUBLISHED events older than the retention window, oldest
// first, sparing the types listed in RetentionKeepEventTypes, so a large
// backlog drains one batch per interval without a long transaction. PENDING,
// PROCESSING, FAILED and INVALID events are not deleted at any age: an INVALID
// event is the durable record that a fact was abandoned after the retry
// budget. A failed sweep is logged and does not affect dispatch.
//
// Pool-per-tenant and schema-per-tenant postgres repositories, and mongo
// repositories with a tenant database resolver, discover every known tenant, so
// each is swept in a pass that dispatches it. Column-per-tenant postgres and
// row-scoped mongo (tenant field, no database resolver) discover only tenants
// with PENDING, PROCESSING or FAILED rows, so they also implement
// PublishedTenantLister: once per RetentionSweepInterval the dispatcher lists
// every tenant holding a PUBLISHED event older than the retention window and
// sweeps each one, idle or not, every sweep scoped to its own tenant.
//
// The interval is kept per dispatcher instance, in memory: N replicas produce
// up to N batches per scope per interval. Deletes are idempotent, so replicas
// racing on one scope delete each row once. A scope's last sweep time is
// forgotten once it is older than the interval, never because the scope
// dropped out of one discovery pass.
//
// These optional interfaces and scheduling controls are backward compatible:
// repositories that do not implement TenantDispatchScopeRepository continue to
// produce one dispatch scope for every ListTenants entry, and
// DispatchOnceResult remains a tenant-scoped operation.
package outbox
