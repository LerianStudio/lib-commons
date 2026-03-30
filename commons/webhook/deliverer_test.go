//go:build unit

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mockLister implements EndpointLister for testing.
type mockLister struct {
	endpoints []Endpoint
	err       error
}

func (m *mockLister) ListActiveEndpoints(_ context.Context) ([]Endpoint, error) {
	return m.endpoints, m.err
}

// mockMetrics implements DeliveryMetrics for testing.
type mockMetrics struct {
	mu    sync.Mutex
	calls []metricCall
}

type metricCall struct {
	EndpointID string
	Success    bool
	StatusCode int
	Attempts   int
}

func (m *mockMetrics) RecordDelivery(_ context.Context, endpointID string, success bool, statusCode int, attempts int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, metricCall{endpointID, success, statusCode, attempts})
}

func (m *mockMetrics) getCalls() []metricCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	cp := make([]metricCall, len(m.calls))
	copy(cp, m.calls)

	return cp
}

// newTestEvent creates a canonical event for tests.
func newTestEvent() *Event {
	return &Event{
		Type:      "order.created",
		Payload:   []byte(`{"id":"123"}`),
		Timestamp: 1700000000,
	}
}

// ssrfBypassClient returns an http.Client whose transport dials directly to
// the listener address, regardless of the URL hostname. This lets us put a
// publicly-routable hostname in the endpoint URL (bypassing SSRF validation)
// while actually connecting to the httptest server on 127.0.0.1.
func ssrfBypassClient(listenAddr string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
				return net.Dial(network, listenAddr)
			},
		},
	}
}

// startTestServer returns an httptest.Server with the handler and the
// "fake" public URL that points to 93.184.216.34 (example.com) but on the
// server's actual port. Tests use ssrfBypassClient to connect.
func startTestServer(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Extract port from the test server (which listens on 127.0.0.1:PORT).
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	// Build a URL with a public hostname so validateResolvedIP doesn't block it.
	// The ssrfBypassClient transport dials the real server regardless.
	publicURL := "http://example.com:" + port

	return srv, publicURL
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDefaultHTTPClient_BlocksRedirects(t *testing.T) {
	t.Parallel()

	client := defaultHTTPClient()
	require.NotNil(t, client.CheckRedirect, "CheckRedirect must be set to block redirects")

	// Simulate a redirect: CheckRedirect should return http.ErrUseLastResponse.
	err := client.CheckRedirect(nil, nil)
	assert.Equal(t, http.ErrUseLastResponse, err,
		"CheckRedirect should return http.ErrUseLastResponse to block redirect-following")
}

func TestDeliver_RedirectNotFollowed(t *testing.T) {
	t.Parallel()

	// Server that always redirects — the deliverer must treat this as a non-2xx
	// response and not follow the redirect.
	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-redir", URL: pubURL, Secret: "", Active: true},
		},
	}

	// Use ssrfBypassClient but add the CheckRedirect policy from defaultHTTPClient.
	client := ssrfBypassClient(srv.Listener.Addr().String())
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(client),
	)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)

	// The redirect (302) is a non-2xx status, so the delivery should fail.
	assert.False(t, results[0].Success, "redirect should not be followed, resulting in non-2xx")
	assert.Equal(t, http.StatusFound, results[0].StatusCode,
		"status code should be the redirect status, not the target's status")
}

func TestNewDeliverer_Defaults(t *testing.T) {
	t.Parallel()

	lister := &mockLister{}
	d := NewDeliverer(lister)

	require.NotNil(t, d)
	assert.Equal(t, 20, d.maxConc, "default maxConcurrency should be 20")
	assert.Equal(t, 3, d.maxRetries, "default maxRetries should be 3")
	assert.NotNil(t, d.client, "default HTTP client should not be nil")
	assert.NotNil(t, d.logger, "logger should default to nop logger")
	assert.Nil(t, d.tracer, "tracer should be nil when not set")
	assert.Nil(t, d.metrics, "metrics should be nil when not set")
	assert.Nil(t, d.decryptor, "decryptor should be nil when not set")
}

