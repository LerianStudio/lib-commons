//go:build unit

package zap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	logpkg "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func newObservedLogger(level zapcore.Level) (*Logger, *observer.ObservedLogs) {
	core, observed := observer.New(level)

	return &Logger{logger: zap.New(core)}, observed
}

// newBufferedLogger creates a Logger that writes JSON to a buffer for output
// inspection (e.g., verifying CWE-117 sanitization in serialized output).
func newBufferedLogger(level zapcore.Level) (*Logger, *strings.Builder) {
	buf := &strings.Builder{}
	ws := zapcore.AddSync(buf)

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "" // omit timestamp for deterministic test output
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		ws,
		level,
	)

	return &Logger{logger: zap.New(core)}, buf
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
	logger, observed := newObservedLogger(zapcore.DebugLevel)

	logger.Debug("debug message")
	logger.Info("info message", String("request_id", "req-1"))
	logger.Warn("warn message")
	logger.Error("error message", ErrorField(errors.New("boom")))

	entries := observed.All()
	require.Len(t, entries, 4)

	assert.Equal(t, zapcore.DebugLevel, entries[0].Level)
	assert.Equal(t, "debug message", entries[0].Message)

	assert.Equal(t, zapcore.InfoLevel, entries[1].Level)
	assert.Equal(t, "info message", entries[1].Message)
	assert.Equal(t, "req-1", entries[1].ContextMap()["request_id"])

	assert.Equal(t, zapcore.WarnLevel, entries[2].Level)
	assert.Equal(t, "warn message", entries[2].Message)

	assert.Equal(t, zapcore.ErrorLevel, entries[3].Level)
	assert.Equal(t, "error message", entries[3].Message)
}

func TestWithZapFieldsAddsFieldsWithoutMutatingParent(t *testing.T) {
	logger, observed := newObservedLogger(zapcore.DebugLevel)
	child := logger.WithZapFields(String("tenant_id", "t-1"))

	logger.Info("parent")
	child.Info("child")

	entries := observed.All()
	require.Len(t, entries, 2)

	_, parentHasTenant := entries[0].ContextMap()["tenant_id"]
	assert.False(t, parentHasTenant)
	assert.Equal(t, "t-1", entries[1].ContextMap()["tenant_id"])
}

func TestSyncReturnsNoErrorForHealthyLogger(t *testing.T) {
	logger, _ := newObservedLogger(zapcore.DebugLevel)

	require.NoError(t, logger.Sync(context.Background()))
}

// errorSink is a zapcore.WriteSyncer that always returns an error on Sync.
type errorSink struct{}

func (e errorSink) Write(p []byte) (int, error) { return len(p), nil }
func (e errorSink) Sync() error                 { return errors.New("simulated sync failure") }

// panicSink is a zapcore.WriteSyncer that panics on Sync, for testing
// the panic-recovery branch in Logger.Sync.
type panicSink struct{}

func (p panicSink) Write(b []byte) (int, error) { return len(b), nil }
func (p panicSink) Sync() error                 { panic("boom from sync") }

func TestSyncReturnsErrorFromFailingSink(t *testing.T) {
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		errorSink{},
		zapcore.DebugLevel,
	)
	logger := &Logger{logger: zap.New(core)}

	err := logger.Sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated sync failure")
}

func TestSyncRecoversPanicFromSink(t *testing.T) {
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		panicSink{},
		zapcore.DebugLevel,
	)
	logger := &Logger{logger: zap.New(core)}

	err := logger.Sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic during logger sync")
}

func TestFieldHelpers(t *testing.T) {
	logger, observed := newObservedLogger(zapcore.DebugLevel)
	logger.Info(
		"helpers",
		String("s", "value"),
		Int("i", 42),
		Bool("b", true),
		Duration("d", 2*time.Second),
	)

	entries := observed.All()
	require.Len(t, entries, 1)
	ctx := entries[0].ContextMap()

	assert.Equal(t, "value", ctx["s"])
	assert.Equal(t, int64(42), ctx["i"])
	assert.Equal(t, true, ctx["b"])
	assert.Equal(t, 2*time.Second, ctx["d"])
}

// ===========================================================================
// CWE-117: Log Injection Prevention for Zap Adapter
//
// Zap serializes output as JSON, which inherently escapes control characters
// in string values. These tests verify that behavior is preserved and that
// injection attempts cannot split log lines or forge entries.
// ===========================================================================

