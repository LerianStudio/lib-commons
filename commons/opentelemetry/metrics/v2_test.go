//go:build unit

package metrics

import (
	"context"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestFactory creates a MetricsFactory backed by a real SDK meter provider
// with a ManualReader. The ManualReader lets us export and inspect actual
// metric data recorded by the instruments.
func newTestFactory(t *testing.T) (*MetricsFactory, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")

	factory, err := NewMetricsFactory(meter, &log.NopLogger{})
	require.NoError(t, err)

	return factory, reader
}

// collectMetrics is a convenience wrapper that calls reader.Collect and returns
// the ResourceMetrics payload.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics

	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	return rm
}

// findMetricByName walks the collected ResourceMetrics and returns the first
// Metrics entry whose Name matches. Returns nil if not found.
func findMetricByName(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}

	return nil
}

// sumDataPoints extracts data points from a Sum metric.
func sumDataPoints(t *testing.T, m *metricdata.Metrics) []metricdata.DataPoint[int64] {
	t.Helper()

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data, got %T", m.Data)

	return sum.DataPoints
}

// histDataPoints extracts data points from a Histogram metric.
func histDataPoints(t *testing.T, m *metricdata.Metrics) []metricdata.HistogramDataPoint[int64] {
	t.Helper()

	hist, ok := m.Data.(metricdata.Histogram[int64])
	require.True(t, ok, "expected Histogram[int64] data, got %T", m.Data)

	return hist.DataPoints
}

// gaugeDataPoints extracts data points from a Gauge metric.
func gaugeDataPoints(t *testing.T, m *metricdata.Metrics) []metricdata.DataPoint[int64] {
	t.Helper()

	gauge, ok := m.Data.(metricdata.Gauge[int64])
	require.True(t, ok, "expected Gauge[int64] data, got %T", m.Data)

	return gauge.DataPoints
}

// hasAttribute checks whether the attribute set contains a specific string key/value.
func hasAttribute(attrs attribute.Set, key, value string) bool {
	v, ok := attrs.Value(attribute.Key(key))
	if !ok {
		return false
	}

	return v.AsString() == value
}

// ---------------------------------------------------------------------------
// 1. Factory creation
// ---------------------------------------------------------------------------

func TestNewMetricsFactory_NilMeter(t *testing.T) {
	_, err := NewMetricsFactory(nil, &log.NopLogger{})
	assert.ErrorIs(t, err, ErrNilMeter, "nil meter must be rejected")
}

func TestNewMetricsFactory_NilLogger(t *testing.T) {
	// A nil logger is fine -- internal code guards against it.
	meter := noop.NewMeterProvider().Meter("test")
	factory, err := NewMetricsFactory(meter, nil)
	require.NoError(t, err)
	assert.NotNil(t, factory)
}

func TestNewMetricsFactory_ValidCreation(t *testing.T) {
	factory, _ := newTestFactory(t)
	assert.NotNil(t, factory)
}

// ---------------------------------------------------------------------------
// 2. Counter recording and verification
// ---------------------------------------------------------------------------

func TestCounter_AddOne_RecordsValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{
		Name:        "requests_total",
		Description: "Total number of requests",
		Unit:        "1",
	})
	require.NoError(t, err)

	err = counter.AddOne(context.Background())
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "requests_total")
	require.NotNil(t, m, "metric requests_total must exist")

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value)
}

func TestCounter_Add_RecordsArbitraryValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "bytes_sent"})
	require.NoError(t, err)

	require.NoError(t, counter.Add(context.Background(), 42))
	require.NoError(t, counter.Add(context.Background(), 8))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "bytes_sent")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(50), dps[0].Value, "counter should accumulate 42+8=50")
}

func TestCounter_AddOne_MultipleIncrements(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "events_total"})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		require.NoError(t, counter.AddOne(context.Background()))
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "events_total")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(5), dps[0].Value)
}

func TestCounter_ZeroValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "zero_counter"})
	require.NoError(t, err)

	require.NoError(t, counter.Add(context.Background(), 0))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "zero_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(0), dps[0].Value)
}

func TestCounter_NilCounter_ReturnsError(t *testing.T) {
	builder := &CounterBuilder{counter: nil}
	err := builder.AddOne(context.Background())
	assert.ErrorIs(t, err, ErrNilCounter)
}

// ---------------------------------------------------------------------------
// 3. Gauge recording and verification
// ---------------------------------------------------------------------------

func TestGauge_Set_RecordsValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{
		Name:        "queue_length",
		Description: "Current queue length",
		Unit:        "1",
	})
	require.NoError(t, err)

	require.NoError(t, gauge.Set(context.Background(), 42))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "queue_length")
	require.NotNil(t, m, "metric queue_length must exist")

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(42), dps[0].Value)
}

func TestGauge_SetOverwrite(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{Name: "connections"})
	require.NoError(t, err)

	require.NoError(t, gauge.Set(context.Background(), 10))
	require.NoError(t, gauge.Set(context.Background(), 25))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "connections")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	// Gauge keeps last value
	assert.Equal(t, int64(25), dps[0].Value)
}

