package http

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-observability/log"
	libSSRF "github.com/LerianStudio/lib-commons/v5/commons/security/ssrf"
)

// ssrfSafeTransport wraps an http.Transport and implements http.RoundTripper.
// On each outbound request it:
//  1. Validates the request URL against the proxy policy (scheme/host allowlists).
//  2. Unless AllowUnsafeDestinations is set, delegates DNS resolution and IP
//     validation to [libSSRF.ResolveAndValidate] which performs both atomically
//     and returns a pinned URL. This eliminates the TOCTOU window between
//     "validate" and "connect" that existed when DNS was resolved separately in
//     DialContext.
//  3. Rewrites the request to use the pinned IP, preserving the original Host
//     header for correct virtual-host routing.
type ssrfSafeTransport struct {
	policy ReverseProxyPolicy
	base   *http.Transport
	// ssrfOpts are functional options forwarded to ssrf.ResolveAndValidate.
	// Stored at construction so that tests can inject a custom lookup function.
	ssrfOpts []libSSRF.Option
}

// newSSRFSafeTransport creates a transport that enforces the given proxy policy
// via [libSSRF.ResolveAndValidate] in its RoundTrip method.
func newSSRFSafeTransport(policy ReverseProxyPolicy) *ssrfSafeTransport {
	return newSSRFSafeTransportWithOpts(policy, nil)
}

// newSSRFSafeTransportWithOpts is the internal constructor that accepts
// optional [libSSRF.Option] values (primarily [libSSRF.WithLookupFunc] for
// tests).
func newSSRFSafeTransportWithOpts(
	policy ReverseProxyPolicy,
	ssrfOpts []libSSRF.Option,
) *ssrfSafeTransport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         dialer.DialContext,
	}

	return &ssrfSafeTransport{
		policy:   policy,
		base:     transport,
		ssrfOpts: ssrfOpts,
	}
}

// RoundTrip validates each outbound request against the proxy policy and, when
// SSRF protection is enabled, atomically resolves DNS and pins the connection
// to a validated IP via [libSSRF.ResolveAndValidate].
func (t *ssrfSafeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateProxyTarget(req.URL, t.policy); err != nil {
		return nil, err
	}

	// When unsafe destinations are allowed (development/testing) skip SSRF
	// resolution and forward the request as-is.
	if t.policy.AllowUnsafeDestinations {
		return t.base.RoundTrip(req)
	}

	// Atomically resolve DNS, validate all IPs, and pin the URL.
	result, err := libSSRF.ResolveAndValidate(req.Context(), req.URL.String(), t.ssrfOpts...)
	if err != nil {
		policyLogger := t.policy.Logger

		if !nilcheck.Interface(policyLogger) {
			policyLogger.Log(req.Context(), log.LevelWarn, "proxy SSRF validation failed",
				log.String("host", req.URL.Host),
				log.Err(err),
			)
		}

		// Map SSRF sentinel errors to the proxy-specific errors that callers
		// already match via errors.Is.
		switch {
		case errors.Is(err, libSSRF.ErrDNSFailed):
			return nil, ErrDNSResolutionFailed
		case errors.Is(err, libSSRF.ErrBlocked):
			return nil, ErrUnsafeProxyDestination
		case errors.Is(err, libSSRF.ErrInvalidURL):
			return nil, ErrInvalidProxyTarget
		default:
			return nil, ErrUnsafeProxyDestination
		}
	}

	// Clone the request so we don't mutate the caller's *http.Request.
	pinned := req.Clone(req.Context())

	pinnedURL, parseErr := url.Parse(result.PinnedURL)
	if parseErr != nil {
		return nil, ErrInvalidProxyTarget
	}

	pinned.URL = pinnedURL
	// Preserve the original Host header so the upstream server routes correctly.
	pinned.Host = result.Authority

	return t.base.RoundTrip(pinned)
}
