package client

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewDefaultHTTPClient_UsesHTTP1Only is a regression test for the
// 2026-04-10 production incident where an inherited "h2" handler in
// http.DefaultTransport.TLSNextProto caused HTTP/2 negotiation and malformed
// responses under concurrent load.
//
// The invariant enforced here: newDefaultHTTPClient must produce a transport
// that will never negotiate HTTP/2 via ALPN, regardless of what any other
// package (OpenTelemetry, middleware, etc.) did to http.DefaultTransport.
func TestNewDefaultHTTPClient_UsesHTTP1Only(t *testing.T) {
	// Simulate OTel boot-time instrumentation that registers an "h2" handler.
	// Save and restore so we don't affect other tests.
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	contaminated := &http.Transport{
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{
			"h2": func(string, *tls.Conn) http.RoundTripper { return nil },
		},
	}
	http.DefaultTransport = contaminated

	client := newDefaultHTTPClient()
	require.NotNil(t, client)
	require.NotNil(t, client.Transport)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "transport must be *http.Transport")

	// TLSNextProto must be non-nil (explicit opt-out) and must NOT contain h2.
	// A nil map would trigger stdlib's HTTP/2 auto-setup; an "h2" entry would
	// make ALPN negotiate HTTP/2.
	require.NotNil(t, transport.TLSNextProto, "TLSNextProto must be non-nil to opt out of HTTP/2 auto-setup")
	assert.Empty(t, transport.TLSNextProto, "TLSNextProto must be empty — no h2 or other handlers")
	_, hasH2 := transport.TLSNextProto["h2"]
	assert.False(t, hasH2, "contaminated http.DefaultTransport must not leak h2 handler into client")

	assert.False(t, transport.ForceAttemptHTTP2, "ForceAttemptHTTP2 must be false")
}

// TestNewDefaultHTTPClient_TransportIsIsolated ensures the client's transport
// does not share memory with http.DefaultTransport. A future mutation of the
// global must not leak into an already-constructed client.
func TestNewDefaultHTTPClient_TransportIsIsolated(t *testing.T) {
	client := newDefaultHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)

	assert.NotSame(t, defaultTransport, transport,
		"client transport must not share pointer identity with http.DefaultTransport")
}

// TestNewDefaultHTTPClient_ExpectedDefaults pins the transport configuration
// so that silent regressions on pool sizing, timeouts, or proxy support are
// caught by CI.
func TestNewDefaultHTTPClient_ExpectedDefaults(t *testing.T) {
	client := newDefaultHTTPClient()
	require.NotNil(t, client)

	assert.Equal(t, 30*time.Second, client.Timeout, "Client.Timeout")

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)

	assert.NotNil(t, transport.Proxy, "Proxy must be set (http.ProxyFromEnvironment)")
	assert.NotNil(t, transport.DialContext, "DialContext must be set via net.Dialer")
	assert.Equal(t, 100, transport.MaxIdleConns, "MaxIdleConns")
	assert.Equal(t, 10, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost")
	assert.Equal(t, 90*time.Second, transport.IdleConnTimeout, "IdleConnTimeout")
	assert.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout, "TLSHandshakeTimeout")
	assert.Equal(t, 1*time.Second, transport.ExpectContinueTimeout, "ExpectContinueTimeout")
}
