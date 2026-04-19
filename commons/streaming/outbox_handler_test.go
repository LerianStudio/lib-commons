//go:build unit

package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// --- Outbox handler registration + relay tests. ---

// TestProducer_RegisterOutboxHandler_RelaysViaPublishDirect exercises the
// loop-prevention invariant at the heart of T4: the handler registered
// with the outbox Dispatcher must call publishDirect, NOT Emit. A
// successful handler call produces a record on the broker (proving
// publishDirect ran) and the CB stays in its current state (proving Emit
// was not re-entered through the fallback path).
func TestProducer_RegisterOutboxHandler_RelaysViaPublishDirect(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	registry := outbox.NewHandlerRegistry()

	// Register the streaming handler for the topic emitted by
	// sampleEvent(). The outbox HandlerRegistry is exact-match; we pass
	// the concrete topic name here.
	topic := "lerian.streaming.transaction.created"
	if err := p.RegisterOutboxHandler(registry, topic); err != nil {
		t.Fatalf("RegisterOutboxHandler err = %v", err)
	}

	// Build a synthetic OutboxEvent whose Payload is the JSON form of a
	// sampleEvent() — this is what publishToOutbox would have written.
	event := sampleEvent()

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal(event) err = %v", err)
	}

	row := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   topic,
		AggregateID: uuid.New(),
		Payload:     payload,
		Status:      outbox.OutboxStatusPending,
	}

	// Drive the handler through the registry. This is what the Dispatcher
	// would do at runtime.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := registry.Handle(ctx, row); err != nil {
		t.Fatalf("registry.Handle err = %v", err)
	}

	// publishDirect ran, so the broker should have the record.
	consumer := newConsumer(t, cluster, topic)

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
		t.Fatalf("no record on broker after handler run; relay path did not reach publishDirect")
	}

	if string(gotRec.Value) != string(event.Payload) {
		t.Errorf("relayed payload = %q; want %q", gotRec.Value, event.Payload)
	}

	// Circuit state must still be CLOSED — the handler path must not run
	// through Emit (which would traverse the CB wrapper). If the handler
	// had wrongly called Emit, and if CB state had been OPEN, we'd see a
	// silent re-enqueue. Here with CLOSED, the cheapest tell is the
	// listener-count & flag: still untouched by the handler path.
	if got := p.circuitState(); got != flagCBClosed {
		t.Errorf("circuitState after handler run = %d; want %d (closed)", got, flagCBClosed)
	}
}

// TestProducer_OutboxHandler_PublishDirectFailure_SurfacesError asserts
// that when publishDirect fails (broker down, bad topic, etc.), the
// handler returns a non-nil wrapped error. The outbox Dispatcher needs
// this signal to mark the row FAILED and schedule a retry with backoff.
// Swallowing the error here would leak events into the void.
func TestProducer_OutboxHandler_PublishDirectFailure_SurfacesError(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	// Close the producer BEFORE running the handler. publishDirect checks
	// the closed flag and returns ErrEmitterClosed — a deterministic
	// failure that doesn't require sabotaging the broker.
	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	event := sampleEvent()

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal err = %v", err)
	}

	row := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "lerian.streaming.transaction.created",
		AggregateID: uuid.New(),
		Payload:     payload,
	}

	err = p.handleOutboxRow(context.Background(), row)
	if err == nil {
		t.Fatal("handleOutboxRow err = nil; want non-nil (closed producer must surface)")
	}

	if !errors.Is(err, ErrEmitterClosed) {
		t.Errorf("handleOutboxRow err = %v; want errors.Is ErrEmitterClosed", err)
	}

	// Row ID should appear in the wrap context so operators can correlate.
	if !strings.Contains(err.Error(), row.ID.String()) {
		t.Errorf("handleOutboxRow err = %q; want substring %q (row ID for correlation)", err.Error(), row.ID.String())
	}
}

