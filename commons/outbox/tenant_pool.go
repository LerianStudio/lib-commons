package outbox

import (
	"context"
	"database/sql"
)

// TenantPoolResolver resolves per-tenant database pools for pool-per-tenant
// deployments, where each tenant's outbox rows live in a dedicated database
// (tenant-manager "isolated" isolation mode) rather than in a shared root pool
// scoped by schema or tenant column.
//
// It returns *sql.DB — the concrete handle a backend needs to begin a
// transaction — so the generic outbox package stays free of the
// bxcodec/dbresolver dependency. Backends bridge their resolver type to
// *sql.DB at the adapter boundary.
//
// TenantPoolResolver sits alongside TenantResolver and TenantDiscoverer; it
// replaces neither. TenantResolver handles in-pool isolation (search_path,
// WHERE filters); TenantPoolResolver handles which pool to reach at all.
//
// Debt: TenantResolver.ApplyTenant hard-types *sql.Tx, which is a
// backend-neutrality smell shared by this package. Reworking both toward a
// backend-neutral transaction abstraction is deferred to v6; until then,
// TenantPoolResolver mirrors the same *sql.DB pragmatism.
type TenantPoolResolver interface {
	// PoolForTenant returns the database pool that owns the given tenant's
	// outbox rows. Implementations must fail closed (return a non-nil error)
	// rather than returning a shared/root pool when the tenant cannot be
	// resolved, so dispatch never crosses tenant boundaries.
	PoolForTenant(ctx context.Context, tenantID string) (*sql.DB, error)

	// ListTenants enumerates the tenant identifiers eligible for dispatch.
	// The dispatcher stamps each returned ID onto the context before invoking
	// the repository, which then routes via PoolForTenant.
	ListTenants(ctx context.Context) ([]string, error)
}
