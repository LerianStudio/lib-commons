package runtime

import libobsruntime "github.com/LerianStudio/lib-observability/runtime"

// ErrorReporter defines an interface for external error reporting services.
type ErrorReporter = libobsruntime.ErrorReporter

// SetErrorReporter configures the global error reporter for panic reporting.
func SetErrorReporter(reporter ErrorReporter) {
	libobsruntime.SetErrorReporter(reporter)
}

// GetErrorReporter returns the currently configured error reporter.
func GetErrorReporter() ErrorReporter {
	return libobsruntime.GetErrorReporter()
}

// SetProductionMode enables or disables production mode for error reporting.
func SetProductionMode(enabled bool) {
	libobsruntime.SetProductionMode(enabled)
}

// IsProductionMode returns whether production mode is enabled.
func IsProductionMode() bool {
	return libobsruntime.IsProductionMode()
}
