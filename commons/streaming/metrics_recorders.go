package streaming

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// This file holds the six record* methods that write to the OTEL
// instruments. Split from metrics.go so neither file crosses the 300-line
// cap and so the ceremonial-but-verbose lazy-init pattern is easy to skim
// top-down.
//
// Every method follows the same shape:
//   1. Nil-receiver guard.
//   2. Nil-factory guard (logs once via warnNilFactoryOnce).
//   3. sync.Once-guarded builder creation. On creation failure, log at
//      ERROR and leave the builder nil — subsequent record* calls silently
//      no-op via the "if builder == nil { return }" check.
//   4. Build a label set and write to the instrument; log WARN on any
//      write error (extremely rare — negative counter value etc.).
//
// The shape is identical across all six methods; the only thing that
// differs is the metric name, label keys, and write-operation (Add / Set /
// Record). No abstraction layer is introduced because the concrete,
// line-by-line shape makes a failed-build-then-silent-no-op invariant
// easy to audit under time pressure.

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

	if err := m.emittedCounter.WithLabels(map[string]string{
		"topic":     topic,
		"operation": operation,
		"outcome":   outcome,
	}).Add(ctx, 1); err != nil {
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

	if err := m.emitDurationHistogram.WithLabels(map[string]string{
		"topic":   topic,
		"outcome": outcome,
	}).Record(ctx, durationMs); err != nil {
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

	if err := m.dlqCounter.WithLabels(map[string]string{
		"topic":       topic,
		"error_class": errorClass,
	}).Add(ctx, 1); err != nil {
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

	if err := m.dlqFailedCounter.WithLabels(map[string]string{
		"topic": topic,
	}).Add(ctx, 1); err != nil {
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

	if err := m.outboxRoutedCounter.WithLabels(map[string]string{
		"topic":  topic,
		"reason": reason,
	}).Add(ctx, 1); err != nil {
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

	if err := m.circuitStateGauge.Set(ctx, int64(state)); err != nil {
		m.logger.Log(ctx, log.LevelWarn, "streaming: metrics: record circuit_state",
			log.String("metric", metricNameCircuitState), log.Err(err))
	}
}
