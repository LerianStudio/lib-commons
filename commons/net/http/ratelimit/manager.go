package ratelimit

import (
	"context"
	"fmt"
	"slices"

	"github.com/LerianStudio/lib-commons/v2/commons"
	"github.com/gofiber/fiber/v2"
)

// Manager orchestrates multiple rate limiters for different use cases.
// It provides a flexible, configuration-driven approach where services
// can define their own rate limiting strategies without modifying libcommons.
//
// Example usage:
//
//	manager := ratelimit.NewManager(ratelimit.ManagerConfig{
//	    Limiters: map[string]ratelimit.Limiter{
//	        "global": globalLimiter,
//	        "auth":   authLimiter,
//	    },
//	    GlobalError: myGlobalError,
//	})
type Manager struct {
	limiters    map[string]Limiter
	globalError *commons.RateLimitError
}

// ManagerConfig holds the configuration for initializing a Manager.
// This uses dependency injection - you provide the Limiter instances.
type ManagerConfig struct {
	// Limiters maps limiter names to their Limiter instances
	// Example: {"auth": authLimiter, "permission": permLimiter}
	// Use NewManagerWithRedis() helper for convenient Redis-based setup
	Limiters map[string]Limiter

	// GlobalError defines the error response for global rate limit exceeded
	GlobalError *commons.RateLimitError
}

// NewManager creates a new rate limit manager with pre-configured Limiter instances.
// This uses dependency injection for maximum flexibility and testability.
//
// Example:
//
//	manager := ratelimit.NewManager(ratelimit.ManagerConfig{
//	    Limiters: map[string]ratelimit.Limiter{
//	        "global": globalLimiter,
//	        "auth":   authLimiter,
//	    },
//	    GlobalError: myGlobalError,
//	})
//
// For Redis-based setup, use the NewManagerWithRedis() helper function.
func NewManager(config ManagerConfig) *Manager {
	limiters := config.Limiters
	if limiters == nil {
		limiters = make(map[string]Limiter)
	}

	return &Manager{
		limiters:    limiters,
		globalError: config.GlobalError,
	}
}

// GetLimiter returns a limiter by name.
// Returns nil if the limiter doesn't exist.
// This allows services to access limiters directly for advanced use cases.
func (m *Manager) GetLimiter(name string) Limiter {
	return m.limiters[name]
}

