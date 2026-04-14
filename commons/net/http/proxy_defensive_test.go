//go:build unit

package http

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	liblog "github.com/LerianStudio/lib-commons/v5/commons/log"
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

func TestValidateResolvedIPs_NoIPs(t *testing.T) {
	t.Parallel()

	ip, err := validateResolvedIPs(context.Background(), nil, "example.com", nil)
	require.Error(t, err)
	assert.Nil(t, ip)
	assert.ErrorIs(t, err, ErrNoResolvedIPs)
}

func TestSSRFSafeTransport_DNSResolutionFailure(t *testing.T) {
	t.Parallel()

	transport := newSSRFSafeTransportWithDeps(ReverseProxyPolicy{}, func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("lookup failed")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := transport.base.DialContext(ctx, "tcp", "example.com:443")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDNSResolutionFailed)
}

func TestValidateResolvedIPs_UnsafeAddressRejected(t *testing.T) {
	t.Parallel()

	ip, err := validateResolvedIPs(context.Background(), []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, "example.com", nil)
	require.Error(t, err)
	assert.Nil(t, ip)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestValidateResolvedIPs_MixedAddressesRejected(t *testing.T) {
	t.Parallel()

	ip, err := validateResolvedIPs(context.Background(), []net.IPAddr{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("127.0.0.1")},
	}, "example.com", nil)
	require.Error(t, err)
	assert.Nil(t, ip)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestValidateResolvedIPs_AllSafeReturnsFirst(t *testing.T) {
	t.Parallel()

	ip, err := validateResolvedIPs(context.Background(), []net.IPAddr{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("1.1.1.1")},
	}, "example.com", nil)
	require.NoError(t, err)
	assert.Equal(t, net.ParseIP("8.8.8.8"), ip)
}

func TestValidateResolvedIPs_TypedNilLogger(t *testing.T) {
	t.Parallel()

	var logger *typedNilProxyLogger

	assert.NotPanics(t, func() {
		ip, err := validateResolvedIPs(context.Background(), nil, "example.com", logger)
		require.Error(t, err)
		assert.Nil(t, ip)
		assert.ErrorIs(t, err, ErrNoResolvedIPs)
	})
}
