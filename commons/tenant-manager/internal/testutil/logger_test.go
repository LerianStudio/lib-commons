//go:build unit

package testutil

import (
	"context"
	"testing"

	liblog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewMockLogger covers the mock logger factory.
func TestNewMockLogger_ReturnsLogger(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	require.NotNil(t, logger)
}

// TestCapturingLogger covers the CapturingLogger.
func TestNewCapturingLogger(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	require.NotNil(t, logger)
}

func TestCapturingLogger_Log_Simple(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	logger.Log(context.Background(), liblog.LevelInfo, "test message")

	msgs := logger.GetMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "test message", msgs[0])
}

func TestCapturingLogger_Log_WithFields(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	logger.Log(context.Background(), liblog.LevelInfo, "test message",
		liblog.String("key1", "value1"),
	)

	msgs := logger.GetMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0], "test message")
	assert.Contains(t, msgs[0], "key1")
}

func TestCapturingLogger_ContainsSubstring(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	logger.Log(context.Background(), liblog.LevelInfo, "hello world")

	assert.True(t, logger.ContainsSubstring("hello"))
	assert.False(t, logger.ContainsSubstring("goodbye"))
}

func TestCapturingLogger_Clear(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	logger.Log(context.Background(), liblog.LevelInfo, "message 1")
	logger.Log(context.Background(), liblog.LevelInfo, "message 2")

	logger.Clear()
	msgs := logger.GetMessages()
	assert.Empty(t, msgs)
}

func TestCapturingLogger_With(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	result := logger.With(liblog.String("k", "v"))
	assert.Same(t, logger, result)
}

func TestCapturingLogger_WithGroup(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	result := logger.WithGroup("group")
	assert.Same(t, logger, result)
}

func TestCapturingLogger_Enabled(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	assert.True(t, logger.Enabled(liblog.LevelInfo))
}

func TestCapturingLogger_Sync(t *testing.T) {
	t.Parallel()

	logger := NewCapturingLogger()
	err := logger.Sync(context.Background())
	assert.NoError(t, err)
}
