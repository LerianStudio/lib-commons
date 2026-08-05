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
	"github.com/LerianStudio/lib-commons/v6/commons/internal/nilcheck"
	libHTTP "github.com/LerianStudio/lib-commons/v6/commons/net/http"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

const (
	keyStateProcessing = "processing"
	keyStateComplete   = "complete"
	retryAfterSeconds  = "1"
)

var (
	errInvalidTTL            = errors.New("idempotency TTL must be positive")
	errResponseTooLarge      = errors.New("idempotency replay response exceeds configured limit")
	errInvalidReplayResponse = errors.New("idempotency replay response is invalid")
)

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

// Option configures the idempotency middleware.
type Option func(*Middleware)

// TTLProvider resolves the retention window for the current request. It is
// evaluated for every keyed mutating request, allowing one middleware instance
// to follow hot-reloaded application policy.
type TTLProvider func(c fiber.Ctx) (time.Duration, error)

// ClientErrorPolicy controls whether successful handler returns with a 4xx
// status are replayed or release their owned idempotency record.
type ClientErrorPolicy uint8

const (
	// ClientErrorPolicyCache preserves the default behavior and replays 4xx
	// responses exactly.
	ClientErrorPolicyCache ClientErrorPolicy = iota
	// ClientErrorPolicyRelease removes the owned processing record for 4xx
	// responses, allowing corrected requests to reuse the same key.
	ClientErrorPolicyRelease
)

// Middleware provides at-most-once request semantics using an atomic [Store].
type Middleware struct {
	store             Store
	logger            log.Logger
	keyPrefix         string
	keyTTL            time.Duration
	maxKeyLength      int
	maxBodyCache      int
	redisTimeout      time.Duration
	ttlProvider       TTLProvider
	responseCodec     ResponseCodec
	clientErrorPolicy ClientErrorPolicy
	onRejected        func(c fiber.Ctx) error
	onConflict        fiber.Handler
	onKeyReuse        fiber.Handler
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

	m := newMiddleware(opts...)
	m.store = newRedisStore(conn)

	return m
}

// NewWithStore creates fail-closed idempotency middleware backed by store.
// A missing or errored store rejects keyed mutating requests with 503.
func NewWithStore(store Store, opts ...Option) *Middleware {
	m := newMiddleware(opts...)
	m.store = store
	m.failClosed = true

	return m
}

func newMiddleware(opts ...Option) *Middleware {
	m := &Middleware{
		logger:            log.NewNop(),
		keyPrefix:         "idempotency:",
		keyTTL:            7 * 24 * time.Hour,
		maxKeyLength:      256,
		maxBodyCache:      1 << 20, // 1 MB default
		redisTimeout:      500 * time.Millisecond,
		responseCodec:     identityResponseCodec{},
		clientErrorPolicy: ClientErrorPolicyCache,
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

// WithKeyPrefix sets the storage key prefix (default: "idempotency:").
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

// WithTTLProvider resolves the key TTL for every request. A provider error or
// non-positive TTL fails closed before the protected handler runs.
func WithTTLProvider(provider TTLProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.ttlProvider = provider
		}
	}
}

// WithResponseCodec installs an application-provided response transformation.
// Use an authenticated-encryption codec for sensitive response bodies. A nil or
// typed-nil codec leaves the default identity codec in place.
func WithResponseCodec(codec ResponseCodec) Option {
	return func(m *Middleware) {
		if !nilcheck.Interface(codec) {
			m.responseCodec = codec
		}
	}
}

// WithClientErrorPolicy controls completion of 4xx responses. The default is
// ClientErrorPolicyCache. Invalid values leave the default unchanged.
func WithClientErrorPolicy(policy ClientErrorPolicy) Option {
	return func(m *Middleware) {
		if policy == ClientErrorPolicyCache || policy == ClientErrorPolicyRelease {
			m.clientErrorPolicy = policy
		}
	}
}

// WithMaxKeyLength sets the maximum allowed idempotency key length in UTF-8
// bytes (default: 256). Multi-byte characters therefore consume more than one
// unit of this limit.
func WithMaxKeyLength(n int) Option {
	return func(m *Middleware) {
		if n > 0 {
			m.maxKeyLength = n
		}
	}
}

// WithRedisTimeout sets the timeout for storage operations (default: 500ms).
// The name is preserved for compatibility with the shipped Redis API.
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

