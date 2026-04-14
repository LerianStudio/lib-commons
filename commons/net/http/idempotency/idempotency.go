package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	libHTTP "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	libRedis "github.com/LerianStudio/lib-commons/v4/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

const (
	keyStateProcessing = "processing"
	keyStateComplete   = "complete"
)

// cachedResponse stores the full HTTP response for idempotent replay.
// Body is stored as raw bytes (base64-encoded in JSON) so that binary and
// non-UTF-8 payloads survive a marshal/unmarshal round-trip. Headers preserves
// response headers that must be faithfully replayed (e.g., Location, ETag,
// Set-Cookie).
type cachedResponse struct {
	StatusCode  int                 `json:"status_code"`
	ContentType string              `json:"content_type"`
	Body        []byte              `json:"body"`
	Headers     map[string][]string `json:"headers,omitempty"`
}

// Option configures the idempotency middleware.
type Option func(*Middleware)

// Middleware provides at-most-once request semantics using Redis SetNX.
type Middleware struct {
	conn         *libRedis.Client
	logger       log.Logger
	keyPrefix    string
	keyTTL       time.Duration
	maxKeyLength int
	maxBodyCache int
	redisTimeout time.Duration
	onRejected   func(c *fiber.Ctx) error
}

// New creates an idempotency middleware backed by the given Redis client.
// Returns nil if conn is nil (nil-safe: Check() returns pass-through).
func New(conn *libRedis.Client, opts ...Option) *Middleware {
	if conn == nil {
		return nil
	}

	m := &Middleware{
		conn:         conn,
		logger:       log.NewNop(),
		keyPrefix:    "idempotency:",
		keyTTL:       7 * 24 * time.Hour,
		maxKeyLength: 256,
		maxBodyCache: 1 << 20, // 1 MB default
		redisTimeout: 500 * time.Millisecond,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	return m
}

// WithLogger sets a structured logger.
func WithLogger(l log.Logger) Option {
	return func(m *Middleware) {
		if l != nil {
			m.logger = l
		}
	}
}

// WithKeyPrefix sets the Redis key prefix (default: "idempotency:").
func WithKeyPrefix(prefix string) Option {
	return func(m *Middleware) {
		if prefix != "" {
			m.keyPrefix = prefix
		}
	}
}

// WithKeyTTL sets how long idempotency keys are retained (default: 7 days).
func WithKeyTTL(ttl time.Duration) Option {
	return func(m *Middleware) {
		if ttl > 0 {
			m.keyTTL = ttl
		}
	}
}

// WithMaxKeyLength sets the maximum allowed idempotency key length (default: 256).
func WithMaxKeyLength(n int) Option {
	return func(m *Middleware) {
		if n > 0 {
			m.maxKeyLength = n
		}
	}
}

// WithRedisTimeout sets the timeout for Redis operations (default: 500ms).
func WithRedisTimeout(d time.Duration) Option {
	return func(m *Middleware) {
		if d > 0 {
			m.redisTimeout = d
		}
	}
}

// WithRejectedHandler sets a custom handler for requests with oversized keys.
// By default, a generic 400 JSON response is returned.
func WithRejectedHandler(fn func(c *fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onRejected = fn
	}
}

// WithMaxBodyCache sets the maximum response body size (in bytes) that will be
// cached in Redis for idempotent replay (default: 1 MB). Responses larger than
// this limit are not cached; duplicate requests will receive a generic
// "already processed" response instead.
// Values <= 0 are ignored.
func WithMaxBodyCache(n int) Option {
	return func(m *Middleware) {
		if n > 0 {
			m.maxBodyCache = n
		}
	}
}

// Check returns a Fiber middleware that enforces idempotency on mutating requests.
// If the Middleware is nil, a pass-through handler is returned.
func (m *Middleware) Check() fiber.Handler {
	if m == nil {
		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	return m.handle
}

// redactKey returns a truncated SHA-256 hash of a Redis key for safe logging.
// Idempotency keys are client-controlled and tenant-scoped, so logging them
// verbatim would emit high-cardinality identifiers and potentially leak tenant
// or client information during incidents.
func redactKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8])
}