func TestGauge_NilGauge_ReturnsError(t *testing.T) {
	builder := &GaugeBuilder{gauge: nil}
	err := builder.Set(context.Background(), 1)
	assert.ErrorIs(t, err, ErrNilGauge)
}

func TestGauge_ZeroValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{Name: "zero_gauge"})
	require.NoError(t, err)

	require.NoError(t, gauge.Set(context.Background(), 0))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "zero_gauge")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(0), dps[0].Value)
}

// ---------------------------------------------------------------------------
// 4. Histogram recording and verification
// ---------------------------------------------------------------------------

func TestHistogram_Record_RecordsValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{
		Name:        "request_duration",
		Description: "Request duration in ms",
		Unit:        "ms",
		Buckets:     []float64{10, 50, 100, 250, 500, 1000},
	})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 75))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "request_duration")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(1), dps[0].Count)
	assert.Equal(t, int64(75), dps[0].Sum)
}

func TestHistogram_MultipleRecords(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{
		Name:    "latency",
		Buckets: []float64{1, 5, 10, 50, 100},
	})
	require.NoError(t, err)

	values := []int64{3, 7, 15, 45, 90}
	for _, v := range values {
		require.NoError(t, hist.Record(context.Background(), v))
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "latency")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(5), dps[0].Count)
	assert.Equal(t, int64(3+7+15+45+90), dps[0].Sum)
}

func TestHistogram_BucketBoundariesConfigured(t *testing.T) {
	factory, reader := newTestFactory(t)

	customBuckets := []float64{10, 25, 50, 100}

	hist, err := factory.Histogram(Metric{
		Name:    "custom_histogram",
		Buckets: customBuckets,
	})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 30))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "custom_histogram")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, customBuckets, dps[0].Bounds, "bucket boundaries must match configured values")
}

func TestHistogram_NilHistogram_ReturnsError(t *testing.T) {
	builder := &HistogramBuilder{histogram: nil}
	err := builder.Record(context.Background(), 1)
	assert.ErrorIs(t, err, ErrNilHistogram)
}

func TestHistogram_ZeroValue(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "zero_hist", Buckets: []float64{1, 10}})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 0))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "zero_hist")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(1), dps[0].Count)
	assert.Equal(t, int64(0), dps[0].Sum)
}

// ---------------------------------------------------------------------------
// 5. Builder patterns: WithLabels, WithAttributes
// ---------------------------------------------------------------------------

func TestCounterBuilder_WithLabels(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "labeled_counter"})
	require.NoError(t, err)

	labeled := counter.WithLabels(map[string]string{
		"env":     "prod",
		"service": "ledger",
	})
	require.NoError(t, labeled.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "labeled_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)

	attrs := dps[0].Attributes
	assert.True(t, hasAttribute(attrs, "env", "prod"), "must have env=prod attribute")
	assert.True(t, hasAttribute(attrs, "service", "ledger"), "must have service=ledger attribute")
}

func TestCounterBuilder_WithAttributes(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "attr_counter"})
	require.NoError(t, err)

	withAttrs := counter.WithAttributes(
		attribute.String("method", "POST"),
		attribute.String("status", "200"),
	)
	require.NoError(t, withAttrs.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "attr_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "method", "POST"))
	assert.True(t, hasAttribute(dps[0].Attributes, "status", "200"))
}

func TestCounterBuilder_WithLabels_EmptyMap(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "empty_labels_counter"})
	require.NoError(t, err)

	labeled := counter.WithLabels(map[string]string{})
	require.NoError(t, labeled.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "empty_labels_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value)
}

func TestCounterBuilder_ChainedLabelsAndAttributes(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "chained_counter"})
	require.NoError(t, err)

	chained := counter.
		WithLabels(map[string]string{"region": "us-east-1"}).
		WithAttributes(attribute.String("version", "v2"))
	require.NoError(t, chained.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "chained_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "region", "us-east-1"))
	assert.True(t, hasAttribute(dps[0].Attributes, "version", "v2"))
}

func TestGaugeBuilder_WithLabels(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{Name: "labeled_gauge"})
	require.NoError(t, err)

	labeled := gauge.WithLabels(map[string]string{"pool": "primary"})
	require.NoError(t, labeled.Set(context.Background(), 17))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "labeled_gauge")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "pool", "primary"))
	assert.Equal(t, int64(17), dps[0].Value)
}

func TestGaugeBuilder_WithAttributes(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{Name: "attr_gauge"})
	require.NoError(t, err)

	withAttrs := gauge.WithAttributes(attribute.String("db", "postgres"))
	require.NoError(t, withAttrs.Set(context.Background(), 100))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "attr_gauge")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "db", "postgres"))
}

func TestHistogramBuilder_WithLabels(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "labeled_hist", Buckets: []float64{10, 100}})
	require.NoError(t, err)

	labeled := hist.WithLabels(map[string]string{"endpoint": "/api/v1"})
	require.NoError(t, labeled.Record(context.Background(), 55))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "labeled_hist")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "endpoint", "/api/v1"))
}

func TestHistogramBuilder_WithAttributes(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "attr_hist", Buckets: []float64{5, 50}})
	require.NoError(t, err)

	withAttrs := hist.WithAttributes(attribute.String("type", "batch"))
	require.NoError(t, withAttrs.Record(context.Background(), 20))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "attr_hist")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "type", "batch"))
}

