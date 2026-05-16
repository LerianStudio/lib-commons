// Package metrics provides an OpenTelemetry-backed metrics factory.
// This package delegates to github.com/LerianStudio/lib-observability/metrics.
package metrics

import (
	libobslog "github.com/LerianStudio/lib-observability/log"
	libobsmetrics "github.com/LerianStudio/lib-observability/metrics"
	"go.opentelemetry.io/otel/metric"
)

// MetricsFactory provides a thread-safe factory for creating and managing OpenTelemetry metrics.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricsFactory instead.
type MetricsFactory = libobsmetrics.MetricsFactory

// CounterBuilder provides a fluent API for recording counter metrics.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.CounterBuilder instead.
type CounterBuilder = libobsmetrics.CounterBuilder

// GaugeBuilder provides a fluent API for recording gauge metrics.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.GaugeBuilder instead.
type GaugeBuilder = libobsmetrics.GaugeBuilder

// HistogramBuilder provides a fluent API for recording histogram metrics.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.HistogramBuilder instead.
type HistogramBuilder = libobsmetrics.HistogramBuilder

// Metric represents a metric definition.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.Metric instead.
type Metric = libobsmetrics.Metric

var (
	// ErrNilMeter indicates that a nil OTEL meter was provided.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilMeter instead.
	ErrNilMeter = libobsmetrics.ErrNilMeter
	// ErrNilFactory is returned when a MetricsFactory method is called on a nil receiver.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilFactory instead.
	ErrNilFactory = libobsmetrics.ErrNilFactory
	// ErrNegativeCounterValue is returned when a negative value is passed to Counter.Add.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNegativeCounterValue instead.
	ErrNegativeCounterValue = libobsmetrics.ErrNegativeCounterValue
	// ErrPercentageOutOfRange is returned when a percentage value is outside [0, 100].
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrPercentageOutOfRange instead.
	ErrPercentageOutOfRange = libobsmetrics.ErrPercentageOutOfRange
	// ErrNilCounter is returned when a counter builder has no instrument.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilCounter instead.
	ErrNilCounter = libobsmetrics.ErrNilCounter
	// ErrNilGauge is returned when a gauge builder has no instrument.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilGauge instead.
	ErrNilGauge = libobsmetrics.ErrNilGauge
	// ErrNilHistogram is returned when a histogram builder has no instrument.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilHistogram instead.
	ErrNilHistogram = libobsmetrics.ErrNilHistogram
	// ErrNilCounterBuilder is returned when a CounterBuilder method is called on a nil receiver.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilCounterBuilder instead.
	ErrNilCounterBuilder = libobsmetrics.ErrNilCounterBuilder
	// ErrNilGaugeBuilder is returned when a GaugeBuilder method is called on a nil receiver.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilGaugeBuilder instead.
	ErrNilGaugeBuilder = libobsmetrics.ErrNilGaugeBuilder
	// ErrNilHistogramBuilder is returned when a HistogramBuilder method is called on a nil receiver.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.ErrNilHistogramBuilder instead.
	ErrNilHistogramBuilder = libobsmetrics.ErrNilHistogramBuilder
)

// Pre-configured metrics.
var (
	// MetricAccountsCreated is the pre-configured metric for account creation events.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricAccountsCreated instead.
	MetricAccountsCreated = libobsmetrics.MetricAccountsCreated
	// MetricTransactionsProcessed is the pre-configured metric for transaction processing events.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricTransactionsProcessed instead.
	MetricTransactionsProcessed = libobsmetrics.MetricTransactionsProcessed
	// MetricTransactionRoutesCreated is the pre-configured metric for transaction route creation events.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricTransactionRoutesCreated instead.
	MetricTransactionRoutesCreated = libobsmetrics.MetricTransactionRoutesCreated
	// MetricOperationRoutesCreated is the pre-configured metric for operation route creation events.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.MetricOperationRoutesCreated instead.
	MetricOperationRoutesCreated = libobsmetrics.MetricOperationRoutesCreated
)

// Default histogram bucket configurations.
var (
	// DefaultLatencyBuckets provides default bucket boundaries for latency histograms.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.DefaultLatencyBuckets instead.
	DefaultLatencyBuckets = libobsmetrics.DefaultLatencyBuckets
	// DefaultAccountBuckets provides default bucket boundaries for account-related histograms.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.DefaultAccountBuckets instead.
	DefaultAccountBuckets = libobsmetrics.DefaultAccountBuckets
	// DefaultTransactionBuckets provides default bucket boundaries for transaction-related histograms.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.DefaultTransactionBuckets instead.
	DefaultTransactionBuckets = libobsmetrics.DefaultTransactionBuckets
)

// NewMetricsFactory creates a new MetricsFactory instance.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.NewMetricsFactory instead.
func NewMetricsFactory(meter metric.Meter, logger libobslog.Logger) (*MetricsFactory, error) {
	return libobsmetrics.NewMetricsFactory(meter, logger)
}

// NewNopFactory returns a MetricsFactory backed by OpenTelemetry's no-op meter.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/metrics.NewNopFactory instead.
func NewNopFactory() *MetricsFactory {
	return libobsmetrics.NewNopFactory()
}
