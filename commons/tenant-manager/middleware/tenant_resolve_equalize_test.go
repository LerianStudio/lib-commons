//go:build unit

package middleware

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/postgres"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubResolverDB implements dbresolver.DB for tests. PingContext returns nil so
// the manager treats the cached connection as healthy and skips the network.
type stubResolverDB struct{}

var _ dbresolver.DB = (*stubResolverDB)(nil)

func (*stubResolverDB) Begin() (dbresolver.Tx, error) { return nil, nil }
func (*stubResolverDB) BeginTx(context.Context, *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}
func (*stubResolverDB) Close() error                                          { return nil }
func (*stubResolverDB) Conn(context.Context) (dbresolver.Conn, error)         { return nil, nil }
func (*stubResolverDB) Driver() driver.Driver                                 { return nil }
func (*stubResolverDB) Exec(string, ...interface{}) (sql.Result, error)       { return nil, nil }
func (*stubResolverDB) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, nil
}
func (*stubResolverDB) Ping() error                                  { return nil }
func (*stubResolverDB) PingContext(context.Context) error            { return nil }
func (*stubResolverDB) Prepare(string) (dbresolver.Stmt, error)      { return nil, nil }
func (*stubResolverDB) PrepareContext(context.Context, string) (dbresolver.Stmt, error) {
	return nil, nil
}
func (*stubResolverDB) Query(string, ...interface{}) (*sql.Rows, error) { return nil, nil }
func (*stubResolverDB) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, nil
}
func (*stubResolverDB) QueryRow(string, ...interface{}) *sql.Row { return nil }
func (*stubResolverDB) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	return nil
}
func (*stubResolverDB) SetConnMaxIdleTime(time.Duration) {}
func (*stubResolverDB) SetConnMaxLifetime(time.Duration) {}
func (*stubResolverDB) SetMaxIdleConns(int)              {}
func (*stubResolverDB) SetMaxOpenConns(int)              {}
func (*stubResolverDB) PrimaryDBs() []*sql.DB            { return nil }
func (*stubResolverDB) ReplicaDBs() []*sql.DB            { return nil }
func (*stubResolverDB) Stats() sql.DBStats               { return sql.DBStats{} }

