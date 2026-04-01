//go:build unit

package ssrf

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// BlockedPrefixes
// ---------------------------------------------------------------------------

// expectedPrefixCount lives in the test file so the production code does not
// export or carry a constant that is only meaningful for test assertions.
// Update this value when adding new CIDR ranges to blockedPrefixes.
const expectedPrefixCount = 10

func TestBlockedPrefixes_ReturnsExpectedCount(t *testing.T) {
	t.Parallel()

	prefixes := BlockedPrefixes()
	assert.Len(t, prefixes, expectedPrefixCount,
		"BlockedPrefixes() should return exactly %d prefixes", expectedPrefixCount)
}

func TestBlockedPrefixes_ReturnsCopy(t *testing.T) {
	t.Parallel()

	a := BlockedPrefixes()
	b := BlockedPrefixes()

	// Mutate the first slice and verify the second is unaffected.
	a[0] = netip.MustParsePrefix("255.255.255.255/32")
	assert.NotEqual(t, a[0], b[0],
		"BlockedPrefixes() must return independent copies")
}

func TestBlockedPrefixes_ContainsExpectedRanges(t *testing.T) {
	t.Parallel()

	expected := []string{
		"0.0.0.0/8",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"2001:db8::/32",
		"100::/64",
	}

	prefixes := BlockedPrefixes()
	strs := make([]string, len(prefixes))

	for i, p := range prefixes {
		strs[i] = p.String()
	}

	for _, exp := range expected {
		assert.Contains(t, strs, exp,
			"BlockedPrefixes() should contain %s", exp)
	}
}

// ---------------------------------------------------------------------------
// IsBlockedAddr — netip.Addr interface
// ---------------------------------------------------------------------------

func TestIsBlockedAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		blocked bool
	}{
		// --- Loopback ---
		{name: "IPv4 loopback 127.0.0.1", addr: "127.0.0.1", blocked: true},
		{name: "IPv4 loopback 127.0.0.2", addr: "127.0.0.2", blocked: true},
		{name: "IPv4 loopback 127.255.255.255", addr: "127.255.255.255", blocked: true},
		{name: "IPv6 loopback ::1", addr: "::1", blocked: true},

		// --- RFC 1918 private ranges ---
		{name: "10.0.0.1 (Class A)", addr: "10.0.0.1", blocked: true},
		{name: "10.255.255.255 (Class A top)", addr: "10.255.255.255", blocked: true},
		{name: "172.16.0.1 (Class B)", addr: "172.16.0.1", blocked: true},
		{name: "172.31.255.255 (Class B top)", addr: "172.31.255.255", blocked: true},
		{name: "192.168.0.1 (Class C)", addr: "192.168.0.1", blocked: true},
		{name: "192.168.255.255 (Class C top)", addr: "192.168.255.255", blocked: true},

		// --- Link-local ---
		{name: "IPv4 link-local 169.254.1.1", addr: "169.254.1.1", blocked: true},
		{name: "IPv4 link-local AWS metadata", addr: "169.254.169.254", blocked: true},
		{name: "IPv6 link-local unicast fe80::1", addr: "fe80::1", blocked: true},

		// --- Link-local multicast ---
		{name: "IPv4 multicast 224.0.0.1", addr: "224.0.0.1", blocked: true},
		{name: "IPv4 multicast 239.255.255.255", addr: "239.255.255.255", blocked: true},

		// --- Unspecified ---
		{name: "IPv4 unspecified 0.0.0.0", addr: "0.0.0.0", blocked: true},
		{name: "IPv6 unspecified ::", addr: "::", blocked: true},

		// --- CGNAT (RFC 6598): 100.64.0.0/10 ---
		{name: "CGNAT low 100.64.0.1", addr: "100.64.0.1", blocked: true},
		{name: "CGNAT mid 100.100.100.100", addr: "100.100.100.100", blocked: true},
		{name: "CGNAT high 100.127.255.254", addr: "100.127.255.254", blocked: true},

		// --- "this network" (RFC 1122): 0.0.0.0/8 ---
		{name: "0.0.0.1 this-network", addr: "0.0.0.1", blocked: true},
		{name: "0.255.255.255 this-network top", addr: "0.255.255.255", blocked: true},

		// --- IETF protocol assignments (RFC 6890): 192.0.0.0/24 ---
		{name: "192.0.0.1 IETF", addr: "192.0.0.1", blocked: true},
		{name: "192.0.0.254 IETF top", addr: "192.0.0.254", blocked: true},

		// --- TEST-NET-1 (RFC 5737): 192.0.2.0/24 ---
		{name: "192.0.2.1 TEST-NET-1", addr: "192.0.2.1", blocked: true},

		// --- Benchmarking (RFC 2544): 198.18.0.0/15 ---
		{name: "198.18.0.1 benchmarking", addr: "198.18.0.1", blocked: true},
		{name: "198.19.255.255 benchmarking top", addr: "198.19.255.255", blocked: true},

		// --- TEST-NET-2 (RFC 5737): 198.51.100.0/24 ---
		{name: "198.51.100.1 TEST-NET-2", addr: "198.51.100.1", blocked: true},
		{name: "198.51.100.10 TEST-NET-2", addr: "198.51.100.10", blocked: true},

		// --- TEST-NET-3 (RFC 5737): 203.0.113.0/24 ---
		{name: "203.0.113.1 TEST-NET-3", addr: "203.0.113.1", blocked: true},
		{name: "203.0.113.10 TEST-NET-3", addr: "203.0.113.10", blocked: true},

		// --- Reserved (RFC 1112): 240.0.0.0/4 ---
		{name: "240.0.0.1 reserved", addr: "240.0.0.1", blocked: true},
		{name: "255.255.255.254 reserved top", addr: "255.255.255.254", blocked: true},

		// --- IPv4-mapped IPv6 (must unmap and check) ---
		{name: "IPv4-mapped loopback ::ffff:127.0.0.1", addr: "::ffff:127.0.0.1", blocked: true},
		{name: "IPv4-mapped private ::ffff:10.0.0.1", addr: "::ffff:10.0.0.1", blocked: true},
		{name: "IPv4-mapped CGNAT ::ffff:100.64.0.1", addr: "::ffff:100.64.0.1", blocked: true},
		{name: "IPv4-mapped TEST-NET-2 ::ffff:198.51.100.10", addr: "::ffff:198.51.100.10", blocked: true},

		// --- Public IPs — must NOT be blocked ---
		{name: "Google DNS 8.8.8.8", addr: "8.8.8.8", blocked: false},
		{name: "Cloudflare 1.1.1.1", addr: "1.1.1.1", blocked: false},
		{name: "Public IPv6 2001:4860:4860::8888", addr: "2001:4860:4860::8888", blocked: false},
		{name: "93.184.216.34 (example.com)", addr: "93.184.216.34", blocked: false},

		// --- Boundary cases — just outside blocked ranges ---
		{name: "100.63.255.255 below CGNAT", addr: "100.63.255.255", blocked: false},
		{name: "100.128.0.1 above CGNAT", addr: "100.128.0.1", blocked: false},
		{name: "172.15.255.255 below Class B", addr: "172.15.255.255", blocked: false},
		{name: "172.32.0.1 above Class B", addr: "172.32.0.1", blocked: false},
		{name: "1.0.0.1 above this-network", addr: "1.0.0.1", blocked: false},
		{name: "198.17.255.255 below benchmarking", addr: "198.17.255.255", blocked: false},
		{name: "198.20.0.1 above benchmarking", addr: "198.20.0.1", blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr := netip.MustParseAddr(tt.addr)
			assert.Equal(t, tt.blocked, IsBlockedAddr(addr),
				"IsBlockedAddr(%s) = %v, want %v", tt.addr, IsBlockedAddr(addr), tt.blocked)
		})
	}
}

