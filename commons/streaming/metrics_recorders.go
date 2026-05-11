package streaming

import (
	"context"
	"sync"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// This file holds the six record* methods that write to the OTEL
// instruments. Split from metrics.go so neither file crosses the 300-line
// cap and so the ceremonial-but-verbose lazy-init pattern is easy to skim
// top-down.
//
// Per-label-set builder caching (T6.1 hardening): the *Builder values
// returned by (*MetricsFactory).Counter/Histogram/Gauge cache the
// underlying OTEL instruments, but WithLabels/WithAttributes allocate a
// fresh builder on every call. On a 10k-RPS service that produces ~40k
// small heap objects per second. We cache a per-labelset builder keyed
// by a stable string (strings are hashable; attribute-set keys would
// require manual fingerprinting), reusing it across record calls.
// Topic + outcome cardinality is bounded per-process, so the cache is
// bounded too.

// labelSetCache is a concurrency-safe map of stable-key → *Builder. The
// concrete builder type is captured as `any` so the same cache shape
// fits counters, histograms, and gauges without generics bleed-through.
type labelSetCache struct {
	// m is keyed by a composed string. Value is one of:
	//   *metrics.CounterBuilder, *metrics.HistogramBuilder, *metrics.GaugeBuilder
	// depending on which record* populated it.
	m sync.Map
}

const metricLabelTopic = "topic"

// recordEmitted increments streaming_emitted_total by 1 with the given
// topic/operation/outcome label set. No-op when m is nil or factory is nil
// (latter also emits a WARN once).
func (m *streamingMetrics) recordEmitted(ctx context.Context, topic, operation, outcome string) {
	if m == nil {
		return
	}

	if m.factory == nil {
		m.warnNilFactoryOnce(ctx)

		return
	}

	m.emittedOnce.Do(func() {
		builder, err := m.factory.Counter(metrics.Metric{
			Name:        metricNameEmitted,
			Unit:        "1",
			Description: "Total streaming emits by topic, operation, and outcome.",
		})
		if err != nil {
			m.logger.Log(ctx, log.LevelError, "streaming: metrics: create emitted counter",
				log.String("metric", metricNameEmitted), log.Err(err))

			return
		}

		m.emittedCounter = builder
	})

	if m.emittedCounter == nil {
		return
	}

	// Cache the labeled builder keyed by (topic, operation, outcome). The
	// hot path re-uses a single *CounterBuilder per tuple instead of
	// allocating one via WithLabels on every Add.
	key := topic + "\x00" + operation + "\x00" + outcome

	builder := m.getOrBuildCounter(&m.emittedCache, key, m.emittedCounter, func() map[string]string {
		return map[string]string{
			metricLabelTopic: topic,
			"operation":      operation,
			"outcome":        outcome,
		}
	})
	if builder == nil {
		return
	}

	if err := builder.Add(ctx, 1); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record emitted",
			log.String("metric", metricNameEmitted), log.Err(err))
	}
}

// recordEmitDuration adds a sample to streaming_emit_duration_ms. The unit is
// milliseconds to match the TRD name; callers pass time.Since(start).Milliseconds().
func (m *streamingMetrics) recordEmitDuration(ctx context.Context, topic, outcome string, durationMs int64) {
	if m == nil {
		return
	}

	if m.factory == nil {
		m.warnNilFactoryOnce(ctx)

		return
	}

	m.emitDurationOnce.Do(func() {
		builder, err := m.factory.Histogram(metrics.Metric{
			Name:        metricNameEmitDurationMS,
			Unit:        "ms",
			Description: "Streaming emit duration in milliseconds by topic and outcome.",
		})
		if err != nil {
			m.logger.Log(ctx, log.LevelError, "streaming: metrics: create duration histogram",
				log.String("metric", metricNameEmitDurationMS), log.Err(err))

			return
		}

		m.emitDurationHistogram = builder
	})

	if m.emitDurationHistogram == nil {
		return
	}

	key := topic + "\x00" + outcome

	builder := m.getOrBuildHistogram(&m.emitDurationCache, key, m.emitDurationHistogram, func() map[string]string {
		return map[string]string{
			metricLabelTopic: topic,
			"outcome":        outcome,
		}
	})
	if builder == nil {
		return
	}

	if err := builder.Record(ctx, durationMs); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record duration",
			log.String("metric", metricNameEmitDurationMS), log.Err(err))
	}
}

