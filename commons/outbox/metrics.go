package outbox

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type dispatcherMetrics struct {
	eventsDispatched  metric.Int64Counter
	eventsFailed      metric.Int64Counter
	eventsStateFailed metric.Int64Counter
	dispatchLatency   metric.Float64Histogram
	queueDepth        metric.Int64Gauge
}

func newDispatcherMetrics(provider metric.MeterProvider) (dispatcherMetrics, error) {
	if provider == nil {
		provider = otel.GetMeterProvider()
	}

	meter := provider.Meter("commons.outbox.dispatcher")

	var (
		metrics dispatcherMetrics
		err     error
	)

	metrics.eventsDispatched, err = meter.Int64Counter(
		"outbox.events.dispatched",
		metric.WithDescription("Number of outbox events successfully published"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.events.dispatched counter: %w", err)
	}

	metrics.eventsFailed, err = meter.Int64Counter(
		"outbox.events.failed",
		metric.WithDescription("Number of outbox events that failed to publish"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.events.failed counter: %w", err)
	}

	metrics.eventsStateFailed, err = meter.Int64Counter(
		"outbox.events.state_update_failed",
		metric.WithDescription("Number of outbox events published but not persisted as published"),
		metric.WithUnit("{event}"),
	)
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

	metrics.queueDepth, err = meter.Int64Gauge(
		"outbox.queue.depth",
		metric.WithDescription("Number of outbox events selected in a dispatch cycle (pending and reclaimed)"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.queue.depth gauge: %w", err)
	}

	return metrics, nil
}
