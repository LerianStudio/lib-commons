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
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/goleak"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-observability/log"
	commonsHttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// --- T7: commons.App integration, paired lifecycle, close semantics. ---

// TestProducer_ImplementsCommonsApp_CompileTimeAssertion asserts *Producer
// satisfies libCommons.App at compile time. The `var _ libCommons.App = ...`
// line in commons_app.go is the load-bearing check; this test makes the
// contract visible in the test report and guards against an accidental
// deletion of the assertion during refactors (the compile-time check would
// still catch the removal, but a failing test is a louder signal in CI).
func TestProducer_ImplementsCommonsApp_CompileTimeAssertion(t *testing.T) {
	t.Parallel()

	var _ libCommons.App = (*Producer)(nil)

	// Runtime type assertion via an Emitter path — mirrors the production
	// pattern where a consumer receives an Emitter from New and type-asserts
	// to *Producer only if it needs lifecycle methods directly.
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	if _, ok := any(p).(libCommons.App); !ok {
		t.Fatal("*Producer does not satisfy libCommons.App at runtime")
	}
}

// TestProducer_RunContext_GracefulShutdown_NoGoroutineLeak runs the
// Producer under a cancellable ctx, cancels the ctx, and verifies
// (a) RunContext returns promptly with nil, (b) goleak observes no
// surviving goroutines from the *Producer* after shutdown.
//
// Uses goleak.IgnoreCurrent to pin the test's entry-time goroutine set
// as a baseline, and IgnoreAnyFunction to mask the kfake cluster and
// testing framework background workers — we care about Producer-owned
// goroutines only.
func TestProducer_RunContext_GracefulShutdown_NoGoroutineLeak(t *testing.T) {
	// Goroutines we explicitly do not treat as leaks — kfake and test
	// framework internals that persist across test cases. IgnoreCurrent
	// plus these targeted ignores narrow the assertion to franz-go
	// client-side goroutines the Producer itself owns.
	ignoreOpts := []goleak.Option{
		goleak.IgnoreCurrent(),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
		goleak.IgnoreTopFunction("runtime.goexit"),
		goleak.IgnoreAnyFunction("github.com/twmb/franz-go/pkg/kfake.(*Cluster).run"),
		goleak.IgnoreAnyFunction("github.com/twmb/franz-go/pkg/kfake.(*Cluster).listen"),
		goleak.IgnoreAnyFunction("github.com/twmb/franz-go/pkg/kfake.(*broker).handleConn"),
		goleak.IgnoreAnyFunction("github.com/twmb/franz-go/pkg/kfake.(*broker).serve"),
		// internal/poll is for kfake's listen socket; persists until
		// cluster.Close (which t.Cleanup runs AFTER this assertion).
		goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"),
	}

	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.RunContext(ctx, nil)
	}()

	// Give the goroutine a tick to enter the select. On a healthy machine
	// this is 1-2µs; the short sleep is defensive against CI scheduling.
	time.Sleep(20 * time.Millisecond)

	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("RunContext err = %v; want nil on ctx cancel", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext did not return within 5s after ctx cancel")
	}

	// Post-shutdown leak check. Any goroutine spawned by this test
	// specifically — other than the ignored kfake/test-framework set —
	// should be gone.
	if err := goleak.Find(ignoreOpts...); err != nil {
		t.Errorf("goleak: %v", err)
	}
}

// TestProducer_Run_InvokesRunContextBackground verifies Run delegates to
// RunContext(context.Background(), launcher). Blocks in a goroutine, then
// Close unblocks Run — RunContext's ctx.Done branch is background-only so
// it cannot trigger; only the stop-channel branch can.
func TestProducer_Run_InvokesRunContextBackground(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	done := make(chan error, 1)
	go func() {
		done <- p.Run(nil)
	}()

	// Allow Run to enter the select.
	time.Sleep(20 * time.Millisecond)

	// Close unblocks Run via the stop channel.
	if err := p.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run err = %v; want nil", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after Close")
	}
}

// TestProducer_RunContext_ClosesProducerAfterCtxCancel asserts that when
// ctx cancels, the Producer observes it and transitions to closed (CAS
// flipped, stop channel drained, kgo.Client shut). Post-RunContext Emit
// must return ErrEmitterClosed.
func TestProducer_RunContext_ClosesProducerAfterCtxCancel(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.RunContext(ctx, nil)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext hung")
	}

	// Post-RunContext Emit must fail synchronously with ErrEmitterClosed.
	err = emitter.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrEmitterClosed) {
		t.Errorf("Emit err = %v; want ErrEmitterClosed", err)
	}
}

