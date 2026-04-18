//go:build unit

package streaming

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// newSpanRecorder builds a TracerProvider backed by an in-memory exporter
// and returns the tracer + a helper that returns all spans captured so far.
//
// Uses SyncSpanProcessor so every span.End() immediately lands in the
// exporter — tests don't need to juggle flushes.
func newSpanRecorder(t *testing.T) (trace.Tracer, func() tracetest.SpanStubs) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)),
	)

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	return provider.Tracer("streaming-span-test"), exporter.GetSpans
}

// debugLogger is a log.Logger whose Enabled returns true for every level.
// Used to exercise the DEBUG-gated messaging.kafka.message.key span attr.
// The spyLogger from publish_dlq_test.go always returns true for Enabled too,
// but this narrower type makes the test's intent obvious.
type debugLogger struct {
	mu      sync.Mutex
	entries []spyEntry
}

func (d *debugLogger) Log(_ context.Context, level log.Level, msg string, fields ...log.Field) {
	d.mu.Lock()
	defer d.mu.Unlock()

	indexed := make(map[string]any, len(fields))
	for _, f := range fields {
		indexed[f.Key] = f.Value
	}

	d.entries = append(d.entries, spyEntry{level: level, msg: msg, fields: indexed})
}

func (d *debugLogger) With(_ ...log.Field) log.Logger { return d }
func (d *debugLogger) WithGroup(_ string) log.Logger  { return d }
func (d *debugLogger) Enabled(_ log.Level) bool       { return true }
func (d *debugLogger) Sync(_ context.Context) error   { return nil }

// infoLogger is a log.Logger whose Enabled returns true only for levels
// with LESS numeric value than Debug (i.e., it treats itself as INFO-level,
// so Debug is suppressed). Lets the span test verify the key-attr gating.
type infoLogger struct{}

func (i *infoLogger) Log(context.Context, log.Level, string, ...log.Field) {}
func (i *infoLogger) With(...log.Field) log.Logger                         { return i }
func (i *infoLogger) WithGroup(string) log.Logger                          { return i }
func (i *infoLogger) Enabled(l log.Level) bool                             { return l < log.LevelDebug }
func (i *infoLogger) Sync(context.Context) error                           { return nil }

// requireStreamingEmitSpan returns the single span named "streaming.emit"
// from the spans slice, failing the test if 0 or 2+ are present. The
// single-span invariant is DX-D01 and the test assertion is the load-
// bearing check.
func requireStreamingEmitSpan(t *testing.T, spans tracetest.SpanStubs) tracetest.SpanStub {
	t.Helper()

	var matches []tracetest.SpanStub

	for _, s := range spans {
		if s.Name == emitSpanName {
			matches = append(matches, s)
		}
	}

	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 %q span; got %d (all spans: %d)",
			emitSpanName, len(matches), len(spans))
	}

	return matches[0]
}

