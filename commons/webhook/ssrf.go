package webhook

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// cgnatBlock is the CGNAT (Carrier-Grade NAT) range defined by RFC 6598.
// Cloud providers frequently use this range for internal routing, so it must
// be blocked to prevent SSRF via addresses like 100.64.0.1.
//
//nolint:gochecknoglobals // package-level CIDR block is intentional for SSRF protection
var cgnatBlock = func() *net.IPNet {
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	return cidr
}()

// additionalBlockedRanges holds CIDR blocks that are not covered by the
// standard net.IP predicates (IsPrivate, IsLoopback, etc.) but must be
// blocked to prevent SSRF attacks:
//
//   - 0.0.0.0/8       "this network" (RFC 1122 §3.2.1.3)
//   - 192.0.0.0/24    IETF protocol assignments (RFC 6890)
//   - 192.0.2.0/24    TEST-NET-1 documentation (RFC 5737)
//   - 198.18.0.0/15   benchmarking (RFC 2544)
//   - 198.51.100.0/24 TEST-NET-2 documentation (RFC 5737)
//   - 203.0.113.0/24  TEST-NET-3 documentation (RFC 5737)
//   - 240.0.0.0/4     reserved/future use (RFC 1112)
//
//nolint:gochecknoglobals // package-level slice is intentional for SSRF protection
var additionalBlockedRanges []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
	} {
		_, block, _ := net.ParseCIDR(cidr)
		if block != nil {
			additionalBlockedRanges = append(additionalBlockedRanges, block)
		}
	}
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
//   - pinnedURL  — original URL with the hostname replaced by the first resolved IP.
//   - originalHost — the original hostname, for use as the HTTP Host header (TLS SNI).
//
// DNS lookup failures are fail-closed: if the hostname cannot be resolved, the
// URL is rejected.  When no resolved IP can be parsed from the DNS response the
// URL is considered unresolvable and an error is returned.
func resolveAndValidateIP(ctx context.Context, rawURL string) (pinnedURL string, originalHost string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("%w: scheme %q not allowed", ErrSSRFBlocked, scheme)
	}

	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("%w: empty hostname", ErrInvalidURL)
	}

	ips, dnsErr := net.DefaultResolver.LookupHost(ctx, host)
	if dnsErr != nil {
		return "", "", fmt.Errorf("%w: DNS lookup failed for %s: %w", ErrSSRFBlocked, host, dnsErr)
	}

	if len(ips) == 0 {
		return "", "", fmt.Errorf("%w: DNS returned no addresses for %s", ErrSSRFBlocked, host)
	}

	var firstValidIP string

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}

		if isPrivateIP(ip) {
			return "", "", fmt.Errorf("%w: resolved IP %s is private/loopback", ErrSSRFBlocked, ipStr)
		}

		if firstValidIP == "" {
			firstValidIP = ipStr
		}
	}

	if firstValidIP == "" {
		return "", "", fmt.Errorf("%w: no valid IPs resolved for %s", ErrInvalidURL, host)
	}

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

	return u.String(), host, nil
}

// isPrivateIP reports whether ip is in a private, loopback, link-local,
// unspecified, CGNAT, multicast, or other reserved range that must not be
// contacted by webhook delivery (SSRF protection).
//
// In addition to the ranges covered by the standard net.IP predicates, this
// function checks the additionalBlockedRanges slice which covers RFC-defined
// special-purpose blocks not included in Go's net package (see init above).
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
