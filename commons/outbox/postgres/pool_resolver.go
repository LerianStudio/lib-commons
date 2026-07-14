package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
)

// ErrTenantPoolUnavailable is returned when a per-tenant pool cannot be
// resolved and no context-installed pool is present. Pool-per-tenant dispatch
// fails closed on this error rather than falling back to a shared root pool,
// which would cross tenant boundaries.
var ErrTenantPoolUnavailable = errors.New("tenant database pool unavailable")

// NoopTenantResolver is the TenantResolver for pool-per-tenant deployments.
//
// In pool-per-tenant mode the database pool itself is the isolation boundary:
// each tenant's outbox rows live in a dedicated database reached via a
// per-tenant pool. There is therefore nothing to scope inside the
// transaction — no search_path to set, no tenant column to filter — so
// ApplyTenant is a no-op and must never touch the transaction.
//
// It is structurally ColumnResolver without the WHERE filter, but a distinct
// type: it reports RequiresTenant() == true (so the dispatcher's
// empty-enumeration guard skips root dispatch) and intentionally does NOT
// implement TenantColumn(), keeping the repository's tenantColumn empty.
type NoopTenantResolver struct{}

// ApplyTenant is a no-op. The pool is the isolation; the transaction must not
// be scoped further.
func (NoopTenantResolver) ApplyTenant(_ context.Context, _ *sql.Tx, _ string) error {
	return nil
}

// RequiresTenant reports true so dispatch never falls back to a root pool when
// tenant enumeration yields nothing.
func (NoopTenantResolver) RequiresTenant() bool {
	return true
}

// newTenantPoolLookup builds the primaryDBLookup installed by
// WithTenantPoolResolver. Resolution order:
//
//	tier-1: a pool pre-installed in the context (tmcore.GetPGContext) — used by
//	        request/write-path callers that already hold the tenant's pool.
//	tier-2: resolver.PoolForTenant using the ctx-stamped tenant ID.
//	tier-3: ErrTenantPoolUnavailable — fail closed, never the root pool.
func newTenantPoolLookup(resolver outbox.TenantPoolResolver) func(context.Context) (*sql.DB, error) {
	return func(ctx context.Context) (*sql.DB, error) {
		if ctx == nil {
			ctx = context.Background()
		}

		// tier-1: context-installed pool, bridged via PrimaryDBs()[0] with the
		// same nil-guards as resolvePrimaryDB. A present-but-empty resolver
		// falls through to tier-2 rather than failing.
		if resolved := tmcore.GetPGContext(ctx); resolved != nil {
			primaryDBs := resolved.PrimaryDBs()
			if len(primaryDBs) > 0 && primaryDBs[0] != nil {
				return primaryDBs[0], nil
			}
		}

		// tier-2: resolver keyed by the ctx-stamped tenant ID.
		tenantID, ok := outbox.TenantIDFromContext(ctx)
		if ok && tenantID != "" && resolver != nil {
			db, err := resolver.PoolForTenant(ctx, tenantID)
			if err != nil {
				return nil, err
			}

			if db != nil {
				return db, nil
			}
		}

		// tier-3: fail closed.
		return nil, ErrTenantPoolUnavailable
	}
}

// tenantOutboxTableMissing reports whether pool mode is active and the current
// tenant's outbox table is absent, so the caller can skip dispatch instead of
// issuing a query that would fail with 42P01. When no guard is configured
// (single-tenant / schema / column mode) it always returns false so behavior is
// unchanged. Probe errors are surfaced to the caller (skip-and-log upstream).
func (repo *Repository) tenantOutboxTableMissing(ctx context.Context) (bool, error) {
	if repo == nil || repo.tablePresence == nil {
		return false, nil
	}

	tenantID, ok := outbox.TenantIDFromContext(ctx)
	if !ok || tenantID == "" {
		// Without a tenant the lookup would fail closed anyway; let the normal
		// path surface ErrTenantIDRequired rather than masking it here.
		return false, nil
	}

	present, err := repo.tablePresence.present(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("outbox table presence check: %w", err)
	}

	return !present, nil
}

// WithTenantPoolResolver installs a pool-per-tenant lookup on the repository,
// exposing the otherwise-dormant per-tenant primary DB lookup.
//
// When set, every read/write/mark operation routes through the resolver: the
// dispatcher stamps a tenant ID onto the context, and the repository resolves
// that tenant's dedicated pool (preferring a pool already installed in the
// context). This preserves at-least-once delivery — an event read from a
// tenant's pool is marked published against the same pool.
//
// ListTenants precedence: when a pool resolver is configured AND no explicit
// TenantDiscoverer was injected, the repository delegates ListTenants to
// resolver.ListTenants. An explicitly injected discoverer always wins.
//
// This option also sets requireTenant so a missing/empty tenant context fails
// closed (ErrTenantPoolUnavailable / ErrTenantIDRequired) rather than reaching
// a shared root pool.
func WithTenantPoolResolver(resolver outbox.TenantPoolResolver) Option {
	return func(repo *Repository) {
		if resolver == nil {
			return
		}

		lookup := newTenantPoolLookup(resolver)

		repo.poolResolver = resolver
		repo.primaryDBLookup = lookup
		repo.requireTenant = true
		repo.tablePresence = newTablePresenceGuard(repo.newTablePresenceProbe(lookup), defaultTablePresenceTTL)
	}
}

// newTablePresenceProbe builds the table-presence probe used in pool mode. It
// resolves the tenant's pool via the supplied lookup and checks whether the
// configured outbox table exists, using to_regclass so a missing table yields
// a clean NULL rather than a 42P01 error.
func (repo *Repository) newTablePresenceProbe(
	lookup func(context.Context) (*sql.DB, error),
) tablePresenceProbe {
	return func(ctx context.Context, tenantID string) (bool, error) {
		db, err := lookup(ctx)
		if err != nil {
			return false, err
		}

		if db == nil {
			return false, ErrNoPrimaryDB
		}

		var regclass sql.NullString

		// to_regclass resolves an identifier path safely; the table name is
		// validated at construction. Parameterized as text.
		if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)", repo.tableName).Scan(&regclass); err != nil {
			return false, fmt.Errorf("probing outbox table presence: %w", err)
		}

		if !regclass.Valid {
			// Operational signal: a tenant pool reachable but missing the outbox
			// table almost always means its migration never ran. Logged here (at
			// the probe, not the ListPending gate) because the presence guard
			// caches per tenant for the TTL, so this warns at most once per TTL
			// per tenant rather than on every dispatch cycle.
			if repo.logger != nil {
				repo.logger.Log(
					ctx,
					libLog.LevelWarn,
					"outbox table absent in tenant database; skipping dispatch (migration likely not applied)",
					libLog.String("tenant.id", tenantID),
					libLog.String("outbox.table", repo.tableName),
				)
			}
		}

		return regclass.Valid, nil
	}
}