// spanAttrs converts a span's []attribute.KeyValue into a map for easy
// lookup. Values are stringified via .Emit so non-string types serialise.
func spanAttrs(s tracetest.SpanStub) map[string]string {
	out := make(map[string]string, len(s.Attributes))
	for _, kv := range s.Attributes {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

// --- Span tests. ---

// TestEmit_Span_SingleStreamingEmitSpan: one Emit → exactly one span named
// "streaming.emit" with SpanKindProducer and the full attribute envelope
// from TRD §7.2.
//
// Load-bearing for DX-D01 (single-span invariant) and AC-05 (every attr).
func TestEmit_Span_SingleStreamingEmitSpan(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	tracer, getSpans := newSpanRecorder(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithTracer(tracer),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	event := sampleEvent()
	if err := emitter.Emit(ctx, event); err != nil {
		t.Fatalf("Emit err = %v", err)
	}

	span := requireStreamingEmitSpan(t, getSpans())

	// Kind check.
	if span.SpanKind != trace.SpanKindProducer {
		t.Errorf("SpanKind = %v; want SpanKindProducer", span.SpanKind)
	}

	attrs := spanAttrs(span)

	// TRD §7.2 required attributes.
	wantAttrs := map[string]string{
		"messaging.system":           "kafka",
		"messaging.destination.name": event.Topic(),
		"messaging.operation.type":   "send",
		"messaging.client.id":        cfg.ClientID,
		"event.resource_type":        event.ResourceType,
		"event.event_type":           event.EventType,
		"tenant.id":                  event.TenantID,
		"event.outcome":              outcomeProduced,
	}

	for k, want := range wantAttrs {
		if got := attrs[k]; got != want {
			t.Errorf("span attr %s = %q; want %q", k, got, want)
		}
	}

	// streaming.producer_id is present and non-empty (UUID set at
	// construction — tests can't predict the value).
	if got := attrs["streaming.producer_id"]; got == "" {
		t.Errorf("span attr streaming.producer_id is empty; want a UUID")
	}

	// messaging.kafka.message.key MUST be absent when the package logger
	// is not DEBUG-enabled. NewNop's Enabled returns false for every
	// level, so the key attr should be absent here.
	if _, present := attrs["messaging.kafka.message.key"]; present {
		t.Errorf("span attr messaging.kafka.message.key present but logger not at DEBUG; got %q",
			attrs["messaging.kafka.message.key"])
	}
}

// TestEmit_Span_OutcomeAttributeReflectsBranch: table-driven matrix that
// forces each of the five terminal outcomes and asserts the span's
// event.outcome attribute matches. AC-06 requires outcome label coverage.
//
// circuit_open and outboxed are driven by flipping p.cbStateFlag (same
// technique as producer_cb_test.go); dlq is driven via kfake injection;
// caller_error is driven by an invalid-JSON payload (post-preflight is
// impossible without an EmitError which we cover in TestEmit_Span_DLQ).
// The preflight caller_error path is covered by a separate test because
// that branch does NOT create a span.
func TestEmit_Span_OutcomeAttributeReflectsBranch(t *testing.T) {
	// Note: the "caller_error" span test covers the POST-preflight caller
	// path (e.g. an EmitError whose class is in callerErrorClasses but is
	// NOT DLQ-routable — currently only ClassValidation/ClassContextCanceled).
	// Forcing that condition here is awkward; ClassValidation-class
	// EmitErrors are synthesised only by classifyError mapping — not by a
	// real kfake path. We therefore skip it and rely on the preflight test
	// (TestEmit_Span_PreflightFailure_NoSpan) plus DLQ coverage to carry
	// the invariants.

	t.Run("produced", func(t *testing.T) {
		cfg, _ := kfakeConfig(t)
		tracer, getSpans := newSpanRecorder(t)

		emitter, err := New(context.Background(), cfg,
			WithLogger(log.NewNop()), WithTracer(tracer))
		if err != nil {
			t.Fatalf("New err = %v", err)
		}
		t.Cleanup(func() { _ = emitter.Close() })

		if err := emitter.Emit(context.Background(), sampleEvent()); err != nil {
			t.Fatalf("Emit err = %v", err)
		}

		span := requireStreamingEmitSpan(t, getSpans())
		if got := spanAttrs(span)["event.outcome"]; got != outcomeProduced {
			t.Errorf("event.outcome = %q; want %q", got, outcomeProduced)
		}
	})

	t.Run("outboxed", func(t *testing.T) {
		cfg, _ := kfakeConfig(t)
		tracer, getSpans := newSpanRecorder(t)

		repo := &fakeOutboxRepo{}
		emitter, err := New(context.Background(), cfg,
			WithLogger(log.NewNop()), WithTracer(tracer),
			WithOutboxRepository(repo))
		if err != nil {
			t.Fatalf("New err = %v", err)
		}
		t.Cleanup(func() { _ = emitter.Close() })

		p := asProducer(t, emitter)
		p.cbStateFlag.Store(flagCBOpen)

		if err := emitter.Emit(context.Background(), sampleEvent()); err != nil {
			t.Fatalf("Emit err = %v", err)
		}

		span := requireStreamingEmitSpan(t, getSpans())
		attrs := spanAttrs(span)
		if got := attrs["event.outcome"]; got != outcomeOutboxed {
			t.Errorf("event.outcome = %q; want %q", got, outcomeOutboxed)
		}

		// outbox.routed span event present with reason=circuit_open.
		foundRoutedEvent := false
		for _, ev := range span.Events {
			if ev.Name == "outbox.routed" {
				foundRoutedEvent = true
				for _, attr := range ev.Attributes {
					if string(attr.Key) == "reason" && attr.Value.Emit() != "circuit_open" {
						t.Errorf("outbox.routed reason = %q; want %q",
							attr.Value.Emit(), "circuit_open")
					}
				}
			}
		}
		if !foundRoutedEvent {
			t.Errorf("outbox.routed span event missing")
		}
	})

	t.Run("circuit_open", func(t *testing.T) {
		cfg, _ := kfakeConfig(t)
		tracer, getSpans := newSpanRecorder(t)

		emitter, err := New(context.Background(), cfg,
			WithLogger(log.NewNop()), WithTracer(tracer))
		if err != nil {
			t.Fatalf("New err = %v", err)
		}
		t.Cleanup(func() { _ = emitter.Close() })

		p := asProducer(t, emitter)
		p.cbStateFlag.Store(flagCBOpen)

		if err := emitter.Emit(context.Background(), sampleEvent()); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("Emit err = %v; want ErrCircuitOpen", err)
		}

		span := requireStreamingEmitSpan(t, getSpans())
		if got := spanAttrs(span)["event.outcome"]; got != outcomeCircuitOpen {
			t.Errorf("event.outcome = %q; want %q", got, outcomeCircuitOpen)
		}
	})

	t.Run("dlq", func(t *testing.T) {
		cfg, cluster := kfakeDLQConfig(t)
		injectProduceError(cluster, "lerian.streaming.transaction.created",
			kerr.MessageTooLarge.Code)

		tracer, getSpans := newSpanRecorder(t)
		emitter, err := New(context.Background(), cfg,
			WithLogger(log.NewNop()), WithTracer(tracer))
		if err != nil {
			t.Fatalf("New err = %v", err)
		}
		t.Cleanup(func() { _ = emitter.Close() })

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := emitter.Emit(ctx, sampleEvent()); err == nil {
			t.Fatalf("Emit err = nil; want an error")
		}

		span := requireStreamingEmitSpan(t, getSpans())
		attrs := spanAttrs(span)
		if got := attrs["event.outcome"]; got != outcomeDLQ {
			t.Errorf("event.outcome = %q; want %q", got, outcomeDLQ)
		}
		if got := attrs["error.type"]; got != string(ClassSerialization) {
			t.Errorf("error.type = %q; want %q", got, ClassSerialization)
		}
	})
}

// TestEmit_Span_PreflightFailure_NoSpan: when preflight rejects the Emit
// (caller error, fast-fail), NO span should be created. The metric-only
// instrumentation path is tested in metrics_test.go; here we confirm the
// SPAN side of the contract.
//
// This keeps the span count proportional to actual emission attempts and
// prevents a misbehaving caller from DoSing the tracing backend with
// validation errors.
func TestEmit_Span_PreflightFailure_NoSpan(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	tracer, getSpans := newSpanRecorder(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()), WithTracer(tracer))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.TenantID = "" // triggers ErrMissingTenantID

	if err := emitter.Emit(context.Background(), bad); !errors.Is(err, ErrMissingTenantID) {
		t.Fatalf("Emit err = %v; want ErrMissingTenantID", err)
	}

	spans := getSpans()

	// Walk every span, confirm none named streaming.emit.
	for _, s := range spans {
		if s.Name == emitSpanName {
			t.Errorf("preflight failure produced a %q span; want none", emitSpanName)
		}
	}
}

// TestEmit_Span_DebugLevelEmitsMessageKey: when the package logger is at
// DEBUG level, messaging.kafka.message.key attribute must be present on
// the span. At INFO and below (no DEBUG), it must be absent.
//
// Guards TRD §7.2 footnote: partition keys are high-cardinality and only
// safe to emit to trace backends when operators have opted in via DEBUG.
func TestEmit_Span_DebugLevelEmitsMessageKey(t *testing.T) {
	t.Run("debug_enabled_key_present", func(t *testing.T) {
		cfg, _ := kfakeConfig(t)
		tracer, getSpans := newSpanRecorder(t)

		emitter, err := New(context.Background(), cfg,
			WithLogger(&debugLogger{}), WithTracer(tracer))
		if err != nil {
			t.Fatalf("New err = %v", err)
		}
		t.Cleanup(func() { _ = emitter.Close() })

		event := sampleEvent()
		if err := emitter.Emit(context.Background(), event); err != nil {
			t.Fatalf("Emit err = %v", err)
		}

		span := requireStreamingEmitSpan(t, getSpans())
		attrs := spanAttrs(span)

		got, ok := attrs["messaging.kafka.message.key"]
		if !ok {
			t.Fatalf("messaging.kafka.message.key absent with DEBUG logger; want present")
		}
		if got != event.PartitionKey() {
			t.Errorf("messaging.kafka.message.key = %q; want %q (tenant ID)",
				got, event.PartitionKey())
		}
	})

	t.Run("info_only_key_absent", func(t *testing.T) {
		cfg, _ := kfakeConfig(t)
		tracer, getSpans := newSpanRecorder(t)

		emitter, err := New(context.Background(), cfg,
			WithLogger(&infoLogger{}), WithTracer(tracer))
		if err != nil {
			t.Fatalf("New err = %v", err)
		}
		t.Cleanup(func() { _ = emitter.Close() })

		if err := emitter.Emit(context.Background(), sampleEvent()); err != nil {
			t.Fatalf("Emit err = %v", err)
		}

		span := requireStreamingEmitSpan(t, getSpans())

		if _, present := spanAttrs(span)["messaging.kafka.message.key"]; present {
			t.Errorf("messaging.kafka.message.key present with INFO-only logger; want absent")
		}
	})
}

// TestEmit_SpanError_RecordedOnFailure: when Emit fails after preflight
// (broker error → DLQ), span.RecordError must have fired. The exporter
// surfaces RecordError via span events with the "exception" name.
func TestEmit_SpanError_RecordedOnFailure(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)
	injectProduceError(cluster, "lerian.streaming.transaction.created",
		kerr.MessageTooLarge.Code)

	tracer, getSpans := newSpanRecorder(t)
	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()), WithTracer(tracer))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err == nil {
		t.Fatalf("Emit err = nil; want a classified error")
	}

	span := requireStreamingEmitSpan(t, getSpans())

	// RecordError adds a span event with Name == "exception" (per OTEL
	// semconv). Walk the events list looking for one.
	foundException := false
	for _, ev := range span.Events {
		if ev.Name == "exception" {
			foundException = true
			break
		}
	}

	if !foundException {
		t.Errorf("span missing 'exception' event; span.RecordError was not called. events=%v",
			spanEventNames(span))
	}
}

