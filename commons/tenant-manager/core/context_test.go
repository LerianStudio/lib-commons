package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestSetTenantIDInContext(t *testing.T) {
	ctx := context.Background()

	ctx = SetTenantIDInContext(ctx, "tenant-123")

	assert.Equal(t, "tenant-123", GetTenantIDFromContext(ctx))
}

func TestGetTenantIDFromContext_NotSet(t *testing.T) {
	ctx := context.Background()

	id := GetTenantIDFromContext(ctx)

	assert.Equal(t, "", id)
}

func TestContextWithTenantID(t *testing.T) {
	ctx := context.Background()

	ctx = ContextWithTenantID(ctx, "tenant-456")

	assert.Equal(t, "tenant-456", GetTenantIDFromContext(ctx))
}

// mockPostgresFallback implements PostgresFallback for testing.
type mockPostgresFallback struct {
	db  dbresolver.DB
	err error
}

func (m *mockPostgresFallback) GetDB() (dbresolver.DB, error) {
	return m.db, m.err
}

// mockMongoFallback implements MongoFallback for testing.
type mockMongoFallback struct {
	client *mongo.Client
	err    error
}

func (m *mockMongoFallback) GetDB(_ context.Context) (*mongo.Client, error) {
	return m.client, m.err
}

// mockMultiTenantPostgresFallback implements both PostgresFallback and MultiTenantChecker.
type mockMultiTenantPostgresFallback struct {
	mockPostgresFallback
	multiTenant bool
}

func (m *mockMultiTenantPostgresFallback) IsMultiTenant() bool {
	return m.multiTenant
}

// mockMultiTenantMongoFallback implements both MongoFallback and MultiTenantChecker.
type mockMultiTenantMongoFallback struct {
	mockMongoFallback
	multiTenant bool
}

func (m *mockMultiTenantMongoFallback) IsMultiTenant() bool {
	return m.multiTenant
}

