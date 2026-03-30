//go:build unit

package webhook

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// isPrivateIP — pure function, no DNS, safe to unit-test exhaustively.
// ---------------------------------------------------------------------------

func TestIsPrivateIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ip      string
		private bool
	}{
		// Loopback
		{name: "IPv4 loopback", ip: "127.0.0.1", private: true},
		{name: "IPv6 loopback", ip: "::1", private: true},

		// RFC 1918 private ranges
		{name: "10.0.0.0/8", ip: "10.0.0.1", private: true},
		{name: "10.255.255.255", ip: "10.255.255.255", private: true},
		{name: "172.16.0.0/12", ip: "172.16.0.1", private: true},
		{name: "172.31.255.255", ip: "172.31.255.255", private: true},
		{name: "192.168.0.0/16", ip: "192.168.0.1", private: true},
		{name: "192.168.255.255", ip: "192.168.255.255", private: true},

		// Link-local unicast
		{name: "IPv4 link-local", ip: "169.254.1.1", private: true},
		{name: "IPv6 link-local unicast", ip: "fe80::1", private: true},

		// Link-local multicast
		{name: "IPv4 link-local multicast", ip: "224.0.0.1", private: true},

		// Unspecified addresses (0.0.0.0 / ::)
		{name: "IPv4 unspecified", ip: "0.0.0.0", private: true},
		{name: "IPv6 unspecified", ip: "::", private: true},

		// CGNAT range (RFC 6598): 100.64.0.0/10
		{name: "CGNAT low end", ip: "100.64.0.1", private: true},
		{name: "CGNAT mid range", ip: "100.100.100.100", private: true},
		{name: "CGNAT high end", ip: "100.127.255.254", private: true},
		// Just below CGNAT range — should NOT be private
		{name: "100.63.255.255 not CGNAT", ip: "100.63.255.255", private: false},
		// Just above CGNAT range — should NOT be private
		{name: "100.128.0.1 not CGNAT", ip: "100.128.0.1", private: false},

		// Public IPs — should NOT be private
		{name: "Google DNS", ip: "8.8.8.8", private: false},
		{name: "Cloudflare", ip: "1.1.1.1", private: false},
		{name: "Public IPv6", ip: "2001:4860:4860::8888", private: false},

		// Edge: 172.15.x is NOT private (just below 172.16)
		{name: "172.15.255.255 not private", ip: "172.15.255.255", private: false},
		// Edge: 172.32.x is NOT private (just above 172.31)
		{name: "172.32.0.1 not private", ip: "172.32.0.1", private: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := net.ParseIP(tt.ip)
			assert.NotNil(t, ip, "failed to parse IP: %s", tt.ip)
			assert.Equal(t, tt.private, isPrivateIP(ip),
				"isPrivateIP(%s) = %v, want %v", tt.ip, isPrivateIP(ip), tt.private)
		})
	}
}

// ---------------------------------------------------------------------------
// resolveAndValidateIP — URL parsing edge cases only. We do NOT hit real DNS.
// ---------------------------------------------------------------------------

func TestResolveAndValidateIP_InvalidURLMalformed(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "://missing-scheme")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

func TestResolveAndValidateIP_EmptyHostnameHTTP(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "http://")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
	assert.Contains(t, err.Error(), "empty hostname")
}

// ---------------------------------------------------------------------------
// resolveAndValidateIP — scheme validation (blocks non-HTTP/HTTPS schemes).
// ---------------------------------------------------------------------------

func TestResolveAndValidateIP_UnsupportedSchemes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "gopher scheme", url: "gopher://evil.com"},
		{name: "file scheme", url: "file:///etc/passwd"},
		{name: "ftp scheme", url: "ftp://example.com/file"},
		{name: "javascript scheme", url: "javascript:alert(1)"},
		{name: "data scheme", url: "data:text/html,<h1>hi</h1>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, _, err := resolveAndValidateIP(context.Background(), tt.url)
			assert.Error(t, err)
			assert.ErrorIs(t, err, ErrSSRFBlocked)
		})
	}
}

func TestValidateScheme_AllowedSchemes(t *testing.T) {
	t.Parallel()

	// These schemes should pass the scheme check — tested via the pure
	// validateScheme function to avoid any DNS dependency.
	tests := []struct {
		name   string
		scheme string
	}{
		{name: "http scheme", scheme: "http"},
		{name: "https scheme", scheme: "https"},
		{name: "HTTP uppercase", scheme: "HTTP"},
		{name: "HTTPS uppercase", scheme: "HTTPS"},
		{name: "mixed case Http", scheme: "Http"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateScheme(tt.scheme)
			assert.NoError(t, err, "scheme %q must be allowed", tt.scheme)
		})
	}
}

