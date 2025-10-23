package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v3/commons"
	"github.com/LerianStudio/lib-commons/v3/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

// RedisConnectionGetter is an interface for getting Redis clients.
// This allows libcommons to work with any Redis connection manager
// without importing specific packages like lib-commons/redis.
type RedisConnectionGetter interface {
	GetClient(ctx context.Context) (redis.UniversalClient, error)
}

// LimiterFactory is a function that creates a Limiter instance.
// This allows users to provide their own limiter creation logic
// without factory.go needing to know about specific implementations.
//
// For Redis-backed rate limiting, use the pre-built factory:
//
//	import "github.com/LerianStudio/lib-commons/v3/commons/redis"
//
//	LimiterFactory: redis.Factory
//
// For custom implementations, define your own:
//
//	LimiterFactory: func(client redis.UniversalClient, config ratelimit.Config) ratelimit.Limiter {
//	    return myCustomLimiter.New(client, config)
//	}
type LimiterFactory func(client redis.UniversalClient, config Config) Limiter

// GlobalHandlerConfig holds the configuration for a simple global rate limiter.
// This is designed for plug-and-play usage in applications that only need
// basic global rate limiting without custom per-endpoint limits.
type GlobalHandlerConfig struct {
	// Enabled controls whether rate limiting is active
	Enabled bool

	// GlobalMax is the maximum number of requests allowed per window
	GlobalMax int

	// GlobalWindowSec is the time window in seconds
	GlobalWindowSec int

	// KeyPrefix is prepended to all Redis keys for namespacing
	KeyPrefix string

	// GlobalError defines the error response returned when rate limit is exceeded
	// Use pkg.ValidateRateLimitError() or similar method to create this error
	GlobalError *commons.RateLimitError

	// RedisConnection is the Redis connection manager
	RedisConnection RedisConnectionGetter

	// LimiterFactory creates Limiter instances. Required when Enabled is true.
	// Example: func(client, config) { return redis.NewRedisLimiter(client, config) }
	LimiterFactory LimiterFactory
}

// GlobalHandler provides a simple handler for global rate limiting only.
// This mirrors the pattern used in plugin-auth's RateLimitHandler but
// only provides global middleware (no auth/permission specific limiters).
type GlobalHandler struct {
	manager *Manager
	config  *GlobalHandlerConfig
	logger  log.Logger
}

// NewGlobalHandler creates a new global rate limit handler.
// This is the plug-and-play version for applications that only need global rate limiting.
// If rate limiting is enabled and initialization fails, this function panics.
// If rate limiting is disabled, returns a handler with no-op middleware.
//
// Example usage:
//
//	import (
//	    libRedis "github.com/LerianStudio/lib-commons/v3/commons/redis"
//	    libRateLimit "github.com/LerianStudio/lib-commons/v3/commons/net/http/ratelimit"
//	)
//
//	handler := libRateLimit.NewGlobalHandler(&libRateLimit.GlobalHandlerConfig{
//	    Enabled:         true,
//	    GlobalMax:       100,
//	    GlobalWindowSec: 60,
//	    KeyPrefix:       "myapp:ratelimit",
//	    GlobalError:     pkg.ValidateRateLimitError(constant.ErrRateLimitExceeded, ""),
//	    RedisConnection: redisConnection,
//	    LimiterFactory:  libRedis.Factory,
//	}, logger)
//
//	// In routes:
//	app.Use(handler.GlobalMiddleware())
func NewGlobalHandler(cfg *GlobalHandlerConfig, lg log.Logger) *GlobalHandler {
	// If rate limiting is disabled, return handler with nil manager
	if !cfg.Enabled {
		if lg != nil {
			lg.Info("Global rate limiting is disabled")
		}

		return &GlobalHandler{
			manager: nil,
			config:  cfg,
			logger:  lg,
		}
	}

	// Validate required configuration
	if cfg.LimiterFactory == nil {
		if lg != nil {
			lg.Error("LimiterFactory is required when rate limiting is enabled")
		}

		panic("LimiterFactory is required when rate limiting is enabled")
	}

	if lg == nil {
		panic("Logger is required for GlobalHandler")
	}

	lg.Infof("Initializing global rate limiter: max=%d requests per %d seconds, prefix=%s",
		cfg.GlobalMax, cfg.GlobalWindowSec, cfg.KeyPrefix)

	// Rate limiting is enabled, so we must initialize properly or panic
	client, err := cfg.RedisConnection.GetClient(context.Background())
	if err != nil {
		lg.Errorf("Failed to get Redis client for rate limiting: %v", err)
		panic(fmt.Sprintf("Failed to get Redis client for rate limiting: %v", err))
	}

	lg.Info("Successfully connected to Redis for rate limiting")

	// Create global limiter using the factory
	globalLimiter := cfg.LimiterFactory(client, Config{
		Max:       cfg.GlobalMax,
		Window:    time.Duration(cfg.GlobalWindowSec) * time.Second,
		KeyPrefix: cfg.KeyPrefix + ":global",
	})

	lg.Info("Global rate limiter created successfully")

	// Create manager with only the global limiter
	manager := NewManager(ManagerConfig{
		Limiters: map[string]Limiter{
			"global": globalLimiter,
		},
		GlobalError: cfg.GlobalError,
	})

	lg.Info("Global rate limit handler initialized successfully")

	return &GlobalHandler{
		manager: manager,
		config:  cfg,
		logger:  lg,
	}
}