func TestIsBlockedAddr_ZeroValue(t *testing.T) {
	t.Parallel()

	// The zero-value netip.Addr{} is invalid and must be blocked (fail-closed).
	var zero netip.Addr
	assert.True(t, IsBlockedAddr(zero), "IsBlockedAddr(netip.Addr{}) must return true (fail-closed)")
}

// ---------------------------------------------------------------------------
// IsBlockedIP — legacy net.IP interface
// ---------------------------------------------------------------------------

func TestIsBlockedIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		{name: "IPv4 loopback", ip: "127.0.0.1", blocked: true},
		{name: "IPv6 loopback", ip: "::1", blocked: true},
		{name: "RFC 1918 10.x", ip: "10.0.0.1", blocked: true},
		{name: "RFC 1918 172.16.x", ip: "172.16.0.1", blocked: true},
		{name: "RFC 1918 192.168.x", ip: "192.168.0.1", blocked: true},
		{name: "Link-local", ip: "169.254.1.1", blocked: true},
		{name: "CGNAT", ip: "100.64.0.1", blocked: true},
		{name: "TEST-NET-1", ip: "192.0.2.1", blocked: true},
		{name: "Multicast", ip: "224.0.0.1", blocked: true},
		{name: "Google DNS", ip: "8.8.8.8", blocked: false},
		{name: "Cloudflare", ip: "1.1.1.1", blocked: false},
		{name: "Public IPv6", ip: "2001:4860:4860::8888", blocked: false},
		// IPv4-mapped IPv6
		{name: "IPv4-mapped loopback", ip: "::ffff:127.0.0.1", blocked: true},
		{name: "IPv4-mapped private", ip: "::ffff:10.0.0.1", blocked: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "failed to parse IP: %s", tt.ip)
			assert.Equal(t, tt.blocked, IsBlockedIP(ip),
				"IsBlockedIP(%s) = %v, want %v", tt.ip, IsBlockedIP(ip), tt.blocked)
		})
	}
}

func TestIsBlockedIP_NilIP(t *testing.T) {
	t.Parallel()

	assert.True(t, IsBlockedIP(nil), "IsBlockedIP(nil) must return true (fail-closed)")
}

// ---------------------------------------------------------------------------
// IsBlockedHostname
// ---------------------------------------------------------------------------

func TestIsBlockedHostname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		host    string
		blocked bool
	}{
		// Exact blocked hostnames
		{name: "localhost", host: "localhost", blocked: true},
		{name: "LOCALHOST uppercase", host: "LOCALHOST", blocked: true},
		{name: "Localhost mixed", host: "Localhost", blocked: true},
		{name: "metadata.google.internal", host: "metadata.google.internal", blocked: true},
		{name: "metadata.gcp.internal", host: "metadata.gcp.internal", blocked: true},
		{name: "AWS metadata IP", host: "169.254.169.254", blocked: true},

		// Dangerous suffixes
		{name: ".local suffix", host: "myhost.local", blocked: true},
		{name: ".internal suffix", host: "service.internal", blocked: true},
		{name: ".cluster.local suffix", host: "api.default.svc.cluster.local", blocked: true},
		{name: ".INTERNAL uppercase", host: "service.INTERNAL", blocked: true},

		// Empty hostname
		{name: "empty string", host: "", blocked: true},

		// Legitimate hostnames — must NOT be blocked
		{name: "example.com", host: "example.com", blocked: false},
		{name: "api.example.com", host: "api.example.com", blocked: false},
		{name: "hooks.stripe.com", host: "hooks.stripe.com", blocked: false},
		{name: "8.8.8.8 public", host: "8.8.8.8", blocked: false},
		{name: "2001:4860:4860::8888", host: "2001:4860:4860::8888", blocked: false},

		// Tricky near-misses that must NOT be blocked
		{name: "localhostx not blocked", host: "localhostx", blocked: false},
		{name: "mylocal not blocked", host: "mylocal", blocked: false},
		{name: "internal.example.com not blocked", host: "internal.example.com", blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.blocked, IsBlockedHostname(tt.host),
				"IsBlockedHostname(%q) = %v, want %v", tt.host, IsBlockedHostname(tt.host), tt.blocked)
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateURL
// ---------------------------------------------------------------------------

func TestValidateURL_ValidPublicURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "https example.com", url: "https://example.com/hook"},
		{name: "http example.com", url: "http://example.com/hook"},
		{name: "https with port", url: "https://example.com:8443/hook"},
		{name: "https with path and query", url: "https://example.com/a/b?c=d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateURL(context.Background(), tt.url)
			assert.NoError(t, err)
		})
	}
}

