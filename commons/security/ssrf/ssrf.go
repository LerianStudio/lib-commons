package ssrf

import (
	"errors"
	"net"
	"net/netip"
)

// Sentinel errors returned by validation functions. Callers should use
// [errors.Is] to check the category because concrete errors wrap these
// sentinels with additional context via [fmt.Errorf] and %w.
var (
	// ErrBlocked is returned when a URL or IP is rejected by SSRF protection.
	ErrBlocked = errors.New("ssrf: blocked")

	// ErrInvalidURL is returned when a URL cannot be parsed or is structurally
	// invalid (empty hostname, missing scheme, etc.).
	ErrInvalidURL = errors.New("ssrf: invalid URL")

	// ErrDNSFailed is returned when DNS resolution fails for a hostname.
	ErrDNSFailed = errors.New("ssrf: DNS resolution failed")
)

// blockedPrefixes is the canonical CIDR blocklist for SSRF protection. It
// covers RFC-defined special-purpose ranges that are not caught by the
// standard library predicates (IsLoopback, IsPrivate, IsLinkLocalUnicast,
// etc.). Each entry is intentionally a netip.Prefix literal so typos are
// caught at init time rather than silently ignored at request time.
// MAINTENANCE: when adding or removing entries, update expectedPrefixCount
// in ssrf_test.go to keep TestBlockedPrefixes_ReturnsExpectedCount in sync.
//
//nolint:gochecknoglobals // package-level CIDR blocklist is intentional for SSRF protection
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network" (RFC 1122 S3.2.1.3)
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT (RFC 6598)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments (RFC 6890)
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1 documentation (RFC 5737)
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking (RFC 2544)
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2 documentation (RFC 5737)
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3 documentation (RFC 5737)
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved / future use (RFC 1112)

	// IPv6 special-purpose ranges not covered by stdlib predicates.
	netip.MustParsePrefix("2001:db8::/32"), // documentation (RFC 3849)
	netip.MustParsePrefix("100::/64"),      // discard-only (RFC 6666)
	netip.MustParsePrefix("2001::/32"),     // Teredo, obfuscated IPv4 client (RFC 4380)
}

// IPv6 prefixes whose addresses carry the IPv4 address they reach. The
// embedded IPv4 address is judged instead of the IPv6 one.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")   // NAT64, IPv4 in the last 32 bits (RFC 6052)
	nat64LocalUse  = netip.MustParsePrefix("64:ff9b:1::/48") // NAT64 local use, /96 inside it (RFC 8215)
	sixToFour      = netip.MustParsePrefix("2002::/16")      // 6to4, IPv4 in bits 16-47 (RFC 3056)
)

// BlockedPrefixes returns a copy of the canonical CIDR blocklist. The returned
// slice is safe to modify without affecting the package state.
func BlockedPrefixes() []netip.Prefix {
	out := make([]netip.Prefix, len(blockedPrefixes))
	copy(out, blockedPrefixes)

	return out
}

// IsBlockedAddr reports whether addr falls in a private, loopback, link-local,
// multicast, unspecified, or other reserved range that must not be contacted by
// outbound HTTP requests.
//
// Check order:
//  1. Unmap IPv4-mapped IPv6 addresses (e.g. ::ffff:127.0.0.1 becomes 127.0.0.1),
//     drop any zone, and judge the IPv4 address a NAT64 or 6to4 address
//     embeds (e.g. 64:ff9b::a9fe:a9fe is judged as 169.254.169.254).
//  2. Standard library predicates: IsLoopback, IsPrivate, IsLinkLocalUnicast,
//     IsLinkLocalMulticast, IsMulticast, IsUnspecified.
//  3. Custom blocklist ([BlockedPrefixes]).
func IsBlockedAddr(addr netip.Addr) bool {
	// Step 0: the zero-value netip.Addr{} is not a real IP address. Treating
	// it as safe would violate the fail-closed principle — block it.
	if !addr.IsValid() {
		return true
	}

	// Step 1: unmap IPv4-mapped IPv6 so that ::ffff:10.0.0.1 is treated
	// identically to 10.0.0.1. A zone never matches a prefix, so drop it.
	addr = addr.Unmap().WithZone("")

	if v4, ok := embeddedIPv4(addr); ok {
		return IsBlockedAddr(v4)
	}

	// Step 2: standard library predicates.
	if addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() {
		return true
	}

	// Step 3: custom CIDR blocklist for ranges not covered by the predicates
	// above (CGNAT, TEST-NETs, benchmarking, reserved, etc.).
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

// embeddedIPv4 returns the IPv4 address a NAT64 or 6to4 address reaches.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()

	switch {
	case nat64WellKnown.Contains(addr), nat64LocalUse.Contains(addr):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(addr):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	default:
		return netip.Addr{}, false
	}
}

// IsBlockedIP reports whether ip falls in a blocked range. This is a
// convenience wrapper for callers that still use the legacy [net.IP] type; it
// converts to [netip.Addr] and delegates to [IsBlockedAddr].
//
// A nil ip is considered blocked (fail-closed).
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		// Unparseable IP — fail-closed.
		return true
	}

	return IsBlockedAddr(addr)
}
