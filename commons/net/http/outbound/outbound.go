package outbound

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/security/ssrf"
)

// Sentinel errors. Errors surfaced through [net/http.Client.Do] arrive wrapped
// in a *url.Error; match them with [errors.Is].
var (
	// ErrRedirectRefused is returned for a 3xx the policy does not follow: every
	// redirect under [RedirectRefuse], and the hop past the limit under
	// [RedirectRevalidate].
	ErrRedirectRefused = errors.New("outbound: redirect refused")

	// ErrInsecureScheme is returned, before any resolution or dial, for a
	// request whose scheme is not https (or not http/https when the plaintext
	// allowance is in effect).
	ErrInsecureScheme = errors.New("outbound: insecure scheme refused")

	// ErrInsecureTLSConfig is returned by the constructors for a TLS config with
	// InsecureSkipVerify set while ALLOW_INSECURE_TLS is not truthy.
	ErrInsecureTLSConfig = errors.New("outbound: TLS certificate verification disabled")

	// ErrInvalidOption is returned by the constructors for an option value
	// outside its documented range.
	ErrInvalidOption = errors.New("outbound: invalid option")

	// ErrNilRequest is returned by the transport for a nil request or URL.
	ErrNilRequest = errors.New("outbound: nil request")
)

// Transport and client defaults. The pool and handshake values follow
// [net/http.DefaultTransport].
const (
	defaultTimeout               = 30 * time.Second
	defaultDialTimeout           = 30 * time.Second
	defaultDialKeepAlive         = 30 * time.Second
	defaultMaxIdleConns          = 100
	defaultIdleConnTimeout       = 90 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
)

// RedirectPolicy selects how the client answers a 3xx response.
type RedirectPolicy uint8

const (
	// RedirectRefuse fails every redirect with [ErrRedirectRefused]. Default.
	RedirectRefuse RedirectPolicy = iota
	// RedirectRevalidate follows up to a hop limit. Every hop passes the same
	// scheme, hostname and dial-time IP checks as the first request.
	RedirectRevalidate
)

// String returns the policy name.
func (p RedirectPolicy) String() string {
	switch p {
	case RedirectRefuse:
		return "refuse"
	case RedirectRevalidate:
		return "revalidate"
	default:
		return "unknown"
	}
}

// Option configures [NewTransport] and [NewClient]. A nil Option is ignored.
type Option func(*config)

type config struct {
	timeout           time.Duration
	redirects         RedirectPolicy
	maxHops           int
	tlsConfig         *tls.Config
	allowInsecureHTTP bool
	allowPrivate      bool
	allowedHostnames  []string
	lookup            ssrf.LookupFunc
	logger            obs.Logger
}

// WithTimeout sets the client's total request timeout. It must be positive;
// the default is 30s. [NewTransport] validates it but has no use for it.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithRedirects selects the redirect policy. maxHops must be 0 with
// [RedirectRefuse] and at least 1 with [RedirectRevalidate].
func WithRedirects(policy RedirectPolicy, maxHops int) Option {
	return func(c *config) {
		c.redirects = policy
		c.maxHops = maxHops
	}
}

// WithTLSConfig sets the TLS client config. It is cloned, so later changes by
// the caller have no effect, and its MinVersion is raised to TLS 1.2 when
// lower. ServerName, when empty, stays the request URL's hostname. A config
// with InsecureSkipVerify is refused unless ALLOW_INSECURE_TLS is truthy. A nil
// config is ignored.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(c *config) {
		if cfg != nil {
			c.tlsConfig = cfg.Clone()
		}
	}
}

// WithAllowInsecureHTTP permits plaintext http:// requests, but only while
// ALLOW_INSECURE_TLS is truthy when the transport is built (logged WARN
// "security bypass active"). Without that variable the option is refused with
// an ERROR log and the transport stays https-only: both keys are required.
func WithAllowInsecureHTTP() Option {
	return func(c *config) { c.allowInsecureHTTP = true }
}