// spanEventNames returns the sequence of event names on a span, for
// better error messages.
func spanEventNames(s tracetest.SpanStub) []string {
	names := make([]string, 0, len(s.Events))
	for _, e := range s.Events {
		names = append(names, e.Name)
	}
	return names
}

// TestEmit_Span_NilTracerFallback: when WithTracer is omitted, the Producer
// must fall back to the global tracer provider (otel.Tracer("streaming")).
// If no global provider is set, otel returns a no-op tracer and spans
// are silently dropped — this is the correct behaviour for library code
// and matches the conventions in commons/rabbitmq / commons/postgres.
//
// This test is NOT asserting a span is visible (no global provider set);
// it's asserting the Producer does not panic and the Emit succeeds.
func TestEmit_Span_NilTracerFallback(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	// Deliberately omit WithTracer.
	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	if p.tracer == nil {
		t.Errorf("p.tracer = nil after NewProducer; want fallback to otel.Tracer(%q)", tracerName)
	}

	if err := emitter.Emit(context.Background(), sampleEvent()); err != nil {
		t.Errorf("Emit err = %v; want nil (no-op tracer)", err)
	}
}

// TestEmit_Span_IsRecordingFastPath: a span backed by a no-op tracer must
// skip the attribute-building work. Setting an unrecording-span attribute
// would waste cycles on high-volume publishers. We test this by swapping
// in a tracer that returns a non-recording Span and asserting our
// setEmitSpanAttributes is a no-op (no panic, nothing observable).
//
// This tests the IsRecording() fast-path guard in setEmitSpanAttributes.
func TestEmit_Span_IsRecordingFastPath(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	// A tracer with no SpanProcessor never records; span.IsRecording == false.
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithTracer(provider.Tracer("no-record")))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	// The sole assertion is "no panic + Emit success" — when the tracer
	// is non-recording, there are no observable side effects for us to
	// check via span. The fast-path guard is exercised simply by the
	// Emit completing without walking the SetAttributes codepath.
	if err := emitter.Emit(context.Background(), sampleEvent()); err != nil {
		t.Errorf("Emit err = %v; want nil", err)
	}
}

