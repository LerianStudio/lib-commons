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
// When enabled, each
// dispatch scope is swept at most once per RetentionSweepInterval: one call to
// DeletePublishedBefore removes up to RetentionBatchSize PUBLISHED events
// older than the retention window, oldest first, sparing the types listed in
// RetentionKeepEventTypes, so a large backlog drains one batch per interval
// without a long transaction. PENDING, PROCESSING, FAILED and INVALID events
// are never deleted: an INVALID event is the durable record that a fact was
// abandoned after the retry budget. A failed sweep is logged and does not
// affect dispatch.
//
// These optional interfaces and scheduling controls are backward compatible:
// repositories that do not implement TenantDispatchScopeRepository continue to
// produce one dispatch scope for every ListTenants entry, and
// DispatchOnceResult remains a tenant-scoped operation.
package outbox