func (m *Middleware) handle(c *fiber.Ctx) error {
	// Idempotency only applies to mutating methods.
	if c.Method() == fiber.MethodGet || c.Method() == fiber.MethodOptions || c.Method() == fiber.MethodHead {
		return c.Next()
	}

	idempotencyKey := c.Get(chttp.IdempotencyKey)
	if idempotencyKey == "" {
		return c.Next()
	}

	if len(idempotencyKey) > m.maxKeyLength {
		if m.onRejected != nil {
			return m.onRejected(c)
		}

		return libHTTP.RespondError(c, http.StatusBadRequest,
			"VALIDATION_ERROR",
			fmt.Sprintf("%s must not exceed %d characters", chttp.IdempotencyKey, m.maxKeyLength),
		)
	}

	// Build a tenant-scoped Redis key for per-tenant isolation.
	tenantID := tmcore.GetTenantIDContext(c.UserContext())
	if tenantID == "" {
		// No tenant context — bypass idempotency to avoid collapsing all
		// tenant-less requests onto a shared key, which breaks isolation.
		// This is consistent with the middleware's fail-open philosophy.
		m.logger.Log(c.UserContext(), log.LevelWarn,
			"idempotency: missing tenant context, bypassing idempotency enforcement")
		return c.Next()
	}

	key := fmt.Sprintf("%s%s:%s", m.keyPrefix, tenantID, idempotencyKey)

	ctx, cancel := context.WithTimeout(c.UserContext(), m.redisTimeout)
	defer cancel()

	client, err := m.conn.GetClient(ctx)
	if err != nil {
		// Redis unavailable — fail-open to preserve availability.
		m.logger.Log(ctx, log.LevelWarn, "idempotency: redis unavailable, failing open", log.Err(err))
		return c.Next()
	}

	// SetNX atomically checks and sets — returns true only if the key was newly created.
	set, setnxErr := client.SetNX(ctx, key, keyStateProcessing, m.keyTTL).Result()
	if setnxErr != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: setnx failed, failing open", log.Err(setnxErr))
		return c.Next()
	}

	responseKey := key + ":response"

	if !set {
		return m.handleDuplicate(ctx, c, client, key, responseKey)
	}

	// Proceed with the actual handler.
	handlerErr := c.Next()

	// Create fresh context for post-handler Redis bookkeeping.
	// The pre-handler ctx may have expired during handler execution.
	postCtx, postCancel := context.WithTimeout(context.WithoutCancel(c.UserContext()), m.redisTimeout)
	defer postCancel()

	m.saveResult(postCtx, c, client, key, responseKey, handlerErr)

	return handlerErr
}

// handleDuplicate processes a duplicate request (one whose idempotency key already exists
// in Redis). It attempts to replay the cached response when available, falls back to a
// conflict response when the original request is still in flight, or returns a generic
// "already processed" response when the key is complete but the body was not cached.
func (m *Middleware) handleDuplicate(
	ctx context.Context,
	c *fiber.Ctx,
	client redis.UniversalClient,
	key, responseKey string,
) error {
	// Read the current key value to distinguish in-flight from completed.
	keyValue, keyErr := client.Get(ctx, key).Result()
	if keyErr != nil && !errors.Is(keyErr, redis.Nil) {
		// Unexpected Redis error (timeout, connection failure) — fail open.
		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: failed to read key state, failing open",
			log.String("key_hash", redactKey(key)), log.Err(keyErr),
		)

		return c.Next()
	}

	// The marker has vanished between the SetNX (which saw it) and this Get.
	// This happens when the original request failed and deleted the key, or
	// the TTL expired in the narrow window. Fail open so the duplicate can
	// be retried rather than returning a false "already processed" response.
	if errors.Is(keyErr, redis.Nil) {
		return c.Next()
	}

	// Try to replay the cached response (true idempotency).
	cached, cacheErr := client.Get(ctx, responseKey).Result()

	switch {
	case cacheErr != nil && !errors.Is(cacheErr, redis.Nil):
		// Unexpected Redis error reading cached response — fail open.
		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: failed to read cached response, failing open",
			log.String("key_hash", redactKey(responseKey)), log.Err(cacheErr),
		)

		return c.Next()
	case cacheErr == nil && cached != "":
		var resp cachedResponse
		if unmarshalErr := json.Unmarshal([]byte(cached), &resp); unmarshalErr != nil {
			// Cache entry is corrupt or written by an incompatible version.
			// Log a warning so operators can investigate, then fall through
			// to the generic "already processed" response (fail-open).
			m.logger.Log(ctx, log.LevelWarn,
				"idempotency: failed to unmarshal cached response, falling through to generic reply",
				log.String("key_hash", redactKey(responseKey)), log.Err(unmarshalErr),
			)
		} else {
			// Replay persisted headers first so the caller sees
			// Location, ETag, Set-Cookie, etc. exactly as sent originally.
			// Use Header.Add (not c.Set) so multi-value headers such as
			// Set-Cookie are appended rather than silently overwritten.
			for name, values := range resp.Headers {
				for _, v := range values {
					c.Response().Header.Add(name, v)
				}
			}

			c.Set(chttp.IdempotencyReplayed, "true")
			c.Set("Content-Type", resp.ContentType)

			// Send (not SendString) preserves binary/non-UTF-8 bodies.
			return c.Status(resp.StatusCode).Send(resp.Body)
		}
	}

	// No cached response available — differentiate by key state.
	c.Set(chttp.IdempotencyReplayed, "true")

	if keyValue == keyStateProcessing {
		// Request is still in flight — tell the client to retry later.
		return libHTTP.RespondError(c, http.StatusConflict,
			"IDEMPOTENCY_CONFLICT",
			"a request with this idempotency key is currently being processed",
		)
	}

	// Key is "complete" but the response body was not cached
	// (e.g., body exceeded maxBodyCache limit).
	return libHTTP.Respond(c, http.StatusOK, libHTTP.ErrorResponse{
		Code:    http.StatusOK,
		Title:   "IDEMPOTENT",
		Message: "request already processed",
	})
}

