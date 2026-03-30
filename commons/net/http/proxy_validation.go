package http

import (
	"net"
	"net/url"
	"strings"

	libSSRF "github.com/LerianStudio/lib-commons/v4/commons/security/ssrf"
)

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
// It delegates to the canonical SSRF package for the actual blocked-range check.
func isUnsafeIP(ip net.IP) bool {
	return libSSRF.IsBlockedIP(ip)
}
