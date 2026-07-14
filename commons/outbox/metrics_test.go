//go:build unit

package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-observability/v2/log"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type testMeterProvider struct {
	metric.MeterProvider
	meter metric.Meter
}

func (provider testMeterProvider) Meter(_ string, _ ...metric.MeterOption) metric.Meter {
	return provider.meter
}

type failingMeter struct {
	metric.Meter
	failOnName string
	failErr    error
}

func (meter failingMeter) Int64Counter(name string, options ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if name == meter.failOnName {
		return nil, meter.failErr
	}

	return meter.Meter.Int64Counter(name, options...)
}

func (meter failingMeter) Float64Histogram(name string, options ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	if name == meter.failOnName {
		return nil, meter.failErr
	}

	return meter.Meter.Float64Histogram(name, options...)
}

func (meter failingMeter) Int64Gauge(name string, options ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	if name == meter.failOnName {
		return nil, meter.failErr
	}

	return meter.Meter.Int64Gauge(name, options...)
}

func TestNewDispatcherMetrics_DefaultProvider(t *testing.T) {
	t.Parallel()

	metrics, err := newDispatcherMetrics(nil, nil)
	require.NoError(t, err)
	require.NotNil(t, metrics.eventsDispatched)
	require.NotNil(t, metrics.eventsFailed)
	require.NotNil(t, metrics.eventsStateFailed)
	require.NotNil(t, metrics.dispatchLatency)
	require.NotNil(t, metrics.queueDepth)
}

func TestNewDispatcherMetrics_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instrument string
		errText    string
	}{
		{name: "eventsDispatched counter", instrument: "outbox.events.dispatched", errText: "create outbox.events.dispatched counter"},
		{name: "eventsFailed counter", instrument: "outbox.events.failed", errText: "create outbox.events.failed counter"},
		{name: "eventsStateFailed counter", instrument: "outbox.events.state_update_failed", errText: "create outbox.events.state_update_failed counter"},
		{name: "dispatchLatency histogram", instrument: "outbox.dispatch.latency", errText: "create outbox.dispatch.latency histogram"},
		{name: "queueDepth gauge", instrument: "outbox.queue.depth", errText: "create outbox.queue.depth gauge"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := testMeterProvider{
				MeterProvider: noop.NewMeterProvider(),
				meter: failingMeter{
					Meter:      noop.NewMeterProvider().Meter("test"),
					failOnName: tt.instrument,
					failErr:    errors.New("instrument creation failed"),
				},
			}

			_, err := newDispatcherMetrics(provider, nil)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.errText)
		})
	}
}

func TestDispatcherMetrics_RecordValuesAndTenantAttributes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	metrics, err := newDispatcherMetrics(provider, libLog.NewNop())
	require.NoError(t, err)

	dispatcher := &Dispatcher{
		cfg: DispatcherConfig{
			IncludeTenantMetrics:      true,
			MaxTenantMetricDimensions: 1,
		},
		logger:  libLog.NewNop(),
		metrics: metrics,
	}

	dispatcher.recordQueueDepth(ctx, "tenant-a", 7)
	dispatcher.addDispatchedEvents(ctx, "tenant-a", 2)
	dispatcher.addFailedEvents(ctx, "tenant-a", 1)
	dispatcher.addStateUpdateFailure(ctx, "tenant-a", 1)
	dispatcher.recordDispatchLatency(ctx, "tenant-a", 150*time.Millisecond)
	dispatcher.recordQueueDepth(ctx, "tenant-b", 3)

	rm := collectOutboxMetrics(t, reader)

	requireIntMetricValue(t, rm, "outbox.queue.depth", overflowTenantMetricLabel, 3)
	requireIntMetricValue(t, rm, "outbox.events.dispatched", "tenant-a", 2)
	requireIntMetricValue(t, rm, "outbox.events.failed", "tenant-a", 1)
	requireIntMetricValue(t, rm, "outbox.events.state_update_failed", "tenant-a", 1)
	requireFloatHistogramValue(t, rm, "outbox.dispatch.latency", "tenant-a", 0.15)
}

func collectOutboxMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	return rm
}

func findOutboxMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, scope := range rm.ScopeMetrics {
		for i := range scope.Metrics {
			if scope.Metrics[i].Name == name {
				return &scope.Metrics[i]
			}
		}
	}

	return nil
}

func requireIntMetricValue(t *testing.T, rm metricdata.ResourceMetrics, name, tenant string, want int64) {
	t.Helper()

	metricData := findOutboxMetric(rm, name)
	require.NotNil(t, metricData, "metric %q not found", name)

	var points []metricdata.DataPoint[int64]
	switch data := metricData.Data.(type) {
	case metricdata.Sum[int64]:
		points = data.DataPoints
	case metricdata.Gauge[int64]:
		points = data.DataPoints
	default:
		t.Fatalf("metric %q has unexpected data type %T", name, metricData.Data)
	}

	for _, point := range points {
		if hasTenantAttribute(point.Attributes, tenant) {
			require.Equal(t, want, point.Value)

			return
		}
	}

	t.Fatalf("metric %q has no datapoint for tenant %q", name, tenant)
}

func requireFloatHistogramValue(t *testing.T, rm metricdata.ResourceMetrics, name, tenant string, want float64) {
	t.Helper()

	metricData := findOutboxMetric(rm, name)
	require.NotNil(t, metricData, "metric %q not found", name)

	histogram, ok := metricData.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "metric %q has unexpected data type %T", name, metricData.Data)

	for _, point := range histogram.DataPoints {
		if hasTenantAttribute(point.Attributes, tenant) {
			require.Equal(t, uint64(1), point.Count)
			require.InDelta(t, want, point.Sum, 0.000001)

			return
		}
	}

	t.Fatalf("metric %q has no datapoint for tenant %q", name, tenant)
}

func hasTenantAttribute(attrs attribute.Set, tenant string) bool {
	iter := attrs.Iter()
	for iter.Next() {
		kv := iter.Attribute()
		if string(kv.Key) == "tenant" && kv.Value.AsString() == tenant {
			return true
		}
	}

	return false
}
