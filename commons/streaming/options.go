package streaming

import (
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"go.opentelemetry.io/otel/trace"
)

// EmitterOption configures a Producer at construction time. Options are
// functional; callers chain them in any order. Producer applies defaults for
// every unset option.
//
// Options that concern optional transport-level security (WithTLSConfig,
// WithSASL) land in T8 — deliberately absent here so unit tests never reach
// into kgo internals.
type EmitterOption func(*emitterOptions)

// emitterOptions is the internal struct assembled from every EmitterOption.
// Producer reads from this struct at New() and does not retain the value
// beyond construction.
type emitterOptions struct {
	// logger is the structured logger. Defaults to log.NewNop() when nil.
	logger log.Logger

	// metricsFactory produces OTEL instruments. Nil is valid — the Producer
	// falls back to a no-op recorder, logs a single warning at first Emit.
	metricsFactory *metrics.MetricsFactory

	// tracer is the OTEL tracer for streaming.emit spans. Nil falls back to
	// the global tracer provider.
	tracer trace.Tracer

	// cbManager, when non-nil, is reused so the caller's process-level manager
	// provides metrics, state listeners, and config governance. Nil means the
	// Producer creates its own manager from its logger.
	cbManager circuitbreaker.Manager

	// partitionKeyFn, when non-nil, overrides Event.PartitionKey() at publish
	// time. Useful for hot-tenant salting (see PRD R2).
	partitionKeyFn func(Event) string

	// closeTimeout caps how long Close waits for flush + outbox drain.
	// Zero means "use the STREAMING_CLOSE_TIMEOUT_S config default (30s)".
	closeTimeout time.Duration

	// outbox, when non-nil, enables circuit-open fallback: Emit writes the
	// event to the outbox instead of returning ErrCircuitOpen while the
	// breaker is open. The caller is responsible for constructing and
	// owning the OutboxRepository — streaming never builds one itself.
	outbox outbox.OutboxRepository
}

// WithLogger sets the structured logger used across the package. When not
// supplied, the Producer uses log.NewNop() so lib-commons stays silent by
// default in library code.
func WithLogger(l log.Logger) EmitterOption {
	return func(o *emitterOptions) {
		o.logger = l
	}
}

// WithMetricsFactory wires the OTEL metrics factory used to register the 6
// streaming instruments (see metrics.go in T6). Passing nil is supported —
// the Producer switches to a no-op recorder and logs a single warning on
// the first Emit.
func WithMetricsFactory(f *metrics.MetricsFactory) EmitterOption {
	return func(o *emitterOptions) {
		o.metricsFactory = f
	}
}

// WithTracer overrides the OTEL tracer used for the streaming.emit span.
// When unset, the Producer uses the global tracer provider.
func WithTracer(t trace.Tracer) EmitterOption {
	return func(o *emitterOptions) {
		o.tracer = t
	}
}

// WithCircuitBreakerManager lets the caller share a process-level
// circuitbreaker.Manager with the Producer. When omitted, the Producer
// constructs its own manager from the supplied logger.
func WithCircuitBreakerManager(m circuitbreaker.Manager) EmitterOption {
	return func(o *emitterOptions) {
		o.cbManager = m
	}
}

// WithPartitionKey overrides the default Event.PartitionKey() behavior at
// publish time. Use for hot-tenant salting (e.g. tenant_id|0..K-1) or to
// partition by aggregate ID within a tenant.
func WithPartitionKey(fn func(Event) string) EmitterOption {
	return func(o *emitterOptions) {
		o.partitionKeyFn = fn
	}
}

// WithCloseTimeout caps how long Close waits for buffered-record flush and
// outbox drain. A zero duration means "use the config default"; negative
// values are normalized to zero by the caller at construction time.
func WithCloseTimeout(d time.Duration) EmitterOption {
	return func(o *emitterOptions) {
		o.closeTimeout = d
	}
}

// WithOutboxRepository wires an OutboxRepository so the Producer can fall
// back to durable storage when the circuit breaker is open. Without this
// option, circuit-open Emits return ErrCircuitOpen.
//
// The typical pairing is: the caller also invokes
// (*Producer).RegisterOutboxHandler on the process-level outbox Dispatcher's
// HandlerRegistry so outbox rows are drained back through publishDirect once
// the broker recovers. See outbox_handler.go for that relay path.
//
// The Producer NEVER constructs an OutboxRepository itself; ownership and
// lifecycle stay with the consuming service. Passing nil is equivalent to
// not calling this option (circuit-open → ErrCircuitOpen).
func WithOutboxRepository(repo outbox.OutboxRepository) EmitterOption {
	return func(o *emitterOptions) {
		o.outbox = repo
	}
}