// recordDLQ increments streaming_dlq_total by 1. Called after a successful
// DLQ publish; the error_class label encodes which of the 8 ErrorClass values
// caused the quarantine.
func (m *streamingMetrics) recordDLQ(ctx context.Context, topic, errorClass string) {
	if m == nil {
		return
	}

	if m.factory == nil {
		m.warnNilFactoryOnce(ctx)

		return
	}

	m.dlqOnce.Do(func() {
		builder, err := m.factory.Counter(metrics.Metric{
			Name:        metricNameDLQTotal,
			Unit:        "1",
			Description: "Total events quarantined to the per-topic DLQ.",
		})
		if err != nil {
			m.logger.Log(ctx, log.LevelError, "streaming: metrics: create dlq counter",
				log.String("metric", metricNameDLQTotal), log.Err(err))

			return
		}

		m.dlqCounter = builder
	})

	if m.dlqCounter == nil {
		return
	}

	key := topic + "\x00" + errorClass

	builder := m.getOrBuildCounter(&m.dlqCache, key, m.dlqCounter, func() map[string]string {
		return map[string]string{
			metricLabelTopic: topic,
			"error_class":    errorClass,
		}
	})
	if builder == nil {
		return
	}

	if err := builder.Add(ctx, 1); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record dlq",
			log.String("metric", metricNameDLQTotal), log.Err(err))
	}
}

// recordDLQFailed increments streaming_dlq_publish_failed_total by 1. Called
// when the DLQ publish itself fails — the alerting signal operators watch
// because a failing DLQ means correlated broker failure across both source
// and DLQ topics.
func (m *streamingMetrics) recordDLQFailed(ctx context.Context, topic string) {
	if m == nil {
		return
	}

	if m.factory == nil {
		m.warnNilFactoryOnce(ctx)

		return
	}

	m.dlqFailedOnce.Do(func() {
		builder, err := m.factory.Counter(metrics.Metric{
			Name:        metricNameDLQFailed,
			Unit:        "1",
			Description: "Total DLQ publish attempts that failed themselves.",
		})
		if err != nil {
			m.logger.Log(ctx, log.LevelError, "streaming: metrics: create dlq_failed counter",
				log.String("metric", metricNameDLQFailed), log.Err(err))

			return
		}

		m.dlqFailedCounter = builder
	})

	if m.dlqFailedCounter == nil {
		return
	}

	builder := m.getOrBuildCounter(&m.dlqFailedCache, topic, m.dlqFailedCounter, func() map[string]string {
		return map[string]string{
			metricLabelTopic: topic,
		}
	})
	if builder == nil {
		return
	}

	if err := builder.Add(ctx, 1); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record dlq_failed",
			log.String("metric", metricNameDLQFailed), log.Err(err))
	}
}

// recordOutboxRouted increments streaming_outbox_routed_total by 1. Called
// when a publish falls back to the outbox. reason is a closed set:
// "circuit_open" (the only T6 caller) or "broker_error" (reserved for v1.1).
func (m *streamingMetrics) recordOutboxRouted(ctx context.Context, topic, reason string) {
	if m == nil {
		return
	}

	if m.factory == nil {
		m.warnNilFactoryOnce(ctx)

		return
	}

	m.outboxRoutedOnce.Do(func() {
		builder, err := m.factory.Counter(metrics.Metric{
			Name:        metricNameOutboxRouted,
			Unit:        "1",
			Description: "Total events routed to the outbox fallback by topic and reason.",
		})
		if err != nil {
			m.logger.Log(ctx, log.LevelError, "streaming: metrics: create outbox_routed counter",
				log.String("metric", metricNameOutboxRouted), log.Err(err))

			return
		}

		m.outboxRoutedCounter = builder
	})

	if m.outboxRoutedCounter == nil {
		return
	}

	key := topic + "\x00" + reason

	builder := m.getOrBuildCounter(&m.outboxRoutedCache, key, m.outboxRoutedCounter, func() map[string]string {
		return map[string]string{
			metricLabelTopic: topic,
			"reason":         reason,
		}
	})
	if builder == nil {
		return
	}

	if err := builder.Add(ctx, 1); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record outbox_routed",
			log.String("metric", metricNameOutboxRouted), log.Err(err))
	}
}

