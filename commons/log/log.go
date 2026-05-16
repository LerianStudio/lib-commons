// Package log defines the v2 logging interface and typed logging fields.
// This package delegates to github.com/LerianStudio/lib-observability/log.
package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// Logger is the package interface for v2 logging.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Logger instead.
//
// NOTE: Mocks are now provided by github.com/LerianStudio/lib-observability/log.
// Running mockgen here would overwrite the compatibility shims in log_mock.go.
type Logger = libobslog.Logger

// Level represents the severity of a log entry.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Level instead.
type Level = libobslog.Level

// Level constants define log severity. Lower numeric values indicate higher
// severity. Setting a logger's Level to a given constant enables that level
// and all levels with lower numeric values (i.e., higher severity).
//
//	LevelError (0) -- only errors
//	LevelWarn  (1) -- errors + warnings
//	LevelInfo  (2) -- errors + warnings + info
//	LevelDebug (3) -- everything
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.LevelError, LevelWarn, LevelInfo, LevelDebug instead.
const (
	LevelError = libobslog.LevelError
	LevelWarn  = libobslog.LevelWarn
	LevelInfo  = libobslog.LevelInfo
	LevelDebug = libobslog.LevelDebug
)

// LevelUnknown represents an invalid or unrecognized log level.
// Returned by ParseLevel on error to distinguish from LevelError (the zero value).
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.LevelUnknown instead.
const LevelUnknown = libobslog.LevelUnknown

// Field is a strongly-typed key/value attribute attached to a log event.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Field instead.
type Field = libobslog.Field

// Any creates a field with an arbitrary value.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Any instead.
func Any(key string, value any) Field {
	return libobslog.Any(key, value)
}

// String creates a string field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.String instead.
func String(key, value string) Field {
	return libobslog.String(key, value)
}

// Int creates an integer field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Int instead.
func Int(key string, value int) Field {
	return libobslog.Int(key, value)
}

// Bool creates a boolean field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Bool instead.
func Bool(key string, value bool) Field {
	return libobslog.Bool(key, value)
}

// Err creates the conventional `error` field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.Err instead.
func Err(err error) Field {
	return libobslog.Err(err)
}

// ParseLevel takes a string level and returns a Level constant.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.ParseLevel instead.
func ParseLevel(lvl string) (Level, error) {
	return libobslog.ParseLevel(lvl)
}
