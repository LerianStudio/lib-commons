//go:build unit

package streaming

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// --- Fake circuitbreaker.Manager + CircuitBreaker for deterministic CB
// state control. The real breaker (sony/gobreaker) cannot be driven into
// OPEN / HALF-OPEN on demand without crafting many failures; a fake lets us
// test the listener + flag-mirroring + publish-branching in isolation. ---

// fakeCB is a test-only circuitbreaker.CircuitBreaker that forwards Execute
// calls directly to the closure. It intentionally does NOT track failures —
// tests drive state via fakeCBManager.ForceTransition.
type fakeCB struct {
	state circuitbreaker.State
	mu    sync.Mutex
}

func (f *fakeCB) Execute(fn func() (any, error)) (any, error) {
	if fn == nil {
		return nil, circuitbreaker.ErrNilCallback
	}
	// The fake does not short-circuit on open state — the Emit closure
	// observes the producer's mirrored cbStateFlag instead. This mirrors
	// real behavior where gobreaker would short-circuit AFTER trip; the T3
	// path is about the flag mirror, not about what gobreaker does
	// pre-trip.
	return fn()
}

func (f *fakeCB) State() circuitbreaker.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeCB) Counts() circuitbreaker.Counts {
	return circuitbreaker.Counts{}
}

// fakeCBManager is a test-only circuitbreaker.Manager that lets tests force
// state transitions via ForceTransition, capturing listener notifications
// deterministically.
type fakeCBManager struct {
	mu        sync.Mutex
	breakers  map[string]*fakeCB
	listeners []circuitbreaker.StateChangeListener
}

func newFakeCBManager() *fakeCBManager {
	return &fakeCBManager{
		breakers: map[string]*fakeCB{},
	}
}

func (f *fakeCBManager) GetOrCreate(name string, _ circuitbreaker.Config) (circuitbreaker.CircuitBreaker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if b, ok := f.breakers[name]; ok {
		return b, nil
	}

	b := &fakeCB{state: circuitbreaker.StateClosed}
	f.breakers[name] = b
	return b, nil
}

func (f *fakeCBManager) Execute(_ string, _ func() (any, error)) (any, error) {
	return nil, errors.New("fakeCBManager.Execute unused in tests")
}

func (f *fakeCBManager) GetState(name string) circuitbreaker.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.breakers[name]; ok {
		return b.state
	}
	return circuitbreaker.StateUnknown
}

func (f *fakeCBManager) GetCounts(_ string) circuitbreaker.Counts {
	return circuitbreaker.Counts{}
}

func (f *fakeCBManager) IsHealthy(name string) bool {
	return f.GetState(name) != circuitbreaker.StateOpen
}

func (f *fakeCBManager) Reset(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.breakers[name]; ok {
		b.state = circuitbreaker.StateClosed
	}
}

func (f *fakeCBManager) RegisterStateChangeListener(listener circuitbreaker.StateChangeListener) {
	if listener == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listeners = append(f.listeners, listener)
}

// ForceTransition drives the named breaker's state and synchronously notifies
// every registered listener with (from, to). Used by tests to step through
// OPEN / HALF-OPEN / CLOSED without racing the gobreaker internals.
func (f *fakeCBManager) ForceTransition(name string, to circuitbreaker.State) {
	f.mu.Lock()

	b, ok := f.breakers[name]
	if !ok {
		f.mu.Unlock()
		return
	}

	from := b.state
	b.state = to

	// Copy listener slice so we notify outside the lock to avoid deadlock
	// if a listener calls back into the manager.
	listeners := make([]circuitbreaker.StateChangeListener, len(f.listeners))
	copy(listeners, f.listeners)
	f.mu.Unlock()

	ctx := context.Background()
	for _, l := range listeners {
		l.OnStateChange(ctx, name, from, to)
	}
}

// listenerCount returns the number of registered listeners (for assertions).
func (f *fakeCBManager) listenerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.listeners)
}

// --- Real T3 tests start here. ---

