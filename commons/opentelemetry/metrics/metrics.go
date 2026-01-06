package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"go.opentelemetry.io/otel/metric"
)

// MetricsFactory provides a thread-safe factory for creating and managing OpenTelemetry metrics
// with lazy initialization using sync.Map for high-performance concurrent access.
type MetricsFactory struct {
	meter      metric.Meter
	counters   sync.Map // string -> metric.Int64Counter
	gauges     sync.Map // string -> metric.Int64Gauge
	histograms sync.Map // string -> metric.Int64Histogram
	logger     log.Logger
}

// Metric represents a metric that can be collected by the server.
type Metric struct {
	Name        string
	Description string
	Unit        string
	// For histograms: bucket boundaries
	Buckets []float64
}

// Pre-configured metrics that can be used to create metrics with default options.
var (
	// MetricAccountsCreated is a metric that measures the number of accounts created by the server.
	MetricAccountsCreated = Metric{
		Name:        "accounts_created",
		Unit:        "1",
		Description: "Measures the number of accounts created by the server.",
	}

	// MetricTransactionsProcessed is a metric that measures the number of transactions processed by the server.
	MetricTransactionsProcessed = Metric{
		Name:        "transactions_processed",
		Unit:        "1",
		Description: "Measures the number of transactions processed by the server.",
	}

	// MetricTransactionRoutesCreated is a metric that measures the number of transaction routes created by the server.
	MetricTransactionRoutesCreated = Metric{
		Name:        "transaction_routes_created",
		Unit:        "1",
		Description: "Measures the number of transaction routes created by the server.",
	}

	// MetricOperationRoutesCreated is a metric that measures the number of operation routes created by the server.
	MetricOperationRoutesCreated = Metric{
		Name:        "operation_routes_created",
		Unit:        "1",
		Description: "Measures the number of operation routes created by the server.",
	}
)

// Default histogram bucket configurations for different metric types.
// Note: These are float64 for OpenTelemetry compatibility, but values represent
// milliseconds since Int64Histogram is used. For sub-millisecond precision,
// consider using Float64Histogram with values in seconds.
var (
	// DefaultLatencyBuckets for latency measurements (in milliseconds for Int64Histogram)
	DefaultLatencyBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

	// DefaultAccountBuckets for account creation counts
	DefaultAccountBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

	// DefaultTransactionBuckets for transaction count per time period
	DefaultTransactionBuckets = []float64{1, 10, 50, 100, 500, 1000, 2500, 5000, 8000, 10000}
)

// NewMetricsFactory creates a new MetricsFactory instance
func NewMetricsFactory(meter metric.Meter, logger log.Logger) *MetricsFactory {
	return &MetricsFactory{
		meter:  meter,
		logger: logger,
	}
}

// Counter creates or retrieves a counter metric and returns a builder for fluent API usage
func (f *MetricsFactory) Counter(m Metric) *CounterBuilder {
	counter := f.getOrCreateCounter(m)

	return &CounterBuilder{
		factory: f,
		counter: counter,
		name:    m.Name,
	}
}

// Gauge creates or retrieves a gauge metric and returns a builder for fluent API usage
func (f *MetricsFactory) Gauge(m Metric) *GaugeBuilder {
	gauge := f.getOrCreateGauge(m)

	return &GaugeBuilder{
		factory: f,
		gauge:   gauge,
		name:    m.Name,
	}
}

// Histogram creates or retrieves a histogram metric and returns a builder for fluent API usage
func (f *MetricsFactory) Histogram(m Metric) *HistogramBuilder {
	// Set default buckets if not provided
	if m.Buckets == nil {
		m.Buckets = selectDefaultBuckets(m.Name)
	}

	histogram := f.getOrCreateHistogram(m)

	return &HistogramBuilder{
		factory:   f,
		histogram: histogram,
		name:      m.Name,
	}
}

// selectDefaultBuckets chooses default buckets based on metric name.
// Uses exact match first, then checks for substrings in a deterministic order.
func selectDefaultBuckets(name string) []float64 {
	nameL := strings.ToLower(name)

	// Check substrings in deterministic priority order
	// More specific patterns first, general patterns last
	patterns := []struct {
		substr  string
		buckets []float64
	}{
		{"latency", DefaultLatencyBuckets},
		{"duration", DefaultLatencyBuckets},
		{"time", DefaultLatencyBuckets},
		{"account", DefaultAccountBuckets},
		{"transaction", DefaultTransactionBuckets},
	}

	for _, p := range patterns {
		if strings.Contains(nameL, p.substr) {
			return p.buckets
		}
	}

	return DefaultLatencyBuckets
}

