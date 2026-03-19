package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/assert"
	constant "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	chttp "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	libRedis "github.com/LerianStudio/lib-commons/v4/commons/redis"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	// headerRetryAfter is the standard HTTP Retry-After header.
	headerRetryAfter = "Retry-After"

	// fallback values when environment variables are not set.
	fallbackDefaultMax     = 500
	fallbackAggressiveMax  = 100
	fallbackRelaxedMax     = 1000
	fallbackWindowSec      = 60
	fallbackRedisTimeoutMS = 500

	// rateLimitTitle is the error title returned when rate limit is exceeded.
	rateLimitTitle = "rate_limit_exceeded"
	// rateLimitMessage is the error message returned when rate limit is exceeded.
	rateLimitMessage = "rate limit exceeded"

	// serviceUnavailableTitle is the error title returned when Redis is unavailable and fail-closed.
	serviceUnavailableTitle = "service_unavailable"
	// serviceUnavailableMessage is the error message returned when Redis is unavailable and fail-closed.
	serviceUnavailableMessage = "rate limiter temporarily unavailable"

	// maxReasonableTierMax is the threshold above which a configuration warning is logged.
	maxReasonableTierMax = 100_000

	// luaIncrExpire is an atomic Lua script that increments the counter, sets expiry on the
	// first request in a window, and returns both the current count and the remaining TTL in
	// milliseconds. Executed atomically by Redis — no other command can interleave, eliminating
	// the race condition present in sequential INCR + EXPIRE calls. Returning the TTL from the
	// same script avoids an extra PTTL roundtrip and ensures the value is consistent with the
	// counter read above.
	luaIncrExpire = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[1]))
    return {count, tonumber(ARGV[1])}
