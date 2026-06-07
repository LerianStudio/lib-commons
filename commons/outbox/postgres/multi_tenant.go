package postgres

import (
	"context"
	"errors"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
)

// ErrMultiTenantMisconfigured is returned when MultiTenantConfig requests
// multi-tenant mode (MultiTenantEnabled) but provides no pool resolver to back
// it. This is a fail-closed guard: the repository never silently degrades to
// root-only dispatch when multi-tenancy was explicitly requested.
var ErrMultiTenantMisconfigured = errors.New(
	"multi-tenant enabled but no pool resolver provided",
)

// MultiTenantConfig is the single wiring entry point for the Postgres outbox
// repository's tenancy mode. The mode is derived from the injected
// dependencies rather than sniffed from the database:
//
//   - PoolResolver set  -> pool-per-tenant mode (NoopTenantResolver isolation,
//     per-tenant pool routing, pool-resolver tenant enumeration unless an
//     explicit TenantDiscoverer is also provided).
//   - PoolResolver nil  -> single-tenant root-pool behavior, byte-identical to
//     NewRepository with the supplied resolver/discoverer.
//
// Schema-per-tenant remains an explicit opt-in: wire a SchemaResolver as both
// TenantResolver and TenantDiscoverer through this config (or via NewRepository
// directly); this constructor performs no schema topology detection.
type MultiTenantConfig struct {
	// Client is the root PostgreSQL client. Required.
	Client *libPostgres.Client

	// PoolResolver, when set, enables pool-per-tenant mode.
	PoolResolver outbox.TenantPoolResolver

	// TenantResolver overrides the in-transaction tenant scoping. Optional in
	// pool mode (defaults to NoopTenantResolver). In single-tenant mode it is
	// required (matching NewRepository).
	TenantResolver outbox.TenantResolver

	// TenantDiscoverer overrides tenant enumeration. When set in pool mode it
	// takes precedence over PoolResolver.ListTenants. Required in single-tenant
	// mode.
	TenantDiscoverer outbox.TenantDiscoverer

	// MultiTenantEnabled asserts that multi-tenant dispatch is intended. When
	// true with no PoolResolver, construction fails (ErrMultiTenantMisconfigured)
	// instead of silently running root-only.
	MultiTenantEnabled bool

	// Options are passed through to the underlying repository.
	Options []Option
}

// NewMultiTenantRepository builds a Postgres outbox repository whose tenancy
// mode is derived from cfg. See MultiTenantConfig for the mode matrix.
func NewMultiTenantRepository(cfg MultiTenantConfig) (*Repository, error) {
	if cfg.Client == nil {
		return nil, ErrConnectionRequired
	}

	poolMode := cfg.PoolResolver != nil

	// Fail-closed mismatch guard: multi-tenant requested without a resolver.
	if cfg.MultiTenantEnabled && !poolMode {
		return nil, ErrMultiTenantMisconfigured
	}

	if !poolMode {
		// Single-tenant / explicitly-resolved mode: byte-identical to NewRepository.
		return NewRepository(cfg.Client, cfg.TenantResolver, cfg.TenantDiscoverer, cfg.Options...)
	}

	// Pool-per-tenant mode.
	tenantResolver := cfg.TenantResolver
	if nilcheck.Interface(tenantResolver) {
		tenantResolver = NoopTenantResolver{}
	}

	explicitDiscoverer := !nilcheck.Interface(cfg.TenantDiscoverer)

	discoverer := cfg.TenantDiscoverer
	if !explicitDiscoverer {
		// The discoverer field must be non-nil for initialized(); ListTenants
		// delegates to the pool resolver when no explicit discoverer was set.
		discoverer = poolDiscovererShim{}
	}

	opts := make([]Option, 0, len(cfg.Options)+1)
	opts = append(opts, cfg.Options...)
	opts = append(opts, WithTenantPoolResolver(cfg.PoolResolver))

	repo, err := NewRepository(cfg.Client, tenantResolver, discoverer, opts...)
	if err != nil {
		return nil, err
	}

	repo.explicitDiscoverer = explicitDiscoverer

	return repo, nil
}

// poolDiscovererShim is a placeholder TenantDiscoverer used in pool mode when
// no explicit discoverer was injected. ListTenants delegates to the pool
// resolver before this is ever reached, so it returns no tenants defensively.
type poolDiscovererShim struct{}

func (poolDiscovererShim) DiscoverTenants(context.Context) ([]string, error) {
	return nil, nil
}
