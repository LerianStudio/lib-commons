//go:build unit

package outbox

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func recordedSpan(t *testing.T, recorder *tracetest.SpanRecorder, name string) (sdktrace.ReadOnlySpan, bool) {
	t.Helper()

	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span, true
		}
	}

	return nil, false
}

func TestDispatcher_RestoresProducerTraceContext(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("outbox-test")

	producerCtx, producerSpan := tracer.Start(context.Background(), "producer.request")
	producerSpanCtx := producerSpan.SpanContext()
	carrier := CaptureTraceContext(producerCtx)
	producerSpan.End()

	require.NotEmpty(t, carrier)

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()

	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{
		ID:           eventID,
		EventType:    "payment.created",
		Payload:      []byte("ok"),
		TraceContext: carrier,
	}}

	var handlerSpanCtx trace.SpanContext

	require.NoError(t, handlers.Register("payment.created", func(ctx context.Context, _ *OutboxEvent) error {
		handlerSpanCtx = trace.SpanContextFromContext(ctx)

		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, tracer, WithPublishMaxAttempts(1))
	require.NoError(t, err)

	require.Equal(t, 1, dispatcher.DispatchOnce(context.Background()))

	require.Equal(t, producerSpanCtx.TraceID(), handlerSpanCtx.TraceID(),
		"handler must run inside the producer trace, not the dispatcher's own trace")

	publishSpan, ok := recordedSpan(t, recorder, "outbox.publish")
	require.True(t, ok, "expected a per-event publish span")
	require.Equal(t, producerSpanCtx.TraceID(), publishSpan.SpanContext().TraceID())
	require.Equal(t, producerSpanCtx.SpanID(), publishSpan.Parent().SpanID())

	cycleSpan, ok := recordedSpan(t, recorder, "outbox.dispatch")
	require.True(t, ok)

	require.Len(t, publishSpan.Links(), 1, "publish span must link back to the dispatch cycle")
	require.Equal(t, cycleSpan.SpanContext().SpanID(), publishSpan.Links()[0].SpanContext.SpanID())
	require.NotEqual(t, cycleSpan.SpanContext().TraceID(), publishSpan.SpanContext().TraceID())
}

func TestDispatcher_WithoutTraceContextKeepsDispatchTrace(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("outbox-test")

	producerCtx, producerSpan := tracer.Start(context.Background(), "producer.request")
	producerSpanCtx := producerSpan.SpanContext()

	_ = producerCtx

	producerSpan.End()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()

	repo.pending = []*OutboxEvent{{
		ID:        uuid.New(),
		EventType: "payment.created",
		Payload:   []byte("ok"),
	}}

	var handlerSpanCtx trace.SpanContext

	require.NoError(t, handlers.Register("payment.created", func(ctx context.Context, _ *OutboxEvent) error {
		handlerSpanCtx = trace.SpanContextFromContext(ctx)

		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, tracer, WithPublishMaxAttempts(1))
	require.NoError(t, err)

	require.Equal(t, 1, dispatcher.DispatchOnce(context.Background()))

	cycleSpan, ok := recordedSpan(t, recorder, "outbox.dispatch")
	require.True(t, ok)

	require.Equal(t, cycleSpan.SpanContext().TraceID(), handlerSpanCtx.TraceID())
	require.NotEqual(t, producerSpanCtx.TraceID(), handlerSpanCtx.TraceID())

	_, ok = recordedSpan(t, recorder, "outbox.publish")
	require.False(t, ok, "no publish span without a carrier: behaviour must be unchanged")
}