func TestValidateURL_BlockedSchemes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "gopher", url: "gopher://evil.com"},
		{name: "file", url: "file:///etc/passwd"},
		{name: "ftp", url: "ftp://example.com/file"},
		{name: "javascript", url: "javascript:alert(1)"},
		{name: "data", url: "data:text/html,<h1>hi</h1>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateURL(context.Background(), tt.url)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}
}

func TestValidateURL_BlockedHostnames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "localhost", url: "http://localhost/hook"},
		{name: "metadata.google.internal", url: "https://metadata.google.internal/computeMetadata"},
		{name: ".local suffix", url: "http://printer.local/status"},
		{name: "k8s internal", url: "http://api.default.svc.cluster.local/health"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateURL(context.Background(), tt.url)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}
}

func TestValidateURL_BlockedIPLiteral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "loopback", url: "http://127.0.0.1/hook"},
		{name: "private 10.x", url: "https://10.0.0.1/hook"},
		{name: "private 192.168.x", url: "https://192.168.1.1/hook"},
		{name: "CGNAT", url: "http://100.64.0.1/hook"},
		{name: "IPv6 loopback", url: "http://[::1]/hook"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateURL(context.Background(), tt.url)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}
}

func TestValidateURL_EmptyHostname(t *testing.T) {
	t.Parallel()

	err := ValidateURL(context.Background(), "http://")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

func TestValidateURL_MalformedURL(t *testing.T) {
	t.Parallel()

	err := ValidateURL(context.Background(), "://missing-scheme")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

func TestValidateURL_NilContext(t *testing.T) {
	t.Parallel()

	err := ValidateURL(nil, "https://example.com/hook")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
	assert.Contains(t, err.Error(), "nil context")
}

func TestValidateURL_HTTPSOnly(t *testing.T) {
	t.Parallel()

	t.Run("https allowed", func(t *testing.T) {
		t.Parallel()

		err := ValidateURL(context.Background(), "https://example.com/hook", WithHTTPSOnly())
		assert.NoError(t, err)
	})

	t.Run("http rejected", func(t *testing.T) {
		t.Parallel()

		err := ValidateURL(context.Background(), "http://example.com/hook", WithHTTPSOnly())
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlocked)
		assert.Contains(t, err.Error(), "HTTPS only")
	})
}

func TestValidateURL_AllowPrivateNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "loopback IP", url: "http://127.0.0.1/hook"},
		{name: "private IP", url: "http://10.0.0.1/hook"},
		{name: "CGNAT IP", url: "http://100.64.0.1/hook"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateURL(context.Background(), tt.url, WithAllowPrivateNetwork())
			// AllowPrivateNetwork bypasses IP blocking but hostname blocking
			// is still evaluated. Since these are IP literals (not "localhost"),
			// they should pass.
			assert.NoError(t, err)
		})
	}
}