func TestNewDeliverer_NilLister(t *testing.T) {
	t.Parallel()

	d := NewDeliverer(nil)
	assert.Nil(t, d, "NewDeliverer should return nil when lister is nil")

	// Nil *Deliverer is safe to use — Deliver returns ErrNilDeliverer.
	err := d.Deliver(context.Background(), newTestEvent())
	require.ErrorIs(t, err, ErrNilDeliverer)

	// DeliverWithResults returns nil slice.
	results := d.DeliverWithResults(context.Background(), newTestEvent())
	assert.Nil(t, results)
}

func TestDeliver_NilDeliverer(t *testing.T) {
	t.Parallel()

	var d *Deliverer

	err := d.Deliver(context.Background(), newTestEvent())

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilDeliverer)
}

func TestDeliver_NilEvent(t *testing.T) {
	t.Parallel()

	lister := &mockLister{}
	d := NewDeliverer(lister)

	err := d.Deliver(context.Background(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil event")
}

func TestDeliver_NoActiveEndpoints(t *testing.T) {
	t.Parallel()

	lister := &mockLister{endpoints: []Endpoint{}}
	d := NewDeliverer(lister)

	err := d.Deliver(context.Background(), newTestEvent())

	assert.NoError(t, err, "empty endpoint list should not be an error")
}

func TestDeliver_ListerError(t *testing.T) {
	t.Parallel()

	listErr := errors.New("database connection refused")
	lister := &mockLister{err: listErr}
	d := NewDeliverer(lister)

	err := d.Deliver(context.Background(), newTestEvent())

	require.Error(t, err)
	assert.ErrorIs(t, err, listErr, "underlying lister error should be wrapped")
	assert.Contains(t, err.Error(), "list endpoints")
}

func TestDeliver_Success(t *testing.T) {
	t.Parallel()

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	metrics := &mockMetrics{}
	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-1", URL: pubURL, Secret: "test-secret", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMetrics(metrics),
		WithMaxRetries(1),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	err := d.Deliver(context.Background(), newTestEvent())
	require.NoError(t, err)

	// fanOut is async — wait for the metrics recording.
	require.Eventually(t, func() bool {
		return len(metrics.getCalls()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	calls := metrics.getCalls()
	require.Len(t, calls, 1)
	assert.True(t, calls[0].Success)
	assert.Equal(t, http.StatusOK, calls[0].StatusCode)
	assert.Equal(t, "ep-1", calls[0].EndpointID)
}

func TestDeliver_Retries(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))

	metrics := &mockMetrics{}
	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-retry", URL: pubURL, Secret: "s", Active: true},
		},
	}

	// maxRetries=3 means 4 total attempts (initial + 3 retries).
	// Server succeeds on attempt 3.
	d := NewDeliverer(lister,
		WithMetrics(metrics),
		WithMaxRetries(3),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)
	assert.True(t, results[0].Success)
	assert.Equal(t, 3, results[0].Attempts, "should have taken 3 attempts")
}

func TestDeliver_ExhaustedRetries(t *testing.T) {
	t.Parallel()

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	metrics := &mockMetrics{}
	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-fail", URL: pubURL, Secret: "s", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMetrics(metrics),
		WithMaxRetries(1),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)
	assert.False(t, results[0].Success)
	assert.ErrorIs(t, results[0].Error, ErrDeliveryFailed)
	assert.Equal(t, 2, results[0].Attempts, "initial + 1 retry = 2")
}

func TestDeliver_HMACSignature(t *testing.T) {
	t.Parallel()

	secret := "my-webhook-secret"
	event := newTestEvent()

	var gotSig string

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-hmac", URL: pubURL, Secret: secret, Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), event)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	// Compute expected HMAC independently.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(event.Payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	assert.Equal(t, expected, gotSig, "HMAC signature should match")
}