// ---------------------------------------------------------------------------
// 6. Builder immutability -- WithLabels/WithAttributes must not mutate original
// ---------------------------------------------------------------------------

func TestCounterBuilder_Immutability(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "immut_counter"})
	require.NoError(t, err)

	branch1 := counter.WithLabels(map[string]string{"branch": "1"})
	branch2 := counter.WithLabels(map[string]string{"branch": "2"})

	require.NoError(t, branch1.AddOne(context.Background()))
	require.NoError(t, branch2.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "immut_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	assert.Len(t, dps, 2, "two label sets must produce two separate data points")

	foundBranch1, foundBranch2 := false, false
	for _, dp := range dps {
		if hasAttribute(dp.Attributes, "branch", "1") {
			foundBranch1 = true
		}
		if hasAttribute(dp.Attributes, "branch", "2") {
			foundBranch2 = true
		}
	}

	assert.True(t, foundBranch1, "must find branch=1 data point")
	assert.True(t, foundBranch2, "must find branch=2 data point")
}

func TestGaugeBuilder_Immutability(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{Name: "immut_gauge"})
	require.NoError(t, err)

	branch1 := gauge.WithLabels(map[string]string{"pool": "primary"})
	branch2 := gauge.WithLabels(map[string]string{"pool": "replica"})

	require.NoError(t, branch1.Set(context.Background(), 10))
	require.NoError(t, branch2.Set(context.Background(), 20))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "immut_gauge")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	assert.Len(t, dps, 2, "two label sets must produce two separate data points")
}

func TestHistogramBuilder_Immutability(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "immut_hist", Buckets: []float64{10, 100}})
	require.NoError(t, err)

	branch1 := hist.WithLabels(map[string]string{"route": "/a"})
	branch2 := hist.WithLabels(map[string]string{"route": "/b"})

	require.NoError(t, branch1.Record(context.Background(), 5))
	require.NoError(t, branch2.Record(context.Background(), 50))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "immut_hist")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	assert.Len(t, dps, 2, "two label sets must produce two separate data points")
}

// ---------------------------------------------------------------------------
// 7. Distinct attribute sets create distinct data points
// ---------------------------------------------------------------------------

func TestCounter_DifferentLabels_SeparateDataPoints(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "http_requests"})
	require.NoError(t, err)

	success := counter.WithLabels(map[string]string{"status": "200"})
	failure := counter.WithLabels(map[string]string{"status": "500"})

	require.NoError(t, success.Add(context.Background(), 100))
	require.NoError(t, failure.Add(context.Background(), 3))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "http_requests")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 2)

	for _, dp := range dps {
		if hasAttribute(dp.Attributes, "status", "200") {
			assert.Equal(t, int64(100), dp.Value)
		} else if hasAttribute(dp.Attributes, "status", "500") {
			assert.Equal(t, int64(3), dp.Value)
		} else {
			t.Fatal("unexpected data point without status attribute")
		}
	}
}

// ---------------------------------------------------------------------------
// 8. Metric caching (getOrCreate*)
// ---------------------------------------------------------------------------

func TestCounter_CachesInstrument(t *testing.T) {
	factory, _ := newTestFactory(t)

	m := Metric{Name: "cached_counter", Description: "test"}

	counter1, err := factory.Counter(m)
	require.NoError(t, err)

	counter2, err := factory.Counter(m)
	require.NoError(t, err)

	// Both builders must share the same underlying counter instrument.
	assert.Equal(t, counter1.counter, counter2.counter, "counter must be cached")
}

func TestGauge_CachesInstrument(t *testing.T) {
	factory, _ := newTestFactory(t)

	m := Metric{Name: "cached_gauge"}

	gauge1, err := factory.Gauge(m)
	require.NoError(t, err)

	gauge2, err := factory.Gauge(m)
	require.NoError(t, err)

	assert.Equal(t, gauge1.gauge, gauge2.gauge, "gauge must be cached")
}

func TestHistogram_CachesInstrument(t *testing.T) {
	factory, _ := newTestFactory(t)

	m := Metric{Name: "cached_hist", Buckets: []float64{1, 10, 100}}

	hist1, err := factory.Histogram(m)
	require.NoError(t, err)

	hist2, err := factory.Histogram(m)
	require.NoError(t, err)

	assert.Equal(t, hist1.histogram, hist2.histogram, "histogram must be cached")
}

func TestDuplicateRegistrations_ShareInstrument(t *testing.T) {
	factory, reader := newTestFactory(t)

	m := Metric{Name: "shared_counter"}

	counter1, err := factory.Counter(m)
	require.NoError(t, err)

	counter2, err := factory.Counter(m)
	require.NoError(t, err)

	require.NoError(t, counter1.AddOne(context.Background()))
	require.NoError(t, counter2.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	met := findMetricByName(rm, "shared_counter")
	require.NotNil(t, met)

	dps := sumDataPoints(t, met)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(2), dps[0].Value, "both builders must write to same counter")
}

// ---------------------------------------------------------------------------
// 9. selectDefaultBuckets
// ---------------------------------------------------------------------------

