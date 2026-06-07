//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) *libPostgres.Client {
	t.Helper()

	client, err := libPostgres.New(libPostgres.Config{
		PrimaryDSN: "postgres://localhost:5432/postgres",
		ReplicaDSN: "postgres://localhost:5432/postgres",
	})
	require.NoError(t, err)

	return client
}

// recordingTenantPoolResolver is a stub outbox.TenantPoolResolver.
type recordingTenantPoolResolver struct {
	pool       *sql.DB
	poolErr    error
	tenants    []string
	tenantsErr error
	poolCalls  []string
	listCalls  int
}

func (r *recordingTenantPoolResolver) PoolForTenant(_ context.Context, tenantID string) (*sql.DB, error) {
	r.poolCalls = append(r.poolCalls, tenantID)

	if r.poolErr != nil {
		return nil, r.poolErr
	}

	return r.pool, nil
}

func (r *recordingTenantPoolResolver) ListTenants(_ context.Context) ([]string, error) {
	r.listCalls++

	if r.tenantsErr != nil {
		return nil, r.tenantsErr
	}

	return append([]string(nil), r.tenants...), nil
}

func validTenantCtx() context.Context {
	return outbox.ContextWithTenantID(context.Background(), "22222222-2222-2222-2222-222222222222")
}

// ---- NoopTenantResolver ----

func TestNoopTenantResolver_ApplyTenantIsNoop(t *testing.T) {
	t.Parallel()

	resolver := NoopTenantResolver{}

	// A nil *sql.Tx would panic if the resolver tried to execute SQL.
	err := resolver.ApplyTenant(context.Background(), nil, "any-tenant")
	require.NoError(t, err)
}

func TestNoopTenantResolver_RequiresTenant(t *testing.T) {
	t.Parallel()

	var resolver outbox.TenantResolver = NoopTenantResolver{}

	reporter, ok := resolver.(tenantRequirementProvider)
	require.True(t, ok)
	require.True(t, reporter.RequiresTenant())
}

func TestNoopTenantResolver_NoTenantColumn(t *testing.T) {
	t.Parallel()

	_, ok := any(NoopTenantResolver{}).(tenantColumnProvider)
	require.False(t, ok, "NoopTenantResolver must not advertise a tenant column")
}

// ---- tier precedence in the pool lookup ----

func TestPoolLookup_TierOneContextPoolWins(t *testing.T) {
	t.Parallel()

	ctxDB := &sql.DB{}
	resolverDB := &sql.DB{}

	poolResolver := &recordingTenantPoolResolver{pool: resolverDB}

	lookup := newTenantPoolLookup(poolResolver)

	ctx := tmcore.ContextWithPG(validTenantCtx(), fakeDBResolver{primary: []*sql.DB{ctxDB}})

	db, err := lookup(ctx)
	require.NoError(t, err)
	require.Same(t, ctxDB, db)
	require.Empty(t, poolResolver.poolCalls, "resolver must not be consulted when ctx pool present")
}

func TestPoolLookup_TierTwoResolverUsedWhenContextEmpty(t *testing.T) {
	t.Parallel()

	resolverDB := &sql.DB{}
	poolResolver := &recordingTenantPoolResolver{pool: resolverDB}

	lookup := newTenantPoolLookup(poolResolver)

	db, err := lookup(validTenantCtx())
	require.NoError(t, err)
	require.Same(t, resolverDB, db)
	require.Equal(t, []string{"22222222-2222-2222-2222-222222222222"}, poolResolver.poolCalls)
}

func TestPoolLookup_TierThreeFailsClosed(t *testing.T) {
	t.Parallel()

	poolResolver := &recordingTenantPoolResolver{}

	lookup := newTenantPoolLookup(poolResolver)

	// No ctx pool, no tenant in context -> fail closed, resolver not consulted.
	db, err := lookup(context.Background())
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
	require.Empty(t, poolResolver.poolCalls)
}

func TestPoolLookup_TierOneNilGuardsFallThrough(t *testing.T) {
	t.Parallel()

	resolverDB := &sql.DB{}
	poolResolver := &recordingTenantPoolResolver{pool: resolverDB}

	lookup := newTenantPoolLookup(poolResolver)

	// ctx pool present but with no primary DBs -> must fall through to resolver.
	ctx := tmcore.ContextWithPG(validTenantCtx(), fakeDBResolver{primary: nil})

	db, err := lookup(ctx)
	require.NoError(t, err)
	require.Same(t, resolverDB, db)
	require.Equal(t, []string{"22222222-2222-2222-2222-222222222222"}, poolResolver.poolCalls)
}

// ---- MultiTenantConfig / NewMultiTenantRepository ----

func TestNewMultiTenantRepository_MismatchGuard(t *testing.T) {
	t.Parallel()

	repo, err := NewMultiTenantRepository(MultiTenantConfig{
		Client:             newTestClient(t),
		MultiTenantEnabled: true,
		// no PoolResolver, no Manager.
	})
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrMultiTenantMisconfigured)
}

func TestNewMultiTenantRepository_PoolModeWired(t *testing.T) {
	t.Parallel()

	poolResolver := &recordingTenantPoolResolver{pool: &sql.DB{}, tenants: []string{testDefaultTenant}}

	repo, err := NewMultiTenantRepository(MultiTenantConfig{
		Client:             newTestClient(t),
		PoolResolver:       poolResolver,
		MultiTenantEnabled: true,
	})
	require.NoError(t, err)
	require.NotNil(t, repo)

	// Pool mode must require a tenant so empty ctx fails closed.
	require.True(t, repo.RequiresTenant())

	// ListTenants delegates to the pool resolver when no explicit discoverer set.
	tenants, err := repo.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{testDefaultTenant}, tenants)
	require.Equal(t, 1, poolResolver.listCalls)
}