// recordCircuitState sets the streaming_circuit_state gauge. state is one of
// flagCBClosed / flagCBHalfOpen / flagCBOpen (0/1/2). The instrument has no
// labels — a single gauge per-process is sufficient.
//
// TRD §7.1 labels this "Int64UpDownCounter (gauge-like)". The underlying
// metrics factory only exposes Int64Gauge; semantically equivalent (both
// emit the current value, not a delta).
func (m *streamingMetrics) recordCircuitState(ctx context.Context, state int32) {
	if m == nil {
		return
	}

	if m.factory == nil {
		m.warnNilFactoryOnce(ctx)

		return
	}

	m.circuitStateOnce.Do(func() {
		builder, err := m.factory.Gauge(metrics.Metric{
			Name:        metricNameCircuitState,
			Unit:        "1",
			Description: "Circuit breaker state: 0=closed, 1=half-open, 2=open.",
		})
		if err != nil {
			m.logger.Log(ctx, log.LevelError, "streaming: metrics: create circuit_state gauge",
				log.String("metric", metricNameCircuitState), log.Err(err))

			return
		}

		m.circuitStateGauge = builder
	})

	if m.circuitStateGauge == nil {
		return
	}

	// circuit_state has no labels, so there is nothing to cache; the
	// labeled builder would be identical to the base builder.
	if err := m.circuitStateGauge.Set(ctx, int64(state)); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record circuit_state",
			log.String("metric", metricNameCircuitState), log.Err(err))
	}
}

// getOrBuildCounter returns a *CounterBuilder for the given labelset,
// caching it in cache keyed by key. First-hit path builds a new builder
// via WithLabels; subsequent hits reuse the same builder pointer. The
// label map is only allocated on the cache-miss branch, so the steady-state
// hot path is one sync.Map read + one type assertion. Returns nil if
// WithLabels returned a nil builder (shouldn't happen in practice).
func (m *streamingMetrics) getOrBuildCounter(
	cache *labelSetCache,
	key string,
	base *metrics.CounterBuilder,
	buildLabels func() map[string]string,
) *metrics.CounterBuilder {
	if cache == nil || base == nil || buildLabels == nil {
		return nil
	}

	if v, ok := cache.m.Load(key); ok {
		if builder, ok := v.(*metrics.CounterBuilder); ok {
			return builder
		}
	}

	built := base.WithLabels(buildLabels())
	if built == nil {
		return nil
	}

	actual, _ := cache.m.LoadOrStore(key, built)
	if builder, ok := actual.(*metrics.CounterBuilder); ok {
		return builder
	}

	return built
}

// getOrBuildHistogram mirrors getOrBuildCounter for histogram builders.
func (m *streamingMetrics) getOrBuildHistogram(
	cache *labelSetCache,
	key string,
	base *metrics.HistogramBuilder,
	buildLabels func() map[string]string,
) *metrics.HistogramBuilder {
	if cache == nil || base == nil || buildLabels == nil {
		return nil
	}

	if v, ok := cache.m.Load(key); ok {
		if builder, ok := v.(*metrics.HistogramBuilder); ok {
			return builder
		}
	}

	built := base.WithLabels(buildLabels())
	if built == nil {
		return nil
	}

	actual, _ := cache.m.LoadOrStore(key, built)
	if builder, ok := actual.(*metrics.HistogramBuilder); ok {
		return builder
	}

	return built
}