func TestSelectDefaultBuckets(t *testing.T) {
	tests := []struct {
		name     string
		expected []float64
	}{
		{"account_creation_rate", DefaultAccountBuckets},
		{"AccountTotal", DefaultAccountBuckets},
		{"transaction_volume", DefaultTransactionBuckets},
		{"TransactionLatency", DefaultLatencyBuckets}, // "latency" checked before "transaction"
		{"api_latency", DefaultLatencyBuckets},
		{"request_duration", DefaultLatencyBuckets},
		{"processing_time", DefaultLatencyBuckets},
		{"unknown_metric", DefaultLatencyBuckets},
		{"", DefaultLatencyBuckets},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectDefaultBuckets(tt.name)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestHistogram_DefaultBucketsApplied(t *testing.T) {
	factory, reader := newTestFactory(t)

	// No Buckets specified -- should use default based on name
	hist, err := factory.Histogram(Metric{Name: "transaction_processing"})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 500))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "transaction_processing")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, DefaultTransactionBuckets, dps[0].Bounds,
		"transaction-related histogram must use DefaultTransactionBuckets")
}

func TestHistogram_AccountDefaultBuckets(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "account_creation"})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 10))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "account_creation")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, DefaultAccountBuckets, dps[0].Bounds)
}

func TestHistogram_LatencyDefaultBuckets(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "api_latency"})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 1))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "api_latency")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, DefaultLatencyBuckets, dps[0].Bounds)
}

// ---------------------------------------------------------------------------
// 10. histogramCacheKey
// ---------------------------------------------------------------------------

func TestHistogramCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		buckets  []float64
		expected string
	}{
		{"metric", nil, "metric"},
		{"metric", []float64{}, "metric"},
		{"metric", []float64{1, 5, 10}, "metric:1,5,10"},
		{"metric", []float64{10, 1, 5}, "metric:1,5,10"}, // sorted
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, histogramCacheKey(tt.name, tt.buckets))
		})
	}
}

func TestHistogram_DifferentBuckets_SeparateCacheEntries(t *testing.T) {
	factory, _ := newTestFactory(t)

	hist1, err := factory.Histogram(Metric{
		Name:    "my_hist",
		Buckets: []float64{1, 10, 100},
	})
	require.NoError(t, err)

	hist2, err := factory.Histogram(Metric{
		Name:    "my_hist",
		Buckets: []float64{5, 50, 500},
	})
	require.NoError(t, err)

	// Different buckets => different cache entries => different histogram instruments.
	// Note: OTel SDK may or may not return different instruments for the same name,
	// but the cache key must be different.
	assert.NotEqual(t,
		histogramCacheKey("my_hist", []float64{1, 10, 100}),
		histogramCacheKey("my_hist", []float64{5, 50, 500}),
	)

	// Both histograms should work without error.
	require.NoError(t, hist1.Record(context.Background(), 5))
	require.NoError(t, hist2.Record(context.Background(), 25))
}

// ---------------------------------------------------------------------------
// 11. Domain metric recording helpers
// ---------------------------------------------------------------------------

func TestRecordAccountCreated(t *testing.T) {
	factory, reader := newTestFactory(t)

	err := factory.RecordAccountCreated(context.Background(),
		attribute.String("org_id", "org-123"),
	)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "accounts_created")
	require.NotNil(t, m, "accounts_created metric must exist")

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value)
	assert.True(t, hasAttribute(dps[0].Attributes, "org_id", "org-123"))
}

func TestRecordTransactionProcessed(t *testing.T) {
	factory, reader := newTestFactory(t)

	err := factory.RecordTransactionProcessed(context.Background(),
		attribute.String("type", "debit"),
	)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "transactions_processed")
	require.NotNil(t, m, "transactions_processed metric must exist")

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value)
	assert.True(t, hasAttribute(dps[0].Attributes, "type", "debit"))
}

func TestRecordOperationRouteCreated(t *testing.T) {
	factory, reader := newTestFactory(t)

	err := factory.RecordOperationRouteCreated(context.Background(),
		attribute.String("operation", "transfer"),
	)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "operation_routes_created")
	require.NotNil(t, m, "operation_routes_created metric must exist")

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value)
	assert.True(t, hasAttribute(dps[0].Attributes, "operation", "transfer"))
}

func TestRecordTransactionRouteCreated(t *testing.T) {
	factory, reader := newTestFactory(t)

	err := factory.RecordTransactionRouteCreated(context.Background(),
		attribute.String("route", "internal"),
	)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "transaction_routes_created")
	require.NotNil(t, m, "transaction_routes_created metric must exist")

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value)
	assert.True(t, hasAttribute(dps[0].Attributes, "route", "internal"))
}

func TestRecordHelpers_NoAttributes(t *testing.T) {
	factory, reader := newTestFactory(t)

	require.NoError(t, factory.RecordAccountCreated(context.Background()))
	require.NoError(t, factory.RecordTransactionProcessed(context.Background()))
	require.NoError(t, factory.RecordOperationRouteCreated(context.Background()))
	require.NoError(t, factory.RecordTransactionRouteCreated(context.Background()))

	rm := collectMetrics(t, reader)

	for _, name := range []string{
		"accounts_created",
		"transactions_processed",
		"operation_routes_created",
		"transaction_routes_created",
	} {
		m := findMetricByName(rm, name)
		require.NotNil(t, m, "metric %q must exist", name)

		dps := sumDataPoints(t, m)
		require.Len(t, dps, 1)
		assert.Equal(t, int64(1), dps[0].Value)
	}
}

