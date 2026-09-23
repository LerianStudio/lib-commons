//go:build unit

package outbox

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestNewOutboxEvent_WithTraceContext(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"key":"value"}`)

	t.Run("captures the caller trace context when the option is used", func(t *testing.T) {
		t.Parallel()

		spanCtx := testSpanContext(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
		ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

		event, err := NewOutboxEvent(ctx, "event.type", uuid.New(), payload, WithTraceContext(ctx))
		require.NoError(t, err)
		require.Equal(
			t,
			map[string]string{
				TraceContextTraceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			event.TraceContext,
		)
	})

	t.Run("leaves the carrier unset without the option", func(t *testing.T) {
		t.Parallel()

		spanCtx := testSpanContext(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
		ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

		event, err := NewOutboxEvent(ctx, "event.type", uuid.New(), payload)
		require.NoError(t, err)
		require.Nil(t, event.TraceContext)
	})

	t.Run("leaves the carrier unset when the caller has no span", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		event, err := NewOutboxEventWithID(ctx, uuid.New(), "event.type", uuid.New(), payload, WithTraceContext(ctx))
		require.NoError(t, err)
		require.Nil(t, event.TraceContext)
	})

	t.Run("sanitizes an explicitly supplied carrier", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		event, err := NewOutboxEvent(ctx, "event.type", uuid.New(), payload, WithTraceCarrier(map[string]string{
			"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			"baggage":     "user=root",
		}))
		require.NoError(t, err)
		require.Equal(
			t,
			map[string]string{
				TraceContextTraceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			event.TraceContext,
		)
	})
}