// TestProducer_Emit_CircuitClosed_PublishesToBroker is the happy-path
// baseline: with the CB in its default CLOSED state, Emit publishes to the
// broker and returns nil.
func TestProducer_Emit_CircuitClosed_PublishesToBroker(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Default state should be CLOSED. The listener might not have fired yet
	// (only fires on transitions), so the initial value of the atomic is
	// the flagCBClosed zero — which is what we want.
	if got := p.circuitState(); got != flagCBClosed {
		t.Errorf("initial circuitState = %d; want %d (closed)", got, flagCBClosed)
	}

	event := sampleEvent()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, event); err != nil {
		t.Fatalf("Emit (CB closed) err = %v; want nil", err)
	}

	// Confirm a record actually landed on the broker.
	consumer := newConsumer(t, cluster, event.Topic())

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
	defer fetchCancel()

	fetches := consumer.PollFetches(fetchCtx)

	var gotRec *kgo.Record
	fetches.EachRecord(func(r *kgo.Record) {
		if gotRec == nil {
			gotRec = r
		}
	})

	if gotRec == nil {
		t.Fatalf("no record fetched from %s; kfake did not receive", event.Topic())
	}
}

// TestProducer_Emit_CircuitOpen_ReturnsErrCircuitOpen verifies that when the
// mirrored flag is OPEN, Emit short-circuits with ErrCircuitOpen and the
// broker sees no record. Uses the fake CB manager to drive state.
func TestProducer_Emit_CircuitOpen_ReturnsErrCircuitOpen(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Force the breaker OPEN. The listener will flip cbStateFlag.
	fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateOpen)

	if got := p.circuitState(); got != flagCBOpen {
		t.Fatalf("circuitState after OPEN transition = %d; want %d", got, flagCBOpen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = emitter.Emit(ctx, sampleEvent())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Emit err = %v; want ErrCircuitOpen", err)
	}

	// Broker must have zero records on our topic — the circuit open branch
	// MUST bypass publishDirect.
	ev := sampleEvent()
	consumer := newConsumer(t, cluster, ev.Topic())

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 1*time.Second)
	defer fetchCancel()

	fetches := consumer.PollFetches(fetchCtx)

	var count int
	fetches.EachRecord(func(_ *kgo.Record) { count++ })

	if count != 0 {
		t.Errorf("records fetched = %d; want 0 (circuit-open path must not touch broker)", count)
	}
}

