package runtime

import (
	libobsmetrics "github.com/LerianStudio/lib-observability/metrics"
	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
)

// PanicMetrics provides panic-related metrics using OpenTelemetry.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.PanicMetrics instead.
type PanicMetrics = libobsruntime.PanicMetrics

// InitPanicMetrics initializes panic metrics with the provided MetricsFactory.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.InitPanicMetrics instead.
func InitPanicMetrics(factory *libobsmetrics.MetricsFactory, logger ...Logger) {
	libobsruntime.InitPanicMetrics(factory, logger...)
}

// GetPanicMetrics returns the singleton PanicMetrics instance.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.GetPanicMetrics instead.
func GetPanicMetrics() *PanicMetrics {
	return libobsruntime.GetPanicMetrics()
}

// ResetPanicMetrics clears the panic metrics singleton.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.ResetPanicMetrics instead.
func ResetPanicMetrics() {
	libobsruntime.ResetPanicMetrics()
}
