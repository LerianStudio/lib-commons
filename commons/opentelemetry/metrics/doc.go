// Package metrics provides a fluent factory for OpenTelemetry metric instruments.
//
// MetricsFactory caches instruments and exposes builder-style APIs for counters,
// gauges, and histograms with low-overhead attribute composition.
// CounterBuilder supports both convenient slice-based attributes
// (WithLabels/WithAttributes) and WithAttributeSet for callers that prebuild an
// attribute.Set once and reuse it on high-frequency Add paths.
//
// Convenience methods (for example RecordTransactionProcessed) are provided for
// common domain metrics used across Lerian services.
package metrics