// Middleware creates a rate limiting middleware for a specific limiter.
// Parameters:
//   - limiterName: the name of the limiter to use (must exist in config)
//   - keyGen: function to generate rate limit keys from requests
//   - opts: optional configuration overrides
//
// This is the power-user method that gives full control over rate limiting behavior.
func (m *Manager) Middleware(limiterName string, keyGen func(*fiber.Ctx) string, opts MiddlewareOptions) fiber.Handler {
	limiter := m.limiters[limiterName]

	if limiter == nil {
		// If limiter doesn't exist, return a no-op middleware
		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	// Apply defaults
	if opts.FailureMode == "" {
		opts.FailureMode = FailOpen
	}

	return createMiddleware(limiter, keyGen, opts)
}

// GlobalMiddleware is a convenience method for global rate limiting.
// Uses the "global" limiter (if configured) with IP-based key generation.
// This is a universally applicable pattern that all services can use.
// By default, skips /health and /version endpoints to ensure they remain accessible.
func (m *Manager) GlobalMiddleware(opts ...MiddlewareOptions) fiber.Handler {
	opt := MiddlewareOptions{
		IncludeHeaders: true,
		SkipPaths:      []string{"/health", "/version"}, // Always skip health checks
		RateLimitError: m.globalError,                   // Use the global error from manager config
	}

	if len(opts) > 0 {
		opt = opts[0]
		// Preserve default skip paths if not explicitly overridden
		if len(opt.SkipPaths) == 0 {
			opt.SkipPaths = []string{"/health", "/version"}
		}
		// Ensure IncludeHeaders defaults to true
		if len(opts) > 0 && !opts[0].IncludeHeaders {
			opt.IncludeHeaders = opts[0].IncludeHeaders
		}
		// Use manager's globalError if not explicitly provided
		if opt.RateLimitError == nil {
			opt.RateLimitError = m.globalError
		}
	}

	return m.Middleware("global", func(c *fiber.Ctx) string {
		return fmt.Sprintf("ip:%s", c.IP())
	}, opt)
}

// Reset clears rate limit data for a specific key across all limiters.
// Useful for administrative overrides or testing.
func (m *Manager) Reset(ctx context.Context, limiterName, key string) error {
	limiter := m.limiters[limiterName]

	if limiter == nil {
		return fmt.Errorf("limiter %s not found", limiterName)
	}

	return limiter.Reset(ctx, key)
}

// ResetAll clears rate limit data for a key across all limiters.
func (m *Manager) ResetAll(ctx context.Context, key string) error {
	for name, limiter := range m.limiters {
		if err := limiter.Reset(ctx, key); err != nil {
			return fmt.Errorf("failed to reset limiter %s: %w", name, err)
		}
	}

	return nil
}

// ListLimiters returns the names of all configured limiters.
// Useful for debugging and administrative interfaces.
func (m *Manager) ListLimiters() []string {
	names := make([]string, 0, len(m.limiters))

	for name := range m.limiters {
		names = append(names, name)
	}

	return names
}

// MiddlewareOptions configures how the middleware behaves.
// This is separate from rate limit Config to allow behavioral overrides
// without changing the underlying rate limit parameters.
type MiddlewareOptions struct {
	// SkipPaths defines paths that bypass rate limiting
	SkipPaths []string

	// FailureMode determines behavior on system errors
	FailureMode FailureMode

	// RateLimitError defines the complete error response structure
	RateLimitError *commons.RateLimitError

	// IncludeHeaders controls X-RateLimit-* headers
	IncludeHeaders bool

	// Logger for rate limit events (optional)
	Logger func(level, format string, args ...interface{})

	// OnRateLimitExceeded callback (optional)
	OnRateLimitExceeded func(c *fiber.Ctx, key string, result *Result)

	// OnError callback (optional)
	OnError func(c *fiber.Ctx, err error)
}

// createMiddleware is the internal middleware factory.
// Separated for testing and code organization.
func createMiddleware(limiter Limiter, keyGen func(*fiber.Ctx) string, opts MiddlewareOptions) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := c.UserContext()

		// Check skip paths
		if slices.Contains(opts.SkipPaths, c.Path()) {
			return c.Next()
		}

		// Generate key
		key := keyGen(c)

		if key == "" {
			return c.Next()
		}

		// Check rate limit
		result, err := limiter.Allow(ctx, key)
		if err != nil {
			if opts.Logger != nil {
				opts.Logger("error", "Rate limiter error: %v", err)
			}

			if opts.OnError != nil {
				opts.OnError(c, err)
			}

			// Handle based on failure mode
			if opts.FailureMode == FailOpen {
				return c.Next()
			}

			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"code":    "SERVICE_UNAVAILABLE",
				"message": "Service temporarily unavailable",
			})
		}

		// Set headers
		if opts.IncludeHeaders {
			c.Set("X-RateLimit-Limit", fmt.Sprintf("%d", result.Limit))
			c.Set("X-RateLimit-Remaining", fmt.Sprintf("%d", result.Remaining))
			c.Set("X-RateLimit-Reset", fmt.Sprintf("%d", result.ResetAt.Unix()))
		}

		// Check if allowed
		if !result.Allowed {
			if opts.Logger != nil {
				opts.Logger("warn", "Rate limit exceeded: %s", key)
			}

			if opts.OnRateLimitExceeded != nil {
				opts.OnRateLimitExceeded(c, key, result)
			}

			retryAfter := max(int(result.RetryAfter.Seconds()), 1)

			c.Set("Retry-After", fmt.Sprintf("%d", retryAfter))

			// Build error response from RateLimitError
			// TODO: Use the error from lib-commons constants and validateBusinessError as fallback
			errorResponse := fiber.Map{
				"code":    "RATE_LIMIT_EXCEEDED",
				"title":   "Rate Limit Exceeded",
				"message": "Too many requests. Please try again later.",
			}

			if opts.RateLimitError != nil {
				errorResponse["code"] = opts.RateLimitError.Code
				errorResponse["title"] = opts.RateLimitError.Title
				errorResponse["message"] = opts.RateLimitError.Message
			}

			return c.Status(fiber.StatusTooManyRequests).JSON(errorResponse)
		}

		return c.Next()
	}
}
