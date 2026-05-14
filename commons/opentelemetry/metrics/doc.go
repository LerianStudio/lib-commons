// Package metrics provides a fluent factory for OpenTelemetry metric instruments.
//
// MetricsFactory caches instruments and exposes builder-style APIs for counters,
// gauges, and histograms with low-overhead attribute composition.
//
// Convenience methods (for example RecordTransactionProcessed) are provided for
// common domain metrics used across Lerian services.
//
// Deprecated: This package is a compatibility shim. Import github.com/LerianStudio/lib-observability/metrics instead.
// This package will be removed in a future major version of lib-commons.
package metrics
