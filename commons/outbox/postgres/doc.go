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
// Every read/write/mark operation resolves the tenant's pool in four tiers:
//
//	tier-1: an opaque dispatcher scope routes through ModulePoolResolver when
//	        module-aware topology is configured.
//	tier-2: a pool pre-installed in the context (tmcore.GetPGContext) — used by
//	        request/write-path callers that already hold the tenant's pool.
//	tier-3: PoolForTenant, keyed by the tenant ID stamped on the context.
//	tier-4: ErrTenantPoolUnavailable — fail closed, never the root pool.
//
// Resolution never falls back to a shared root pool, so dispatch cannot cross
// tenant boundaries. An event read from a tenant's pool is marked published
// against the same pool, preserving at-least-once delivery.
//
// The default (platform) tenant is the one exception to Tenant Manager lookup:
// because it is not registered in Tenant Manager, ManagerPoolResolver routes it
// to the root pool directly via defaultTenantID.
//
// # Module-aware pool topology
//
// Services with a generic database plus named module databases compose their
// existing pool resolvers with NewModulePoolResolver. The constructor accepts a
// generic TenantPoolResolver, the platform default tenant ID, a
// TenantConfigLoader, and ordered ModulePool bindings. The generic resolver
// remains the authoritative tenant roster and the backward-compatible
// PoolForTenant path.
//
//	moduleResolver, err := postgres.NewModulePoolResolver(
//		genericResolver,
//		defaultTenantID,
//		func(ctx context.Context, tenantID string) (*tmcore.TenantConfig, error) {
//			return tmClient.GetTenantConfig(ctx, tenantID, serviceName)
//		},
//		postgres.ModulePool{Name: "consignado", Resolver: consignadoResolver},
//	)
//	if err != nil {
//		return err
//	}
//
//	repo, err := postgres.NewMultiTenantRepository(postgres.MultiTenantConfig{
//		Client:             rootClient,
//		PoolResolver:       moduleResolver,
//		MultiTenantEnabled: true,
//	})
//	if err != nil {
//		return err
//	}
//
// Each ModulePool.Name must exactly match the tenant-manager Databases map key.
// Its Resolver must resolve that module's pool for the real tenant ID. The
// generic resolver remains responsible for ListTenants and for writes that use
// the legacy TenantPoolResolver path.
//
// ListTenantDispatchScopes keeps TenantID equal to the real tenant ID and uses
// PoolKey only as opaque routing metadata. One tenant may therefore produce
// several dispatch scopes. Generic and module resources with the same canonical
// host, port, database, and schema produce one scope, so one physical outbox is
// never scanned twice. Empty schema and public schema are equivalent, as are an
// unset port and 5432. Database and schema names compare case-sensitively
// because quoted mixed-case PostgreSQL identifiers denote distinct objects.
//
// The platform default tenant never reaches the topology loader. ListTenants
// appends it to the roster precisely because Tenant Manager does not enumerate
// it, so looking it up is a guaranteed not-found answer and one WARN per
// refresh. It always resolves to exactly one generic scope. A non-default
// tenant with no last-known-good snapshot is skipped in isolation; healthy
// tenants continue through the refresh.
//
// Concurrent topology refreshes are monotonic: a refresh that started earlier
// cannot replace a newer snapshot when it completes later. EvictTenant removes
// every cached scope for one real tenant immediately, so new resolutions fail
// closed, and prevents already-running older refreshes from restoring those
// scopes. A pool resolution that began before eviction may still complete.
//
// Eviction is an explicit lifecycle obligation. The resolver does not subscribe
// to tenant events itself. The caller that accepts a tenant removal, suspension,
// or database-topology invalidation event must call EvictTenant(tenantID) before
// tearing down the underlying generic and module pools. If the tenant becomes
// active again, a later successful ListTenantDispatchScopes refresh repopulates
// its current topology.
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
// logging a WARN. The probe result is cached per tenant dispatch scope for a
// 60s TTL using the exact (real tenant ID, PoolKey) scope, so a missing module
// table cannot contaminate the generic pool's presence result (or vice versa),
// and the warning fires at most once per TTL per scope.
//
// # Backward compatibility
//
// ModulePoolResolver still implements outbox.TenantPoolResolver. ListTenants and
// PoolForTenant delegate to the generic resolver, so request/write paths and
// existing callers remain unchanged. A repository wired directly with a legacy
// ManagerPoolResolver still dispatches one pool per tenant. Only the dispatcher's
// optional TenantDispatchScopeRepository path observes named module scopes.
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
