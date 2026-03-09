package log

import "context"

// NopLogger is a no-op logger implementation.
type NopLogger struct{}

// NewNop creates a no-op logger implementation.
func NewNop() Logger {
	return &NopLogger{}
}

// Log drops all log events.
func (l *NopLogger) Log(_ context.Context, _ Level, _ string, _ ...Field) {}

// With returns the same no-op logger.
//
//nolint:ireturn
func (l *NopLogger) With(_ ...Field) Logger {
	return l
}

// WithGroup returns the same no-op logger.
//
//nolint:ireturn
func (l *NopLogger) WithGroup(_ string) Logger {
	return l
}

// Enabled always returns false for NopLogger.
func (l *NopLogger) Enabled(_ Level) bool {
	return false
}

// Sync is a no-op and always returns nil.
func (l *NopLogger) Sync(_ context.Context) error { return nil }