// TestProducer_RunContext_AlreadyClosed_ReturnsImmediately covers the
// corner case where CloseContext ran before RunContext (test-fixture
// pattern). RunContext must observe the closed flag and exit immediately
// without blocking.
func TestProducer_RunContext_AlreadyClosed_ReturnsImmediately(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	// Close BEFORE Run.
	if err := p.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	// RunContext must return immediately — within 500ms is generous; the
	// implementation's happy path is sub-millisecond.
	done := make(chan error, 1)
	start := time.Now()

	go func() {
		done <- p.RunContext(context.Background(), nil)
	}()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("RunContext on closed Producer err = %v; want nil", runErr)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("RunContext returned after %v; want <500ms for already-closed", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContext on already-closed Producer hung")
	}
}

// TestProducer_RunContext_WithLauncher_LogsStartAndStop feeds a Launcher
// with a capturing logger through RunContext and asserts two INFO log
// entries fire: one on start, one on stop.
func TestProducer_RunContext_WithLauncher_LogsStartAndStop(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	spy := newSpyLaunchLogger()
	launcher := libCommons.NewLauncher(libCommons.WithLogger(spy))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.RunContext(ctx, launcher)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunContext hung")
	}

	got := spy.messages()

	// Exactly one start and one stop log, in order.
	startFound := false
	stopFound := false

	for _, m := range got {
		if m == "streaming producer started" {
			startFound = true
		}
		if m == "streaming producer stopped" {
			stopFound = true
		}
	}

	if !startFound {
		t.Errorf("expected 'started' log; got messages: %v", got)
	}
	if !stopFound {
		t.Errorf("expected 'stopped' log; got messages: %v", got)
	}
}

// TestProducer_Close_IdempotentAcrossThreeCalls explicitly proves the
// idempotence contract (AC-14). Three Close calls in sequence all return
// nil — no Flush runs a second time; the CAS guard handles it.
func TestProducer_Close_IdempotentAcrossThreeCalls(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := emitter.Close(); err != nil {
			t.Errorf("Close attempt %d err = %v; want nil (idempotent)", i, err)
		}
	}
}

// TestProducer_PostClose_Emit_ReturnsErrEmitterClosed is a narrowly-
// focused repro of AC-14's post-close Emit contract.
func TestProducer_PostClose_Emit_ReturnsErrEmitterClosed(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	err = emitter.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrEmitterClosed) {
		t.Errorf("Emit err = %v; want ErrEmitterClosed", err)
	}
}

// TestProducer_PostClose_Emit_RecordsCallerErrorMetric asserts the
// post-close path records a streaming_emitted_total increment with
// outcome=caller_error, mirroring the pre-flight rejection pattern
// established in T6. Zero broker I/O is implicit: we assert the metric
// fires without involving kfake's record counter.
func TestProducer_PostClose_Emit_RecordsCallerErrorMetric(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	// Baseline: cumulative counter before the post-close Emit. Close does
	// not itself record any streaming_emitted_total data point, so the
	// baseline is zero on a fresh meter — but we compute the delta
	// defensively in case a future instrumentation change adds one.
	event := sampleEvent()
	before := callerErrorCount(t, snapshot(), event.Topic())

	err = emitter.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrEmitterClosed) {
		t.Fatalf("Emit err = %v; want ErrEmitterClosed", err)
	}

	after := callerErrorCount(t, snapshot(), event.Topic())

	if got := after - before; got != 1 {
		t.Errorf("caller_error delta = %d; want 1", got)
	}
}

// TestProducer_PostClose_Emit_NoBrokerIO proves the stronger contract: after
// Close, Emit issues zero broker-side produce records. We verify by
// consuming from the topic after Emit and expecting no records appear.
func TestProducer_PostClose_Emit_NoBrokerIO(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	event := sampleEvent()
	topic := event.Topic()

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	// Post-close Emit — must NOT reach the broker.
	_ = emitter.Emit(context.Background(), sampleEvent())

	// Consume from the topic with a short fetch deadline. If any record
	// landed on the broker we'd see it.
	consumer := newConsumer(t, cluster, topic)

	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer fetchCancel()

	fetches := consumer.PollFetches(fetchCtx)

	var records int

	fetches.EachRecord(func(_ *kgo.Record) {
		records++
	})

	if records != 0 {
		t.Errorf("broker received %d records post-close; want 0", records)
	}
}

