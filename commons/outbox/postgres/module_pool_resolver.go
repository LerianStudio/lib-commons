// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	obsbridge "github.com/LerianStudio/lib-commons/v6/commons/obs/obsbridge"

	"github.com/LerianStudio/lib-commons/v6/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
)

const defaultTopologyRefreshInterval = time.Minute

var (
	// ErrGenericPoolResolverRequired is returned when module-aware composition
	// has no generic tenant pool resolver.
	ErrGenericPoolResolverRequired = errors.New("generic tenant pool resolver is required")

	// ErrTenantConfigLoaderRequired is returned when module topology cannot be
	// loaded from tenant configuration.
	ErrTenantConfigLoaderRequired = errors.New("tenant config loader is required")

	// ErrModulePoolNameRequired is returned when a named module has no name.
	ErrModulePoolNameRequired = errors.New("module pool name is required")

	// ErrModulePoolResolverRequired is returned when a named module has no pool
	// resolver.
	ErrModulePoolResolverRequired = errors.New("module tenant pool resolver is required")

	// ErrDuplicateModulePool is returned when a module is registered twice.
	ErrDuplicateModulePool = errors.New("duplicate module pool")

	// ErrTenantPostgresConfigRequired is returned when a successfully loaded
	// tenant topology has no generic PostgreSQL resource.
	ErrTenantPostgresConfigRequired = errors.New("tenant generic postgres config is required")
)

// TenantConfigLoader loads the database topology for one real tenant ID.
type TenantConfigLoader func(ctx context.Context, tenantID string) (*tmcore.TenantConfig, error)

// ModulePool binds a tenant-manager module name to its pool resolver.
type ModulePool struct {
	Name     string
	Resolver outbox.TenantPoolResolver
}

// ModulePoolResolverConfig controls tenant topology refresh behavior.
type ModulePoolResolverConfig struct {
	// TopologyRefreshInterval is the maximum age of the cached tenant and
	// physical-database topology. Non-positive values use one minute.
	TopologyRefreshInterval time.Duration
}

// DefaultModulePoolResolverConfig returns the default topology refresh policy.
func DefaultModulePoolResolverConfig() ModulePoolResolverConfig {
	return ModulePoolResolverConfig{TopologyRefreshInterval: defaultTopologyRefreshInterval}
}

func (config *ModulePoolResolverConfig) normalize() {
	if config.TopologyRefreshInterval <= 0 {
		config.TopologyRefreshInterval = defaultTopologyRefreshInterval
	}
}

type modulePoolBinding struct {
	name     string
	resolver outbox.TenantPoolResolver
}

// ModulePoolResolver composes a generic tenant pool resolver with named module
// resolvers. It enumerates each physical tenant database once while keeping
// pool routing separate from the real tenant identity.
//
// Topology is refreshed from TenantConfig at the configured interval. A failed
// refresh uses that tenant's last-known-good topology when available and is
// retried after the same interval; otherwise only the failed tenant is skipped.
// A tenant absent from the authoritative generic list is removed from the cache
// with all module scopes.
type ModulePoolResolver struct {
	generic         outbox.TenantPoolResolver
	defaultTenantID string
	loadConfig      TenantConfigLoader
	modules         []modulePoolBinding
	moduleByName    map[string]outbox.TenantPoolResolver

	refreshMu sync.Mutex

	topologyMu         sync.RWMutex
	topology           map[string][]outbox.TenantDispatchScope
	orderedScopes      []outbox.TenantDispatchScope
	topologyRefreshed  time.Time
	topologyValid      bool
	refreshInterval    time.Duration
	now                func() time.Time
	refreshID          atomic.Uint64
	committedRefreshID uint64
}

// NewModulePoolResolver builds a module-aware resolver. The generic resolver is
// the authoritative tenant list and default pool. Modules are evaluated in the
// supplied order; when multiple entries identify the same physical database,
// the first entry wins and the database is scanned once.
func NewModulePoolResolver(
	generic outbox.TenantPoolResolver,
	defaultTenantID string,
	loadConfig TenantConfigLoader,
	modules ...ModulePool,
) (*ModulePoolResolver, error) {
	return NewModulePoolResolverWithConfig(
		generic,
		defaultTenantID,
		loadConfig,
		DefaultModulePoolResolverConfig(),
		modules...,
	)
}

