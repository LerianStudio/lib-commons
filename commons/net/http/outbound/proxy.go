package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/security/ssrf"
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/idna"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// WithProxy sends every request through the forward proxy at proxyURL: an
// https target through a CONNECT tunnel, with TLS end to end to the target,
// and an http target (only under a plaintext allowance) in absolute form, so
// the proxy then sees that request, credentials included, in the clear.
//
// proxyURL is cloned, and a nil URL is ignored. It must be an http:// or
// https:// URL with an ASCII host, an optional port and no path, query or
// fragment; userinfo becomes the Proxy-Authorization credentials. Anything
// else, or combining it with [WithProxyFromEnvironment], fails construction
// with [ErrInvalidOption]. A proxy at a link-local (cloud metadata),
// unspecified or multicast address is refused, at construction for an IP
// literal and at connect time for a name ([ssrf.ErrBlocked]); loopback and
// private proxies, such as a sidecar or a corporate proxy, are allowed. An
// https:// proxy is verified with the client's TLS config, under the proxy's
// name unless that config sets ServerName, which then applies to the proxy
// and the target alike.
//
// Behind a proxy the client still refuses, before contacting the proxy, a
// non-https scheme without an allowance, a blocked hostname, a blocked IP
// literal and a target name that resolves, through the client's own resolver
// ([WithLookupFunc] when given), to any blocked address. A target name the
// client cannot resolve is refused ([ssrf.ErrDNSFailed]) unless
// [WithProxyUnresolvedTargets] is set. What the client cannot check is the
// address the proxy itself resolves and connects to: a DNS answer that
// changes between the two lookups (DNS rebinding) is the proxy's to police,
// so a proxied client should be given target URLs from configuration or
// vetted sources, never raw user input. [WithAllowPrivateNetwork] turns the
// target resolution off along with the IP-range blocklist. The client logs
// WARN "outbound forward proxy active" (feature outbound_forward_proxy, with
// the target_ip_check in force) when it is built.
func WithProxy(proxyURL *url.URL) Option {
	return func(c *config) {
		if proxyURL != nil {
			clone := *proxyURL
			c.proxyURL = &clone
		}
	}
}

// WithProxyFromEnvironment selects the forward proxy from HTTPS_PROXY (https
// targets), HTTP_PROXY (http targets) and NO_PROXY, lowercase names taking
// precedence, with the semantics of [net/http.ProxyFromEnvironment]: a value
// without a scheme is read as http://, and a host NO_PROXY matches is dialed
// directly with the full dial-time SSRF check.
//
// The environment is read once, when the client is built; later changes have
// no effect on it. Each proxy value is validated as [WithProxy] describes and
// a bad one fails construction with [ErrInvalidOption]; when neither proxy
// variable is set the client uses no proxy. The posture change described at
// [WithProxy] applies to every proxied request.
func WithProxyFromEnvironment() Option {
	return func(c *config) { c.proxyFromEnvironment = true }
}

// WithProxyUnresolvedTargets lets a proxied request whose target name the
// client cannot resolve go to the proxy, for egress where only the proxy has
// DNS for external names. A target name that does resolve is still refused
// when any answer is blocked. It needs [WithProxy] or
// [WithProxyFromEnvironment] ([ErrInvalidOption] otherwise), and the forward
// proxy audit line records it.
func WithProxyUnresolvedTargets() Option {
	return func(c *config) { c.proxyUnresolvedTargets = true }
}

// proxyFunc is the type of [net/http.Transport.Proxy].
type proxyFunc func(*http.Request) (*url.URL, error)

