package ratelimit

import (
	"net/url"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/gofiber/fiber/v2"
)

// IdentityFunc extracts the client identity from a Fiber request context.
// The returned string is used as part of the Redis key for rate limiting.
type IdentityFunc func(c *fiber.Ctx) string

// Option configures a RateLimiter via functional options.
type Option func(*RateLimiter)

// WithLogger provides a structured logger for rate limiter warnings and errors.
// When not provided, a no-op logger is used.
func WithLogger(l log.Logger) Option {
	return func(rl *RateLimiter) {
		if l != nil {
			rl.logger = l
		}
	}
}

// WithKeyPrefix sets a service-specific prefix for Redis keys.
// For example, WithKeyPrefix("tenant-manager") produces keys like
// "tenant-manager:ratelimit:default:192.168.1.1".
// When not provided, keys have no prefix: "ratelimit:default:192.168.1.1".
func WithKeyPrefix(prefix string) Option {
	return func(rl *RateLimiter) {
		rl.keyPrefix = prefix
	}
}

// WithIdentityFunc sets a custom identity extractor for rate limiting.
// The identity function determines how clients are grouped for rate limiting.
// When not provided, IdentityFromIP() is used.
func WithIdentityFunc(fn IdentityFunc) Option {
	return func(rl *RateLimiter) {
		if fn != nil {
			rl.identityFunc = fn
		}
	}
}

// WithFailOpen controls the behavior when Redis is unavailable.
// When true (default), requests are allowed through on Redis failures.
// When false, requests receive a 503 Service Unavailable response on Redis failures.
func WithFailOpen(failOpen bool) Option {
	return func(rl *RateLimiter) {
		rl.failOpen = failOpen
	}
}

// WithOnLimited sets an optional callback that is invoked when a request exceeds the rate limit.
// This can be used for custom metrics, alerting, or logging beyond the built-in behavior.
func WithOnLimited(fn func(c *fiber.Ctx, tier Tier)) Option {
	return func(rl *RateLimiter) {
		rl.onLimited = fn
	}
}

// IdentityFromIP returns an IdentityFunc that extracts the client IP address.
// This is the default identity function.
func IdentityFromIP() IdentityFunc {
	return func(c *fiber.Ctx) string {
		return c.IP()
	}
}

// IdentityFromHeader returns an IdentityFunc that extracts the value of the given
// HTTP header, returned as "hdr:<url-encoded-value>". If the header is empty, it falls
// back to the client IP address encoded as "ip:<url-encoded-ip>". The type prefix
// prevents a header value that happens to equal an IP address from colliding with the
// IP-based fallback identity.
func IdentityFromHeader(header string) IdentityFunc {
	return func(c *fiber.Ctx) string {
		if val := c.Get(header); val != "" {
			return "hdr:" + url.QueryEscape(val)
		}

		return "ip:" + url.QueryEscape(c.IP())
	}
}

// IdentityFromIPAndHeader returns an IdentityFunc that combines the client IP address
// with the value of the given HTTP header. The resulting identity has the form
// "ip:<encodedIP>:hdr:<encodedHeaderValue>". Both components are URL-encoded so that
// IPv6 colons (encoded as %3A) cannot be confused with the structural colons used as
// field separators. If the header is empty, only the encoded IP is returned:
// "ip:<encodedIP>".
func IdentityFromIPAndHeader(header string) IdentityFunc {
	return func(c *fiber.Ctx) string {
		encodedIP := url.QueryEscape(c.IP())
		if val := c.Get(header); val != "" {
			return "ip:" + encodedIP + ":hdr:" + url.QueryEscape(val)
		}

		return "ip:" + encodedIP
	}
}

// WithRedisTimeout sets the timeout for Redis operations in the rate limiter.
// If a Redis operation does not complete within the timeout, it is treated as a Redis
// error and handled according to the fail-open/fail-closed policy (WithFailOpen).
// Default is 500ms. Values <= 0 are ignored.
func WithRedisTimeout(d time.Duration) Option {
	return func(rl *RateLimiter) {
		if d > 0 {
			rl.redisTimeout = d
		}
	}
}

// TierFunc selects a rate limit Tier for the incoming request.
// It is used with WithDynamicRateLimit to apply different limits per request attribute
// (e.g., HTTP method, path, or authenticated identity).
type TierFunc func(c *fiber.Ctx) Tier

// MethodTierSelector returns a TierFunc that applies different tiers based on HTTP method:
//   - write: applied to POST, PUT, PATCH, DELETE (state-mutating methods)
//   - read:  applied to GET, HEAD, OPTIONS and all other methods
//
// This mirrors the pattern where write operations are rate-limited more aggressively
// than read operations on the same endpoint group.
//
// Example:
//
//	rl.WithDynamicRateLimit(ratelimit.MethodTierSelector(
//	    ratelimit.AggressiveTier(),  // write
//	    ratelimit.DefaultTier(),     // read
//	))
func MethodTierSelector(write, read Tier) TierFunc {
	return func(c *fiber.Ctx) Tier {
		switch c.Method() {
		case fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete:
			return write
		default:
			return read
		}
	}
}
