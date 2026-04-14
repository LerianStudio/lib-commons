package zap

import (
	"context"
	"fmt"
	"strings"
	"time"

	logpkg "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/LerianStudio/lib-commons/v5/commons/security"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field is a typed structured logging field (zap alias kept for convenience methods).
type Field = zap.Field

// Logger is a strict structured logger that implements log.Logger.
//
// It intentionally does not expose printf/line/fatal helpers.
type Logger struct {
	logger      *zap.Logger
	atomicLevel zap.AtomicLevel
	// consoleEncoding is true when the logger uses console encoding.
	// When true, messages are sanitized to prevent CWE-117 log injection,
	// since console encoding does not inherently escape control characters
	// the way JSON encoding does.
	consoleEncoding bool
}

// Compile-time assertion: *Logger implements logpkg.Logger.
var _ logpkg.Logger = (*Logger)(nil)

func (l *Logger) must() *zap.Logger {
	if l == nil || l.logger == nil {
		return zap.NewNop()
	}

	return l.logger
}

// ---------------------------------------------------------------------------
// log.Logger interface methods
// ---------------------------------------------------------------------------

// Log implements log.Logger. It dispatches to the appropriate zap level.
// If ctx carries an active OpenTelemetry span, trace_id and span_id are
// automatically appended so logs correlate with distributed traces.
//
// Unknown levels are treated as LevelInfo (consistent with GoLogger policy).
func (l *Logger) Log(ctx context.Context, level logpkg.Level, msg string, fields ...logpkg.Field) {
	zapFields := logFieldsToZap(fields)

	if ctx != nil {
		if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
			zapFields = append(zapFields,
				zap.String("trace_id", sc.TraceID().String()),
				zap.String("span_id", sc.SpanID().String()),
			)
		}
	}

	// Sanitize message for console encoding (CWE-117 prevention).
	// JSON encoding handles this via its built-in escaping.
	safeMsg := l.sanitizeConsoleMsg(msg)

	switch level {
	case logpkg.LevelDebug:
		l.must().Debug(safeMsg, zapFields...)
	case logpkg.LevelInfo:
		l.must().Info(safeMsg, zapFields...)
	case logpkg.LevelWarn:
		l.must().Warn(safeMsg, zapFields...)
	case logpkg.LevelError:
		l.must().Error(safeMsg, zapFields...)
	default:
		// Unknown level policy: treat as Info. This is consistent across both
		// GoLogger and zap backends. See log.Level documentation.
		l.must().Info(safeMsg, zapFields...)
	}
}

// With returns a child logger with additional structured fields.
//
//nolint:ireturn
func (l *Logger) With(fields ...logpkg.Field) logpkg.Logger {
	if l == nil {
		return &Logger{logger: zap.NewNop()}
	}

	return &Logger{
		logger:          l.must().With(logFieldsToZap(fields)...),
		atomicLevel:     l.atomicLevel,
		consoleEncoding: l.consoleEncoding,
	}
}

// WithGroup returns a child logger that nests subsequent fields under a namespace.
// Empty group names are silently ignored, consistent with GoLogger behavior.
//
//nolint:ireturn
func (l *Logger) WithGroup(name string) logpkg.Logger {
	if l == nil {
		return &Logger{logger: zap.NewNop()}
	}

	if name == "" {
		return l
	}

	return &Logger{
		logger:          l.must().With(zap.Namespace(name)),
		atomicLevel:     l.atomicLevel,
		consoleEncoding: l.consoleEncoding,
	}
}

// Enabled reports whether the logger would emit a log at the given level.
func (l *Logger) Enabled(level logpkg.Level) bool {
	return l.must().Core().Enabled(logLevelToZap(level))
}

// Sync flushes buffered logs, respecting context cancellation.
func (l *Logger) Sync(ctx context.Context) error {
	if ctx == nil {
		return l.must().Sync()
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				runtime.HandlePanicValue(ctx, nil, r, "zap", "sync")

				done <- fmt.Errorf("panic during logger sync: %v", r)
			}
		}()

		done <- l.must().Sync()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// ---------------------------------------------------------------------------
