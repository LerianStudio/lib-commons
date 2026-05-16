//go:build unit

package zap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	logpkg "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

// newZapLogger creates a logger using the public New() constructor for shim tests.
func newZapLogger(t *testing.T) *Logger {
	t.Helper()

	logger, err := New(Config{Environment: EnvironmentDevelopment, OTelLibraryName: "test"})
	require.NoError(t, err)

	return logger
}

func TestLoggerNilReceiverFallsBackToNop(t *testing.T) {
	var nilLogger *Logger

	assert.NotPanics(t, func() {
		nilLogger.Info("message")
	})
}

func TestLoggerNilUnderlyingFallsBackToNop(t *testing.T) {
	logger := &Logger{}

	assert.NotPanics(t, func() {
		logger.Info("message")
	})
}

func TestStructuredLoggingMethods(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)

	assert.NotPanics(t, func() {
		logger.Debug("debug message")
		logger.Info("info message", String("request_id", "req-1"))
		logger.Warn("warn message")
		logger.Error("error message", ErrorField(errors.New("boom")))
	})
}

func TestWithZapFieldsAddsFieldsWithoutMutatingParent(t *testing.T) {
	t.Setenv("LOG_ENCODING", "json")

	originalStderr := os.Stderr
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)

	os.Stderr = writeEnd
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = readEnd.Close()
		_ = writeEnd.Close()
	})

	logger := newZapLogger(t)
	child := logger.WithZapFields(String("tenant_id", "t-1"))

	assert.NotNil(t, child)
	assert.NotSame(t, logger, child)

	logger.Info("parent")
	child.Info("child")

	assertSyncAllowed(t, logger.Sync(context.Background()))
	assertSyncAllowed(t, child.Sync(context.Background()))
	require.NoError(t, writeEnd.Close())
	os.Stderr = originalStderr

	output, err := io.ReadAll(readEnd)
	require.NoError(t, err)

	entries := parseZapOutput(t, string(output))
	require.Len(t, entries, 2)

	_, parentHasTenant := entries[0]["tenant_id"]
	assert.False(t, parentHasTenant)
	assert.Equal(t, "t-1", entries[1]["tenant_id"])
}

func TestSyncReturnsNoErrorForHealthyLogger(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)
	// Sync may return "bad file descriptor" when stderr is not a real tty in CI;
	// accept only that known zap/stdout sink behavior.
	assertSyncAllowed(t, logger.Sync(context.Background()))
}

func assertSyncAllowed(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		assert.Contains(t, err.Error(), "bad file descriptor")
	}
}

func parseZapOutput(t *testing.T, output string) []map[string]any {
	t.Helper()

	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}

		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		entries = append(entries, entry)
	}

	return entries
}

func TestFieldHelpers(t *testing.T) {
	t.Parallel()

	// Verify field constructors produce fields with correct keys.
	assert.Equal(t, "s", String("s", "value").Key)
	assert.Equal(t, "i", Int("i", 42).Key)
	assert.Equal(t, "b", Bool("b", true).Key)
	assert.Equal(t, "d", Duration("d", 2*time.Second).Key)
}

func TestZapLevelFiltering(t *testing.T) {
	t.Parallel()

	// Production → info level: debug must be disabled.
	logger, err := New(Config{Environment: EnvironmentProduction, OTelLibraryName: "svc"})
	require.NoError(t, err)

	assert.False(t, logger.Enabled(logpkg.LevelDebug), "production logger must suppress debug")
	assert.True(t, logger.Enabled(logpkg.LevelInfo), "production logger must emit info")
	assert.True(t, logger.Enabled(logpkg.LevelError), "production logger must emit error")
}

func TestZapRawReturnsUnderlyingLogger(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)
	raw := logger.Raw()
	assert.NotNil(t, raw)
}

func TestZapRawOnNilReturnsNop(t *testing.T) {
	t.Parallel()

	var logger *Logger
	raw := logger.Raw()
	assert.NotNil(t, raw, "Raw() on nil logger should return nop, not nil")
}

func TestZapErrorFieldHelper(t *testing.T) {
	t.Parallel()

	f := ErrorField(errors.New("test error"))
	assert.Equal(t, "error", f.Key)
}

func TestZapAnyFieldHelper(t *testing.T) {
	t.Parallel()

	f := Any("key", []string{"a", "b"})
	assert.Equal(t, "key", f.Key)
}

func TestLogAllLevels(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)

	assert.NotPanics(t, func() {
		logger.Log(context.Background(), logpkg.LevelDebug, "debug via Log")
		logger.Log(context.Background(), logpkg.LevelInfo, "info via Log")
		logger.Log(context.Background(), logpkg.LevelWarn, "warn via Log")
		logger.Log(context.Background(), logpkg.LevelError, "error via Log")
	})
}

func TestLogDefaultLevel(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)

	// Unknown level must not panic.
	assert.NotPanics(t, func() {
		logger.Log(context.Background(), logpkg.Level(99), "unknown level")
	})
}

func TestLogWithNilContext(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)

	assert.NotPanics(t, func() {
		//nolint:staticcheck // Testing nil context intentionally.
		logger.Log(nil, logpkg.LevelInfo, "nil context")
	})
}

func TestWithReturnsChildLogger(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)
	child := logger.With(logpkg.String("k", "v"))

	assert.NotNil(t, child)
	assert.NotSame(t, logger, child)
}

func TestWithGroupNamespacesFields(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)
	child := logger.WithGroup("http")

	assert.NotNil(t, child)
	assert.NotSame(t, logger, child)
}

func TestEnabledReportsCorrectly(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{Environment: EnvironmentDevelopment, OTelLibraryName: "svc"})
	require.NoError(t, err)

	assert.True(t, logger.Enabled(logpkg.LevelDebug), "development logger must enable debug")
	assert.True(t, logger.Enabled(logpkg.LevelError), "development logger must enable error")

	prodLogger, err := New(Config{Environment: EnvironmentProduction, OTelLibraryName: "svc"})
	require.NoError(t, err)

	assert.False(t, prodLogger.Enabled(logpkg.LevelDebug), "production logger must suppress debug")
}

func TestSyncWithCancelledContext(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, logger.Sync(ctx), context.Canceled)
}

func TestLevelOnNilReceiverReturnsDefault(t *testing.T) {
	t.Parallel()

	var logger *Logger
	level := logger.Level()

	assert.Equal(t, zapcore.InfoLevel, level.Level(),
		"nil receiver should return default AtomicLevel (info)")
}

func TestWithGroupEmptyNameReturnsReceiver(t *testing.T) {
	t.Parallel()

	logger := newZapLogger(t)
	same := logger.WithGroup("")

	assert.Same(t, logger, same, "WithGroup(\"\") should return the same logger")
}
