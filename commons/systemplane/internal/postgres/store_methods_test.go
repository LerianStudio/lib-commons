//go:build unit

package postgres

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newClosedStore returns a Store that is nil.
func newNilStore() *Store {
	return nil
}

// newStoreWithCancel creates a minimal Store with a cancel function to avoid panic.
func newStoreWithCancel() *Store {
	s := &Store{}
	s.cfg.Table = "systemplane_entries"
	// Use a context with cancel to set up the cancel function
	_, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	return s
}

// TestGet_NilReceiver covers the nil receiver guard on Get.
func TestGet_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, _, err := s.Get(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestSet_NilReceiver covers the nil receiver guard on Set.
func TestSet_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Set(context.Background(), store.Entry{Namespace: "global", Key: "log.level"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestClose_NilReceiver covers the nil receiver guard.
func TestClose_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Close()
	require.NoError(t, err)
}

// TestClose_Idempotent covers the double-close is safe.
func TestClose_Idempotent(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()

	err := s.Close()
	require.NoError(t, err)
	assert.True(t, s.closed)

	// Second close should also succeed
	err = s.Close()
	require.NoError(t, err)
}

// TestSet_EmptyNamespace covers the namespace validation.
func TestSet_EmptyNamespace(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()
	err := s.Set(context.Background(), store.Entry{Namespace: "", Key: "log.level"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace must not be empty")
}

// TestSet_EmptyKey covers the key validation.
func TestSet_EmptyKey(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()
	err := s.Set(context.Background(), store.Entry{Namespace: "global", Key: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key must not be empty")
}

// TestGet_ClosedStore covers the closed state.
func TestGet_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()
	_ = s.Close()

	_, _, err := s.Get(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestSet_ClosedStore covers the closed state.
func TestSet_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()
	_ = s.Close()

	err := s.Set(context.Background(), store.Entry{Namespace: "global", Key: "log.level"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestSubscribe_NilReceiver covers the nil receiver guard on Subscribe.
func TestSubscribe_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Subscribe(context.Background(), func(_ store.Event) {})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestSubscribe_ClosedStore covers the closed state.
func TestSubscribe_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()
	_ = s.Close()

	err := s.Subscribe(context.Background(), func(_ store.Event) {})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestList_NilReceiver covers the nil receiver guard.
func TestList_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.List(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestList_ClosedStore covers the closed state.
func TestList_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newStoreWithCancel()
	_ = s.Close()

	_, err := s.List(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}
