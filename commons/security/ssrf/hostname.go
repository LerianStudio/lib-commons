package ssrf

import "strings"

// blockedHostnames contains exact hostnames that must be rejected regardless of
// IP resolution. Case-insensitive comparison is used.
//
//nolint:gochecknoglobals // package-level hostname blocklist is intentional for SSRF protection
var blockedHostnames = map[string]bool{
	"localhost":                true,
	"metadata.google.internal": true,
	"metadata.gcp.internal":    true,
	// AWS metadata IP as a hostname (also caught by IP check — defense-in-depth).
	"169.254.169.254": true,
	// Azure Instance Metadata Service (also caught by IP check and .internal
	// suffix — defense-in-depth).
	"metadata.azure.internal": true,
}

// blockedSuffixes contains hostname suffixes that indicate internal or
// non-routable DNS names. Any hostname ending with one of these suffixes is
// rejected.
//
// Note on ".internal": this blocks cloud metadata endpoints and internal DNS
// names. In corporate environments that use ".internal" as a legitimate TLD,
// use [WithAllowHostname] to exempt specific hosts.
//
//nolint:gochecknoglobals // package-level suffix blocklist is intentional for SSRF protection
var blockedSuffixes = []string{
	".local",         // mDNS / Bonjour (RFC 6762)
	".internal",      // cloud metadata, internal DNS
	".cluster.local", // Kubernetes internal DNS
}

// normalizeHostname lowercases the hostname and strips the optional trailing
// root label (".") so that "localhost." and "localhost" are treated identically.
// This prevents trivial SSRF bypasses via the DNS root-label spelling.
func normalizeHostname(hostname string) string {
	return strings.TrimRight(strings.ToLower(hostname), ".")
}

// IsBlockedHostname reports whether hostname matches known dangerous patterns.
//
// Checks performed (case-insensitive, trailing root label stripped):
//   - Empty hostname.
//   - Exact match against known blocked hostnames (localhost, cloud metadata
//     endpoints, AWS metadata IP).
//   - Suffix match against dangerous suffixes (.local, .internal,
//     .cluster.local).
//
// Note: the ".internal" suffix blocks cloud metadata and internal DNS names but
// may affect legitimate ".internal" domains in corporate environments. Use
// [WithAllowHostname] to exempt specific hostnames when calling
// [ValidateURL] or [ResolveAndValidate].
func IsBlockedHostname(hostname string) bool {
	if hostname == "" {
		return true
	}

	lower := normalizeHostname(hostname)

	if blockedHostnames[lower] {
		return true
	}

	for _, suffix := range blockedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}

	return false
}

// isBlockedHostnameWithConfig performs the same check as [IsBlockedHostname]
// but respects the allowedHostnames override from functional options.
func isBlockedHostnameWithConfig(hostname string, cfg *config) bool {
	if hostname == "" {
		return true
	}

	lower := normalizeHostname(hostname)

	// Check allow-list override first.
	if cfg != nil && cfg.allowedHostnames[lower] {
		return false
	}

	if blockedHostnames[lower] {
		return true
	}

	for _, suffix := range blockedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}

	return false
}
