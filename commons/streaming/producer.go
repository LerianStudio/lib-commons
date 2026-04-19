package streaming

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// tracerName + emitSpanName live in emit_span.go (colocated with the
// attribute-builder setEmitSpanAttributes) so the OTEL-naming contract is
// a single-file read.

// Circuit-breaker-related constants (flagCB*, cbServiceNamePrefix) and the
// initCircuitBreaker / buildCBConfig helpers live in cb_init.go. Keeping them
// colocated with the state listener (cb_listener.go) makes the CB integration
// easy to reason about as a unit.
//
// Close/CloseContext/Healthy and healthPingTimeout live in lifecycle.go so
// this file stays focused on construction and the hot-path Emit dispatch.

// Producer is the franz-go-backed Emitter implementation. It owns one
// *kgo.Client per instance; the client multiplexes produces across broker
// connections internally.
//
// Safe for concurrent use from any number of goroutines (DX-A03). Internal
// state uses atomics; no user-visible mutex. Background components (circuit-
// state listener, metrics, span wrap) land in T3-T6; T2 ships the bare
// happy-path plus synchronous Close / Healthy.
//
// Compile-time assertion below (var _ Emitter) pins the three-method
// interface so a refactor that drops a method fails the build immediately.
type Producer struct {
	// client is the franz-go broker client. Assembled from Config in New.
	// Holds internal broker connections, batching, and retry state.
	client *kgo.Client

	// cfg is the effective runtime configuration — values after env load,
	// validation, and option overrides.
	cfg Config

	// cbManager is the shared circuit-breaker manager. Non-nil after New;
	// either the caller supplied one via WithCircuitBreakerManager or the
	// Producer built its own. Multiple Producers can share a manager so the
	// caller has a process-wide view of breaker state.
	cbManager circuitbreaker.Manager

	// cb is this Producer's breaker instance — always the one named
	// "streaming.producer:<producerID>" in cbManager.
	cb circuitbreaker.CircuitBreaker

	// cbServiceName is the service name registered with cbManager. Stored
	// so the state-change listener can filter events that belong to other
	// breakers in a shared manager (the listener is called for every
	// service, not just ours).
	cbServiceName string

	// cbStateFlag mirrors the breaker's state via the state-change listener.
	// Writes come exclusively from the listener; reads come from the publish
	// hot path. Using atomic.Int32 keeps both lock-free. Values are the
	// flagCB* constants above.
	cbStateFlag atomic.Int32

	// tracer is the OTEL tracer used for the streaming.emit span. Never
	// nil after NewProducer — falls back to otel.Tracer("streaming") when
	// the caller did not supply WithTracer.
	tracer trace.Tracer

	// logger is the structured logger. Never nil; New substitutes
	// log.NewNop() when none supplied, so internal log sites can call
	// Log(ctx, ...) unguarded.
	logger log.Logger

	// metrics holds lazy-initialised OTEL instruments. Never nil after
	// NewProducer; when WithMetricsFactory is omitted (or passed nil) the
	// streamingMetrics still wraps a nil factory and degrades record* calls
	// to a single WARN + silent no-ops. Kept non-nil so Emit can call
	// p.metrics.recordEmitted unconditionally.
	metrics *streamingMetrics

	// closed flips to true on Close; subsequent Emit calls return
	// ErrEmitterClosed synchronously before any I/O.
	closed atomic.Bool

	// stop is closed by CloseContext on first successful CAS. RunContext
	// selects on this channel alongside ctx.Done() so app-bootstrap-driven
	// shutdown and ctx-cancel-driven shutdown converge on the same exit
	// path. Unbuffered, close-only — never sent on.
	stop chan struct{}

	// stopOnce guards the close(p.stop) call so concurrent callers do not
	// panic on a double-close. Mirrors outbox.Dispatcher.stopOnce.
	stopOnce sync.Once

	// producerID uniquely identifies this Producer instance. Used in the
	// circuit-breaker service name and as a span attribute.
	producerID string

	// partFn, when non-nil, overrides Event.PartitionKey() at publish time.
	// Default (nil) means struct-level PartitionKey().
	partFn func(Event) string

	// toggles copies the Config.EventToggles map at construction. Reads in
	// publishDirect are racy-by-design: operators are expected to redeploy
	// to flip an event toggle, not hot-reload.
	toggles map[string]bool

	// closeTimeout caps Close's Flush deadline. Resolved in New from the
	// option override or Config.CloseTimeout.
	closeTimeout time.Duration

	// outbox, when non-nil, enables the circuit-open fallback: Emit writes
	// a serialized event to the OutboxRepository and returns nil instead
	// of ErrCircuitOpen while the breaker is open. The corresponding relay
	// is registered via RegisterOutboxHandler on the outbox Dispatcher's
	// HandlerRegistry. Nil means fallback is disabled (T3 behavior).
	outbox outbox.OutboxRepository

	// allowSystemEvents, when true, permits Event.SystemEvent=true through
	// preflight. When false (the default), any SystemEvent emission is
	// rejected synchronously with ErrSystemEventsNotAllowed. See
	// WithAllowSystemEvents.
	allowSystemEvents bool
}