// saveResult performs post-handler Redis bookkeeping: on success it caches the response
// body and marks the key as complete in a single round-trip via a Redis pipeline; on
// handler error it deletes both keys so the client can retry with the same idempotency key.
func (m *Middleware) saveResult(
	ctx context.Context,
	c *fiber.Ctx,
	client redis.UniversalClient,
	key, responseKey string,
	handlerErr error,
) {
	statusCode := c.Response().StatusCode()

	// Treat handler errors and 5xx responses the same way: delete keys so the
	// client can retry. Fiber handlers commonly write a 5xx and return nil, so
	// checking handlerErr alone is not sufficient — caching a transient 5xx
	// would make it non-retriable for the full TTL.
	if handlerErr == nil && statusCode < http.StatusInternalServerError {
		body := c.Response().Body()

		pipe := client.Pipeline()

		if len(body) <= m.maxBodyCache {
			// Capture response headers for faithful replay.
			headers := make(map[string][]string)

			for hdrKey, value := range c.Response().Header.All() {
				name := string(hdrKey)
				// Skip headers managed by the middleware itself and
				// transfer-encoding / content-length which Fiber sets on send.
				switch name {
				case "Content-Type", "Content-Length", "Transfer-Encoding",
					chttp.IdempotencyReplayed:
					continue
				}

				headers[name] = append(headers[name], string(value))
			}

			resp := cachedResponse{
				StatusCode:  statusCode,
				ContentType: string(c.Response().Header.ContentType()),
				Body:        body,
				Headers:     headers,
			}

			if data, marshalErr := json.Marshal(resp); marshalErr == nil {
				pipe.Set(ctx, responseKey, string(data), m.keyTTL)
			} else {
				m.logger.Log(ctx, log.LevelWarn,
					"idempotency: failed to marshal cached response",
					log.Err(marshalErr),
					log.String("idempotency_key_hash", redactKey(key)),
				)
			}
		} else {
			m.logger.Log(ctx, log.LevelWarn,
				"idempotency: response body exceeds maxBodyCache, skipping cache",
				log.Int("body_size", len(body)),
				log.Int("max_body_cache", m.maxBodyCache),
			)
		}

		pipe.Set(ctx, key, keyStateComplete, m.keyTTL)

		if _, pipeErr := pipe.Exec(ctx); pipeErr != nil {
			m.logger.Log(ctx, log.LevelWarn,
				"idempotency: failed to atomically cache response and mark complete",
				log.Err(pipeErr),
			)
		}
	} else {
		pipe := client.Pipeline()
		pipe.Del(ctx, key)
		pipe.Del(ctx, responseKey)

		if _, pipeErr := pipe.Exec(ctx); pipeErr != nil {
			m.logger.Log(ctx, log.LevelWarn,
				"idempotency: failed to delete keys after handler error",
				log.Err(pipeErr),
			)
		}
	}
}
