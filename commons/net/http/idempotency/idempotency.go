package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	libHTTP "github.com/LerianStudio/lib-commons/v6/commons/net/http"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/gofiber/fiber/v3"
	"github.com/redis/go-redis/v9"
)

const (
	keyStateProcessing = "processing"
	keyStateComplete   = "complete"
)

// stateSeparator divides the key state from the request fingerprint in the
// marker value: "processing:<hex>" / "complete:<hex>". A value carrying no
// separator was written by a version that stored no fingerprint — see
// splitKeyValue.
const stateSeparator = ":"

// requestFingerprint identifies WHICH request an idempotency key was spent on.
//
// It hashes the raw body bytes exactly as received, never a re-serialization:
// re-encoding would let JSON key order, pretty-printing or charset differences
// change the digest between a request and its own retry, and a false mismatch on
// a money path is worse than the reuse it would catch — the caller may respond by
// retrying under a NEW key, producing the duplicate this guard exists to prevent.
//
// Method and path join the digest because an identical body sent to a different
// operation is a different request. The query string does NOT: clients append
// cache-busting parameters on retry, and that must not read as reuse.
func requestFingerprint(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte("\n"))
	h.Write([]byte(path))
	h.Write([]byte("\n"))
	h.Write(body)

	return hex.EncodeToString(h.Sum(nil))
}

// splitKeyValue decodes a marker value into its state and fingerprint.
//
// found reports whether a fingerprint was present. It is false for records
// written before fingerprints existed, and those MUST keep the old
// replay-on-key-alone behaviour: rejecting them would turn a legitimate
// in-flight retry into a refusal for the length of a deploy. The exposure is
// bounded — legacy records expire with their TTL and no new ones are written.
func splitKeyValue(value string) (state, fingerprint string, found bool) {
	state, fingerprint, found = strings.Cut(value, stateSeparator)
	if !found {
		return value, "", false
	}

	return state, fingerprint, true
}

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
	onRejected   func(c fiber.Ctx) error
	// failClosed inverts the transient-Redis-error behavior. Default (false)
	// fails open — requests proceed without idempotency coverage to preserve
	// availability. When true, transient Redis errors abort with 503 so a
	// mutation never runs without at-most-once protection.
	failClosed    bool
	onUnavailable func(c fiber.Ctx) error
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
func WithRejectedHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onRejected = fn
	}
}

// WithFailClosed controls behavior on transient Redis errors. When false
// (the default) the middleware fails open: requests proceed without
// idempotency coverage to preserve availability. When true it fails closed:
// any transient Redis error — including on the duplicate/replay path — aborts
// with 503 rather than running the mutation without at-most-once protection.
func WithFailClosed(v bool) Option {
	return func(m *Middleware) {
		m.failClosed = v
	}
}

// WithUnavailableHandler sets a custom handler invoked when fail-closed is
// enabled and the idempotency store is unavailable. By default a generic 503
// JSON response is returned. Has no effect unless WithFailClosed(true) is set.
func WithUnavailableHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onUnavailable = fn
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