// TestProducer_OutboxHandler_InvalidPayload_ReturnsError: a row whose
// Payload is not valid JSON must surface an error (not panic). The
// Dispatcher will mark it FAILED; operators can then MarkInvalid it from
// the DB so future cycles skip it.
func TestProducer_OutboxHandler_InvalidPayload_ReturnsError(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	row := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "lerian.streaming.transaction.created",
		AggregateID: uuid.New(),
		Payload:     []byte("{not-json"),
	}

	err = p.handleOutboxRow(context.Background(), row)
	if err == nil {
		t.Fatal("handleOutboxRow err = nil; want error for malformed payload")
	}

	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("err = %q; want substring %q", err.Error(), "unmarshal")
	}
}

// TestProducer_OutboxHandler_NonStreamingEventType_NoOp: the handler's
// prefix guard returns nil (not an error) for rows whose EventType is
// NOT a streaming event. This is the defensive branch that protects
// against bootstrap misconfiguration: if someone registers our handler
// for "payment.created" by mistake, we must NOT publish that payload to
// a streaming topic and we must NOT mark the row FAILED (which would
// discard or retry someone else's event).
func TestProducer_OutboxHandler_NonStreamingEventType_NoOp(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	row := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created", // NOT our prefix
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
	}

	err = p.handleOutboxRow(context.Background(), row)
	if err != nil {
		t.Errorf("handleOutboxRow(non-streaming row) err = %v; want nil (silent skip)", err)
	}
}

// TestProducer_RegisterOutboxHandler_MultipleEventTypes: the variadic
// form registers the same handler against every listed event type. This
// is the expected usage pattern for a service emitting N distinct
// streaming events.
func TestProducer_RegisterOutboxHandler_MultipleEventTypes(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	registry := outbox.NewHandlerRegistry()

	types := []string{
		"lerian.streaming.transaction.created",
		"lerian.streaming.account.updated",
		"lerian.streaming.ledger.posted",
	}

	if err := p.RegisterOutboxHandler(registry, types...); err != nil {
		t.Fatalf("RegisterOutboxHandler err = %v", err)
	}

	// Sanity: every registered type routes to our handler. Invoke each
	// with a bare non-JSON payload so the handler returns an error
	// (unmarshal failure) — that error's presence proves we reached the
	// handler at all.
	for _, et := range types {
		row := &outbox.OutboxEvent{
			ID:          uuid.New(),
			EventType:   et,
			AggregateID: uuid.New(),
			Payload:     []byte("{bad-json"),
		}

		if err := registry.Handle(context.Background(), row); err == nil {
			t.Errorf("registry.Handle(%q) err = nil; want unmarshal error (handler not reached)", et)
		}
	}
}

// TestProducer_RegisterOutboxHandler_ZeroEventTypes is permitted but a
// no-op. It returns nil without registering anything — the typical
// indicator of a caller wiring mistake, but not ours to enforce.
func TestProducer_RegisterOutboxHandler_ZeroEventTypes(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	registry := outbox.NewHandlerRegistry()

	if err := p.RegisterOutboxHandler(registry); err != nil {
		t.Errorf("RegisterOutboxHandler() with no event types err = %v; want nil (no-op)", err)
	}
}

// TestProducer_RegisterOutboxHandler_DuplicateSurfacesError: the
// HandlerRegistry rejects duplicate registrations. Our wrapper must
// surface that error (not silently succeed) so a bootstrap bug shows up
// at startup rather than when the conflict first matters.
func TestProducer_RegisterOutboxHandler_DuplicateSurfacesError(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	registry := outbox.NewHandlerRegistry()

	const topic = "lerian.streaming.transaction.created"

	if err := p.RegisterOutboxHandler(registry, topic); err != nil {
		t.Fatalf("first RegisterOutboxHandler err = %v", err)
	}

	err = p.RegisterOutboxHandler(registry, topic)
	if err == nil {
		t.Fatal("second RegisterOutboxHandler err = nil; want duplicate-registration error")
	}

	if !errors.Is(err, outbox.ErrHandlerAlreadyRegistered) {
		t.Errorf("err = %v; want errors.Is outbox.ErrHandlerAlreadyRegistered", err)
	}
}

