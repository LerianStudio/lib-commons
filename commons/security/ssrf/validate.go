package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// ResolveResult holds the output of [ResolveAndValidate]. Callers should use
// [PinnedURL] for the actual HTTP request, set the Host header to [Authority],
// and configure TLS ServerName to [SNIHostname].
type ResolveResult struct {
	// PinnedURL is the original URL with the hostname replaced by the first
	// safe resolved IP address. Using this URL for the HTTP request eliminates
	// the TOCTOU window between DNS validation and connection.
	PinnedURL string

	// Authority is the original host:port value (url.URL.Host) before DNS
	// pinning. It preserves explicit non-default ports and should be used as
	// the HTTP Host header value so the target server routes correctly.
	Authority string

	// SNIHostname is the bare hostname without port (url.URL.Hostname()). It
	// should be used for TLS SNI / certificate verification.
	SNIHostname string
}

// ValidateURL checks a URL for SSRF safety without performing DNS resolution.
// It validates the scheme, hostname blocking, and any IP literal in the
// hostname.
//
// Use [ResolveAndValidate] when DNS pinning is needed (i.e. when you intend to
// actually connect to the URL). ValidateURL is suitable for pre-flight
// validation where DNS resolution is deferred or not desired.
//
// Errors wrap [ErrBlocked] or [ErrInvalidURL] for programmatic inspection via
// [errors.Is].
func ValidateURL(ctx context.Context, rawURL string, opts ...Option) error {
	if ctx == nil {
		return fmt.Errorf("%w: %w", ErrInvalidURL, errors.New("nil context"))
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	cfg := buildConfig(opts)

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	if err := validateScheme(u.Scheme, cfg); err != nil {
		return err
	}

	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("%w: empty hostname", ErrInvalidURL)
	}

	// Check hostname-level blocking (localhost, metadata endpoints, etc.).
	if isBlockedHostnameWithConfig(hostname, cfg) {
		return fmt.Errorf("%w: hostname %q is blocked", ErrBlocked, hostname)
	}

	// If the hostname is an IP literal, validate it against the CIDR blocklist.
	if !cfg.allowPrivate {
		if addr, err := netip.ParseAddr(hostname); err == nil {
			if IsBlockedAddr(addr) {
				return fmt.Errorf("%w: IP %s is in a blocked range", ErrBlocked, hostname)
			}
		}
	}

	return nil
}

// ResolveAndValidate performs DNS resolution, validates all resolved IPs
// against the SSRF blocklist, and returns a pinned URL. This eliminates the
// TOCTOU window between "validate" and "connect" by combining both into a
// single DNS lookup.
//
// Flow:
//  1. Parse URL, validate scheme (http/https only, or https-only with [WithHTTPSOnly]).
//  2. Check hostname blocking ([IsBlockedHostname]).
//  3. Resolve DNS (single lookup via [net.DefaultResolver] or [WithLookupFunc]).
//  4. Validate resolved IPs — reject if ANY IP is blocked ([IsBlockedAddr]).
//  5. Pin URL to first safe IP, return [ResolveResult].
//
// DNS lookup failures are fail-closed: if the hostname cannot be resolved the
// URL is rejected. When every resolved IP is blocked the URL is also rejected.
//
// Errors wrap [ErrBlocked], [ErrInvalidURL], or [ErrDNSFailed] for
// programmatic inspection via [errors.Is].
func ResolveAndValidate(ctx context.Context, rawURL string, opts ...Option) (*ResolveResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, errors.New("nil context"))
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	cfg := buildConfig(opts)

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	if err := validateScheme(u.Scheme, cfg); err != nil {
		return nil, err
	}

	hostname := u.Hostname()
	if hostname == "" {
		return nil, fmt.Errorf("%w: empty hostname", ErrInvalidURL)
	}

	// Hostname-level blocking (localhost, metadata endpoints, dangerous suffixes).
	if isBlockedHostnameWithConfig(hostname, cfg) {
		return nil, fmt.Errorf("%w: hostname %q is blocked", ErrBlocked, hostname)
	}

	// DNS resolution — use custom resolver if provided, otherwise default.
	ips, dnsErr := lookupHost(ctx, hostname, cfg)
	if dnsErr != nil {
		return nil, fmt.Errorf("%w: lookup failed for %s: %w", ErrDNSFailed, hostname, dnsErr)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no addresses returned for %s", ErrDNSFailed, hostname)
	}

	// Validate every resolved IP and find the first safe one.
	var firstSafeIP string

	for _, ipStr := range ips {
		addr, parseErr := netip.ParseAddr(ipStr)
		if parseErr != nil {
			// Skip unparseable entries; if none survive we fail below.
			continue
		}

		if !cfg.allowPrivate && IsBlockedAddr(addr) {
			return nil, fmt.Errorf("%w: resolved IP %s is in a blocked range", ErrBlocked, ipStr)
		}

		if firstSafeIP == "" {
			firstSafeIP = ipStr
		}
	}

	if firstSafeIP == "" {
		return nil, fmt.Errorf("%w: no valid IPs resolved for %s", ErrInvalidURL, hostname)
	}

	// Preserve the original authority before rewriting the host.
	authority := u.Host

	// Pin to first safe IP — prevents DNS rebinding across retries.
	port := u.Port()

	switch {
	case port != "":
		u.Host = net.JoinHostPort(firstSafeIP, port)
	case strings.Contains(firstSafeIP, ":"):
		// Bare IPv6 literal must be bracket-wrapped for url.URL.Host.
		u.Host = "[" + firstSafeIP + "]"
	default:
		u.Host = firstSafeIP
	}

	return &ResolveResult{
		PinnedURL:   u.String(),
		Authority:   authority,
		SNIHostname: hostname,
	}, nil
}

// validateScheme checks that the URL scheme is allowed. By default only "http"
// and "https" are permitted. With [WithHTTPSOnly], only "https" is allowed.
func validateScheme(scheme string, cfg *config) error {
	s := strings.ToLower(scheme)

	if cfg.httpsOnly {
		if s != "https" {
			return fmt.Errorf("%w: scheme %q not allowed (HTTPS only)", ErrBlocked, scheme)
		}

		return nil
	}

	if s != "http" && s != "https" {
		return fmt.Errorf("%w: scheme %q not allowed", ErrBlocked, scheme)
	}

	return nil
}

// lookupHost resolves hostname via the configured lookup function or the
// default resolver.
func lookupHost(ctx context.Context, hostname string, cfg *config) ([]string, error) {
	if cfg.lookupFunc != nil {
		return cfg.lookupFunc(ctx, hostname)
	}

	return net.DefaultResolver.LookupHost(ctx, hostname)
}
