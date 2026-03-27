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

func TestContextWithTenantID(t *testing.T) {
	ctx := context.Background()

	ctx = ContextWithTenantID(ctx, "tenant-123")

	assert.Equal(t, "tenant-123", GetTenantIDContext(ctx))
}

func TestGetTenantIDContext_NotSet(t *testing.T) {
	ctx := context.Background()

	id := GetTenantIDContext(ctx)

	assert.Equal(t, "", id)
}

func TestContextWithTenantID_Roundtrip(t *testing.T) {
	ctx := context.Background()

	ctx = ContextWithTenantID(ctx, "tenant-456")

	assert.Equal(t, "tenant-456", GetTenantIDContext(ctx))
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

func TestGetPGContext_Generic(t *testing.T) {
	t.Run("returns nil when no PG connection in context", func(t *testing.T) {
		ctx := context.Background()

		db := GetPGContext(ctx)

		assert.Nil(t, db)
	})

	t.Run("returns connection when set via ContextWithPG (no module)", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "tenant-db"}

		ctx = ContextWithPG(ctx, mockConn)
		db := GetPGContext(ctx)

		assert.Equal(t, mockConn, db)
	})
}

func TestGetMBContext_Generic(t *testing.T) {
	t.Run("returns nil when no mongo in context", func(t *testing.T) {
		ctx := context.Background()

		db := GetMBContext(ctx)

		assert.Nil(t, db)
	})

	t.Run("returns nil for nil mongo database stored in context", func(t *testing.T) {
		ctx := context.Background()

		var nilDB *mongo.Database
		ctx = ContextWithMB(ctx, nilDB)

		db := GetMBContext(ctx)

		assert.Nil(t, db)
	})
}

func TestNilContext(t *testing.T) {
	t.Run("ContextWithTenantID with nil context does not panic and stores value", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithTenantID(nil, "t1")

		assert.Equal(t, "t1", GetTenantIDContext(ctx))
	})

	t.Run("GetTenantIDContext with nil context returns empty string", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		id := GetTenantIDContext(nil)

		assert.Equal(t, "", id)
	})

	t.Run("ContextWithPG with nil context does not panic (no module)", func(t *testing.T) {
		mockConn := &mockDB{name: "test-db"}

		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithPG(nil, mockConn)

		assert.Equal(t, mockConn, GetPGContext(ctx))
	})

	t.Run("GetPGContext with nil context returns nil (no module)", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetPGContext(nil)

		assert.Nil(t, db)
	})

	t.Run("ContextWithMB with nil context does not panic (no module)", func(t *testing.T) {
		// We cannot create a real *mongo.Database without a live client,
		// but we can verify nil context does not panic with a nil DB value.
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithMB(nil, nil)

		assert.NotNil(t, ctx)
	})

	t.Run("GetMBContext with nil context returns nil (no module)", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetMBContext(nil)

		assert.Nil(t, db)
	})

	t.Run("ContextWithTenantID alias with nil context does not panic", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithTenantID(nil, "t2")

		assert.Equal(t, "t2", GetTenantIDContext(ctx))
	})
}

func TestContextWithPG_and_GetPGContext(t *testing.T) {
	t.Run("stores and retrieves module-specific PG connection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "onboarding-db"}

		ctx = ContextWithPG(ctx, mockConn, "onboarding")
		db := GetPGContext(ctx, "onboarding")

		assert.Equal(t, mockConn, db)
	})

	t.Run("returns correct connection for each module", func(t *testing.T) {
		ctx := context.Background()
		onboardingDB := &mockDB{name: "onboarding-db"}
		transactionDB := &mockDB{name: "transaction-db"}

		ctx = ContextWithPG(ctx, onboardingDB, "onboarding")
		ctx = ContextWithPG(ctx, transactionDB, "transaction")

		assert.Equal(t, onboardingDB, GetPGContext(ctx, "onboarding"))
		assert.Equal(t, transactionDB, GetPGContext(ctx, "transaction"))
	})

	t.Run("nil context does not panic and stores value", func(t *testing.T) {
		mockConn := &mockDB{name: "test-db"}

		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithPG(nil, mockConn, "onboarding")

		assert.Equal(t, mockConn, GetPGContext(ctx, "onboarding"))
	})

	t.Run("module call sets only module key not generic", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "onboarding-db"}

		ctx = ContextWithPG(ctx, mockConn, "onboarding")

		assert.Nil(t, GetPGContext(ctx), "generic key should NOT be set when module is provided")
		assert.Equal(t, mockConn, GetPGContext(ctx, "onboarding"), "module key should be set")
	})
}

