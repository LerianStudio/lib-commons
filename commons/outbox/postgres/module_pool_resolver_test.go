//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
)

type modulePoolResolverStub struct {
	mu          sync.Mutex
	tenants     []string
	pools       map[string]*sql.DB
	resolved    []string
	listErr     error
	poolErr     map[string]error
	poolStarted chan string
	poolRelease <-chan struct{}
}

func (resolver *modulePoolResolverStub) PoolForTenant(_ context.Context, tenantID string) (*sql.DB, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	resolver.resolved = append(resolver.resolved, tenantID)
	if resolver.poolStarted != nil {
		resolver.poolStarted <- tenantID
	}
	if resolver.poolRelease != nil {
		<-resolver.poolRelease
	}
	if err := resolver.poolErr[tenantID]; err != nil {
		return nil, err
	}

	return resolver.pools[tenantID], nil
}

func (resolver *modulePoolResolverStub) ListTenants(context.Context) ([]string, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	return append([]string(nil), resolver.tenants...), resolver.listErr
}

func (resolver *modulePoolResolverStub) setTenants(tenants []string) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	resolver.tenants = append([]string(nil), tenants...)
}

func (resolver *modulePoolResolverStub) resolvedTenants() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	return append([]string(nil), resolver.resolved...)
}

func TestModulePoolResolver_ListTenantDispatchScopes_DeduplicatesPhysicalDatabases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		genericDB  string
		moduleDB   string
		wantScopes []outbox.TenantDispatchScope
	}{
		{
			name:      "distinct databases retain both dispatch scopes",
			genericDB: "generic",
			moduleDB:  "consignado",
			wantScopes: []outbox.TenantDispatchScope{
				{TenantID: "tenant-a"},
				{TenantID: "tenant-a", PoolKey: "consignado"},
			},
		},
		{
			name:      "shared database is scanned once",
			genericDB: "shared",
			moduleDB:  "shared",
			wantScopes: []outbox.TenantDispatchScope{
				{TenantID: "tenant-a"},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}}
			module := &modulePoolResolverStub{}
			resolver, err := NewModulePoolResolver(
				generic,
				"default-tenant",
				func(context.Context, string) (*tmcore.TenantConfig, error) {
					return tenantConfigWithDatabases(test.genericDB, test.moduleDB), nil
				},
				ModulePool{Name: "consignado", Resolver: module},
			)
			require.NoError(t, err)

			scopes, err := resolver.ListTenantDispatchScopes(t.Context())

			require.NoError(t, err)
			assert.Equal(t, test.wantScopes, scopes)
		})
	}
}

func TestModulePoolResolver_ListTenantDispatchScopes_CanonicalizesPhysicalDatabaseIdentity(t *testing.T) {
	t.Parallel()

	sharedScope := []outbox.TenantDispatchScope{{TenantID: "tenant-a"}}
	distinctScopes := []outbox.TenantDispatchScope{
		{TenantID: "tenant-a"},
		{TenantID: "tenant-a", PoolKey: "consignado"},
	}

	tests := []struct {
		name       string
		genericPG  *tmcore.PostgreSQLConfig
		modulePG   *tmcore.PostgreSQLConfig
		wantScopes []outbox.TenantDispatchScope
	}{
		{
			name: "host case, trailing dot, whitespace, default schema, and default port merge",
			genericPG: &tmcore.PostgreSQLConfig{
				Host: " POSTGRES.EXAMPLE.COM. ", Port: 0, Database: " ledger ", Schema: "",
			},
			modulePG: &tmcore.PostgreSQLConfig{
				Host: "postgres.example.com", Port: 5432, Database: "ledger", Schema: " public ",
			},
			wantScopes: sharedScope,
		},
		{
			name: "differently cased database names stay distinct",
			genericPG: &tmcore.PostgreSQLConfig{
				Host: "postgres.example.com", Port: 5432, Database: "Ledger",
			},
			modulePG: &tmcore.PostgreSQLConfig{
				Host: "postgres.example.com", Port: 5432, Database: "ledger",
			},
			wantScopes: distinctScopes,
		},
		{
			name: "differently cased schemas stay distinct",
			genericPG: &tmcore.PostgreSQLConfig{
				Host: "postgres.example.com", Port: 5432, Database: "ledger", Schema: "Public",
			},
			modulePG: &tmcore.PostgreSQLConfig{
				Host: "postgres.example.com", Port: 5432, Database: "ledger", Schema: "public",
			},
			wantScopes: distinctScopes,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}}
			resolver, err := NewModulePoolResolver(
				generic,
				"default-tenant",
				func(context.Context, string) (*tmcore.TenantConfig, error) {
					return &tmcore.TenantConfig{Databases: map[string]tmcore.DatabaseConfig{
						"aaa-generic": {PostgreSQL: test.genericPG},
						"consignado":  {PostgreSQL: test.modulePG},
					}}, nil
				},
				ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
			)
			require.NoError(t, err)

			scopes, err := resolver.ListTenantDispatchScopes(t.Context())

			require.NoError(t, err)
			assert.Equal(t, test.wantScopes, scopes)
		})
	}
}

