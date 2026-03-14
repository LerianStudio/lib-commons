//go:build unit

package http

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeReverseProxy_UpstreamTransportFailureReturns502(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	err = ServeReverseProxy("http://"+addr, ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{"127.0.0.1"},
		AllowUnsafeDestinations: true,
	}, rr, req)
	require.NoError(t, err)

	resp := rr.Result()
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestSSRFSafeTransport_DialContext_RejectsPrivateIP(t *testing.T) {
	t.Parallel()

	transport := newSSRFSafeTransport(ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{"localhost"},
		AllowUnsafeDestinations: false,
	})

	require.NotNil(t, transport)
	require.NotNil(t, transport.base)
	require.NotNil(t, transport.base.DialContext, "DialContext should be set when AllowUnsafeDestinations is false")

	_, err := transport.base.DialContext(context.Background(), "tcp", "localhost:80")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestSSRFSafeTransport_DialContext_AllowsWhenUnsafeEnabled(t *testing.T) {
	t.Parallel()

	transport := newSSRFSafeTransport(ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{"localhost"},
		AllowUnsafeDestinations: true,
	})

	require.NotNil(t, transport)
	require.NotNil(t, transport.base)
	require.NotNil(t, transport.base.DialContext)
}

func TestSSRFSafeTransport_RoundTrip_RejectsUntrustedScheme(t *testing.T) {
	t.Parallel()

	transport := newSSRFSafeTransport(ReverseProxyPolicy{
		AllowedSchemes:          []string{"https"},
		AllowedHosts:            []string{"example.com"},
		AllowUnsafeDestinations: false,
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/path", nil)

	_, err := transport.RoundTrip(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUntrustedProxyScheme)
}

func TestSSRFSafeTransport_RoundTrip_RejectsUntrustedHost(t *testing.T) {
	t.Parallel()

	transport := newSSRFSafeTransport(ReverseProxyPolicy{
		AllowedSchemes:          []string{"https"},
		AllowedHosts:            []string{"trusted.com"},
		AllowUnsafeDestinations: false,
	})

	req := httptest.NewRequest(http.MethodGet, "https://evil.com/path", nil)

	_, err := transport.RoundTrip(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUntrustedProxyHost)
}

func TestSSRFSafeTransport_RoundTrip_RejectsPrivateIPInRedirect(t *testing.T) {
	t.Parallel()

	transport := newSSRFSafeTransport(ReverseProxyPolicy{
		AllowedSchemes:          []string{"https"},
		AllowedHosts:            []string{"127.0.0.1"},
		AllowUnsafeDestinations: false,
	})

	req := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin", nil)

	_, err := transport.RoundTrip(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestNewSSRFSafeTransport_PolicyIsStored(t *testing.T) {
	t.Parallel()

	policy := ReverseProxyPolicy{
		AllowedSchemes:          []string{"https", "http"},
		AllowedHosts:            []string{"api.example.com"},
		AllowUnsafeDestinations: false,
	}

	transport := newSSRFSafeTransport(policy)

	assert.Equal(t, policy.AllowedSchemes, transport.policy.AllowedSchemes)
	assert.Equal(t, policy.AllowedHosts, transport.policy.AllowedHosts)
	assert.Equal(t, policy.AllowUnsafeDestinations, transport.policy.AllowUnsafeDestinations)
}

func TestErrDNSResolutionFailed_Exists(t *testing.T) {
	t.Parallel()

	assert.NotNil(t, ErrDNSResolutionFailed)
	assert.Contains(t, ErrDNSResolutionFailed.Error(), "DNS resolution failed")
}