// TestCWE117_ZapMessageNewlineInjection verifies that newlines in log messages
// are properly escaped in JSON output, preventing log line splitting.
func TestCWE117_ZapMessageNewlineInjection(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "LF in message",
			message: "legitimate\n{\"level\":\"error\",\"msg\":\"forged entry\"}",
		},
		{
			name:    "CR in message",
			message: "legitimate\r{\"level\":\"error\",\"msg\":\"forged entry\"}",
		},
		{
			name:    "CRLF in message",
			message: "legitimate\r\n{\"level\":\"error\",\"msg\":\"forged entry\"}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newBufferedLogger(zapcore.DebugLevel)
			logger.Info(tt.message)
			require.NoError(t, logger.Sync(context.Background()))

			out := buf.String()
			// JSON output from zap should be a single line per entry
			lines := strings.Split(strings.TrimSpace(out), "\n")
			assert.Len(t, lines, 1,
				"CWE-117: zap JSON output must be a single line, got %d lines:\n%s", len(lines), out)

			// The raw newline characters should not appear in the JSON output
			// (JSON encoder escapes them as \n, \r)
			assert.NotContains(t, out, "forged entry\"}",
				"forged JSON entry must not appear as a separate parseable line")
		})
	}
}

// TestCWE117_ZapFieldValueInjection verifies field values with newlines
// are escaped by zap's JSON encoder.
func TestCWE117_ZapFieldValueInjection(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)

	maliciousValue := "user123\n{\"level\":\"error\",\"msg\":\"ADMIN ACCESS GRANTED\"}"
	logger.Info("login", String("user_id", maliciousValue))
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1,
		"CWE-117: field value injection must not create extra JSON lines")
}

// TestCWE117_ZapFieldNameInjection verifies that field names with control
// characters are escaped by zap's JSON encoder.
func TestCWE117_ZapFieldNameInjection(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)

	// Field name with embedded newline
	logger.Info("event", zap.String("key\ninjected", "value"))
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1,
		"CWE-117: field name injection must not create extra JSON lines")
}

// TestCWE117_ZapNullByteInMessage verifies null bytes in messages are handled.
func TestCWE117_ZapNullByteInMessage(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)
	logger.Info("before\x00after")
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "null byte must not split log output")
}

// TestCWE117_ZapANSIEscapeInMessage verifies ANSI escapes don't break output.
func TestCWE117_ZapANSIEscapeInMessage(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)
	logger.Info("normal \x1b[31mRED\x1b[0m normal")
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "ANSI escape must not split log output")
}

// TestCWE117_ZapTabInMessage verifies tab characters are handled in JSON output.
func TestCWE117_ZapTabInMessage(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)
	logger.Info("col1\tcol2\tcol3")
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "tabs must not split log output")
	// JSON encoder escapes tabs as \t in the JSON string
	assert.Contains(t, out, "col1")
	assert.Contains(t, out, "col2")
}

// TestCWE117_ZapWithPreservesSanitization verifies that child loggers created
// via With() still properly handle injection attempts.
func TestCWE117_ZapWithPreservesSanitization(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)
	child := logger.WithZapFields(String("session", "sess\n{\"forged\":true}"))
	child.Info("child message")
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1,
		"CWE-117: With() must not allow field injection to split lines")
}

// TestCWE117_ZapMultipleVectorsSimultaneously combines multiple attack vectors.
func TestCWE117_ZapMultipleVectorsSimultaneously(t *testing.T) {
	logger, buf := newBufferedLogger(zapcore.DebugLevel)

	// Message with injection
	msg := "event\n{\"level\":\"error\",\"msg\":\"forged\"}\ttab\r\nmore"
	// Fields with injection
	logger.Info(msg,
		zap.String("user\nfake", "val\nfake"),
		zap.String("safe_key", "safe_val"))
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1,
		"CWE-117: combined attack vectors must not create multiple JSON lines")
}

// ===========================================================================
// Zap Level Filtering Tests
// ===========================================================================

// TestZapLevelFiltering verifies that the observed logger correctly filters
// by log level.
func TestZapLevelFiltering(t *testing.T) {
	t.Run("info level suppresses debug", func(t *testing.T) {
		logger, observed := newObservedLogger(zapcore.InfoLevel)
		logger.Debug("should be suppressed")
		logger.Info("should appear")

		entries := observed.All()
		require.Len(t, entries, 1)
		assert.Equal(t, "should appear", entries[0].Message)
	})

	t.Run("error level suppresses warn and below", func(t *testing.T) {
		logger, observed := newObservedLogger(zapcore.ErrorLevel)
		logger.Debug("suppressed")
		logger.Info("suppressed")
		logger.Warn("suppressed")
		logger.Error("visible")

		entries := observed.All()
		require.Len(t, entries, 1)
		assert.Equal(t, "visible", entries[0].Message)
	})
}

// TestZapRawReturnsUnderlyingLogger verifies Raw() returns the inner zap.Logger.
func TestZapRawReturnsUnderlyingLogger(t *testing.T) {
	logger, _ := newObservedLogger(zapcore.DebugLevel)
	raw := logger.Raw()
	assert.NotNil(t, raw)
}

