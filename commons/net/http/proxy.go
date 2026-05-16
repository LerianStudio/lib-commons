package http

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
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
	// This allowlist is hostname-based only and does not restrict destination ports.
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

// ServeReverseProxy serves a reverse proxy for a given URL,
// enforcing explicit policy checks.
//
// Security: Uses a custom transport that validates resolved IPs at connection time
// to prevent DNS rebinding attacks.
func ServeReverseProxy(target string, policy ReverseProxyPolicy, res http.ResponseWriter, req *http.Request) error {
	if req == nil {
		return ErrNilProxyRequest
	}

	if req.URL == nil {
		return ErrNilProxyRequestURL
	}

	if nilcheck.Interface(res) {
		return ErrNilProxyResponse
	}

	targetURL, err := url.Parse(target)
	if err != nil {
		return ErrInvalidProxyTarget
	}

	if err := validateProxyTarget(targetURL, policy); err != nil {
		if !nilcheck.Interface(policy.Logger) {
			policy.Logger.Log(req.Context(), log.LevelWarn, "reverse proxy target rejected",
				log.String("target_host", targetURL.Host),
				log.String("target_scheme", targetURL.Scheme),
				log.Err(err),
			)
		}

		return err
	}

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
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = newSSRFSafeTransport(policy)

	// Preserve current v4 forwarding semantics for existing consumers.
	// This retains caller headers as-is, including auth/session headers, per user decision.
	opentelemetry.InjectHTTPContext(req.Context(), req.Header)
	req.URL.Host = targetURL.Host
	req.URL.Scheme = targetURL.Scheme
	req.Header.Set(constant.HeaderForwardedHost, req.Host)
	req.Host = targetURL.Host

	// #nosec G704 -- target validated via validateProxyTarget with scheme/host allowlists and IP safety; ssrfSafeTransport re-validates resolved IPs at connection time
	proxy.ServeHTTP(res, req)

	return nil
}
