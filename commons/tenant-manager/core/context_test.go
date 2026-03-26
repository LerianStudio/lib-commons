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

func TestGetPGConnectionFromContext(t *testing.T) {
	t.Run("returns nil when no PG connection in context", func(t *testing.T) {
		ctx := context.Background()

		db := GetPGConnectionFromContext(ctx)

		assert.Nil(t, db)
	})

	t.Run("returns connection when set via ContextWithPGConnection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "tenant-db"}

		ctx = ContextWithPGConnection(ctx, mockConn)
		db := GetPGConnectionFromContext(ctx)

		assert.Equal(t, mockConn, db)
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
		ctx = ContextWithMongo(ctx, nilDB)

		db := GetMongoFromContext(ctx)

		assert.Nil(t, db)
	})
}

func TestNilContext(t *testing.T) {
	t.Run("SetTenantIDInContext with nil context does not panic and stores value", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := SetTenantIDInContext(nil, "t1")

		assert.Equal(t, "t1", GetTenantIDFromContext(ctx))
	})

	t.Run("GetTenantIDFromContext with nil context returns empty string", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		id := GetTenantIDFromContext(nil)

		assert.Equal(t, "", id)
	})

	t.Run("ContextWithPGConnection with nil context does not panic", func(t *testing.T) {
		mockConn := &mockDB{name: "test-db"}

		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithPGConnection(nil, mockConn)

		assert.Equal(t, mockConn, GetPGConnectionFromContext(ctx))
	})

	t.Run("GetPGConnectionFromContext with nil context returns nil", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetPGConnectionFromContext(nil)

		assert.Nil(t, db)
	})

	t.Run("ContextWithMongo with nil context does not panic", func(t *testing.T) {
		// We cannot create a real *mongo.Database without a live client,
		// but we can verify nil context does not panic with a nil DB value.
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithMongo(nil, nil)

		assert.NotNil(t, ctx)
	})

	t.Run("GetMongoFromContext with nil context returns nil", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetMongoFromContext(nil)

		assert.Nil(t, db)
	})

	t.Run("GetTenantID alias with nil context returns empty string", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		id := GetTenantID(nil)

		assert.Equal(t, "", id)
	})

	t.Run("ContextWithTenantID alias with nil context does not panic", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithTenantID(nil, "t2")

		assert.Equal(t, "t2", GetTenantIDFromContext(ctx))
	})

	t.Run("GetPostgresForTenant with nil context returns error", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db, err := GetPostgresForTenant(nil)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("GetMongoForTenant with nil context returns error", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db, err := GetMongoForTenant(nil)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

}

func TestContextWithPG_and_GetPG(t *testing.T) {
	t.Run("stores and retrieves module-specific PG connection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "onboarding-db"}

		ctx = ContextWithPG(ctx, "onboarding", mockConn)
		db := GetPG(ctx, "onboarding")

		assert.Equal(t, mockConn, db)
	})

	t.Run("returns correct connection for each module", func(t *testing.T) {
		ctx := context.Background()
		onboardingDB := &mockDB{name: "onboarding-db"}
		transactionDB := &mockDB{name: "transaction-db"}

		ctx = ContextWithPG(ctx, "onboarding", onboardingDB)
		ctx = ContextWithPG(ctx, "transaction", transactionDB)

		assert.Equal(t, onboardingDB, GetPG(ctx, "onboarding"))
		assert.Equal(t, transactionDB, GetPG(ctx, "transaction"))
	})

	t.Run("nil context does not panic and stores value", func(t *testing.T) {
		mockConn := &mockDB{name: "test-db"}

		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithPG(nil, "onboarding", mockConn)

		assert.Equal(t, mockConn, GetPG(ctx, "onboarding"))
	})
}

func TestGetPG_ModuleIsolation(t *testing.T) {
	t.Run("different modules do not interfere", func(t *testing.T) {
		ctx := context.Background()
		onboardingDB := &mockDB{name: "onboarding-db"}

		ctx = ContextWithPG(ctx, "onboarding", onboardingDB)

		assert.Equal(t, onboardingDB, GetPG(ctx, "onboarding"))
		assert.Nil(t, GetPG(ctx, "transaction"), "unstored module should return nil")
	})

	t.Run("module-specific key does not collide with generic PG connection", func(t *testing.T) {
		ctx := context.Background()
		genericDB := &mockDB{name: "generic-db"}
		moduleDB := &mockDB{name: "module-db"}

		ctx = ContextWithPGConnection(ctx, genericDB)
		ctx = ContextWithPG(ctx, "onboarding", moduleDB)

		assert.Equal(t, genericDB, GetPGConnectionFromContext(ctx), "generic key should be intact")
		assert.Equal(t, moduleDB, GetPG(ctx, "onboarding"), "module key should be intact")
	})
}

func TestGetPG_NilContext(t *testing.T) {
	t.Run("returns nil for nil context", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetPG(nil, "onboarding")

		assert.Nil(t, db)
	})
}

func TestGetPG_MissingModule(t *testing.T) {
	t.Run("returns nil for unstored module", func(t *testing.T) {
		ctx := context.Background()

		db := GetPG(ctx, "nonexistent")

		assert.Nil(t, db)
	})
}

func TestContextWithMB_and_GetMB(t *testing.T) {
	t.Run("stores and retrieves module-specific Mongo database", func(t *testing.T) {
		ctx := context.Background()

		// We cannot create a real *mongo.Database without a live client,
		// but we can test the type assertion path with nil.
		// The important thing is that the context key mechanism works.
		ctx = ContextWithMB(ctx, "onboarding", nil)
		db := GetMB(ctx, "onboarding")

		assert.Nil(t, db, "nil mongo.Database stored should return nil")
	})

	t.Run("nil context does not panic", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithMB(nil, "onboarding", nil)

		assert.NotNil(t, ctx)
	})

	t.Run("GetMB with nil context returns nil", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetMB(nil, "onboarding")

		assert.Nil(t, db)
	})
}

func TestGetMB_ModuleIsolation(t *testing.T) {
	t.Run("different modules do not interfere", func(t *testing.T) {
		ctx := context.Background()

		ctx = ContextWithMB(ctx, "onboarding", nil)

		assert.Nil(t, GetMB(ctx, "onboarding"))
		assert.Nil(t, GetMB(ctx, "transaction"), "unstored module should return nil")
	})

	t.Run("module-specific key does not collide with generic Mongo connection", func(t *testing.T) {
		ctx := context.Background()

		ctx = ContextWithMongo(ctx, nil)
		ctx = ContextWithMB(ctx, "onboarding", nil)

		// Both paths return nil (since we can't create real *mongo.Database without a live client),
		// but the important thing is that neither call panics and context keys don't collide.
		assert.Nil(t, GetMongoFromContext(ctx))
		assert.Nil(t, GetMB(ctx, "onboarding"))
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

		// Use ContextWithMongo with a nil *mongo.Database to test the path
		// (We cannot create a real *mongo.Database without a live client,
		// but we can test the nil path and the type assertion path.)
		var nilDB *mongo.Database
		ctx = ContextWithMongo(ctx, nilDB)

		// nil *mongo.Database stored in context: type assertion succeeds but value is nil
		db := GetMongoFromContext(ctx)
		assert.Nil(t, db)

		// GetMongoForTenant should return error for nil db
		result, err := GetMongoForTenant(ctx)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}
