package outbox

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"go.opentelemetry.io/otel/attribute"

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
	attrs             []attribute.KeyValue // on every series; the builders already carry them
}

// tableAttributes returns outbox.table for a TableNamer repository, else none.
func tableAttributes(repo OutboxRepository) []attribute.KeyValue {
	if namer, ok := repo.(TableNamer); ok && namer.OutboxTable() != "" {
		return []attribute.KeyValue{attribute.String("outbox.table", namer.OutboxTable())}
	}

	return nil
}

func newDispatcherMetrics(provider metric.MeterProvider, logger obs.Logger, attrs ...attribute.KeyValue) (dispatcherMetrics, error) {
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

	metrics.attrs = attrs
	metrics.eventsDispatched = metrics.eventsDispatched.WithAttributes(attrs...)
	metrics.eventsFailed = metrics.eventsFailed.WithAttributes(attrs...)
	metrics.eventsStateFailed = metrics.eventsStateFailed.WithAttributes(attrs...)
	metrics.eventsPurged = metrics.eventsPurged.WithAttributes(attrs...)
	metrics.queueDepth = metrics.queueDepth.WithAttributes(attrs...)
	metrics.queueOldestAge = metrics.queueOldestAge.WithAttributes(attrs...)

	return metrics, nil
}

// waitingSample is the oldest waiting event's age last read for one dispatch scope.
type waitingSample struct {
	tenantKey string
	age       int64
}

// observeOldestWaiting stores the scope's oldest waiting age after its dispatch when the
// repository is an OldestWaitingReader; a failed read keeps the scope's last sample.
func (dispatcher *Dispatcher) observeOldestWaiting(ctx context.Context, scope TenantDispatchScope) {
	reader, ok := dispatcher.repo.(OldestWaitingReader)
	if !ok || ctx.Err() != nil {
		return
	}

	oldest, found, err := reader.OldestWaitingCreatedAt(dispatcher.withClaimScope(ctx))
	if err != nil {
		dispatcher.resolvedLogger().Log(ctx, obs.LevelWarn, "outbox oldest waiting event read failed", "error", sanitizeErrorForStorage(err))

		return
	}

	sample := &waitingSample{tenantKey: tenantKeyFromContext(ctx)}
	if found {
		sample.age = max(0, int64(dispatcher.currentTime().Sub(oldest)/time.Second))
	}

	dispatcher.scopeActivityMu.Lock()
	activity := dispatcher.scopeActivity[scope]
	activity.waiting = sample
	dispatcher.scopeActivity[scope] = activity
	dispatcher.scopeActivityMu.Unlock()
}

// recordOldestWaitingAge sets outbox.queue.oldest_age to the largest age across the known
// scopes sharing each series, so one scope's 0 never hides another's waiting fact. Cold
// scopes keep their last sample; a series no scope reports any more falls to 0.
func (dispatcher *Dispatcher) recordOldestWaitingAge(ctx context.Context) {
	if dispatcher.metrics.queueOldestAge == nil {
		return
	}

	dispatcher.scopeActivityMu.Lock()

	samples := make([]waitingSample, 0, len(dispatcher.scopeActivity))
	for _, activity := range dispatcher.scopeActivity {
		if activity.waiting != nil {
			samples = append(samples, *activity.waiting)
		}
	}

	dispatcher.scopeActivityMu.Unlock()

	maxByLabel := make(map[string]int64, len(samples))

	for _, sample := range samples {
		label := ""
		if attr, ok := dispatcher.tenantMetricAttribute(sample.tenantKey); ok {
			label = attr.Value.AsString()
		}

		maxByLabel[label] = max(maxByLabel[label], sample.age)
	}

	dispatcher.scopeActivityMu.Lock()
	previous := dispatcher.oldestAgeLabels
	dispatcher.oldestAgeLabels = maxByLabel
	dispatcher.scopeActivityMu.Unlock()

	ages := maps.Clone(maxByLabel)

	for label := range previous {
		if _, live := ages[label]; !live {
			ages[label] = 0
		}
	}

	for label, age := range ages {
		builder := dispatcher.metrics.queueOldestAge
		if label != "" {
			builder = builder.WithAttributes(attribute.String("tenant", label))
		}

		if err := builder.Set(ctx, age); err != nil {
			dispatcher.logMetricError(ctx, "record outbox.queue.oldest_age", err)
		}
	}
}
