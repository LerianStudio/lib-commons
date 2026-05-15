package metrics

import libobsmetrics "github.com/LerianStudio/lib-observability/metrics"

var (
	// MetricSystemCPUUsage is a gauge that records the current CPU usage percentage.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricSystemCPUUsage instead.
	MetricSystemCPUUsage = libobsmetrics.MetricSystemCPUUsage

	// MetricSystemMemUsage is a gauge that records the current memory usage percentage.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricSystemMemUsage instead.
	MetricSystemMemUsage = libobsmetrics.MetricSystemMemUsage
)