// TestProducer_CBStateListener_UpdatesFlag verifies the listener observes
// transitions in all three directions and mirrors the state atomically.
func TestProducer_CBStateListener_UpdatesFlag(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Listener must have registered exactly once.
	if got := fakeMgr.listenerCount(); got != 1 {
		t.Errorf("listenerCount = %d; want 1", got)
	}

	cases := []struct {
		name string
		to   circuitbreaker.State
		want int32
	}{
		{"open", circuitbreaker.StateOpen, flagCBOpen},
		{"half-open", circuitbreaker.StateHalfOpen, flagCBHalfOpen},
		{"closed", circuitbreaker.StateClosed, flagCBClosed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeMgr.ForceTransition(p.cbServiceName, tc.to)

			if got := p.circuitState(); got != tc.want {
				t.Errorf("circuitState after %s = %d; want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestProducer_CBStateListener_IgnoresOtherServices confirms that when the
// Manager fires OnStateChange for a DIFFERENT service name, our listener
// leaves the flag untouched — tenant isolation inside a shared manager.
func TestProducer_CBStateListener_IgnoresOtherServices(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Fire a state change for an UNRELATED service. Our listener should
	// leave cbStateFlag alone.
	_, _ = fakeMgr.GetOrCreate("some.other.service", circuitbreaker.HTTPServiceConfig())
	fakeMgr.ForceTransition("some.other.service", circuitbreaker.StateOpen)

	if got := p.circuitState(); got != flagCBClosed {
		t.Errorf("circuitState after other-service OPEN = %d; want %d (closed)", got, flagCBClosed)
	}

	// And flipping OUR service still works.
	fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateOpen)
	if got := p.circuitState(); got != flagCBOpen {
		t.Errorf("circuitState after own OPEN = %d; want %d", got, flagCBOpen)
	}
}

// oneShotPanicLogger panics on the FIRST Log call, then returns normally.
// This models "the switch-case log call panics; the deferred RecoverAndLog
// then successfully logs the panic through the same logger" — which is the
// realistic single-fault scenario the listener must survive.
type oneShotPanicLogger struct {
	fired atomic.Bool
}

func (l *oneShotPanicLogger) Log(_ context.Context, _ log.Level, _ string, _ ...log.Field) {
	if l.fired.CompareAndSwap(false, true) {
		panic("oneShotPanicLogger: intentional first-call panic for test")
	}
	// Subsequent calls (like the one from RecoverAndLog) no-op.
}

func (l *oneShotPanicLogger) With(_ ...log.Field) log.Logger { return l }
func (l *oneShotPanicLogger) WithGroup(_ string) log.Logger  { return l }
func (l *oneShotPanicLogger) Enabled(_ log.Level) bool       { return true }
func (l *oneShotPanicLogger) Sync(_ context.Context) error   { return nil }

// TestProducer_CBListener_PanicSafe asserts that a logger panic inside the
// listener does NOT propagate out of OnStateChange — the runtime.RecoverAndLog
// guard must absorb it. Uses a one-shot panicking logger so the recovery's
// own log call lands successfully and the panic is contained.
func TestProducer_CBListener_PanicSafe(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	logger := &oneShotPanicLogger{}

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(logger),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Trigger the transition — the one-shot logger will panic when the
	// switch-case calls Log. RecoverAndLog must absorb it.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic leaked from listener: %v", r)
			}
		}()

		fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateOpen)
	}()

	// The atomic store happens BEFORE the panic in the switch arm, so the
	// flag must have been updated.
	if got := p.circuitState(); got != flagCBOpen {
		t.Errorf("circuitState after panicking transition = %d; want %d (atomic store runs before log call)", got, flagCBOpen)
	}

	if !logger.fired.Load() {
		t.Error("oneShotPanicLogger did not fire; test did not exercise the panic path")
	}
}

// TestProducer_CBListener_SubMillisecond wall-clocks the listener to ensure
// it completes well under the Manager's 10-second deadline (TRD R5). The
// threshold is 1ms; if this ever regresses, we've likely added I/O to the
// listener in violation of the design.
func TestProducer_CBListener_SubMillisecond(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	listener := &streamingStateListener{producer: p}

	// Measure a handful of transitions and assert the median is under 1ms.
	// A single sample can be arbitrarily slow under CI scheduling pressure.
	const (
		samples   = 10
		threshold = 1 * time.Millisecond
	)

	under := 0
	for i := 0; i < samples; i++ {
		start := time.Now()
		listener.OnStateChange(context.Background(), p.cbServiceName, circuitbreaker.StateClosed, circuitbreaker.StateOpen)
		if time.Since(start) < threshold {
			under++
		}
	}

	// Require the majority of samples to be under 1ms. If fewer than 6 are
	// sub-millisecond, someone probably added I/O to the listener.
	if under < samples/2+1 {
		t.Errorf("listener samples under %v: %d/%d — 10s deadline risk", threshold, under, samples)
	}
}

// TestProducer_PreFlight_DoesNotFeedCB asserts that emitting N events with
// ErrMissingTenantID does NOT trip the breaker — caller faults live OUTSIDE
// the circuit-breaker wrapper.
func TestProducer_PreFlight_DoesNotFeedCB(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	bad := sampleEvent()
	bad.TenantID = ""
	bad.SystemEvent = false

	// Emit far more than CBMinRequests (10) of the bad event — if pre-flight
	// fed the breaker, a ratio > 0.5 would trip it OPEN.
	for i := 0; i < 100; i++ {
		err := emitter.Emit(context.Background(), bad)
		if !errors.Is(err, ErrMissingTenantID) {
			t.Fatalf("iter %d Emit err = %v; want ErrMissingTenantID", i, err)
		}
	}

	// Breaker must still be closed. Reading from the real manager: our flag
	// is the mirror, but since the REAL breaker has not seen any Execute
	// calls, state is still CLOSED.
	if got := p.circuitState(); got != flagCBClosed {
		t.Errorf("circuitState after 100 pre-flight failures = %d; want %d (closed)", got, flagCBClosed)
	}
}

