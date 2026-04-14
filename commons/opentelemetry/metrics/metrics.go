package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
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

var (
	// ErrNilMeter indicates that a nil OTEL meter was provided.
	ErrNilMeter = errors.New("metric meter cannot be nil")
	// ErrNilFactory is returned when a MetricsFactory method is called on a nil receiver.
	ErrNilFactory = errors.New("metrics factory is nil")
	// ErrNegativeCounterValue is returned when a negative value is passed to Counter.Add.
	ErrNegativeCounterValue = errors.New("counter value must not be negative")
	// ErrPercentageOutOfRange is returned when a percentage value is outside [0, 100].
	ErrPercentageOutOfRange = errors.New("percentage value must be between 0 and 100")
)

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
// Values are in seconds for consistency with OpenTelemetry conventions.
var (
	// DefaultLatencyBuckets for latency measurements (in seconds)
	DefaultLatencyBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

	// DefaultAccountBuckets for account creation counts
	DefaultAccountBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

	// DefaultTransactionBuckets for transaction count per time period
	DefaultTransactionBuckets = []float64{1, 10, 50, 100, 500, 1000, 2500, 5000, 8000, 10000}
)

// NewMetricsFactory creates a new MetricsFactory instance.
func NewMetricsFactory(meter metric.Meter, logger log.Logger) (*MetricsFactory, error) {
	if meter == nil {
		return nil, ErrNilMeter
	}

	return &MetricsFactory{
		meter:  meter,
		logger: logger,
	}, nil
}

// NewNopFactory returns a MetricsFactory backed by OpenTelemetry's no-op meter.
// It is safe for use as a fallback when a real meter is unavailable.
func NewNopFactory() *MetricsFactory {
	return &MetricsFactory{
		meter:  noop.NewMeterProvider().Meter("nop"),
		logger: log.NewNop(),
	}
}

// Counter creates or retrieves a counter metric and returns a builder for fluent API usage
func (f *MetricsFactory) Counter(m Metric) (*CounterBuilder, error) {
	if f == nil {
		return nil, ErrNilFactory
	}

	counter, err := f.getOrCreateCounter(m)
	if err != nil {
		return nil, err
	}

	return &CounterBuilder{
		factory: f,
		counter: counter,
		name:    m.Name,
	}, nil
}

// Gauge creates or retrieves a gauge metric and returns a builder for fluent API usage
func (f *MetricsFactory) Gauge(m Metric) (*GaugeBuilder, error) {
	if f == nil {
		return nil, ErrNilFactory
	}

	gauge, err := f.getOrCreateGauge(m)
	if err != nil {
		return nil, err
	}

	return &GaugeBuilder{
		factory: f,
		gauge:   gauge,
		name:    m.Name,
	}, nil
}

// Histogram creates or retrieves a histogram metric and returns a builder for fluent API usage
func (f *MetricsFactory) Histogram(m Metric) (*HistogramBuilder, error) {
	if f == nil {
		return nil, ErrNilFactory
	}

	// Set default buckets if not provided
	if m.Buckets == nil {
		m.Buckets = selectDefaultBuckets(m.Name)
	}

	histogram, err := f.getOrCreateHistogram(m)
	if err != nil {
		return nil, err
	}

	return &HistogramBuilder{
		factory:   f,
		histogram: histogram,
		name:      m.Name,
	}, nil
}

// selectDefaultBuckets chooses default buckets based on metric name.
// Uses exact match first, then checks for substrings in a deterministic order.
func selectDefaultBuckets(name string) []float64 {
	nameL := strings.ToLower(name)

	// Check substrings in deterministic priority order.
	// Latency/duration/time patterns first to avoid "transaction_latency"
	// matching "transaction" instead of "latency".
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
func (f *MetricsFactory) getOrCreateCounter(m Metric) (metric.Int64Counter, error) {
	if f == nil {
		return nil, ErrNilFactory
	}

	if counter, exists := f.counters.Load(m.Name); exists {
		if c, ok := counter.(metric.Int64Counter); ok {
			return c, nil
		}

		return nil, fmt.Errorf("counter cache contains invalid type for %q", m.Name)
	}

	// Create new counter with proper options
	counterOpts := f.addCounterOptions(m)

	counter, err := f.meter.Int64Counter(m.Name, counterOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Log(context.Background(), log.LevelError, "failed to create counter metric", log.String("metric_name", m.Name), log.Err(err))
		}

		return nil, fmt.Errorf("create counter %q: %w", m.Name, err)
	}

	// Store in sync.Map for future use
	if actual, loaded := f.counters.LoadOrStore(m.Name, counter); loaded {
		// Another goroutine created it first, use that one
		if c, ok := actual.(metric.Int64Counter); ok {
			return c, nil
		}

		return nil, fmt.Errorf("counter cache contains invalid type for %q", m.Name)
	}

	return counter, nil
}