func TestNewModulePoolResolver_Validation(t *testing.T) {
	t.Parallel()

	validResolver := &modulePoolResolverStub{}
	validLoader := func(context.Context, string) (*tmcore.TenantConfig, error) {
		return tenantConfigWithDatabases("generic", "module"), nil
	}
	tests := []struct {
		name          string
		generic       outbox.TenantPoolResolver
		defaultTenant string
		loader        TenantConfigLoader
		modules       []ModulePool
		wantErr       error
	}{
		{
			name:          "generic resolver required",
			defaultTenant: "default-tenant",
			loader:        validLoader,
			wantErr:       ErrGenericPoolResolverRequired,
		},
		{
			name:    "default tenant required",
			generic: validResolver,
			loader:  validLoader,
			wantErr: ErrDefaultTenantIDRequired,
		},
		{
			name:          "config loader required",
			generic:       validResolver,
			defaultTenant: "default-tenant",
			wantErr:       ErrTenantConfigLoaderRequired,
		},
		{
			name:          "module name required",
			generic:       validResolver,
			defaultTenant: "default-tenant",
			loader:        validLoader,
			modules:       []ModulePool{{Resolver: validResolver}},
			wantErr:       ErrModulePoolNameRequired,
		},
		{
			name:          "module resolver required",
			generic:       validResolver,
			defaultTenant: "default-tenant",
			loader:        validLoader,
			modules:       []ModulePool{{Name: "consignado"}},
			wantErr:       ErrModulePoolResolverRequired,
		},
		{
			name:          "duplicate module rejected",
			generic:       validResolver,
			defaultTenant: "default-tenant",
			loader:        validLoader,
			modules: []ModulePool{
				{Name: "consignado", Resolver: validResolver},
				{Name: " consignado ", Resolver: validResolver},
			},
			wantErr: ErrDuplicateModulePool,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver, err := NewModulePoolResolver(
				test.generic,
				test.defaultTenant,
				test.loader,
				test.modules...,
			)

			require.Nil(t, resolver)
			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestModulePoolResolver_LegacyTenantPoolResolverContract(t *testing.T) {
	t.Parallel()

	genericDB := &sql.DB{}
	generic := &modulePoolResolverStub{
		tenants: []string{"tenant-a"},
		pools:   map[string]*sql.DB{"tenant-a": genericDB},
	}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "module"), nil
		},
	)
	require.NoError(t, err)

	tenants, err := resolver.ListTenants(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"tenant-a"}, tenants)

	pool, err := resolver.PoolForTenant(t.Context(), " tenant-a ")
	require.NoError(t, err)
	assert.Same(t, genericDB, pool)
}

func TestModulePoolResolver_ListTenantDispatchScopes_GlobalListFailureUsesLastKnownGood(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "module"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	initial, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Len(t, initial, 2)

	generic.mu.Lock()
	generic.listErr = errors.New("tenant manager unavailable")
	generic.mu.Unlock()
	resolver.InvalidateTopology()

	cached, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, initial, cached)
}

