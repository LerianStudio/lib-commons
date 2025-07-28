package opentelemetry

import (
	"strings"
	"sync"

	"github.com/LerianStudio/lib-commons/commons/log"
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

// MetricOption configures metric creation options
type MetricOption struct {
	Description string
	Unit        string
	// For histograms: bucket boundaries
	Buckets []float64
}

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
func (f *MetricsFactory) Counter(name string, opts ...MetricOption) *CounterBuilder {
	var option MetricOption
	if len(opts) > 0 {
		option = opts[0]
	}

	counter := f.getOrCreateCounter(name, option)
	return &CounterBuilder{
		factory: f,
		counter: counter,
		name:    name,
	}
}

// Gauge creates or retrieves a gauge metric and returns a builder for fluent API usage
func (f *MetricsFactory) Gauge(name string, opts ...MetricOption) *GaugeBuilder {
	var option MetricOption
	if len(opts) > 0 {
		option = opts[0]
	}

	gauge := f.getOrCreateGauge(name, option)
	return &GaugeBuilder{
		factory: f,
		gauge:   gauge,
		name:    name,
	}
}

// Histogram creates or retrieves a histogram metric and returns a builder for fluent API usage
func (f *MetricsFactory) Histogram(name string, opts ...MetricOption) *HistogramBuilder {
	var option MetricOption
	if len(opts) > 0 {
		option = opts[0]
	}

	// Set default buckets if not provided
	if option.Buckets == nil {
		if containsSubstring(name, "latency", "duration", "time") {
			option.Buckets = DefaultLatencyBuckets
		} else if containsSubstring(name, "account") {
			option.Buckets = DefaultAccountBuckets
		} else if containsSubstring(name, "transaction") {
			option.Buckets = DefaultTransactionBuckets
		} else {
			option.Buckets = DefaultLatencyBuckets // Default fallback
		}
	}

	histogram := f.getOrCreateHistogram(name, option)
	return &HistogramBuilder{
		factory:   f,
		histogram: histogram,
		name:      name,
	}
}

// getOrCreateCounter lazily creates or retrieves an existing counter
func (f *MetricsFactory) getOrCreateCounter(name string, option MetricOption) metric.Int64Counter {
	if counter, exists := f.counters.Load(name); exists {
		return counter.(metric.Int64Counter)
	}

	// Create new counter with proper options
	counterOpts := []metric.Int64CounterOption{}
	if option.Description != "" {
		counterOpts = append(counterOpts, metric.WithDescription(option.Description))
	}
	if option.Unit != "" {
		counterOpts = append(counterOpts, metric.WithUnit(option.Unit))
	}

	counter, err := f.meter.Int64Counter(name, counterOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Errorf("Failed to create counter metric '%s': %v", name, err)
		}
		// Return nil - builders will handle nil gracefully
		return nil
	}

	// Store in sync.Map for future use
	if actual, loaded := f.counters.LoadOrStore(name, counter); loaded {
		// Another goroutine created it first, use that one
		return actual.(metric.Int64Counter)
	}

	return counter
}

// getOrCreateGauge lazily creates or retrieves an existing gauge
func (f *MetricsFactory) getOrCreateGauge(name string, option MetricOption) metric.Int64Gauge {
	if gauge, exists := f.gauges.Load(name); exists {
		return gauge.(metric.Int64Gauge)
	}

	// Create new gauge with proper options
	gaugeOpts := []metric.Int64GaugeOption{}
	if option.Description != "" {
		gaugeOpts = append(gaugeOpts, metric.WithDescription(option.Description))
	}
	if option.Unit != "" {
		gaugeOpts = append(gaugeOpts, metric.WithUnit(option.Unit))
	}

	gauge, err := f.meter.Int64Gauge(name, gaugeOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Errorf("Failed to create gauge metric '%s': %v", name, err)
		}
		// Return nil - builders will handle nil gracefully
		return nil
	}

	// Store in sync.Map for future use
	if actual, loaded := f.gauges.LoadOrStore(name, gauge); loaded {
		// Another goroutine created it first, use that one
		return actual.(metric.Int64Gauge)
	}

	return gauge
}

// getOrCreateHistogram lazily creates or retrieves an existing histogram
func (f *MetricsFactory) getOrCreateHistogram(name string, option MetricOption) metric.Int64Histogram {
	if histogram, exists := f.histograms.Load(name); exists {
		return histogram.(metric.Int64Histogram)
	}

	// Create new histogram with proper options
	histogramOpts := []metric.Int64HistogramOption{}
	if option.Description != "" {
		histogramOpts = append(histogramOpts, metric.WithDescription(option.Description))
	}
	if option.Unit != "" {
		histogramOpts = append(histogramOpts, metric.WithUnit(option.Unit))
	}
	if option.Buckets != nil {
		histogramOpts = append(histogramOpts, metric.WithExplicitBucketBoundaries(option.Buckets...))
	}

	histogram, err := f.meter.Int64Histogram(name, histogramOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Errorf("Failed to create histogram metric '%s': %v", name, err)
		}
		// Return nil - builders will handle nil gracefully
		return nil
	}

	// Store in sync.Map for future use
	if actual, loaded := f.histograms.LoadOrStore(name, histogram); loaded {
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
