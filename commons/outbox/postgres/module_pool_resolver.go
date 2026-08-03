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

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	observability "github.com/LerianStudio/lib-observability/v2"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
)

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

type modulePoolBinding struct {
	name     string
	resolver outbox.TenantPoolResolver
}

// ModulePoolResolver composes a generic tenant pool resolver with named module
// resolvers. It enumerates each physical tenant database once while keeping
// pool routing separate from the real tenant identity.
//
// Topology is refreshed from TenantConfig on each enumeration. A failed refresh
// uses that tenant's last-known-good topology when available; otherwise only
// the failed tenant is skipped. A tenant absent from the authoritative generic
// list is removed from the cache with all module scopes.
type ModulePoolResolver struct {
	generic         outbox.TenantPoolResolver
	defaultTenantID string
	loadConfig      TenantConfigLoader
	modules         []modulePoolBinding
	moduleByName    map[string]outbox.TenantPoolResolver

	topologyMu         sync.RWMutex
	topology           map[string][]outbox.TenantDispatchScope
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

	refreshID := resolver.refreshID.Add(1)

	tenantIDs, err := resolver.generic.ListTenants(ctx)
	if err != nil {
		cached := resolver.cachedScopes()
		if len(cached) > 0 {
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
		resolver.committedRefreshID = refreshID
	}
	resolver.topologyMu.Unlock()

	return orderedScopes, nil
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

func (resolver *ModulePoolResolver) cachedScopes() []outbox.TenantDispatchScope {
	resolver.topologyMu.RLock()
	defer resolver.topologyMu.RUnlock()

	return resolver.cachedScopesLocked()
}

func (resolver *ModulePoolResolver) cachedScopesLocked() []outbox.TenantDispatchScope {
	tenantIDs := make([]string, 0, len(resolver.topology))
	for tenantID := range resolver.topology {
		tenantIDs = append(tenantIDs, tenantID)
	}

	slices.Sort(tenantIDs)

	var scopes []outbox.TenantDispatchScope
	for _, tenantID := range tenantIDs {
		scopes = append(scopes, resolver.topology[tenantID]...)
	}

	return scopes
}

func (resolver *ModulePoolResolver) logTopologyFailure(
	ctx context.Context,
	message string,
	tenantID string,
	err error,
) {
	logger, _, _, _ := observability.NewTrackingFromContext(ctx) //nolint:dogsled // Standard tracking extraction; only logger is needed.
	if logger == nil {
		return
	}

	fields := []libLog.Field{libLog.Err(err)}
	if tenantID != "" {
		fields = append(fields, libLog.String("tenant.id", tenantID))
	}

	logger.Log(ctx, libLog.LevelWarn, message, fields...)
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

	schema := strings.ToLower(strings.TrimSpace(config.Schema))
	if schema == "" {
		schema = "public"
	}

	return strings.Join([]string{
		host,
		strconv.Itoa(config.Port),
		strings.ToLower(strings.TrimSpace(config.Database)),
		schema,
	}, "\x00")
}

var (
	_ outbox.TenantPoolResolver  = (*ModulePoolResolver)(nil)
	_ tenantDispatchPoolResolver = (*ModulePoolResolver)(nil)
)
