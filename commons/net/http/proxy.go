package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	constant "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	// ErrInvalidProxyTarget indicates the proxy target URL is malformed or empty.
	ErrInvalidProxyTarget = errors.New("invalid proxy target")
	// ErrUntrustedProxyScheme indicates the proxy target uses a disallowed URL scheme.
	ErrUntrustedProxyScheme = errors.New("untrusted proxy scheme")
	// ErrUntrustedProxyHost indicates the proxy target hostname is not in the allowed list.
	ErrUntrustedProxyHost = errors.New("untrusted proxy host")
	// ErrUnsafeProxyDestination indicates the proxy target resolves to a private or loopback address.
	ErrUnsafeProxyDestination = errors.New("unsafe proxy destination")
	// ErrNilProxyRequest indicates a nil HTTP request was passed to the reverse proxy.
	ErrNilProxyRequest = errors.New("proxy request cannot be nil")
	// ErrNilProxyResponse indicates a nil HTTP response writer was passed to the reverse proxy.
	ErrNilProxyResponse = errors.New("proxy response writer cannot be nil")
	// ErrNilProxyRequestURL indicates the HTTP request has a nil URL.
	ErrNilProxyRequestURL = errors.New("proxy request URL cannot be nil")
	// ErrDNSResolutionFailed indicates the proxy target hostname could not be resolved.
	ErrDNSResolutionFailed = errors.New("DNS resolution failed for proxy target")
	// ErrNoResolvedIPs indicates DNS resolution returned zero IP addresses for the proxy target.
	ErrNoResolvedIPs = errors.New("no resolved IPs for proxy target")
)

// ReverseProxyPolicy defines strict trust boundaries for reverse proxy targets.
type ReverseProxyPolicy struct {
	AllowedSchemes []string
	// AllowedHosts restricts proxy targets to the listed hostnames (case-insensitive).
	// An empty or nil slice rejects all hosts (secure-by-default), matching AllowedSchemes behavior.
	// Callers must explicitly populate this to permit proxy targets.
	// See isAllowedHost and ErrUntrustedProxyHost for enforcement details.
	AllowedHosts            []string
	AllowUnsafeDestinations bool
	// Logger is an optional structured logger for security-relevant events.
	// When nil, no logging is performed.
	Logger log.Logger
}

// DefaultReverseProxyPolicy returns a strict-by-default reverse proxy policy.
func DefaultReverseProxyPolicy() ReverseProxyPolicy {
	return ReverseProxyPolicy{
		AllowedSchemes:          []string{"https"},
		AllowedHosts:            nil,
		AllowUnsafeDestinations: false,
	}
}

// ServeReverseProxy serves a reverse proxy for a given URL, enforcing policy checks.
//
// Security: Uses a custom transport that validates resolved IPs at connection time
// to prevent DNS rebinding attacks and blocks redirect following to untrusted destinations.
func ServeReverseProxy(target string, policy ReverseProxyPolicy, res http.ResponseWriter, req *http.Request) error {
	if req == nil {
		return ErrNilProxyRequest
	}

	if req.URL == nil {
		return ErrNilProxyRequestURL
	}

	if res == nil {
		return ErrNilProxyResponse
	}

	targetURL, err := url.Parse(target)
	if err != nil {
		return ErrInvalidProxyTarget
	}

	if err := validateProxyTarget(targetURL, policy); err != nil {
		if policy.Logger != nil {
			// Log the sanitized target (scheme + host only, no path/query) and the rejection reason.
			policy.Logger.Log(req.Context(), log.LevelWarn, "reverse proxy target rejected",
				log.String("target_host", targetURL.Host),
				log.String("target_scheme", targetURL.Scheme),
				log.Err(err),
			)
		}

		return err
	}

	// Start an OTEL client span so the proxied request appears in distributed traces.
	// We use targetURL.Host (scheme + host only) to avoid leaking credentials or paths.
	ctx, span := otel.Tracer("http.proxy").Start(
		req.Context(),
		"http.reverse_proxy",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("http.url", targetURL.Host),
		attribute.String("http.method", req.Method),
	)

	req = req.WithContext(ctx)

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = newSSRFSafeTransport(policy)

	// Propagate distributed trace context into the proxied request so
	// downstream services can continue the same trace.
	opentelemetry.InjectHTTPContext(req.Context(), req.Header)

	// Update the headers to allow for SSL redirection
	req.URL.Host = targetURL.Host
	req.URL.Scheme = targetURL.Scheme

	req.Header.Set(constant.HeaderForwardedHost, req.Host)
	req.Host = targetURL.Host

	// #nosec G704 -- target validated via validateProxyTarget with scheme/host allowlists and IP safety; ssrfSafeTransport re-validates resolved IPs at connection time
	proxy.ServeHTTP(res, req)

	return nil
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
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast()
}

