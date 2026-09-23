//go:build unit

package outbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func testSpanContext(t *testing.T, traceHex, spanHex string) trace.SpanContext {
	t.Helper()

	traceID, err := trace.TraceIDFromHex(traceHex)
	require.NoError(t, err)

	spanID, err := trace.SpanIDFromHex(spanHex)
	require.NoError(t, err)

	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
}

func TestCaptureTraceContext(t *testing.T) {
	t.Parallel()

	t.Run("captures traceparent from an active span context", func(t *testing.T) {
		t.Parallel()

		spanCtx := testSpanContext(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
		ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

		carrier := CaptureTraceContext(ctx)

		require.Equal(
			t,
			map[string]string{
				TraceContextTraceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			carrier,
		)
	})

	t.Run("returns nil without a span context", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, CaptureTraceContext(context.Background()))
	})

	t.Run("returns nil for a nil context", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, CaptureTraceContext(nil)) //nolint:staticcheck // nil context is an explicit guard case.
	})
}

func TestSanitizeTraceContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		carrier map[string]string
		want    map[string]string
	}{
		{
			name:    "nil carrier",
			carrier: nil,
			want:    nil,
		},
		{
			name:    "drops keys outside the W3C allowlist",
			carrier: map[string]string{"traceparent": "tp", "baggage": "user=root", "authorization": "Bearer x"},
			want:    map[string]string{"traceparent": "tp"},
		},
		{
			name:    "keeps tracestate",
			carrier: map[string]string{"traceparent": "tp", "tracestate": "vendor=1"},
			want:    map[string]string{"traceparent": "tp", "tracestate": "vendor=1"},
		},
		{
			name:    "normalizes key case and trims values",
			carrier: map[string]string{"TraceParent": "  tp  "},
			want:    map[string]string{"traceparent": "tp"},
		},
		{
			name:    "drops empty values",
			carrier: map[string]string{"traceparent": "   ", "tracestate": ""},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, SanitizeTraceContext(tt.carrier))
		})
	}
}

func TestRestoreTraceContext(t *testing.T) {
	t.Parallel()

	t.Run("restores the producer span context", func(t *testing.T) {
		t.Parallel()

		producer := testSpanContext(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
		carrier := CaptureTraceContext(trace.ContextWithSpanContext(context.Background(), producer))

		restored, ok := RestoreTraceContext(context.Background(), carrier)
		require.True(t, ok)
		require.Equal(t, producer.TraceID(), trace.SpanContextFromContext(restored).TraceID())
		require.Equal(t, producer.SpanID(), trace.SpanContextFromContext(restored).SpanID())
	})

	t.Run("reports false for an unusable carrier", func(t *testing.T) {
		t.Parallel()

		restored, ok := RestoreTraceContext(context.Background(), map[string]string{"traceparent": "garbage"})
		require.False(t, ok)
		require.False(t, trace.SpanContextFromContext(restored).IsValid())
	})

	t.Run("reports false for an empty carrier", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		restored, ok := RestoreTraceContext(ctx, nil)
		require.False(t, ok)
		require.Equal(t, ctx, restored)
	})
}
