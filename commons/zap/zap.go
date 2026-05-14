// Package zap provides a structured logger adapting go.uber.org/zap to commons/log.Logger.
// This package delegates to github.com/LerianStudio/lib-observability/zap.
package zap

import (
	"time"

	libobszap "github.com/LerianStudio/lib-observability/zap"
)

// Field is a typed structured logging field (zap alias kept for convenience methods).
type Field = libobszap.Field

// Logger is a strict structured logger that implements log.Logger.
type Logger = libobszap.Logger

// Environment controls the baseline logger profile.
type Environment = libobszap.Environment

// Config contains all required logger initialization inputs.
type Config = libobszap.Config

const (
	// EnvironmentProduction enables production-safe logging defaults.
	EnvironmentProduction = libobszap.EnvironmentProduction
	// EnvironmentStaging enables staging-safe logging defaults.
	EnvironmentStaging = libobszap.EnvironmentStaging
	// EnvironmentUAT enables UAT-safe logging defaults.
	EnvironmentUAT = libobszap.EnvironmentUAT
	// EnvironmentDevelopment enables verbose development logging defaults.
	EnvironmentDevelopment = libobszap.EnvironmentDevelopment
	// EnvironmentLocal enables verbose local-development logging defaults.
	EnvironmentLocal = libobszap.EnvironmentLocal
)

// New creates a structured logger from the given configuration.
func New(cfg Config) (*Logger, error) {
	return libobszap.New(cfg)
}

// Any creates a field with any value.
func Any(key string, value any) Field {
	return libobszap.Any(key, value)
}

// String creates a string field.
func String(key, value string) Field {
	return libobszap.String(key, value)
}

// Int creates an int field.
func Int(key string, value int) Field {
	return libobszap.Int(key, value)
}

// Bool creates a bool field.
func Bool(key string, value bool) Field {
	return libobszap.Bool(key, value)
}

// Duration creates a duration field.
func Duration(key string, value time.Duration) Field {
	return libobszap.Duration(key, value)
}

// ErrorField creates an error field.
func ErrorField(err error) Field {
	return libobszap.ErrorField(err)
}