// TestProducer_WithCircuitBreakerManager_ReusesCallerManager asserts that
// a caller-supplied manager is the one wired onto the Producer — not a fresh
// default-constructed one.
func TestProducer_WithCircuitBreakerManager_ReusesCallerManager(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	callerMgr, err := circuitbreaker.NewManager(log.NewNop())
	if err != nil {
		t.Fatalf("NewManager err = %v", err)
	}

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(callerMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	if p.cbManager != callerMgr {
		t.Errorf("producer.cbManager = %p; want caller-supplied %p", p.cbManager, callerMgr)
	}

	// Breaker was registered with caller's manager under our service name.
	if got := callerMgr.GetState(p.cbServiceName); got != circuitbreaker.StateClosed {
		t.Errorf("initial state in caller mgr = %q; want %q", got, circuitbreaker.StateClosed)
	}
}

// TestProducer_WithoutCallerManager_BuildsOwnManager: when the caller does
// NOT supply a manager, New constructs one from the logger.
func TestProducer_WithoutCallerManager_BuildsOwnManager(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	if p.cbManager == nil {
		t.Fatal("producer.cbManager is nil; want self-constructed manager")
	}

	if p.cb == nil {
		t.Fatal("producer.cb is nil; GetOrCreate should have populated it")
	}

	if p.cbServiceName != cbServiceNamePrefix+p.producerID {
		t.Errorf("cbServiceName = %q; want %q", p.cbServiceName, cbServiceNamePrefix+p.producerID)
	}
}

// TestProducer_Emit_CB_RaceWithListener runs many concurrent Emit calls
// while the fake manager cycles state transitions. Asserts no data race (via
// -race) and no panic. The 100x50 setup is chosen to exceed goroutine
// scheduling granularity under normal CI; go test -race will surface any
// concurrent read/write on cbStateFlag.
func TestProducer_Emit_CB_RaceWithListener(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	const (
		emitGoroutines   = 100
		stateTransitions = 50
	)

	var wg sync.WaitGroup
	wg.Add(emitGoroutines + 1)

	stopTransitions := make(chan struct{})

	// Listener-side: rapidly cycle OPEN / CLOSED / HALF-OPEN.
	go func() {
		defer wg.Done()

		states := []circuitbreaker.State{
			circuitbreaker.StateOpen,
			circuitbreaker.StateHalfOpen,
			circuitbreaker.StateClosed,
		}

		for i := 0; i < stateTransitions; i++ {
			select {
			case <-stopTransitions:
				return
			default:
			}

			fakeMgr.ForceTransition(p.cbServiceName, states[i%len(states)])
		}
	}()

	// Producer-side: many concurrent emits. They'll see either nil (CLOSED)
	// or ErrCircuitOpen (OPEN) or nil via HALF-OPEN — all acceptable. The
	// assertion is the absence of race/panic.
	var emitCount atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for i := 0; i < emitGoroutines; i++ {
		go func() {
			defer wg.Done()

			// Each goroutine issues a handful of emits; we don't care about
			// the return value beyond "no panic".
			for j := 0; j < 5; j++ {
				_ = emitter.Emit(ctx, sampleEvent())
				emitCount.Add(1)
			}
		}()
	}

	wg.Wait()
	close(stopTransitions)

	// Sanity: some number of emits got observed. We don't pin a specific
	// expectation since timing varies.
	if got := emitCount.Load(); got == 0 {
		t.Errorf("no emits recorded; expected > 0")
	}
}

// TestProducer_BuildCBConfig_RespectsOverrides verifies that non-zero config
// values override the HTTPServiceConfig preset.
func TestProducer_BuildCBConfig_RespectsOverrides(t *testing.T) {
	t.Parallel()

	cfg := Config{
		CBFailureRatio: 0.9,
		CBMinRequests:  25,
		CBTimeout:      45 * time.Second,
	}

	cb := buildCBConfig(cfg)

	if cb.FailureRatio != 0.9 {
		t.Errorf("FailureRatio = %v; want 0.9", cb.FailureRatio)
	}

	if cb.MinRequests != 25 {
		t.Errorf("MinRequests = %d; want 25", cb.MinRequests)
	}

	if cb.Timeout != 45*time.Second {
		t.Errorf("Timeout = %v; want 45s", cb.Timeout)
	}
}

// TestProducer_BuildCBConfig_ZeroUsesPreset verifies that zero config values
// fall through to the HTTPServiceConfig preset.
func TestProducer_BuildCBConfig_ZeroUsesPreset(t *testing.T) {
	t.Parallel()

	cfg := Config{} // all zeros

	cb := buildCBConfig(cfg)
	preset := circuitbreaker.HTTPServiceConfig()

	if cb.FailureRatio != preset.FailureRatio {
		t.Errorf("FailureRatio = %v; want preset %v", cb.FailureRatio, preset.FailureRatio)
	}

	if cb.MinRequests != preset.MinRequests {
		t.Errorf("MinRequests = %d; want preset %d", cb.MinRequests, preset.MinRequests)
	}

	if cb.Timeout != preset.Timeout {
		t.Errorf("Timeout = %v; want preset %v", cb.Timeout, preset.Timeout)
	}
}

// TestIsCallerError_CircuitOpenIsNotCaller asserts ErrCircuitOpen is treated
// as an infrastructure error, NOT a caller-correctable fault. Callers cannot
// "fix" an open circuit from their side — they wait for recovery.
func TestIsCallerError_CircuitOpenIsNotCaller(t *testing.T) {
	t.Parallel()

	if IsCallerError(ErrCircuitOpen) {
		t.Errorf("IsCallerError(ErrCircuitOpen) = true; want false (infra fault)")
	}
}

// TestProducer_CircuitState_NilReceiver is the defensive nil-receiver guard
// on circuitState. Not reachable through public API, but the nil-safe
// contract is promised in its godoc.
func TestProducer_CircuitState_NilReceiver(t *testing.T) {
	t.Parallel()

	var p *Producer
	if got := p.circuitState(); got != flagCBClosed {
		t.Errorf("nil.circuitState = %d; want %d (closed)", got, flagCBClosed)
	}
}

// TestProducer_CBStateListener_UnknownStateLogsWarning drives a transition
// to StateUnknown (which shouldn't happen in practice, but the switch has a
// case for it). Confirms no panic and no flag change.
func TestProducer_CBStateListener_UnknownStateLogsWarning(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	fakeMgr := newFakeCBManager()

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Force an OPEN first so the flag has a known non-zero value.
	fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateOpen)

	if got := p.circuitState(); got != flagCBOpen {
		t.Fatalf("pre-unknown circuitState = %d; want %d", got, flagCBOpen)
	}

	// Drive to StateUnknown. Our switch has a default case that logs but
	// does not touch the flag — so the previous value (open) must persist.
	fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateUnknown)

	if got := p.circuitState(); got != flagCBOpen {
		t.Errorf("post-unknown circuitState = %d; want %d (unchanged)", got, flagCBOpen)
	}
}

// TestProducer_CBListener_NilReceiver is the defensive nil-receiver guard on
// OnStateChange. Should not panic.
func TestProducer_CBListener_NilReceiver(t *testing.T) {
	t.Parallel()

	var l *streamingStateListener

	// Should not panic. We defer a recover to fail the test on any panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil listener.OnStateChange panicked: %v", r)
		}
	}()

	l.OnStateChange(context.Background(), "svc", circuitbreaker.StateClosed, circuitbreaker.StateOpen)
}

// TestProducer_CBListener_NilProducer guards against the edge where
// someone constructs a listener with a nil producer (shouldn't happen in
// New, but defensive).
func TestProducer_CBListener_NilProducer(t *testing.T) {
	t.Parallel()

	l := &streamingStateListener{producer: nil}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("listener with nil producer panicked: %v", r)
		}
	}()

	l.OnStateChange(context.Background(), "svc", circuitbreaker.StateClosed, circuitbreaker.StateOpen)
}
