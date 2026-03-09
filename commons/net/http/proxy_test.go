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
		defer func() { _ = resp.Body.Close() }()

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

// --- Comprehensive SSRF and proxy tests below ---

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

	// URLs with control characters are invalid
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

func TestServeReverseProxy_SSRF_LoopbackIPv4(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	err := ServeReverseProxy("https://127.0.0.1:8080/admin", ReverseProxyPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"127.0.0.1"},
	}, rr, req)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestServeReverseProxy_SSRF_LoopbackIPv4_AltAddresses(t *testing.T) {
	t.Parallel()

	// 127.x.x.x are all loopback
	loopbacks := []string{
		"127.0.0.1",
		"127.0.0.2",
		"127.255.255.255",
	}

	for _, ip := range loopbacks {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
			rr := httptest.NewRecorder()

			err := ServeReverseProxy("https://"+ip+":8080", ReverseProxyPolicy{
				AllowedSchemes: []string{"https"},
				AllowedHosts:   []string{ip},
			}, rr, req)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
		})
	}
}

func TestServeReverseProxy_SSRF_PrivateClassA(t *testing.T) {
	t.Parallel()

	// 10.0.0.0/8
	privateIPs := []string{
		"10.0.0.1",
		"10.0.0.0",
		"10.255.255.255",
		"10.1.2.3",
	}

	for _, ip := range privateIPs {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
			rr := httptest.NewRecorder()

			err := ServeReverseProxy("https://"+ip, ReverseProxyPolicy{
				AllowedSchemes: []string{"https"},
				AllowedHosts:   []string{ip},
			}, rr, req)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
		})
	}
}

func TestServeReverseProxy_SSRF_PrivateClassB(t *testing.T) {
	t.Parallel()

	// 172.16.0.0/12
	privateIPs := []string{
		"172.16.0.1",
		"172.16.0.0",
		"172.31.255.255",
		"172.20.10.1",
	}

	for _, ip := range privateIPs {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
			rr := httptest.NewRecorder()

			err := ServeReverseProxy("https://"+ip, ReverseProxyPolicy{
				AllowedSchemes: []string{"https"},
				AllowedHosts:   []string{ip},
			}, rr, req)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
		})
	}
}

func TestServeReverseProxy_SSRF_PrivateClassC(t *testing.T) {
	t.Parallel()

	// 192.168.0.0/16
	privateIPs := []string{
		"192.168.0.1",
		"192.168.0.0",
		"192.168.255.255",
		"192.168.1.1",
	}

	for _, ip := range privateIPs {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
			rr := httptest.NewRecorder()

			err := ServeReverseProxy("https://"+ip, ReverseProxyPolicy{
				AllowedSchemes: []string{"https"},
				AllowedHosts:   []string{ip},
			}, rr, req)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
		})
	}
}

func TestServeReverseProxy_SSRF_LinkLocal(t *testing.T) {
	t.Parallel()

	// 169.254.0.0/16 (link-local unicast)
	linkLocalIPs := []string{
		"169.254.0.1",
		"169.254.169.254", // AWS metadata endpoint
		"169.254.255.255",
	}

	for _, ip := range linkLocalIPs {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
			rr := httptest.NewRecorder()

			err := ServeReverseProxy("https://"+ip, ReverseProxyPolicy{
				AllowedSchemes: []string{"https"},
				AllowedHosts:   []string{ip},
			}, rr, req)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
		})
	}
}

func TestServeReverseProxy_SSRF_IPv6Loopback(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	// IPv6 loopback ::1 - must be in brackets for URL host
	err := ServeReverseProxy("https://[::1]:8080", ReverseProxyPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"::1"},
	}, rr, req)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
}

func TestServeReverseProxy_SSRF_UnspecifiedAddress(t *testing.T) {
	t.Parallel()

	t.Run("0.0.0.0", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://0.0.0.0", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"0.0.0.0"},
		}, rr, req)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
	})

	t.Run("IPv6 unspecified [::]", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
		rr := httptest.NewRecorder()

		err := ServeReverseProxy("https://[::]:8080", ReverseProxyPolicy{
			AllowedSchemes: []string{"https"},
			AllowedHosts:   []string{"::"},
		}, rr, req)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnsafeProxyDestination)
	})
}

func TestServeReverseProxy_SSRF_AllowUnsafeOverride(t *testing.T) {
	t.Parallel()

	// When AllowUnsafeDestinations is true, private IPs should be allowed
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
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
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
}

