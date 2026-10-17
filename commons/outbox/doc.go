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
// The fallback scope, dispatched when discovery finds no tenant (the default
// scope, or the tenant carried by the run context), backs off the same way.
// While it is cold, a tick returns before tenant discovery and opens no span,
// so an idle single-scope dispatcher exports no trace. The first event after an
// idle period, or a tenant that discovery would newly list, can therefore wait
// up to ColdDispatchInterval, rounded up to the next DispatchInterval tick;
// WithColdDispatchInterval lowers that bound.
//
// A failed publish ends one of two ways. With a RetryClassifier configured, an error it calls
// non-retryable, such as a malformed event no retry can fix, marks the event INVALID at once;
// without one every error is retryable. A retryable error, such as an unavailable broker,
// marks it FAILED: it is retried no sooner than RetryWindow after each failure until
// MaxDispatchAttempts is spent, then marked INVALID. WithRetryHorizon states that budget as a
// minimum duration (seven days, say) instead of a count. A FAILED event whose attempts already
// reach a budget since lowered (a shorter horizon, a longer RetryWindow under one, a rollback)
// is never retried again nor marked INVALID: it stays FAILED until requeued.
//
// Dispatcher.Requeue puts one INVALID or FAILED event back in the queue, attempts reset, for
// the dispatcher itself to publish again under the same id, event type and payload, so there
// is never a second publisher and consumers dedupe on the id; it needs a repository
// implementing Requeuer. It does not undo what OnInvalid did: a service whose OnInvalid moved
// its own domain state restores it before requeueing, or keeps those types out of the redrive.
// The outbox.queue.oldest_age gauge reports, on an OldestWaitingReader repository, the age of
// the oldest PENDING, PROCESSING or FAILED event in the claim scope, the largest across the
// dispatch scopes sharing a series: the alarm for a fact left waiting. The postgres and mongo
// adapters implement both, and TableNamer: every dispatcher metric then carries outbox.table,
// so two dispatchers in one process (one per outbox table) never overwrite each other's gauges.
//
// Retention is opt-in through WithRetentionPublished and needs a repository
// implementing the optional PublishedPurger capability (the postgres and mongo
// adapters do); NewDispatcher returns ErrOutboxRetentionUnsupported otherwise.
// When enabled, a dispatch scope is swept at most once per
// RetentionSweepInterval: one call to DeletePublishedBefore removes up to
// RetentionBatchSize PUBLISHED events published longer ago than the retention
// window, oldest first, sparing the types listed in RetentionKeepEventTypes, so
// a large backlog drains one batch per interval without a long transaction.
// PENDING, PROCESSING and FAILED events are not deleted at any age. INVALID
// events, the record of a fact abandoned after the retry budget, are kept
// forever unless WithRetentionInvalid(window) is set (postgres only, with or
// without the published retention): the sweep then also deletes them, same
// batch size and kept types, once they have been INVALID longer than window.
// Both ages read updated_at, stamped by the move into the terminal status. A
// failed sweep is logged and does not affect dispatch.
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