func TestResolvePostgres(t *testing.T) {
	t.Run("returns tenant DB from context when present", func(t *testing.T) {
		ctx := context.Background()
		tenantConn := &mockDB{name: "tenant-db"}
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockPostgresFallback{db: fallbackConn}

		ctx = ContextWithTenantPGConnection(ctx, tenantConn)
		db, err := ResolvePostgres(ctx, fallback)

		assert.NoError(t, err)
		assert.Equal(t, tenantConn, db)
	})

	t.Run("falls back to static connection when no tenant in context", func(t *testing.T) {
		ctx := context.Background()
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockPostgresFallback{db: fallbackConn}

		db, err := ResolvePostgres(ctx, fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})

	t.Run("returns fallback error when fallback fails", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockPostgresFallback{err: assert.AnError}

		db, err := ResolvePostgres(ctx, fallback)

		assert.Nil(t, db)
		assert.Error(t, err)
	})

	t.Run("returns ErrTenantContextRequired when multi-tenant and no connection in context", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMultiTenantPostgresFallback{
			mockPostgresFallback: mockPostgresFallback{db: &mockDB{name: "fallback-db"}},
			multiTenant:          true,
		}

		db, err := ResolvePostgres(ctx, fallback)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("returns context connection when multi-tenant and connection present", func(t *testing.T) {
		ctx := context.Background()
		tenantConn := &mockDB{name: "tenant-db"}
		fallback := &mockMultiTenantPostgresFallback{
			mockPostgresFallback: mockPostgresFallback{db: &mockDB{name: "fallback-db"}},
			multiTenant:          true,
		}

		ctx = ContextWithTenantPGConnection(ctx, tenantConn)
		db, err := ResolvePostgres(ctx, fallback)

		assert.NoError(t, err)
		assert.Equal(t, tenantConn, db)
	})

	t.Run("falls back normally when multi-tenant is false", func(t *testing.T) {
		ctx := context.Background()
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockMultiTenantPostgresFallback{
			mockPostgresFallback: mockPostgresFallback{db: fallbackConn},
			multiTenant:          false,
		}

		db, err := ResolvePostgres(ctx, fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})

	t.Run("falls back normally when fallback does not implement MultiTenantChecker", func(t *testing.T) {
		ctx := context.Background()
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockPostgresFallback{db: fallbackConn}

		db, err := ResolvePostgres(ctx, fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})
}

// mockDB implements dbresolver.DB interface for testing purposes.
type mockDB struct {
	name string
}

// Ensure mockDB implements dbresolver.DB interface.
var _ dbresolver.DB = (*mockDB)(nil)

func (m *mockDB) Begin() (dbresolver.Tx, error) { return nil, nil }
func (m *mockDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}
func (m *mockDB) Close() error                                               { return nil }
func (m *mockDB) Conn(ctx context.Context) (dbresolver.Conn, error)          { return nil, nil }
func (m *mockDB) Driver() driver.Driver                                      { return nil }
func (m *mockDB) Exec(query string, args ...interface{}) (sql.Result, error) { return nil, nil }
func (m *mockDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return nil, nil
}
func (m *mockDB) Ping() error                                   { return nil }
func (m *mockDB) PingContext(ctx context.Context) error         { return nil }
func (m *mockDB) Prepare(query string) (dbresolver.Stmt, error) { return nil, nil }
func (m *mockDB) PrepareContext(ctx context.Context, query string) (dbresolver.Stmt, error) {
	return nil, nil
}
func (m *mockDB) Query(query string, args ...interface{}) (*sql.Rows, error) { return nil, nil }
func (m *mockDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return nil, nil
}
func (m *mockDB) QueryRow(query string, args ...interface{}) *sql.Row { return nil }
func (m *mockDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return nil
}
func (m *mockDB) SetConnMaxIdleTime(d time.Duration) {}
func (m *mockDB) SetConnMaxLifetime(d time.Duration) {}
func (m *mockDB) SetMaxIdleConns(n int)              {}
func (m *mockDB) SetMaxOpenConns(n int)              {}
func (m *mockDB) PrimaryDBs() []*sql.DB              { return nil }
func (m *mockDB) ReplicaDBs() []*sql.DB              { return nil }
func (m *mockDB) Stats() sql.DBStats                 { return sql.DBStats{} }

func TestContextWithModulePGConnection(t *testing.T) {
	t.Run("stores and retrieves module connection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "module-db"}
		fallback := &mockPostgresFallback{db: &mockDB{name: "fallback-db"}}

		ctx = ContextWithModulePGConnection(ctx, "onboarding", mockConn)
		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, mockConn, db)
	})
}

func TestResolveModuleDB(t *testing.T) {
	t.Run("returns module DB from context when present", func(t *testing.T) {
		ctx := context.Background()
		moduleConn := &mockDB{name: "module-db"}
		fallback := &mockPostgresFallback{db: &mockDB{name: "fallback-db"}}

		ctx = ContextWithModulePGConnection(ctx, "onboarding", moduleConn)
		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, moduleConn, db)
	})

	t.Run("falls back to static connection when module not in context", func(t *testing.T) {
		ctx := context.Background()
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockPostgresFallback{db: fallbackConn}

		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})

	t.Run("does not cross modules", func(t *testing.T) {
		ctx := context.Background()
		txnConn := &mockDB{name: "transaction-db"}
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockPostgresFallback{db: fallbackConn}

		ctx = ContextWithModulePGConnection(ctx, "transaction", txnConn)
		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})

	t.Run("returns fallback error when fallback fails", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockPostgresFallback{err: assert.AnError}

		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.Nil(t, db)
		assert.Error(t, err)
	})

	t.Run("returns ErrTenantContextRequired when multi-tenant and no connection in context", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMultiTenantPostgresFallback{
			mockPostgresFallback: mockPostgresFallback{db: &mockDB{name: "fallback-db"}},
			multiTenant:          true,
		}

		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("returns context connection when multi-tenant and connection present", func(t *testing.T) {
		ctx := context.Background()
		moduleConn := &mockDB{name: "module-db"}
		fallback := &mockMultiTenantPostgresFallback{
			mockPostgresFallback: mockPostgresFallback{db: &mockDB{name: "fallback-db"}},
			multiTenant:          true,
		}

		ctx = ContextWithModulePGConnection(ctx, "onboarding", moduleConn)
		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, moduleConn, db)
	})

	t.Run("falls back normally when multi-tenant is false", func(t *testing.T) {
		ctx := context.Background()
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockMultiTenantPostgresFallback{
			mockPostgresFallback: mockPostgresFallback{db: fallbackConn},
			multiTenant:          false,
		}

		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})

	t.Run("falls back normally when fallback does not implement MultiTenantChecker", func(t *testing.T) {
		ctx := context.Background()
		fallbackConn := &mockDB{name: "fallback-db"}
		fallback := &mockPostgresFallback{db: fallbackConn}

		db, err := ResolveModuleDB(ctx, "onboarding", fallback)

		assert.NoError(t, err)
		assert.Equal(t, fallbackConn, db)
	})

	t.Run("works with arbitrary module names", func(t *testing.T) {
		ctx := context.Background()
		reportingConn := &mockDB{name: "reporting-db"}
		fallback := &mockPostgresFallback{db: &mockDB{name: "fallback-db"}}

		ctx = ContextWithModulePGConnection(ctx, "reporting", reportingConn)
		db, err := ResolveModuleDB(ctx, "reporting", fallback)

		assert.NoError(t, err)
		assert.Equal(t, reportingConn, db)
	})
}

