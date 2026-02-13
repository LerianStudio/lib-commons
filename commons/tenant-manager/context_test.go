package tenantmanager

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
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

func (m *mockDB) Begin() (dbresolver.Tx, error)                                        { return nil, nil }
func (m *mockDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}
func (m *mockDB) Close() error                                             { return nil }
func (m *mockDB) Conn(ctx context.Context) (dbresolver.Conn, error)        { return nil, nil }
func (m *mockDB) Driver() driver.Driver                                    { return nil }
func (m *mockDB) Exec(query string, args ...interface{}) (sql.Result, error) { return nil, nil }
func (m *mockDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return nil, nil
}
func (m *mockDB) Ping() error                  { return nil }
func (m *mockDB) PingContext(ctx context.Context) error { return nil }
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

func TestContextWithOnboardingPGConnection(t *testing.T) {
	t.Run("stores and retrieves onboarding connection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "onboarding-db"}

		ctx = ContextWithOnboardingPGConnection(ctx, mockConn)
		db, err := GetOnboardingPostgresForTenant(ctx)

		assert.NoError(t, err)
		assert.Equal(t, mockConn, db)
	})
}

func TestContextWithTransactionPGConnection(t *testing.T) {
	t.Run("stores and retrieves transaction connection", func(t *testing.T) {
		ctx := context.Background()
		mockConn := &mockDB{name: "transaction-db"}

		ctx = ContextWithTransactionPGConnection(ctx, mockConn)
		db, err := GetTransactionPostgresForTenant(ctx)

		assert.NoError(t, err)
		assert.Equal(t, mockConn, db)
	})
}

func TestGetOnboardingPostgresForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetOnboardingPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("does not fallback to generic connection", func(t *testing.T) {
		ctx := context.Background()
		genericConn := &mockDB{name: "generic-db"}

		// Set only the generic connection
		ctx = ContextWithTenantPGConnection(ctx, genericConn)

		// Onboarding getter should NOT find it
		db, err := GetOnboardingPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("does not fallback to transaction connection", func(t *testing.T) {
		ctx := context.Background()
		transactionConn := &mockDB{name: "transaction-db"}

		// Set only the transaction connection
		ctx = ContextWithTransactionPGConnection(ctx, transactionConn)

		// Onboarding getter should NOT find it
		db, err := GetOnboardingPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}

func TestGetTransactionPostgresForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetTransactionPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("does not fallback to generic connection", func(t *testing.T) {
		ctx := context.Background()
		genericConn := &mockDB{name: "generic-db"}

		// Set only the generic connection
		ctx = ContextWithTenantPGConnection(ctx, genericConn)

		// Transaction getter should NOT find it
		db, err := GetTransactionPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("does not fallback to onboarding connection", func(t *testing.T) {
		ctx := context.Background()
		onboardingConn := &mockDB{name: "onboarding-db"}

		// Set only the onboarding connection
		ctx = ContextWithOnboardingPGConnection(ctx, onboardingConn)

		// Transaction getter should NOT find it
		db, err := GetTransactionPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}

func TestModuleConnectionIsolation(t *testing.T) {
	t.Run("setting one module connection does not affect the other", func(t *testing.T) {
		ctx := context.Background()
		onboardingConn := &mockDB{name: "onboarding-db"}
		transactionConn := &mockDB{name: "transaction-db"}

		// Set both connections
		ctx = ContextWithOnboardingPGConnection(ctx, onboardingConn)
		ctx = ContextWithTransactionPGConnection(ctx, transactionConn)

		// Each getter should return its own connection
		onbDB, onbErr := GetOnboardingPostgresForTenant(ctx)
		txnDB, txnErr := GetTransactionPostgresForTenant(ctx)

		assert.NoError(t, onbErr)
		assert.NoError(t, txnErr)
		assert.Equal(t, onboardingConn, onbDB)
		assert.Equal(t, transactionConn, txnDB)

		// Verify they are different
		assert.NotEqual(t, onbDB, txnDB)
	})

	t.Run("module connections are independent of generic connection", func(t *testing.T) {
		ctx := context.Background()
		genericConn := &mockDB{name: "generic-db"}
		onboardingConn := &mockDB{name: "onboarding-db"}
		transactionConn := &mockDB{name: "transaction-db"}

		// Set all three connections
		ctx = ContextWithTenantPGConnection(ctx, genericConn)
		ctx = ContextWithOnboardingPGConnection(ctx, onboardingConn)
		ctx = ContextWithTransactionPGConnection(ctx, transactionConn)

		// Generic getter returns generic connection
		genDB, genErr := GetPostgresForTenant(ctx)
		assert.NoError(t, genErr)
		assert.Equal(t, genericConn, genDB)

		// Module getters return their specific connections
		onbDB, onbErr := GetOnboardingPostgresForTenant(ctx)
		assert.NoError(t, onbErr)
		assert.Equal(t, onboardingConn, onbDB)

		txnDB, txnErr := GetTransactionPostgresForTenant(ctx)
		assert.NoError(t, txnErr)
		assert.Equal(t, transactionConn, txnDB)

		// All three are different
		assert.NotEqual(t, genDB, onbDB)
		assert.NotEqual(t, genDB, txnDB)
		assert.NotEqual(t, onbDB, txnDB)
	})
}