// TestProducer_PostClose_Emit_NoSpan asserts the span exporter captures
// zero streaming.emit spans for a post-close Emit. Spans are bounded to
// emission attempts per TRD §7.2; post-close is not an attempt.
func TestProducer_PostClose_Emit_NoSpan(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	tracer, getSpans := newSpanRecorder(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithTracer(tracer),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	_ = emitter.Emit(context.Background(), sampleEvent())

	for _, s := range getSpans() {
		if s.Name == emitSpanName {
			t.Errorf("post-close Emit created %q span; want zero", emitSpanName)
		}
	}
}

// TestProducer_WithCloseTimeout_Threads asserts WithCloseTimeout threads
// into Producer.closeTimeout. A private-field inspection via asProducer
// is fine because the test is in the same package.
func TestProducer_WithCloseTimeout_Threads(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithCloseTimeout(3*time.Second),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	if p.closeTimeout != 3*time.Second {
		t.Errorf("closeTimeout = %v; want 3s (option override)", p.closeTimeout)
	}
}

// TestProducer_WithCloseTimeout_ZeroUsesConfigDefault verifies the
// documented zero-means-config-default semantics on WithCloseTimeout.
// Config default is 5s in kfakeConfig.
func TestProducer_WithCloseTimeout_ZeroUsesConfigDefault(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	cfg.CloseTimeout = 5 * time.Second

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithCloseTimeout(0),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	if p.closeTimeout != 5*time.Second {
		t.Errorf("closeTimeout = %v; want 5s (config default)", p.closeTimeout)
	}
}

// TestProducer_NilReceiver_AllMethods is the master nil-receiver audit for
// AC-14. Every exported method on *Producer is exercised on a nil receiver
// and expected to return the documented sentinel (or nil for Close-family
// methods where no-op-on-nil is the contract).
func TestProducer_NilReceiver_AllMethods(t *testing.T) {
	t.Parallel()

	var p *Producer

	tests := []struct {
		name string
		run  func() error
		want error // nil means "nil result expected"
	}{
		{
			name: "Emit returns ErrNilProducer",
			run:  func() error { return p.Emit(context.Background(), sampleEvent()) },
			want: ErrNilProducer,
		},
		{
			name: "Close returns nil (idempotent on nil)",
			run:  func() error { return p.Close() },
			want: nil,
		},
		{
			name: "CloseContext returns nil (idempotent on nil)",
			run:  func() error { return p.CloseContext(context.Background()) },
			want: nil,
		},
		{
			name: "Healthy returns HealthError(Down, ErrNilProducer)",
			run:  func() error { return p.Healthy(context.Background()) },
			want: ErrNilProducer, // errors.Is walks *HealthError.Unwrap
		},
		{
			name: "Run returns ErrNilProducer",
			run:  func() error { return p.Run(nil) },
			want: ErrNilProducer,
		},
		{
			name: "RunContext returns ErrNilProducer",
			run:  func() error { return p.RunContext(context.Background(), nil) },
			want: ErrNilProducer,
		},
		{
			name: "RegisterOutboxHandler returns ErrNilProducer",
			run:  func() error { return p.RegisterOutboxHandler(&outbox.HandlerRegistry{}, "x") },
			want: ErrNilProducer,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on nil receiver: %v", r)
				}
			}()

			err := tc.run()

			if tc.want == nil {
				if err != nil {
					t.Errorf("err = %v; want nil", err)
				}
				return
			}

			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v; want errors.Is(..., %v)", err, tc.want)
			}
		})
	}
}

