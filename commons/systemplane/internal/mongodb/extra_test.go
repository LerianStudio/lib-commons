//go:build unit

package mongodb

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
)

// TestDroppedEvents_NilReceiver covers the nil-safe guard on DroppedEvents.
func TestDroppedEvents_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	assert.Equal(t, int64(0), s.DroppedEvents())
}

// TestDroppedEvents_ZeroOnNew covers initial state.
func TestDroppedEvents_ZeroOnNew(t *testing.T) {
	t.Parallel()

	s := &Store{}
	assert.Equal(t, int64(0), s.DroppedEvents())
}

// TestLogWarn_NilReceiver covers the nil receiver guard.
func TestLogWarn_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	assert.NotPanics(t, func() {
		s.logWarn(context.Background(), "test message")
	})
}

// TestLogWarn_NilLogger covers the nil logger guard.
func TestLogWarn_NilLogger(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{Logger: nil}}
	assert.NotPanics(t, func() {
		s.logWarn(context.Background(), "test message")
	})
}

// TestLogInfo_NilReceiver covers the nil receiver guard.
func TestLogInfo_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	assert.NotPanics(t, func() {
		s.logInfo(context.Background(), "test message")
	})
}

// TestLogInfo_NilLogger covers the nil logger guard.
func TestLogInfo_NilLogger(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{Logger: nil}}
	assert.NotPanics(t, func() {
		s.logInfo(context.Background(), "test message")
	})
}

// TestLogWarn_WithNopLogger covers the enabled path using nop logger.
func TestLogWarn_WithNopLogger(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	s := &Store{cfg: Config{Logger: logger}}
	assert.NotPanics(t, func() {
		s.logWarn(context.Background(), "test message", log.String("k", "v"))
	})
}

// TestLogInfo_WithNopLogger covers the enabled path.
func TestLogInfo_WithNopLogger(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	s := &Store{cfg: Config{Logger: logger}}
	assert.NotPanics(t, func() {
		s.logInfo(context.Background(), "test message", log.String("k", "v"))
	})
}

// TestIsClosed_FreshStore verifies isClosed returns false on a fresh store.
func TestIsClosed_FreshStore(t *testing.T) {
	t.Parallel()

	s := &Store{}
	assert.False(t, s.isClosed())
}
