package streaming

import (
	"context"
	"sync"
	"testing"
	"time"
)

// MockEmitter is a concurrency-safe, zero-dependency test double for Emitter.
// It captures every emitted Event (deep-copied) and lets tests inspect, count,
// or wait on them via the Assert* helpers and WaitForEvent.
//
// Safe for concurrent use from any number of goroutines (DX-A03). All
// internal state is guarded by an unexported mutex; captured events are
// deep-copied on Emit so post-hoc caller mutation does not change the
// captured slice.
type MockEmitter struct {
	mu     sync.Mutex
	events []Event
	err    error
	closed bool
}

// NewMockEmitter returns a fresh MockEmitter with an empty event buffer and
// no injected error.
func NewMockEmitter() *MockEmitter {
	return &MockEmitter{
		events: make([]Event, 0),
	}
}

// Emit captures a deep copy of the event. When SetError has set an error,
// that error is returned and the event is NOT captured — simulating a
// publish failure in the caller's code path.
func (m *MockEmitter) Emit(_ context.Context, event Event) error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return m.err
	}

	m.events = append(m.events, deepCopyEvent(event))

	return nil
}

// Events returns a snapshot of captured events in emission order. The
// returned slice is a deep copy — callers may mutate it without affecting
// the mock's internal state.
func (m *MockEmitter) Events() []Event {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Event, len(m.events))
	for i, e := range m.events {
		out[i] = deepCopyEvent(e)
	}

	return out
}

// SetError makes subsequent Emit calls return err without capturing the
// event. Pass nil to restore the happy path.
func (m *MockEmitter) SetError(err error) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.err = err
}

// Reset clears the captured event buffer and the injected error. Leaves
// the closed flag intact — use a fresh NewMockEmitter for full reset.
func (m *MockEmitter) Reset() {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.events = m.events[:0]
	m.err = nil
}

// Close is idempotent; always returns nil. The MockEmitter does not reject
// Emit after Close — tests that care about the post-Close contract should
// check on a real Producer.
func (m *MockEmitter) Close() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true

	return nil
}

// Healthy always returns nil — the mock is always "healthy" unless tests
// explicitly override via SetError (which affects Emit only, by design).
func (m *MockEmitter) Healthy(_ context.Context) error {
	return nil
}

// deepCopyEvent returns a fully independent copy of the Event. The Payload
// slice is copied so caller-side mutation after Emit does not change the
// captured bytes.
func deepCopyEvent(e Event) Event {
	var payload []byte
	if len(e.Payload) > 0 {
		payload = make([]byte, len(e.Payload))
		copy(payload, e.Payload)
	}

	return Event{
		TenantID:        e.TenantID,
		ResourceType:    e.ResourceType,
		EventType:       e.EventType,
		EventID:         e.EventID,
		SchemaVersion:   e.SchemaVersion,
		Timestamp:       e.Timestamp,
		Source:          e.Source,
		Subject:         e.Subject,
		DataContentType: e.DataContentType,
		DataSchema:      e.DataSchema,
		SystemEvent:     e.SystemEvent,
		Payload:         payload,
	}
}

// mockAsserter is the narrow interface the assertEventEmittedImpl,
// assertEventCountImpl, assertTenantIDImpl, assertNoEventsImpl, and
// waitForEventImpl functions require. Both *testing.T and a test-local
// spy satisfy it, so the helpers' failure behavior is directly testable.
type mockAsserter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// AssertEventEmitted fails t when no captured event has the given resource
// and event type pair. Uses testing.TB so helpers work in benchmarks and
// fuzz tests. Calls t.Helper() for clean stack traces.
func AssertEventEmitted(t testing.TB, m *MockEmitter, resourceType, eventType string) {
	t.Helper()
	assertEventEmittedImpl(t, m, resourceType, eventType)
}

// AssertEventCount fails t when the count of captured events matching the
// resource + event type pair does not equal n.
func AssertEventCount(t testing.TB, m *MockEmitter, resourceType, eventType string, n int) {
	t.Helper()
	assertEventCountImpl(t, m, resourceType, eventType, n)
}

// AssertTenantID fails t when no captured event carries the given tenant ID.
func AssertTenantID(t testing.TB, m *MockEmitter, tenantID string) {
	t.Helper()
	assertTenantIDImpl(t, m, tenantID)
}

// AssertNoEvents fails t when any event was captured.
func AssertNoEvents(t testing.TB, m *MockEmitter) {
	t.Helper()
	assertNoEventsImpl(t, m)
}

// WaitForEvent blocks until the matcher returns true on a newly-observed
// event, or timeout elapses. Calls t.Fatalf on timeout. Returns the matching
// event on success.
//
// Under testing/synctest the polling loop is fully deterministic — time
// advances only when every goroutine in the bubble is blocked.
func WaitForEvent(t testing.TB, ctx context.Context, m *MockEmitter, matcher func(Event) bool, timeout time.Duration) Event {
	t.Helper()
	return waitForEventImpl(t, ctx, m, matcher, timeout)
}

// --- Internal implementations on the narrow mockAsserter interface. ---

func assertEventEmittedImpl(t mockAsserter, m *MockEmitter, resourceType, eventType string) {
	t.Helper()

	for _, e := range m.Events() {
		if e.ResourceType == resourceType && e.EventType == eventType {
			return
		}
	}

	t.Errorf("expected event %s.%s to be emitted; none found in %d captured events", resourceType, eventType, len(m.Events()))
}

func assertEventCountImpl(t mockAsserter, m *MockEmitter, resourceType, eventType string, n int) {
	t.Helper()

	count := 0

	for _, e := range m.Events() {
		if e.ResourceType == resourceType && e.EventType == eventType {
			count++
		}
	}

	if count != n {
		t.Errorf("expected %d events of %s.%s; got %d", n, resourceType, eventType, count)
	}
}

func assertTenantIDImpl(t mockAsserter, m *MockEmitter, tenantID string) {
	t.Helper()

	for _, e := range m.Events() {
		if e.TenantID == tenantID {
			return
		}
	}

	t.Errorf("expected at least one event with TenantID=%q; none found in %d captured events", tenantID, len(m.Events()))
}

func assertNoEventsImpl(t mockAsserter, m *MockEmitter) {
	t.Helper()

	if got := len(m.Events()); got != 0 {
		t.Errorf("expected no events to be emitted; got %d", got)
	}
}

// waitForEventImpl polls m.Events() every pollInterval until the matcher
// returns true or timeout elapses. On timeout, calls Fatalf and returns
// a zero Event so the caller's assignment is well-defined.
//
// The polling-loop pattern (rather than a channel subscription) keeps the
// mock's public surface lean; the loop is fully deterministic under
// testing/synctest because time.Sleep is virtualized inside the bubble.
func waitForEventImpl(t mockAsserter, ctx context.Context, m *MockEmitter, matcher func(Event) bool, timeout time.Duration) Event {
	t.Helper()

	const pollInterval = 1 * time.Millisecond

	deadline := time.Now().Add(timeout)

	for {
		for _, e := range m.Events() {
			if matcher(e) {
				return e
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("WaitForEvent timed out after %v", timeout)
			return Event{}
		}

		// Respect context cancellation; unusual in unit tests but prevents
		// the loop from running forever under a canceled ctx.
		select {
		case <-ctx.Done():
			t.Fatalf("WaitForEvent canceled: %v", ctx.Err())
			return Event{}
		case <-time.After(pollInterval):
		}
	}
}