// newPGManagerWithCachedConn builds a fresh PG manager whose connection cache
// has been pre-populated with a working stub for the given tenantID. This lets
// resolvePostgres complete without HTTP/DB round-trips.
func newPGManagerWithCachedConn(t *testing.T, tenantID string) (*tmpostgres.Manager, dbresolver.DB) {
	t.Helper()

	c, err := client.NewClient("http://localhost:0", nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	// Disable async pool revalidation so the test does not spawn background goroutines.
	mgr := tmpostgres.NewManager(c, "ledger", tmpostgres.WithConnectionsCheckInterval(0))
	db := &stubResolverDB{}
	tmpostgres.InjectCachedConnectionForTest(mgr, tenantID, db)

	return mgr, db
}

// -----------------------------------------------------------------------------
// resolvePostgres — dual-mode equalization (the fix)
// -----------------------------------------------------------------------------

func TestResolvePostgres_DualMode_PopulatesGenericAndModuleKeys(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-dual"

	mgrGeneric, _ := newPGManagerWithCachedConn(t, tenantID)
	mgrModule, _ := newPGManagerWithCachedConn(t, tenantID)

	mw := NewTenantMiddleware(
		WithPG(mgrGeneric),                // single-manager registration
		WithPG(mgrModule, "systemplane"), // module-specific registration
	)

	// Sanity: registration state matches dual-mode invariant.
	require.NotNil(t, mw.postgres, "single-manager field must be populated")
	require.Len(t, mw.pgModules, 1, "module map must hold one entry")

	ctx, err := mw.resolvePostgres(context.Background(), tenantID)
	require.NoError(t, err)

	assert.NotNil(t, core.GetPGContext(ctx),
		"generic GetPGContext(ctx) must be non-nil in dual mode (regression pin for the bug fix)")
	assert.NotNil(t, core.GetPGContext(ctx, "systemplane"),
		"module-specific GetPGContext(ctx, \"systemplane\") must be non-nil in dual mode")
}

func TestResolvePostgres_PureSingleManager_OnlyGenericKey(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-single"

	mgr, _ := newPGManagerWithCachedConn(t, tenantID)
	mw := NewTenantMiddleware(WithPG(mgr))

	require.NotNil(t, mw.postgres)
	require.Empty(t, mw.pgModules)

	ctx, err := mw.resolvePostgres(context.Background(), tenantID)
	require.NoError(t, err)

	assert.NotNil(t, core.GetPGContext(ctx), "generic key must be set")
	assert.Nil(t, core.GetPGContext(ctx, "anything"), "module key must remain unset in pure single mode")
}

func TestResolvePostgres_PureMultiModule_OnlyModuleKeys(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-multi"

	mgrFoo, _ := newPGManagerWithCachedConn(t, tenantID)
	mgrBar, _ := newPGManagerWithCachedConn(t, tenantID)

	mw := NewTenantMiddleware(
		WithPG(mgrFoo, "foo"),
		WithPG(mgrBar, "bar"),
	)

	require.Nil(t, mw.postgres, "single-manager field must remain nil in pure multi-module mode")
	require.Len(t, mw.pgModules, 2)

	ctx, err := mw.resolvePostgres(context.Background(), tenantID)
	require.NoError(t, err)

	assert.Nil(t, core.GetPGContext(ctx),
		"generic key must remain nil in pure multi-module mode (no equalization without single-manager registration)")
	assert.NotNil(t, core.GetPGContext(ctx, "foo"), "module foo must be set")
	assert.NotNil(t, core.GetPGContext(ctx, "bar"), "module bar must be set")
}

func TestResolvePostgres_DualModeDifferentManagers_EachKeyResolvesToItsManager(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-dual-distinct"

	mgrGeneric, dbGeneric := newPGManagerWithCachedConn(t, tenantID)
	mgrModule, dbModule := newPGManagerWithCachedConn(t, tenantID)

	mw := NewTenantMiddleware(
		WithPG(mgrGeneric),
		WithPG(mgrModule, "foo"),
	)

	ctx, err := mw.resolvePostgres(context.Background(), tenantID)
	require.NoError(t, err)

	gotGeneric := core.GetPGContext(ctx)
	gotModule := core.GetPGContext(ctx, "foo")

	require.NotNil(t, gotGeneric)
	require.NotNil(t, gotModule)
	assert.Same(t, dbGeneric, gotGeneric, "generic key must resolve to the single-manager's connection")
	assert.Same(t, dbModule, gotModule, "module key must resolve to the module manager's connection")
}

func TestResolvePostgres_DualModeMultipleModules_GenericPlusAllModules(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-dual-many"

	mgrGeneric, _ := newPGManagerWithCachedConn(t, tenantID)
	mgrFoo, _ := newPGManagerWithCachedConn(t, tenantID)
	mgrBar, _ := newPGManagerWithCachedConn(t, tenantID)

	mw := NewTenantMiddleware(
		WithPG(mgrGeneric),
		WithPG(mgrFoo, "foo"),
		WithPG(mgrBar, "bar"),
	)

	ctx, err := mw.resolvePostgres(context.Background(), tenantID)
	require.NoError(t, err)

	assert.NotNil(t, core.GetPGContext(ctx), "generic key set by equalization")
	assert.NotNil(t, core.GetPGContext(ctx, "foo"))
	assert.NotNil(t, core.GetPGContext(ctx, "bar"))
}

// TestResolvePostgres_DualMode_GenericFailurePropagates verifies that when
// modules succeed but the single-manager GetConnection fails, the equalization
// step returns the dedicated "generic single-manager:" error (and does not
// silently mask the failure with the success of the module path).
func TestResolvePostgres_DualMode_GenericFailurePropagates(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-dual-generic-fail"

	// Module manager has a cached connection → succeeds.
	mgrModule, _ := newPGManagerWithCachedConn(t, tenantID)

	// Generic manager points at an HTTP backend that returns 500 → fails.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)
	mgrGeneric := tmpostgres.NewManager(c, "ledger", tmpostgres.WithConnectionsCheckInterval(0))

	mw := NewTenantMiddleware(
		WithPG(mgrGeneric),
		WithPG(mgrModule, "foo"),
	)

	ctx, err := mw.resolvePostgres(context.Background(), tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generic single-manager:",
		"dual-mode generic failure must be attributed to the single-manager step, not silently dropped")
	// On error, ctx is the original unmodified context — no keys leaked.
	assert.Nil(t, core.GetPGContext(ctx))
	assert.Nil(t, core.GetPGContext(ctx, "foo"))
}

// -----------------------------------------------------------------------------
// Mongo registration patterns: registration-state regression pin.
//
// The behavior fix in resolveMongo is structurally symmetric to resolvePostgres.
// A full behavioral assertion would require a real *mongo.Client whose Ping
// succeeds, which cannot be mocked without a running MongoDB instance — that is
// covered by integration tests. The registration-state test below pins the
// pre-condition for the equalization branch (both m.mongo and m.mongoModules
// populated simultaneously) so that a future refactor that removes one side of
// the dual-mode invariant will be caught here.
// -----------------------------------------------------------------------------

func TestWithMB_DualMode_BothFieldsPopulated(t *testing.T) {
	t.Parallel()

	_, mgrGeneric := newTestManagers(t)
	_, mgrModule := newTestManagers(t)

	mw := NewTenantMiddleware(
		WithMB(mgrGeneric),
		WithMB(mgrModule, "systemplane"),
	)

	require.NotNil(t, mw.mongo, "single-manager Mongo field must be populated")
	require.Len(t, mw.mongoModules, 1)
	assert.Same(t, mgrGeneric, mw.mongo)
	assert.Same(t, mgrModule, mw.mongoModules["systemplane"])
	assert.True(t, mw.Enabled())
}

// TestResolveMongo_DualMode_ModuleFailureShortCircuits confirms that, in dual
// mode, a module-level failure short-circuits before the equalization step (so
// the generic key is never populated on partial failure).
func TestResolveMongo_DualMode_ModuleFailureShortCircuits(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	mgr := tmmongo.NewManager(c, "ledger")
	mw := NewTenantMiddleware(
		WithMB(mgr),
		WithMB(mgr, "foo"),
	)

	ctx, err := mw.resolveMongo(context.Background(), "tenant-mb-dual")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "module foo:",
		"module-level failure must short-circuit before the equalization branch")
	assert.Nil(t, core.GetMBContext(ctx), "no keys leaked on early failure")
	assert.Nil(t, core.GetMBContext(ctx, "foo"))
}
