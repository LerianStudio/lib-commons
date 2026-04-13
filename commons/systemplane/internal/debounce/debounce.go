// Package debounce provides a trailing-edge, per-key debouncer used by the
// systemplane Client to coalesce rapid change notifications into a single
// callback invocation.
package debounce

import "time"

// Debouncer coalesces rapid submissions for the same key, firing the
// most-recently submitted function after the configured window elapses
// with no new submissions for that key.
//
// All methods are nil-receiver safe.
type Debouncer struct {
	window time.Duration
}

// New creates a Debouncer with the given trailing-edge window.
// A zero or negative window disables debouncing (functions fire immediately).
func New(window time.Duration) *Debouncer {
	return &Debouncer{window: window}
}

// Submit schedules fn to be called after the debounce window for key.
// If Submit is called again for the same key before the window elapses,
// the previous fn is replaced and the timer resets.
//
// Nil-receiver safe: does nothing on a nil Debouncer.
func (d *Debouncer) Submit(key string, fn func()) {
	if d == nil {
		return
	}

	// TODO(phase-4): implement per-key timer map with mutex
	_ = key
	_ = fn
}

// Close stops all pending timers and releases resources. Idempotent.
//
// Nil-receiver safe: does nothing on a nil Debouncer.
func (d *Debouncer) Close() {
	if d == nil {
		return
	}

	// TODO(phase-4): stop all timers, clear map
}