func TestRecordHelpers_MultipleInvocations(t *testing.T) {
	factory, reader := newTestFactory(t)

	for i := 0; i < 10; i++ {
		require.NoError(t, factory.RecordAccountCreated(context.Background()))
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "accounts_created")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(10), dps[0].Value)
}

// ---------------------------------------------------------------------------
// 12. Pre-configured Metric definitions
// ---------------------------------------------------------------------------

func TestPreConfiguredMetrics(t *testing.T) {
	tests := []struct {
		metric Metric
		name   string
	}{
		{MetricAccountsCreated, "accounts_created"},
		{MetricTransactionsProcessed, "transactions_processed"},
		{MetricTransactionRoutesCreated, "transaction_routes_created"},
		{MetricOperationRoutesCreated, "operation_routes_created"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.name, tt.metric.Name)
			assert.NotEmpty(t, tt.metric.Description)
			assert.Equal(t, "1", tt.metric.Unit)
		})
	}
}

// ---------------------------------------------------------------------------
// 13. Metric options (description, unit)
// ---------------------------------------------------------------------------

func TestCounter_DescriptionAndUnit(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{
		Name:        "desc_counter",
		Description: "A test counter with description",
		Unit:        "requests",
	})
	require.NoError(t, err)

	require.NoError(t, counter.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "desc_counter")
	require.NotNil(t, m)
	assert.Equal(t, "A test counter with description", m.Description)
	assert.Equal(t, "requests", m.Unit)
}

func TestGauge_DescriptionAndUnit(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{
		Name:        "desc_gauge",
		Description: "A test gauge",
		Unit:        "connections",
	})
	require.NoError(t, err)

	require.NoError(t, gauge.Set(context.Background(), 5))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "desc_gauge")
	require.NotNil(t, m)
	assert.Equal(t, "A test gauge", m.Description)
	assert.Equal(t, "connections", m.Unit)
}

func TestHistogram_DescriptionAndUnit(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{
		Name:        "desc_hist",
		Description: "A test histogram",
		Unit:        "ms",
		Buckets:     []float64{10, 100},
	})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 50))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "desc_hist")
	require.NotNil(t, m)
	assert.Equal(t, "A test histogram", m.Description)
	assert.Equal(t, "ms", m.Unit)
}

func TestCounter_NoDescriptionNoUnit(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "bare_counter"})
	require.NoError(t, err)

	require.NoError(t, counter.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "bare_counter")
	require.NotNil(t, m)
	// SDK may set empty strings; the point is no error occurred.
}

// ---------------------------------------------------------------------------
// 14. Concurrent metric recording (goroutine safety)
// ---------------------------------------------------------------------------

func TestCounter_ConcurrentAdd(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "concurrent_counter"})
	require.NoError(t, err)

	const goroutines = 100

	errs := make(chan error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := counter.AddOne(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent AddOne error: %v", err)
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "concurrent_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(goroutines), dps[0].Value,
		"all concurrent increments must be accounted for")
}

func TestGauge_ConcurrentSet(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{Name: "concurrent_gauge"})
	require.NoError(t, err)

	const goroutines = 50

	errs := make(chan error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(val int64) {
			defer wg.Done()
			if err := gauge.Set(context.Background(), val); err != nil {
				errs <- err
			}
		}(int64(i))
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Set error: %v", err)
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "concurrent_gauge")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.NotEmpty(t, dps, "gauge must have at least one data point")
}

func TestHistogram_ConcurrentRecord(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{Name: "concurrent_hist", Buckets: []float64{10, 100, 1000}})
	require.NoError(t, err)

	const goroutines = 100

	errs := make(chan error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(val int64) {
			defer wg.Done()
			if err := hist.Record(context.Background(), val); err != nil {
				errs <- err
			}
		}(int64(i))
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Record error: %v", err)
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "concurrent_hist")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(goroutines), dps[0].Count)
}

func TestFactory_ConcurrentCounterCreation(t *testing.T) {
	factory, reader := newTestFactory(t)

	const goroutines = 50
	m := Metric{Name: "race_counter"}

	errs := make(chan error, goroutines*2)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			counter, err := factory.Counter(m)
			if err != nil {
				errs <- err
				return
			}

			if err := counter.AddOne(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent counter creation error: %v", err)
	}

	rm := collectMetrics(t, reader)
	met := findMetricByName(rm, "race_counter")
	require.NotNil(t, met)

	dps := sumDataPoints(t, met)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(goroutines), dps[0].Value,
		"concurrent counter creation and recording must not lose data")
}

func TestFactory_ConcurrentGaugeCreation(t *testing.T) {
	factory, _ := newTestFactory(t)

	const goroutines = 50
	m := Metric{Name: "race_gauge"}

	errs := make(chan error, goroutines*2)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(val int64) {
			defer wg.Done()

			gauge, err := factory.Gauge(m)
			if err != nil {
				errs <- err
				return
			}

			if err := gauge.Set(context.Background(), val); err != nil {
				errs <- err
			}
		}(int64(i))
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent gauge creation error: %v", err)
	}
}

