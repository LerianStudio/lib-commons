//go:build unit

package http

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeReverseProxy(t *testing.T) {
	t.Parallel()

	t.Run("rejects untrusted scheme", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("http://api.partner.com", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"api.partner.com"},
		}, rr, req)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUntrustedProxyScheme))
	})

	t.Run("rejects untrusted host", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://api.partner.com", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"trusted.partner.com"},
		}, rr, req)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUntrustedProxyHost))
	})

	t.Run("rejects localhost destination", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://localhost", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"localhost"},
		}, rr, req)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnsafeProxyDestination))
	})

	t.Run("proxies request when policy allows target", func(t *testing.T) {
		t.Parallel()

		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("proxied"))
		}))
		defer target.Close()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
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
		assert.Equal(t, "proxied", string(body))
	})
}

func requestHostFromURL(t *testing.T, rawURL string) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	return req.URL.Hostname()
}

func TestDefaultReverseProxyPolicy(t *testing.T) {
	t.Parallel()

	policy := DefaultReverseProxyPolicy()

	assert.Equal(t, []string{"https"}, policy.AllowedSchemes)
	assert.Nil(t, policy.AllowedHosts)
	assert.False(t, policy.AllowUnsafeDestinations)
}

func TestIsAllowedScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scheme  string
		allowed []string
		want    bool
	}{
		{"https in https list", "https", []string{"https"}, true},
		{"http in http/https list", "http", []string{"http", "https"}, true},
		{"ftp not in http/https list", "ftp", []string{"http", "https"}, false},
		{"case insensitive", "HTTPS", []string{"https"}, true},
		{"empty allowed list", "https", []string{}, false},
		{"nil allowed list", "https", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isAllowedScheme(tt.scheme, tt.allowed))
		})
	}
}

func TestIsAllowedHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		host    string
		allowed []string
		want    bool
	}{
		{"exact match", "example.com", []string{"example.com"}, true},
		{"case insensitive", "Example.COM", []string{"example.com"}, true},
		{"not in list", "evil.com", []string{"good.com"}, false},
		{"empty list", "example.com", []string{}, false},
		{"nil list", "example.com", nil, false},
		{"multiple hosts", "api.example.com", []string{"web.example.com", "api.example.com"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isAllowedHost(tt.host, tt.allowed))
		})
	}
}

func TestServeReverseProxy_NilRequest(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()

	err := ServeReverseProxy("https://example.com", DefaultReverseProxyPolicy(), rr, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilProxyRequest)
}

func TestServeReverseProxy_NilResponseWriter(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)

	err := ServeReverseProxy("https://example.com", DefaultReverseProxyPolicy(), nil, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilProxyResponse)
}

func TestServeReverseProxy_InvalidTargetURL(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	err := ServeReverseProxy("://invalid", ReverseProxyPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"invalid"},
	}, rr, req)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidProxyTarget)
}

func TestServeReverseProxy_EmptyTarget(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	err := ServeReverseProxy("", ReverseProxyPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"example.com"},
	}, rr, req)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidProxyTarget)
}

func TestServeReverseProxy_SchemeValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  string
		schemes []string
		hosts   []string
		wantErr error
	}{
		{name: "file scheme rejected (no host)", target: "file:///etc/passwd", schemes: []string{"https"}, hosts: []string{""}, wantErr: ErrInvalidProxyTarget},
		{name: "gopher scheme rejected", target: "gopher://evil.com", schemes: []string{"https"}, hosts: []string{"evil.com"}, wantErr: ErrUntrustedProxyScheme},
		{name: "ftp scheme rejected", target: "ftp://files.example.com", schemes: []string{"https"}, hosts: []string{"files.example.com"}, wantErr: ErrUntrustedProxyScheme},
		{name: "data scheme rejected", target: "data:text/html,<h1>Hello</h1>", schemes: []string{"https"}, hosts: []string{""}, wantErr: ErrInvalidProxyTarget},
		{name: "empty allowed schemes rejects everything", target: "https://example.com", schemes: []string{}, hosts: []string{"example.com"}, wantErr: ErrUntrustedProxyScheme},
		{name: "javascript scheme rejected", target: "javascript://evil.com", schemes: []string{"https"}, hosts: []string{"evil.com"}, wantErr: ErrUntrustedProxyScheme},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
			rr := httptest.NewRecorder()

			err := ServeReverseProxy(tt.target, ReverseProxyPolicy{
				AllowedSchemes: tt.schemes,
				AllowedHosts:   tt.hosts,
			}, rr, req)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestServeReverseProxy_AllowedHostEnforcement(t *testing.T) {
	t.Parallel()

	t.Run("empty allowed hosts rejects all", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://example.com", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{},
		}, rr, req)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUntrustedProxyHost)
	})

	t.Run("nil allowed hosts rejects all", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://example.com", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   nil,
		}, rr, req)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUntrustedProxyHost)
	})

	t.Run("case insensitive host matching", func(t *testing.T) {
		t.Parallel()

		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer target.Close()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
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
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "ok", string(body))
	})

	t.Run("host not in list is rejected", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://evil.com", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"trusted.com", "also-trusted.com"},
		}, rr, req)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUntrustedProxyHost)
	})
}
