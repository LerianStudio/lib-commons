// Package healthcheck gates the per-tenant health check of a cached connection.
// Each multi-tenant connection manager (postgres, mongo) asks the gate what to do
// before pinging a cached connection, and the gate enforces three things: at most
// one check per interval per tenant, at most one check in flight per tenant, and
// no caller handed a connection whose in-flight check has not yet published a
// verdict -- otherwise a caller could be given a pool that the check is about to
// condemn and close.
package healthcheck

import (
	"context"
	"sync"
	"time"
)

// Action is what a caller must do about the cached connection for a tenant.
type Action int

const (
	// Skip means the last check passed recently enough; use the cached connection.
	Skip Action = iota
	// Run means this caller owns the check and must publish the verdict with End.
	Run
	// Wait means another caller owns the check: Await the channel Begin returned,
	// then resolve the tenant from scratch, since the check may have evicted it.
	Wait
)

// Gate tracks, per tenant, when the cached connection last passed a health check
// and whether a check is in flight. A nil *Gate is usable and asks every caller to
// run its own check.
type Gate struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[string]time.Time
	inflight map[string]chan struct{}
}

// NewGate returns a gate that allows one check per interval per tenant. An interval
// of zero or less disables the gate: every caller runs its own check, which is the
// behaviour from before the gate existed.
func NewGate(interval time.Duration) *Gate {
	return &Gate{
		interval: max(interval, 0),
		last:     make(map[string]time.Time),
		inflight: make(map[string]chan struct{}),
	}
}

// Begin decides what the caller must do about the cached connection for key. The
// returned channel is non-nil only for Wait, and is closed once the in-flight check
// publishes its verdict.
func (g *Gate) Begin(key string) (Action, <-chan struct{}) {
	if g == nil || g.interval <= 0 {
		return Run, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if done, ok := g.inflight[key]; ok {
		return Wait, done
	}

	if last, ok := g.last[key]; ok && time.Since(last) <= g.interval {
		return Skip, nil
	}

	g.inflight[key] = make(chan struct{})

	return Run, nil
}

// End publishes the verdict of a check the caller owned and releases everyone
// waiting on it. The check time is recorded only when the connection passed, so a
// failure leaves the next caller due to check whatever connection replaces it.
// Callers must call End for every Begin that returned Run, including when the check
// panics, or waiting callers never wake.
func (g *Gate) End(key string, healthy bool) {
	if g == nil || g.interval <= 0 {
		return
	}

	g.mu.Lock()

	done := g.inflight[key]
	delete(g.inflight, key)

	if healthy {
		g.last[key] = time.Now()
	} else {
		delete(g.last, key)
	}

	g.mu.Unlock()

	if done != nil {
		close(done)
	}
}

// Forget drops the recorded check time for a key, so the connection that replaces
// it is checked on its first use. Managers call this wherever they evict a tenant.
// An in-flight check is left alone: its owner still has to publish a verdict to the
// callers waiting on it.
func (g *Gate) Forget(key string) {
	if g == nil {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.last, key)
}

// ForgetAll drops every recorded check time, for a manager closing all its tenants.
func (g *Gate) ForgetAll() {
	if g == nil {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	clear(g.last)
}

// Await blocks until the in-flight check publishes its verdict, or the caller's
// context ends first.
func Await(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
