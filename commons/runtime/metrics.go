package runtime

import (
	"context"

	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
	libobsmetrics "github.com/LerianStudio/lib-observability/metrics"
)

// PanicMetrics provides panic-related metrics using OpenTelemetry.
type PanicMetrics = libobsruntime.PanicMetrics

// InitPanicMetrics initializes panic metrics with the provided MetricsFactory.
func InitPanicMetrics(factory *libobsmetrics.MetricsFactory, logger ...Logger) {
	libobsruntime.InitPanicMetrics(factory, logger...)
}

// GetPanicMetrics returns the singleton PanicMetrics instance.
func GetPanicMetrics() *PanicMetrics {
	return libobsruntime.GetPanicMetrics()
}

// ResetPanicMetrics clears the panic metrics singleton.
func ResetPanicMetrics() {
	libobsruntime.ResetPanicMetrics()
}

// RecordPanicRecovered increments the panic_recovered_total counter.
// This is a package-level helper for callers that don't have a PanicMetrics instance.
func recordPanicMetric(ctx context.Context, component, goroutineName string) {
	pm := GetPanicMetrics()
	if pm != nil {
		pm.RecordPanicRecovered(ctx, component, goroutineName)
	}
}