func TestDeliver_Headers(t *testing.T) {
	t.Parallel()

	event := newTestEvent()

	var (
		gotContentType string
		gotEventType   string
		gotTimestamp   string
	)

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotEventType = r.Header.Get("X-Webhook-Event")
		gotTimestamp = r.Header.Get("X-Webhook-Timestamp")
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-hdr", URL: pubURL, Secret: "", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), event)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, event.Type, gotEventType)
	assert.Equal(t, strconv.FormatInt(event.Timestamp, 10), gotTimestamp)
}

func TestDeliver_EncryptedSecret_WithDecryptor(t *testing.T) {
	t.Parallel()

	plainSecret := "decrypted-secret"
	event := newTestEvent()

	var gotSig string

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))

	decryptor := func(ciphertext string) (string, error) {
		if ciphertext == "abc123" {
			return plainSecret, nil
		}

		return "", errors.New("unknown ciphertext")
	}

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-enc", URL: pubURL, Secret: "enc:abc123", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithSecretDecryptor(decryptor),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), event)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	// Verify HMAC was computed with the *decrypted* secret.
	mac := hmac.New(sha256.New, []byte(plainSecret))
	mac.Write(event.Payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	assert.Equal(t, expected, gotSig)
}

func TestDeliver_EncryptedSecret_NoDecryptor(t *testing.T) {
	t.Parallel()

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	_ = srv // keep server alive via t.Cleanup

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-no-dec", URL: pubURL, Secret: "enc:ciphertext", Active: true},
		},
	}

	// No decryptor configured — fail-closed: delivery should abort.
	d := NewDeliverer(lister, WithMaxRetries(0))

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)
	assert.False(t, results[0].Success)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "no decryptor configured")
}

func TestDeliverWithResults_ReturnsPerEndpoint(t *testing.T) {
	t.Parallel()

	// Server 1: always succeeds.
	srv1, pubURL1 := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Server 2: always fails.
	srv2, pubURL2 := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	// Both servers need to be reachable via the same client. Since each
	// ssrfBypassClient pins all dials to a single address, we create a
	// mux-style transport that dispatches by port.
	addr1 := srv1.Listener.Addr().String()
	addr2 := srv2.Listener.Addr().String()

	_, port1, _ := net.SplitHostPort(addr1)
	_, port2, _ := net.SplitHostPort(addr2)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				_, p, _ := net.SplitHostPort(addr)
				switch p {
				case port1:
					return net.Dial(network, addr1)
				case port2:
					return net.Dial(network, addr2)
				default:
					return net.Dial(network, addr)
				}
			},
		},
	}

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "good", URL: pubURL1, Secret: "s1", Active: true},
			{ID: "bad", URL: pubURL2, Secret: "s2", Active: true},
		},
	}

	d := NewDeliverer(lister, WithMaxRetries(0), WithHTTPClient(client))

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 2)

	// Build a map for order-independent assertions.
	byID := make(map[string]DeliveryResult, len(results))
	for _, r := range results {
		byID[r.EndpointID] = r
	}

	assert.True(t, byID["good"].Success)
	assert.False(t, byID["bad"].Success)
	assert.ErrorIs(t, byID["bad"].Error, ErrDeliveryFailed)
}

func TestDeliver_InactiveEndpoints_Skipped(t *testing.T) {
	t.Parallel()

	var called atomic.Int32

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "active", URL: pubURL, Secret: "", Active: true},
			{ID: "inactive", URL: pubURL, Secret: "", Active: false},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), newTestEvent())

	// Only the active endpoint should produce a result.
	require.Len(t, results, 1)
	assert.Equal(t, "active", results[0].EndpointID)
	assert.True(t, results[0].Success)
}

