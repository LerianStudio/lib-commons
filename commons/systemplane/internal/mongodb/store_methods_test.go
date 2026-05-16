//go:build unit

package mongodb

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// newMinimalStore creates a Store with minimal setup to avoid panics on method calls
// that check isClosed() before any DB access.
func newMinimalMongoStore() *Store {
	s := &Store{}
	s.tracer = noop.NewTracerProvider().Tracer(tracerName)
	_, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	return s
}

// TestMongoGet_NilReceiver covers the nil receiver guard.
func TestMongoGet_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, _, err := s.Get(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoSet_NilReceiver covers the nil receiver guard.
func TestMongoSet_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Set(context.Background(), store.Entry{Namespace: "global", Key: "log.level"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoList_NilReceiver covers the nil receiver guard.
func TestMongoList_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.List(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoClose_NilReceiver covers the nil receiver guard.
func TestMongoClose_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Close()
	require.NoError(t, err)
}

// TestMongoClose_Idempotent covers double-close safety.
func TestMongoClose_Idempotent(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()

	err := s.Close()
	require.NoError(t, err)

	// Second close should also be safe
	err = s.Close()
	require.NoError(t, err)
}

// TestMongoGet_ClosedStore covers the closed state check.
func TestMongoGet_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	_ = s.Close()

	_, _, err := s.Get(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoList_ClosedStore covers the closed state check.
func TestMongoList_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	_ = s.Close()

	_, err := s.List(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoSet_ClosedStore covers the closed state check.
func TestMongoSet_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	_ = s.Close()

	err := s.Set(context.Background(), store.Entry{Namespace: "global", Key: "log.level"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoSet_ValidationError covers empty namespace/key check.
func TestMongoSet_EmptyNamespaceKey(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()

	// Empty namespace and key
	err := s.Set(context.Background(), store.Entry{Namespace: "", Key: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace and key must be non-empty")
}

// TestMongoSubscribe_NilReceiver covers the nil receiver guard.
func TestMongoSubscribe_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	err := s.Subscribe(context.Background(), func(_ store.Event) {})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoSubscribe_ClosedStore covers the closed state.
func TestMongoSubscribe_ClosedStore(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	_ = s.Close()

	err := s.Subscribe(context.Background(), func(_ store.Event) {})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// TestMongoSubscribe_NilHandler covers the nil handler guard.
func TestMongoSubscribe_NilHandler(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	// Add a valid ctx to the store so it doesn't panic
	s.ctx, s.cancel = context.WithCancel(context.Background())

	err := s.Subscribe(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler must not be nil")
}

// TestSafeInvokeHandler covers the handler invocation.
func TestSafeInvokeHandler_CallsHandler(t *testing.T) {
	t.Parallel()

	s := newMinimalMongoStore()
	s.ctx, s.cancel = context.WithCancel(context.Background())

	called := false
	var receivedEvent store.Event

	s.safeInvokeHandler(context.Background(), func(evt store.Event) {
		called = true
		receivedEvent = evt
	}, store.Event{
		Namespace: "global",
		Key:       "log.level",
		TenantID:  "_global",
	})

	assert.True(t, called)
	assert.Equal(t, "global", receivedEvent.Namespace)
}