func TestModulePoolResolver_ListTenantDispatchScopes_DefaultTenantIncludesConfiguredModulePools(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"default-tenant", "default-tenant", " "}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(_ context.Context, tenantID string) (*tmcore.TenantConfig, error) {
			require.Equal(t, "default-tenant", tenantID)

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	scopes, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []outbox.TenantDispatchScope{
		{TenantID: "default-tenant"},
		{TenantID: "default-tenant", PoolKey: "consignado"},
	}, scopes)
}

func TestModulePoolResolver_ListTenantDispatchScopes_DefaultTenantLoadFailureKeepsGenericPool(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"default-tenant"}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return nil, errors.New("default module topology unavailable")
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	scopes, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []outbox.TenantDispatchScope{{TenantID: "default-tenant"}}, scopes)
}

func TestModulePoolResolver_ListTenantDispatchScopes_IsolatesTenantFailureAndUsesLastKnownGood(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a", "tenant-b"}}
	module := &modulePoolResolverStub{}
	failTenantA := false
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(_ context.Context, tenantID string) (*tmcore.TenantConfig, error) {
			if tenantID == "tenant-a" && failTenantA {
				return nil, errors.New("tenant config unavailable")
			}

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)

	initial, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Len(t, initial, 4)

	failTenantA = true
	resolver.InvalidateTopology()
	withFailure, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, initial, withFailure, "tenant-a must retain its last-known-good topology while tenant-b remains healthy")
}

func TestModulePoolResolver_ListTenantDispatchScopes_FailureWithoutTopologySkipsOnlyFailedTenant(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a", "tenant-b"}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(_ context.Context, tenantID string) (*tmcore.TenantConfig, error) {
			if tenantID == "tenant-a" {
				return nil, errors.New("tenant config unavailable")
			}

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	scopes, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []outbox.TenantDispatchScope{
		{TenantID: "tenant-b"},
		{TenantID: "tenant-b", PoolKey: "consignado"},
	}, scopes)
}

func TestModulePoolResolver_ListTenantDispatchScopes_RemovalEvictsEveryScope(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}}
	failLoad := false
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			if failLoad {
				return nil, errors.New("tenant config unavailable")
			}

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	beforeRemoval, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Len(t, beforeRemoval, 2)

	generic.setTenants(nil)
	resolver.InvalidateTopology()
	afterRemoval, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, afterRemoval)

	generic.setTenants([]string{"tenant-a"})
	failLoad = true
	resolver.InvalidateTopology()
	afterReappearanceWithFailure, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Empty(t, afterReappearanceWithFailure, "removed tenant must not retain stale last-known-good module scopes")
}

func TestModulePoolResolver_EvictTenant_RemovesEveryScopeImmediately(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": {}}}
	module := &modulePoolResolverStub{pools: map[string]*sql.DB{"tenant-a": {}}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	resolver.EvictTenant(" tenant-a ")

	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
}

func TestModulePoolResolver_PoolForTenantDispatchScope_RoutesWithoutChangingTenantIdentity(t *testing.T) {
	t.Parallel()

	genericDB := &sql.DB{}
	moduleDB := &sql.DB{}
	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": genericDB}}
	module := &modulePoolResolverStub{pools: map[string]*sql.DB{"tenant-a": moduleDB}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	pool, err := resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)

	require.NoError(t, err)
	assert.Same(t, moduleDB, pool)
	assert.Empty(t, generic.resolvedTenants())
	assert.Equal(t, []string{"tenant-a"}, module.resolvedTenants())
}

func TestModulePoolResolver_PoolForTenantDispatchScope_FailsClosedForUnknownRemovedAndBrokenScopes(t *testing.T) {
	t.Parallel()

	moduleFailure := errors.New("module pool unavailable")
	generic := &modulePoolResolverStub{
		tenants: []string{"tenant-a"},
		pools:   map[string]*sql.DB{"tenant-a": {}},
	}
	module := &modulePoolResolverStub{
		pools:   map[string]*sql.DB{"tenant-a": nil},
		poolErr: map[string]error{"tenant-a": moduleFailure},
	}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "module"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "unknown"},
	)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)

	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	require.ErrorIs(t, err, moduleFailure)

	generic.setTenants(nil)
	resolver.InvalidateTopology()
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
}

