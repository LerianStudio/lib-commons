// Package log defines the v2 logging interface and typed logging fields.
// This package delegates to github.com/LerianStudio/lib-observability/log.
package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// Logger is the package interface for v2 logging.
//
//go:generate mockgen --destination=log_mock.go --package=log . Logger
type Logger = libobslog.Logger

// Level represents the severity of a log entry.
type Level = libobslog.Level

// Level constants define log severity. Lower numeric values indicate higher
// severity. Setting a logger's Level to a given constant enables that level
// and all levels with lower numeric values (i.e., higher severity).
//
//	LevelError (0) -- only errors
//	LevelWarn  (1) -- errors + warnings
//	LevelInfo  (2) -- errors + warnings + info
//	LevelDebug (3) -- everything
const (
	LevelError = libobslog.LevelError
	LevelWarn  = libobslog.LevelWarn
	LevelInfo  = libobslog.LevelInfo
	LevelDebug = libobslog.LevelDebug
)

// LevelUnknown represents an invalid or unrecognized log level.
// Returned by ParseLevel on error to distinguish from LevelError (the zero value).
const LevelUnknown = libobslog.LevelUnknown

// Field is a strongly-typed key/value attribute attached to a log event.
type Field = libobslog.Field

// Any creates a field with an arbitrary value.
var Any = libobslog.Any

// String creates a string field.
var String = libobslog.String

// Int creates an integer field.
var Int = libobslog.Int

// Bool creates a boolean field.
var Bool = libobslog.Bool

// Err creates the conventional `error` field.
var Err = libobslog.Err

// ParseLevel takes a string level and returns a Level constant.
var ParseLevel = libobslog.ParseLevel