// TestZapRawOnNilReturnsNop verifies Raw() on nil returns nop logger.
func TestZapRawOnNilReturnsNop(t *testing.T) {
	var logger *Logger
	raw := logger.Raw()
	assert.NotNil(t, raw, "Raw() on nil logger should return nop, not nil")
}

// TestZapErrorFieldHelper verifies the ErrorField helper.
func TestZapErrorFieldHelper(t *testing.T) {
	logger, observed := newObservedLogger(zapcore.DebugLevel)
	testErr := errors.New("test error")
	logger.Error("failed", ErrorField(testErr))

	entries := observed.All()
	require.Len(t, entries, 1)
	assert.Equal(t, "test error", entries[0].ContextMap()["error"].(string))
}

// TestZapAnyFieldHelper verifies the Any helper with various types.
func TestZapAnyFieldHelper(t *testing.T) {
	logger, observed := newObservedLogger(zapcore.DebugLevel)
	logger.Info("test",
		Any("slice", []string{"a", "b"}),
		Any("map", map[string]int{"x": 1}))

	entries := observed.All()
	require.Len(t, entries, 1)
	// Verify fields exist (exact format depends on zap encoding)
	ctx := entries[0].ContextMap()
	assert.NotNil(t, ctx["slice"])
	assert.NotNil(t, ctx["map"])
}

// ===========================================================================
// log.Logger interface coverage
// ===========================================================================

func TestLogAllLevels(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)

	logger.Log(context.Background(), logpkg.LevelDebug, "debug via Log")
	logger.Log(context.Background(), logpkg.LevelInfo, "info via Log")
	logger.Log(context.Background(), logpkg.LevelWarn, "warn via Log")
	logger.Log(context.Background(), logpkg.LevelError, "error via Log")

	entries := observed.All()
	require.Len(t, entries, 4)

	assert.Equal(t, zapcore.DebugLevel, entries[0].Level)
	assert.Equal(t, zapcore.InfoLevel, entries[1].Level)
	assert.Equal(t, zapcore.WarnLevel, entries[2].Level)
	assert.Equal(t, zapcore.ErrorLevel, entries[3].Level)
}

func TestLogDefaultLevel(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)

	// Use an undefined level value to hit the default case
	logger.Log(context.Background(), logpkg.Level(99), "default level")

	entries := observed.All()
	require.Len(t, entries, 1)
	assert.Equal(t, zapcore.InfoLevel, entries[0].Level, "unknown level should default to Info")
}

func TestLogWithNilContext(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)

	assert.NotPanics(t, func() {
		//nolint:staticcheck // intentionally passing nil context
		logger.Log(nil, logpkg.LevelInfo, "nil ctx message")
	})

	entries := observed.All()
	require.Len(t, entries, 1)
	assert.Equal(t, "nil ctx message", entries[0].Message)
	// No trace_id/span_id should be present
	_, hasTrace := entries[0].ContextMap()["trace_id"]
	assert.False(t, hasTrace)
}

func TestLogWithOTelSpanInjectsTraceFields(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)

	// Create a span context with valid trace ID and span ID
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.Log(ctx, logpkg.LevelInfo, "traced message", logpkg.String("request_id", "req-42"))

	entries := observed.All()
	require.Len(t, entries, 1)

	cm := entries[0].ContextMap()
	assert.Equal(t, traceID.String(), cm["trace_id"])
	assert.Equal(t, spanID.String(), cm["span_id"])
	assert.Equal(t, "req-42", cm["request_id"])
}

func TestLogWithInvalidSpanDoesNotInjectTraceFields(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)

	// Background context has no active span — SpanContext is invalid
	logger.Log(context.Background(), logpkg.LevelInfo, "no span")

	entries := observed.All()
	require.Len(t, entries, 1)

	_, hasTrace := entries[0].ContextMap()["trace_id"]
	assert.False(t, hasTrace)
}

func TestWithReturnsChildLogger(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)

	child := logger.With(logpkg.String("component", "auth"))
	child.Log(context.Background(), logpkg.LevelInfo, "child msg")

	// Parent should not have the field
	logger.Log(context.Background(), logpkg.LevelInfo, "parent msg")

	entries := observed.All()
	require.Len(t, entries, 2)

	assert.Equal(t, "auth", entries[0].ContextMap()["component"])
	_, parentHas := entries[1].ContextMap()["component"]
	assert.False(t, parentHas)
}

func TestWithGroupNamespacesFields(t *testing.T) {
	t.Parallel()

	// Use a buffered JSON logger so we can inspect the serialized output
	// and verify the namespaced field structure.
	logger, buf := newBufferedLogger(zapcore.DebugLevel)

	grouped := logger.WithGroup("http")
	grouped.Log(context.Background(), logpkg.LevelInfo, "grouped msg", logpkg.String("method", "GET"))
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	// The JSON output should contain the "http" namespace wrapping "method"
	assert.Contains(t, out, `"http"`)
	assert.Contains(t, out, `"method"`)
	assert.Contains(t, out, `"GET"`)
	assert.Contains(t, out, "grouped msg")
}