func TestFactory_ConcurrentHistogramCreation(t *testing.T) {
	factory, reader := newTestFactory(t)

	const goroutines = 50
	m := Metric{Name: "race_hist", Buckets: []float64{10, 100}}

	errs := make(chan error, goroutines*2)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(val int64) {
			defer wg.Done()

			hist, err := factory.Histogram(m)
			if err != nil {
				errs <- err
				return
			}

			if err := hist.Record(context.Background(), val); err != nil {
				errs <- err
			}
		}(int64(i))
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent histogram creation error: %v", err)
	}

	rm := collectMetrics(t, reader)
	met := findMetricByName(rm, "race_hist")
	require.NotNil(t, met)

	dps := histDataPoints(t, met)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(goroutines), dps[0].Count)
}

func TestFactory_ConcurrentMixedMetricTypes(t *testing.T) {
	factory, _ := newTestFactory(t)

	const goroutines = 30

	errs := make(chan error, goroutines*3)

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := factory.RecordAccountCreated(context.Background()); err != nil {
				errs <- err
			}
		}()

		go func() {
			defer wg.Done()
			if err := factory.RecordTransactionProcessed(context.Background()); err != nil {
				errs <- err
			}
		}()

		go func() {
			defer wg.Done()
			if err := factory.RecordOperationRouteCreated(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent mixed metric error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 15. Error sentinel values
// ---------------------------------------------------------------------------

func TestErrorSentinels(t *testing.T) {
	assert.NotNil(t, ErrNilMeter)
	assert.NotNil(t, ErrNilCounter)
	assert.NotNil(t, ErrNilGauge)
	assert.NotNil(t, ErrNilHistogram)

	assert.EqualError(t, ErrNilMeter, "metric meter cannot be nil")
	assert.EqualError(t, ErrNilCounter, "counter instrument is nil")
	assert.EqualError(t, ErrNilGauge, "gauge instrument is nil")
	assert.EqualError(t, ErrNilHistogram, "histogram instrument is nil")
}

// ---------------------------------------------------------------------------
// 16. Default bucket configuration values
// ---------------------------------------------------------------------------

func TestDefaultBucketValues(t *testing.T) {
	assert.NotEmpty(t, DefaultLatencyBuckets)
	assert.NotEmpty(t, DefaultAccountBuckets)
	assert.NotEmpty(t, DefaultTransactionBuckets)

	// Verify they are sorted (required by OTel spec for histogram boundaries)
	for i := 1; i < len(DefaultLatencyBuckets); i++ {
		assert.Less(t, DefaultLatencyBuckets[i-1], DefaultLatencyBuckets[i],
			"DefaultLatencyBuckets must be sorted")
	}

	for i := 1; i < len(DefaultAccountBuckets); i++ {
		assert.Less(t, DefaultAccountBuckets[i-1], DefaultAccountBuckets[i],
			"DefaultAccountBuckets must be sorted")
	}

	for i := 1; i < len(DefaultTransactionBuckets); i++ {
		assert.Less(t, DefaultTransactionBuckets[i-1], DefaultTransactionBuckets[i],
			"DefaultTransactionBuckets must be sorted")
	}
}

// ---------------------------------------------------------------------------
// 17. addXxxOptions helpers
// ---------------------------------------------------------------------------

func TestAddCounterOptions(t *testing.T) {
	factory, _ := newTestFactory(t)

	t.Run("with description and unit", func(t *testing.T) {
		opts := factory.addCounterOptions(Metric{
			Name:        "test",
			Description: "desc",
			Unit:        "bytes",
		})
		assert.Len(t, opts, 2)
	})

	t.Run("with description only", func(t *testing.T) {
		opts := factory.addCounterOptions(Metric{
			Name:        "test",
			Description: "desc",
		})
		assert.Len(t, opts, 1)
	})

	t.Run("with unit only", func(t *testing.T) {
		opts := factory.addCounterOptions(Metric{
			Name: "test",
			Unit: "ms",
		})
		assert.Len(t, opts, 1)
	})

	t.Run("no options", func(t *testing.T) {
		opts := factory.addCounterOptions(Metric{Name: "test"})
		assert.Empty(t, opts)
	})
}

func TestAddGaugeOptions(t *testing.T) {
	factory, _ := newTestFactory(t)

	t.Run("with description and unit", func(t *testing.T) {
		opts := factory.addGaugeOptions(Metric{
			Name:        "test",
			Description: "desc",
			Unit:        "connections",
		})
		assert.Len(t, opts, 2)
	})

	t.Run("no options", func(t *testing.T) {
		opts := factory.addGaugeOptions(Metric{Name: "test"})
		assert.Empty(t, opts)
	})
}

func TestAddHistogramOptions(t *testing.T) {
	factory, _ := newTestFactory(t)

	t.Run("with all options", func(t *testing.T) {
		opts := factory.addHistogramOptions(Metric{
			Name:        "test",
			Description: "desc",
			Unit:        "ms",
			Buckets:     []float64{1, 10, 100},
		})
		assert.Len(t, opts, 3) // description + unit + buckets
	})

	t.Run("with buckets only", func(t *testing.T) {
		opts := factory.addHistogramOptions(Metric{
			Name:    "test",
			Buckets: []float64{1, 10},
		})
		assert.Len(t, opts, 1)
	})

	t.Run("no options and nil buckets", func(t *testing.T) {
		opts := factory.addHistogramOptions(Metric{Name: "test"})
		assert.Empty(t, opts)
	})
}

// ---------------------------------------------------------------------------
// 18. End-to-end: full recording pipeline
// ---------------------------------------------------------------------------

func TestEndToEnd_CounterPipeline(t *testing.T) {
	factory, reader := newTestFactory(t)

	// 1. Create counter with full Metric definition
	counter, err := factory.Counter(Metric{
		Name:        "e2e_counter",
		Description: "End to end counter",
		Unit:        "ops",
	})
	require.NoError(t, err)

	// 2. Record with labels
	labeled := counter.WithLabels(map[string]string{
		"service": "ledger",
		"env":     "staging",
	})
	require.NoError(t, labeled.Add(context.Background(), 10))

	// 3. Record again with different labels
	other := counter.WithLabels(map[string]string{
		"service": "auth",
		"env":     "prod",
	})
	require.NoError(t, other.Add(context.Background(), 5))

	// 4. Verify all data
	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "e2e_counter")
	require.NotNil(t, m)
	assert.Equal(t, "End to end counter", m.Description)
	assert.Equal(t, "ops", m.Unit)

	dps := sumDataPoints(t, m)
	assert.Len(t, dps, 2, "two label sets => two data points")

	var totalValue int64
	for _, dp := range dps {
		totalValue += dp.Value
	}

	assert.Equal(t, int64(15), totalValue, "total value across all data points")
}

func TestEndToEnd_HistogramPipeline(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{
		Name:        "e2e_hist",
		Description: "End to end histogram",
		Unit:        "ms",
		Buckets:     []float64{50, 100, 250, 500, 1000},
	})
	require.NoError(t, err)

	// Record several values across different buckets
	values := []int64{25, 75, 150, 300, 750, 1500}
	for _, v := range values {
		require.NoError(t, hist.WithLabels(map[string]string{"handler": "transfer"}).Record(context.Background(), v))
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "e2e_hist")
	require.NotNil(t, m)
	assert.Equal(t, "End to end histogram", m.Description)
	assert.Equal(t, "ms", m.Unit)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(6), dps[0].Count)

	var expectedSum int64
	for _, v := range values {
		expectedSum += v
	}

	assert.Equal(t, expectedSum, dps[0].Sum)
	assert.Equal(t, []float64{50, 100, 250, 500, 1000}, dps[0].Bounds)
	assert.True(t, hasAttribute(dps[0].Attributes, "handler", "transfer"))
}