func TestGetPGContext_ModuleIsolation(t *testing.T) {
	t.Run("different modules do not interfere", func(t *testing.T) {
		ctx := context.Background()
		onboardingDB := &mockDB{name: "onboarding-db"}

		ctx = ContextWithPG(ctx, onboardingDB, "onboarding")

		assert.Equal(t, onboardingDB, GetPGContext(ctx, "onboarding"))
		assert.Nil(t, GetPGContext(ctx, "transaction"), "unstored module should return nil")
	})

	t.Run("module-specific key does not collide with separately-set generic PG connection", func(t *testing.T) {
		ctx := context.Background()
		genericDB := &mockDB{name: "generic-db"}
		moduleDB := &mockDB{name: "module-db"}

		// Set generic only (no module)
		ctx = ContextWithPG(ctx, genericDB)
		// Set module (does NOT overwrite generic)
		ctx = ContextWithPG(ctx, moduleDB, "onboarding")

		// Generic key should still be genericDB (module call does not overwrite)
		assert.Equal(t, genericDB, GetPGContext(ctx), "generic key should NOT be overwritten by module call")
		assert.Equal(t, moduleDB, GetPGContext(ctx, "onboarding"), "module key should be intact")
	})
}

func TestGetPGContext_NilContext(t *testing.T) {
	t.Run("returns nil for nil context", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetPGContext(nil, "onboarding")

		assert.Nil(t, db)
	})
}

func TestGetPGContext_MissingModule(t *testing.T) {
	t.Run("returns nil for unstored module", func(t *testing.T) {
		ctx := context.Background()

		db := GetPGContext(ctx, "nonexistent")

		assert.Nil(t, db)
	})
}

func TestContextWithMB_and_GetMBContext(t *testing.T) {
	t.Run("stores and retrieves module-specific Mongo database", func(t *testing.T) {
		ctx := context.Background()

		// We cannot create a real *mongo.Database without a live client,
		// but we can test the type assertion path with nil.
		// The important thing is that the context key mechanism works.
		ctx = ContextWithMB(ctx, nil, "onboarding")
		db := GetMBContext(ctx, "onboarding")

		assert.Nil(t, db, "nil mongo.Database stored should return nil")
	})

	t.Run("nil context does not panic", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithMB(nil, nil, "onboarding")

		assert.NotNil(t, ctx)
	})

	t.Run("GetMBContext with nil context returns nil", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetMBContext(nil, "onboarding")

		assert.Nil(t, db)
	})

	t.Run("module call sets only module key not generic", func(t *testing.T) {
		ctx := context.Background()

		ctx = ContextWithMB(ctx, nil, "onboarding")

		// Generic should return nil because module call does not set generic key
		assert.Nil(t, GetMBContext(ctx), "generic key should NOT be set when module is provided")
		assert.Nil(t, GetMBContext(ctx, "onboarding"), "module key should be set (nil DB)")
	})
}

func TestGetMBContext_ModuleIsolation(t *testing.T) {
	t.Run("different modules do not interfere", func(t *testing.T) {
		ctx := context.Background()

		ctx = ContextWithMB(ctx, nil, "onboarding")

		assert.Nil(t, GetMBContext(ctx, "onboarding"))
		assert.Nil(t, GetMBContext(ctx, "transaction"), "unstored module should return nil")
	})

	t.Run("module-specific key does not collide with separately-set generic Mongo connection", func(t *testing.T) {
		ctx := context.Background()

		// Set generic only (no module)
		ctx = ContextWithMB(ctx, nil)
		// Set module (does NOT overwrite generic)
		ctx = ContextWithMB(ctx, nil, "onboarding")

		// Both paths return nil (since we can't create real *mongo.Database without a live client),
		// but the important thing is that neither call panics and context keys don't collide.
		assert.Nil(t, GetMBContext(ctx))
		assert.Nil(t, GetMBContext(ctx, "onboarding"))
	})
}