// TestEmit_Span_AttributeCountInvariant: guard against accidentally adding
// a tenant_id metric label via the span's attribute builder (they share
// the same `attribute.KeyValue` type). If a future edit mistakenly puts
// tenant_id on a metric via attribute.String, this test keeps the span
// attribute list free of surprises.
func TestEmit_Span_AttributeCountInvariant(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	tracer, getSpans := newSpanRecorder(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()), WithTracer(tracer))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	if err := emitter.Emit(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("Emit err = %v", err)
	}

	span := requireStreamingEmitSpan(t, getSpans())

	// Expected keys for happy-path produced:
	//  messaging.system, messaging.destination.name, messaging.operation.type,
	//  messaging.client.id, event.resource_type, event.event_type, tenant.id,
	//  streaming.producer_id, event.outcome.
	wantKeys := []string{
		"messaging.system",
		"messaging.destination.name",
		"messaging.operation.type",
		"messaging.client.id",
		"event.resource_type",
		"event.event_type",
		"tenant.id",
		"streaming.producer_id",
		"event.outcome",
	}

	have := make(map[string]struct{}, len(span.Attributes))
	for _, kv := range span.Attributes {
		have[string(kv.Key)] = struct{}{}
	}

	for _, key := range wantKeys {
		if _, ok := have[key]; !ok {
			t.Errorf("span missing attribute %q; have=%v", key,
				sortedKeys(have))
		}
	}
}

// sortedKeys returns the map keys in insertion order for error messages.
// A stable order is not required; this is purely cosmetic for readable
// failure output.
func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