// Check returns a Fiber middleware that enforces idempotency on supported mutating requests.
// Requests without tenant context bypass idempotency to preserve tenant isolation.
// If the Middleware is nil, a pass-through handler is returned.
func (m *Middleware) Check() fiber.Handler {
	if m == nil {
		return func(c fiber.Ctx) error {
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

// onRedisError decides how to respond to a transient Redis error. By default
// it fails open — proceeding to the handler to preserve availability. When
// failClosed is set it aborts with 503 (or the configured unavailable handler)
// so the request never runs without idempotency coverage. Callers must have
// already logged the underlying error.
func (m *Middleware) onRedisError(c fiber.Ctx) error {
	if !m.failClosed {
		return c.Next()
	}

	if m.onUnavailable != nil {
		return m.onUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusServiceUnavailable,
		"IDEMPOTENCY_UNAVAILABLE",
		"idempotency store unavailable; request rejected to preserve at-most-once semantics",
	)
}

func (m *Middleware) handle(c fiber.Ctx) error {
	// Idempotency only applies to mutating methods.
	switch c.Method() {
	case fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete:
		// Apply idempotency to mutating methods only.
	default:
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
	tenantID := tmcore.GetTenantIDContext(c.Context())
	if tenantID == "" {
		// No tenant context — bypass idempotency to avoid collapsing all
		// tenant-less requests onto a shared key, which breaks isolation.
		// This is consistent with the middleware's fail-open philosophy.
		return c.Next()
	}

	key := fmt.Sprintf("%s%s:%s", m.keyPrefix, tenantID, idempotencyKey)

	ctx, cancel := context.WithTimeout(c.Context(), m.redisTimeout)
	defer cancel()

	client, err := m.conn.GetClient(ctx)
	if err != nil {
		// Redis unavailable — fail open by default, or closed if configured.
		m.logger.Log(ctx, log.LevelWarn, "idempotency: redis unavailable", log.Err(err))
		return m.onRedisError(c)
	}

	// The fingerprint is computed BEFORE the handler runs, while the body is
	// untouched, and carried through to saveResult — recomputing it afterwards
	// would hash whatever the handler left behind.
	fingerprint := requestFingerprint(c.Method(), c.Path(), c.Body())

	// SetNX atomically checks and sets — returns true only if the key was newly created.
	// The marker carries the fingerprint so a later duplicate can tell a genuine
	// retry from the same key spent on a different request.
	set, setnxErr := client.SetNX(ctx, key, keyStateProcessing+stateSeparator+fingerprint, m.keyTTL).Result()
	if setnxErr != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: setnx failed", log.Err(setnxErr))
		return m.onRedisError(c)
	}

	responseKey := key + ":response"

	if !set {
		return m.handleDuplicate(ctx, c, client, key, responseKey, fingerprint)
	}

	// Proceed with the actual handler.
	handlerErr := c.Next()

	// Create fresh context for post-handler Redis bookkeeping.
	// The pre-handler ctx may have expired during handler execution.
	postCtx, postCancel := context.WithTimeout(context.WithoutCancel(c.Context()), m.redisTimeout)
	defer postCancel()

	m.saveResult(postCtx, c, client, key, responseKey, fingerprint, handlerErr)

	return handlerErr
}

// handleDuplicate processes a duplicate request (one whose idempotency key already exists
// in Redis). It attempts to replay the cached response when available, falls back to a
// conflict response when the original request is still in flight, or returns a generic
// "already processed" response when the key is complete but the body was not cached.
func (m *Middleware) handleDuplicate(
	ctx context.Context,
	c fiber.Ctx,
	client redis.UniversalClient,
	key, responseKey, fingerprint string,
) error {
	// Read the current key value to distinguish in-flight from completed.
	keyValue, keyErr := client.Get(ctx, key).Result()
	if keyErr != nil && !errors.Is(keyErr, redis.Nil) {
		// Unexpected Redis error (timeout, connection failure) on a known
		// duplicate. Fail open by default, or closed if configured — failing
		// closed avoids silently re-running an already-seen mutation.
		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: failed to read key state",
			log.String("key_hash", redactKey(key)), log.Err(keyErr),
		)

		return m.onRedisError(c)
	}

	// The marker has vanished between the SetNX (which saw it) and this Get.
	// This happens when the original request failed and deleted the key, or
	// the TTL expired in the narrow window. Fail open so the duplicate can
	// be retried rather than returning a false "already processed" response.
	if errors.Is(keyErr, redis.Nil) {
		return c.Next()
	}

	keyState, storedFingerprint, hasFingerprint := splitKeyValue(keyValue)

	switch {
	case !hasFingerprint:
		// Written before fingerprints existed. Replay on the key alone, as that
		// version did, and say so — the alternative refuses legitimate retries
		// mid-deploy. Bounded: legacy records expire and none are created.
		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: record predates payload fingerprinting, replaying without comparison",
			log.String("key_hash", redactKey(key)),
		)
	case storedFingerprint != fingerprint:
		// The key was spent on a DIFFERENT request. Replaying here would hand
		// this caller the other request's response: its own operation never ran
		// and it would be told the operation succeeded. The ledger would hold no
		// record of it and nobody would retry, because the status said success.
		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: key reused with a different request payload, refusing",
			log.String("key_hash", redactKey(key)),
			log.String("key_state", keyState),
		)

		return libHTTP.RespondError(c, http.StatusUnprocessableEntity,
			"IDEMPOTENCY_KEY_REUSE",
			"this idempotency key was already used for a different request; "+
				"do not retry with a new key — reconcile the original request first",
		)
	}

	// Try to replay the cached response (true idempotency).
	cached, cacheErr := client.Get(ctx, responseKey).Result()

	switch {
	case cacheErr != nil && !errors.Is(cacheErr, redis.Nil):
		// Unexpected Redis error reading the cached response on a known
		// duplicate. Fail open by default, or closed if configured.
		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: failed to read cached response",
			log.String("key_hash", redactKey(responseKey)), log.Err(cacheErr),
		)

		return m.onRedisError(c)
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

	if keyState == keyStateProcessing {
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
// handler failure or 5xx response it deletes both keys so the client can retry with the same idempotency key.
func (m *Middleware) saveResult(
	ctx context.Context,
	c fiber.Ctx,
	client redis.UniversalClient,
	key, responseKey, fingerprint string,
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

		// Carry the same fingerprint forward. Dropping it here would leave a
		// completed record indistinguishable from a legacy one, so the very next
		// duplicate would replay without a comparison.
		pipe.Set(ctx, key, keyStateComplete+stateSeparator+fingerprint, m.keyTTL)

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
				"idempotency: failed to delete keys after handler failure or 5xx response",
				log.Err(pipeErr),
			)
		}
	}
}