end
local pttl = redis.call('PTTL', KEYS[1])
return {count, pttl}
`
)

// hashKey returns the first 16 hex characters of the SHA-256 hash of key (64-bit prefix).
// Used in logs and traces instead of the raw key to avoid leaking client identifiers
// (IP addresses, tenant IDs) and to keep telemetry cardinality low.
func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8])
}

// Tier defines a rate limiting level with its own limits and window.
type Tier struct {
	// Name is a human-readable identifier for the tier (e.g., "default", "export", "dispatch").
	Name string
	// Max is the maximum number of requests allowed within the window.
	Max int
	// Window is the duration of the rate limit window.
	Window time.Duration
}

// DefaultTier returns a tier configured via environment variables with sensible defaults.
//
// Environment variables:
//   - RATE_LIMIT_MAX: maximum requests (default: 500)
//   - RATE_LIMIT_WINDOW_SEC: window duration in seconds (default: 60)
func DefaultTier() Tier {
	return Tier{
		Name:   "default",
		Max:    int(commons.GetenvIntOrDefault("RATE_LIMIT_MAX", fallbackDefaultMax)),
		Window: time.Duration(commons.GetenvIntOrDefault("RATE_LIMIT_WINDOW_SEC", fallbackWindowSec)) * time.Second,
	}
}

// AggressiveTier returns a stricter tier configured via environment variables.
//
// Environment variables:
//   - AGGRESSIVE_RATE_LIMIT_MAX: maximum requests (default: 100)
//   - AGGRESSIVE_RATE_LIMIT_WINDOW_SEC: window duration in seconds (default: 60)
func AggressiveTier() Tier {
	return Tier{
		Name:   "aggressive",
		Max:    int(commons.GetenvIntOrDefault("AGGRESSIVE_RATE_LIMIT_MAX", fallbackAggressiveMax)),
		Window: time.Duration(commons.GetenvIntOrDefault("AGGRESSIVE_RATE_LIMIT_WINDOW_SEC", fallbackWindowSec)) * time.Second,
	}
}

// RelaxedTier returns a more permissive tier configured via environment variables.
//
// Environment variables:
//   - RELAXED_RATE_LIMIT_MAX: maximum requests (default: 1000)
//   - RELAXED_RATE_LIMIT_WINDOW_SEC: window duration in seconds (default: 60)
func RelaxedTier() Tier {
	return Tier{
		Name:   "relaxed",
		Max:    int(commons.GetenvIntOrDefault("RELAXED_RATE_LIMIT_MAX", fallbackRelaxedMax)),
		Window: time.Duration(commons.GetenvIntOrDefault("RELAXED_RATE_LIMIT_WINDOW_SEC", fallbackWindowSec)) * time.Second,
	}
}

// RateLimiter provides distributed rate limiting via Redis.
// It uses a fixed window counter pattern with an atomic Lua script (INCR + PEXPIRE)
// to prevent keys from being left without TTL on connection failures.
//
// A nil RateLimiter is safe to use: WithRateLimit returns a pass-through handler.
type RateLimiter struct {
	conn         *libRedis.Client
	logger       log.Logger
	keyPrefix    string
	identityFunc IdentityFunc
	failOpen     bool
	onLimited    func(c *fiber.Ctx, tier Tier)
	redisTimeout time.Duration
}

// New creates a RateLimiter. Returns nil when:
//   - conn is nil
//   - RATE_LIMIT_ENABLED environment variable is set to "false"
//
// A nil RateLimiter is safe to use: WithRateLimit returns a pass-through handler.
func New(conn *libRedis.Client, opts ...Option) *RateLimiter {
	rl := &RateLimiter{
		logger:       log.NewNop(),
		identityFunc: IdentityFromIP(),
		failOpen:     true,
		redisTimeout: time.Duration(commons.GetenvIntOrDefault("RATE_LIMIT_REDIS_TIMEOUT_MS", fallbackRedisTimeoutMS)) * time.Millisecond,
	}

	for _, opt := range opts {
		opt(rl)
	}

	if commons.GetenvOrDefault("RATE_LIMIT_ENABLED", "true") == "false" {
		rl.logger.Log(context.Background(), log.LevelInfo,
			"rate limiter disabled via RATE_LIMIT_ENABLED=false; all requests will pass through")

		return nil
	}

	if conn == nil {
		asserter := assert.New(context.Background(), rl.logger, "http.ratelimit", "New")
		_ = asserter.Never(context.Background(), "redis connection is nil; rate limiter disabled")

		return nil
	}

	rl.conn = conn

	return rl
}

// WithRateLimit returns a fiber.Handler that applies rate limiting for the given tier.
// If the RateLimiter is nil, it returns a pass-through handler that calls c.Next().
func (rl *RateLimiter) WithRateLimit(tier Tier) fiber.Handler {
	if rl == nil {
		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	if tier.Max > maxReasonableTierMax {
		rl.logger.Log(context.Background(), log.LevelWarn,
			"rate limit tier max is unusually high; verify configuration",
			log.String("tier", tier.Name),
			log.Int("max", tier.Max),
			log.Int("threshold", maxReasonableTierMax),
		)
	}

	return func(c *fiber.Ctx) error {
		return rl.check(c, tier)
	}
}

// WithDefaultRateLimit is a convenience function that creates a RateLimiter and returns
// a fiber.Handler with the default tier (500 req/60s).
func WithDefaultRateLimit(conn *libRedis.Client, opts ...Option) fiber.Handler {
	return New(conn, opts...).WithRateLimit(DefaultTier())
}

// WithDynamicRateLimit returns a fiber.Handler that selects the rate limit tier per
// request using the provided TierFunc. This allows applying different limits based on
// request attributes such as HTTP method, path, or identity.
//
// If the RateLimiter is nil, it returns a pass-through handler that calls c.Next().
//
// Example — stricter limits for write operations:
//
//	app.Use(rl.WithDynamicRateLimit(ratelimit.MethodTierSelector(
//	    ratelimit.AggressiveTier(),
//	    ratelimit.DefaultTier(),
//	)))
func (rl *RateLimiter) WithDynamicRateLimit(fn TierFunc) fiber.Handler {
	if rl == nil || fn == nil {
		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	return func(c *fiber.Ctx) error {
		return rl.check(c, fn(c))
	}
}

// check is the shared core of WithRateLimit and WithDynamicRateLimit. It runs the rate
// limit check for the given tier and either passes the request through or returns an
// appropriate error response.
func (rl *RateLimiter) check(c *fiber.Ctx, tier Tier) error {
	ctx := c.UserContext()
	if ctx == nil {
		ctx = context.Background()
	}

	_, tracer, _, _ := commons.NewTrackingFromContext(ctx) //nolint:dogsled

	ctx, span := tracer.Start(ctx, "middleware.ratelimit.check")
	defer span.End()

	identity := rl.identityFunc(c)
	key := rl.buildKey(tier, identity)
	keyHash := hashKey(key)

	span.SetAttributes(
		attribute.String("ratelimit.tier", tier.Name),
		attribute.String("ratelimit.key_hash", keyHash),
	)

	count, ttl, err := rl.incrementCounter(ctx, key, tier)
	if err != nil {
		return rl.handleRedisError(c, ctx, span, tier, keyHash, err)
	}

	allowed := count <= int64(tier.Max)
	span.SetAttributes(attribute.Bool("ratelimit.allowed", allowed))

	if !allowed {
		return rl.handleLimitExceeded(c, ctx, span, tier, keyHash, ttl)
	}

	remaining := max(int64(tier.Max)-count, 0)
	resetAt := time.Now().Add(ttl).Unix()

	c.Set(constant.RateLimitLimit, strconv.Itoa(tier.Max))
	c.Set(constant.RateLimitRemaining, strconv.FormatInt(remaining, 10))
	c.Set(constant.RateLimitReset, strconv.FormatInt(resetAt, 10))

	return c.Next()
}

// buildKey constructs the Redis key for the rate limit counter.
// Format: {keyPrefix}:ratelimit:{tier.Name}:{identity} (with prefix)
// Format: ratelimit:{tier.Name}:{identity} (without prefix)
func (rl *RateLimiter) buildKey(tier Tier, identity string) string {
	if rl.keyPrefix != "" {
		return fmt.Sprintf("%s:ratelimit:%s:%s", rl.keyPrefix, tier.Name, identity)
	}

	return fmt.Sprintf("ratelimit:%s:%s", tier.Name, identity)
}

// incrementCounter atomically increments the counter and sets expiry using a Lua script.
// Returns the current count and the remaining TTL of the key. On the first request of a
// window the TTL equals the full window; on subsequent requests it reflects the actual
// remaining time, which is used for accurate Retry-After and X-RateLimit-Reset headers.
func (rl *RateLimiter) incrementCounter(ctx context.Context, key string, tier Tier) (count int64, ttl time.Duration, err error) {
	client, err := rl.conn.GetClient(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("get redis client: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, rl.redisTimeout)
	defer cancel()

	vals, err := client.Eval(timeoutCtx, luaIncrExpire, []string{key}, tier.Window.Milliseconds()).Slice()
	if err != nil {
		return 0, 0, fmt.Errorf("redis eval failed for tier %s: %w", tier.Name, err)
	}

	if len(vals) < 2 {
		return 0, 0, fmt.Errorf("unexpected lua result length %d for tier %s", len(vals), tier.Name)
	}

	count, ok := vals[0].(int64)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected lua result type %T for count (tier %s)", vals[0], tier.Name)
	}

	ttlMs, ok := vals[1].(int64)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected lua result type %T for ttl (tier %s)", vals[1], tier.Name)
	}

	// Guard against -1 (no expiry) or -2 (key not found) from PTTL; fall back to full window.
	if ttlMs <= 0 {
		ttlMs = tier.Window.Milliseconds()
	}

	return count, time.Duration(ttlMs) * time.Millisecond, nil
}

// handleRedisError handles a Redis communication failure during rate limit check.
func (rl *RateLimiter) handleRedisError(
	c *fiber.Ctx,
	ctx context.Context,
	span trace.Span,
	tier Tier,
	keyHash string,
	err error,
) error {
	rl.logger.Log(ctx, log.LevelWarn, "rate limiter redis error",
		log.String("tier", tier.Name),
		log.String("key_hash", keyHash),
		log.Err(err),
	)

	libOpentelemetry.HandleSpanError(span, "rate limiter redis error", err)

	if rl.failOpen {
		return c.Next()
	}

	return chttp.Respond(c, http.StatusServiceUnavailable, chttp.ErrorResponse{
		Code:    http.StatusServiceUnavailable,
		Title:   serviceUnavailableTitle,
		Message: serviceUnavailableMessage,
	})
}

// handleLimitExceeded handles the case when the rate limit has been exceeded.
// ttl is the actual remaining TTL of the Redis key, used for accurate Retry-After
// and X-RateLimit-Reset headers instead of the full window duration.
func (rl *RateLimiter) handleLimitExceeded(
	c *fiber.Ctx,
	ctx context.Context,
	span trace.Span,
	tier Tier,
	keyHash string,
	ttl time.Duration,
) error {
	rl.logger.Log(ctx, log.LevelWarn, "rate limit exceeded",
		log.String("tier", tier.Name),
		log.String("key_hash", keyHash),
		log.Int("max", tier.Max),
	)

	libOpentelemetry.HandleSpanBusinessErrorEvent(
		span,
		"rate limit exceeded",
		fiber.NewError(http.StatusTooManyRequests, rateLimitMessage),
	)

	if rl.onLimited != nil {
		rl.onLimited(c, tier)
	}

	// Ceiling division: round up to the nearest second so the client never receives a
	// Retry-After value that has already elapsed by the time they read the response.
	retryAfterSec := int(ttl / time.Second)
	if ttl%time.Second > 0 {
		retryAfterSec++
	}

	if retryAfterSec < 1 {
		retryAfterSec = 1
	}

	resetAt := time.Now().Add(ttl).Unix()

	c.Set(headerRetryAfter, strconv.Itoa(retryAfterSec))
	c.Set(constant.RateLimitLimit, strconv.Itoa(tier.Max))
	c.Set(constant.RateLimitRemaining, "0")
	c.Set(constant.RateLimitReset, strconv.FormatInt(resetAt, 10))

	return chttp.Respond(c, http.StatusTooManyRequests, chttp.ErrorResponse{
		Code:    http.StatusTooManyRequests,
		Title:   rateLimitTitle,
		Message: rateLimitMessage,
	})
}