// GlobalMiddleware returns the global rate limit middleware.
// Returns a no-op middleware if rate limiting is disabled.
func (h *GlobalHandler) GlobalMiddleware() fiber.Handler {
	if !h.config.Enabled || h.manager == nil {
		return func(c *fiber.Ctx) error {
			if h.logger != nil {
				h.logger.Warn("Rate limiting is disabled")
			}

			return c.Next()
		}
	}

	return h.manager.GlobalMiddleware(MiddlewareOptions{
		IncludeHeaders: true,
		Logger:         h.createLogger(),
		OnRateLimitExceeded: func(c *fiber.Ctx, key string, result *Result) {
			ctx := c.UserContext()
			logger, _, _, _ := commons.NewTrackingFromContext(ctx)

			logger.Warnf("Global rate limit exceeded - IP: %s, Endpoint: %s, Limit: %d/%ds",
				c.IP(), c.Path(), result.Limit, h.config.GlobalWindowSec)
		},
		OnError: h.createErrorHandler(),
	})
}

// GetManager returns the underlying manager.
// Useful for advanced use cases. Returns nil if rate limiting is disabled.
func (h *GlobalHandler) GetManager() *Manager {
	return h.manager
}

// createLogger creates a logger function adapter for the rate limiter.
func (h *GlobalHandler) createLogger() func(level, format string, args ...any) {
	if h.logger == nil {
		// Return no-op logger if none configured
		return func(level, format string, args ...any) {}
	}

	return func(level, format string, args ...any) {
		switch level {
		case "error":
			h.logger.Errorf(format, args...)
		case "warn":
			h.logger.Warnf(format, args...)
		default:
			h.logger.Infof(format, args...)
		}
	}
}

// createErrorHandler creates an error handler for rate limiter errors.
func (h *GlobalHandler) createErrorHandler() func(c *fiber.Ctx, err error) {
	return func(c *fiber.Ctx, err error) {
		ctx := c.UserContext()
		logger, _, _, _ := commons.NewTrackingFromContext(ctx)

		logger.Errorf("Global rate limiter system error - IP: %s, Endpoint: %s, Error: %v",
			c.IP(), c.Path(), err)

		// TODO: Add OpenTelemetry metric for rate limiter errors
		// metric.rateLimitErrors.Add(ctx, 1, attribute.String("error", err.Error()))
	}
}
