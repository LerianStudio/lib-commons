// Package runtime provides panic recovery, safe goroutines, and production mode helpers.
// This package delegates to github.com/LerianStudio/lib-observability/runtime.
package runtime

import libobsruntime "github.com/LerianStudio/lib-observability/runtime"

// PanicPolicy determines how a recovered panic should be handled.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.PanicPolicy instead.
type PanicPolicy = libobsruntime.PanicPolicy

const (
	// KeepRunning logs the panic and stack trace, then continues execution.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.KeepRunning instead.
	KeepRunning = libobsruntime.KeepRunning
	// CrashProcess logs the panic and stack trace, then re-panics to crash the process.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.CrashProcess instead.
	CrashProcess = libobsruntime.CrashProcess
)