// getOrCreateGauge lazily creates or retrieves an existing gauge
func (f *MetricsFactory) getOrCreateGauge(m Metric) (metric.Int64Gauge, error) {
	if f == nil {
		return nil, ErrNilFactory
	}

	if gauge, exists := f.gauges.Load(m.Name); exists {
		if g, ok := gauge.(metric.Int64Gauge); ok {
			return g, nil
		}

		return nil, fmt.Errorf("gauge cache contains invalid type for %q", m.Name)
	}

	// Create new gauge with proper options
	gaugeOpts := f.addGaugeOptions(m)

	gauge, err := f.meter.Int64Gauge(m.Name, gaugeOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Log(context.Background(), log.LevelError, "failed to create gauge metric", log.String("metric_name", m.Name), log.Err(err))
		}

		return nil, fmt.Errorf("create gauge %q: %w", m.Name, err)
	}

	// Store in sync.Map for future use
	if actual, loaded := f.gauges.LoadOrStore(m.Name, gauge); loaded {
		// Another goroutine created it first, use that one
		if g, ok := actual.(metric.Int64Gauge); ok {
			return g, nil
		}

		return nil, fmt.Errorf("gauge cache contains invalid type for %q", m.Name)
	}

	return gauge, nil
}

// getOrCreateHistogram lazily creates or retrieves an existing histogram.
// Uses a composite key (name + buckets hash) to ensure different bucket configs
// result in different histograms.
func (f *MetricsFactory) getOrCreateHistogram(m Metric) (metric.Int64Histogram, error) {
	if f == nil {
		return nil, ErrNilFactory
	}

	// Sort buckets before both cache key computation and instrument creation
	// to ensure the instrument configuration matches the cache key.
	if len(m.Buckets) > 1 {
		sorted := make([]float64, len(m.Buckets))
		copy(sorted, m.Buckets)
		sort.Float64s(sorted)
		m.Buckets = sorted
	}

	cacheKey := histogramCacheKey(m.Name, m.Buckets)

	if histogram, exists := f.histograms.Load(cacheKey); exists {
		if h, ok := histogram.(metric.Int64Histogram); ok {
			return h, nil
		}

		return nil, fmt.Errorf("histogram cache contains invalid type for %q", cacheKey)
	}

	// Create new histogram with proper options
	histogramOpts := f.addHistogramOptions(m)

	histogram, err := f.meter.Int64Histogram(m.Name, histogramOpts...)
	if err != nil {
		if f.logger != nil {
			f.logger.Log(context.Background(), log.LevelError, "failed to create histogram metric", log.String("metric_name", m.Name), log.Err(err))
		}

		return nil, fmt.Errorf("create histogram %q: %w", m.Name, err)
	}

	// Store in sync.Map for future use
	if actual, loaded := f.histograms.LoadOrStore(cacheKey, histogram); loaded {
		// Another goroutine created it first, use that one
		if h, ok := actual.(metric.Int64Histogram); ok {
			return h, nil
		}

		return nil, fmt.Errorf("histogram cache contains invalid type for %q", cacheKey)
	}

	return histogram, nil
}

// histogramCacheKey generates a unique cache key based on name and bucket configuration.
func histogramCacheKey(name string, buckets []float64) string {
	if len(buckets) == 0 {
		return name
	}

	sortedBuckets := make([]float64, len(buckets))
	copy(sortedBuckets, buckets)
	sort.Float64s(sortedBuckets)

	bucketStrings := make([]string, len(sortedBuckets))
	for i, b := range sortedBuckets {
		bucketStrings[i] = strconv.FormatFloat(b, 'g', -1, 64)
	}

	return fmt.Sprintf("%s:%s", name, strings.Join(bucketStrings, ","))
}

func (f *MetricsFactory) addCounterOptions(m Metric) []metric.Int64CounterOption {
	var opts []metric.Int64CounterOption
	if m.Description != "" {
		opts = append(opts, metric.WithDescription(m.Description))
	}

	if m.Unit != "" {
		opts = append(opts, metric.WithUnit(m.Unit))
	}

	return opts
}

func (f *MetricsFactory) addGaugeOptions(m Metric) []metric.Int64GaugeOption {
	var opts []metric.Int64GaugeOption
	if m.Description != "" {
		opts = append(opts, metric.WithDescription(m.Description))
	}

	if m.Unit != "" {
		opts = append(opts, metric.WithUnit(m.Unit))
	}

	return opts
}

func (f *MetricsFactory) addHistogramOptions(m Metric) []metric.Int64HistogramOption {
	var opts []metric.Int64HistogramOption
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
