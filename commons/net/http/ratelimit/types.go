package ratelimit

import (
	"context"
	"time"
)

// Limiter defines the interface for rate limiting implementations.
// This interface can be implemented by different backends (Redis, in-memory, etc.)
// and is designed to be framework-agnostic for reuse across services.
//
//go:generate mockgen --destination=../../../mocks/libcommons/ratelimit/limiter.mock.go --package=ratelimit . Limiter
type Limiter interface {
	// Allow checks if a request should be allowed based on the key.
	// Returns Result containing decision, remaining count, and reset time.
	// Error is returned only for system failures, not rate limit violations.
	Allow(ctx context.Context, key string) (*Result, error)

	// Reset clears the rate limit counter for a specific key.
	// Useful for administrative overrides or testing.
	Reset(ctx context.Context, key string) error

	// GetConfig returns the current limiter configuration.
	// This is useful for middleware to populate response headers.
	GetConfig() Config
}

// Config holds rate limiter configuration parameters.
// This struct is designed to be serializable and can be loaded from
// environment variables or configuration files.
type Config struct {
	// Max is the maximum number of requests allowed in the window
	Max int

	// Window is the time duration for the rate limit window
	Window time.Duration

	// KeyPrefix is prepended to all Redis keys for namespacing
	// Example: "ratelimit:auth" or "ratelimit:api"
	KeyPrefix string
}

// Result contains the outcome of a rate limit check.
// This struct is designed to provide all information needed
// to make decisions and populate HTTP headers.
type Result struct {
	// Allowed indicates if the request should be processed
	Allowed bool

	// Remaining is the number of requests remaining in the window
	// Will be 0 if rate limit is exceeded
	Remaining int

	// Limit is the maximum requests allowed (from Config.Max)
	Limit int

	// ResetAt is when the rate limit window resets
	ResetAt time.Time

	// RetryAfter is duration until the rate limit resets
	// Useful for Retry-After header
	RetryAfter time.Duration
}

// KeyGenerator is a function type that generates rate limit keys
// from request context. This allows flexible rate limiting strategies:
// - By IP: func(ctx) string { return "ip:" + getIP(ctx) }
// - By User: func(ctx) string { return "user:" + getUserID(ctx) }
// - By Organization: func(ctx) string { return "org:" + getOrgID(ctx) }
// - By API Key: func(ctx) string { return "apikey:" + getAPIKey(ctx) }
// - Composite: func(ctx) string { return "ip:" + getIP(ctx) + ":user:" + getUserID(ctx) }
type KeyGenerator func(ctx context.Context) string

// Strategy defines different rate limiting strategies.
// This enum can be extended as new strategies are needed.
type Strategy string

const (
	// StrategySlidingWindow uses sliding time windows (more accurate, prevents bursts)
	StrategySlidingWindow Strategy = "sliding_window"

	// StrategyFixedWindow uses fixed time windows (simple but can have burst issues)
	StrategyFixedWindow Strategy = "fixed_window"
)

// FailureMode defines how the rate limiter behaves on system errors.
type FailureMode string

const (
	// FailOpen allows requests through when rate limiter fails (prioritizes availability)
	FailOpen FailureMode = "fail_open"

	// FailClosed blocks requests when rate limiter fails (prioritizes security)
	FailClosed FailureMode = "fail_closed"
)

// ErrorHandler is a function type for handling rate limiter errors.
// This allows custom error handling strategies (logging, metrics, alerts).
// Returns true to allow the request, false to block it.
type ErrorHandler func(ctx context.Context, err error, failureMode FailureMode) bool
