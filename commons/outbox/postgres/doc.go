// Package postgres provides PostgreSQL adapters for outbox repository contracts.
//
// # Tenancy strategies
//
// The adapter supports three mutually exclusive tenancy strategies. Choose one
// per deployment; the mode is derived from the injected dependencies, not
// sniffed from the database.
//
//   - pool-per-tenant (recommended): each tenant's outbox rows live in a
//     dedicated database resolved through a TenantPoolResolver. The pool is the
//     isolation boundary; there is no schema scan and no tenant column.
//   - schema-per-tenant (legacy): tenant rows live in per-tenant schemas of a
//     shared pool, selected via search_path. Migrations in migrations/.
//   - column-per-tenant: tenant rows share one table, isolated by a tenant
//     column WHERE filter. Migrations in migrations/column/.
//
// SchemaResolver enforces non-empty tenant context by default. Use
// WithAllowEmptyTenant only for explicit single-tenant/public-schema flows.
//
// # Pool-per-tenant
//
// In pool-per-tenant deployments the tenant's outbox rows live in its own
// database, following the tenant-manager "isolated" isolation model. The
// database pool itself is the isolation boundary, so there is nothing to scope
// inside the transaction: no schema scan, no search_path, no tenant column.
// NoopTenantResolver is used for in-transaction scoping precisely because it
// must never touch the transaction.
//
// The outbox_events table is NOT provisioned by this package. It is created by
// the consumer's per-tenant migration runner, applying the same DDL as the
// schema track inside each tenant database. This adapter only reads, writes,
// and marks rows; it never issues DDL.
//
// # Wiring
//
// Pool-per-tenant mode is wired through NewMultiTenantRepository with a
// ManagerPoolResolver backed by a tenant-manager Manager and Client:
//
//	// Enable the tenant-manager client circuit breaker so a Tenant Manager
//	// outage fails fast on enumeration instead of stalling every dispatch
//	// cycle. WithCircuitBreaker(threshold, timeout) trips after `threshold`
//	// consecutive service failures and probes again after `timeout`.
//	tmClient, err := tmclient.NewClient(
//		tmclient.WithBaseURL(tenantManagerURL),
//		tmclient.WithCircuitBreaker(5, 30*time.Second),
//	)
//	if err != nil {
//		return err
//	}
//
//	// rootClient resolves the platform default tenant's pool; defaultTenantID
//	// is the platform tenant, which lives in the root pool and is never looked
//	// up in Tenant Manager.
//	resolver, err := postgres.NewManagerPoolResolver(
//		tmManager,       // *tmpostgres.Manager: resolves per-tenant pools
//		tmClient,        // *tmclient.Client: enumerates active tenants
//		rootClient,      // resolverProvider: platform default tenant's pool
//		serviceName,     // service whose active tenants are enumerated
//		defaultTenantID, // platform tenant ID, routed to the root pool
//	)
//	if err != nil {
//		return err
//	}
//
//	repo, err := postgres.NewMultiTenantRepository(postgres.MultiTenantConfig{
//		Client:             rootClient,
//		PoolResolver:       resolver,
//		MultiTenantEnabled: true,
//	})
//	if err != nil {
//		return err
//	}
//
// Setting PoolResolver enables pool-per-tenant mode and installs
// NoopTenantResolver as the in-transaction scoping. MultiTenantEnabled is a
// fail-closed assertion: true with no PoolResolver fails construction with
// ErrMultiTenantMisconfigured rather than silently degrading to root-only
// dispatch.
//
// # Pool resolution (tiered, fail-closed)
//
// Every read/write/mark operation resolves the tenant's pool in three tiers:
//
//	tier-1: a pool pre-installed in the context (tmcore.GetPGContext) — used by
//	        request/write-path callers that already hold the tenant's pool.
//	tier-2: PoolForTenant, keyed by the tenant ID stamped on the context.
//	tier-3: ErrTenantPoolUnavailable — fail closed, never the root pool.
//
// Resolution never falls back to a shared root pool, so dispatch cannot cross
// tenant boundaries. An event read from a tenant's pool is marked published
// against the same pool, preserving at-least-once delivery.
//
// The default (platform) tenant is the one exception to Tenant Manager lookup:
// because it is not registered in Tenant Manager, ManagerPoolResolver routes it
// to the root pool directly via defaultTenantID.
//
// # Tenant enumeration (ListTenants precedence)
//
// The dispatcher enumerates dispatchable tenants and stamps each ID onto the
// context before invoking the repository. Precedence: an explicitly injected
// TenantDiscoverer always wins; only when none is set does the repository
// delegate enumeration to the pool resolver's ListTenants.
// ManagerPoolResolver.ListTenants returns the service's active tenants from
// Tenant Manager plus the platform default tenant (appended when absent, since
// Tenant Manager does not enumerate it).
//
// # Missing table: skip and log
//
// A tenant pool that is reachable but lacks the outbox table almost always
// means its per-tenant migration never ran. Rather than letting dispatch fail
// with 42P01, the repository probes table presence (via to_regclass, which
// yields a clean NULL for an absent table) and skips dispatch for that tenant,
// logging a WARN. The probe result is cached per tenant for a 60s TTL, so the
// warning fires at most once per TTL per tenant rather than on every cycle.
//
// # Tenant Manager unavailable
//
// When Tenant Manager is down, tenant enumeration fails and the dispatch cycle
// is skipped. PENDING rows are left untouched and are dispatched on a later
// cycle once Tenant Manager recovers: delivery is delayed, not lost. Enabling
// the tenant-manager client circuit breaker (tmclient.WithCircuitBreaker) is
// recommended so a sustained outage fails fast instead of stalling each cycle
// on per-tenant timeouts.
//
// # Migrating off schema-per-tenant
//
// Schema-per-tenant is legacy: still maintained, but not recommended for new
// adopters. New deployments should prefer pool-per-tenant, whose isolation is
// enforced by the database pool rather than by search_path discipline. The
// migration seam is NewMultiTenantRepository: a schema-track deployment wires a
// SchemaResolver as both TenantResolver and TenantDiscoverer, while a
// pool-track deployment sets PoolResolver instead. The outbox_events DDL is
// identical across both tracks, so the table definition carries over unchanged;
// only the wiring and the location of each tenant's rows differ.
package postgres