// TestProducer_RegisterOutboxHandler_NilReceiver and
// TestProducer_RegisterOutboxHandler_NilRegistry cover the two nil guards
// on the registration path.
func TestProducer_RegisterOutboxHandler_NilReceiver(t *testing.T) {
	t.Parallel()

	var p *Producer

	err := p.RegisterOutboxHandler(outbox.NewHandlerRegistry(), "lerian.streaming.x")
	if !errors.Is(err, ErrNilProducer) {
		t.Errorf("nil.RegisterOutboxHandler err = %v; want ErrNilProducer", err)
	}
}

func TestProducer_RegisterOutboxHandler_NilRegistry(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	err = p.RegisterOutboxHandler(nil, "lerian.streaming.x")
	if !errors.Is(err, ErrNilOutboxRegistry) {
		t.Errorf("RegisterOutboxHandler(nil registry) err = %v; want ErrNilOutboxRegistry", err)
	}
}

// TestProducer_HandleOutboxRow_NilReceiver: defensive guard. Unreachable
// in practice but promised in the godoc contract.
func TestProducer_HandleOutboxRow_NilReceiver(t *testing.T) {
	t.Parallel()

	var p *Producer
	err := p.handleOutboxRow(context.Background(), &outbox.OutboxEvent{})
	if !errors.Is(err, ErrNilProducer) {
		t.Errorf("nil.handleOutboxRow err = %v; want ErrNilProducer", err)
	}
}

// TestProducer_HandleOutboxRow_NilRow: nil *OutboxEvent must surface the
// outbox sentinel, not panic.
func TestProducer_HandleOutboxRow_NilRow(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	err = p.handleOutboxRow(context.Background(), nil)
	if !errors.Is(err, outbox.ErrOutboxEventRequired) {
		t.Errorf("handleOutboxRow(nil row) err = %v; want outbox.ErrOutboxEventRequired", err)
	}
}

// TestProducer_EndToEnd_OutboxFallbackAndRelay wires the full T4 happy
// path as callers would experience it:
//
//  1. Emit with the circuit OPEN + outbox wired → row lands in fake repo.
//  2. Register the streaming handler against the outbox registry.
//  3. Manually invoke the handler with the captured row (simulating what
//     the Dispatcher does after the broker recovers).
//  4. Assert the broker now has the record — proving the end-to-end
//     flow works without a loop back into the outbox.
func TestProducer_EndToEnd_OutboxFallbackAndRelay(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	fakeMgr := newFakeCBManager()
	fakeRepo := &fakeOutboxRepo{}

	emitter, err := New(
		context.Background(),
		cfg,
		WithLogger(log.NewNop()),
		WithCircuitBreakerManager(fakeMgr),
		WithOutboxRepository(fakeRepo),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Phase 1: CB OPEN, Emit routes to outbox.
	fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateOpen)

	event := sampleEvent()
	event.Subject = "end-to-end-subject"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, event); err != nil {
		t.Fatalf("Emit (CB open) err = %v", err)
	}

	row := fakeRepo.firstCreated()
	if row == nil {
		t.Fatal("outbox row was not created")
	}

	// Phase 2: Broker recovers (CB back to CLOSED). Register handler and
	// invoke it with the captured row.
	fakeMgr.ForceTransition(p.cbServiceName, circuitbreaker.StateClosed)

	registry := outbox.NewHandlerRegistry()
	if err := p.RegisterOutboxHandler(registry, row.EventType); err != nil {
		t.Fatalf("RegisterOutboxHandler err = %v", err)
	}

	if err := registry.Handle(ctx, row); err != nil {
		t.Fatalf("registry.Handle err = %v", err)
	}

	// Phase 3: The record is now on the broker.
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
		t.Fatal("no record on broker after replay; end-to-end flow broken")
	}

	// Payload round-trip: broker record's value must equal the original
	// event.Payload. This also proves the JSON marshal+unmarshal path
	// through the outbox preserved the bytes exactly.
	if string(gotRec.Value) != string(event.Payload) {
		t.Errorf("relayed record.Value = %q; want %q", gotRec.Value, event.Payload)
	}
}