func TestValidateURL_AllowHostname(t *testing.T) {
	t.Parallel()

	t.Run("exempted hostname passes", func(t *testing.T) {
		t.Parallel()

		err := ValidateURL(context.Background(), "http://service.internal/hook",
			WithAllowHostname("service.internal"))
		assert.NoError(t, err)
	})

	t.Run("non-exempted hostname still blocked", func(t *testing.T) {
		t.Parallel()

		err := ValidateURL(context.Background(), "http://other.internal/hook",
			WithAllowHostname("service.internal"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlocked)
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()

		err := ValidateURL(context.Background(), "http://Service.Internal/hook",
			WithAllowHostname("service.internal"))
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// ResolveAndValidate
// ---------------------------------------------------------------------------

// fakeLookup returns a LookupFunc that yields the given IPs.
func fakeLookup(ips ...string) LookupFunc {
	return func(_ context.Context, _ string) ([]string, error) {
		return ips, nil
	}
}

// failLookup returns a LookupFunc that always fails.
func failLookup() LookupFunc {
	return func(_ context.Context, _ string) ([]string, error) {
		return nil, errors.New("simulated DNS failure")
	}
}

func TestResolveAndValidate_PublicIP(t *testing.T) {
	t.Parallel()

	result, err := ResolveAndValidate(context.Background(),
		"https://example.com:8443/hook",
		WithLookupFunc(fakeLookup("93.184.216.34")),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "https://93.184.216.34:8443/hook", result.PinnedURL)
	assert.Equal(t, "example.com:8443", result.Authority)
	assert.Equal(t, "example.com", result.SNIHostname)
}

func TestResolveAndValidate_PublicIPNoPort(t *testing.T) {
	t.Parallel()

	result, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("93.184.216.34")),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "https://93.184.216.34/hook", result.PinnedURL)
	assert.Equal(t, "example.com", result.Authority)
	assert.Equal(t, "example.com", result.SNIHostname)
}

func TestResolveAndValidate_IPv6Public(t *testing.T) {
	t.Parallel()

	result, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("2001:4860:4860::8888")),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "https://[2001:4860:4860::8888]/hook", result.PinnedURL)
	assert.Equal(t, "example.com", result.Authority)
	assert.Equal(t, "example.com", result.SNIHostname)
}

func TestResolveAndValidate_BlockedIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   string
	}{
		{name: "loopback", ip: "127.0.0.1"},
		{name: "private 10.x", ip: "10.0.0.1"},
		{name: "private 192.168.x", ip: "192.168.1.1"},
		{name: "CGNAT", ip: "100.64.0.1"},
		{name: "link-local", ip: "169.254.169.254"},
		{name: "TEST-NET-1", ip: "192.0.2.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ResolveAndValidate(context.Background(),
				"https://example.com/hook",
				WithLookupFunc(fakeLookup(tt.ip)),
			)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}
}

func TestResolveAndValidate_DNSFailure(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(failLookup()),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDNSFailed)
}

func TestResolveAndValidate_DNSEmptyResult(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup()),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDNSFailed)
}

func TestResolveAndValidate_BlockedScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "gopher", url: "gopher://evil.com"},
		{name: "file", url: "file:///etc/passwd"},
		{name: "ftp", url: "ftp://example.com/file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ResolveAndValidate(context.Background(), tt.url,
				WithLookupFunc(fakeLookup("93.184.216.34")),
			)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}
}

func TestResolveAndValidate_BlockedHostname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "localhost", url: "http://localhost/hook"},
		{name: "metadata.google.internal", url: "https://metadata.google.internal/computeMetadata"},
		{name: ".local suffix", url: "http://printer.local/status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ResolveAndValidate(context.Background(), tt.url,
				WithLookupFunc(fakeLookup("93.184.216.34")),
			)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}
}

