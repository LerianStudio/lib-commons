package ratelimit

import (
	"context"
	"net/url"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
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
// than read operations on the same tier.
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

// TierResolver resolves the rate limit Tier for a given tenant and tier name.
// Implementations must be concurrency-safe and should not perform I/O in the hot path.
type TierResolver interface {
	// Resolve returns the Tier for a tenant + tier name.
	// Returns (tier, true) if tenant-specific config exists for that tier.
	// Returns (zero, false) if not configured — caller should use Fallback.
	Resolve(tenantID, tierName string) (Tier, bool)

	// Fallback returns the fallback Tier for a tier name.
	// Used when no tenant-specific config exists.
	Fallback(tierName string) Tier
}

// TenantMethodTierSelector returns a TierFunc that combines tenant identity
// and HTTP method to select the appropriate Tier.
//
// Resolution flow per request:
//  1. extractTenantID(c.UserContext()) — O(1), from JWT context
//  2. TierFromMethod(c.Method()) — POST/PUT/PATCH/DELETE → "aggressive", others → "default"
//  3. resolver.Resolve(tenantID, tier) — O(1), cache read
//  4. Falls back to resolver.Fallback(tier) when (3) returns false
//
// extractTenantID is injected to avoid importing tenant-manager packages.
// Replaces MethodTierSelector when multi-tenant rate limiting is enabled.
// The existing MethodTierSelector continues to work for services without tenant config.
func TenantMethodTierSelector(
	resolver TierResolver,
	extractTenantID func(ctx context.Context) string,
) TierFunc {
	if resolver == nil || extractTenantID == nil {
		fallback := DefaultTier()
		return func(_ *fiber.Ctx) Tier {
			return fallback
		}
	}

	return func(c *fiber.Ctx) Tier {
		tenantID := extractTenantID(c.UserContext())
		tierName := TierFromMethod(c.Method())

		if tenantID == "" {
			return resolver.Fallback(tierName)
		}

		if tier, ok := resolver.Resolve(tenantID, tierName); ok {
			return tier
		}

		return resolver.Fallback(tierName)
	}
}

// WithTenantResolver enables multi-tenant rate limiting on the RateLimiter.
// The resolver looks up tenant-specific tier configurations from cache.
// The extractTenantID function extracts the tenant ID from the request context
// (typically from JWT claims).
//
// When the resolver returns false for a tenant+tier, resolver.Fallback(tierName) is used.
// When resolver or extractTenantID is nil, ForTier uses local fallbacks (single-tenant).
//
// Example:
//
//	rl := ratelimit.New(conn,
//	    ratelimit.WithTenantResolver(resolver, core.GetTenantIDContext),
//	)
func WithTenantResolver(r TierResolver, extractTenantID func(context.Context) string) Option {
	return func(rl *RateLimiter) {
		rl.tierResolver = r
		rl.extractTenantID = extractTenantID
	}
}

// WithTierFallback registers a local fallback Tier by its Name.
// Used in single-tenant mode or as fallback when the TierResolver returns false.
// Overrides the built-in defaults ("default", "aggressive", "relaxed").
// The tier name is taken from tier.Name; if empty, the option is ignored.
//
// Example:
//
//	rl := ratelimit.New(conn,
//	    ratelimit.WithTierFallback(ratelimit.Tier{Name: "export", Max: 10, Window: 60 * time.Second}),
//	)
func WithTierFallback(tier Tier) Option {
	return func(rl *RateLimiter) {
		if tier.Name != "" {
			rl.tierFallbacks[tier.Name] = tier
		}
	}
}

// TierFromMethod maps HTTP methods to tier names.
// POST, PUT, PATCH, DELETE → "aggressive" (state-mutating operations).
// GET, HEAD, OPTIONS and all others → "default".
//
// Exported so that services building custom TierFunc implementations can reuse
// the canonical method-to-tier mapping without duplicating the switch statement.
func TierFromMethod(method string) string {
	switch method {
	case fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete:
		return "aggressive"
	default:
		return "default"
	}
}
