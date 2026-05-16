package runtime

import libobsruntime "github.com/LerianStudio/lib-observability/runtime"

// ErrorReporter defines an interface for external error reporting services.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.ErrorReporter instead.
type ErrorReporter = libobsruntime.ErrorReporter

// SetErrorReporter configures the global error reporter for panic reporting.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.SetErrorReporter instead.
func SetErrorReporter(reporter ErrorReporter) {
	libobsruntime.SetErrorReporter(reporter)
}

// GetErrorReporter returns the currently configured error reporter.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.GetErrorReporter instead.
func GetErrorReporter() ErrorReporter {
	return libobsruntime.GetErrorReporter()
}

// SetProductionMode enables or disables production mode for error reporting.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.SetProductionMode instead.
func SetProductionMode(enabled bool) {
	libobsruntime.SetProductionMode(enabled)
}

// IsProductionMode returns whether production mode is enabled.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.IsProductionMode instead.
func IsProductionMode() bool {
	return libobsruntime.IsProductionMode()
}
