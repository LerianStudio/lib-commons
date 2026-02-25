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

func TestGetPostgresForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
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

		ctx = ContextWithModulePGConnection(ctx, "onboarding", mockConn)
		db, err := GetModulePostgresForTenant(ctx, "onboarding")

		assert.NoError(t, err)
		assert.Equal(t, mockConn, db)
	})
}

func TestGetModulePostgresForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetModulePostgresForTenant(ctx, "onboarding")

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("does not fallback to generic connection", func(t *testing.T) {
		ctx := context.Background()
		genericConn := &mockDB{name: "generic-db"}

		ctx = ContextWithTenantPGConnection(ctx, genericConn)

		db, err := GetModulePostgresForTenant(ctx, "onboarding")

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("does not fallback to other module connection", func(t *testing.T) {
		ctx := context.Background()
		txnConn := &mockDB{name: "transaction-db"}

		ctx = ContextWithModulePGConnection(ctx, "transaction", txnConn)

		db, err := GetModulePostgresForTenant(ctx, "onboarding")

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("works with arbitrary module names", func(t *testing.T) {
		ctx := context.Background()
		reportingConn := &mockDB{name: "reporting-db"}

		ctx = ContextWithModulePGConnection(ctx, "reporting", reportingConn)
		db, err := GetModulePostgresForTenant(ctx, "reporting")

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

		ctx = ContextWithModulePGConnection(ctx, "onboarding", onbConn)
		ctx = ContextWithModulePGConnection(ctx, "transaction", txnConn)
		ctx = ContextWithModulePGConnection(ctx, "reporting", rptConn)

		onbDB, onbErr := GetModulePostgresForTenant(ctx, "onboarding")
		txnDB, txnErr := GetModulePostgresForTenant(ctx, "transaction")
		rptDB, rptErr := GetModulePostgresForTenant(ctx, "reporting")

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

		ctx = ContextWithTenantPGConnection(ctx, genericConn)
		ctx = ContextWithModulePGConnection(ctx, "mymodule", moduleConn)

		genDB, genErr := GetPostgresForTenant(ctx)
		modDB, modErr := GetModulePostgresForTenant(ctx, "mymodule")

		assert.NoError(t, genErr)
		assert.NoError(t, modErr)
		assert.Equal(t, genericConn, genDB)
		assert.Equal(t, moduleConn, modDB)
		assert.NotEqual(t, genDB, modDB)
	})
}

func TestContextWithTenantMongo(t *testing.T) {
	t.Run("returns nil when no mongo in context", func(t *testing.T) {
		ctx := context.Background()

		db := GetMongoFromContext(ctx)

		assert.Nil(t, db)
	})
}

func TestGetMongoForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetMongoForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("returns ErrTenantContextRequired for nil db in context", func(t *testing.T) {
		ctx := context.Background()

		// Use ContextWithTenantMongo with a nil *mongo.Database to test the path
		// (We cannot create a real *mongo.Database without a live client,
		// but we can test the nil path and the type assertion path.)
		var nilDB *mongo.Database
		ctx = ContextWithTenantMongo(ctx, nilDB)

		// nil *mongo.Database stored in context: type assertion succeeds but value is nil
		db := GetMongoFromContext(ctx)
		assert.Nil(t, db)

		// GetMongoForTenant should return error for nil db
		result, err := GetMongoForTenant(ctx)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}
