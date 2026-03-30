package ssrf

import (
	"context"
	"strings"
)

// LookupFunc is the signature for a DNS resolver function. It mirrors
// [net.Resolver.LookupHost] and is used by [WithLookupFunc] to inject a custom
// resolver for testing or special environments.
type LookupFunc func(ctx context.Context, host string) ([]string, error)

// Option configures the behaviour of [ValidateURL] and [ResolveAndValidate].
type Option func(*config)

// config holds the resolved configuration built from functional [Option] values.
type config struct {
	// httpsOnly rejects URLs whose scheme is not "https".
	httpsOnly bool

	// allowPrivate bypasses IP blocking entirely. Intended for local
	// development and testing only — never enable in production.
	allowPrivate bool

	// lookupFunc overrides the default DNS resolver. When nil, the default
	// net.DefaultResolver.LookupHost is used.
	lookupFunc LookupFunc

	// allowedHostnames exempts specific hostnames from [IsBlockedHostname]
	// checks. Keys are stored lower-cased.
	allowedHostnames map[string]bool
}

// buildConfig applies all options to a default config.
func buildConfig(opts []Option) *config {
	cfg := &config{}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// WithHTTPSOnly rejects any URL whose scheme is not "https". Use this when the
// target must always be contacted over TLS.
func WithHTTPSOnly() Option {
	return func(c *config) {
		c.httpsOnly = true
	}
}

// WithAllowPrivateNetwork bypasses IP blocking entirely, allowing connections
// to loopback, private, and link-local addresses. This is intended exclusively
// for local development and testing — never enable it in production.
func WithAllowPrivateNetwork() Option {
	return func(c *config) {
		c.allowPrivate = true
	}
}

// WithLookupFunc sets a custom DNS resolver. This is primarily useful in tests
// to avoid real DNS lookups and to exercise specific resolution scenarios (e.g.
// all IPs blocked, mixed safe/blocked IPs).
func WithLookupFunc(fn LookupFunc) Option {
	return func(c *config) {
		c.lookupFunc = fn
	}
}

// WithAllowHostname exempts a specific hostname from [IsBlockedHostname]
// checks. The comparison is case-insensitive. This is useful in corporate
// environments where a legitimate service runs on a ".internal" domain.
//
// Multiple calls accumulate — each call adds one hostname to the allow-list.
func WithAllowHostname(hostname string) Option {
	return func(c *config) {
		if c.allowedHostnames == nil {
			c.allowedHostnames = make(map[string]bool)
		}

		c.allowedHostnames[strings.ToLower(hostname)] = true
	}
}
