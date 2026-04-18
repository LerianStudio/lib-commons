//go:build unit

package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestMockEmitter_CaptureAndEvents verifies the simplest contract: Emit
// records the event, and Events() returns the slice in FIFO order.
func TestMockEmitter_CaptureAndEvents(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()

	events := []Event{
		{ResourceType: "transaction", EventType: "created", TenantID: "t-1"},
		{ResourceType: "account", EventType: "updated", TenantID: "t-2"},
		{ResourceType: "ledger", EventType: "closed", TenantID: "t-3"},
	}

	for _, e := range events {
		if err := m.Emit(ctx, e); err != nil {
			t.Fatalf("Emit returned unexpected error: %v", err)
		}
	}

	got := m.Events()
	if len(got) != len(events) {
		t.Fatalf("len(Events()) = %d; want %d", len(got), len(events))
	}
	for i, e := range events {
		if got[i].ResourceType != e.ResourceType || got[i].EventType != e.EventType || got[i].TenantID != e.TenantID {
			t.Errorf("Events()[%d] = %+v; want %+v", i, got[i], e)
		}
	}
}

// TestMockEmitter_DeepCopy ensures captured events are independent of the
// caller's struct — mutating the original Payload after Emit does not change
// the captured value.
func TestMockEmitter_DeepCopy(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()

	payload := json.RawMessage(`{"amount":100}`)
	e := Event{
		ResourceType: "transaction",
		EventType:    "created",
		TenantID:     "t-1",
		Payload:      payload,
	}

	if err := m.Emit(ctx, e); err != nil {
		t.Fatalf("Emit returned unexpected error: %v", err)
	}

	// Mutate the payload bytes underneath the original (simulate caller reuse).
	for i := range payload {
		payload[i] = 'X'
	}

	captured := m.Events()[0]
	if string(captured.Payload) != `{"amount":100}` {
		t.Errorf("captured payload mutated by caller: got %q; want %q", string(captured.Payload), `{"amount":100}`)
	}
}

// TestMockEmitter_SetError forces Emit to return a preset error.
func TestMockEmitter_SetError(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()

	m.SetError(ErrPayloadTooLarge)

	err := m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})
	if err != ErrPayloadTooLarge {
		t.Errorf("Emit() err = %v; want ErrPayloadTooLarge", err)
	}

	// Clearing restores the happy path.
	m.SetError(nil)
	if err := m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"}); err != nil {
		t.Errorf("Emit after SetError(nil) returned error: %v", err)
	}
}

// TestMockEmitter_Reset clears all captured events.
func TestMockEmitter_Reset(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()

	_ = m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})
	_ = m.Emit(ctx, Event{ResourceType: "account", EventType: "updated", TenantID: "t-1"})

	if len(m.Events()) != 2 {
		t.Fatalf("pre-reset len = %d; want 2", len(m.Events()))
	}

	m.Reset()

	if len(m.Events()) != 0 {
		t.Errorf("post-reset len = %d; want 0", len(m.Events()))
	}
}

// TestMockEmitter_ConcurrentEmits spawns 1000 goroutines emitting once each
// and asserts every emission is captured. Run with -race to detect any
// unguarded shared state (DX-A03, DX-C01).
func TestMockEmitter_ConcurrentEmits(t *testing.T) {
	t.Parallel()

	const n = 1000

	m := NewMockEmitter()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_ = m.Emit(ctx, Event{
				ResourceType: "transaction",
				EventType:    "created",
				TenantID:     fmt.Sprintf("t-%d", i),
			})
		}(i)
	}
	wg.Wait()

	if got := len(m.Events()); got != n {
		t.Errorf("captured %d events; want %d", got, n)
	}
}

// TestMockEmitter_CloseHealthy exercises the lifecycle surface.
func TestMockEmitter_CloseHealthy(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()

	if err := m.Healthy(ctx); err != nil {
		t.Errorf("Healthy() = %v; want nil", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close() = %v; want nil", err)
	}
	// Idempotent close.
	if err := m.Close(); err != nil {
		t.Errorf("second Close() = %v; want nil", err)
	}
}

// TestMockEmitter_NilReceiver verifies every method is nil-safe. A nil
// *MockEmitter is a common shape when a test helper forgets to initialize.
func TestMockEmitter_NilReceiver(t *testing.T) {
	t.Parallel()

	var m *MockEmitter

	if err := m.Emit(context.Background(), Event{}); err != nil {
		t.Errorf("nil.Emit = %v; want nil", err)
	}
	if got := m.Events(); got != nil {
		t.Errorf("nil.Events = %v; want nil", got)
	}
	// SetError and Reset should be no-ops (no panic).
	m.SetError(ErrNotJSON)
	m.Reset()

	if err := m.Close(); err != nil {
		t.Errorf("nil.Close = %v; want nil", err)
	}
	if err := m.Healthy(context.Background()); err != nil {
		t.Errorf("nil.Healthy = %v; want nil", err)
	}
}

// TestAssertEventEmitted_Pass passes when a matching event exists. We use the
// internal assertEventEmitted helper (takes the narrow tbSpy interface) to
// avoid threading a real *testing.T into a failure-expected branch. The
// exported helper takes testing.TB and delegates to the narrow form.
func TestAssertEventEmitted_Pass(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	_ = m.Emit(context.Background(), Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})

	spy := &tbSpy{}
	assertEventEmittedImpl(spy, m, "transaction", "created")
	if spy.Failed() {
		t.Errorf("assertEventEmittedImpl failed for matching event; spy = %+v", spy)
	}

	// Also exercise the exported wrappers with a real *testing.T — these
	// must not fail for a matching event. This drives the public-surface
	// coverage lines in mock.go.
	AssertEventEmitted(t, m, "transaction", "created")
	AssertEventCount(t, m, "transaction", "created", 1)
	AssertTenantID(t, m, "t-1")
}

