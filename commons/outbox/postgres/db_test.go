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

type primaryDBProviderFunc func() (*sql.DB, error)

func (fn primaryDBProviderFunc) Primary() (*sql.DB, error) {
	return fn()
}

type resolverClientStub struct {
	resolveFn func(context.Context) error
	primaryFn func() (*sql.DB, error)
}

func (s resolverClientStub) Resolver(ctx context.Context) (dbresolver.DB, error) {
	if s.resolveFn != nil {
		if err := s.resolveFn(ctx); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func (s resolverClientStub) Primary() (*sql.DB, error) {
	if s.primaryFn == nil {
		return nil, nil
	}

	return s.primaryFn()
}

func TestResolvePrimaryDB_NilClient(t *testing.T) {
	t.Parallel()

	db, err := resolvePrimaryDB(context.Background(), nil)
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrConnectionRequired)
}

func TestResolvePrimaryDB_ResolverFailure(t *testing.T) {
	t.Parallel()

	resolverErr := errors.New("resolver unavailable")
	client := resolverClientStub{
		resolveFn: func(_ context.Context) error {
			return resolverErr
		},
	}

	db, err := resolvePrimaryDB(context.Background(), client)
	require.Nil(t, db)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to initialize database resolver")
	require.ErrorIs(t, err, resolverErr)
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

func TestResolvePrimaryDB_ResolverInvokedBeforePrimary(t *testing.T) {
	t.Parallel()

	calledResolver := false
	calledPrimary := false

	client := resolverClientStub{
		resolveFn: func(_ context.Context) error {
			calledResolver = true
			return nil
		},
		primaryFn: func() (*sql.DB, error) {
			calledPrimary = true
			return &sql.DB{}, nil
		},
	}

	_, err := resolvePrimaryDB(context.Background(), client)
	require.NoError(t, err)
	require.True(t, calledResolver)
	require.True(t, calledPrimary)
}

func TestResolvePrimaryDB_NilContextUsesBackground(t *testing.T) {
	t.Parallel()

	expected := &sql.DB{}
	db, err := resolvePrimaryDB(nil, primaryDBProviderFunc(func() (*sql.DB, error) { //nolint:staticcheck // intentional nil context
		return expected, nil
	}))
	require.NoError(t, err)
	require.Same(t, expected, db)
}

func TestResolvePrimaryDB_PrimaryError(t *testing.T) {
	t.Parallel()

	primaryErr := errors.New("disk on fire")
	db, err := resolvePrimaryDB(context.Background(), primaryDBProviderFunc(func() (*sql.DB, error) {
		return nil, primaryErr
	}))
	require.Nil(t, db)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get database connection")
	require.ErrorIs(t, err, primaryErr)
}
