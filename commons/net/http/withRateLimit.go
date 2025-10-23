package http

import (
	"fmt"
	"slices"

	cn "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/LerianStudio/lib-commons/v2/commons/net/http/ratelimit"
	"github.com/gofiber/fiber/v2"
)

// RateLimitConfig configures the rate limiting middleware for Fiber.
// This is a generic configuration that can be used across all Fiber-based services.
type RateLimitConfig struct {
	// Limiter is the rate limiting implementation (usually Redis-backed)
	Limiter ratelimit.Limiter

	// KeyGenerator extracts the rate limit key from the request
	// Examples:
	//   - By IP: func(c) string { return c.IP() }
	//   - By User: func(c) string { return extractUserID(c) }
	//   - By API Key: func(c) string { return c.Get("X-API-Key") }
	KeyGenerator func(c *fiber.Ctx) string

	// SkipPaths defines paths that bypass rate limiting
	// Typically: health checks, metrics endpoints
	SkipPaths []string

	// FailureMode determines behavior when rate limiter fails
	// FailOpen: Allow requests (prioritize availability)
	// FailClosed: Block requests (prioritize security)
	FailureMode ratelimit.FailureMode

	// ErrorCode is returned when rate limit is exceeded
	ErrorCode string

	// ErrorMessage is the human-readable error message
	ErrorMessage string

	// IncludeHeaders controls whether to add rate limit headers
	// Standard headers: X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset
	IncludeHeaders bool

	// Logger is an optional function for logging rate limit events
	// If nil, no logging is performed (service can inject its own logger)
	Logger func(level string, format string, args ...any)

	// OnRateLimitExceeded is an optional callback when rate limit is exceeded
	// Useful for metrics, alerts, or custom actions
	OnRateLimitExceeded func(c *fiber.Ctx, key string, result *ratelimit.Result)

	// OnError is an optional callback when rate limiter encounters errors
	// Useful for metrics, alerts, or custom error handling
	OnError func(c *fiber.Ctx, err error)
}

// RateLimitMiddleware creates a Fiber middleware for rate limiting.
// This middleware:
// 1. Extracts rate limit key from request (via KeyGenerator)
// 2. Checks rate limit using the Limiter
// 3. Sets standard rate limit response headers
// 4. Blocks request if rate limit exceeded
// 5. Handles errors gracefully based on FailureMode
//
// This is a generic implementation suitable for any Fiber-based service.
func RateLimitMiddleware(config RateLimitConfig) fiber.Handler {
	applyDefaults(&config)

	return func(c *fiber.Ctx) error {
		ctx := c.UserContext()

		// Check if path should skip rate limiting
		if shouldSkipPath(c.Path(), config.SkipPaths) {
			return c.Next()
		}

		// Extract rate limit key from request
		key := config.KeyGenerator(c)
		if key == "" {
			// If key generator returns empty, skip rate limiting
			// This allows conditional rate limiting
			return c.Next()
		}

		// Check rate limit
		result, err := config.Limiter.Allow(ctx, key)

		// Handle rate limiter errors
		if err != nil {
			return handleRateLimiterError(c, config, err, key)
		}

		// Set rate limit headers if enabled
		if config.IncludeHeaders {
			setRateLimitHeaders(c, result)
		}

		// Check if rate limit exceeded
		if !result.Allowed {
			return handleRateLimitExceeded(c, config, key, result)
		}

		// Rate limit check passed, continue processing
		return c.Next()
	}
}

// applyDefaults sets default values for the rate limit configuration.
func applyDefaults(config *RateLimitConfig) {
	if config.KeyGenerator == nil {
		// Default: rate limit by IP address
		config.KeyGenerator = func(c *fiber.Ctx) string {
			return c.IP()
		}
	}

	if config.FailureMode == "" {
		config.FailureMode = ratelimit.FailOpen
	}

	if config.ErrorCode == "" {
		config.ErrorCode = "RATE_LIMIT_EXCEEDED"
	}

	if config.ErrorMessage == "" {
		config.ErrorMessage = "Too many requests. Please try again later."
	}

	// Default to including headers
	if !config.IncludeHeaders {
		config.IncludeHeaders = true
	}
}

