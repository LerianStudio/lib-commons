// Package ssrf provides server-side request forgery (SSRF) protection for HTTP
// clients that connect to user-controlled URLs.
//
// # Design Decisions
//
//   - Fail-closed: every validation error rejects the request. DNS lookup
//     failures, unparseable IPs, and empty resolution results all return an
//     error rather than silently allowing the request through.
//
//   - DNS pinning: [ResolveAndValidate] performs a single DNS lookup, validates
//     all resolved IPs, and rewrites the URL to the first safe IP. This
//     eliminates the TOCTOU (time-of-check-to-time-of-use) window that exists
//     when validation and connection happen in separate steps; a DNS rebinding
//     attack cannot change the record between those two operations.
//
//   - Single source of truth: the [BlockedPrefixes] list is the canonical CIDR
//     blocklist for the entire lib-commons module. Both the webhook deliverer
//     and the reverse-proxy helper delegate to this package instead of
//     maintaining their own blocklists.
//
//   - Modern types: [netip.Prefix] and [netip.Addr] are the canonical types.
//     A legacy [net.IP] entry point ([IsBlockedIP]) is provided for callers
//     that have not yet migrated, but it delegates to [IsBlockedAddr] after
//     conversion.
//
// # Usage
//
// Quick IP check (no DNS):
//
//	addr, _ := netip.ParseAddr("10.0.0.1")
//	if ssrf.IsBlockedAddr(addr) {
//	    // reject
//	}
//
// Full URL validation with DNS pinning:
//
//	result, err := ssrf.ResolveAndValidate(ctx, "https://example.com/hook")
//	if err != nil {
//	    // reject — err wraps ssrf.ErrBlocked, ssrf.ErrInvalidURL, or ssrf.ErrDNSFailed
//	}
//	// Use result.PinnedURL for the actual HTTP request.
//	// Set the Host header to result.Authority.
//	// Set TLS ServerName to result.SNIHostname.
//
// Custom DNS resolver for tests:
//
//	result, err := ssrf.ResolveAndValidate(ctx, rawURL,
//	    ssrf.WithLookupFunc(func(_ context.Context, _ string) ([]string, error) {
//	        return []string{"93.184.216.34"}, nil
//	    }),
//	)
package ssrf
