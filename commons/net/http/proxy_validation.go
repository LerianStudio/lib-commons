package http

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var blockedProxyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// validateProxyTarget checks a parsed URL against the reverse proxy policy.
func validateProxyTarget(targetURL *url.URL, policy ReverseProxyPolicy) error {
	if targetURL == nil || targetURL.Scheme == "" || targetURL.Host == "" {
		return ErrInvalidProxyTarget
	}

	if !isAllowedScheme(targetURL.Scheme, policy.AllowedSchemes) {
		return ErrUntrustedProxyScheme
	}

	hostname := targetURL.Hostname()
	if hostname == "" {
		return ErrInvalidProxyTarget
	}

	if strings.EqualFold(hostname, "localhost") && !policy.AllowUnsafeDestinations {
		return ErrUnsafeProxyDestination
	}

	if !isAllowedHost(hostname, policy.AllowedHosts) {
		return ErrUntrustedProxyHost
	}

	if ip := net.ParseIP(hostname); ip != nil && isUnsafeIP(ip) && !policy.AllowUnsafeDestinations {
		return ErrUnsafeProxyDestination
	}

	return nil
}

// isAllowedScheme reports whether scheme is in the allowed list (case-insensitive).
func isAllowedScheme(scheme string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}

	for _, candidate := range allowed {
		if strings.EqualFold(scheme, candidate) {
			return true
		}
	}

	return false
}

// isAllowedHost reports whether host is in the allowed list (case-insensitive).
func isAllowedHost(host string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		return false
	}

	for _, candidate := range allowedHosts {
		if strings.EqualFold(host, candidate) {
			return true
		}
	}

	return false
}

// isUnsafeIP reports whether ip is a loopback, private, or otherwise non-routable address.
func isUnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
		return true
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}

	addr = addr.Unmap()

	for _, prefix := range blockedProxyPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}