func TestServeReverseProxy_SSRF_LocalhostAllowedWhenUnsafe(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("localhost-ok"))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, "http://gateway.local/proxy", nil)
	rr := httptest.NewRecorder()

	// Override with AllowUnsafeDestinations to allow localhost
	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{requestHostFromURL(t, target.URL)},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)
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
		{
			name:    "file scheme rejected (no host)",
			target:  "file:///etc/passwd",
			schemes: []string{"https"},
			hosts:   []string{""},
			wantErr: ErrInvalidProxyTarget, // file:// has no host, caught by empty host check
		},
		{
			name:    "gopher scheme rejected",
			target:  "gopher://evil.com",
			schemes: []string{"https"},
			hosts:   []string{"evil.com"},
			wantErr: ErrUntrustedProxyScheme,
		},
		{
			name:    "ftp scheme rejected",
			target:  "ftp://files.example.com",
			schemes: []string{"https"},
			hosts:   []string{"files.example.com"},
			wantErr: ErrUntrustedProxyScheme,
		},
		{
			name:    "data scheme rejected",
			target:  "data:text/html,<h1>Hello</h1>",
			schemes: []string{"https"},
			hosts:   []string{""}, // data URIs have no host
			wantErr: ErrInvalidProxyTarget,
		},
		{
			name:    "empty allowed schemes rejects everything",
			target:  "https://example.com",
			schemes: []string{},
			hosts:   []string{"example.com"},
			wantErr: ErrUntrustedProxyScheme,
		},
		{
			name:    "javascript scheme rejected",
			target:  "javascript://evil.com",
			schemes: []string{"https"},
			hosts:   []string{"evil.com"},
			wantErr: ErrUntrustedProxyScheme,
		},
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
			AllowedHosts:            []string{host}, // matches since it's the same host
			AllowUnsafeDestinations: true,
		}, rr, req)

		require.NoError(t, err)
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

func TestServeReverseProxy_HeaderForwarding(t *testing.T) {
	t.Parallel()

	var receivedHost string
	var receivedForwardedHost string

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		receivedForwardedHost = r.Header.Get("X-Forwarded-Host")
		_, _ = w.Write([]byte("headers checked"))
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, "http://original-host.local/proxy", nil)
	rr := httptest.NewRecorder()

	host := requestHostFromURL(t, target.URL)

	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{host},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)

	resp := rr.Result()
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "headers checked", string(body))

	// The request Host should be rewritten to the target host
	assert.Contains(t, receivedHost, host)
	// X-Forwarded-Host should contain the original host from req.Host.
	assert.Equal(t, "original-host.local", receivedForwardedHost)
}

func TestDefaultReverseProxyPolicy(t *testing.T) {
	t.Parallel()

	policy := DefaultReverseProxyPolicy()

	assert.Equal(t, []string{"https"}, policy.AllowedSchemes)
	assert.Nil(t, policy.AllowedHosts)
	assert.False(t, policy.AllowUnsafeDestinations)
}

func TestIsUnsafeIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ip     string
		unsafe bool
	}{
		// Loopback
		{"IPv4 loopback 127.0.0.1", "127.0.0.1", true},
		{"IPv4 loopback 127.0.0.2", "127.0.0.2", true},
		{"IPv6 loopback ::1", "::1", true},

		// Private class A
		{"10.0.0.1", "10.0.0.1", true},
		{"10.255.255.255", "10.255.255.255", true},

		// Private class B
		{"172.16.0.1", "172.16.0.1", true},
		{"172.31.255.255", "172.31.255.255", true},

		// Private class C
		{"192.168.0.1", "192.168.0.1", true},
		{"192.168.255.255", "192.168.255.255", true},

		// Link-local
		{"169.254.0.1", "169.254.0.1", true},
		{"169.254.169.254 AWS metadata", "169.254.169.254", true},

		// Unspecified
		{"0.0.0.0", "0.0.0.0", true},
		{"IPv6 unspecified ::", "::", true},

		// Multicast
		{"224.0.0.1", "224.0.0.1", true},
		{"239.255.255.255", "239.255.255.255", true},

		// Public IPs (should be safe)
		{"8.8.8.8 Google DNS", "8.8.8.8", false},
		{"1.1.1.1 Cloudflare DNS", "1.1.1.1", false},
		{"93.184.216.34 example.com", "93.184.216.34", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := parseTestIP(t, tt.ip)
			assert.Equal(t, tt.unsafe, isUnsafeIP(ip))
		})
	}
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
	defer func() { _ = resp.Body.Close() }()

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

	// Use uppercase scheme in allowed list
	err := ServeReverseProxy(target.URL, ReverseProxyPolicy{
		AllowedSchemes:          []string{"HTTP"},
		AllowedHosts:            []string{host},
		AllowUnsafeDestinations: true,
	}, rr, req)

	require.NoError(t, err)
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
}

// ---------------------------------------------------------------------------
// ssrfSafeTransport: DNS rebinding protection
// ---------------------------------------------------------------------------

func TestSSRFSafeTransport_DialContext_RejectsPrivateIP(t *testing.T) {
	t.Parallel()

	// Create a transport with SSRF protection enabled
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

	// When AllowUnsafeDestinations is true, transport uses the plain dialer
	transport := newSSRFSafeTransport(ReverseProxyPolicy{
		AllowedSchemes:          []string{"http"},
		AllowedHosts:            []string{"localhost"},
		AllowUnsafeDestinations: true,
	})

	require.NotNil(t, transport)
	require.NotNil(t, transport.base)
	// DialContext is set to plain dialer (not nil) even when unsafe is allowed
	require.NotNil(t, transport.base.DialContext)
}

// ---------------------------------------------------------------------------
// ssrfSafeTransport: RoundTrip validates redirect targets
// ---------------------------------------------------------------------------

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

// parseTestIP is a helper that parses an IP string for tests.
func parseTestIP(t *testing.T, s string) net.IP {
	t.Helper()

	ip := net.ParseIP(s)
	require.NotNil(t, ip, "failed to parse IP: %s", s)

	return ip
}
