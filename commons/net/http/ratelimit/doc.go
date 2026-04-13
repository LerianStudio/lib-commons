// Package ratelimit provides distributed rate limiting for Fiber HTTP servers backed
// by Redis. It uses a fixed-window counter implemented as an atomic Lua script
// (INCR + PEXPIRE) to guarantee that no key is left without a TTL even under
// concurrent load or connection failures.
//
// # Quick start — single-tenant
//
//	conn, _ := redis.New(ctx, cfg)
//
//	rl := ratelimit.New(conn,
//	    ratelimit.WithKeyPrefix("my-service"),
//	    ratelimit.WithLogger(logger),
//	    ratelimit.WithTierFallback(ratelimit.Tier{Name: "export", Max: 10, Window: 60 * time.Second}),
//	)
//
//	// Per-route tier selection
//	v1.Get("/accounts", rl.ForTier("default"), h.ListAccounts)
//	v1.Post("/transactions", rl.ForTier("aggressive"), h.CreateTransaction)
//	export := v1.Group("/export", rl.ForTier("export"))
//
// # Quick start — multi-tenant
//
//	rl := ratelimit.New(conn,
//	    ratelimit.WithKeyPrefix("my-service"),
//	    ratelimit.WithLogger(logger),
//	    ratelimit.WithTenantResolver(resolver, core.GetTenantIDContext),
//	    ratelimit.WithTierFallback(ratelimit.Tier{Name: "export", Max: 10, Window: 60 * time.Second}),
//	)
//
//	// Same routes — ForTier resolves tenant-specific limits from cache,
//	// falling back to local tiers when the tenant has no specific config.
//	v1.Get("/accounts", rl.ForTier("default"), h.ListAccounts)
//	v1.Post("/transactions", rl.ForTier("aggressive"), h.CreateTransaction)
//	export := v1.Group("/export", rl.ForTier("export"))
//
// # Legacy API
//
// WithRateLimit(tier) and WithDynamicRateLimit(TierFunc) remain available for
// backward compatibility. ForTier is the recommended API for new code as it
// supports both single-tenant and multi-tenant modes with the same route definitions.
//
// # Nil-safe usage
//
// New returns nil when the rate limiter is disabled (RATE_LIMIT_ENABLED=false)
// or when the Redis connection is nil. A nil *RateLimiter is always safe to use:
// ForTier, WithRateLimit, and WithDynamicRateLimit return a pass-through handler
// that calls c.Next() without enforcing any limit.
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
package ratelimit
