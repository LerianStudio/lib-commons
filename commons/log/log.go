package log

import (
	"context"
	"fmt"
	"strings"
)

// Logger is the package interface for v2 logging.
//
//go:generate mockgen --destination=log_mock.go --package=log . Logger
type Logger interface {
	Log(ctx context.Context, level Level, msg string, fields ...Field)
	With(fields ...Field) Logger
	WithGroup(name string) Logger
	Enabled(level Level) bool
	Sync(ctx context.Context) error
}

// Level represents the severity of a log entry.
//
// Lower numeric values indicate higher severity (LevelError=0 is most severe,
// LevelDebug=3 is least). This is inverted from slog/zap conventions where
// higher numeric values mean higher severity.
//
// The GoLogger.Enabled comparison uses l.Level >= level, which works because
// the logger's Level acts as a verbosity ceiling: a logger at LevelInfo (2)
// emits Error (0), Warn (1), and Info (2) messages, but suppresses Debug (3).
type Level uint8

// Level constants define log severity. Lower numeric values indicate higher
// severity. Setting a logger's Level to a given constant enables that level
// and all levels with lower numeric values (i.e., higher severity).
//
//	LevelError (0) -- only errors
//	LevelWarn  (1) -- errors + warnings
//	LevelInfo  (2) -- errors + warnings + info
//	LevelDebug (3) -- everything
const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

// LevelUnknown represents an invalid or unrecognized log level.
// Returned by ParseLevel on error to distinguish from LevelError (the zero value).
const LevelUnknown Level = 255

const (
	errLevelName = "error" // String form of LevelError (severity name)
	errFieldKey  = "error" // Conventional field key for Err() helper
)

// String returns the string representation of a log level.
func (level Level) String() string {
	switch level {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return errLevelName
	default:
		return "unknown"
	}
}

// ParseLevel takes a string level and returns a Level constant.
// Leading and trailing whitespace is trimmed before matching.
func ParseLevel(lvl string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case errLevelName:
		return LevelError, nil
	}

	return LevelUnknown, fmt.Errorf("not a valid Level: %q", lvl)
}

// Field is a strongly-typed key/value attribute attached to a log event.
type Field struct {
	Key   string
	Value any
}

// Any creates a field with an arbitrary value.
//
// WARNING: prefer typed constructors (String, Int, Bool, Err) to avoid
// accidentally logging sensitive data (passwords, tokens, PII). If using
// Any, ensure the value is sanitized or non-sensitive.
func Any(key string, value any) Field {
	return Field{Key: key, Value: value}
}

// String creates a string field.
func String(key, value string) Field {
	return Field{Key: key, Value: value}
}

// Int creates an integer field.
func Int(key string, value int) Field {
	return Field{Key: key, Value: value}
}

// Bool creates a boolean field.
func Bool(key string, value bool) Field {
	return Field{Key: key, Value: value}
}

// Err creates the conventional `error` field.
func Err(err error) Field {
	return Field{Key: errFieldKey, Value: err}
}
