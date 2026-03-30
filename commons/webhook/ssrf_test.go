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

