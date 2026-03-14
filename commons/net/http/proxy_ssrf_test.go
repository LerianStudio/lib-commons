//go:build unit

package http

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	loopbacks := []string{"127.0.0.1", "127.0.0.2", "127.255.255.255"}

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

	privateIPs := []string{"10.0.0.1", "10.0.0.0", "10.255.255.255", "10.1.2.3"}

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

	privateIPs := []string{"172.16.0.1", "172.16.0.0", "172.31.255.255", "172.20.10.1"}

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

	privateIPs := []string{"192.168.0.1", "192.168.0.0", "192.168.255.255", "192.168.1.1"}

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

	linkLocalIPs := []string{"169.254.0.1", "169.254.169.254", "169.254.255.255"}

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
	defer func() { require.NoError(t, resp.Body.Close()) }()
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
	assert.Equal(t, "localhost-ok", string(body))
}

func TestIsUnsafeIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ip     string
		unsafe bool
	}{
		{"IPv4 loopback 127.0.0.1", "127.0.0.1", true},
		{"IPv4 loopback 127.0.0.2", "127.0.0.2", true},
		{"IPv6 loopback ::1", "::1", true},
		{"10.0.0.1", "10.0.0.1", true},
		{"10.255.255.255", "10.255.255.255", true},
		{"172.16.0.1", "172.16.0.1", true},
		{"172.31.255.255", "172.31.255.255", true},
		{"192.168.0.1", "192.168.0.1", true},
		{"192.168.255.255", "192.168.255.255", true},
		{"169.254.0.1", "169.254.0.1", true},
		{"169.254.169.254 AWS metadata", "169.254.169.254", true},
		{"0.0.0.0", "0.0.0.0", true},
		{"IPv6 unspecified ::", "::", true},
		{"IPv4-mapped loopback ::ffff:127.0.0.1", "::ffff:127.0.0.1", true},
		{"IPv4-mapped private ::ffff:10.0.0.1", "::ffff:10.0.0.1", true},
		{"Documentation 192.0.0.1", "192.0.0.1", true},
		{"Documentation 192.0.2.1", "192.0.2.1", true},
		{"IPv4-mapped documentation ::ffff:198.51.100.10", "::ffff:198.51.100.10", true},
		{"Documentation 198.51.100.10", "198.51.100.10", true},
		{"Documentation 203.0.113.10", "203.0.113.10", true},
		{"224.0.0.1", "224.0.0.1", true},
		{"239.255.255.255", "239.255.255.255", true},
		{"8.8.8.8 Google DNS", "8.8.8.8", false},
		{"1.1.1.1 Cloudflare DNS", "1.1.1.1", false},
		{"93.184.216.34 example.com", "93.184.216.34", false},
		{"CGNAT 100.64.0.1", "100.64.0.1", true},
		{"Benchmark 198.18.0.1", "198.18.0.1", true},
		{"Reserved 240.0.0.1", "240.0.0.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := parseTestIP(t, tt.ip)
			assert.Equal(t, tt.unsafe, isUnsafeIP(ip))
		})
	}
}

func parseTestIP(t *testing.T, s string) net.IP {
	t.Helper()

	ip := net.ParseIP(s)
	require.NotNil(t, ip, "failed to parse IP: %s", s)

	return ip
}