// Compile-time assertion: *Producer must satisfy Emitter. A missing method
// fails the build here rather than at a distant call site.
var _ Emitter = (*Producer)(nil)

// New constructs an Emitter.
//
// When Config.Enabled is false OR the broker list is empty, New returns a
// NoopEmitter without constructing a franz-go client — this is the fail-safe
// for services that run without a broker (feature-flag-off, local-dev).
// Callers who need a real *Producer in such environments should use
// NewProducer (see below) which forces construction and returns an error
// when the broker is unreachable.
//
// Validation runs on the Config before any franz-go calls so common caller
// mistakes (empty brokers, invalid compression codec, missing CloudEvents
// source) surface as a single wrapped error.
func New(ctx context.Context, cfg Config, opts ...EmitterOption) (Emitter, error) {
	// Fail-safe branches: no broker configured / disabled master switch.
	// Both return the NoopEmitter — same contract, zero state.
	if !cfg.Enabled || len(cfg.Brokers) == 0 {
		return NewNoopEmitter(), nil
	}

	return NewProducer(ctx, cfg, opts...)
}

// NewProducer is the unconditional Producer constructor. It never substitutes
// a NoopEmitter; callers who need a guaranteed real producer (e.g. tests that
// type-assert on *Producer) reach for this. When Config.Enabled is false or
// the broker list is empty, NewProducer returns the corresponding validation
// error rather than silently falling back.
//
// For normal service bootstrap, prefer New — it picks the right
// implementation from Config alone.
func NewProducer(ctx context.Context, cfg Config, opts ...EmitterOption) (*Producer, error) {
	// Belt-and-suspenders: even if LoadConfig wasn't used, re-validate here
	// so ad-hoc Config{} constructions don't slip past with missing fields.
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("streaming: invalid config: %w", err)
	}

	// Assemble the emitter options.
	resolvedOpts := &emitterOptions{}

	for _, apply := range opts {
		if apply != nil {
			apply(resolvedOpts)
		}
	}

	logger := resolvedOpts.logger
	if logger == nil {
		logger = log.NewNop()
	}

	// Resolve close timeout: explicit option > Config default > hard-coded
	// fallback. Zero option duration is treated as "use Config", matching
	// the documented semantics on WithCloseTimeout.
	closeTimeout := resolvedOpts.closeTimeout
	if closeTimeout <= 0 {
		closeTimeout = cfg.CloseTimeout
	}

	if closeTimeout <= 0 {
		closeTimeout = 30 * time.Second
	}

	// Build the franz-go options slice. Every knob is pinned explicitly per
	// TRD risk R1 — franz-go's defaults have flipped between versions in
	// the past, and a silent latency change would be operationally
	// catastrophic.
	kgoOpts, err := buildKgoOpts(cfg, *resolvedOpts)
	if err != nil {
		return nil, err
	}

	client, err := kgo.NewClient(kgoOpts...)
	if err != nil {
		return nil, fmt.Errorf("streaming: kgo client init: %w", err)
	}

	// Tracer fallback. A nil tracer here means the caller did not invoke
	// WithTracer; we use the global tracer provider's "streaming" tracer
	// so spans still surface in whatever provider the service bootstrapped.
	// If the process has no provider set, otel.Tracer returns a no-op
	// tracer — start/end are cheap and correct under that backend.
	tracer := resolvedOpts.tracer
	if tracer == nil {
		tracer = otel.Tracer(tracerName)
	}

	p := &Producer{
		client:            client,
		cfg:               cfg,
		cbManager:         resolvedOpts.cbManager,
		tracer:            tracer,
		logger:            logger,
		metrics:           newStreamingMetrics(resolvedOpts.metricsFactory, logger),
		producerID:        uuid.NewString(),
		partFn:            resolvedOpts.partitionKeyFn,
		toggles:           cfg.EventToggles,
		closeTimeout:      closeTimeout,
		outbox:            resolvedOpts.outbox,
		stop:              make(chan struct{}),
		allowSystemEvents: resolvedOpts.allowSystemEvents,
	}

	// Wire the circuit breaker: resolve manager, register service-named
	// breaker, register state listener. initCircuitBreaker populates
	// p.cb, p.cbManager, p.cbServiceName. Failure here means we cannot
	// publish safely; close the client to release sockets and propagate.
	if err := p.initCircuitBreaker(); err != nil {
		p.client.Close()

		return nil, err
	}

	return p, nil
}

// Emit and its helpers live in emit.go (plus setEmitSpanAttributes in
// emit_span.go). Keeping producer.go focused on construction/lifecycle
// makes it easier to audit the hot path in isolation.