// getOrCreateCounter lazily creates or retrieves an existing counter
func (f *MetricsFactory) getOrCreateCounter(m Metric) metric.Int64Counter {
	if counter, exists := f.counters.Load(m.Name); exists {
		return counter.(metric.Int64Counter)
	}

	// Create new counter with proper options
	counterOpts := f.addCounterOptions(m)

	counter, err := f.meter.Int64Counter(m.Name, counterOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Errorf("Failed to create counter metric '%s': %v", m.Name, err)
		}
		// Return nil - builders will handle nil gracefully
		return nil
	}

	// Store in sync.Map for future use
	if actual, loaded := f.counters.LoadOrStore(m.Name, counter); loaded {
		// Another goroutine created it first, use that one
		return actual.(metric.Int64Counter)
	}

	return counter
}

// getOrCreateGauge lazily creates or retrieves an existing gauge
func (f *MetricsFactory) getOrCreateGauge(m Metric) metric.Int64Gauge {
	if gauge, exists := f.gauges.Load(m.Name); exists {
		return gauge.(metric.Int64Gauge)
	}

	// Create new gauge with proper options
	gaugeOpts := f.addGaugeOptions(m)

	gauge, err := f.meter.Int64Gauge(m.Name, gaugeOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Errorf("Failed to create gauge metric '%s': %v", m.Name, err)
		}
		// Return nil - builders will handle nil gracefully
		return nil
	}

	// Store in sync.Map for future use
	if actual, loaded := f.gauges.LoadOrStore(m.Name, gauge); loaded {
		// Another goroutine created it first, use that one
		return actual.(metric.Int64Gauge)
	}

	return gauge
}

// getOrCreateHistogram lazily creates or retrieves an existing histogram.
// Uses a composite key (name + buckets hash) to ensure different bucket configs
// result in different histograms.
func (f *MetricsFactory) getOrCreateHistogram(m Metric) metric.Int64Histogram {
	cacheKey := histogramCacheKey(m.Name, m.Buckets)

	if histogram, exists := f.histograms.Load(cacheKey); exists {
		return histogram.(metric.Int64Histogram)
	}

	// Create new histogram with proper options
	histogramOpts := f.addHistogramOptions(m)

	histogram, err := f.meter.Int64Histogram(m.Name, histogramOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Errorf("Failed to create histogram metric '%s': %v", m.Name, err)
		}
		// Return nil - builders will handle nil gracefully
		return nil
	}

	// Store in sync.Map for future use
	if actual, loaded := f.histograms.LoadOrStore(cacheKey, histogram); loaded {
		// Another goroutine created it first, use that one
		return actual.(metric.Int64Histogram)
	}

	return histogram
}

// histogramCacheKey generates a unique cache key based on name and bucket configuration.
func histogramCacheKey(name string, buckets []float64) string {
	if len(buckets) == 0 {
		return name
	}

	sortedBuckets := make([]float64, len(buckets))
	copy(sortedBuckets, buckets)
	sort.Float64s(sortedBuckets)

	return fmt.Sprintf("%s:%v", name, sortedBuckets)
}

func (f *MetricsFactory) addCounterOptions(m Metric) []metric.Int64CounterOption {
	opts := []metric.Int64CounterOption{}
	if m.Description != "" {
		opts = append(opts, metric.WithDescription(m.Description))
	}

	if m.Unit != "" {
		opts = append(opts, metric.WithUnit(m.Unit))
	}

	return opts
}

func (f *MetricsFactory) addGaugeOptions(m Metric) []metric.Int64GaugeOption {
	opts := []metric.Int64GaugeOption{}
	if m.Description != "" {
		opts = append(opts, metric.WithDescription(m.Description))
	}

	if m.Unit != "" {
		opts = append(opts, metric.WithUnit(m.Unit))
	}

	return opts
}

func (f *MetricsFactory) addHistogramOptions(m Metric) []metric.Int64HistogramOption {
	opts := []metric.Int64HistogramOption{}
	if m.Description != "" {
		opts = append(opts, metric.WithDescription(m.Description))
	}

	if m.Unit != "" {
		opts = append(opts, metric.WithUnit(m.Unit))
	}

	if m.Buckets != nil {
		opts = append(opts, metric.WithExplicitBucketBoundaries(m.Buckets...))
	}

	return opts
}
