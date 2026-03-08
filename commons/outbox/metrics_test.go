//go:build unit

package outbox

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
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

	metrics, err := newDispatcherMetrics(nil)
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

			_, err := newDispatcherMetrics(provider)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.errText)
		})
	}
}