// WithConflictHandler sets a custom handler for duplicate requests whose
// original request is still processing. By default, a generic 409 JSON response
// is returned.
func WithConflictHandler(fn fiber.Handler) Option {
	return func(m *Middleware) {
		m.onConflict = fn
	}
}

// WithKeyReuseHandler sets a custom handler for an idempotency key reused with
// a different request method, path, or body. By default, a generic 422 JSON
// response is returned.
func WithKeyReuseHandler(fn fiber.Handler) Option {
	return func(m *Middleware) {
		m.onKeyReuse = fn
	}
}

// WithFailClosed controls behavior on transient errors from the built-in Redis
// store. When false (the default) the middleware fails open: requests proceed
// without idempotency coverage to preserve availability. When true it fails
// closed with 503. [NewWithStore] always fails closed and does not allow this
// option to weaken caller-provided storage.
func WithFailClosed(v bool) Option {
	return func(m *Middleware) {
		m.failClosed = v
	}
}

// WithUnavailableHandler sets a custom handler invoked when the middleware is
// fail-closed and the idempotency store is unavailable. By default a generic
// 503 JSON response is returned. It applies to [NewWithStore] and to [New] when
// [WithFailClosed] is enabled.
func WithUnavailableHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onUnavailable = fn
	}
}

// WithMaxBodyCache sets the maximum raw response body size (in bytes) that can
// be persisted for exact replay (default: 1 MB). The encoded replay payload is
// bounded to twice this value. A response exceeding either bound fails closed
// with 503 after the handler returns; no generic success response is stored.
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

// onStoreError decides how to respond to a transient store error. Callers must
// have already logged the underlying error.
func (m *Middleware) onStoreError(c fiber.Ctx) error {
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

func (m *Middleware) respondPostHandlerStoreError(c fiber.Ctx) error {
	if m.onUnavailable != nil {
		return m.onUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusServiceUnavailable,
		"IDEMPOTENCY_UNAVAILABLE",
		"request processing finished but its replay response could not be persisted; "+
			"do not retry with a new key — reconcile the original request first",
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
			fmt.Sprintf("%s must not exceed %d bytes", chttp.IdempotencyKey, m.maxKeyLength),
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
	if nilcheck.Interface(m.store) {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: store unavailable")

		return m.onStoreError(c)
	}

	ctx, cancel := context.WithTimeout(c.Context(), m.redisTimeout)
	defer cancel()

	fingerprint := requestFingerprint(c.Method(), c.Path(), c.Body())

	ttl, err := m.resolveTTL(c)
	if err != nil {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: TTL provider failed", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	return m.handleStore(ctx, c, key, fingerprint, ttl)
}

func (m *Middleware) resolveTTL(c fiber.Ctx) (time.Duration, error) {
	ttl := m.keyTTL
	if m.ttlProvider != nil {
		var err error

		ttl, err = m.ttlProvider(c)
		if err != nil {
			return 0, err
		}
	}

	if ttl <= 0 {
		return 0, errInvalidTTL
	}

	return ttl, nil
}

func (m *Middleware) handleStore(ctx context.Context, c fiber.Ctx, key, fingerprint string, ttl time.Duration) error {
	owner := uuid.NewString()
	record := storeRecord{
		State:       keyStateProcessing,
		Fingerprint: fingerprint,
		Owner:       owner,
	}

	processing, err := json.Marshal(record)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: failed to marshal processing record", log.Err(err))

		return m.onStoreError(c)
	}

	stored, acquired, err := m.store.Acquire(ctx, key, processing, ttl)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: store acquire failed", log.Err(err))

		return m.onStoreError(c)
	}

	if acquired {
		return m.handleStoreAcquired(c, key, processing, record, ttl)
	}

	// Decoded into a FRESH struct, never into the candidate record above.
	// encoding/json leaves absent fields untouched, so unmarshalling over the
	// candidate would let a stored object that omits "fingerprint" inherit this
	// request's own fingerprint and sail through the mismatch gate, and one that
	// omits "state" inherit "processing" and answer 409. Routing must observe
	// only what the store actually holds.
	var current storeRecord
	if err := json.Unmarshal(stored, &current); err != nil {
		legacy, isLegacy := decodeLegacyRecord(stored)
		if !isLegacy {
			m.logger.Log(ctx, log.LevelWarn, "idempotency: failed to unmarshal stored record", log.Err(err))

			return m.onStoreError(c)
		}

		m.logger.Log(ctx, log.LevelWarn,
			"idempotency: stored record predates the atomic record format, answering from it without replay",
			log.String("record_state", legacy.State))

		return m.respondLegacy(c, legacy, fingerprint)
	}

	if current.Fingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	switch current.State {
	case keyStateProcessing:
		return m.respondConflict(c)
	case keyStateComplete:
		return m.replay(c, current.Response)
	default:
		m.logger.Log(ctx, log.LevelWarn, "idempotency: store returned invalid record state")

		return m.onStoreError(c)
	}
}

func (m *Middleware) handleStoreAcquired(
	c fiber.Ctx,
	key string,
	processing []byte,
	record storeRecord,
	ttl time.Duration,
) error {
	handlerErr := c.Next()

	postCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), m.redisTimeout)
	defer cancel()

	statusCode := c.Response().StatusCode()
	if handlerErr != nil || statusCode >= http.StatusInternalServerError {
		applied, err := m.store.Release(postCtx, key, processing)
		if err != nil {
			m.logger.Log(postCtx, log.LevelWarn, "idempotency: store release failed", log.Err(err))
		} else if !applied {
			m.logger.Log(postCtx, log.LevelWarn, "idempotency: store release rejected stale owner")
		}

		return handlerErr
	}

	if statusCode >= http.StatusBadRequest && m.clientErrorPolicy == ClientErrorPolicyRelease {
		applied, err := m.store.Release(postCtx, key, processing)
		if err != nil {
			m.logger.Log(postCtx, log.LevelWarn, "idempotency: client-error cleanup failed", log.Err(err))
		} else if !applied {
			m.logger.Log(postCtx, log.LevelWarn, "idempotency: client-error cleanup rejected stale owner")
		}

		return handlerErr
	}

	response, err := m.captureResponse(postCtx, c)
	if err != nil {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: failed to capture replay response", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	record.State = keyStateComplete
	record.Response = response

	completed, err := json.Marshal(record)
	if err != nil {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: failed to marshal completed record", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	applied, err := m.store.Complete(postCtx, key, processing, completed, ttl)
	if err != nil {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: store completion failed", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	if !applied {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: store completion rejected stale owner")

		return m.respondPostHandlerStoreError(c)
	}

	return handlerErr
}

