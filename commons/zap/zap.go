// Package zap provides a structured logger adapting go.uber.org/zap to commons/log.Logger.
// This package delegates to github.com/LerianStudio/lib-observability/zap.
package zap

import (
	"time"

	libobszap "github.com/LerianStudio/lib-observability/zap"
)

// Field is a typed structured logging field (zap alias kept for convenience methods).
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Field instead.
type Field = libobszap.Field

// Logger is a strict structured logger that implements log.Logger.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Logger instead.
type Logger = libobszap.Logger

// Environment controls the baseline logger profile.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Environment instead.
type Environment = libobszap.Environment

// Config contains all required logger initialization inputs.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Config instead.
type Config = libobszap.Config

const (
	// EnvironmentProduction enables production-safe logging defaults.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/zap.EnvironmentProduction instead.
	EnvironmentProduction = libobszap.EnvironmentProduction
	// EnvironmentStaging enables staging-safe logging defaults.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/zap.EnvironmentStaging instead.
	EnvironmentStaging = libobszap.EnvironmentStaging
	// EnvironmentUAT enables UAT-safe logging defaults.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/zap.EnvironmentUAT instead.
	EnvironmentUAT = libobszap.EnvironmentUAT
	// EnvironmentDevelopment enables verbose development logging defaults.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/zap.EnvironmentDevelopment instead.
	EnvironmentDevelopment = libobszap.EnvironmentDevelopment
	// EnvironmentLocal enables verbose local-development logging defaults.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/zap.EnvironmentLocal instead.
	EnvironmentLocal = libobszap.EnvironmentLocal
)

// New creates a structured logger from the given configuration.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.New instead.
func New(cfg Config) (*Logger, error) {
	return libobszap.New(cfg)
}

// Any creates a field with any value.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Any instead.
func Any(key string, value any) Field {
	return libobszap.Any(key, value)
}

// String creates a string field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.String instead.
func String(key, value string) Field {
	return libobszap.String(key, value)
}

// Int creates an int field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Int instead.
func Int(key string, value int) Field {
	return libobszap.Int(key, value)
}

// Bool creates a bool field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Bool instead.
func Bool(key string, value bool) Field {
	return libobszap.Bool(key, value)
}

// Duration creates a duration field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.Duration instead.
func Duration(key string, value time.Duration) Field {
	return libobszap.Duration(key, value)
}

// ErrorField creates an error field.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/zap.ErrorField instead.
func ErrorField(err error) Field {
	return libobszap.ErrorField(err)
}