// configureProxy returns the transport's proxy function (nil without a proxy)
// and its dial function, which sends a dial to a proxy address through the
// proxy dialer and every other dial through direct.
func configureProxy(ctx context.Context, cfg *config, base *net.Dialer, direct ssrf.DialFunc) (proxyFunc, ssrf.DialFunc, error) {
	switch {
	case cfg.proxyURL != nil && cfg.proxyFromEnvironment:
		return nil, nil, fmt.Errorf("%w: WithProxy and WithProxyFromEnvironment are mutually exclusive", ErrInvalidOption)
	case cfg.proxyUnresolvedTargets && cfg.proxyURL == nil && !cfg.proxyFromEnvironment:
		return nil, nil, fmt.Errorf("%w: WithProxyUnresolvedTargets needs WithProxy or WithProxyFromEnvironment", ErrInvalidOption)
	case cfg.proxyURL != nil:
		return optionProxy(ctx, cfg, base, direct)
	case cfg.proxyFromEnvironment:
		return environmentProxy(ctx, cfg, base, direct)
	default:
		return nil, direct, nil
	}
}

func optionProxy(ctx context.Context, cfg *config, base *net.Dialer, direct ssrf.DialFunc) (proxyFunc, ssrf.DialFunc, error) {
	proxyURL := cfg.proxyURL
	proxyURL.Scheme = strings.ToLower(proxyURL.Scheme)

	if err := validateProxyURL(proxyURL); err != nil {
		return nil, nil, err
	}

	route := newProxyRoute(base, direct, proxyURL)

	logProxy(ctx, cfg, "option", proxyURL, "http,https")

	return func(*http.Request) (*url.URL, error) { return proxyURL, nil }, route.dial, nil
}

func environmentProxy(ctx context.Context, cfg *config, base *net.Dialer, direct ssrf.DialFunc) (proxyFunc, ssrf.DialFunc, error) {
	env := httpproxy.FromEnvironment()

	httpsProxy, err := parseEnvironmentProxy(env.HTTPSProxy, "HTTPS_PROXY")
	if err != nil {
		return nil, nil, err
	}

	httpProxy, err := parseEnvironmentProxy(env.HTTPProxy, "HTTP_PROXY")
	if err != nil {
		return nil, nil, err
	}

	if httpsProxy == nil && httpProxy == nil {
		return nil, direct, nil
	}

	logEnvironmentProxies(ctx, cfg, httpsProxy, httpProxy)

	route := newProxyRoute(base, direct, httpsProxy, httpProxy)
	choose := env.ProxyFunc()

	return func(req *http.Request) (*url.URL, error) {
		proxyURL, err := choose(req.URL)
		if err != nil {
			return nil, fmt.Errorf("outbound: proxy selection: %w", err)
		}

		// A direct dial to a proxy's own address would take the proxy dialer
		// and skip the target's dial-time check.
		if proxyURL == nil && route.isProxyAddr(targetAddr(req.URL)) {
			return nil, fmt.Errorf("%w: direct request to the forward proxy's address %s", ssrf.ErrBlocked, req.URL.Host)
		}

		return proxyURL, nil
	}, route.dial, nil
}

// parseEnvironmentProxy parses one proxy variable with httpproxy's rule (a
// value that does not parse with a scheme and host is retried with http://)
// and validates it. Errors never repeat the raw value, which may carry
// credentials.
func parseEnvironmentProxy(raw, name string) (*url.URL, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil // an unset variable selects no proxy, which is not an error
	}

	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
		if prefixed, prefixErr := url.Parse("http://" + raw); prefixErr == nil {
			proxyURL, err = prefixed, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a valid proxy URL", ErrInvalidOption, name)
	}

	if err := validateProxyURL(proxyURL); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	return proxyURL, nil
}