// WithAllowPrivateNetwork lifts the IP-range blocklist, so loopback, RFC 1918,
// link-local and other reserved addresses may be dialed. It is a code-level
// decision with no environment switch, logged WARN "security bypass active"
// when the transport is built. The hostname blocklist (localhost, cloud
// metadata names, .internal, .local) still applies; exempt a name with
// [WithAllowHostname].
func WithAllowPrivateNetwork() Option {
	return func(c *config) { c.allowPrivate = true }
}

// WithAllowHostname exempts one hostname from the hostname blocklist
// (case-insensitive). Its resolved addresses still pass the IP blocklist.
// Calls accumulate.
func WithAllowHostname(hostname string) Option {
	return func(c *config) { c.allowedHostnames = append(c.allowedHostnames, hostname) }
}

// WithLookupFunc replaces the DNS resolver, for tests or a custom resolver.
// A nil function is ignored.
func WithLookupFunc(fn ssrf.LookupFunc) Option {
	return func(c *config) {
		if fn != nil {
			c.lookup = fn
		}
	}
}

// WithLogger sets the logger for the security-bypass audit lines. A nil logger
// is ignored.
func WithLogger(logger obs.Logger) Option {
	return func(c *config) {
		if !nilcheck.Interface(logger) {
			c.logger = logger
		}
	}
}

// NewTransport returns an outbound [net/http.RoundTripper] that:
//   - refuses a non-https request before resolving or dialing
//     ([ErrInsecureScheme]), unless the plaintext allowance is in effect;
//   - refuses a blocked hostname or IP literal ([ssrf.ErrBlocked]);
//   - dials through [ssrf.DialContext], so the IP actually connected to is
//     checked at connect time and DNS rebinding cannot reach a blocked range;
//   - ignores HTTP_PROXY/HTTPS_PROXY (Proxy is nil), because behind a proxy
//     the dial check would judge the proxy's IP instead of the target's;
//   - verifies the certificate against the URL hostname, sent as SNI.
//
// The concrete type is deliberately hidden, so the safety fields cannot be
// reassigned after construction. The transport follows no redirect itself;
// the redirect policy lives in the client from [NewClient].
func NewTransport(opts ...Option) (http.RoundTripper, error) {
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	transport, err := newGuardedTransport(cfg)
	if err != nil {
		return nil, err
	}

	return transport, nil
}

// NewClient returns an [net/http.Client] over [NewTransport] with the
// configured timeout (default 30s) and redirect policy (default
// [RedirectRefuse]). The client has no cookie jar.
func NewClient(opts ...Option) (*http.Client, error) {
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	transport, err := newGuardedTransport(cfg)
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Transport:     transport,
		Timeout:       cfg.timeout,
		CheckRedirect: checkRedirect(cfg.redirects, cfg.maxHops),
	}, nil
}

func buildConfig(opts []Option) (*config, error) {
	cfg := &config{timeout: defaultTimeout, logger: obs.Nop()}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	if cfg.timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be positive, got %s", ErrInvalidOption, cfg.timeout)
	}

	switch {
	case cfg.redirects == RedirectRefuse && cfg.maxHops != 0:
		return nil, fmt.Errorf("%w: %s redirects take no hop limit, got %d", ErrInvalidOption, cfg.redirects, cfg.maxHops)
	case cfg.redirects == RedirectRevalidate && cfg.maxHops < 1:
		return nil, fmt.Errorf("%w: %s redirects need a hop limit of at least 1, got %d", ErrInvalidOption, cfg.redirects, cfg.maxHops)
	case cfg.redirects > RedirectRevalidate:
		return nil, fmt.Errorf("%w: unknown redirect policy %d", ErrInvalidOption, cfg.redirects)
	}

	return cfg, nil
}

// guardedTransport admits a request by scheme and hostname, then hands it to
// a transport whose every dial is SSRF-checked.
type guardedTransport struct {
	base      *http.Transport
	allowHTTP bool
	ssrfOpts  []ssrf.Option
}