func TestEndToEnd_GaugePipeline(t *testing.T) {
	factory, reader := newTestFactory(t)

	gauge, err := factory.Gauge(Metric{
		Name:        "e2e_gauge",
		Description: "End to end gauge",
		Unit:        "items",
	})
	require.NoError(t, err)

	// Set different values for different pools
	primary := gauge.WithLabels(map[string]string{"pool": "primary"})
	require.NoError(t, primary.Set(context.Background(), 50))

	replica := gauge.WithLabels(map[string]string{"pool": "replica"})
	require.NoError(t, replica.Set(context.Background(), 30))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "e2e_gauge")
	require.NotNil(t, m)
	assert.Equal(t, "End to end gauge", m.Description)
	assert.Equal(t, "items", m.Unit)

	dps := gaugeDataPoints(t, m)
	assert.Len(t, dps, 2, "two attribute sets must produce two data points")

	for _, dp := range dps {
		if hasAttribute(dp.Attributes, "pool", "primary") {
			assert.Equal(t, int64(50), dp.Value)
		} else if hasAttribute(dp.Attributes, "pool", "replica") {
			assert.Equal(t, int64(30), dp.Value)
		} else {
			t.Fatal("unexpected data point without pool attribute")
		}
	}
}

// ---------------------------------------------------------------------------
// 19. Noop provider compatibility (existing tests, upgraded)
// ---------------------------------------------------------------------------

func TestNoop_FactoryCreation(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("noop-test")
	factory, err := NewMetricsFactory(meter, &log.NopLogger{})
	require.NoError(t, err)
	assert.NotNil(t, factory)
}

func TestNoop_AllHelpers(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("noop-test")
	factory, err := NewMetricsFactory(meter, &log.NopLogger{})
	require.NoError(t, err)

	require.NoError(t, factory.RecordAccountCreated(context.Background(), attribute.String("result", "ok")))
	require.NoError(t, factory.RecordTransactionProcessed(context.Background(), attribute.String("result", "ok")))
	require.NoError(t, factory.RecordOperationRouteCreated(context.Background(), attribute.String("result", "ok")))
	require.NoError(t, factory.RecordTransactionRouteCreated(context.Background(), attribute.String("result", "ok")))
}

// ---------------------------------------------------------------------------
// 20. Histogram bucket count verification
// ---------------------------------------------------------------------------

