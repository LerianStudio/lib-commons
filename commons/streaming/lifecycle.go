package streaming

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// healthPingTimeout caps the per-Healthy broker Ping. 500ms is aligned with
// commons/rabbitmq and the `net/http.HealthWithDependencies` conventions —
// readiness probes should return fast.
const healthPingTimeout = 500 * time.Millisecond

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
//
// Closes the internal stop channel so any in-flight (*Producer).RunContext
// unblocks — converging ctx-cancel-driven and Close-driven shutdown on the
// same exit path. stopOnce protects against a double-close panic when
// RunContext and an external Close race.
//
// Nil-ctx safe: if ctx is nil, falls back to context.Background. Close()
// already passes context.Background, but a direct external CloseContext(nil)
// should not panic.
func (p *Producer) CloseContext(ctx context.Context) error {
	if p == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// CompareAndSwap guarantees exactly-one Flush + Close even under
	// concurrent Close callers.
	if !p.closed.CompareAndSwap(false, true) {
		// Already closed — but still ensure the stop channel is closed
		// in case this Producer was constructed before the stop chan
		// was introduced (defensive; current New always sets it).
		p.signalStop()

		return nil
	}

	// Unblock any in-flight RunContext before Flush so the Launcher
	// goroutine can observe the shutdown and exit cleanly. Safe to call
	// before Flush — RunContext's own CloseContext invocation is a no-op
	// under the CAS guard above.
	p.signalStop()

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

// signalStop closes the stop channel exactly once. Safe for concurrent
// callers via sync.Once. Nil-safe on the channel field so Producers
// constructed without the stop chan (theoretically possible in tests that
// build a *Producer directly) don't panic.
func (p *Producer) signalStop() {
	if p == nil {
		return
	}

	p.stopOnce.Do(func() {
		if p.stop != nil {
			close(p.stop)
		}
	})
}

// Healthy reports readiness. Returns nil when the Producer is not closed and
// the broker responds to a Ping within healthPingTimeout. Otherwise returns a
// *HealthError with a typed State() so consumers of
// net/http.HealthWithDependencies can differentiate Degraded from Down.
//
// T2 ships the broker-ping rung only. The Degraded/Down split based on
// outbox availability arrives with T4 (outbox integration).
//
// Nil-receiver safe. Nil-ctx safe: falls back to context.Background if ctx
// is nil (RunContext applies the same defense; symmetry across lifecycle
// entry points).
func (p *Producer) Healthy(ctx context.Context) error {
	if p == nil {
		return NewHealthError(Down, ErrNilProducer)
	}

	if ctx == nil {
		ctx = context.Background()
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