func newGuardedTransport(cfg *config) (*guardedTransport, error) {
	ctx := context.Background()

	tlsConfig, err := resolveTLSConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	ssrfOpts := make([]ssrf.Option, 0, len(cfg.allowedHostnames)+2)
	if cfg.lookup != nil {
		ssrfOpts = append(ssrfOpts, ssrf.WithLookupFunc(cfg.lookup))
	}

	if cfg.allowPrivate {
		cfg.logger.Log(ctx, obs.LevelWarn, "security bypass active", "feature", "outbound_private_network")

		ssrfOpts = append(ssrfOpts, ssrf.WithAllowPrivateNetwork())
	}

	for _, hostname := range cfg.allowedHostnames {
		ssrfOpts = append(ssrfOpts, ssrf.WithAllowHostname(hostname))
	}

	dialer := &net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultDialKeepAlive}

	return &guardedTransport{
		base: &http.Transport{
			Proxy:                 nil,
			DialContext:           ssrf.DialContext(dialer, ssrfOpts...),
			TLSClientConfig:       tlsConfig,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          defaultMaxIdleConns,
			IdleConnTimeout:       defaultIdleConnTimeout,
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ExpectContinueTimeout: defaultExpectContinueTimeout,
		},
		allowHTTP: plaintextAllowed(ctx, cfg),
		ssrfOpts:  ssrfOpts,
	}, nil
}

func resolveTLSConfig(ctx context.Context, cfg *config) (*tls.Config, error) {
	tlsConfig := cfg.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	}

	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}

	if tlsConfig.InsecureSkipVerify {
		if !commons.AllowInsecureTLS() {
			return nil, fmt.Errorf("%w: set %s=true to permit", ErrInsecureTLSConfig, commons.EnvAllowInsecureTLS)
		}

		cfg.logger.Log(ctx, obs.LevelWarn, "security bypass active",
			"feature", "outbound_tls_skip_verify",
			"env_var", commons.EnvAllowInsecureTLS,
		)
	}

	return tlsConfig, nil
}

// plaintextAllowed applies the double key: the option and ALLOW_INSECURE_TLS.
func plaintextAllowed(ctx context.Context, cfg *config) bool {
	if !cfg.allowInsecureHTTP {
		return false
	}

	if !commons.AllowInsecureTLS() {
		cfg.logger.Log(ctx, obs.LevelError,
			"outbound plaintext HTTP allowance rejected; set "+commons.EnvAllowInsecureTLS+"=true to permit",
			"env_var", commons.EnvAllowInsecureTLS,
		)

		return false
	}

	cfg.logger.Log(ctx, obs.LevelWarn, "security bypass active",
		"feature", "outbound_insecure_http",
		"env_var", commons.EnvAllowInsecureTLS,
	)

	return true
}

// RoundTrip admits req, then sends it. A refused request's body is closed, as
// the RoundTripper contract requires.
func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		closeBody(req)

		return nil, ErrNilRequest
	}

	if err := g.admit(req); err != nil {
		closeBody(req)

		return nil, err
	}

	return g.base.RoundTrip(req)
}

func (g *guardedTransport) admit(req *http.Request) error {
	switch scheme := strings.ToLower(req.URL.Scheme); {
	case scheme == "https":
	case scheme == "http" && g.allowHTTP:
	default:
		return fmt.Errorf("%w: %q", ErrInsecureScheme, req.URL.Scheme)
	}

	return ssrf.ValidateURL(req.Context(), req.URL.String(), g.ssrfOpts...)
}

// CloseIdleConnections closes the underlying transport's idle connections, so
// [net/http.Client.CloseIdleConnections] reaches them.
func (g *guardedTransport) CloseIdleConnections() {
	g.base.CloseIdleConnections()
}

func closeBody(req *http.Request) {
	if req != nil && req.Body != nil {
		_ = req.Body.Close()
	}
}

func checkRedirect(policy RedirectPolicy, maxHops int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if policy == RedirectRevalidate && len(via) <= maxHops {
			return nil
		}

		if policy == RedirectRevalidate {
			return fmt.Errorf("%w: more than %d hops, next host %s", ErrRedirectRefused, maxHops, req.URL.Host)
		}

		return fmt.Errorf("%w: to host %s", ErrRedirectRefused, req.URL.Host)
	}
}

var _ http.RoundTripper = (*guardedTransport)(nil)
