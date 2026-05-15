// Package ratelimit provides distributed rate limiting for Fiber HTTP servers backed
// by Redis. It uses a fixed-window counter implemented as an atomic Lua script
// (INCR + PEXPIRE) to guarantee that no key is left without a TTL even under
// concurrent load or connection failures.
//
// # Quick start
//
//	conn, _ := redis.New(ctx, cfg)
//
//	rl := ratelimit.New(conn,
//	    ratelimit.WithKeyPrefix("my-service"),
//	    ratelimit.WithLogger(logger),
//	)
//
//	// Fixed tier — applied globally
//	app.Use(rl.WithRateLimit(ratelimit.DefaultTier()))
//
//	// Dynamic tier — write operations are rate-limited more aggressively
//	app.Use(rl.WithDynamicRateLimit(ratelimit.MethodTierSelector(
//	    ratelimit.AggressiveTier(), // POST, PUT, PATCH, DELETE
//	    ratelimit.DefaultTier(),    // GET, HEAD, OPTIONS
//	)))
//
// # Nil-safe usage
//
// New returns nil when the rate limiter is disabled (RATE_LIMIT_ENABLED=false)
// or when the Redis connection is nil. A nil *RateLimiter is always safe to use:
// WithRateLimit and WithDynamicRateLimit return a pass-through handler that calls
// c.Next() without enforcing any limit.
//
// # Identity functions
//
// The identity function determines how clients are grouped for rate limiting.
// IdentityFromIPAndHeader combines the client IP with an HTTP header value using
// a # separator — not : — to avoid ambiguity with IPv6 addresses (e.g.
// "2001:db8::1#tenant-abc" instead of "2001:db8::1:tenant-abc").
//
// # Redis key format
//
// Keys follow the pattern: [prefix:]ratelimit:<tier>:<identity>
// Example: "my-service:ratelimit:default:192.168.1.1"
//
// # Error responses
//
// By default, exceeded requests return the historical lib-commons
// ErrorResponse body. WithExceededHandler lets services keep the distributed
// rate-limit middleware while writing their own 429 envelope after the
// middleware has set Retry-After and X-RateLimit-* headers. WithOnLimited
// remains a side-effect hook for metrics/logging and is invoked before the
// exceeded handler.
//
// # Storage primitives
//
// RedisStorage.Increment exposes the package's atomic fixed-window counter
// primitive for services that need custom middleware ergonomics but still want
// the same Redis correctness guarantees. The method performs INCR + PEXPIRE in
// one Lua EVAL and returns the post-increment count plus the remaining TTL from
// the same Redis round trip.
package ratelimit