func TestComputeHMAC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		secret  string
		want    string
	}{
		{
			name:    "known vector",
			payload: []byte(`{"id":"123"}`),
			secret:  "secret",
			want: func() string {
				mac := hmac.New(sha256.New, []byte("secret"))
				mac.Write([]byte(`{"id":"123"}`))
				return hex.EncodeToString(mac.Sum(nil))
			}(),
		},
		{
			name:    "empty payload",
			payload: []byte{},
			secret:  "key",
			want: func() string {
				mac := hmac.New(sha256.New, []byte("key"))
				mac.Write([]byte{})
				return hex.EncodeToString(mac.Sum(nil))
			}(),
		},
		{
			name:    "empty secret",
			payload: []byte("data"),
			secret:  "",
			want: func() string {
				mac := hmac.New(sha256.New, []byte(""))
				mac.Write([]byte("data"))
				return hex.EncodeToString(mac.Sum(nil))
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := computeHMAC(tt.payload, tt.secret)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Static HMAC test vector — externally verified hex digest
// ---------------------------------------------------------------------------

// TestComputeHMAC_StaticVector verifies computeHMAC against a known reference
// value produced by:
//
//	echo -n 'test-payload' | openssl dgst -sha256 -hmac 'test-secret' | awk '{print $2}'
//	→ 5b12467d7c448555779e70d76204105c67d27d1c991f3080c19732f9ac1988ef
func TestComputeHMAC_StaticVector(t *testing.T) {
	t.Parallel()

	const (
		payload  = "test-payload"
		secret   = "test-secret"
		expected = "5b12467d7c448555779e70d76204105c67d27d1c991f3080c19732f9ac1988ef"
	)

	got := computeHMAC([]byte(payload), secret)
	assert.Equal(t, expected, got,
		"HMAC-SHA256 of %q with key %q must match externally verified reference hex", payload, secret)
}

// ---------------------------------------------------------------------------
// Non-retryable 4xx responses — break immediately, do not exhaust retries
// ---------------------------------------------------------------------------

// TestDeliver_NonRetryable4xx verifies that when a server always returns 400
// (Bad Request), the deliverer does NOT retry — it returns immediately after
// the first attempt, even when maxRetries is set higher.
func TestDeliver_NonRetryable4xx(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-400", URL: pubURL, Secret: "", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(3), // 3 retries configured, but 4xx must short-circuit
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)

	assert.False(t, results[0].Success, "400 response must not be considered successful")
	assert.Equal(t, http.StatusBadRequest, results[0].StatusCode)
	assert.Equal(t, 1, int(attempts.Load()),
		"only 1 HTTP attempt must be made for a non-retryable 4xx status")
	assert.Equal(t, 1, results[0].Attempts,
		"DeliveryResult.Attempts must reflect the single attempt")
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "non-retryable status 400")
}

func TestDeliver_BodyIsSent(t *testing.T) {
	t.Parallel()

	ev := newTestEvent()

	var gotBody []byte

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			gotBody = body
		}

		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-body", URL: pubURL, Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), ev)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	assert.Equal(t, ev.Payload, gotBody)
}

func TestDeliver_NoSignature_WhenSecretEmpty(t *testing.T) {
	t.Parallel()

	var gotSig string

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-nosig", URL: pubURL, Secret: "", Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	assert.Empty(t, gotSig, "no signature header when secret is empty")
}

func TestWithOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       []Option
		checkConc  int
		checkRetry int
	}{
		{
			name:       "custom concurrency and retries",
			opts:       []Option{WithMaxConcurrency(5), WithMaxRetries(10)},
			checkConc:  5,
			checkRetry: 10,
		},
		{
			name:       "zero concurrency uses default",
			opts:       []Option{WithMaxConcurrency(0)},
			checkConc:  defaultMaxConcurrency,
			checkRetry: defaultMaxRetries,
		},
		{
			name:       "negative retries uses default",
			opts:       []Option{WithMaxRetries(-1)},
			checkConc:  defaultMaxConcurrency,
			checkRetry: defaultMaxRetries,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := NewDeliverer(&mockLister{}, tt.opts...)
			assert.Equal(t, tt.checkConc, d.maxConc)
			assert.Equal(t, tt.checkRetry, d.maxRetries)
		})
	}
}