// ssrfSafeTransport wraps an http.Transport with a DialContext that validates
// resolved IP addresses against the SSRF policy at connection time.
// This prevents DNS rebinding attacks where a hostname resolves to a safe IP
// during validation but a private IP at connection time.
//
// It also implements http.RoundTripper to validate redirect targets, preventing
// an allowed host from redirecting to an internal/unsafe destination.
type ssrfSafeTransport struct {
	policy ReverseProxyPolicy
	base   *http.Transport
}

// newSSRFSafeTransport creates a transport that enforces the given proxy policy
// on both DNS resolution (via DialContext) and redirect targets (via RoundTrip).
func newSSRFSafeTransport(policy ReverseProxyPolicy) *ssrfSafeTransport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if !policy.AllowUnsafeDestinations {
		policyLogger := policy.Logger

		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}

			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				if policyLogger != nil {
					policyLogger.Log(ctx, log.LevelWarn, "proxy DNS resolution failed",
						log.String("host", host),
						log.Err(err),
					)
				}

				return nil, fmt.Errorf("%w: %w", ErrDNSResolutionFailed, err)
			}

			safeIP, err := validateResolvedIPs(ctx, ips, host, policyLogger)
			if err != nil {
				return nil, err
			}

			// Connect using the already-validated numeric IP to prevent
			// a second DNS resolution (TOCTOU / DNS rebinding).
			if safeIP != nil && port != "" {
				addr = net.JoinHostPort(safeIP.String(), port)
			} else if safeIP != nil {
				addr = safeIP.String()
			}

			return dialer.DialContext(ctx, network, addr)
		}
	} else {
		transport.DialContext = dialer.DialContext
	}

	return &ssrfSafeTransport{
		policy: policy,
		base:   transport,
	}
}

// RoundTrip validates each outgoing request (including redirects) against the
// proxy policy before forwarding. This prevents an allowed target from using
// redirects to reach private/internal endpoints.
func (t *ssrfSafeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateProxyTarget(req.URL, t.policy); err != nil {
		return nil, err
	}

	return t.base.RoundTrip(req)
}

// validateResolvedIPs checks all resolved IPs against the SSRF policy.
// Returns the first safe IP for use in the connection, or an error if any IP
// is unsafe or if no IPs were resolved.
func validateResolvedIPs(ctx context.Context, ips []net.IPAddr, host string, logger log.Logger) (net.IP, error) {
	if len(ips) == 0 {
		if logger != nil {
			logger.Log(ctx, log.LevelWarn, "proxy target resolved to no IPs",
				log.String("host", host),
			)
		}

		return nil, ErrNoResolvedIPs
	}

	var safeIP net.IP

	for _, ipAddr := range ips {
		if isUnsafeIP(ipAddr.IP) {
			if logger != nil {
				logger.Log(ctx, log.LevelWarn, "proxy target resolved to unsafe IP",
					log.String("host", host),
				)
			}

			return nil, ErrUnsafeProxyDestination
		}

		if safeIP == nil {
			safeIP = ipAddr.IP
		}
	}

	return safeIP, nil
}
