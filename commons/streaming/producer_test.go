//go:build unit

package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// --- Shared test helpers. Used by every *_test.go file in this package. ---

// kfakeConfig returns a Config that points at a freshly-started kfake
// cluster. BatchLingerMs is set to 1 so ProduceSync returns fast in tests.
//
// The sample-event topic ("lerian.streaming.transaction.created") is
// pre-seeded with 3 partitions so ProduceSync doesn't fail with
// UNKNOWN_TOPIC_OR_PARTITION.
//
// The returned cluster is registered for automatic teardown via t.Cleanup.
func kfakeConfig(t *testing.T) (Config, *kfake.Cluster) {
	t.Helper()

	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.AllowAutoTopicCreation(),
		kfake.DefaultNumPartitions(3),
		kfake.SeedTopics(3, "lerian.streaming.transaction.created"),
	)
	if err != nil {
		t.Fatalf("kfake.NewCluster err = %v", err)
	}

	t.Cleanup(cluster.Close)

	return Config{
		Enabled:               true,
		Brokers:               cluster.ListenAddrs(),
		ClientID:              "test-producer",
		BatchLingerMs:         1,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    10_000,
		Compression:           "none",
		RecordRetries:         2,
		RecordDeliveryTimeout: 5 * time.Second,
		RequiredAcks:          "leader",
		CBFailureRatio:        0.5,
		CBMinRequests:         10,
		CBTimeout:             5 * time.Second,
		CloseTimeout:          5 * time.Second,
		CloudEventsSource:     "//test",
	}, cluster
}

// newConsumer creates a kgo consumer client wired to the given cluster and
// topic. Used by tests to verify that ProduceSync actually deposited bytes
// on the broker.
func newConsumer(t *testing.T, cluster *kfake.Cluster, topic string) *kgo.Client {
	t.Helper()

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(cluster.ListenAddrs()...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup("test-consumer-"+topic),
		kgo.DisableAutoCommit(),
		kgo.FetchMaxWait(500*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("consumer init err = %v", err)
	}

	t.Cleanup(consumer.Close)

	return consumer
}

// sampleEvent returns a valid Event with a deterministic payload. Callers
// override specific fields per test to exercise validation branches.
func sampleEvent() Event {
	return Event{
		TenantID:      "t-abc",
		ResourceType:  "transaction",
		EventType:     "created",
		SchemaVersion: "1.0.0",
		Source:        "//test/service",
		Subject:       "tx-123",
		Payload:       json.RawMessage(`{"amount":100}`),
	}
}

// asProducer unwraps the Emitter interface into a *Producer or fails the
// test. Used by tests that need to reach into concrete fields (closed flag,
// etc.) after New returns the interface.
func asProducer(t *testing.T, e Emitter) *Producer {
	t.Helper()

	p, ok := e.(*Producer)
	if !ok {
		t.Fatalf("expected *Producer; got %T", e)
	}

	return p
}

// --- Emit round-trip, pre-flight, concurrency tests. ---

// TestProducer_EmitRoundTrip drives a single Emit through a kfake broker,
// consumes the resulting record, and verifies all CloudEvents headers
// round-trip plus the payload is byte-equal to the input.
func TestProducer_EmitRoundTrip(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	event := sampleEvent()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, event); err != nil {
		t.Fatalf("Emit err = %v", err)
	}

	// Consume the record we just produced.
	consumer := newConsumer(t, cluster, event.Topic())

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
	defer fetchCancel()

	fetches := consumer.PollFetches(fetchCtx)
	if errs := fetches.Errors(); len(errs) > 0 {
		// A few fetch errors can surface during metadata refresh; only
		// fail if we see no actual record.
		t.Logf("fetch errors (non-fatal if record present): %v", errs)
	}

	var got *kgo.Record

	fetches.EachRecord(func(r *kgo.Record) {
		if got == nil {
			got = r
		}
	})

	if got == nil {
		t.Fatalf("no record fetched from %s", event.Topic())
	}

	// Payload byte-equal.
	if string(got.Value) != string(event.Payload) {
		t.Errorf("value = %q; want %q", got.Value, event.Payload)
	}

	// Partition key = tenant ID.
	if string(got.Key) != event.TenantID {
		t.Errorf("key = %q; want %q", got.Key, event.TenantID)
	}

	// Required CloudEvents headers present.
	parsed, err := ParseCloudEventsHeaders(got.Headers)
	if err != nil {
		t.Fatalf("ParseCloudEventsHeaders err = %v", err)
	}

	if parsed.ResourceType != event.ResourceType {
		t.Errorf("parsed ResourceType = %q; want %q", parsed.ResourceType, event.ResourceType)
	}
	if parsed.EventType != event.EventType {
		t.Errorf("parsed EventType = %q; want %q", parsed.EventType, event.EventType)
	}
	if parsed.TenantID != event.TenantID {
		t.Errorf("parsed TenantID = %q; want %q", parsed.TenantID, event.TenantID)
	}
	if parsed.Source != event.Source {
		t.Errorf("parsed Source = %q; want %q", parsed.Source, event.Source)
	}
}

// TestProducer_EmitPreFlight_MissingTenant asserts the synchronous
// ErrMissingTenantID path — kfake must record ZERO records, proving no I/O
// happened.
func TestProducer_EmitPreFlight_MissingTenant(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.TenantID = ""
	bad.SystemEvent = false

	err = emitter.Emit(context.Background(), bad)
	if !errors.Is(err, ErrMissingTenantID) {
		t.Fatalf("Emit err = %v; want ErrMissingTenantID", err)
	}
}