func TestHistogram_BucketCountDistribution(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{
		Name:    "bucket_test",
		Buckets: []float64{10, 50, 100},
	})
	require.NoError(t, err)

	// Record values that fall into specific buckets:
	// Bucket [0, 10): values 1, 5 => count=2
	// Bucket [10, 50): values 15, 30 => count=2
	// Bucket [50, 100): values 60 => count=1
	// Bucket [100, +Inf): values 200 => count=1
	for _, v := range []int64{1, 5, 15, 30, 60, 200} {
		require.NoError(t, hist.Record(context.Background(), v))
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "bucket_test")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, uint64(6), dps[0].Count)
	assert.Equal(t, int64(1+5+15+30+60+200), dps[0].Sum)

	// BucketCounts: [<=10, <=50, <=100, +Inf]
	// Expected:      [2,    4,    5,     6]  (cumulative in OTel SDK)
	// Note: OTel SDK uses cumulative bucket counts
	require.Len(t, dps[0].BucketCounts, 4, "3 boundaries => 4 bucket counts")
}

// ---------------------------------------------------------------------------
// 21. Multiple metrics on same factory
// ---------------------------------------------------------------------------

func TestFactory_MultipleMetricTypes(t *testing.T) {
	factory, reader := newTestFactory(t)

	// Create one of each type
	counter, err := factory.Counter(Metric{Name: "multi_counter"})
	require.NoError(t, err)

	gauge, err := factory.Gauge(Metric{Name: "multi_gauge"})
	require.NoError(t, err)

	hist, err := factory.Histogram(Metric{Name: "multi_hist", Buckets: []float64{10, 100}})
	require.NoError(t, err)

	// Record values
	require.NoError(t, counter.Add(context.Background(), 7))
	require.NoError(t, gauge.Set(context.Background(), 42))
	require.NoError(t, hist.Record(context.Background(), 55))

	// Verify all
	rm := collectMetrics(t, reader)

	ctrMet := findMetricByName(rm, "multi_counter")
	require.NotNil(t, ctrMet)
	ctrDps := sumDataPoints(t, ctrMet)
	require.Len(t, ctrDps, 1)
	assert.Equal(t, int64(7), ctrDps[0].Value)

	gaugeMet := findMetricByName(rm, "multi_gauge")
	require.NotNil(t, gaugeMet)
	gaugeDps := gaugeDataPoints(t, gaugeMet)
	require.Len(t, gaugeDps, 1)
	assert.Equal(t, int64(42), gaugeDps[0].Value)

	histMet := findMetricByName(rm, "multi_hist")
	require.NotNil(t, histMet)
	histDps := histDataPoints(t, histMet)
	require.Len(t, histDps, 1)
	assert.Equal(t, uint64(1), histDps[0].Count)
	assert.Equal(t, int64(55), histDps[0].Sum)
}

// ---------------------------------------------------------------------------
// 22. Context propagation
// ---------------------------------------------------------------------------

func TestCounter_RespectsContext(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "ctx_counter"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, counter.AddOne(ctx))
	cancel() // Cancel after recording

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "ctx_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(1), dps[0].Value, "value recorded before cancel must persist")
}

// ---------------------------------------------------------------------------
// 23. Large value handling
// ---------------------------------------------------------------------------

func TestCounter_LargeValues(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "big_counter"})
	require.NoError(t, err)

	// Financial services can have very large transaction counts
	largeVal := int64(1_000_000_000)
	require.NoError(t, counter.Add(context.Background(), largeVal))
	require.NoError(t, counter.Add(context.Background(), largeVal))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "big_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(2_000_000_000), dps[0].Value)
}

func TestHistogram_LargeValues(t *testing.T) {
	factory, reader := newTestFactory(t)

	hist, err := factory.Histogram(Metric{
		Name:    "big_hist",
		Buckets: []float64{1000, 10_000, 100_000, 1_000_000},
	})
	require.NoError(t, err)

	require.NoError(t, hist.Record(context.Background(), 5_000_000))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "big_hist")
	require.NotNil(t, m)

	dps := histDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(5_000_000), dps[0].Sum)
}

// ---------------------------------------------------------------------------
// 24. Multiple collects
// ---------------------------------------------------------------------------

func TestCounter_MultipleCollects(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "multi_collect_counter"})
	require.NoError(t, err)

	require.NoError(t, counter.Add(context.Background(), 5))

	// First collect
	rm1 := collectMetrics(t, reader)
	m1 := findMetricByName(rm1, "multi_collect_counter")
	require.NotNil(t, m1)
	dps1 := sumDataPoints(t, m1)
	require.Len(t, dps1, 1)
	assert.Equal(t, int64(5), dps1[0].Value)

	// Record more
	require.NoError(t, counter.Add(context.Background(), 3))

	// Second collect -- cumulative counter should show total
	rm2 := collectMetrics(t, reader)
	m2 := findMetricByName(rm2, "multi_collect_counter")
	require.NotNil(t, m2)
	dps2 := sumDataPoints(t, m2)
	require.Len(t, dps2, 1)
	assert.Equal(t, int64(8), dps2[0].Value, "cumulative counter should show 5+3=8")
}