func TestModuleConnectionIsolationGeneric(t *testing.T) {
	t.Run("multiple modules are isolated from each other", func(t *testing.T) {
		ctx := context.Background()
		onbConn := &mockDB{name: "onboarding-db"}
		txnConn := &mockDB{name: "transaction-db"}
		rptConn := &mockDB{name: "reporting-db"}
		fallback := &mockPostgresFallback{db: &mockDB{name: "fallback-db"}}

		ctx = ContextWithModulePGConnection(ctx, "onboarding", onbConn)
		ctx = ContextWithModulePGConnection(ctx, "transaction", txnConn)
		ctx = ContextWithModulePGConnection(ctx, "reporting", rptConn)

		onbDB, onbErr := ResolveModuleDB(ctx, "onboarding", fallback)
		txnDB, txnErr := ResolveModuleDB(ctx, "transaction", fallback)
		rptDB, rptErr := ResolveModuleDB(ctx, "reporting", fallback)

		assert.NoError(t, onbErr)
		assert.NoError(t, txnErr)
		assert.NoError(t, rptErr)
		assert.Equal(t, onbConn, onbDB)
		assert.Equal(t, txnConn, txnDB)
		assert.Equal(t, rptConn, rptDB)
	})

	t.Run("module connections are independent of generic connection", func(t *testing.T) {
		ctx := context.Background()
		genericConn := &mockDB{name: "generic-db"}
		moduleConn := &mockDB{name: "module-db"}
		fallback := &mockPostgresFallback{db: &mockDB{name: "fallback-db"}}

		ctx = ContextWithTenantPGConnection(ctx, genericConn)
		ctx = ContextWithModulePGConnection(ctx, "mymodule", moduleConn)

		genDB, genErr := ResolvePostgres(ctx, fallback)
		modDB, modErr := ResolveModuleDB(ctx, "mymodule", fallback)

		assert.NoError(t, genErr)
		assert.NoError(t, modErr)
		assert.Equal(t, genericConn, genDB)
		assert.Equal(t, moduleConn, modDB)
		assert.NotEqual(t, genDB, modDB)
	})
}

func TestGetMongoFromContext(t *testing.T) {
	t.Run("returns nil when no mongo in context", func(t *testing.T) {
		ctx := context.Background()

		db := GetMongoFromContext(ctx)

		assert.Nil(t, db)
	})

	t.Run("returns nil for nil mongo database stored in context", func(t *testing.T) {
		ctx := context.Background()

		var nilDB *mongo.Database
		ctx = ContextWithTenantMongo(ctx, nilDB)

		db := GetMongoFromContext(ctx)

		assert.Nil(t, db)
	})
}

func TestResolveMongo(t *testing.T) {
	t.Run("returns tenant mongo DB from context when present", func(t *testing.T) {
		ctx := context.Background()
		tenantDB := &mongo.Database{}
		fallback := &mockMongoFallback{err: assert.AnError}

		ctx = ContextWithTenantMongo(ctx, tenantDB)
		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.NoError(t, err)
		assert.Equal(t, tenantDB, db)
	})

	t.Run("falls back to static connection when no tenant in context", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMongoFallback{err: assert.AnError}

		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.Nil(t, db)
		assert.Error(t, err)
	})

	t.Run("falls back when nil mongo stored in context", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMongoFallback{err: assert.AnError}

		var nilDB *mongo.Database
		ctx = ContextWithTenantMongo(ctx, nilDB)

		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.Nil(t, db)
		assert.Error(t, err)
	})

	t.Run("returns ErrTenantContextRequired when multi-tenant and no connection in context", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMultiTenantMongoFallback{
			mockMongoFallback: mockMongoFallback{client: &mongo.Client{}},
			multiTenant:       true,
		}

		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("returns context connection when multi-tenant and connection present", func(t *testing.T) {
		ctx := context.Background()
		tenantDB := &mongo.Database{}
		fallback := &mockMultiTenantMongoFallback{
			mockMongoFallback: mockMongoFallback{client: &mongo.Client{}},
			multiTenant:       true,
		}

		ctx = ContextWithTenantMongo(ctx, tenantDB)
		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.NoError(t, err)
		assert.Equal(t, tenantDB, db)
	})

	t.Run("falls back normally when multi-tenant is false", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMultiTenantMongoFallback{
			mockMongoFallback: mockMongoFallback{err: assert.AnError},
			multiTenant:       false,
		}

		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.Nil(t, db)
		assert.Error(t, err)
	})

	t.Run("falls back normally when fallback does not implement MultiTenantChecker", func(t *testing.T) {
		ctx := context.Background()
		fallback := &mockMongoFallback{err: assert.AnError}

		db, err := ResolveMongo(ctx, fallback, "testdb")

		assert.Nil(t, db)
		assert.Error(t, err)
	})
}