// validateProxyURL accepts an http or https URL with an ASCII host, a valid
// optional port and nothing after the authority but an optional "/". An IP
// literal must also pass the proxy address check.
func validateProxyURL(u *url.URL) error {
	where := describeProxy(u)

	scheme := strings.ToLower(u.Scheme)
	if scheme != schemeHTTP && scheme != schemeHTTPS {
		return fmt.Errorf("%w: proxy %s: scheme must be http or https", ErrInvalidOption, where)
	}

	switch {
	case u.Opaque != "":
		return fmt.Errorf("%w: proxy %s: opaque URL", ErrInvalidOption, where)
	case u.Path != "" && u.Path != "/":
		return fmt.Errorf("%w: proxy %s: URL must have no path", ErrInvalidOption, where)
	case u.RawQuery != "" || u.ForceQuery:
		return fmt.Errorf("%w: proxy %s: URL must have no query", ErrInvalidOption, where)
	case u.Fragment != "":
		return fmt.Errorf("%w: proxy %s: URL must have no fragment", ErrInvalidOption, where)
	}

	if err := validateProxyHost(u.Hostname()); err != nil {
		return fmt.Errorf("%w: proxy %s: %w", ErrInvalidOption, where, err)
	}

	if err := validateProxyPort(u); err != nil {
		return fmt.Errorf("%w: proxy %s: %w", ErrInvalidOption, where, err)
	}

	return nil
}

var (
	errProxyHost = errors.New("host must be an ASCII hostname or an IP literal")
	errProxyPort = errors.New("port must be a number from 1 to 65535")
)

func validateProxyHost(host string) error {
	if host == "" {
		return errProxyHost
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		if isForbiddenProxyAddr(addr) {
			return fmt.Errorf("%w: proxy IP %s is link-local, unspecified or multicast", ssrf.ErrBlocked, host)
		}

		return nil
	}

	for _, r := range host {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'

		if !isLetter && !isDigit && r != '-' && r != '.' && r != '_' {
			return errProxyHost
		}
	}

	return nil
}

// validateProxyPort checks the port after the host's last colon, which
// url.URL.Port hides when it is not numeric.
func validateProxyPort(u *url.URL) error {
	host := u.Host
	if end := strings.LastIndexByte(host, ']'); end >= 0 {
		host = host[end+1:]
	}

	colon := strings.LastIndexByte(host, ':')
	if colon < 0 {
		return nil
	}

	port, err := strconv.Atoi(host[colon+1:])
	if err != nil || port < 1 || port > 65535 {
		return errProxyPort
	}

	return nil
}

// describeProxy names a proxy in errors and logs by scheme and host only: no
// userinfo, and no path or query, which a malformed value may have taken
// credentials into.
func describeProxy(u *url.URL) string {
	if u.User != nil {
		return u.Scheme + "://[redacted]@" + u.Host
	}

	return u.Scheme + "://" + u.Host
}

func logProxy(ctx context.Context, cfg *config, source string, proxyURL *url.URL, targets string) {
	cfg.logger.Log(ctx, obs.LevelWarn, "outbound forward proxy active",
		"feature", "outbound_forward_proxy",
		"source", source,
		"proxy", describeProxy(proxyURL),
		"targets", targets,
		"target_ip_check", describeTargetCheck(cfg),
	)
}

// describeTargetCheck names, for the audit line, how a proxied target's
// resolved addresses are checked.
func describeTargetCheck(cfg *config) string {
	switch {
	case cfg.allowPrivate:
		return "off: private network allowed"
	case cfg.proxyUnresolvedTargets:
		return "resolved by the client; unresolved names delegated to the proxy"
	default:
		return "resolved by the client, fail-closed"
	}
}

func logEnvironmentProxies(ctx context.Context, cfg *config, httpsProxy, httpProxy *url.URL) {
	switch {
	case httpsProxy != nil && httpProxy != nil && httpsProxy.String() == httpProxy.String():
		logProxy(ctx, cfg, "environment", httpsProxy, "http,https")
	default:
		if httpsProxy != nil {
			logProxy(ctx, cfg, "environment", httpsProxy, schemeHTTPS)
		}

		if httpProxy != nil {
			logProxy(ctx, cfg, "environment", httpProxy, schemeHTTP)
		}
	}
}

