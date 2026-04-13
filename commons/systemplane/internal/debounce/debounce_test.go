package debounce

import (
	"sync/atomic"
	"testing"
	"time"
)

const (
	testWindow = 50 * time.Millisecond
	// slack provides headroom for timer scheduling jitter.
	slack = 100 * time.Millisecond
)

func TestDebouncer_SingleSubmitFiresAfterWindow(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	t.Cleanup(d.Close)

	var fired atomic.Int32

	d.Submit("k", func() { fired.Add(1) })

	time.Sleep(testWindow + slack)

	if got := fired.Load(); got != 1 {
		t.Fatalf("expected fn to fire exactly once, got %d", got)
	}
}

func TestDebouncer_RapidSubmitsCoalesce(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	t.Cleanup(d.Close)

	var fired atomic.Int32

	for range 10 {
		d.Submit("k", func() { fired.Add(1) })
		time.Sleep(testWindow / 5) // each submit is well within the window
	}

	// Wait for the trailing edge to fire.
	time.Sleep(testWindow + slack)

	if got := fired.Load(); got != 1 {
		t.Fatalf("expected coalesced to a single fire, got %d", got)
	}
}

func TestDebouncer_DifferentKeysIndependent(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	t.Cleanup(d.Close)

	var firedA, firedB atomic.Int32

	d.Submit("a", func() { firedA.Add(1) })
	d.Submit("b", func() { firedB.Add(1) })

	time.Sleep(testWindow + slack)

	if firedA.Load() != 1 {
		t.Fatalf("expected key 'a' to fire once, got %d", firedA.Load())
	}

	if firedB.Load() != 1 {
		t.Fatalf("expected key 'b' to fire once, got %d", firedB.Load())
	}
}

func TestDebouncer_ClosePreventsFires(t *testing.T) {
	t.Parallel()

	d := New(testWindow)

	var fired atomic.Int32

	d.Submit("k", func() { fired.Add(1) })
	d.Close()

	// Wait well past the window.
	time.Sleep(2 * testWindow)

	if got := fired.Load(); got != 0 {
		t.Fatalf("expected no fire after Close, got %d", got)
	}
}

func TestDebouncer_NilReceiverSafe(t *testing.T) {
	t.Parallel()

	var d *Debouncer
	// Must not panic.
	d.Submit("k", func() {})
	d.Close()
}

func TestDebouncer_PanicInFnRecovered(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	t.Cleanup(d.Close)

	var secondFired atomic.Int32

	// First submit: panics.
	d.Submit("k1", func() { panic("boom") })
	time.Sleep(testWindow + slack)

	// Second submit after the panic: debouncer must still work.
	d.Submit("k2", func() { secondFired.Add(1) })
	time.Sleep(testWindow + slack)

	if secondFired.Load() != 1 {
		t.Fatal("debouncer broke after panic; second submit did not fire")
	}
}

func TestDebouncer_ZeroWindowInvokesSync(t *testing.T) {
	t.Parallel()

	d := New(0)
	t.Cleanup(d.Close)

	fired := false

	d.Submit("k", func() { fired = true })

	// With zero window, fn is invoked synchronously — no sleep needed.
	if !fired {
		t.Fatal("expected synchronous invocation with zero window")
	}
}

func TestDebouncer_CloseIdempotent(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	// Multiple closes must not panic.
	d.Close()
	d.Close()
	d.Close()
}

func TestDebouncer_SubmitAfterCloseIsNoop(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	d.Close()

	var fired atomic.Int32

	d.Submit("k", func() { fired.Add(1) })
	time.Sleep(testWindow + slack)

	if got := fired.Load(); got != 0 {
		t.Fatalf("expected no fire after Close, got %d", got)
	}
}

func TestDebouncer_NilFnIgnored(t *testing.T) {
	t.Parallel()

	d := New(testWindow)
	t.Cleanup(d.Close)

	// Must not panic.
	d.Submit("k", nil)
}
