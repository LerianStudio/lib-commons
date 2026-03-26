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

func TestGetTenantPGConnectionFromContext(t *testing.T) {
	t.Run("returns nil when no PG connection in context", func(t *testing.T) {
		ctx := context.Background()

		db := GetTenantPGConnectionFromContext(ctx)

		assert.Nil(t, db)
	})

	t.Run("returns connection when set via ContextWithTenantPGConnection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "tenant-db"}

		ctx = ContextWithTenantPGConnection(ctx, mockConn)
		db := GetTenantPGConnectionFromContext(ctx)

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
		ctx = ContextWithTenantMongo(ctx, nilDB)

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

	t.Run("ContextWithTenantPGConnection with nil context does not panic", func(t *testing.T) {
		mockConn := &mockDB{name: "test-db"}

		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithTenantPGConnection(nil, mockConn)

		assert.Equal(t, mockConn, GetTenantPGConnectionFromContext(ctx))
	})

	t.Run("GetTenantPGConnectionFromContext with nil context returns nil", func(t *testing.T) {
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		db := GetTenantPGConnectionFromContext(nil)

		assert.Nil(t, db)
	})

	t.Run("ContextWithTenantMongo with nil context does not panic", func(t *testing.T) {
		// We cannot create a real *mongo.Database without a live client,
		// but we can verify nil context does not panic with a nil DB value.
		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety guard
		ctx := ContextWithTenantMongo(nil, nil)

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
