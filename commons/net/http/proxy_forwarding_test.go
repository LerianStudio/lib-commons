//go:build unit

package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeReverseProxy_HeaderForwarding(t *testing.T) {
	t.Parallel()

	var receivedHost string
	var receivedForwardedHost string
	var receivedForwardedFor string
	var receivedForwardedProto string
	var receivedForwarded string
	var receivedRealIP string
	var receivedAuthorization string
	var receivedCookie string

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		receivedForwardedHost = r.Header.Get("X-Forwarded-Host")
		receivedForwardedFor = r.Header.Get("X-Forwarded-For")
		receivedForwardedProto = r.Header.Get("X-Forwarded-Proto")
		receivedForwarded = r.Header.Get("Forwarded")
		receivedRealIP = r.Header.Get("X-Real-Ip")
		receivedAuthorization = r.Header.Get("Authorization")
		receivedCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte("headers checked"))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, "http://original-host.local/proxy", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=203.0.113.10;proto=https")
	req.Header.Set("X-Real-Ip", "203.0.113.10")
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Cookie", "session=abc123")
	rr := httptest.NewRecorder()

	host := requestHostFromURL(t, target.URL)

	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{host},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)

	resp := rr.Result()
	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "headers checked", string(body))
	assert.Contains(t, receivedHost, host)
	assert.Equal(t, "original-host.local", receivedForwardedHost)
	assert.Equal(t, "https", receivedForwardedProto)
	assert.Contains(t, receivedForwardedFor, "203.0.113.10")
	assert.Equal(t, "for=203.0.113.10;proto=https", receivedForwarded)
	assert.Equal(t, "203.0.113.10", receivedRealIP)
	assert.Equal(t, "Bearer test-token", receivedAuthorization)
	assert.Equal(t, "session=abc123", receivedCookie)
}

func TestServeReverseProxy_ProxyPassesResponseBody(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"created"}`))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	host := requestHostFromURL(t, target.URL)

	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{host},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)

	resp := rr.Result()
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"created"}`, string(body))
}

func TestServeReverseProxy_CaseInsensitiveScheme(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	host := requestHostFromURL(t, target.URL)

	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"HTTP"},
		AllowedHosts:            []string{host},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)

	resp := rr.Result()
	defer func() { require.NoError(t, resp.Body.Close()) }()
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))
}

func TestServeReverseProxy_MultipleAllowedSchemes(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("multi-scheme"))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	host := requestHostFromURL(t, target.URL)

	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"https", "http"},
		AllowedHosts:            []string{host},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)

	resp := rr.Result()
	defer func() { require.NoError(t, resp.Body.Close()) }()
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "multi-scheme", string(body))
}
