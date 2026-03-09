//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	libPostgres "github.com/LerianStudio/lib-commons/v4/commons/postgres"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/require"
)

type resolverProviderFunc func(context.Context) (dbresolver.DB, error)

func (fn resolverProviderFunc) Resolver(ctx context.Context) (dbresolver.DB, error) {
	return fn(ctx)
}

type fakeDBResolver struct {
	primary []*sql.DB
}

func (resolver fakeDBResolver) Begin() (dbresolver.Tx, error) { return nil, nil }

func (resolver fakeDBResolver) BeginTx(context.Context, *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}

func (resolver fakeDBResolver) Close() error { return nil }

func (resolver fakeDBResolver) Conn(context.Context) (dbresolver.Conn, error) { return nil, nil }

func (resolver fakeDBResolver) Driver() driver.Driver { return nil }

func (resolver fakeDBResolver) Exec(string, ...interface{}) (sql.Result, error) { return nil, nil }

func (resolver fakeDBResolver) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, nil
}

func (resolver fakeDBResolver) Ping() error { return nil }

func (resolver fakeDBResolver) PingContext(context.Context) error { return nil }

func (resolver fakeDBResolver) Prepare(string) (dbresolver.Stmt, error) { return nil, nil }

func (resolver fakeDBResolver) PrepareContext(context.Context, string) (dbresolver.Stmt, error) {
	return nil, nil
}

func (resolver fakeDBResolver) Query(string, ...interface{}) (*sql.Rows, error) { return nil, nil }

func (resolver fakeDBResolver) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, nil
}

func (resolver fakeDBResolver) QueryRow(string, ...interface{}) *sql.Row { return nil }

func (resolver fakeDBResolver) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	return nil
}

func (resolver fakeDBResolver) SetConnMaxIdleTime(time.Duration) {}

func (resolver fakeDBResolver) SetConnMaxLifetime(time.Duration) {}

func (resolver fakeDBResolver) SetMaxIdleConns(int) {}

func (resolver fakeDBResolver) SetMaxOpenConns(int) {}

func (resolver fakeDBResolver) PrimaryDBs() []*sql.DB { return resolver.primary }

func (resolver fakeDBResolver) ReplicaDBs() []*sql.DB { return nil }

func (resolver fakeDBResolver) Stats() sql.DBStats { return sql.DBStats{} }

func TestResolvePrimaryDB_NilClient(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), nil)
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrConnectionRequired)
}

func TestResolvePrimaryDB_NilContext(t *testing.T) {
	t.Parallel()

	client, err := libPostgres.New(libPostgres.Config{
		PrimaryDSN: "postgres://localhost:5432/postgres",
		ReplicaDSN: "postgres://localhost:5432/postgres",
	})
	require.NoError(t, err)

	db, err := resolvePrimaryDB(nil, client)
	require.Nil(t, db)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get database connection")
	require.True(t, errors.Is(err, libPostgres.ErrNilContext))
}

func TestResolvePrimaryDB_ResolverFailure(t *testing.T) {
	t.Parallel()

	client, err := libPostgres.New(libPostgres.Config{
		PrimaryDSN: "postgres://invalid:invalid@127.0.0.1:1/postgres",
		ReplicaDSN: "postgres://invalid:invalid@127.0.0.1:1/postgres",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	db, err := resolvePrimaryDB(ctx, client)
	require.Nil(t, db)
	require.ErrorContains(t, err, "failed to get database connection")
	require.NotErrorIs(t, err, ErrNoPrimaryDB)
	require.NotErrorIs(t, err, ErrConnectionRequired)
}

func TestResolvePrimaryDB_NilResolvedDB(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), resolverProviderFunc(func(context.Context) (dbresolver.DB, error) {
		return nil, nil
	}))
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrNoPrimaryDB)
}

func TestResolvePrimaryDB_EmptyPrimaryDBs(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), resolverProviderFunc(func(context.Context) (dbresolver.DB, error) {
		return fakeDBResolver{primary: []*sql.DB{}}, nil
	}))
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrNoPrimaryDB)
}

func TestResolvePrimaryDB_NilPrimaryDBEntry(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), resolverProviderFunc(func(context.Context) (dbresolver.DB, error) {
		return fakeDBResolver{primary: []*sql.DB{nil}}, nil
	}))
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrNoPrimaryDB)
}
