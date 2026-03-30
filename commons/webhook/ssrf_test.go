//go:build unit

package webhook

import (
	"context"
	"errors"
	"fmt"
	"testing"

	libSSRF "github.com/LerianStudio/lib-commons/v4/commons/security/ssrf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// resolveAndValidateIP — URL validation (no real DNS unless noted)
// ---------------------------------------------------------------------------

// TestResolveAndValidateIP_InvalidURL checks that a completely malformed URL
// is rejected with ErrInvalidURL.
func TestResolveAndValidateIP_InvalidURL(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "://no-scheme")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
}

// TestResolveAndValidateIP_EmptyHostname checks that an empty hostname is
// rejected with ErrInvalidURL and a descriptive message.
func TestResolveAndValidateIP_EmptyHostname(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "http://")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidURL)
	assert.Contains(t, err.Error(), "empty hostname")
}

// TestResolveAndValidateIP_BlockedSchemes verifies that non-HTTP/HTTPS schemes
// are rejected before DNS lookup.
func TestResolveAndValidateIP_BlockedSchemes(t *testing.T) {
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
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSSRFBlocked,
				"non-HTTP/HTTPS scheme must return ErrSSRFBlocked")
		})
	}
}

// TestResolveAndValidateIP_BlockedHostname confirms that hostnames rejected by
// hostname-level SSRF validation are mapped to ErrSSRFBlocked.
func TestResolveAndValidateIP_BlockedHostname(t *testing.T) {
	t.Parallel()

	_, _, _, err := resolveAndValidateIP(context.Background(), "http://localhost")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSSRFBlocked,
		"localhost must be blocked by hostname-level SSRF protection")
}

// ---------------------------------------------------------------------------
// mapSSRFError — sentinel error translation (fail-closed for unknown errors)
// ---------------------------------------------------------------------------

// TestMapSSRFError verifies that all four branches of mapSSRFError translate
// canonical SSRF sentinel errors into the webhook package's error types, and
// that unrecognized errors are mapped to ErrSSRFBlocked (fail-closed).
func TestMapSSRFError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  error
		wantIs error
	}{
		{
			name:   "ErrInvalidURL maps to webhook ErrInvalidURL",
			input:  fmt.Errorf("validation: %w", libSSRF.ErrInvalidURL),
			wantIs: ErrInvalidURL,
		},
		{
			name:   "ErrBlocked maps to webhook ErrSSRFBlocked",
			input:  fmt.Errorf("blocked: %w", libSSRF.ErrBlocked),
			wantIs: ErrSSRFBlocked,
		},
		{
			name:   "ErrDNSFailed maps to webhook ErrSSRFBlocked",
			input:  fmt.Errorf("dns: %w", libSSRF.ErrDNSFailed),
			wantIs: ErrSSRFBlocked,
		},
		{
			name:   "unknown error maps to webhook ErrSSRFBlocked (fail-closed)",
			input:  errors.New("unexpected internal error"),
			wantIs: ErrSSRFBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mapSSRFError(tt.input)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantIs)
		})
	}
}
