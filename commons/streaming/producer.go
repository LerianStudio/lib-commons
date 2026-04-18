package streaming

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// healthPingTimeout caps the per-Healthy broker Ping. 500ms is aligned with
// commons/rabbitmq and the `net/http.HealthWithDependencies` conventions —
// readyness probes should return fast.
const healthPingTimeout = 500 * time.Millisecond

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

	// cbManager is the shared circuit-breaker manager; nil until T3 wires
	// it. Retained here so T3 can patch without reshaping the struct.
	cbManager circuitbreaker.Manager

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
	}

	// Touch ctx to silence unused-parameter warnings; T3 uses it to create
	// the CB manager, T6 to create span/metric resources. We deliberately
	// keep the ctx parameter today so the T2 signature is forward-
	// compatible with T3-T7.
	_ = ctx

	return p, nil
}

// Emit publishes a single Event. See (*Producer).publishDirect for the
// validation-then-produce implementation. This is the public façade; T2
// keeps it a thin wrapper so T3 can slot in the circuit-breaker Execute
// closure without reshaping callers.
//
// Nil-receiver safe: returns ErrNilProducer rather than panicking.
func (p *Producer) Emit(ctx context.Context, event Event) error {
	if p == nil {
		return ErrNilProducer
	}

	return p.publishDirect(ctx, event)
}

// Close is idempotent: subsequent calls return nil without re-flushing. The
// first call flips the atomic, flushes the underlying franz-go client under
// a deadline derived from Producer.closeTimeout, then closes the client.
//
// Flush errors surface as returned errors so the caller can decide whether
// to proceed with shutdown or wait — but Client.Close is called regardless
// so socket resources always get reclaimed.
//
// Nil-receiver safe.
func (p *Producer) Close() error {
	if p == nil {
		return nil
	}

	return p.CloseContext(context.Background())
}

// CloseContext is the paired ctx-aware variant. The provided context bounds
// the Flush; if the caller passes a context that's already canceled, Flush
// returns immediately and any un-flushed records are abandoned.
//
// A fresh deadline derived from Producer.closeTimeout is applied on top of
// the caller's context so Flush cannot hang indefinitely even on a
// background context. This preserves the "Close never blocks service
// shutdown forever" contract.
func (p *Producer) CloseContext(ctx context.Context) error {
	if p == nil {
		return nil
	}

	// CompareAndSwap guarantees exactly-one Flush + Close even under
	// concurrent Close callers.
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}

	if p.client == nil {
		return nil
	}

	// Derive a deadline so a misbehaving broker or stuck flush cannot
	// deadlock service shutdown. If the caller already supplied a shorter
	// deadline, context.WithTimeout uses the tighter one.
	flushCtx, cancel := context.WithTimeout(ctx, p.closeTimeout)
	defer cancel()

	flushErr := p.client.Flush(flushCtx)

	// Always close the client so connection sockets are reclaimed even on
	// Flush failure. kgo.Client.Close returns no error.
	p.client.Close()

	if flushErr != nil {
		return fmt.Errorf("streaming: flush on close: %w", flushErr)
	}

	return nil
}

// Healthy reports readiness. Returns nil when the Producer is not closed and
// the broker responds to a Ping within healthPingTimeout. Otherwise returns a
// *HealthError with a typed State() so consumers of
// net/http.HealthWithDependencies can differentiate Degraded from Down.
//
// T2 ships the broker-ping rung only. The Degraded/Down split based on
// outbox availability arrives with T4 (outbox integration).
//
// Nil-receiver safe.
func (p *Producer) Healthy(ctx context.Context) error {
	if p == nil {
		return NewHealthError(Down, ErrNilProducer)
	}

	if p.closed.Load() {
		return NewHealthError(Down, ErrEmitterClosed)
	}

	if p.client == nil {
		return NewHealthError(Down, errors.New("streaming: client not initialized"))
	}

	pingCtx, cancel := context.WithTimeout(ctx, healthPingTimeout)
	defer cancel()

	if err := p.client.Ping(pingCtx); err != nil {
		return NewHealthError(Degraded, err)
	}

	return nil
}