// Convenience methods (direct zap.Field access for performance-sensitive code)
// ---------------------------------------------------------------------------

// WithZapFields returns a child logger with additional zap.Field values.
// Use this when working directly with zap fields for performance.
func (l *Logger) WithZapFields(fields ...Field) *Logger {
	if l == nil {
		return &Logger{logger: zap.NewNop()}
	}

	return &Logger{
		logger:          l.must().With(fields...),
		atomicLevel:     l.atomicLevel,
		consoleEncoding: l.consoleEncoding,
	}
}

// Debug logs a message with debug severity.
func (l *Logger) Debug(message string, fields ...Field) {
	l.must().Debug(message, fields...)
}

// Info logs a message with info severity.
func (l *Logger) Info(message string, fields ...Field) {
	l.must().Info(message, fields...)
}

// Warn logs a message with warn severity.
func (l *Logger) Warn(message string, fields ...Field) {
	l.must().Warn(message, fields...)
}

// Error logs a message with error severity.
func (l *Logger) Error(message string, fields ...Field) {
	l.must().Error(message, fields...)
}

// Raw returns the underlying zap logger.
func (l *Logger) Raw() *zap.Logger {
	return l.must()
}

// Level returns the runtime-adjustable level handle for this logger.
// On a nil receiver, a default AtomicLevel (info) is returned.
func (l *Logger) Level() zap.AtomicLevel {
	if l == nil {
		return zap.NewAtomicLevel()
	}

	return l.atomicLevel
}

// Any creates a field with any value.
func Any(key string, value any) Field {
	return zap.Any(key, value)
}

// String creates a string field.
func String(key, value string) Field {
	return zap.String(key, value)
}

// Int creates an int field.
func Int(key string, value int) Field {
	return zap.Int(key, value)
}

// Bool creates a bool field.
func Bool(key string, value bool) Field {
	return zap.Bool(key, value)
}

// Duration creates a duration field.
func Duration(key string, value time.Duration) Field {
	return zap.Duration(key, value)
}

// ErrorField creates an error field.
func ErrorField(err error) Field {
	return zap.Error(err)
}

// ---------------------------------------------------------------------------
// Internal conversion helpers
// ---------------------------------------------------------------------------

// logLevelToZap converts a log.Level to a zapcore.Level.
func logLevelToZap(level logpkg.Level) zapcore.Level {
	switch level {
	case logpkg.LevelDebug:
		return zapcore.DebugLevel
	case logpkg.LevelInfo:
		return zapcore.InfoLevel
	case logpkg.LevelWarn:
		return zapcore.WarnLevel
	case logpkg.LevelError:
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// redactedValue is the placeholder used for sensitive field values in log output.
const redactedValue = "[REDACTED]"

// consoleControlCharReplacer neutralizes control characters that can split log
// lines or forge entries in console-encoded output (CWE-117). JSON encoding
// handles this automatically via its escaping rules.
var consoleControlCharReplacer = strings.NewReplacer(
	"\n", `\n`,
	"\r", `\r`,
	"\t", `\t`,
	"\x00", `\0`,
)

// sanitizeConsoleMsg escapes control characters in a message string
// when the logger is configured with console encoding.
func (l *Logger) sanitizeConsoleMsg(msg string) string {
	if l != nil && l.consoleEncoding {
		return consoleControlCharReplacer.Replace(msg)
	}

	return msg
}

// logFieldsToZap converts log.Field values to zap.Field values.
// Sensitive field keys (matched via security.IsSensitiveField) are redacted.
func logFieldsToZap(fields []logpkg.Field) []zap.Field {
	zapFields := make([]zap.Field, len(fields))
	for i, f := range fields {
		if security.IsSensitiveField(f.Key) {
			zapFields[i] = zap.String(f.Key, redactedValue)
		} else {
			zapFields[i] = zap.Any(f.Key, f.Value)
		}
	}

	return zapFields
}