func TestWithHTTPClient_Nil_KeepsDefault(t *testing.T) {
	t.Parallel()

	d := NewDeliverer(&mockLister{}, WithHTTPClient(nil))
	assert.NotNil(t, d.client, "nil http.Client option should keep default")
}

func TestDeliverWithResults_NilDeliverer(t *testing.T) {
	t.Parallel()

	var d *Deliverer

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	assert.Nil(t, results)
}

func TestDeliverWithResults_NilEvent(t *testing.T) {
	t.Parallel()

	d := NewDeliverer(&mockLister{})

	results := d.DeliverWithResults(context.Background(), nil)
	assert.Nil(t, results)
}

func TestDeliverWithResults_ListerError(t *testing.T) {
	t.Parallel()

	listErr := errors.New("db down")
	d := NewDeliverer(&mockLister{err: listErr})

	results := d.DeliverWithResults(context.Background(), newTestEvent())
	require.Len(t, results, 1)
	assert.ErrorIs(t, results[0].Error, listErr)
}

func TestFilterActive(t *testing.T) {
	t.Parallel()

	all := []Endpoint{
		{ID: "a", Active: true},
		{ID: "b", Active: false},
		{ID: "c", Active: true},
	}

	active := filterActive(all)
	require.Len(t, active, 2)
	assert.Equal(t, "a", active[0].ID)
	assert.Equal(t, "c", active[1].ID)
}

func TestResolveSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		decryptor SecretDecryptor
		want      string
		wantErr   string
	}{
		{
			name: "empty string passthrough",
			raw:  "",
			want: "",
		},
		{
			name: "plaintext passthrough",
			raw:  "my-secret",
			want: "my-secret",
		},
		{
			name:      "encrypted with decryptor",
			raw:       "enc:cipher",
			decryptor: func(_ string) (string, error) { return "plain", nil },
			want:      "plain",
		},
		{
			name:    "encrypted without decryptor",
			raw:     "enc:cipher",
			wantErr: "no decryptor configured",
		},
		{
			name: "decryptor returns error",
			raw:  "enc:bad",
			decryptor: func(_ string) (string, error) {
				return "", fmt.Errorf("bad ciphertext")
			},
			wantErr: "decrypt secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := &Deliverer{decryptor: tt.decryptor}

			got, err := d.resolveSecret(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sanitizeURL — credential redaction
// ---------------------------------------------------------------------------

func TestSanitizeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips query params",
			raw:  "https://example.com/webhook?token=secret&api_key=abc",
			want: "https://example.com/webhook",
		},
		{
			name: "strips userinfo",
			raw:  "https://user:pass@example.com/webhook",
			want: "https://example.com/webhook",
		},
		{
			name: "strips both userinfo and query",
			raw:  "https://admin:hunter2@example.com/hook?key=val",
			want: "https://example.com/hook",
		},
		{
			name: "no credentials passes through",
			raw:  "https://example.com/webhook",
			want: "https://example.com/webhook",
		},
		{
			name: "invalid URL returns placeholder",
			raw:  "://bad\x7f",
			want: "[invalid-url]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitizeURL(tt.raw)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Versioned signatures — v1 HMAC with timestamp binding
// ---------------------------------------------------------------------------

func TestComputeHMACv1(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	secret := "test-secret"
	timestamp := int64(1700000000)

	sig := computeHMACv1(payload, timestamp, secret)
	assert.True(t, strings.HasPrefix(sig, "v1,sha256="),
		"v1 signature must start with 'v1,sha256='")

	// Verify it is deterministic.
	sig2 := computeHMACv1(payload, timestamp, secret)
	assert.Equal(t, sig, sig2, "same input must produce same signature")

	// Different timestamp must produce different signature.
	sig3 := computeHMACv1(payload, timestamp+1, secret)
	assert.NotEqual(t, sig, sig3, "different timestamp must produce different signature")
}

func TestComputeSignature_V0Default(t *testing.T) {
	t.Parallel()

	d := &Deliverer{sigVersion: SignatureV0}
	payload := []byte(`{"id":"123"}`)
	timestamp := int64(1700000000)
	secret := "test-secret"

	sig := d.computeSignature(payload, timestamp, secret)
	assert.True(t, strings.HasPrefix(sig, "sha256="),
		"v0 signature must start with 'sha256='")
	assert.False(t, strings.HasPrefix(sig, "v1,"),
		"v0 signature must NOT have v1 prefix")
}

func TestComputeSignature_V1(t *testing.T) {
	t.Parallel()

	d := &Deliverer{sigVersion: SignatureV1}
	payload := []byte(`{"id":"123"}`)
	timestamp := int64(1700000000)
	secret := "test-secret"

	sig := d.computeSignature(payload, timestamp, secret)
	assert.True(t, strings.HasPrefix(sig, "v1,sha256="),
		"v1 signature must start with 'v1,sha256='")
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	secret := "test-secret"
	timestamp := int64(1700000000)

	tests := []struct {
		name    string
		sig     string
		wantErr string
	}{
		{
			name: "valid v0 signature",
			sig:  "sha256=" + computeHMAC(payload, secret),
		},
		{
			name: "valid v1 signature",
			sig:  computeHMACv1(payload, timestamp, secret),
		},
		{
			name:    "invalid v0 signature",
			sig:     "sha256=deadbeef",
			wantErr: "v0 signature mismatch",
		},
		{
			name:    "invalid v1 signature",
			sig:     "v1,sha256=deadbeef",
			wantErr: "v1 signature mismatch",
		},
		{
			name:    "unrecognized format",
			sig:     "v99,md5=abc",
			wantErr: "unrecognized signature format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := VerifySignature(payload, timestamp, secret, tt.sig)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVerifySignatureWithFreshness(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	secret := "test-secret"
	now := time.Now().Unix()

	// Fresh v1 signature (timestamp = now).
	freshSig := computeHMACv1(payload, now, secret)

	err := VerifySignatureWithFreshness(payload, now, secret, freshSig, 5*time.Minute)
	assert.NoError(t, err, "fresh v1 signature within tolerance must pass")

	// Stale v1 signature (timestamp = 1 hour ago).
	staleTS := now - 3600
	staleSig := computeHMACv1(payload, staleTS, secret)

	err = VerifySignatureWithFreshness(payload, staleTS, secret, staleSig, 5*time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside tolerance window")

	// V0 signature — freshness check is skipped (timestamp not signed).
	v0Sig := "sha256=" + computeHMAC(payload, secret)

	err = VerifySignatureWithFreshness(payload, staleTS, secret, v0Sig, 5*time.Minute)
	assert.NoError(t, err, "v0 signature skips freshness check")
}

func TestDeliver_V1Signature_EndToEnd(t *testing.T) {
	t.Parallel()

	secret := "v1-secret"
	event := newTestEvent()

	var gotSig string

	srv, pubURL := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))

	lister := &mockLister{
		endpoints: []Endpoint{
			{ID: "ep-v1", URL: pubURL, Secret: secret, Active: true},
		},
	}

	d := NewDeliverer(lister,
		WithMaxRetries(0),
		WithSignatureVersion(SignatureV1),
		WithHTTPClient(ssrfBypassClient(srv.Listener.Addr().String())),
	)

	results := d.DeliverWithResults(context.Background(), event)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	// The signature should be v1 format.
	assert.True(t, strings.HasPrefix(gotSig, "v1,sha256="),
		"v1 deliverer must produce v1 signature format, got: %s", gotSig)

	// Verify the signature is correct.
	err := VerifySignature(event.Payload, event.Timestamp, secret, gotSig)
	assert.NoError(t, err, "v1 signature from deliverer must verify correctly")
}
