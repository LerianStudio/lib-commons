// Package log provides mock logger compatibility shims.
// This file re-exports mock types from github.com/LerianStudio/lib-observability/log.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log mocks directly.
// Do NOT run mockgen against this file; it is a hand-maintained compatibility shim.
package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// MockLogger is a mock of Logger interface.
type MockLogger = libobslog.MockLogger

// MockLoggerMockRecorder is the mock recorder for MockLogger.
type MockLoggerMockRecorder = libobslog.MockLoggerMockRecorder

// NewMockLogger creates a new mock instance.
var NewMockLogger = libobslog.NewMockLogger