// NewModulePoolResolverWithConfig builds a module-aware resolver with an
// explicit topology refresh policy.
func NewModulePoolResolverWithConfig(
	generic outbox.TenantPoolResolver,
	defaultTenantID string,
	loadConfig TenantConfigLoader,
	config ModulePoolResolverConfig,
	modules ...ModulePool,
) (*ModulePoolResolver, error) {
	if generic == nil {
		return nil, ErrGenericPoolResolverRequired
	}

	defaultTenantID = strings.TrimSpace(defaultTenantID)
	if defaultTenantID == "" {
		return nil, ErrDefaultTenantIDRequired
	}

	if loadConfig == nil {
		return nil, ErrTenantConfigLoaderRequired
	}

	config.normalize()

	bindings := make([]modulePoolBinding, 0, len(modules))
	moduleByName := make(map[string]outbox.TenantPoolResolver, len(modules))

	for _, module := range modules {
		name := strings.TrimSpace(module.Name)
		if name == "" {
			return nil, ErrModulePoolNameRequired
		}

		if module.Resolver == nil {
			return nil, fmt.Errorf("%w: %s", ErrModulePoolResolverRequired, name)
		}

		if _, exists := moduleByName[name]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateModulePool, name)
		}

		bindings = append(bindings, modulePoolBinding{name: name, resolver: module.Resolver})
		moduleByName[name] = module.Resolver
	}

	return &ModulePoolResolver{
		generic:         generic,
		defaultTenantID: defaultTenantID,
		loadConfig:      loadConfig,
		modules:         bindings,
		moduleByName:    moduleByName,
		topology:        make(map[string][]outbox.TenantDispatchScope),
		refreshInterval: config.TopologyRefreshInterval,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}, nil
}

// PoolForTenant preserves the TenantPoolResolver contract by routing to the
// generic pool. Module routing is available through dispatcher scopes.
func (resolver *ModulePoolResolver) PoolForTenant(ctx context.Context, tenantID string) (*sql.DB, error) {
	if resolver == nil || resolver.generic == nil {
		return nil, ErrTenantPoolUnavailable
	}

	return resolver.generic.PoolForTenant(ctx, strings.TrimSpace(tenantID))
}

// ListTenants preserves the legacy TenantPoolResolver contract and returns real
// tenant IDs only. Pool scope identity is never encoded into these values.
func (resolver *ModulePoolResolver) ListTenants(ctx context.Context) ([]string, error) {
	if resolver == nil || resolver.generic == nil {
		return nil, ErrTenantPoolUnavailable
	}

	return resolver.generic.ListTenants(ctx)
}

// ListTenantDispatchScopes returns one dispatch scope per physical tenant
// database. It isolates per-tenant topology failures and atomically replaces
// cached topology so removed tenants lose every module scope.
func (resolver *ModulePoolResolver) ListTenantDispatchScopes(
	ctx context.Context,
) ([]outbox.TenantDispatchScope, error) {
	if resolver == nil || resolver.generic == nil {
		return nil, ErrTenantPoolUnavailable
	}

	now := resolver.currentTime()
	if scopes, fresh := resolver.cachedFreshScopes(now); fresh {
		return scopes, nil
	}

	resolver.refreshMu.Lock()
	defer resolver.refreshMu.Unlock()

	now = resolver.currentTime()
	if scopes, fresh := resolver.cachedFreshScopes(now); fresh {
		return scopes, nil
	}

	refreshID := resolver.refreshID.Add(1)

	tenantIDs, err := resolver.generic.ListTenants(ctx)
	if err != nil {
		cached, initialized := resolver.cachedScopesWithState()
		if initialized {
			resolver.commitRefreshAttempt(refreshID, now)
			resolver.logTopologyFailure(ctx, "tenant list refresh failed; using last-known-good outbox topology", "", err)

			return cached, nil
		}

		return nil, fmt.Errorf("list generic outbox tenants: %w", err)
	}

	uniqueTenantIDs := uniqueTenantIDs(tenantIDs)
	nextTopology := make(map[string][]outbox.TenantDispatchScope, len(uniqueTenantIDs))
	orderedScopes := make([]outbox.TenantDispatchScope, 0, len(uniqueTenantIDs)*(len(resolver.modules)+1))

	for _, tenantID := range uniqueTenantIDs {
		scopes, topologyErr := resolver.scopesForTenant(ctx, tenantID)
		if topologyErr != nil {
			scopes = resolver.cachedTenantScopes(tenantID)
			if len(scopes) == 0 && tenantID == resolver.defaultTenantID {
				scopes = []outbox.TenantDispatchScope{{TenantID: tenantID}}
			}

			resolver.logTopologyFailure(ctx, "tenant outbox topology refresh failed", tenantID, topologyErr)
		}

		if len(scopes) == 0 {
			continue
		}

		nextTopology[tenantID] = slices.Clone(scopes)
		orderedScopes = append(orderedScopes, scopes...)
	}

	resolver.topologyMu.Lock()
	if refreshID <= resolver.committedRefreshID {
		orderedScopes = resolver.cachedScopesLocked()
	} else {
		resolver.topology = nextTopology
		resolver.orderedScopes = slices.Clone(orderedScopes)
		resolver.topologyRefreshed = now
		resolver.topologyValid = true
		resolver.committedRefreshID = refreshID
	}
	resolver.topologyMu.Unlock()

	return orderedScopes, nil
}

