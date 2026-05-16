//go:build unit

package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	liblog "github.com/LerianStudio/lib-observability/log"
	libSSRF "github.com/LerianStudio/lib-commons/v5/commons/security/ssrf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type typedNilProxyResponseWriter struct{}

func (*typedNilProxyResponseWriter) Header() http.Header {
	panic("typedNilProxyResponseWriter should not be used")
}

func (*typedNilProxyResponseWriter) Write([]byte) (int, error) {
	panic("typedNilProxyResponseWriter should not be used")
}

func (*typedNilProxyResponseWriter) WriteHeader(int) {
	panic("typedNilProxyResponseWriter should not be used")
}

type typedNilProxyLogger struct{}

func (*typedNilProxyLogger) Log(context.Context, liblog.Level, string, ...liblog.Field) {
	panic("typedNilProxyLogger should not be used")
}

func (*typedNilProxyLogger) With(...liblog.Field) liblog.Logger {
	panic("typedNilProxyLogger should not be used")
}

func (*typedNilProxyLogger) WithGroup(string) liblog.Logger {
	panic("typedNilProxyLogger should not be used")
}

func (*typedNilProxyLogger) Enabled(liblog.Level) bool {
	panic("typedNilProxyLogger should not be used")
}

func (*typedNilProxyLogger) Sync(context.Context) error {
	panic("typedNilProxyLogger should not be used")
}

func TestServeReverseProxy_NilRequestURL(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	req.URL = nil
	rr := httptest.NewRecorder()

	err := ServeReverseProxy("https://example.com", ReverseProxyPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"example.com"},
	}, rr, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilProxyRequestURL)
}

func TestServeReverseProxy_NilHeaderMap(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	req.Header = nil
	rr := httptest.NewRecorder()

	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{requestHostFromURL(t, target.URL)},
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

func TestServeReverseProxy_TypedNilResponseWriter(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	var res *typedNilProxyResponseWriter

	err := ServeReverseProxy("https://example.com", ReverseProxyPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"example.com"},
	}, res, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilProxyResponse)
}

func TestServeReverseProxy_TypedNilLoggerOnValidationError(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()
	var logger *typedNilProxyLogger

	assert.NotPanics(t, func() {
		err := ServeReverseProxy("http://example.com", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"example.com"},
			Logger:         logger,
		}, rr, req)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUntrustedProxyScheme)
	})
}

func TestSSRFSafeTransport_DNSResolutionFailure(t *testing.T) {
	t.Parallel()

	lookupFail := libSSRF.WithLookupFunc(func(_ context.Context, _ string) ([]string, error) {
		return nil, errors.New("lookup failed")
	})

	transport := newSSRFSafeTransportWithOpts(
		ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"example.com"},
		},
		[]libSSRF.Option{lookupFail},
	)

	req := httptest.NewRequest(http.MethodGet, "https://example.com/path", nil)

	_, err := transport.RoundTrip(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDNSResolutionFailed)
}

func TestSSRFSafeTransport_UnsafeAddressRejected(t *testing.T) {
	t.Parallel()

	lookupLoopback := libSSRF.WithLookupFunc(func(_ context.Context, _ string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	})

	transport := newSSRFSafeTransportWithOpts(
		ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"example.com"},
		},
		[]libSSRF.Option{lookupLoopback},
	)

	req := httptest.NewRequest(http.MethodGet, "https://example.com/path", nil)

	_, err := transport.RoundTrip(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestSSRFSafeTransport_MixedAddressesRejected(t *testing.T) {
	t.Parallel()

	lookupMixed := libSSRF.WithLookupFunc(func(_ context.Context, _ string) ([]string, error) {
		return []string{"8.8.8.8", "127.0.0.1"}, nil
	})

	transport := newSSRFSafeTransportWithOpts(
		ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"example.com"},
		},
		[]libSSRF.Option{lookupMixed},
	)

	req := httptest.NewRequest(http.MethodGet, "https://example.com/path", nil)

	_, err := transport.RoundTrip(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestSSRFSafeTransport_SafeAddressPins(t *testing.T) {
	t.Parallel()

	lookupSafe := libSSRF.WithLookupFunc(func(_ context.Context, _ string) ([]string, error) {
		return []string{"93.184.216.34"}, nil
	})

	transport := newSSRFSafeTransportWithOpts(
		ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"example.com"},
		},
		[]libSSRF.Option{lookupSafe},
	)

	req := httptest.NewRequest(http.MethodGet, "https://example.com/path", nil)

	// The RoundTrip will attempt to actually dial the pinned IP which will
	// fail (no server listening), but that's a transport error, not an SSRF
	// rejection. The key assertion is that we do NOT get an SSRF error.
	_, err := transport.RoundTrip(req)
	// The connection will fail at the TCP level, but it must NOT be an SSRF error.
	if err != nil {
		assert.NotErrorIs(t, err, ErrUnsafeProxyDestination)
		assert.NotErrorIs(t, err, ErrDNSResolutionFailed)
		assert.NotErrorIs(t, err, ErrInvalidProxyTarget)
	}
}

func TestSSRFSafeTransport_TypedNilLoggerOnSSRFFailure(t *testing.T) {
	t.Parallel()

	var logger *typedNilProxyLogger

	lookupLoopback := libSSRF.WithLookupFunc(func(_ context.Context, _ string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	})

	transport := newSSRFSafeTransportWithOpts(
		ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"example.com"},
			Logger:         logger,
		},
		[]libSSRF.Option{lookupLoopback},
	)

	req := httptest.NewRequest(http.MethodGet, "https://example.com/path", nil)

	assert.NotPanics(t, func() {
		_, err := transport.RoundTrip(req)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
	})
}
