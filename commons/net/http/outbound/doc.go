// Package outbound builds net/http clients for calls to hosts the caller does
// not fully control: partner APIs, callback URLs, anything whose address comes
// from configuration or from a tenant.
//
// It is the stdlib net/http counterpart to the Fiber helpers in commons/net/http,
// and it reuses commons/security/ssrf as its single blocklist.
//
// # What the client refuses
//
//   - A scheme other than https, before any resolution or dial
//     ([ErrInsecureScheme]). Plaintext needs two keys: [WithAllowInsecureHTTP]
//     in code AND ALLOW_INSECURE_TLS truthy in the environment.
//   - A blocked hostname (localhost, cloud metadata names, .internal, .local,
//     .cluster.local) or blocked IP literal ([ssrf.ErrBlocked]).
//   - A connection to a private, loopback, link-local or reserved address. The
//     check runs at dial time on the address actually connected to
//     ([ssrf.DialContext]), so a DNS answer that changes between validation
//     and connect (DNS rebinding) cannot reach a blocked range.
//   - Every 3xx by default ([ErrRedirectRefused]); a 3xx is an error, not a
//     response that could pass for success. The one exception is a 304
//     answering a conditional request (If-None-Match or If-Modified-Since),
//     which is not a redirect. [RedirectRevalidate] follows a bounded number of
//     hops, each through the same checks; a 3xx the client cannot follow (no
//     Location, 300, or a 307/308 whose request body cannot be replayed) is
//     still [ErrRedirectRefused].
//   - A TLS config with InsecureSkipVerify unless ALLOW_INSECURE_TLS is truthy
//     ([ErrInsecureTLSConfig]); TLS below 1.2.
//
// Environment proxies (HTTP_PROXY, HTTPS_PROXY) are ignored: behind a proxy the
// dial check would judge the proxy's address instead of the target's.
//
// # Private networks
//
// [WithAllowPrivateNetwork] lifts the IP-range blocklist for a client that must
// reach a private network, such as a partner reached over a private link. It is
// a code-level decision, logged WARN "security bypass active" when the client
// is built. The hostname blocklist still applies; exempt a specific internal
// name with [WithAllowHostname].
//
// # Usage
//
//	client, err := outbound.NewClient(
//	    outbound.WithTimeout(10*time.Second),
//	    outbound.WithLogger(logger),
//	)
//	if err != nil {
//	    return err
//	}
//
//	resp, err := client.Do(req)
//	switch {
//	case errors.Is(err, ssrf.ErrBlocked), errors.Is(err, outbound.ErrInsecureScheme):
//	    // refused by policy: do not retry
//	case errors.Is(err, outbound.ErrRedirectRefused):
//	    // the target answered 3xx
//	}
//
// [NewTransport] returns the same guarded transport for composition, for
// example under commons/net/http/pacing. It refuses the 3xx responses its
// policy does not follow on its own, so a client built around it cannot take
// one for success. It follows nothing itself: under [RedirectRevalidate] a
// client built around it must set its own CheckRedirect for the hop limit.
package outbound
