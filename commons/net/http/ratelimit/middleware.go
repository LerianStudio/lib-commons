package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/assert"
	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	chttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
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

	// invalidWindowTitle is the error title returned when a tier has a zero or sub-millisecond window.
	invalidWindowTitle = "misconfigured_rate_limiter"
	// invalidWindowMessage is the error message returned when a tier has a zero or sub-millisecond window.
	invalidWindowMessage = "rate limiter tier window is zero; contact the service operator"
	// invalidTenantContextTitle is returned when the request context contains a malformed tenant ID.
	invalidTenantContextTitle = "invalid_tenant_context"
	// invalidTenantContextMessage is intentionally generic so raw tenant identifiers never leak.
	invalidTenantContextMessage = "tenant context is invalid"

	// policyBlockedTitle is the error title returned when strict-tier enforcement
	// requires rate limiting but the limiter cannot be safely enabled.
	policyBlockedTitle = "security_policy_violation"
	// policyBlockedMessage is the default error message returned when the rate
	// limiter is fail-closed by security policy.
	policyBlockedMessage = "rate limiting is mandatory in strict tier"
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
	conn            *libRedis.Client
	storageBackend  *RedisStorage
	logger          log.Logger
	keyPrefix       string
	identityFunc    IdentityFunc
	failOpen        bool
	policyBlocked   bool
	policyMessage   string
	onLimited       func(c *fiber.Ctx, tier Tier)
	exceededHandler func(c *fiber.Ctx, tier Tier, ttl time.Duration) error
	redisTimeout    time.Duration
}

