//go:build unit

package mongodb

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// Store nil-receiver guards
// -------------------------------------------------------------------

func TestStore_List_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.List(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_Get_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, ok, err := s.Get(context.Background(), "ns", "key")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
	assert.False(t, ok)
}

func TestStore_Set_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Set(context.Background(), store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_GetTenantValue_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, ok, err := s.GetTenantValue(context.Background(), "tenant-1", "ns", "key")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
	assert.False(t, ok)
}

func TestStore_SetTenantValue_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.SetTenantValue(context.Background(), "tenant-1", store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_DeleteTenantValue_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.DeleteTenantValue(context.Background(), "tenant-1", "ns", "key", "actor")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// -------------------------------------------------------------------
// SetTenantValue — TenantSchemaDisabled guard (fires before tracer)
// -------------------------------------------------------------------

func TestStore_SetTenantValue_TenantSchemaDisabled(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: false}}
	err := s.SetTenantValue(context.Background(), "tenant-1", store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled)
}

// -------------------------------------------------------------------
// DeleteTenantValue — TenantSchemaDisabled guard (fires before tracer)
// -------------------------------------------------------------------

func TestStore_DeleteTenantValue_TenantSchemaDisabled(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: false}}
	err := s.DeleteTenantValue(context.Background(), "tenant-1", "ns", "key", "actor")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled)
}

// -------------------------------------------------------------------
// Store closed-state guards
// -------------------------------------------------------------------

func TestStore_List_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	_, err := s.List(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_Get_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	_, _, err := s.Get(context.Background(), "ns", "key")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_Set_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	err := s.Set(context.Background(), store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_GetTenantValue_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	_, _, err := s.GetTenantValue(context.Background(), "tenant-1", "ns", "key")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_SetTenantValue_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	err := s.SetTenantValue(context.Background(), "tenant-1", store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestStore_DeleteTenantValue_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	err := s.DeleteTenantValue(context.Background(), "tenant-1", "ns", "key", "actor")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}
