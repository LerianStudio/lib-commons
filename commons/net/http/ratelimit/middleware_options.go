package ratelimit

import (
	"net/url"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
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
		if !nilcheck.Interface(l) {
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
//
// Anything WithOnLimited writes to c is clobbered by the subsequent 429 body write (whether
// the default chttp.ErrorResponse responder or a WithExceededHandler responder), so this hook
// is for side effects only. Use WithExceededHandler to replace the response body.
func WithOnLimited(fn func(c *fiber.Ctx, tier Tier)) Option {
	return func(rl *RateLimiter) {
		rl.onLimited = fn
	}
}

// WithExceededHandler installs a consumer-controlled responder for the 429 (rate-limit-exceeded)
// path. When set, the middleware:
//
//  1. sets Retry-After / X-RateLimit-* headers as it does today,
//  2. fires the WithOnLimited callback (if configured) for side-effect metrics/alerting,
//  3. invokes fn(c, tier, ttl) to write the response body. fn's return value propagates to
//     the Fiber chain as the handler's error return.
//
// When unset (default), the middleware continues to call chttp.Respond with the built-in
// {Code, Title, Message} envelope verbatim — fully backward-compatible with v5.1.0.
//
// The ttl argument is the actual remaining TTL of the Redis counter key (not the configured
// Window), so consumers that surface a retry-after-millis field in their envelope get the
// same accuracy as the built-in Retry-After header.
//
// WithExceededHandler ONLY governs the 429 body. The 503 fail-closed path (handleRedisError)
// and the 503 policy-blocked path (blockedHandler) are NOT affected and keep their built-in
// chttp.ErrorResponse responders — those have distinct semantics (Redis unavailable vs.
// strict-tier security violation vs. rate exceeded) and should not share a body responder.
//
// Passing nil is a no-op: the option leaves any previously-installed handler in place.
func WithExceededHandler(fn func(c *fiber.Ctx, tier Tier, ttl time.Duration) error) Option {
	return func(rl *RateLimiter) {
		if fn != nil {
			rl.exceededHandler = fn
		}
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
// "ip:<encodedIP>#hdr:<encodedHeaderValue>". Both components are URL-encoded so that
// IPv6 colons (encoded as %3A) and '#' characters (encoded as %23) cannot appear as
// raw values, making '#' an unambiguous inter-component separator. If the header is
// empty, only the encoded IP is returned: "ip:<encodedIP>".
func IdentityFromIPAndHeader(header string) IdentityFunc {
	return func(c *fiber.Ctx) string {
		encodedIP := url.QueryEscape(c.IP())
		if val := c.Get(header); val != "" {
			return "ip:" + encodedIP + "#hdr:" + url.QueryEscape(val)
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