// InvalidateTopology expires the cached tenant topology. The next enumeration
// performs one coalesced refresh while retaining last-known-good scopes for the
// existing fail-open behavior if that refresh fails.
func (resolver *ModulePoolResolver) InvalidateTopology() {
	if resolver == nil {
		return
	}

	invalidationID := resolver.refreshID.Add(1)
	resolver.topologyMu.Lock()

	resolver.topologyValid = false
	if invalidationID > resolver.committedRefreshID {
		resolver.committedRefreshID = invalidationID
	}
	resolver.topologyMu.Unlock()
}

// PoolForTenantDispatchScope resolves a scope generated by
// ListTenantDispatchScopes. Unknown or removed scopes fail closed.
func (resolver *ModulePoolResolver) PoolForTenantDispatchScope(
	ctx context.Context,
	scope outbox.TenantDispatchScope,
) (*sql.DB, error) {
	if resolver == nil {
		return nil, ErrTenantPoolUnavailable
	}

	scope.TenantID = strings.TrimSpace(scope.TenantID)

	scope.PoolKey = strings.TrimSpace(scope.PoolKey)
	if scope.TenantID == "" {
		return nil, ErrTenantPoolUnavailable
	}

	resolver.topologyMu.RLock()

	if !slices.Contains(resolver.topology[scope.TenantID], scope) {
		resolver.topologyMu.RUnlock()

		return nil, ErrTenantPoolUnavailable
	}

	poolResolver := resolver.generic

	if scope.PoolKey == "" {
		resolver.topologyMu.RUnlock()
	} else {
		moduleResolver, ok := resolver.moduleByName[scope.PoolKey]
		if !ok || moduleResolver == nil {
			resolver.topologyMu.RUnlock()

			return nil, ErrTenantPoolUnavailable
		}

		poolResolver = moduleResolver

		resolver.topologyMu.RUnlock()
	}

	pool, err := poolResolver.PoolForTenant(ctx, scope.TenantID)
	if err != nil {
		if scope.PoolKey == "" {
			return nil, fmt.Errorf("resolve generic outbox pool: %w", err)
		}

		return nil, fmt.Errorf("resolve module %q outbox pool: %w", scope.PoolKey, err)
	}

	if pool == nil {
		return nil, ErrTenantPoolUnavailable
	}

	return pool, nil
}

// EvictTenant removes every cached dispatch scope for tenantID immediately.
// Pool resolutions already in progress may complete, but new resolutions fail
// closed, and topology refreshes started before eviction cannot restore scopes.
func (resolver *ModulePoolResolver) EvictTenant(tenantID string) {
	if resolver == nil {
		return
	}

	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return
	}

	evictionID := resolver.refreshID.Add(1)
	resolver.topologyMu.Lock()
	delete(resolver.topology, tenantID)
	resolver.orderedScopes = slices.DeleteFunc(resolver.orderedScopes, func(scope outbox.TenantDispatchScope) bool {
		return scope.TenantID == tenantID
	})
	resolver.topologyValid = false

	if evictionID > resolver.committedRefreshID {
		resolver.committedRefreshID = evictionID
	}
	resolver.topologyMu.Unlock()
}