// admitProxiedTarget resolves the target of a request that will go through
// the proxy and refuses it when any answer is blocked, or when the name does
// not resolve and unresolved targets are not delegated. A direct request, an
// IP literal (judged by ssrf.ValidateURL) and a client with private networks
// allowed are not resolved here.
func (g *guardedTransport) admitProxiedTarget(req *http.Request) error {
	if g.proxy == nil || g.allowPrivate {
		return nil
	}

	if _, err := netip.ParseAddr(strings.TrimRight(req.URL.Hostname(), ".")); err == nil {
		return nil
	}

	proxyURL, err := g.proxy(req)
	if err != nil || proxyURL == nil {
		return err
	}

	_, err = ssrf.ResolveAndValidate(req.Context(), req.URL.String(), g.ssrfOpts...)
	if err != nil && g.proxyUnresolvedTargets && errors.Is(err, ssrf.ErrDNSFailed) {
		return nil
	}

	return err
}

// proxyRoute dials the configured proxy addresses through the proxy dialer
// and every other address through the SSRF-checked direct dialer.
type proxyRoute struct {
	addrs  map[string]struct{}
	proxy  ssrf.DialFunc
	direct ssrf.DialFunc
}

func newProxyRoute(base *net.Dialer, direct ssrf.DialFunc, proxies ...*url.URL) *proxyRoute {
	addrs := make(map[string]struct{}, len(proxies))

	for _, proxyURL := range proxies {
		if proxyURL != nil {
			addrs[targetAddr(proxyURL)] = struct{}{}
		}
	}

	dialer := *base
	dialer.ControlContext = func(_ context.Context, _, address string, _ syscall.RawConn) error {
		return checkProxyConnectAddr(address)
	}

	return &proxyRoute{addrs: addrs, proxy: dialer.DialContext, direct: direct}
}

func (r *proxyRoute) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if r.isProxyAddr(address) {
		return r.proxy(ctx, network, address)
	}

	return r.direct(ctx, network, address)
}

func (r *proxyRoute) isProxyAddr(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}

	_, ok := r.addrs[addrKey(host, port)]

	return ok
}

// targetAddr is the host:port net/http dials for u: the port defaults by
// scheme, and the host is in ASCII form.
func targetAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, schemeHTTPS) {
			port = "443"
		}
	}

	return addrKey(u.Hostname(), port)
}

// addrKey normalizes host:port for comparison: lowercase, and a non-ASCII
// name in the IDNA form net/http dials.
func addrKey(host, port string) string {
	if !isASCII(host) {
		if ascii, err := idna.Lookup.ToASCII(host); err == nil {
			host = ascii
		}
	}

	return net.JoinHostPort(strings.ToLower(host), port)
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}

	return true
}

// checkProxyConnectAddr judges the address the proxy dialer is about to
// connect to. An address it cannot parse is refused.
func checkProxyConnectAddr(address string) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: proxy connect address %q is not an IP: %w", ssrf.ErrBlocked, address, err)
	}

	if isForbiddenProxyAddr(addrPort.Addr()) {
		return fmt.Errorf("%w: proxy connect address %s is link-local, unspecified or multicast", ssrf.ErrBlocked, address)
	}

	return nil
}

var (
	awsMetadataIPv6 = netip.MustParseAddr("fd00:ec2::254")
	nat64WellKnown  = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour       = netip.MustParsePrefix("2002::/16")
)

// isForbiddenProxyAddr is the proxy's address rule, narrower than the SSRF
// blocklist: loopback and private proxies are legitimate, while link-local
// (169.254.169.254 and every other cloud metadata endpoint in that range,
// fe80::/10), the AWS IPv6 metadata address, unspecified and multicast
// addresses are not. IPv4-mapped, NAT64 and 6to4 forms are judged by the
// IPv4 address they reach.
func isForbiddenProxyAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}

	addr = addr.Unmap().WithZone("")

	raw := addr.As16()

	switch {
	case nat64WellKnown.Contains(addr):
		addr = netip.AddrFrom4([4]byte(raw[12:16]))
	case sixToFour.Contains(addr):
		addr = netip.AddrFrom4([4]byte(raw[2:6]))
	}

	return addr == awsMetadataIPv6 ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}