func TestValidateScheme_BlockedSchemes(t *testing.T) {
	t.Parallel()

	// These schemes must be rejected — tested via the pure validateScheme
	// function to avoid any DNS dependency.
	tests := []struct {
		name   string
		scheme string
	}{
		{name: "gopher scheme", scheme: "gopher"},
		{name: "file scheme", scheme: "file"},
		{name: "ftp scheme", scheme: "ftp"},
		{name: "javascript scheme", scheme: "javascript"},
		{name: "data scheme", scheme: "data"},
		{name: "empty scheme", scheme: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateScheme(tt.scheme)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSSRFBlocked,
				"scheme %q must be blocked", tt.scheme)
		})
	}
}

// ---------------------------------------------------------------------------
// resolveAndValidateIP — URL parsing and scheme blocking (no DNS)
// ---------------------------------------------------------------------------

// TestResolveAndValidateIP_InvalidScheme checks that non-HTTP/HTTPS schemes are
// rejected before DNS lookup.
func TestResolveAndValidateIP_InvalidScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "gopher scheme", url: "gopher://example.com"},
		{name: "file scheme", url: "file:///etc/passwd"},
		{name: "ftp scheme", url: "ftp://example.com/file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, _, err := resolveAndValidateIP(context.Background(), tt.url)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSSRFBlocked,
				"non-HTTP/HTTPS scheme must return ErrSSRFBlocked")
		})
	}
}

// TestResolveAndValidateIP_EmptyHostname checks that an empty hostname is rejected.
func TestResolveAndValidateIP_EmptyHostname(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "http://")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
	assert.Contains(t, err.Error(), "empty hostname")
}

// TestResolveAndValidateIP_InvalidURL checks that a completely malformed URL
// is rejected with ErrInvalidURL.
func TestResolveAndValidateIP_InvalidURL(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "://no-scheme")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

// TestResolveAndValidateIP_PrivateIP confirms that hostnames that resolve to a
// loopback/private address are blocked. "localhost" resolves to 127.0.0.1
// (loopback), which must trigger ErrSSRFBlocked.
//
// This test does perform a real DNS lookup for "localhost". On all POSIX
// systems "localhost" is defined in /etc/hosts as 127.0.0.1, so the lookup
// is local and requires no network access. Environments that strip /etc/hosts
// may see the DNS fall-back path (original URL returned, no error), in which
// case the test is skipped gracefully.
func TestResolveAndValidateIP_PrivateIP(t *testing.T) {
	t.Parallel()

	// Probe the local DNS before asserting — skip if localhost doesn't resolve.
	addrs, lookupErr := net.LookupHost("localhost")
	if lookupErr != nil || len(addrs) == 0 {
		t.Skip("localhost DNS lookup failed or returned no results — skipping SSRF private-IP test")
	}

	_, _, _, err := resolveAndValidateIP(context.Background(), "http://localhost")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSSRFBlocked,
		"localhost (127.0.0.1) must be blocked as a private/loopback address")
}

// ---------------------------------------------------------------------------
// isPrivateIP — additional blocked ranges (RFC-defined special-purpose blocks)
// ---------------------------------------------------------------------------

// TestResolveAndValidateIP_AllBlockedRanges tests isPrivateIP directly against
// representative IPs from every additional CIDR block listed in ssrf.go:
//
//   - 0.0.0.1    → 0.0.0.0/8     "this network" (RFC 1122 §3.2.1.3)
//   - 192.0.0.1  → 192.0.0.0/24  IETF protocol assignments (RFC 6890)
//   - 192.0.2.1  → 192.0.2.0/24  TEST-NET-1 documentation (RFC 5737)
//   - 198.18.0.1 → 198.18.0.0/15 benchmarking (RFC 2544)
//   - 198.51.100.1 → 198.51.100.0/24 TEST-NET-2 documentation (RFC 5737)
//   - 203.0.113.1 → 203.0.113.0/24 TEST-NET-3 documentation (RFC 5737)
//   - 240.0.0.1  → 240.0.0.0/4   reserved / future use (RFC 1112)
//   - 239.255.255.255 → multicast (net.IP.IsMulticast)
func TestResolveAndValidateIP_AllBlockedRanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   string
	}{
		{name: "0/8 this-network", ip: "0.0.0.1"},
		{name: "192.0.0/24 IETF", ip: "192.0.0.1"},
		{name: "192.0.2/24 TEST-NET-1", ip: "192.0.2.1"},
		{name: "198.18/15 benchmarking", ip: "198.18.0.1"},
		{name: "198.51.100/24 TEST-NET-2", ip: "198.51.100.1"},
		{name: "203.0.113/24 TEST-NET-3", ip: "203.0.113.1"},
		{name: "240/4 reserved", ip: "240.0.0.1"},
		{name: "multicast 239.255.255.255", ip: "239.255.255.255"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "test setup error: %s is not a valid IP", tt.ip)
			assert.True(t, isPrivateIP(ip),
				"isPrivateIP(%s) must return true for blocked range %s", tt.ip, tt.name)
		})
	}
}