func TestModulePoolResolver_PoolForTenantDispatchScope_NilGenericPoolFailsClosed(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": nil}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "generic"), nil
		},
	)
	require.NoError(t, err)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	pool, err := resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a"},
	)

	require.Nil(t, pool)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
}

func TestModulePoolResolver_EvictTenant_ReturnsBeforeInFlightPoolResolutionCompletes(t *testing.T) {
	t.Parallel()

	moduleDB := &sql.DB{}
	releasePool := make(chan struct{})
	poolStarted := make(chan string, 1)
	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": {}}}
	module := &modulePoolResolverStub{
		pools:       map[string]*sql.DB{"tenant-a": moduleDB},
		poolStarted: poolStarted,
		poolRelease: releasePool,
	}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	routeResult := make(chan error, 1)
	go func() {
		pool, routeErr := resolver.PoolForTenantDispatchScope(
			t.Context(),
			outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
		)
		if routeErr == nil && pool != moduleDB {
			routeErr = errors.New("resolved wrong module pool")
		}
		routeResult <- routeErr
	}()
	assert.Equal(t, "tenant-a", <-poolStarted)

	evicted := make(chan struct{})
	go func() {
		resolver.EvictTenant("tenant-a")
		close(evicted)
	}()
	evictedImmediately := false
	select {
	case <-evicted:
		evictedImmediately = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePool)
	require.True(t, evictedImmediately, "eviction waited for an in-flight pool resolution")

	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)

	require.NoError(t, <-routeResult)
}

func TestModulePoolResolver_EvictTenant_StaleRefreshCannotRestoreScopes(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": {}}}
	module := &modulePoolResolverStub{pools: map[string]*sql.DB{"tenant-a": {}}}
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var loadCalls atomic.Int32
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			if loadCalls.Add(1) == 2 {
				close(refreshStarted)
				<-releaseRefresh
			}

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	resolver.InvalidateTopology()

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := resolver.ListTenantDispatchScopes(t.Context())
		refreshDone <- refreshErr
	}()
	<-refreshStarted

	resolver.EvictTenant("tenant-a")
	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)

	close(releaseRefresh)
	require.NoError(t, <-refreshDone)
	_, err = resolver.PoolForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
}

func TestRepository_TenantDispatchScope_RoutesModulePoolWithRealTenantContext(t *testing.T) {
	t.Parallel()

	moduleDB := &sql.DB{}
	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": {}}}
	module := &modulePoolResolverStub{pools: map[string]*sql.DB{"tenant-a": moduleDB}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "module"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)
	repo := &Repository{poolResolver: resolver}
	scopes, err := repo.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Len(t, scopes, 2)

	ctx := repo.ContextForTenantDispatchScope(t.Context(), scopes[1])
	ctx = outbox.ContextWithTenantID(ctx, scopes[1].TenantID)
	pool, err := newTenantPoolLookup(resolver)(ctx)

	require.NoError(t, err)
	assert.Same(t, moduleDB, pool)
	tenantID, ok := outbox.TenantIDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "tenant-a", tenantID)
}

func TestRepository_ListTenantDispatchScopes_LegacyResolverPreservesRealTenantIDs(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		client:           newTestClient(t),
		tenantResolver:   NoopTenantResolver{},
		tenantDiscoverer: poolDiscovererShim{},
		poolResolver:     &modulePoolResolverStub{tenants: []string{"tenant-a", "tenant-b"}},
	}

	scopes, err := repo.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []outbox.TenantDispatchScope{
		{TenantID: "tenant-a"},
		{TenantID: "tenant-b"},
	}, scopes)

	legacyCtx := repo.ContextForTenantDispatchScope(
		outbox.ContextWithTenantID(t.Context(), "tenant-a"),
		outbox.TenantDispatchScope{TenantID: "tenant-a"},
	)
	_, hasOpaqueScope := legacyCtx.Value(tenantDispatchPoolContextKey{}).(outbox.TenantDispatchScope)
	assert.False(t, hasOpaqueScope, "legacy resolvers must keep the tenant-only lookup path")

	var nilRepo *Repository
	_, err = nilRepo.ListTenantDispatchScopes(t.Context())
	require.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