func TestNewMultiTenantRepository_SingleTenantIdenticalToNewRepository(t *testing.T) {
	t.Parallel()

	rootClient := newTestClient(t)

	plain, err := NewRepository(rootClient, noopTenantResolver{}, noopTenantDiscoverer{})
	require.NoError(t, err)

	mt, err := NewMultiTenantRepository(MultiTenantConfig{
		Client:           rootClient,
		TenantResolver:   noopTenantResolver{},
		TenantDiscoverer: noopTenantDiscoverer{},
	})
	require.NoError(t, err)

	require.Equal(t, plain.requireTenant, mt.requireTenant)
	require.Equal(t, plain.tenantColumn, mt.tenantColumn)
	require.Equal(t, plain.tableName, mt.tableName)
	require.Nil(t, mt.primaryDBLookup, "single-tenant mode must not install a pool lookup")
}

// ---- ListTenants precedence ----

func TestRepository_ListTenants_ExplicitDiscovererWinsOverPoolResolver(t *testing.T) {
	t.Parallel()

	root := newTestClient(t)
	poolResolver := &recordingTenantPoolResolver{tenants: []string{"from-pool"}}

	repo, err := NewMultiTenantRepository(MultiTenantConfig{
		Client:             root,
		PoolResolver:       poolResolver,
		TenantDiscoverer:   staticDiscoverer{ids: []string{"from-discoverer"}},
		MultiTenantEnabled: true,
	})
	require.NoError(t, err)

	tenants, err := repo.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"from-discoverer"}, tenants)
	require.Equal(t, 0, poolResolver.listCalls, "explicit discoverer must win")
}

// ---- table presence guard ----

func TestTablePresenceGuard_MissingTableSkipsAndCaches(t *testing.T) {
	t.Parallel()

	calls := 0
	probe := func(context.Context, string) (bool, error) {
		calls++
		return false, nil
	}

	guard := newTablePresenceGuard(probe, 50*time.Millisecond)

	present, err := guard.present(context.Background(), "tenant-x")
	require.NoError(t, err)
	require.False(t, present)

	// Second call within interval is served from cache.
	present, err = guard.present(context.Background(), "tenant-x")
	require.NoError(t, err)
	require.False(t, present)
	require.Equal(t, 1, calls, "presence must be cached within the interval")
}

func TestTablePresenceGuard_InvalidatesOnError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("connection refused")
	calls := 0
	probe := func(context.Context, string) (bool, error) {
		calls++
		return false, sentinel
	}

	guard := newTablePresenceGuard(probe, time.Hour)

	_, err := guard.present(context.Background(), "tenant-y")
	require.ErrorIs(t, err, sentinel)

	// Error must not be cached: a subsequent call re-probes.
	_, err = guard.present(context.Background(), "tenant-y")
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 2, calls, "errors must invalidate the cache")
}

func TestTablePresenceGuard_ReprobesAfterInterval(t *testing.T) {
	t.Parallel()

	calls := 0
	probe := func(context.Context, string) (bool, error) {
		calls++
		return true, nil
	}

	guard := newTablePresenceGuard(probe, time.Millisecond)

	_, err := guard.present(context.Background(), "tenant-z")
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond)

	_, err = guard.present(context.Background(), "tenant-z")
	require.NoError(t, err)
	require.Equal(t, 2, calls, "presence must be re-probed after the interval elapses")
}

// ---- table presence guard wiring into dispatch read path ----

func TestRepository_ListPending_SkipsWhenTableMissing(t *testing.T) {
	t.Parallel()

	probeCalls := 0

	repo := &Repository{
		client:             newTestClient(t),
		tenantResolver:     NoopTenantResolver{},
		tenantDiscoverer:   poolDiscovererShim{},
		poolResolver:       &recordingTenantPoolResolver{},
		requireTenant:      true,
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
		tablePresence: newTablePresenceGuard(func(context.Context, string) (bool, error) {
			probeCalls++
			return false, nil // table absent
		}, time.Hour),
		// primaryDBLookup intentionally panics if reached: a missing table must
		// short-circuit before any tenant transaction is opened.
		primaryDBLookup: func(context.Context) (*sql.DB, error) {
			t.Fatal("primaryDBLookup must not be reached when table is missing")
			return nil, nil
		},
	}

	events, err := repo.ListPending(validTenantCtx(), 10)
	require.NoError(t, err)
	require.Nil(t, events)
	require.Equal(t, 1, probeCalls)
}

func TestRepository_ListPending_ProbeErrorSurfaces(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("tenant db unreachable")

	repo := &Repository{
		client:             newTestClient(t),
		tenantResolver:     NoopTenantResolver{},
		tenantDiscoverer:   poolDiscovererShim{},
		poolResolver:       &recordingTenantPoolResolver{},
		requireTenant:      true,
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
		tablePresence: newTablePresenceGuard(func(context.Context, string) (bool, error) {
			return false, sentinel
		}, time.Hour),
	}

	events, err := repo.ListPending(validTenantCtx(), 10)
	require.Nil(t, events)
	require.ErrorIs(t, err, sentinel)
}

// staticDiscoverer is a fixed outbox.TenantDiscoverer.
type staticDiscoverer struct {
	ids []string
}

func (d staticDiscoverer) DiscoverTenants(context.Context) ([]string, error) {
	return append([]string(nil), d.ids...), nil
}