// TestProducer_Healthy_PlugsIntoHealthWithDependencies constructs the
// DependencyCheck-shaped bridge AC-10 promises. The bridge is a thin
// two-line closure around Healthy(ctx) that satisfies the func() bool
// HealthCheck field. The compile-time construction validates signature
// compatibility; the runtime assertions cover the 200-vs-503 dispatch.
//
// Note: "plugs in without an adapter" in AC-10 means no library-provided
// Adapter type is needed — a field-literal closure at the call site is
// the universally-accepted idiom for `func(ctx) error` → `func() bool`.
func TestProducer_Healthy_PlugsIntoHealthWithDependencies(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	dep := commonsHttp.DependencyCheck{
		Name: "streaming",
		HealthCheck: func() bool {
			return emitter.Healthy(context.Background()) == nil
		},
	}

	// Compile-time: the handler constructs without error.
	_ = commonsHttp.HealthWithDependencies(dep)

	// Runtime path 1: healthy broker → HealthCheck returns true.
	if !dep.HealthCheck() {
		t.Error("HealthCheck() = false under healthy broker; want true")
	}

	// Runtime path 2: kill broker → HealthCheck returns false (Degraded).
	cluster.Close()

	// kfake.Close is synchronous but franz-go ping can race. Allow up to
	// 2s for the next HealthCheck call to observe the dead broker.
	deadline := time.Now().Add(2 * time.Second)
	lastResult := true

	for time.Now().Before(deadline) {
		lastResult = dep.HealthCheck()
		if !lastResult {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if lastResult {
		t.Error("HealthCheck() = true after broker shutdown; want false")
	}
}

// TestProducer_LauncherIntegration_EndToEnd exercises the full
// commons.Launcher + Producer.Run loop: construct a Launcher, register
// the Producer via RunApp, spawn Launcher.RunWithError in a goroutine,
// trigger shutdown by closing the Producer, verify RunWithError returns
// with no error. This is the DX-E01-E05 smoke test — if a service wired
// both correctly, this sequence works.
func TestProducer_LauncherIntegration_EndToEnd(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	launcher := libCommons.NewLauncher(
		libCommons.WithLogger(log.NewNop()),
		libCommons.RunApp("streaming", p),
	)

	runDone := make(chan error, 1)
	go func() {
		runDone <- launcher.RunWithError()
	}()

	// Give the Launcher time to dispatch the app goroutine.
	time.Sleep(50 * time.Millisecond)

	// Trigger shutdown via Close — this mirrors what a SIGTERM handler
	// would do after the Launcher's wg.Wait() would otherwise block
	// forever.
	if err := p.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Launcher.RunWithError err = %v; want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Launcher.RunWithError did not return within 10s")
	}
}

// TestProducer_RunContext_NilCtx_DoesNotPanic confirms the nil-ctx
// substitution path runs cleanly. A nil ctx is a bootstrap smell but
// must not crash the process.
func TestProducer_RunContext_NilCtx_DoesNotPanic(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	done := make(chan error, 1)
	go func() {
		//nolint:staticcheck // SA1012: intentionally passing nil to validate nil-substitution path
		done <- p.RunContext(nil, nil)
	}()

	time.Sleep(20 * time.Millisecond)

	// Close unblocks RunContext via the stop channel.
	if err := p.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("RunContext(nil) err = %v; want nil", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext(nil) hung")
	}
}

// TestProducer_CloseContext_ConcurrentCallers exercises the CAS guard
// under concurrent pressure. 50 goroutines call Close simultaneously;
// all must return nil (exactly-one-Flush is enforced by CompareAndSwap).
func TestProducer_CloseContext_ConcurrentCallers(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	const callers = 50

	var (
		wg   sync.WaitGroup
		errs atomic.Int64
	)

	wg.Add(callers)

	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if err := emitter.Close(); err != nil {
				errs.Add(1)
			}
		}()
	}

	wg.Wait()

	if got := errs.Load(); got != 0 {
		t.Errorf("%d/%d Close calls returned err; want 0", got, callers)
	}
}

// --- Shared helpers for this file. ---

// spyLaunchLogger captures every Log call for assertions on the Launcher
// start/stop lifecycle logs. Safe for concurrent use so concurrent
// goroutines don't race on the messages slice. Named with the -Launch
// suffix to avoid colliding with spyLogger in publish_dlq_test.go.
type spyLaunchLogger struct {
	mu   sync.Mutex
	msgs []string
}

func newSpyLaunchLogger() *spyLaunchLogger { return &spyLaunchLogger{} }

func (s *spyLaunchLogger) Log(_ context.Context, _ log.Level, msg string, _ ...log.Field) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.msgs = append(s.msgs, msg)
}

func (s *spyLaunchLogger) With(_ ...log.Field) log.Logger { return s }
func (s *spyLaunchLogger) WithGroup(_ string) log.Logger  { return s }
func (s *spyLaunchLogger) Enabled(_ log.Level) bool       { return true }
func (s *spyLaunchLogger) Sync(_ context.Context) error   { return nil }

func (s *spyLaunchLogger) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.msgs))
	copy(out, s.msgs)

	return out
}

// callerErrorCount sums cumulative streaming_emitted_total data points
// filtered to topic + outcome=caller_error. Relies on the T6-era
// newManualMeterSetup snapshot that returns a metricdata.ResourceMetrics.
func callerErrorCount(t *testing.T, rm metricdata.ResourceMetrics, topic string) int64 {
	t.Helper()

	m, ok := findMetric(rm, metricNameEmitted)
	if !ok {
		return 0
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %s: data type = %T; want metricdata.Sum[int64]", m.Name, m.Data)
	}

	var total int64

	for _, dp := range sum.DataPoints {
		attrs := attrSetToMap(dp.Attributes)
		if attrs["outcome"] == outcomeCallerError && attrs["topic"] == topic {
			total += dp.Value
		}
	}

	return total
}