func (resolver *ModulePoolResolver) scopesForTenant(
	ctx context.Context,
	tenantID string,
) ([]outbox.TenantDispatchScope, error) {
	config, err := resolver.loadConfig(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load tenant config: %w", err)
	}

	if config == nil {
		return nil, ErrTenantPostgresConfigRequired
	}

	genericConfig := config.GetPostgreSQLConfig("", "")
	if genericConfig == nil {
		return nil, ErrTenantPostgresConfigRequired
	}

	seenResources := map[string]struct{}{postgresResourceIdentity(genericConfig): {}}
	scopes := make([]outbox.TenantDispatchScope, 0, len(resolver.modules)+1)
	scopes = append(scopes, outbox.TenantDispatchScope{TenantID: tenantID})

	for _, module := range resolver.modules {
		moduleConfig := config.GetPostgreSQLConfig("", module.name)
		if moduleConfig == nil {
			continue
		}

		identity := postgresResourceIdentity(moduleConfig)
		if _, exists := seenResources[identity]; exists {
			continue
		}

		seenResources[identity] = struct{}{}

		scopes = append(scopes, outbox.TenantDispatchScope{TenantID: tenantID, PoolKey: module.name})
	}

	return scopes, nil
}

func (resolver *ModulePoolResolver) cachedTenantScopes(tenantID string) []outbox.TenantDispatchScope {
	resolver.topologyMu.RLock()
	defer resolver.topologyMu.RUnlock()

	return slices.Clone(resolver.topology[tenantID])
}

func (resolver *ModulePoolResolver) cachedScopesWithState() ([]outbox.TenantDispatchScope, bool) {
	resolver.topologyMu.RLock()
	defer resolver.topologyMu.RUnlock()

	return resolver.cachedScopesLocked(), resolver.topologyValid || !resolver.topologyRefreshed.IsZero()
}

func (resolver *ModulePoolResolver) cachedFreshScopes(now time.Time) ([]outbox.TenantDispatchScope, bool) {
	resolver.topologyMu.RLock()
	defer resolver.topologyMu.RUnlock()

	if !resolver.topologyValid || !now.Before(resolver.topologyRefreshed.Add(resolver.refreshInterval)) {
		return nil, false
	}

	return resolver.cachedScopesLocked(), true
}

func (resolver *ModulePoolResolver) commitRefreshAttempt(refreshID uint64, now time.Time) {
	resolver.topologyMu.Lock()
	defer resolver.topologyMu.Unlock()

	if refreshID <= resolver.committedRefreshID {
		return
	}

	resolver.topologyRefreshed = now
	resolver.topologyValid = true
	resolver.committedRefreshID = refreshID
}

func (resolver *ModulePoolResolver) currentTime() time.Time {
	if resolver.now == nil {
		return time.Now().UTC()
	}

	return resolver.now().UTC()
}

func (resolver *ModulePoolResolver) cachedScopesLocked() []outbox.TenantDispatchScope {
	return slices.Clone(resolver.orderedScopes)
}

func (resolver *ModulePoolResolver) logTopologyFailure(
	ctx context.Context,
	message string,
	tenantID string,
	err error,
) {
	logger, _, _, _ := obsbridge.TrackingFromContext(ctx) //nolint:dogsled // Standard tracking extraction; only logger is needed.
	if nilcheck.Interface(logger) {
		return
	}

	fields := []any{"error", err}
	if tenantID != "" {
		fields = append(fields, "tenant.id", tenantID)
	}

	logger.Log(ctx, obs.LevelWarn, message, fields...)
}

func uniqueTenantIDs(tenantIDs []string) []string {
	unique := make([]string, 0, len(tenantIDs))
	seen := make(map[string]struct{}, len(tenantIDs))

	for _, tenantID := range tenantIDs {
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			continue
		}

		if _, exists := seen[tenantID]; exists {
			continue
		}

		seen[tenantID] = struct{}{}
		unique = append(unique, tenantID)
	}

	return unique
}

func postgresResourceIdentity(config *tmcore.PostgreSQLConfig) string {
	if config == nil {
		return ""
	}

	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(config.Host)), ".")

	// Database and schema names compare case-sensitively: PostgreSQL folds
	// unquoted identifiers to lowercase, but quoted mixed-case names denote
	// distinct catalog objects, so folding here could merge distinct databases.
	schema := strings.TrimSpace(config.Schema)
	if schema == "" {
		schema = "public"
	}

	port := config.Port
	if port == 0 {
		port = 5432
	}

	return strings.Join([]string{
		host,
		strconv.Itoa(port),
		strings.TrimSpace(config.Database),
		schema,
	}, "\x00")
}

var (
	_ outbox.TenantPoolResolver  = (*ModulePoolResolver)(nil)
	_ tenantDispatchPoolResolver = (*ModulePoolResolver)(nil)
)
