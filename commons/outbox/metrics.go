package outbox

import (
	"fmt"

	libLog "github.com/LerianStudio/lib-observability/log"
	libMetrics "github.com/LerianStudio/lib-observability/metrics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const outboxMetricUnitEvent = "{event}"

type dispatcherMetrics struct {
	eventsDispatched  *libMetrics.CounterBuilder
	eventsFailed      *libMetrics.CounterBuilder
	eventsStateFailed *libMetrics.CounterBuilder
	dispatchLatency   metric.Float64Histogram
	queueDepth        *libMetrics.GaugeBuilder
}

func newDispatcherMetrics(provider metric.MeterProvider, logger libLog.Logger) (dispatcherMetrics, error) {
	if provider == nil {
		provider = otel.GetMeterProvider()
	}

	meter := provider.Meter("commons.outbox.dispatcher")

	factory, err := libMetrics.NewMetricsFactory(meter, logger)
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox metrics factory: %w", err)
	}

	var metrics dispatcherMetrics

	metrics.eventsDispatched, err = factory.Counter(libMetrics.Metric{
		Name:        "outbox.events.dispatched",
		Description: "Number of outbox events successfully published",
		Unit:        outboxMetricUnitEvent,
	})
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.events.dispatched counter: %w", err)
	}

	metrics.eventsFailed, err = factory.Counter(libMetrics.Metric{
		Name:        "outbox.events.failed",
		Description: "Number of outbox events that failed to publish",
		Unit:        outboxMetricUnitEvent,
	})
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.events.failed counter: %w", err)
	}

	metrics.eventsStateFailed, err = factory.Counter(libMetrics.Metric{
		Name:        "outbox.events.state_update_failed",
		Description: "Number of outbox events published but not persisted as published",
		Unit:        outboxMetricUnitEvent,
	})
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.events.state_update_failed counter: %w", err)
	}

	metrics.dispatchLatency, err = meter.Float64Histogram(
		"outbox.dispatch.latency",
		metric.WithDescription("Time taken per dispatch cycle"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.dispatch.latency histogram: %w", err)
	}

	metrics.queueDepth, err = factory.Gauge(libMetrics.Metric{
		Name:        "outbox.queue.depth",
		Description: "Number of outbox events selected in a dispatch cycle (pending and reclaimed)",
		Unit:        outboxMetricUnitEvent,
	})
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.queue.depth gauge: %w", err)
	}

	return metrics, nil
}
