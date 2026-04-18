package streaming

import (
	"context"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// Outcome label values. The "outcome" label on streaming metrics is a closed
// enum of five values per TRD §7.1 — Gate 2 made this addition to keep
// histogram bucket-cardinality bounded. NEVER introduce a sixth value without
// a TRD amendment: downstream dashboards and SLO alerts key off this set.
//
// - outcomeProduced: direct publish succeeded (CLOSED circuit, broker healthy)
// - outcomeOutboxed: circuit OPEN but outbox fallback wrote the event durably
// - outcomeCircuitOpen: circuit OPEN and no outbox → caller got ErrCircuitOpen
// - outcomeCallerError: preflight rejection or caller-class EmitError
// - outcomeDLQ: publish failed with an infra class; payload went to {topic}.dlq
const (
	outcomeProduced    = "produced"
	outcomeOutboxed    = "outboxed"
	outcomeCircuitOpen = "circuit_open"
	outcomeCallerError = "caller_error"
	outcomeDLQ         = "dlq"
)

// Metric names. Kept colocated with the recorder so a TRD rename is a one-file
// edit. Names match TRD §7.1 verbatim — do not reword casually; they are the
// public contract for Grafana dashboards and alert rules.
const (
	metricNameEmitted        = "streaming_emitted_total"
	metricNameEmitDurationMS = "streaming_emit_duration_ms"
	metricNameDLQTotal       = "streaming_dlq_total"
	metricNameDLQFailed      = "streaming_dlq_publish_failed_total"
	metricNameOutboxRouted   = "streaming_outbox_routed_total"
	metricNameCircuitState   = "streaming_circuit_state"
)

// streamingMetrics holds lazy-initialised OTEL instruments for the streaming
// package. Mirrors the pattern in commons/circuitbreaker/manager.go:85-103
// but per-instrument-lazy (instead of one-shot init) so a nil factory logs
// once and subsequent callers pay zero cost beyond the sync.Once check.
//
// Nil-safety rules (every exported record* method enforces these):
//   - nil receiver → no-op, no panic
//   - nil factory  → warn-once log, then no-op forever after
//   - builder creation error → log at ERROR level, record is a no-op; we
//     do NOT retry because a creation failure indicates a misconfigured
//     meter that retries won't fix
//
// Concurrency: sync.Once protects each instrument's first build. Reads of
// the *Builder pointer after the Once runs are safe because sync.Once
// establishes happens-before for the write.
//
// Per-instrument record methods live in metrics_recorders.go — split from
// this file to keep both under the 300-line cap.
type streamingMetrics struct {
	// factory is the user-supplied metrics factory. MAY be nil — see warnOnce.
	factory *metrics.MetricsFactory

	// logger is never nil — newStreamingMetrics substitutes log.NewNop() when
	// the caller passes nil, mirroring the broader package convention.
	logger log.Logger

	// Instrument builders + their once-guards. A separate Once per instrument
	// keeps build failures isolated: if histogram creation fails, counters
	// still work.
	emittedOnce    sync.Once
	emittedCounter *metrics.CounterBuilder

	emitDurationOnce      sync.Once
	emitDurationHistogram *metrics.HistogramBuilder

	dlqOnce    sync.Once
	dlqCounter *metrics.CounterBuilder

	dlqFailedOnce    sync.Once
	dlqFailedCounter *metrics.CounterBuilder

	outboxRoutedOnce    sync.Once
	outboxRoutedCounter *metrics.CounterBuilder

	circuitStateOnce  sync.Once
	circuitStateGauge *metrics.GaugeBuilder

	// warnOnce guards the single "metrics disabled" WARN the Producer emits
	// when factory == nil at first-record time. Subsequent calls are silent.
	warnOnce sync.Once
}

// newStreamingMetrics constructs a streamingMetrics whose behaviour is
// determined by factory:
//
//   - factory != nil → real recording; instruments build lazily on first touch.
//   - factory == nil → all record* methods are no-ops after a single WARN log.
//
// logger is substituted with log.NewNop() when nil so record* methods can
// unconditionally call logger.Log without guarding.
func newStreamingMetrics(factory *metrics.MetricsFactory, logger log.Logger) *streamingMetrics {
	if logger == nil {
		logger = log.NewNop()
	}

	return &streamingMetrics{
		factory: factory,
		logger:  logger,
	}
}

// warnNilFactoryOnce emits a single WARN log when the metrics factory is nil.
// Subsequent calls are silent. The guard lives inside sync.Once so repeated
// nil-factory record* calls don't spam logs.
func (m *streamingMetrics) warnNilFactoryOnce(ctx context.Context) {
	if m == nil {
		return
	}

	m.warnOnce.Do(func() {
		m.logger.Log(ctx, log.LevelWarn,
			"streaming: metrics factory is nil; metrics are disabled")
	})
}

// durationMilliseconds converts a time.Duration to int64 milliseconds. A
// named helper so the units are obvious at call sites.
func durationMilliseconds(d time.Duration) int64 {
	return d.Milliseconds()
}