func TestResolveAndValidate_EmptyHostname(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(context.Background(), "http://",
		WithLookupFunc(fakeLookup("93.184.216.34")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

func TestResolveAndValidate_MalformedURL(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(context.Background(), "://no-scheme",
		WithLookupFunc(fakeLookup("93.184.216.34")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

func TestResolveAndValidate_NilContext(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(nil, "https://example.com/hook",
		WithLookupFunc(fakeLookup("93.184.216.34")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
	assert.Contains(t, err.Error(), "nil context")
}

func TestResolveAndValidate_CanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveAndValidate(ctx, "https://example.com/hook",
		WithLookupFunc(fakeLookup("93.184.216.34")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestResolveAndValidate_HTTPSOnly(t *testing.T) {
	t.Parallel()

	t.Run("https passes", func(t *testing.T) {
		t.Parallel()

		result, err := ResolveAndValidate(context.Background(),
			"https://example.com/hook",
			WithLookupFunc(fakeLookup("93.184.216.34")),
			WithHTTPSOnly(),
		)

		require.NoError(t, err)
		assert.Contains(t, result.PinnedURL, "https://")
	})

	t.Run("http rejected", func(t *testing.T) {
		t.Parallel()

		_, err := ResolveAndValidate(context.Background(),
			"http://example.com/hook",
			WithLookupFunc(fakeLookup("93.184.216.34")),
			WithHTTPSOnly(),
		)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlocked)
	})
}

func TestResolveAndValidate_AllowPrivateNetwork(t *testing.T) {
	t.Parallel()

	result, err := ResolveAndValidate(context.Background(),
		"http://example.com/hook",
		WithLookupFunc(fakeLookup("10.0.0.1")),
		WithAllowPrivateNetwork(),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "http://10.0.0.1/hook", result.PinnedURL)
}

func TestResolveAndValidate_AllowHostname(t *testing.T) {
	t.Parallel()

	result, err := ResolveAndValidate(context.Background(),
		"http://service.internal/hook",
		WithLookupFunc(fakeLookup("93.184.216.34")),
		WithAllowHostname("service.internal"),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "http://93.184.216.34/hook", result.PinnedURL)
	assert.Equal(t, "service.internal", result.SNIHostname)
}

func TestResolveAndValidate_MultipleIPs_FirstSafe(t *testing.T) {
	t.Parallel()

	// First IP is safe — should be selected for pinning.
	result, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("93.184.216.34", "1.1.1.1")),
	)

	require.NoError(t, err)
	assert.Equal(t, "https://93.184.216.34/hook", result.PinnedURL)
}

func TestResolveAndValidate_MultipleIPs_BlockedAmongSafe(t *testing.T) {
	t.Parallel()

	// First IP is blocked — fail-closed: reject the entire request.
	_, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("10.0.0.1", "93.184.216.34")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlocked)
}

func TestResolveAndValidate_AllIPsBlocked(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("127.0.0.1", "10.0.0.1", "192.168.1.1")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlocked)
}

func TestResolveAndValidate_UnparseableIPSkipped(t *testing.T) {
	t.Parallel()

	// DNS returns an unparseable entry followed by a valid one.
	result, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("not-an-ip", "93.184.216.34")),
	)

	require.NoError(t, err)
	assert.Equal(t, "https://93.184.216.34/hook", result.PinnedURL)
}

func TestResolveAndValidate_AllUnparseable(t *testing.T) {
	t.Parallel()

	_, err := ResolveAndValidate(context.Background(),
		"https://example.com/hook",
		WithLookupFunc(fakeLookup("not-an-ip", "also-not-an-ip")),
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

// ---------------------------------------------------------------------------
// Options composition
// ---------------------------------------------------------------------------

func TestOptions_MultipleAllowHostname(t *testing.T) {
	t.Parallel()

	opts := []Option{
		WithAllowHostname("a.internal"),
		WithAllowHostname("b.internal"),
		WithLookupFunc(fakeLookup("93.184.216.34")),
	}

	t.Run("first allowed", func(t *testing.T) {
		t.Parallel()

		result, err := ResolveAndValidate(context.Background(), "http://a.internal/hook", opts...)
		require.NoError(t, err)
		assert.Equal(t, "a.internal", result.SNIHostname)
	})

	t.Run("second allowed", func(t *testing.T) {
		t.Parallel()

		result, err := ResolveAndValidate(context.Background(), "http://b.internal/hook", opts...)
		require.NoError(t, err)
		assert.Equal(t, "b.internal", result.SNIHostname)
	})

	t.Run("third still blocked", func(t *testing.T) {
		t.Parallel()

		_, err := ResolveAndValidate(context.Background(), "http://c.internal/hook", opts...)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlocked)
	})
}

// ---------------------------------------------------------------------------
// validateScheme (internal, tested via ValidateURL/ResolveAndValidate above,
// but explicit coverage for edge cases)
// ---------------------------------------------------------------------------

func TestValidateScheme_CaseInsensitive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scheme string
	}{
		{name: "HTTP uppercase", scheme: "HTTP"},
		{name: "HTTPS uppercase", scheme: "HTTPS"},
		{name: "Mixed Http", scheme: "Http"},
		{name: "Mixed hTtPs", scheme: "hTtPs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateScheme(tt.scheme, &config{})
			assert.NoError(t, err, "scheme %q must be allowed", tt.scheme)
		})
	}
}

func TestValidateScheme_EmptyScheme(t *testing.T) {
	t.Parallel()

	err := validateScheme("", &config{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlocked)
}

// ---------------------------------------------------------------------------
// isBlockedHostnameWithConfig
// ---------------------------------------------------------------------------

func TestIsBlockedHostnameWithConfig_NilConfig(t *testing.T) {
	t.Parallel()

	// nil config should behave like IsBlockedHostname.
	assert.True(t, isBlockedHostnameWithConfig("localhost", nil))
	assert.False(t, isBlockedHostnameWithConfig("example.com", nil))
}

func TestIsBlockedHostnameWithConfig_AllowOverride(t *testing.T) {
	t.Parallel()

	cfg := &config{
		allowedHostnames: map[string]bool{
			"special.internal": true,
		},
	}

	assert.False(t, isBlockedHostnameWithConfig("special.internal", cfg),
		"allowed hostname should not be blocked")
	assert.True(t, isBlockedHostnameWithConfig("other.internal", cfg),
		"non-allowed .internal hostname should still be blocked")
}

// ---------------------------------------------------------------------------
// Error sentinel identity
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// IPv6 special-purpose ranges — documentation and discard
// ---------------------------------------------------------------------------

// TestIsBlockedAddr_IPv6_DocumentationRange verifies that addresses in the
// IPv6 documentation range 2001:db8::/32 (RFC 3849) are blocked.
func TestIsBlockedAddr_IPv6_DocumentationRange(t *testing.T) {
	t.Parallel()

	addr := netip.MustParseAddr("2001:db8::1")
	assert.True(t, IsBlockedAddr(addr),
		"IPv6 documentation address 2001:db8::1 must be blocked (RFC 3849)")
}

// TestIsBlockedAddr_IPv6_DiscardRange verifies that addresses in the
// IPv6 discard-only range 100::/64 (RFC 6666) are blocked.
func TestIsBlockedAddr_IPv6_DiscardRange(t *testing.T) {
	t.Parallel()

	addr := netip.MustParseAddr("100::1")
	assert.True(t, IsBlockedAddr(addr),
		"IPv6 discard-only address 100::1 must be blocked (RFC 6666)")
}

func TestSentinelErrors_AreDistinct(t *testing.T) {
	t.Parallel()

	assert.NotErrorIs(t, ErrBlocked, ErrInvalidURL)
	assert.NotErrorIs(t, ErrBlocked, ErrDNSFailed)
	assert.NotErrorIs(t, ErrInvalidURL, ErrDNSFailed)
}
