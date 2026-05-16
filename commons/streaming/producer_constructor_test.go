//go:build unit

package streaming

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/LerianStudio/lib-observability/log"
)

// TestProducer_New_DisabledReturnsNoop proves the fail-safe: cfg.Enabled=false
// returns a NoopEmitter without attempting to connect. We check via type
// identity.
func TestProducer_New_DisabledReturnsNoop(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Enabled:           false,
		Brokers:           []string{"broker:9092"},
		Compression:       "lz4",
		RequiredAcks:      "all",
		CloudEventsSource: "//test",
	}

	emitter, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New(disabled) err = %v", err)
	}

	if _, ok := emitter.(*NoopEmitter); !ok {
		t.Errorf("New(disabled) returned %T; want *NoopEmitter", emitter)
	}
}

// TestProducer_New_NoBrokersReturnsNoop proves the complementary branch:
// empty broker slice returns Noop even if Enabled=true. This is the
// operator-forgot-to-configure case; the service boots silently instead of
// crashing.
func TestProducer_New_NoBrokersReturnsNoop(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Enabled:           true,
		Brokers:           []string{},
		Compression:       "lz4",
		RequiredAcks:      "all",
		CloudEventsSource: "//test",
	}

	emitter, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New(empty brokers) err = %v", err)
	}

	if _, ok := emitter.(*NoopEmitter); !ok {
		t.Errorf("New(empty brokers) returned %T; want *NoopEmitter", emitter)
	}
}

// TestProducer_NewProducer_FailsOnInvalidConfig: NewProducer does NOT fall
// back to Noop. Invalid config surfaces as a returned error.
func TestProducer_NewProducer_FailsOnInvalidConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Enabled:           true,
		Brokers:           []string{},
		Compression:       "lz4",
		RequiredAcks:      "all",
		CloudEventsSource: "//test",
	}

	_, err := NewProducer(context.Background(), cfg)
	if !errors.Is(err, ErrMissingBrokers) {
		t.Fatalf("NewProducer err = %v; want ErrMissingBrokers", err)
	}
}

// TestProducer_NilReceiver_Emit_ReturnsErr: calling Emit on a nil *Producer
// returns ErrNilProducer instead of panicking. Mirrors the
// nil-receiver-safe contract in circuitbreaker.
func TestProducer_NilReceiver_Emit_ReturnsErr(t *testing.T) {
	t.Parallel()

	var p *Producer

	err := p.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrNilProducer) {
		t.Errorf("nil.Emit err = %v; want ErrNilProducer", err)
	}
}

// TestProducer_NilReceiver_CloseAndHealthy: Close and Healthy also must not
// panic on nil receiver.
func TestProducer_NilReceiver_CloseAndHealthy(t *testing.T) {
	t.Parallel()

	var p *Producer

	if err := p.Close(); err != nil {
		t.Errorf("nil.Close err = %v; want nil", err)
	}

	err := p.Healthy(context.Background())
	if err == nil {
		t.Error("nil.Healthy err = nil; want *HealthError")
	}
}

// TestProducer_CredentialSanitization asserts that when the underlying
// client surfaces an error containing credentialed URLs, the EmitError's
// Error() string does NOT contain the raw credential. DX-B06.
//
// Rather than triggering a real SASL failure (kfake doesn't expose that
// directly without extra setup), we directly test the sanitization path via
// an EmitError synthesized with a credentialed cause — this is the layer
// that callers actually see.
func TestProducer_CredentialSanitization(t *testing.T) {
	t.Parallel()

	cause := errors.New("dial sasl://user:hunter2@broker:9092: connection refused")
	ee := &EmitError{
		ResourceType: "transaction",
		EventType:    "created",
		TenantID:     "t-abc",
		Topic:        "lerian.streaming.transaction.created",
		Class:        ClassBrokerUnavailable,
		Cause:        cause,
	}

	msg := ee.Error()

	if strings.Contains(msg, "hunter2") {
		t.Errorf("Error() contained credential; got %q", msg)
	}

	if strings.Contains(msg, "user:hunter2@") {
		t.Errorf("Error() contained raw userinfo; got %q", msg)
	}

	// Sanity: the error class + topic must still surface so operators can
	// correlate.
	if !strings.Contains(msg, string(ClassBrokerUnavailable)) {
		t.Errorf("Error() missing class; got %q", msg)
	}

	if !strings.Contains(msg, "lerian.streaming.transaction.created") {
		t.Errorf("Error() missing topic; got %q", msg)
	}
}

// TestProducer_New_RealProducer_AsProducer: happy path — New with valid
// kfake cfg returns a concrete *Producer.
func TestProducer_New_RealProducer_AsProducer(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	if p.producerID == "" {
		t.Error("producerID is empty; want non-empty UUID")
	}

	if p.client == nil {
		t.Error("client is nil; want non-nil kgo client")
	}
}

// TestProducer_New_WithPartitionKeyOverride: operator-supplied partition-key
// function is actually used on the produced record.
func TestProducer_New_WithPartitionKeyOverride(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	fixed := "FIXED-PART-KEY"
	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithPartitionKey(func(_ Event) string { return fixed }),
	)
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

	consumer := newConsumer(t, cluster, event.Topic())

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
	defer fetchCancel()

	fetches := consumer.PollFetches(fetchCtx)

	var got *kgo.Record

	fetches.EachRecord(func(r *kgo.Record) {
		if got == nil {
			got = r
		}
	})

	if got == nil {
		t.Fatal("no record fetched")
	}

	if string(got.Key) != fixed {
		t.Errorf("partition key = %q; want %q", got.Key, fixed)
	}
}
