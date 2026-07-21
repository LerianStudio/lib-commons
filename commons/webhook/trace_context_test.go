//go:build unit

package webhook

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestDoHTTP_InjectsTraceContext verifies the deliverer injects a valid W3C
// traceparent that matches the active span, and that it does NOT leak baggage
// (tenant IDs, secrets, etc.) via the propagator.
func TestDoHTTP_InjectsTraceContext(t *testing.T) {
	t.Parallel()

	var gotHeaders http.Header

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))

	// Real SDK tracer so the context carries a valid, sampled span context.
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-delivery")
	defer span.End()

	wantTraceID := span.SpanContext().TraceID().String()
	require.True(t, span.SpanContext().TraceID().IsValid())

	// Seed baggage with sensitive values. The explicit TraceContext propagator
	// must NOT propagate these to the external receiver.
	const tenantSentinel = "tenant-should-not-leak-42"
	member, err := baggage.NewMember("tenant_id", tenantSentinel)
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx = baggage.ContextWithBaggage(ctx, bag)

	// No endpoint secret: keeps the test focused on trace-context injection.
	lister := &mockLister{
		endpoints: []Endpoint{{ID: "ep-trace", URL: pubURL, Active: true}},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(ctx, newTestEvent())
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	traceparent := gotHeaders.Get("traceparent")
	require.NotEmpty(t, traceparent, "receiver must get a traceparent header")
	assert.True(t, strings.HasPrefix(traceparent, "00-"), "W3C traceparent version 00")
	assert.Contains(t, traceparent, wantTraceID, "traceparent must carry the active trace ID")

	// Baggage must not leak — neither as a baggage header nor anywhere else.
	assert.Empty(t, gotHeaders.Get("baggage"), "baggage must not be propagated")

	for name, values := range gotHeaders {
		for _, v := range values {
			assert.NotContains(t, v, tenantSentinel,
				"tenant/baggage value leaked in header %s", name)
		}
	}
}