func (m *Middleware) captureResponse(ctx context.Context, c fiber.Ctx) ([]byte, error) {
	body := c.Response().Body()
	if len(body) > m.maxBodyCache {
		m.logger.Log(c.Context(), log.LevelWarn,
			"idempotency: response body exceeds maxBodyCache, skipping cache",
			log.Int("body_size", len(body)),
			log.Int("max_body_cache", m.maxBodyCache),
		)

		return nil, errResponseTooLarge
	}

	headers := make(map[string][]string)

	for hdrKey, value := range c.Response().Header.All() {
		name := string(hdrKey)
		switch name {
		case "Content-Type", "Content-Length", "Transfer-Encoding", chttp.IdempotencyReplayed:
			continue
		}

		headers[name] = append(headers[name], string(value))
	}

	response := cachedResponse{
		StatusCode:  c.Response().StatusCode(),
		ContentType: string(c.Response().Header.ContentType()),
		Body:        append([]byte(nil), body...),
		Headers:     headers,
	}

	plaintext, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("marshal replay response: %w", err)
	}

	encoded, err := m.responseCodec.Encode(ctx, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encode replay response: %w", err)
	}

	if len(encoded) == 0 || len(encoded) > m.maxEncodedResponseBytes() {
		return nil, errResponseTooLarge
	}

	return encoded, nil
}

func (m *Middleware) maxEncodedResponseBytes() int {
	maxInt := int(^uint(0) >> 1)
	if m.maxBodyCache > maxInt/2 {
		return maxInt
	}

	return m.maxBodyCache * 2
}