// TestAssertEventEmitted_Fail calls Errorf when no matching event exists.
func TestAssertEventEmitted_Fail(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	_ = m.Emit(context.Background(), Event{ResourceType: "account", EventType: "updated", TenantID: "t-1"})

	spy := &tbSpy{}
	assertEventEmittedImpl(spy, m, "transaction", "created")
	if !spy.Failed() {
		t.Errorf("assertEventEmittedImpl did not fail for missing event")
	}
}

// TestAssertEventCount_Pass asserts the exact occurrence count.
func TestAssertEventCount_Pass(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()
	_ = m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})
	_ = m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-2"})

	spy := &tbSpy{}
	assertEventCountImpl(spy, m, "transaction", "created", 2)
	if spy.Failed() {
		t.Errorf("assertEventCountImpl failed for matching count; spy = %+v", spy)
	}
}

// TestAssertEventCount_Fail fires on mismatch.
func TestAssertEventCount_Fail(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	ctx := context.Background()
	_ = m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})

	spy := &tbSpy{}
	assertEventCountImpl(spy, m, "transaction", "created", 5)
	if !spy.Failed() {
		t.Errorf("assertEventCountImpl did not fail for mismatch (got 1, want 5)")
	}
}

// TestAssertTenantID_Pass passes when at least one event has the tenant.
func TestAssertTenantID_Pass(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	_ = m.Emit(context.Background(), Event{ResourceType: "transaction", EventType: "created", TenantID: "t-123"})

	spy := &tbSpy{}
	assertTenantIDImpl(spy, m, "t-123")
	if spy.Failed() {
		t.Errorf("assertTenantIDImpl failed for matching tenant; spy = %+v", spy)
	}
}

// TestAssertTenantID_Fail fires when no event carries the tenant.
func TestAssertTenantID_Fail(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	_ = m.Emit(context.Background(), Event{ResourceType: "transaction", EventType: "created", TenantID: "t-other"})

	spy := &tbSpy{}
	assertTenantIDImpl(spy, m, "t-123")
	if !spy.Failed() {
		t.Errorf("assertTenantIDImpl did not fail for missing tenant")
	}
}

// TestAssertNoEvents_Pass passes on a fresh mock.
func TestAssertNoEvents_Pass(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()

	spy := &tbSpy{}
	assertNoEventsImpl(spy, m)
	if spy.Failed() {
		t.Errorf("assertNoEventsImpl failed on empty mock; spy = %+v", spy)
	}

	// Also exercise the exported wrapper with a real *testing.T — drives
	// the public-surface coverage line.
	AssertNoEvents(t, m)
}

// TestAssertNoEvents_Fail fires when any event was emitted.
func TestAssertNoEvents_Fail(t *testing.T) {
	t.Parallel()

	m := NewMockEmitter()
	_ = m.Emit(context.Background(), Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})

	spy := &tbSpy{}
	assertNoEventsImpl(spy, m)
	if !spy.Failed() {
		t.Errorf("assertNoEventsImpl did not fail when events were present")
	}
}

// TestWaitForEvent_DeterministicMatch uses testing/synctest so polling timing
// is virtual. The goroutine emits after 10ms virtual; the wait timeout is 1s.
// Under synctest, the wait resolves after the emit with no real-wall-clock
// sleep (DX-C04).
func TestWaitForEvent_DeterministicMatch(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		m := NewMockEmitter()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = m.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"})
		}()

		got := WaitForEvent(t, ctx, m, func(e Event) bool {
			return e.ResourceType == "transaction" && e.EventType == "created"
		}, 1*time.Second)

		if got.TenantID != "t-1" {
			t.Errorf("WaitForEvent returned event %+v; want TenantID=t-1", got)
		}
	})
}

// TestWaitForEvent_Timeout fires Fatalf when timeout elapses without a match.
// We use waitForEventImpl (narrow interface) so we can observe the failure
// without aborting the outer test.
func TestWaitForEvent_Timeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		m := NewMockEmitter()
		ctx := context.Background()

		spy := &tbSpy{}
		// This returns zero Event on timeout in the internal helper.
		waitForEventImpl(spy, ctx, m, func(e Event) bool {
			return e.ResourceType == "nope"
		}, 50*time.Millisecond)

		if !spy.Failed() {
			t.Errorf("waitForEventImpl timeout did not record a failure on spy")
		}
	})
}

// tbSpy is the minimal interface for internal *Impl helpers. It records
// whether Errorf / Fatalf were called so the test can verify pass/fail
// behavior without bringing down the outer test.
type tbSpy struct {
	mu       sync.Mutex
	failed   bool
	messages []string
}

func (s *tbSpy) Helper()                           {}
func (s *tbSpy) Errorf(format string, args ...any) { s.record(fmt.Sprintf(format, args...)) }
func (s *tbSpy) Fatalf(format string, args ...any) { s.record(fmt.Sprintf(format, args...)) }

func (s *tbSpy) record(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = true
	s.messages = append(s.messages, msg)
}

func (s *tbSpy) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}