func TestRepository_TenantDispatchScope_MismatchedTenantFailsClosed(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{
		tenants: []string{"tenant-a", "tenant-b"},
		pools:   map[string]*sql.DB{"tenant-a": {}, "tenant-b": {}},
	}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "module"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)
	repo := &Repository{poolResolver: resolver}
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)

	ctx := repo.ContextForTenantDispatchScope(
		outbox.ContextWithTenantID(t.Context(), "tenant-b"),
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)
	_, err = newTenantPoolLookup(resolver)(ctx)

	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
	assert.Empty(t, generic.resolvedTenants())

	ctx = repo.ContextForTenantDispatchScope(
		t.Context(),
		outbox.TenantDispatchScope{TenantID: "tenant-a"},
	)
	_, err = newTenantPoolLookup(resolver)(ctx)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
	assert.Empty(t, generic.resolvedTenants())
}

func TestRepository_TablePresence_IsCachedPerTenantDispatchScope(t *testing.T) {
	t.Parallel()

	probeCalls := 0
	repo := &Repository{}
	repo.tablePresence = newTablePresenceGuard(func(ctx context.Context, _ string) (bool, error) {
		probeCalls++
		scope, ok := tenantDispatchScopeFromContext(ctx)
		require.True(t, ok)

		return scope.PoolKey == "", nil
	}, time.Hour)

	genericCtx := context.WithValue(
		outbox.ContextWithTenantID(t.Context(), "tenant-a"),
		tenantDispatchPoolContextKey{},
		outbox.TenantDispatchScope{TenantID: "tenant-a"},
	)
	moduleCtx := context.WithValue(
		outbox.ContextWithTenantID(t.Context(), "tenant-a"),
		tenantDispatchPoolContextKey{},
		outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
	)

	genericMissing, err := repo.tenantOutboxTableMissing(genericCtx)
	require.NoError(t, err)
	moduleMissing, err := repo.tenantOutboxTableMissing(moduleCtx)
	require.NoError(t, err)

	assert.False(t, genericMissing)
	assert.True(t, moduleMissing)
	assert.Equal(t, 2, probeCalls)
}

func TestModulePoolResolver_ConcurrentListRouteAndRemoval_IsRaceFree(t *testing.T) {
	t.Parallel()

	generic := &modulePoolResolverStub{tenants: []string{"tenant-a"}, pools: map[string]*sql.DB{"tenant-a": {}}}
	module := &modulePoolResolverStub{pools: map[string]*sql.DB{"tenant-a": {}}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)

	const iterations = 100
	var waitGroup sync.WaitGroup
	unexpectedErrors := make(chan error, iterations*2)
	waitGroup.Add(3)

	go func() {
		defer waitGroup.Done()
		for range iterations {
			if _, listErr := resolver.ListTenantDispatchScopes(t.Context()); listErr != nil {
				unexpectedErrors <- listErr
			}
		}
	}()
	go func() {
		defer waitGroup.Done()
		for range iterations {
			_, poolErr := resolver.PoolForTenantDispatchScope(
				t.Context(),
				outbox.TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"},
			)
			if poolErr != nil && !errors.Is(poolErr, ErrTenantPoolUnavailable) {
				unexpectedErrors <- poolErr
			}
		}
	}()
	go func() {
		defer waitGroup.Done()
		for iteration := range iterations {
			if iteration%2 == 0 {
				generic.setTenants(nil)
			} else {
				generic.setTenants([]string{"tenant-a"})
			}
		}
	}()

	waitGroup.Wait()
	close(unexpectedErrors)
	for unexpectedErr := range unexpectedErrors {
		require.NoError(t, unexpectedErr)
	}
}

func tenantConfigWithDatabases(genericDatabase, moduleDatabase string) *tmcore.TenantConfig {
	return &tmcore.TenantConfig{Databases: map[string]tmcore.DatabaseConfig{
		"aaa-generic": {
			PostgreSQL: &tmcore.PostgreSQLConfig{Host: "postgres", Port: 5432, Database: genericDatabase},
		},
		"consignado": {
			PostgreSQL: &tmcore.PostgreSQLConfig{Host: "postgres", Port: 5432, Database: moduleDatabase},
		},
	}}
}
