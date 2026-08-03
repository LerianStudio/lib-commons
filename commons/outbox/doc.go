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
// This optional interface is backward compatible: repositories that do not
// implement it continue to produce one dispatch scope for every ListTenants
// entry, and DispatchOnceResult remains a tenant-scoped operation.
package outbox