func TestEnabledReportsCorrectly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		coreLevel zapcore.Level
		checkLvl  logpkg.Level
		expected  bool
	}{
		{"debug enabled at debug", zapcore.DebugLevel, logpkg.LevelDebug, true},
		{"info enabled at debug", zapcore.DebugLevel, logpkg.LevelInfo, true},
		{"warn enabled at debug", zapcore.DebugLevel, logpkg.LevelWarn, true},
		{"error enabled at debug", zapcore.DebugLevel, logpkg.LevelError, true},
		{"debug disabled at info", zapcore.InfoLevel, logpkg.LevelDebug, false},
		{"info enabled at info", zapcore.InfoLevel, logpkg.LevelInfo, true},
		{"debug disabled at error", zapcore.ErrorLevel, logpkg.LevelDebug, false},
		{"info disabled at error", zapcore.ErrorLevel, logpkg.LevelInfo, false},
		{"warn disabled at error", zapcore.ErrorLevel, logpkg.LevelWarn, false},
		{"error enabled at error", zapcore.ErrorLevel, logpkg.LevelError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, _ := newObservedLogger(tt.coreLevel)
			assert.Equal(t, tt.expected, logger.Enabled(tt.checkLvl))
		})
	}
}

func TestSyncWithCancelledContext(t *testing.T) {
	t.Parallel()

	logger, _ := newObservedLogger(zapcore.DebugLevel)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := logger.Sync(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLevelReturnsAtomicLevel(t *testing.T) {
	t.Parallel()

	al := zap.NewAtomicLevelAt(zapcore.WarnLevel)
	logger := &Logger{
		logger:      zap.NewNop(),
		atomicLevel: al,
	}

	assert.Equal(t, zapcore.WarnLevel, logger.Level().Level())
}

func TestLevelOnNilReceiverReturnsDefault(t *testing.T) {
	t.Parallel()

	var logger *Logger
	// Should not panic and should return a usable default level.
	level := logger.Level()
	assert.Equal(t, zapcore.InfoLevel, level.Level(),
		"nil receiver should return default AtomicLevel (info)")
}

func TestWithGroupEmptyNameReturnsReceiver(t *testing.T) {
	t.Parallel()

	logger, _ := newObservedLogger(zapcore.DebugLevel)

	// Empty name should return the same logger instance (no namespace created).
	same := logger.WithGroup("")
	assert.Equal(t, logger, same, "WithGroup(\"\") should return the same logger")
}

func TestSensitiveFieldRedaction(t *testing.T) {
	t.Parallel()

	logger, observed := newObservedLogger(zapcore.DebugLevel)
	logger.Log(context.Background(), logpkg.LevelInfo, "login",
		logpkg.String("password", "super_secret"),
		logpkg.String("api_key", "key-12345"),
		logpkg.String("user_id", "u-42"),
	)

	entries := observed.All()
	require.Len(t, entries, 1)
	ctx := entries[0].ContextMap()

	assert.Equal(t, "[REDACTED]", ctx["password"],
		"password field must be redacted")
	assert.Equal(t, "[REDACTED]", ctx["api_key"],
		"api_key field must be redacted")
	assert.Equal(t, "u-42", ctx["user_id"],
		"non-sensitive fields must pass through")
}

func TestConsoleEncodingSanitizesMessages(t *testing.T) {
	// Create a console-encoded logger and verify newlines are sanitized
	buf := &strings.Builder{}
	ws := zapcore.AddSync(buf)

	encoderCfg := zap.NewDevelopmentEncoderConfig()
	encoderCfg.TimeKey = ""
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		ws,
		zapcore.DebugLevel,
	)

	logger := &Logger{
		logger:          zap.New(core),
		consoleEncoding: true,
	}

	logger.Log(context.Background(), logpkg.LevelInfo, "line1\nline2\rline3")
	require.NoError(t, logger.Sync(context.Background()))

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1,
		"console output with injection attempt must remain a single line, got: %q", out)
}

func TestLogLevelToZapConversions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    logpkg.Level
		expected zapcore.Level
	}{
		{logpkg.LevelDebug, zapcore.DebugLevel},
		{logpkg.LevelInfo, zapcore.InfoLevel},
		{logpkg.LevelWarn, zapcore.WarnLevel},
		{logpkg.LevelError, zapcore.ErrorLevel},
		{logpkg.Level(42), zapcore.InfoLevel}, // default
	}

	for _, tt := range tests {
		t.Run(tt.input.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, logLevelToZap(tt.input))
		})
	}
}
