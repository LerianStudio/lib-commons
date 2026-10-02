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
//     ([ErrInsecureScheme]). Plaintext needs one of two allowances, see
//     "Plaintext" below.
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
// Environment proxies (HTTP_PROXY, HTTPS_PROXY) are ignored unless a proxy
// option asks for them, see "Forward proxy" below.
//
// # Plaintext
//
// [WithAllowPlaintextHTTP] permits http:// for one client. It is a code-level
// decision, logged WARN "security bypass active" when the client is built, and
// it neither reads nor sets ALLOW_INSECURE_TLS, so it relaxes nothing else in
// the process: not other clients, not the TLS of database, cache or broker
// connections, and not this client's own certificate verification.
//
// [WithAllowInsecureHTTP] is the development allowance: it takes effect only
// while ALLOW_INSECURE_TLS is truthy in the environment, and is refused with an
// ERROR log otherwise.
//
// # Forward proxy
//
// A client that must leave through a forward proxy (BYOC egress) opts in with
// [WithProxy], a fixed proxy URL, or [WithProxyFromEnvironment], which reads
// HTTPS_PROXY, HTTP_PROXY and NO_PROXY once, when the client is built. Either
// one logs WARN "outbound forward proxy active" (feature
// outbound_forward_proxy) with the proxy's scheme and host, never its
// credentials. Only http:// and https:// proxies are accepted; a malformed
// proxy URL fails construction with [ErrInvalidOption]. An https:// proxy is
// verified with the client's TLS config under the proxy's own host and is
// always spoken to in HTTP/1.1, so an egress gateway that also offers h2
// still receives a plain CONNECT.
//
// What still holds behind a proxy:
//   - The scheme, hostname blocklist and IP-literal checks run before the
//     proxy is contacted, so a refused target never reaches it. A host that
//     ends in a numeric label but is not a canonical IP literal ("127.1",
//     "2130706433") is a blocked hostname, because the proxy may read it as
//     an address.
//   - The client resolves a target name itself (through [WithLookupFunc] when
//     given) and refuses it when any answer is blocked, before the proxy is
//     contacted. A name the client cannot resolve is refused
//     ([ssrf.ErrDNSFailed]); [WithProxyUnresolvedTargets] sends it to the
//     proxy instead, for egress where only the proxy resolves external names.
//   - An https target goes through a CONNECT tunnel: TLS is end to end, with
//     the target's name as SNI, its certificate verified and the TLS 1.2
//     floor. An http target needs a plaintext allowance and is sent to the
//     proxy in absolute form, so the proxy sees it, credentials included.
//   - The redirect policy is unchanged.
//   - A host NO_PROXY matches is dialed directly, through the full dial-time
//     check; a direct request to the proxy's own address is refused.
//   - The proxy's own address must not be link-local (which covers cloud
//     metadata endpoints), unspecified or multicast ([ssrf.ErrBlocked]).
//     Loopback and private proxies, such as a sidecar or a corporate proxy,
//     are allowed.
//
// What changes: the proxy, not the client, makes the connection, after its own
// lookup of the target's name. A DNS answer that changes between the client's
// lookup and the proxy's (DNS rebinding), and a name the client delegated
// unresolved, are the proxy's to police. Give a proxied client target URLs
// from configuration or vetted sources, never raw user input.
// [WithAllowPrivateNetwork] turns the client-side target resolution off along
// with the IP-range blocklist.
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
