package webhook

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// cidr4 constructs a static *net.IPNet for an IPv4 CIDR. Using a helper keeps
// each entry in the table below to a single readable line. All four octets are
// accepted so future CIDR additions don't require changing the signature.
//
//nolint:unparam // d is currently always 0 for these network addresses; kept for generality.
func cidr4(a, b, c, d byte, ones int) *net.IPNet {
	return &net.IPNet{
		IP:   net.IPv4(a, b, c, d).To4(),
		Mask: net.CIDRMask(ones, 32),
	}
}

// cgnatBlock is the CGNAT (Carrier-Grade NAT) range defined by RFC 6598.
// Cloud providers frequently use this range for internal routing, so it must
// be blocked to prevent SSRF via addresses like 100.64.0.1.
//
//nolint:gochecknoglobals // package-level CIDR block is intentional for SSRF protection
var cgnatBlock = cidr4(100, 64, 0, 0, 10)

// additionalBlockedRanges holds CIDR blocks that are not covered by the
// standard net.IP predicates (IsPrivate, IsLoopback, etc.) but must be
// blocked to prevent SSRF attacks.
//
// All entries are compile-time-constructed net.IPNet literals — no runtime
// string parsing, no init() required, and typos surface as test failures
// rather than startup panics.
//
//nolint:gochecknoglobals // package-level slice is intentional for SSRF protection
var additionalBlockedRanges = []*net.IPNet{
	cidr4(0, 0, 0, 0, 8),       // 0.0.0.0/8       — "this network" (RFC 1122 §3.2.1.3)
	cidr4(192, 0, 0, 0, 24),    // 192.0.0.0/24    — IETF protocol assignments (RFC 6890)
	cidr4(192, 0, 2, 0, 24),    // 192.0.2.0/24    — TEST-NET-1 documentation (RFC 5737)
	cidr4(198, 18, 0, 0, 15),   // 198.18.0.0/15   — benchmarking (RFC 2544)
	cidr4(198, 51, 100, 0, 24), // 198.51.100.0/24 — TEST-NET-2 documentation (RFC 5737)
	cidr4(203, 0, 113, 0, 24),  // 203.0.113.0/24  — TEST-NET-3 documentation (RFC 5737)
	cidr4(240, 0, 0, 0, 4),     // 240.0.0.0/4     — reserved/future use (RFC 1112)
}

// resolveAndValidateIP performs a single DNS lookup for the hostname in rawURL,
// validates every resolved IP against the SSRF blocklist, and returns a new URL
// with the hostname replaced by the first resolved IP (DNS pinning).
//
// Combining validation and pinning into one lookup eliminates the TOCTOU window
// that exists when validateResolvedIP and pinResolvedIP are called sequentially:
// a DNS rebinding attack could change the record between those two calls, causing
// the pinned IP to differ from the validated one.
//
// On success it returns:
//   - pinnedURL        — original URL with the hostname replaced by the first resolved IP.
//   - originalAuthority — the original host:port authority (u.Host), suitable for the
//     HTTP Host header so explicit non-default ports are preserved.
//   - sniHostname       — the bare hostname (u.Hostname(), port stripped), suitable for
//     TLS SNI / certificate verification.
//
// DNS lookup failures are fail-closed: if the hostname cannot be resolved, the
// URL is rejected.  When no resolved IP can be parsed from the DNS response the
// URL is considered unresolvable and an error is returned.
func resolveAndValidateIP(ctx context.Context, rawURL string) (pinnedURL, originalAuthority, sniHostname string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	if err := validateScheme(u.Scheme); err != nil {
		return "", "", "", err
	}

	host := u.Hostname()
	if host == "" {
		return "", "", "", fmt.Errorf("%w: empty hostname", ErrInvalidURL)
	}

	ips, dnsErr := net.DefaultResolver.LookupHost(ctx, host)
	if dnsErr != nil {
		return "", "", "", fmt.Errorf("%w: DNS lookup failed for %s: %w", ErrSSRFBlocked, host, dnsErr)
	}

	if len(ips) == 0 {
		return "", "", "", fmt.Errorf("%w: DNS returned no addresses for %s", ErrSSRFBlocked, host)
	}

	var firstValidIP string

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}

		if isPrivateIP(ip) {
			return "", "", "", fmt.Errorf("%w: resolved IP %s is private/loopback", ErrSSRFBlocked, ipStr)
		}

		if firstValidIP == "" {
			firstValidIP = ipStr
		}
	}

	if firstValidIP == "" {
		return "", "", "", fmt.Errorf("%w: no valid IPs resolved for %s", ErrInvalidURL, host)
	}

	// Preserve the original authority (host:port) for the HTTP Host header
	// before rewriting u.Host to the pinned IP.
	authority := u.Host

	// Pin to first valid resolved IP to prevent DNS rebinding across retries.
	port := u.Port()

	switch {
	case port != "":
		u.Host = net.JoinHostPort(firstValidIP, port)
	case strings.Contains(firstValidIP, ":"):
		// Bare IPv6 literal must be bracket-wrapped for url.URL.Host.
		u.Host = "[" + firstValidIP + "]"
	default:
		u.Host = firstValidIP
	}

	return u.String(), authority, host, nil
}

// validateScheme checks that the URL scheme is http or https. All other
// schemes are rejected to prevent SSRF via exotic protocols (file://, gopher://, etc.).
// This is a pure function with no DNS or I/O dependency, making it safe for
// isolated unit testing.
func validateScheme(scheme string) error {
	s := strings.ToLower(scheme)
	if s != "http" && s != "https" {
		return fmt.Errorf("%w: scheme %q not allowed", ErrSSRFBlocked, scheme)
	}

	return nil
}

// isPrivateIP reports whether ip is in a private, loopback, link-local,
// unspecified, CGNAT, multicast, or other reserved range that must not be
// contacted by webhook delivery (SSRF protection).
//
// In addition to the ranges covered by the standard net.IP predicates, this
// function checks the additionalBlockedRanges slice which covers RFC-defined
// special-purpose blocks not included in Go's net package.
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		cgnatBlock.Contains(ip) {
		return true
	}

	for _, block := range additionalBlockedRanges {
		if block.Contains(ip) {
			return true
		}
	}

	return false
}
