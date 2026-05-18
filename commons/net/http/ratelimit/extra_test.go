//go:build unit

package ratelimit

import (
	"testing"

	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
)

// TestWithRedisStorageLogger covers the option function.
func TestWithRedisStorageLogger_SetsLogger(t *testing.T) {
	t.Parallel()

	logger := libLog.NewNop()
	s := &RedisStorage{}
	opt := WithRedisStorageLogger(logger)
	opt(s)
	assert.Equal(t, logger, s.logger)
}

func TestWithRedisStorageLogger_NilDoesNotOverwrite(t *testing.T) {
	t.Parallel()

	logger := libLog.NewNop()
	s := &RedisStorage{logger: logger}
	opt := WithRedisStorageLogger(nil)
	opt(s)
	// nil should be a no-op
	assert.Equal(t, logger, s.logger)
}

// TestLogError_NilStorage covers the nil storage guard.
func TestLogError_NilStorage(t *testing.T) {
	t.Parallel()

	var s *RedisStorage
	// Should not panic when storage is nil
	assert.NotPanics(t, func() {
		s.logError(nil, "test message", nil)
	})
}

// TestLogError_WithLogger covers the logger path.
func TestLogError_WithLogger(t *testing.T) {
	t.Parallel()

	logger := libLog.NewNop()
	s := &RedisStorage{logger: logger}
	// Should not panic with a logger
	assert.NotPanics(t, func() {
		s.logError(nil, "test message", assert.AnError, "key", "value")
	})
}
