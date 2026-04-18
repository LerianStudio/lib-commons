package streaming

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

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

	// tracer is the OTEL tracer used for the streaming.emit span. nil
	// falls back to the global tracer provider (T6 owns the wrap).
	tracer trace.Tracer

	// logger is the structured logger. Never nil; New substitutes
	// log.NewNop() when none supplied, so internal log sites can call
	// Log(ctx, ...) unguarded.
	logger log.Logger

	// closed flips to true on Close; subsequent Emit calls return
	// ErrEmitterClosed synchronously before any I/O.
	closed atomic.Bool

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
	kgoOpts, err := buildKgoOpts(cfg)
	if err != nil {
		return nil, err
	}

	client, err := kgo.NewClient(kgoOpts...)
	if err != nil {
		return nil, fmt.Errorf("streaming: kgo client init: %w", err)
	}

	p := &Producer{
		client:       client,
		cfg:          cfg,
		cbManager:    resolvedOpts.cbManager,
		tracer:       resolvedOpts.tracer,
		logger:       logger,
		producerID:   uuid.NewString(),
		partFn:       resolvedOpts.partitionKeyFn,
		toggles:      cfg.EventToggles,
		closeTimeout: closeTimeout,
		outbox:       resolvedOpts.outbox,
	}

	// Wire the circuit breaker: resolve manager, register service-named
	// breaker, register state listener. initCircuitBreaker populates
	// p.cb, p.cbManager, p.cbServiceName. Failure here means we cannot
	// publish safely; close the client to release sockets and propagate.
	if err := p.initCircuitBreaker(); err != nil {
		p.client.Close()

		return nil, err
	}

	// Touch ctx to silence unused-parameter warnings; T6 uses it to create
	// span/metric resources. We deliberately keep the ctx parameter today
	// so the T3 signature is forward-compatible with T4-T7.
	_ = ctx

	return p, nil
}

// Emit publishes a single Event. It performs caller-side validation
// synchronously (tenant/source/size/JSON), applies defaults on a local copy
// so the caller's struct is untouched, then dispatches the produce through
// the circuit-breaker wrapper so infrastructure faults feed the breaker but
// caller faults do NOT.
//
// Circuit-open behavior:
//   - With WithOutboxRepository wired: Emit writes the event to the outbox
//     and returns nil. The outbox Dispatcher will drain it via the handler
//     registered by RegisterOutboxHandler (bypasses the breaker on replay).
//   - Without an outbox wired: Emit returns ErrCircuitOpen so the caller
//     can fail fast or implement its own fallback.
//
// In either case the circuit breaker itself stays untouched during the
// circuit-open branch — it is already OPEN; there is nothing to "feed".
//
// Nil-receiver safe: returns ErrNilProducer rather than panicking.
func (p *Producer) Emit(ctx context.Context, event Event) error {
	if p == nil {
		return ErrNilProducer
	}

	if p.closed.Load() {
		return ErrEmitterClosed
	}

	// Apply defaults on a local copy first so the caller's struct is not
	// mutated. Safe before validation because defaults (EventID, Timestamp,
	// SchemaVersion, DataContentType) do not influence any validation check.
	(&event).ApplyDefaults()

	// Pre-flight validation runs OUTSIDE the circuit-breaker wrapper so a
	// barrage of caller mistakes cannot trip the breaker — breaker counts
	// only reflect infrastructure health, not caller hygiene.
	if err := p.preFlight(event); err != nil {
		return err
	}

	// cb.Execute takes a closure with signature func() (any, error). We close
	// over the outer ctx and defaulted-event. A typed-nil result is fine;
	// the caller discards it. Errors are propagated verbatim — Execute wraps
	// ErrOpenState / ErrTooManyRequests into its own diagnostic so callers
	// can still errors.Is for those sentinels.
	_, err := p.cb.Execute(func() (any, error) {
		// Circuit-open fallback. When the breaker is OPEN we observe the
		// mirrored flag and either (a) route to the outbox if one is
		// configured — caller sees nil after a durable write — or (b) fail
		// fast with ErrCircuitOpen. Either way we return (nil, nil) on the
		// outbox-success path so gobreaker leaves the breaker untouched;
		// returning an error here would count against the already-open
		// breaker for no useful reason.
		if p.cbStateFlag.Load() == flagCBOpen {
			if p.outbox != nil {
				if obxErr := p.publishToOutbox(ctx, event); obxErr != nil {
					// Outbox write itself failed: surface the error so the
					// caller knows the event wasn't durably captured. This
					// is a rare but load-bearing failure mode — silent drop
					// here would lose the event entirely.
					return nil, obxErr
				}

				// Outbox wrote successfully. Caller sees nil; Dispatcher
				// will drain the row back through publishDirect once the
				// broker recovers.
				return nil, nil //nolint:nilnil // CB Execute signature mandates (any, error); nil result is discarded
			}

			// No outbox configured — T3 behavior preserved. Caller gets an
			// explicit sentinel they can errors.Is against.
			return nil, ErrCircuitOpen
		}

		if err := p.publishDirect(ctx, event); err != nil {
			return nil, err
		}

		// Success: the result value is unused by Emit and the CB manager
		// only inspects the error. A nil, nil return is the idiomatic
		// success signal for this Execute signature.
		return nil, nil //nolint:nilnil // CB Execute signature mandates (any, error); nil result is discarded
	})

	return err
}