// handleRateLimiterError handles errors from the rate limiter.
// Behavior depends on the configured FailureMode:
// - FailOpen: Allow request but log error
// - FailClosed: Block request with 503 Service Unavailable
func handleRateLimiterError(c *fiber.Ctx, config RateLimitConfig, err error, key string) error {
	if config.Logger != nil {
		config.Logger("error", "Rate limiter error: %v (key: %s, mode: %s)", err, key, config.FailureMode)
	}

	if config.OnError != nil {
		config.OnError(c, err)
	}

	// Behavior depends on failure mode
	if config.FailureMode == ratelimit.FailOpen {
		// Fail open: allow request but log error
		if config.Logger != nil {
			config.Logger("warn", "Rate limiter failed open, allowing request")
		}

		return c.Next()
	}

	// Fail closed: block request with 503 Service Unavailable
	if config.Logger != nil {
		config.Logger("warn", "Rate limiter failed closed, blocking request")
	}

	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
		"code":    "SERVICE_UNAVAILABLE",
		"message": "Service temporarily unavailable. Please try again later.",
	})
}

// handleRateLimitExceeded handles the case when rate limit is exceeded.
// Returns 429 Too Many Requests with Retry-After header.
func handleRateLimitExceeded(c *fiber.Ctx, config RateLimitConfig, key string, result *ratelimit.Result) error {
	if config.Logger != nil {
		config.Logger("warn", "Rate limit exceeded for key: %s (limit: %d, remaining: %d)",
			key, result.Limit, result.Remaining)
	}

	if config.OnRateLimitExceeded != nil {
		config.OnRateLimitExceeded(c, key, result)
	}

	// Set Retry-After header (RFC 6585)
	retryAfterSeconds := max(int(result.RetryAfter.Seconds()), 1)

	c.Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))

	// Return 429 Too Many Requests with generic error structure
	return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
		"code":    config.ErrorCode,
		"message": config.ErrorMessage,
	})
}

// setRateLimitHeaders sets standard rate limit headers on the response.
// These headers help clients implement proper backoff and retry logic.
// Headers follow industry standards (GitHub, Twitter, Stripe conventions):
// - X-RateLimit-Limit: Maximum requests allowed in the window
// - X-RateLimit-Remaining: Requests remaining in current window
// - X-RateLimit-Reset: Unix timestamp when the window resets
func setRateLimitHeaders(c *fiber.Ctx, result *ratelimit.Result) {
	c.Set(cn.RateLimitLimit, fmt.Sprintf("%d", result.Limit))
	c.Set(cn.RateLimitRemaining, fmt.Sprintf("%d", result.Remaining))
	c.Set(cn.RateLimitReset, fmt.Sprintf("%d", result.ResetAt.Unix()))
}

// shouldSkipPath checks if a path should bypass rate limiting.
// Exact path matching is used for performance.
func shouldSkipPath(path string, skipPaths []string) bool {
	return slices.Contains(skipPaths, path)
}

// Helper functions for common key generation strategies
// These can be used directly or as building blocks for custom strategies

// KeyByIP generates rate limit key from client IP address.
// This is the most common strategy and protects against IP-based attacks.
func KeyByIP(c *fiber.Ctx) string {
	return fmt.Sprintf("ip:%s", c.IP())
}

// KeyByIPAndEndpoint generates rate limit key from IP and endpoint path.
// This allows different rate limits per endpoint while still tracking per-IP.
func KeyByIPAndEndpoint(c *fiber.Ctx) string {
	return fmt.Sprintf("ip:%s:endpoint:%s", c.IP(), c.Path())
}

// KeyByHeader generates rate limit key from a specific header.
// Useful for API keys, tenant IDs, or custom identifiers.
func KeyByHeader(headerName string) func(*fiber.Ctx) string {
	return func(c *fiber.Ctx) string {
		value := c.Get(headerName)

		if value != "" {
			return fmt.Sprintf("header:%s:%s", headerName, value)
		}

		// Fallback to IP if header not present
		return KeyByIP(c)
	}
}

// KeyComposite generates a composite key from multiple dimensions.
// This allows hierarchical rate limiting (e.g., limit per IP AND per user).
// Example usage:
//
//	KeyComposite(
//	  func(c) string { return "ip:" + c.IP() },
//	  func(c) string { return "endpoint:" + c.Path() },
//	)
func KeyComposite(generators ...func(*fiber.Ctx) string) func(*fiber.Ctx) string {
	return func(c *fiber.Ctx) string {
		var key string

		for i, gen := range generators {
			part := gen(c)

			if part == "" {
				continue
			}

			if i > 0 && key != "" {
				key += ":"
			}

			key += part
		}

		return key
	}
}