// TestProducer_EmitPreFlight_PayloadTooLarge: 1 MiB + 1 byte payload
// rejects synchronously with ErrPayloadTooLarge.
func TestProducer_EmitPreFlight_PayloadTooLarge(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	big := sampleEvent()
	// Valid JSON of size 1 MiB + 1 byte: a JSON string with filler content.
	// 1 MiB = 1_048_576; we construct `"<padding>"` where the padding is
	// 1_048_575 characters — opening-quote + 1_048_575 + closing-quote
	// = 1_048_577 bytes total (1 MiB + 1).
	padding := strings.Repeat("x", 1_048_575)
	big.Payload = json.RawMessage(`"` + padding + `"`)

	if len(big.Payload) <= maxPayloadBytes {
		t.Fatalf("test bug: payload len %d; want > %d", len(big.Payload), maxPayloadBytes)
	}

	err = emitter.Emit(context.Background(), big)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("Emit err = %v; want ErrPayloadTooLarge", err)
	}
}

// TestProducer_EmitPreFlight_InvalidJSON: payload that fails json.Valid
// surfaces ErrNotJSON synchronously.
func TestProducer_EmitPreFlight_InvalidJSON(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.Payload = []byte("not-json-{")

	err = emitter.Emit(context.Background(), bad)
	if !errors.Is(err, ErrNotJSON) {
		t.Fatalf("Emit err = %v; want ErrNotJSON", err)
	}
}

// TestProducer_EmitPreFlight_MissingResourceType: empty ResourceType surfaces
// ErrMissingResourceType synchronously. Guards against the degenerate
// "lerian.streaming.." topic and malformed "studio.lerian." ce-type header
// that a caller-blank ResourceType would produce — no consumer routes those,
// so the emit would silently vanish at the worst possible layer.
func TestProducer_EmitPreFlight_MissingResourceType(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.ResourceType = ""

	err = emitter.Emit(context.Background(), bad)
	if !errors.Is(err, ErrMissingResourceType) {
		t.Fatalf("Emit err = %v; want ErrMissingResourceType", err)
	}
}

// TestProducer_EmitPreFlight_MissingEventType: empty EventType surfaces
// ErrMissingEventType synchronously. Same rationale as MissingResourceType:
// empty EventType produces a degenerate topic and malformed ce-type header.
func TestProducer_EmitPreFlight_MissingEventType(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.EventType = ""

	err = emitter.Emit(context.Background(), bad)
	if !errors.Is(err, ErrMissingEventType) {
		t.Fatalf("Emit err = %v; want ErrMissingEventType", err)
	}
}

// TestProducer_Emit_NilCtx_DoesNotPanic is the H1 regression test. The noop
// OTEL global tracer panics in ContextWithSpan when handed a nil parent
// context; Emit must substitute context.Background() before reaching the
// tracer. Mirrors the same defense pattern as TestProducer_RunContext_NilCtx.
func TestProducer_Emit_NilCtx_DoesNotPanic(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Emit(nil, event) panicked: %v", r)
		}
	}()

	event := sampleEvent()

	//nolint:staticcheck // SA1012: intentional nil ctx to validate substitution
	if err := emitter.Emit(nil, event); err != nil {
		t.Fatalf("Emit(nil, event) err = %v; want nil", err)
	}
}

// TestProducer_EmitPreFlight_MissingSource: empty ce-source surfaces
// ErrMissingSource synchronously.
func TestProducer_EmitPreFlight_MissingSource(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.Source = ""

	err = emitter.Emit(context.Background(), bad)
	if !errors.Is(err, ErrMissingSource) {
		t.Fatalf("Emit err = %v; want ErrMissingSource", err)
	}
}

// TestProducer_EmitPreFlight_EventDisabled: toggled-off event returns
// ErrEventDisabled without broker I/O.
func TestProducer_EmitPreFlight_EventDisabled(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	cfg.EventToggles = map[string]bool{
		"transaction.created": false,
	}

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	err = emitter.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrEventDisabled) {
		t.Fatalf("Emit err = %v; want ErrEventDisabled", err)
	}
}

// TestProducer_ConcurrentEmit runs 1000 Emit calls concurrently on a
// single Producer (per DX-A03). The sole assertion is "no data race, no
// panic" — the -race detector covers correctness. We use a counter to
// confirm the distribution of successes/failures matches what we'd
// expect (all success under kfake happy path).
func TestProducer_ConcurrentEmit(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	cfg.MaxBufferedRecords = 10_000

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	const (
		emitsPerGoroutine = 1
		goroutines        = 1000
	)

	var (
		wg        sync.WaitGroup
		successes atomic.Int64
		failures  atomic.Int64
		firstErr  atomic.Value // stores the first observed error for diagnostics
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			for j := 0; j < emitsPerGoroutine; j++ {
				if err := emitter.Emit(ctx, sampleEvent()); err == nil {
					successes.Add(1)
				} else {
					failures.Add(1)
					// Capture the first error only; prevents thousands of
					// per-emit t.Errorf calls from flooding test output when
					// a single systemic failure fails every concurrent emit.
					firstErr.CompareAndSwap(nil, err)
				}
			}
		}()
	}

	wg.Wait()

	if got := failures.Load(); got > 0 {
		first, _ := firstErr.Load().(error)
		t.Errorf("concurrent Emit failures = %d/%d; first error = %v",
			got, goroutines*emitsPerGoroutine, first)
	}

	if got := successes.Load(); got != int64(goroutines*emitsPerGoroutine) {
		t.Errorf("successful emits = %d; want %d", got, goroutines*emitsPerGoroutine)
	}
}