// New creates a RateLimiter. In permissive or warn-only modes, it returns nil when:
//   - conn is nil
//   - RATE_LIMIT_ENABLED environment variable is set to "false"
//
// In strict enforced mode, it falls back to a non-nil fail-closed limiter when
// rate limiting is mandatory but cannot be safely enabled.
//
// A nil RateLimiter is safe to use: WithRateLimit returns a pass-through handler.
func New(conn *libRedis.Client, opts ...Option) *RateLimiter {
	timeoutMS := commons.GetenvIntOrDefault("RATE_LIMIT_REDIS_TIMEOUT_MS", fallbackRedisTimeoutMS)
	if timeoutMS <= 0 {
		timeoutMS = fallbackRedisTimeoutMS
	}

	rl := &RateLimiter{
		logger:       log.NewNop(),
		identityFunc: IdentityFromIP(),
		failOpen:     true,
		redisTimeout: time.Duration(timeoutMS) * time.Millisecond,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(rl)
	}

	strictTier := commons.CurrentTier() == commons.TierStrict
	enforcementEnabled := commons.IsSecurityEnforcementEnabled()

	// Security policy: in strict enforced mode, fail-open is coerced to fail-closed
	// unless an explicit override is present.
	if strictTier && rl.failOpen {
		result := commons.CheckSecurityRule(commons.RuleRateLimitFailOpen, true)
		if err := commons.EnforceSecurityRule(context.Background(), rl.logger, "ratelimit", result); err != nil {
			rl.logger.Log(context.Background(), log.LevelError, err.Error())
			rl.failOpen = false
		}
	}

	rateLimitDisabled := commons.GetenvOrDefault("RATE_LIMIT_ENABLED", "true") == "false"
	if rateLimitDisabled {
		if strictTier {
			// commons.CheckSecurityRule + commons.EnforceSecurityRule already respect
			// the enforcement mode for the rateLimitDisabled/strictTier path, so we
			// do not branch on enforcementEnabled here. The Redis-nil path below
			// checks enforcementEnabled explicitly because it must choose between
			// returning nil and newPolicyBlockedLimiter(policyBlockedMessage).
			result := commons.CheckSecurityRule(commons.RuleRateLimitDisabled, true)
			if err := commons.EnforceSecurityRule(context.Background(), rl.logger, "ratelimit", result); err != nil {
				logPolicyBlockedStartup(rl.logger,
					"CRITICAL: rate limiter fail-closed is active; all requests will be rejected with 503 until RATE_LIMIT_ENABLED is restored or an override is configured")
				rl.logger.Log(context.Background(), log.LevelError, err.Error())

				return newPolicyBlockedLimiter(rl.logger, policyBlockedMessage)
			}
		}

		rl.logger.Log(context.Background(), log.LevelInfo,
			"rate limiter disabled via RATE_LIMIT_ENABLED=false; all requests will pass through")

		return nil
	}

	if conn == nil {
		asserter := assert.New(context.Background(), rl.logger, "http.ratelimit", "New")
		if err := asserter.Never(context.Background(), "redis connection is nil; rate limiter disabled"); err == nil {
			rl.logger.Log(context.Background(), log.LevelWarn, "rate limiter assertion unexpectedly passed")
		}

		if strictTier && enforcementEnabled {
			logPolicyBlockedStartup(rl.logger,
				"CRITICAL: rate limiter fail-closed is active; all requests will be rejected with 503 until Redis is configured")

			return newPolicyBlockedLimiter(rl.logger, policyBlockedMessage)
		}

		return nil
	}

	rl.conn = conn
	rl.storageBackend = NewRedisStorage(conn, WithRedisStorageLogger(rl.logger))

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

	if rl.policyBlocked {
		return rl.blockedHandler()
	}

	if tier.Window <= 0 || tier.Window.Milliseconds() == 0 {
		rl.logger.Log(context.Background(), log.LevelError,
			"rate limit tier has invalid window; all requests will be rejected",
			log.String("tier", tier.Name),
			log.Int("max", tier.Max),
		)

		return func(c *fiber.Ctx) error {
			return chttp.Respond(c, http.StatusInternalServerError, chttp.ErrorResponse{
				Code:    http.StatusInternalServerError,
				Title:   invalidWindowTitle,
				Message: invalidWindowMessage,
			})
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

	if rl.policyBlocked {
		return rl.blockedHandler()
	}

	return func(c *fiber.Ctx) error {
		tier := fn(c)

		if tier.Window <= 0 || tier.Window.Milliseconds() == 0 {
			ctx := c.UserContext()
			if ctx == nil {
				ctx = context.Background()
			}

			rl.logger.Log(ctx, log.LevelError,
				"rate limit tier has invalid window; request rejected",
				log.String("tier", tier.Name),
				log.Int("max", tier.Max),
			)

			return chttp.Respond(c, http.StatusInternalServerError, chttp.ErrorResponse{
				Code:    http.StatusInternalServerError,
				Title:   invalidWindowTitle,
				Message: invalidWindowMessage,
			})
		}

		return rl.check(c, tier)
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

// buildKey constructs the Redis key for the rate limit counter. Without a custom
// prefix, the storage layer prepends its own "ratelimit:" namespace. With a custom
// prefix, this method preserves the historical external wire shape by placing the
// service prefix before the ratelimit namespace.
//
// Wire-format examples:
//
//	WithKeyPrefix unset:     ratelimit:{tier.Name}:{identity}
//	WithKeyPrefix("my-svc"): my-svc:ratelimit:{tier.Name}:{identity}
func (rl *RateLimiter) buildKey(tier Tier, identity string) string {
	if rl.keyPrefix != "" {
		return fmt.Sprintf("%s:%s%s:%s", rl.keyPrefix, keyPrefix, tier.Name, identity)
	}

	return fmt.Sprintf("%s:%s", tier.Name, identity)
}

// incrementCounter atomically increments the counter and sets expiry by delegating to the
// storage-layer primitive RedisStorage.Increment, bounded by the limiter's configured
// rl.redisTimeout so a slow Redis cannot stall the request beyond the middleware's budget.
// Returns the current count and the remaining TTL of the key. On the first request of a
// window the TTL equals the full window; on subsequent requests it reflects the actual
// remaining time, which is used for accurate Retry-After and X-RateLimit-Reset headers.
//
// The key passed in here is package-owned middleware output: either the
// tier-scoped suffix when no custom key prefix is configured, or the full
// historical "<prefix>:ratelimit:..." wire key when WithKeyPrefix is used.
// The unexported full-key path preserves that historical middleware shape while
// public RedisStorage.Increment keeps arbitrary caller keys inside the default
// storage namespace.
func (rl *RateLimiter) incrementCounter(ctx context.Context, key string, tier Tier) (count int64, ttl time.Duration, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, rl.redisTimeout)
	defer cancel()

	if rl.keyPrefix != "" {
		return rl.storage().incrementFullKey(timeoutCtx, key, tier.Window)
	}

	return rl.storage().Increment(timeoutCtx, key, tier.Window)
}

// storage returns the cached RedisStorage view onto the limiter's Redis connection.
func (rl *RateLimiter) storage() *RedisStorage {
	return rl.storageBackend
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

	if errors.Is(err, ErrInvalidTenantContext) {
		return chttp.Respond(c, http.StatusBadRequest, chttp.ErrorResponse{
			Code:    http.StatusBadRequest,
			Title:   invalidTenantContextTitle,
			Message: invalidTenantContextMessage,
		})
	}

	if rl.failOpen {
		return c.Next()
	}

	return chttp.Respond(c, http.StatusServiceUnavailable, chttp.ErrorResponse{
		Code:    http.StatusServiceUnavailable,
		Title:   serviceUnavailableTitle,
		Message: serviceUnavailableMessage,
	})
}

func newPolicyBlockedLimiter(logger log.Logger, message string) *RateLimiter {
	if message == "" {
		message = policyBlockedMessage
	}

	return &RateLimiter{
		logger:        logger,
		identityFunc:  IdentityFromIP(),
		failOpen:      false,
		policyBlocked: true,
		policyMessage: message,
		redisTimeout:  time.Duration(fallbackRedisTimeoutMS) * time.Millisecond,
	}
}

func logPolicyBlockedStartup(logger log.Logger, message string) {
	if message == "" {
		return
	}

	logger.Log(context.Background(), log.LevelError, message)
}

func (rl *RateLimiter) blockedHandler() fiber.Handler {
	message := rl.policyMessage
	if message == "" {
		message = policyBlockedMessage
	}

	return func(c *fiber.Ctx) error {
		return chttp.Respond(c, http.StatusServiceUnavailable, chttp.ErrorResponse{
			Code:    http.StatusServiceUnavailable,
			Title:   policyBlockedTitle,
			Message: message,
		})
	}
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

	// Consumer-controlled responder: when set, the middleware hands over body writing
	// entirely. Headers above are already written. The handler's return value propagates
	// to Fiber. See WithExceededHandler for the contract.
	if rl.exceededHandler != nil {
		return rl.exceededHandler(c, tier, ttl)
	}

	return chttp.Respond(c, http.StatusTooManyRequests, chttp.ErrorResponse{
		Code:    http.StatusTooManyRequests,
		Title:   rateLimitTitle,
		Message: rateLimitMessage,
	})
}
