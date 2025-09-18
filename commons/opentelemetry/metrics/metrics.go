package metrics

import (
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

// Default histogram bucket configurations for different metric types
var (
	// DefaultLatencyBuckets for latency measurements (in seconds)
	DefaultLatencyBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

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
		if containsSubstring(m.Name, "latency", "duration", "time") {
			m.Buckets = DefaultLatencyBuckets
		} else if containsSubstring(m.Name, "account") {
			m.Buckets = DefaultAccountBuckets
		} else if containsSubstring(m.Name, "transaction") {
			m.Buckets = DefaultTransactionBuckets
		} else {
			m.Buckets = DefaultLatencyBuckets // Default fallback
		}
	}

	histogram := f.getOrCreateHistogram(m)

	return &HistogramBuilder{
		factory:   f,
		histogram: histogram,
		name:      m.Name,
	}
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

// getOrCreateHistogram lazily creates or retrieves an existing histogram
func (f *MetricsFactory) getOrCreateHistogram(m Metric) metric.Int64Histogram {
	if histogram, exists := f.histograms.Load(m.Name); exists {
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
	if actual, loaded := f.histograms.LoadOrStore(m.Name, histogram); loaded {
		// Another goroutine created it first, use that one
		return actual.(metric.Int64Histogram)
	}

	return histogram
}

// containsSubstring checks if name contains any of the given substrings (case-insensitive)
func containsSubstring(name string, substrings ...string) bool {
	nameL := strings.ToLower(name)
	for _, substr := range substrings {
		if strings.Contains(nameL, strings.ToLower(substr)) {
			return true
		}
	}

	return false
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
