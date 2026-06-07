//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/require"
)

// stubConnManager is a tenantConnectionManager seam used to assert the
// default-tenant branch never reaches Tenant Manager.
type stubConnManager struct {
	t          *testing.T
	failIfUsed bool
	db         *sql.DB
	err        error
	gotID      string
}

func (m *stubConnManager) GetConnection(_ context.Context, tenantID string) (tenantPoolConnection, error) {
	if m.failIfUsed {
		m.t.Fatalf("GetConnection must not be called for default tenant, got %q", tenantID)
	}

	m.gotID = tenantID

	if m.err != nil {
		return nil, m.err
	}

	return stubPoolConnection{db: m.db}, nil
}

type stubPoolConnection struct {
	db  *sql.DB
	err error
}

func (c stubPoolConnection) GetDB() (dbresolver.DB, error) {
	if c.err != nil {
		return nil, c.err
	}

	return fakeDBResolver{primary: []*sql.DB{c.db}}, nil
}

// stubTenantLister is a tenantServiceLister seam over the TM client.
type stubTenantLister struct {
	ids []string
	err error
}

func (l stubTenantLister) listTenantIDs(_ context.Context, _ string) ([]string, error) {
	if l.err != nil {
		return nil, l.err
	}

	return append([]string(nil), l.ids...), nil
}

const testDefaultTenant = "11111111-1111-1111-1111-111111111111"

func newTestPoolResolver(mgr tenantConnectionManager, lister tenantServiceLister, defaultTenant string) *ManagerPoolResolver {
	return &ManagerPoolResolver{
		manager:         mgr,
		lister:          lister,
		service:         "transaction",
		defaultTenantID: defaultTenant,
	}
}

func TestManagerPoolResolver_PoolForTenant_DefaultTenantUsesRootNeverManager(t *testing.T) {
	t.Parallel()

	rootDB := &sql.DB{}
	root := resolverProviderFunc(func(context.Context) (dbresolver.DB, error) {
		return fakeDBResolver{primary: []*sql.DB{rootDB}}, nil
	})

	mgr := &stubConnManager{t: t, failIfUsed: true}

	resolver := newTestPoolResolver(mgr, stubTenantLister{}, testDefaultTenant)
	resolver.rootClient = root

	db, err := resolver.PoolForTenant(context.Background(), testDefaultTenant)
	require.NoError(t, err)
	require.Same(t, rootDB, db)
}

func TestManagerPoolResolver_PoolForTenant_NonDefaultUsesManager(t *testing.T) {
	t.Parallel()

	tenantDB := &sql.DB{}
	mgr := &stubConnManager{t: t, db: tenantDB}

	resolver := newTestPoolResolver(mgr, stubTenantLister{}, testDefaultTenant)

	db, err := resolver.PoolForTenant(context.Background(), "22222222-2222-2222-2222-222222222222")
	require.NoError(t, err)
	require.Same(t, tenantDB, db)
	require.Equal(t, "22222222-2222-2222-2222-222222222222", mgr.gotID)
}

func TestManagerPoolResolver_PoolForTenant_ManagerErrorPropagates(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("tenant deprovisioned")
	mgr := &stubConnManager{t: t, err: sentinel}

	resolver := newTestPoolResolver(mgr, stubTenantLister{}, testDefaultTenant)

	db, err := resolver.PoolForTenant(context.Background(), "33333333-3333-3333-3333-333333333333")
	require.Nil(t, db)
	require.ErrorIs(t, err, sentinel)
}

func TestManagerPoolResolver_PoolForTenant_NilGetDBResult(t *testing.T) {
	t.Parallel()

	// GetDB returns a resolver whose PrimaryDBs is empty -> fail closed.
	mgr := &stubConnManager{t: t, db: nil}

	resolver := newTestPoolResolver(mgr, stubTenantLister{}, testDefaultTenant)

	db, err := resolver.PoolForTenant(context.Background(), "44444444-4444-4444-4444-444444444444")
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrNoPrimaryDB)
}

