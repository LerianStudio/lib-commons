// Package metrics provides an OpenTelemetry-backed metrics factory.
// This package delegates to github.com/LerianStudio/lib-observability/metrics.
package metrics

import (
	libobsmetrics "github.com/LerianStudio/lib-observability/metrics"
	"go.opentelemetry.io/otel/metric"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// MetricsFactory provides a thread-safe factory for creating and managing OpenTelemetry metrics.
type MetricsFactory = libobsmetrics.MetricsFactory

// CounterBuilder provides a fluent API for recording counter metrics.
type CounterBuilder = libobsmetrics.CounterBuilder

// GaugeBuilder provides a fluent API for recording gauge metrics.
type GaugeBuilder = libobsmetrics.GaugeBuilder

// HistogramBuilder provides a fluent API for recording histogram metrics.
type HistogramBuilder = libobsmetrics.HistogramBuilder

// Metric represents a metric definition.
type Metric = libobsmetrics.Metric

var (
	// ErrNilMeter indicates that a nil OTEL meter was provided.
	ErrNilMeter = libobsmetrics.ErrNilMeter
	// ErrNilFactory is returned when a MetricsFactory method is called on a nil receiver.
	ErrNilFactory = libobsmetrics.ErrNilFactory
	// ErrNegativeCounterValue is returned when a negative value is passed to Counter.Add.
	ErrNegativeCounterValue = libobsmetrics.ErrNegativeCounterValue
	// ErrPercentageOutOfRange is returned when a percentage value is outside [0, 100].
	ErrPercentageOutOfRange = libobsmetrics.ErrPercentageOutOfRange
	// ErrNilCounter is returned when a counter builder has no instrument.
	ErrNilCounter = libobsmetrics.ErrNilCounter
	// ErrNilGauge is returned when a gauge builder has no instrument.
	ErrNilGauge = libobsmetrics.ErrNilGauge
	// ErrNilHistogram is returned when a histogram builder has no instrument.
	ErrNilHistogram = libobsmetrics.ErrNilHistogram
	// ErrNilCounterBuilder is returned when a CounterBuilder method is called on a nil receiver.
	ErrNilCounterBuilder = libobsmetrics.ErrNilCounterBuilder
	// ErrNilGaugeBuilder is returned when a GaugeBuilder method is called on a nil receiver.
	ErrNilGaugeBuilder = libobsmetrics.ErrNilGaugeBuilder
	// ErrNilHistogramBuilder is returned when a HistogramBuilder method is called on a nil receiver.
	ErrNilHistogramBuilder = libobsmetrics.ErrNilHistogramBuilder
)

// Pre-configured metrics.
var (
	MetricAccountsCreated       = libobsmetrics.MetricAccountsCreated
	MetricTransactionsProcessed = libobsmetrics.MetricTransactionsProcessed
	MetricTransactionRoutesCreated = libobsmetrics.MetricTransactionRoutesCreated
	MetricOperationRoutesCreated   = libobsmetrics.MetricOperationRoutesCreated
)

// Default histogram bucket configurations.
var (
	DefaultLatencyBuckets     = libobsmetrics.DefaultLatencyBuckets
	DefaultAccountBuckets     = libobsmetrics.DefaultAccountBuckets
	DefaultTransactionBuckets = libobsmetrics.DefaultTransactionBuckets
)

// NewMetricsFactory creates a new MetricsFactory instance.
func NewMetricsFactory(meter metric.Meter, logger log.Logger) (*MetricsFactory, error) {
	return libobsmetrics.NewMetricsFactory(meter, logger)
}

// NewNopFactory returns a MetricsFactory backed by OpenTelemetry's no-op meter.
func NewNopFactory() *MetricsFactory {
	return libobsmetrics.NewNopFactory()
}
