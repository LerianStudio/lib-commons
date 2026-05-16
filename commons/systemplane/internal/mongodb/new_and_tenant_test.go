//go:build unit

package mongodb

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ---------------------------------------------------------------------------
// New — validation-only paths (no real MongoDB required)
// ---------------------------------------------------------------------------

func TestNew_NilClient(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Client: nil, Database: "mydb"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNilBackend)
}

func TestNew_EmptyDatabase(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Client: &mongo.Client{}, Database: ""})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNilBackend)
	assert.Contains(t, err.Error(), "database name")
}

// ---------------------------------------------------------------------------
// ListTenantValues — nil and closed guards
// ---------------------------------------------------------------------------

func TestListTenantValues_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.ListTenantValues(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestListTenantValues_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	require.NoError(t, s.Close())

	_, err := s.ListTenantValues(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// ListTenantOverrides — nil and closed guards
// ---------------------------------------------------------------------------

func TestListTenantOverrides_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.ListTenantOverrides(context.Background(), "", "", "", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestListTenantOverrides_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	require.NoError(t, s.Close())

	_, err := s.ListTenantOverrides(context.Background(), "", "", "", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// ListTenantsForKey — nil and closed guards
// ---------------------------------------------------------------------------

func TestListTenantsForKey_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.ListTenantsForKey(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestListTenantsForKey_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	require.NoError(t, s.Close())

	_, err := s.ListTenantsForKey(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// GetTenantValue — nil and closed guards + sentinel guard
// ---------------------------------------------------------------------------

func TestGetTenantValue_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, _, err := s.GetTenantValue(context.Background(), "tenant-1", "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestGetTenantValue_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	require.NoError(t, s.Close())

	_, _, err := s.GetTenantValue(context.Background(), "tenant-1", "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// SetTenantValue — nil and closed guards
// ---------------------------------------------------------------------------

func TestSetTenantValue_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.SetTenantValue(context.Background(), "tenant-1", store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestSetTenantValue_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	require.NoError(t, s.Close())

	err := s.SetTenantValue(context.Background(), "tenant-1", store.Entry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// DeleteTenantValue — nil and closed guards
// ---------------------------------------------------------------------------

func TestDeleteTenantValue_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.DeleteTenantValue(context.Background(), "tenant-1", "global", "log.level", "actor")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestDeleteTenantValue_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	require.NoError(t, s.Close())

	err := s.DeleteTenantValue(context.Background(), "tenant-1", "global", "log.level", "actor")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}
