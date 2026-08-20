//go:build unit

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// clientDurationMetric is the outbound RED metric httpobs registers. Asserted by
// name so the test fails loudly if the upstream contract ever drifts.
const clientDurationMetric = "http.client.request.duration"

// withGlobalMeterProvider installs a collectable MeterProvider as the OTel global
// for the duration of the test. The instrumentation deliberately reads the
// globals (a service installs them via Telemetry.ApplyGlobals), so this is what
// the production path actually sees.
//
// WARNING: mutates global state — tests using it must NOT call t.Parallel().
func withGlobalMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	original := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)

	t.Cleanup(func() {
		otel.SetMeterProvider(original)
		_ = mp.Shutdown(context.Background())
	})

	return reader
}

// withGlobalTracerProvider installs a recording TracerProvider as the OTel global
// for the duration of the test. httpobs produces NO span unless a TracerProvider
// is passed, so this is what proves the wiring forwards the global one.
//
// WARNING: mutates global state — tests using it must NOT call t.Parallel().
func withGlobalTracerProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)

	t.Cleanup(func() {
		otel.SetTracerProvider(original)
		_ = tp.Shutdown(context.Background())
	})

	return recorder
}

// metricNames returns every metric name currently collected.
func metricNames(t *testing.T, reader *sdkmetric.ManualReader) []string {
	t.Helper()

	rm := &metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(context.Background(), rm))

	var names []string

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names = append(names, m.Name)
		}
	}

	return names
}

// doRequestThrough drives one real request through the given client so the
// instrumented transport actually runs, and drains the body — httpobs ends the
// span on body close, so a leaked body would leave the span unfinished.
func doRequestThrough(t *testing.T, c *http.Client) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	resp, err := c.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

// TestNewDefaultHTTPClientWrapsTransport verifies the outbound transport is the
// instrumented wrapper rather than the bare *http.Transport. This is the whole
// point of the wiring: a service gets outbound telemetry by bumping lib-commons.
func TestNewDefaultHTTPClientWrapsTransport(t *testing.T) {
	c := newDefaultHTTPClient()

	require.NotNil(t, c.Transport)

	_, bare := c.Transport.(*http.Transport)
	assert.False(t, bare,
		"transport must be wrapped by httpobs, not the raw *http.Transport")
}

// TestNewDefaultHTTPClientPreservesTuning verifies wrapping does not cost the
// caller its connection-pool tuning: the wrapper must sit ON TOP of the tuned
// transport, never replace it.
func TestNewDefaultHTTPClientPreservesTuning(t *testing.T) {
	base := newDefaultTransport()

	assert.NotNil(t, base.Proxy, "Proxy must be set (http.ProxyFromEnvironment)")
	assert.NotNil(t, base.DialContext, "DialContext must be set via net.Dialer")
	assert.Equal(t, 100, base.MaxIdleConns, "MaxIdleConns")
	assert.Equal(t, 10, base.MaxIdleConnsPerHost, "MaxIdleConnsPerHost")
	assert.Equal(t, 90*time.Second, base.IdleConnTimeout, "IdleConnTimeout")
	assert.Equal(t, 10*time.Second, base.TLSHandshakeTimeout, "TLSHandshakeTimeout")
	assert.Equal(t, 1*time.Second, base.ExpectContinueTimeout, "ExpectContinueTimeout")

	// The HTTP/1.1-only opt-out must survive the wrapping (see
	// TestNewDefaultHTTPClient_UsesHTTP1Only for why this matters).
	require.NotNil(t, base.TLSNextProto)
	assert.Empty(t, base.TLSNextProto)
	assert.False(t, base.ForceAttemptHTTP2)

	// And the client keeps its own timeout.
	assert.Equal(t, 30*time.Second, newDefaultHTTPClient().Timeout, "Client.Timeout")
}

// TestOutboundRequestEmitsDurationMetric proves the wrapper is live end to end:
// a real request through the default client records the outbound RED metric.
func TestOutboundRequestEmitsDurationMetric(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	doRequestThrough(t, newDefaultHTTPClient())

	assert.Contains(t, metricNames(t, reader), clientDurationMetric,
		"outbound requests must record http.client.request.duration")
}

// TestOutboundRequestEmitsClientSpan pins the subtle half of the contract:
// httpobs emits NO span unless a TracerProvider is passed, so the wiring must
// forward the global one and not rely on httpobs defaulting it.
func TestOutboundRequestEmitsClientSpan(t *testing.T) {
	recorder := withGlobalTracerProvider(t)

	doRequestThrough(t, newDefaultHTTPClient())

	spans := recorder.Ended()
	require.NotEmpty(t, spans, "a CLIENT span must be produced for outbound requests")

	assert.Equal(t, trace.SpanKindClient, spans[0].SpanKind())
	assert.Equal(t, "HTTP GET", spans[0].Name(),
		"span name must stay bounded — never fold the URL path in")
}

// TestWithTimeoutInstrumentsBareClient covers the second construction site: the
// option builds a client from nothing when none exists yet, and that client must
// be instrumented too. httpobs is nil-safe, so the nil base falls back to
// http.DefaultTransport.
func TestWithTimeoutInstrumentsBareClient(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	c := &Client{}
	WithTimeout(5 * time.Second)(c)

	require.NotNil(t, c.httpClient)
	assert.Equal(t, 5*time.Second, c.httpClient.Timeout, "the option's own job must still happen")

	require.NotNil(t, c.httpClient.Transport, "the bare client must get an instrumented transport")

	doRequestThrough(t, c.httpClient)

	assert.Contains(t, metricNames(t, reader), clientDurationMetric)
}

// TestWithTimeoutKeepsExistingTransport verifies the option only fills in a
// missing client — it must never swap the transport of one already configured.
func TestWithTimeoutKeepsExistingTransport(t *testing.T) {
	existing := &http.Transport{}
	c := &Client{httpClient: &http.Client{Transport: existing}}

	WithTimeout(5 * time.Second)(c)

	assert.Same(t, existing, c.httpClient.Transport,
		"an existing client keeps its transport; only the timeout is set")
}

// TestInstrumentedTransportIsNilSafe pins the degradation contract: a nil base is
// valid and never panics, and telemetry being off is not an error.
func TestInstrumentedTransportIsNilSafe(t *testing.T) {
	assert.NotPanics(t, func() {
		assert.NotNil(t, instrumentedTransport(nil))
	})
}