func TestManagerPoolResolver_PoolForTenant_EmptyTenantFailsClosed(t *testing.T) {
	t.Parallel()

	mgr := &stubConnManager{t: t, failIfUsed: true}
	resolver := newTestPoolResolver(mgr, stubTenantLister{}, testDefaultTenant)

	db, err := resolver.PoolForTenant(context.Background(), "")
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)
}

func TestManagerPoolResolver_ListTenants_AppendsDefaultWhenAbsent(t *testing.T) {
	t.Parallel()

	lister := stubTenantLister{ids: []string{
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
	}}

	resolver := newTestPoolResolver(&stubConnManager{t: t}, lister, testDefaultTenant)

	tenants, err := resolver.ListTenants(context.Background())
	require.NoError(t, err)
	require.Contains(t, tenants, testDefaultTenant)
	require.Len(t, tenants, 3)
}

func TestManagerPoolResolver_ListTenants_DefaultAlreadyPresentNotDuplicated(t *testing.T) {
	t.Parallel()

	lister := stubTenantLister{ids: []string{
		testDefaultTenant,
		"22222222-2222-2222-2222-222222222222",
	}}

	resolver := newTestPoolResolver(&stubConnManager{t: t}, lister, testDefaultTenant)

	tenants, err := resolver.ListTenants(context.Background())
	require.NoError(t, err)
	require.Len(t, tenants, 2)

	count := 0
	for _, id := range tenants {
		if id == testDefaultTenant {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestManagerPoolResolver_ListTenants_ErrorWrapped(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("tenant manager down")
	resolver := newTestPoolResolver(&stubConnManager{t: t}, stubTenantLister{err: sentinel}, testDefaultTenant)

	tenants, err := resolver.ListTenants(context.Background())
	require.Nil(t, tenants)
	require.ErrorIs(t, err, sentinel)
	require.ErrorContains(t, err, "list active tenants")
}

func TestNewManagerPoolResolver_PublicNilArgs(t *testing.T) {
	t.Parallel()

	root := resolverProviderFunc(func(context.Context) (dbresolver.DB, error) {
		return fakeDBResolver{primary: []*sql.DB{{}}}, nil
	})

	// Public constructor validates the concrete tenant-manager handles before
	// delegating to the seam-friendly constructor.
	_, err := NewManagerPoolResolver(nil, nil, root, "transaction", testDefaultTenant)
	require.ErrorIs(t, err, ErrManagerRequired)
}

func TestNewManagerPoolResolver_Validation(t *testing.T) {
	t.Parallel()

	root := resolverProviderFunc(func(context.Context) (dbresolver.DB, error) {
		return fakeDBResolver{primary: []*sql.DB{{}}}, nil
	})

	_, err := newManagerPoolResolver(nil, stubTenantLister{}, root, "transaction", testDefaultTenant)
	require.ErrorIs(t, err, ErrManagerRequired)

	// nil rootClient.
	_, err = newManagerPoolResolver(&stubConnManager{t: t}, stubTenantLister{}, nil, "transaction", testDefaultTenant)
	require.ErrorIs(t, err, ErrConnectionRequired)

	_, err = newManagerPoolResolver(&stubConnManager{t: t}, stubTenantLister{}, root, "  ", testDefaultTenant)
	require.ErrorIs(t, err, ErrServiceRequired)

	_, err = newManagerPoolResolver(&stubConnManager{t: t}, stubTenantLister{}, root, "transaction", "")
	require.ErrorIs(t, err, ErrDefaultTenantIDRequired)

	_, err = newManagerPoolResolver(&stubConnManager{t: t}, stubTenantLister{}, root, "transaction", "bad tenant id!!")
	require.ErrorIs(t, err, ErrInvalidDefaultTenantID)

	resolver, err := newManagerPoolResolver(&stubConnManager{t: t}, stubTenantLister{}, root, "transaction", testDefaultTenant)
	require.NoError(t, err)
	require.NotNil(t, resolver)
}
