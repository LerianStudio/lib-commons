//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	libPostgres "github.com/LerianStudio/lib-commons/v4/commons/postgres"
	"github.com/stretchr/testify/require"
)

type primaryDBProviderFunc func() (*sql.DB, error)

func (fn primaryDBProviderFunc) Primary() (*sql.DB, error) {
	return fn()
}

func TestResolvePrimaryDB_NilClient(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), nil)
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrConnectionRequired)
}

func TestResolvePrimaryDB_PrimaryFailure(t *testing.T) {
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
	require.True(t, errors.Is(err, libPostgres.ErrNotConnected))
}

func TestResolvePrimaryDB_NilPrimaryDB(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), primaryDBProviderFunc(func() (*sql.DB, error) {
		return nil, nil
	}))
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrNoPrimaryDB)
}

func TestResolvePrimaryDB_ReturnsPrimaryDB(t *testing.T) {
	t.Parallel()

	expected := &sql.DB{}
	db, err := resolvePrimaryDB(context.Background(), primaryDBProviderFunc(func() (*sql.DB, error) {
		return expected, nil
	}))
	require.NoError(t, err)
	require.Same(t, expected, db)
}
