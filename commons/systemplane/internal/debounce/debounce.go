// Package debounce provides a trailing-edge, per-key debouncer used by the
// systemplane Client to coalesce rapid change notifications into a single
// callback invocation.
//
// The Debouncer is generic on the key type (any Go comparable). Callers that
// previously used string keys can continue doing so; callers on the changefeed
// hot path use a struct key to avoid per-event string-concat allocations (see
// commons/systemplane/client.go onEvent).
package debounce

import (
	"fmt"
	"sync"
	"time"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-observability/runtime"
)

// Debouncer coalesces rapid submissions for the same key, firing the
// most-recently submitted function after the configured window elapses
// with no new submissions for that key.
//
// The type parameter K is any Go comparable type: strings, plain structs
// of comparable fields, pointers, etc. Structs are preferred on hot paths
// to avoid per-Submit string allocations.
//
// All methods are nil-receiver safe.
type Debouncer[K comparable] struct {
	window time.Duration
	logger log.Logger
	mu     sync.Mutex
	timers map[K]*time.Timer
	closed bool
}

// Option configures a Debouncer. The type parameter matches the Debouncer
// it will be applied to.
type Option[K comparable] func(*Debouncer[K])

// WithLogger sets a structured logger for panic-recovery diagnostics.
func WithLogger[K comparable](l log.Logger) Option[K] {
	return func(d *Debouncer[K]) {
		if l != nil {
			d.logger = l
		}
	}
}

// New creates a trailing-edge debouncer with the given quiet window.
// A zero or negative window disables debouncing: Submit invokes fn
// synchronously inline, with panic recovery via lib-observability/runtime.
func New[K comparable](window time.Duration, opts ...Option[K]) *Debouncer[K] {
	d := &Debouncer[K]{
		window: window,
		logger: log.NewNop(),
		timers: make(map[K]*time.Timer),
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// Submit schedules fn for invocation after the debouncer's quiet window
// elapses without another Submit for the same key. Subsequent calls for
// the same key reset the timer, so only the last-submitted fn fires.
//
// When the window is zero or negative, fn is invoked synchronously with
// panic recovery.
//
// Nil-receiver safe: does nothing if d is nil.
func (d *Debouncer[K]) Submit(key K, fn func()) {
	if d == nil || fn == nil {
		return
	}

	// Zero/negative window: synchronous invocation with panic recovery.
	if d.window <= 0 {
		d.invokeWithRecover(key, fn)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return
	}

	// Stop existing timer for this key (if any) so we can reset.
	if existing, ok := d.timers[key]; ok {
		existing.Stop()
	}

	d.timers[key] = time.AfterFunc(d.window, func() {
		d.fire(key, fn)
	})
}

// Close cancels all pending timers and marks the debouncer as closed.
// Further Submit calls become no-ops. Idempotent. Nil-receiver safe.
func (d *Debouncer[K]) Close() {
	if d == nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return
	}

	d.closed = true

	for key, timer := range d.timers {
		timer.Stop()
		delete(d.timers, key)
	}
}

// fire removes the key from the timer map (under lock) and invokes fn
// with panic recovery. If the debouncer has been closed between the
// timer being scheduled and firing, the invocation is skipped.
func (d *Debouncer[K]) fire(key K, fn func()) {
	d.mu.Lock()

	if d.closed {
		d.mu.Unlock()
		return
	}

	delete(d.timers, key)

	d.mu.Unlock()

	d.invokeWithRecover(key, fn)
}

// invokeWithRecover calls fn inside a deferred RecoverAndLog so that a
// panicking callback cannot crash the process or break the debouncer.
// The key is rendered with fmt.Sprint into the recovery component name
// so crash logs identify which key's callback blew up regardless of
// whether K is a string, struct, or something else.
func (d *Debouncer[K]) invokeWithRecover(key K, fn func()) {
	defer runtime.RecoverAndLog(d.logger, fmt.Sprintf("debounce:%v", key))

	fn()
}
