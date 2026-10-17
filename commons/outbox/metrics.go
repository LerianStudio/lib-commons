package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	libMetrics "github.com/LerianStudio/lib-observability/v4/metrics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const outboxMetricUnitEvent = "{event}"

type dispatcherMetrics struct {
	eventsDispatched  *libMetrics.CounterBuilder
	eventsFailed      *libMetrics.CounterBuilder
	eventsStateFailed *libMetrics.CounterBuilder
	eventsPurged      *libMetrics.CounterBuilder
	dispatchLatency   metric.Float64Histogram
	queueDepth        *libMetrics.GaugeBuilder
	queueOldestAge    *libMetrics.GaugeBuilder
}

func newDispatcherMetrics(provider metric.MeterProvider, logger obs.Logger) (dispatcherMetrics, error) {
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

	metrics.eventsPurged, err = factory.Counter(libMetrics.Metric{
		Name:        "outbox.events.purged",
		Description: "Number of outbox events deleted by the retention sweep",
		Unit:        outboxMetricUnitEvent,
	})
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.events.purged counter: %w", err)
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

	metrics.queueOldestAge, err = factory.Gauge(libMetrics.Metric{
		Name:        "outbox.queue.oldest_age",
		Description: "Age of the oldest PENDING, PROCESSING or FAILED outbox event, 0 when none",
		Unit:        "s",
	})
	if err != nil {
		return dispatcherMetrics{}, fmt.Errorf("create outbox.queue.oldest_age gauge: %w", err)
	}

	return metrics, nil
}

// recordOldestWaitingAge sets outbox.queue.oldest_age after a dispatch cycle when the
// repository is an OldestWaitingReader; a failed read keeps the last value.
func (dispatcher *Dispatcher) recordOldestWaitingAge(ctx context.Context, tenantKey string) {
	reader, ok := dispatcher.repo.(OldestWaitingReader)
	if !ok || dispatcher.metrics.queueOldestAge == nil || ctx.Err() != nil {
		return
	}

	oldest, found, err := reader.OldestWaitingCreatedAt(dispatcher.withClaimScope(ctx))
	if err != nil {
		dispatcher.resolvedLogger().Log(ctx, obs.LevelWarn, "outbox oldest waiting event read failed", "error", sanitizeErrorForStorage(err))

		return
	}

	age := int64(0)
	if found {
		age = max(0, int64(dispatcher.currentTime().Sub(oldest)/time.Second))
	}

	builder := dispatcher.metrics.queueOldestAge
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		builder = builder.WithAttributes(attr)
	}

	if err := builder.Set(ctx, age); err != nil {
		dispatcher.logMetricError(ctx, "record outbox.queue.oldest_age", err)
	}
}