func (m *Middleware) replay(c fiber.Ctx, encoded []byte) error {
	if len(encoded) == 0 || len(encoded) > m.maxEncodedResponseBytes() {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: completed record has no replay response")

		return m.respondPostHandlerStoreError(c)
	}

	plaintext, err := m.responseCodec.Decode(c.Context(), encoded)
	if err != nil || len(plaintext) == 0 || len(plaintext) > m.maxEncodedResponseBytes() {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: failed to decode replay response", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	var response cachedResponse
	if err := json.Unmarshal(plaintext, &response); err != nil {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: failed to unmarshal replay response", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	if response.StatusCode < http.StatusContinue || response.StatusCode > 599 || len(response.Body) > m.maxBodyCache {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: decoded replay response is invalid", log.Err(errInvalidReplayResponse))

		return m.respondPostHandlerStoreError(c)
	}

	c.Set(chttp.IdempotencyReplayed, "true")

	for name, values := range response.Headers {
		for _, value := range values {
			c.Response().Header.Add(name, value)
		}
	}

	c.Set("Content-Type", response.ContentType)

	return c.Status(response.StatusCode).Send(response.Body)
}

// legacyStateSeparator divides the key state from the request fingerprint in
// the plain-text record lib-commons v6.4.0 and earlier wrote under the primary
// key: "processing:<hex>" / "complete:<hex>".
//
// DELETABLE — and the condition is nameable. Nothing writes this shape any
// more: since v6.5.0 the primary key holds one atomic JSON record. Existing
// legacy values expire with their own TTL, so this branch and everything it
// reaches (decodeLegacyRecord, respondLegacy, and their tests) can be removed
// once no deployment is running lib-commons v6.4.0 or earlier against a shared
// store. Until then, removing it re-executes a legitimate retry's mutation
// during the rolling deploy that crosses the format change.
const legacyStateSeparator = ":"

// decodeLegacyRecord recognises the v6.4.0 plain-text record and reports
// whether stored actually is one.
//
// The detector is deliberately CLOSED: the value must split on the separator
// AND its state part must equal one of the two known states exactly. Any other
// undecodable value is left to the store-error path, unchanged. A permissive
// detector would be a second version of the defect this branch fixes — unknown
// bytes granting permission to answer a mutation without running it, or worse,
// to run it a second time.
//
// The returned record carries no Owner and no Response: a legacy value stored
// neither. The cached body lived in a separate "<key>:response" sidecar that
// the [Store] interface exposes no way to read, which is why the complete case
// reports "already processed" rather than replaying — see respondLegacy.
func decodeLegacyRecord(stored []byte) (storeRecord, bool) {
	state, fingerprint, found := strings.Cut(string(stored), legacyStateSeparator)
	if !found || (state != keyStateProcessing && state != keyStateComplete) {
		return storeRecord{}, false
	}

	return storeRecord{State: state, Fingerprint: fingerprint}, true
}

// respondLegacy answers a request whose key already holds a v6.4.0 record. The
// record is an EXISTING record, never an absent one: falling through to the
// handler would execute the mutation a second time.
//
// The fingerprint gate runs first, before the state routing, exactly as it does
// for the JSON record and as v6.4.0 itself did. Answering a differing payload
// with "already processed" would report success for an operation that never ran.
//
// A complete record cannot be replayed exactly: v6.4.0 kept the response body in
// a separate sidecar key that [Store] cannot read. This is v6.4.0's own answer
// for its own complete-but-uncached case, reused rather than reinvented.
func (m *Middleware) respondLegacy(c fiber.Ctx, legacy storeRecord, fingerprint string) error {
	if legacy.Fingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	if legacy.State == keyStateProcessing {
		return m.respondConflict(c)
	}

	c.Set(chttp.IdempotencyReplayed, "true")

	return libHTTP.Respond(c, http.StatusOK, libHTTP.ErrorResponse{
		Code:    http.StatusOK,
		Title:   "IDEMPOTENT",
		Message: "request already processed",
	})
}

func (m *Middleware) respondConflict(c fiber.Ctx) error {
	c.Set(chttp.IdempotencyReplayed, "true")
	c.Set(fiber.HeaderRetryAfter, retryAfterSeconds)

	if m.onConflict != nil {
		return m.onConflict(c)
	}

	return libHTTP.RespondError(c, http.StatusConflict,
		"IDEMPOTENCY_CONFLICT",
		"a request with this idempotency key is currently being processed",
	)
}

func (m *Middleware) respondKeyReuse(c fiber.Ctx) error {
	if m.onKeyReuse != nil {
		return m.onKeyReuse(c)
	}

	return libHTTP.RespondError(c, http.StatusUnprocessableEntity,
		"IDEMPOTENCY_KEY_REUSE",
		"this idempotency key was already used for a different request; "+
			"do not retry with a new key — reconcile the original request first",
	)
}
