//go:build unit

package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// --- GROUP D: payload-size boundary + caller non-mutation. ---

// TestProducer_EmitPreFlight_PayloadAtExactBoundary pins the three-point
// acceptance shape around maxPayloadBytes (1 MiB). The existing
// TestProducer_EmitPreFlight_PayloadTooLarge only covers the +1 case; this
// test adds the exactly-at-limit and limit-minus-1 cases so a future refactor
// that flips the boundary operator (> vs >=) breaks the test loudly.
//
// The test asserts on preFlight directly (not through Emit) because the kfake
// broker has its own max.message.bytes that is strictly below our 1 MiB
// payload cap once CloudEvents headers are added. preFlight is the boundary
// under test — whether kfake accepts the wire-level message is an orthogonal
// concern that TestIntegration_DLQRouting already exercises on a real Redpanda.
//
// JSON payload shape: a quoted string `"<padding>"`. To reach len == N we
// need padding of N-2 bytes.
func TestProducer_EmitPreFlight_PayloadAtExactBoundary(t *testing.T) {
	tests := []struct {
		name        string
		payloadSize int
		wantErr     error // nil means preFlight accepts
	}{
		{
			name:        "at exact boundary (1 MiB)",
			payloadSize: maxPayloadBytes,
			wantErr:     nil,
		},
		{
			name:        "one byte below boundary",
			payloadSize: maxPayloadBytes - 1,
			wantErr:     nil,
		},
		{
			name:        "one byte above boundary",
			payloadSize: maxPayloadBytes + 1,
			wantErr:     ErrPayloadTooLarge,
		},
	}

	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Construct a JSON string literal whose byte length equals
			// tt.payloadSize. Quote characters contribute 2 bytes.
			padding := strings.Repeat("x", tt.payloadSize-2)
			payload := []byte(`"` + padding + `"`)

			if len(payload) != tt.payloadSize {
				t.Fatalf("test bug: payload len = %d; want %d", len(payload), tt.payloadSize)
			}

			// Assert json.Valid on the happy-path cases. An invalid payload
			// would surface ErrNotJSON instead of ErrPayloadTooLarge and mask
			// the assertion.
			if tt.wantErr == nil && !json.Valid(payload) {
				t.Fatalf("test bug: payload is not valid JSON at size %d", tt.payloadSize)
			}

			event := sampleEvent()
			event.Payload = json.RawMessage(payload)
			(&event).ApplyDefaults()

			err := p.preFlight(event)

			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("preFlight err = %v; want nil for payload size %d", err, tt.payloadSize)
				}
				return
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("preFlight err = %v; want errors.Is(%v)", err, tt.wantErr)
			}
		})
	}
}

// TestProducer_Emit_DoesNotMutateCaller pins the value-semantics invariant:
// Emit receives Event by value and must never touch the caller's struct.
// ApplyDefaults runs on a LOCAL copy inside Emit — this test proves the
// caller's zero-valued EventID / Timestamp / SchemaVersion / DataContentType
// remain zero after Emit returns.
//
// A future refactor that changes the Emit signature to `Emit(ctx, *Event)`
// or that accidentally mutates the parameter would break this test. That
// is the intended failure mode: callers (including our integration tests)
// rely on "Emit is read-only on the Event" to safely reuse event templates.
func TestProducer_Emit_DoesNotMutateCaller(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	// Construct an Event with explicit zero values on every field
	// ApplyDefaults would populate. Only the fields preFlight requires
	// (TenantID, ResourceType, EventType, Source, Payload) are populated.
	event := Event{
		TenantID:        "t-caller-mut",
		ResourceType:    "transaction",
		EventType:       "created",
		EventID:         "",          // zero — ApplyDefaults would fill uuid
		Timestamp:       time.Time{}, // zero — ApplyDefaults would fill now UTC
		SchemaVersion:   "",          // zero — ApplyDefaults would fill "1.0.0"
		Source:          "//test/caller-mut",
		DataContentType: "", // zero — ApplyDefaults would fill "application/json"
		Payload:         json.RawMessage(`{"k":"v"}`),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, event); err != nil {
		t.Fatalf("Emit err = %v", err)
	}

	// Caller's struct MUST remain zero on the four defaulted fields.
	if event.EventID != "" {
		t.Errorf("caller EventID mutated: %q; want empty", event.EventID)
	}

	if !event.Timestamp.IsZero() {
		t.Errorf("caller Timestamp mutated: %v; want zero", event.Timestamp)
	}

	if event.SchemaVersion != "" {
		t.Errorf("caller SchemaVersion mutated: %q; want empty", event.SchemaVersion)
	}

	if event.DataContentType != "" {
		t.Errorf("caller DataContentType mutated: %q; want empty", event.DataContentType)
	}
}
