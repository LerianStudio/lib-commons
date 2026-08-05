package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	"github.com/redis/go-redis/v9"
)

const (
	keyStateProcessing = "processing"
	keyStateComplete   = "complete"
	stateSeparator     = ":"
	retryAfterSeconds  = "1"
)

var (
	errInvalidTTL            = errors.New("idempotency TTL must be positive")
	errResponseTooLarge      = errors.New("idempotency replay response exceeds configured limit")
	errInvalidReplayResponse = errors.New("idempotency replay response is invalid")
	errInvalidStoreRecord    = errors.New("idempotency store record is invalid")
)

type storeFailureClass uint8

const (
	storeFailureUnsafe storeFailureClass = iota
	storeFailureTransientBeforeObservation
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
	legacyReader      legacyResponseReader
	bridgeStore       legacyBridgeStore
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
	legacyBridge      bool
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
	store := newRedisStore(conn)
	m.store = store
	m.legacyReader = store
	m.bridgeStore = store

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

// WithRedisLegacyBridge enables the temporary Redis rolling-upgrade format for
// services moving from lib-commons v6.2. When original and canonical tenant
// spellings differ, bridge writes atomically create the exact v6.2 marker and
// companion response in the original namespace plus a current JSON record in
// the canonical namespace. Reads observe both before acquisition.
// The option is supported only by New with standalone or sentinel Redis;
// NewWithStore and Redis Cluster reject keyed mutations with 503 because they
// cannot satisfy the bridge contract atomically. A custom ResponseCodec is also
// rejected because v6.2 readers require the plaintext cached-response envelope.
func WithRedisLegacyBridge() Option {
	return func(m *Middleware) {
		m.legacyBridge = true
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

// onStoreError fails open only for a connectivity error before this request has
// observed persisted state or acquired a claim. Every other error is ambiguous
// with an existing mutation and therefore fails closed.
func (m *Middleware) onStoreError(c fiber.Ctx, err error, recordObserved bool) error {
	if !m.failClosed && classifyStoreFailure(err, recordObserved) == storeFailureTransientBeforeObservation {
		return c.Next()
	}

	return m.respondUnavailable(c)
}

func classifyStoreFailure(err error, recordObserved bool) storeFailureClass {
	if recordObserved || err == nil {
		return storeFailureUnsafe
	}

	if errors.Is(err, redis.ErrClosed) {
		return storeFailureTransientBeforeObservation
	}

	var operationError *net.OpError
	if errors.As(err, &operationError) && operationError.Op == "dial" {
		return storeFailureTransientBeforeObservation
	}

	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return storeFailureTransientBeforeObservation
	}

	return storeFailureUnsafe
}

func (m *Middleware) respondUnavailable(c fiber.Ctx) error {
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

	var bridgeKeys bridgeKeyPair

	if m.legacyBridge {
		var err error

		bridgeKeys, err = m.resolveBridgeKeys(c, tenantID, idempotencyKey)
		if err != nil {
			m.logger.Log(c.Context(), log.LevelWarn, "idempotency: invalid legacy bridge tenant identity")

			return m.respondBridgeConfigurationError(c,
				"legacy bridge tenant identity is invalid or does not match the authenticated tenant")
		}

		if !usesIdentityResponseCodec(m.responseCodec) {
			m.logger.Log(c.Context(), log.LevelWarn,
				"idempotency: Redis legacy bridge requires the default plaintext response codec")

			return m.respondBridgeConfigurationError(c,
				"legacy bridge requires the default plaintext response codec")
		}

		if m.bridgeStore == nil {
			m.logger.Log(c.Context(), log.LevelWarn,
				"idempotency: Redis legacy bridge requested without built-in Redis store")

			return m.respondUnavailable(c)
		}
	}

	key := fmt.Sprintf("%s%s:%s", m.keyPrefix, tenantID, idempotencyKey)
	if nilcheck.Interface(m.store) {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: store unavailable")

		return m.respondUnavailable(c)
	}

	ctx, cancel := context.WithTimeout(c.Context(), m.redisTimeout)
	defer cancel()

	fingerprint := requestFingerprint(c.Method(), c.Path(), c.Body())

	ttl, err := m.resolveTTL(c)
	if err != nil {
		m.logger.Log(c.Context(), log.LevelWarn, "idempotency: TTL provider failed", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	if m.legacyBridge {
		return m.handleBridgeStore(ctx, c, bridgeKeys, fingerprint, ttl)
	}

	return m.handleStore(ctx, c, key, fingerprint, ttl)
}

func (m *Middleware) resolveBridgeKeys(c fiber.Ctx, trustedTenantID, idempotencyKey string) (bridgeKeyPair, error) {
	canonicalTenantID, err := tmcore.CanonicalTenantID(trustedTenantID)
	if err != nil || canonicalTenantID != trustedTenantID {
		return bridgeKeyPair{}, errInvalidStoreRecord
	}

	originalTenantID := tmcore.GetOriginalTenantIDContext(c.Context())
	if originalTenantID == "" {
		originalTenantID = trustedTenantID
	}

	normalizedOriginal, err := tmcore.CanonicalTenantID(originalTenantID)
	if err != nil || normalizedOriginal != trustedTenantID {
		return bridgeKeyPair{}, errInvalidStoreRecord
	}

	return bridgeKeyPair{
		legacy:    fmt.Sprintf("%s%s:%s", m.keyPrefix, originalTenantID, idempotencyKey),
		canonical: fmt.Sprintf("%s%s:%s", m.keyPrefix, trustedTenantID, idempotencyKey),
	}, nil
}

func usesIdentityResponseCodec(codec ResponseCodec) bool {
	_, ok := codec.(identityResponseCodec)

	return ok
}

func (m *Middleware) respondBridgeConfigurationError(c fiber.Ctx, message string) error {
	if m.onUnavailable != nil {
		return m.onUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", message)
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

		return m.respondUnavailable(c)
	}

	stored, acquired, err := m.store.Acquire(ctx, key, processing, ttl)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: store acquire failed", log.Err(err))

		return m.onStoreError(c, err, false)
	}

	if acquired {
		return m.handleStoreAcquired(c, key, processing, record, ttl)
	}

	return m.handleStoredRecord(ctx, c, key, fingerprint, stored)
}

func (m *Middleware) handleStoredRecord(ctx context.Context, c fiber.Ctx, key, fingerprint string, stored []byte) error {
	decoded, err := decodeCurrentStoreRecord(stored)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: failed to unmarshal stored record", log.Err(err))

		if m.legacyReader != nil && errors.Is(err, errInvalidStoreRecord) && !json.Valid(stored) {
			return m.handleLegacyRecord(ctx, c, key, fingerprint, stored)
		}

		return m.respondUnavailable(c)
	}

	if decoded.Fingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	switch decoded.State {
	case keyStateProcessing:
		return m.respondConflict(c)
	case keyStateComplete:
		return m.replay(c, decoded.Response)
	default:
		m.logger.Log(ctx, log.LevelWarn, "idempotency: store returned invalid record state")

		return m.respondUnavailable(c)
	}
}

func decodeCurrentStoreRecord(stored []byte) (storeRecord, error) {
	var record *storeRecord
	if err := json.Unmarshal(stored, &record); err != nil {
		return storeRecord{}, fmt.Errorf("%w: decode JSON: %w", errInvalidStoreRecord, err)
	}

	if record == nil {
		return storeRecord{}, fmt.Errorf("%w: null JSON", errInvalidStoreRecord)
	}

	if record.State != keyStateProcessing && record.State != keyStateComplete {
		return storeRecord{}, fmt.Errorf("%w: unknown state", errInvalidStoreRecord)
	}

	if !validSHA256Fingerprint(record.Fingerprint) {
		return storeRecord{}, fmt.Errorf("%w: invalid fingerprint", errInvalidStoreRecord)
	}

	if record.Owner == "" {
		return storeRecord{}, fmt.Errorf("%w: owner is required", errInvalidStoreRecord)
	}

	if record.State == keyStateComplete && len(record.Response) == 0 {
		return storeRecord{}, fmt.Errorf("%w: completed record has no response", errInvalidStoreRecord)
	}

	if record.State == keyStateProcessing && len(record.Response) != 0 {
		return storeRecord{}, fmt.Errorf("%w: processing record has a response", errInvalidStoreRecord)
	}

	return *record, nil
}

func validSHA256Fingerprint(fingerprint string) bool {
	if len(fingerprint) != sha256.Size*2 {
		return false
	}

	digest, err := hex.DecodeString(fingerprint)

	return err == nil && len(digest) == sha256.Size
}

func (m *Middleware) handleLegacyRecord(
	ctx context.Context,
	c fiber.Ctx,
	key, fingerprint string,
	stored []byte,
) error {
	state, storedFingerprint, valid := parseLegacyRecord(stored)
	if !valid {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: stored record is neither current nor valid legacy data")

		return m.respondUnavailable(c)
	}

	if storedFingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	switch state {
	case keyStateProcessing:
		return m.respondConflict(c)
	case keyStateComplete:
		response, err := m.legacyReader.ReadLegacyResponse(ctx, key)
		if err != nil {
			m.logger.Log(ctx, log.LevelWarn, "idempotency: failed to read legacy replay response", log.Err(err))

			if errors.Is(err, errLegacyResponseNotFound) {
				return m.respondUnavailable(c)
			}

			return m.onStoreError(c, err, true)
		}

		return m.replayPlaintext(c, response)
	default:
		m.logger.Log(ctx, log.LevelWarn, "idempotency: legacy record has unknown state")

		return m.respondUnavailable(c)
	}
}

func parseLegacyRecord(value []byte) (string, string, bool) {
	state, fingerprint, found := strings.Cut(string(value), stateSeparator)
	if !found || state == "" || !validSHA256Fingerprint(fingerprint) {
		return "", "", false
	}

	return state, fingerprint, true
}

func (m *Middleware) handleBridgeStore(
	ctx context.Context,
	c fiber.Ctx,
	keys bridgeKeyPair,
	fingerprint string,
	ttl time.Duration,
) error {
	owner := uuid.NewString()
	record := storeRecord{State: keyStateProcessing, Fingerprint: fingerprint, Owner: owner}

	canonicalProcessing, err := json.Marshal(record)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: failed to marshal bridge processing record", log.Err(err))

		return m.respondUnavailable(c)
	}

	processing := bridgeRecordPair{
		legacy:    []byte(keyStateProcessing + stateSeparator + fingerprint),
		canonical: canonicalProcessing,
	}

	stored, acquired, err := m.bridgeStore.AcquireBridge(ctx, keys, processing, owner, ttl)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: legacy bridge acquire failed", log.Err(err))

		return m.onStoreError(c, err, false)
	}

	if !acquired {
		return m.handleBridgeStoredRecord(ctx, c, keys, fingerprint, stored)
	}

	return m.handleBridgeAcquired(c, keys, processing, record, ttl)
}

func (m *Middleware) handleBridgeStoredRecord(
	ctx context.Context,
	c fiber.Ctx,
	keys bridgeKeyPair,
	fingerprint string,
	stored bridgeRecordPair,
) error {
	if !keys.dualNamespace() {
		current := stored.legacy
		if len(current) == 0 {
			current = stored.canonical
		}

		return m.handleStoredRecord(ctx, c, keys.canonical, fingerprint, current)
	}

	switch {
	case len(stored.legacy) == 0:
		return m.handleStoredRecord(ctx, c, keys.canonical, fingerprint, stored.canonical)
	case len(stored.canonical) == 0:
		return m.handleLegacyRecord(ctx, c, keys.legacy, fingerprint, stored.legacy)
	}

	legacyState, legacyFingerprint, valid := parseLegacyRecord(stored.legacy)
	if !valid {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: bridge legacy record is invalid")

		return m.respondUnavailable(c)
	}

	currentRecord, err := decodeCurrentStoreRecord(stored.canonical)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: bridge canonical record is invalid", log.Err(err))

		return m.respondUnavailable(c)
	}

	if legacyFingerprint != currentRecord.Fingerprint || legacyState != currentRecord.State {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: bridge namespaces disagree")

		return m.respondUnavailable(c)
	}

	if currentRecord.Fingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	if currentRecord.State == keyStateProcessing {
		return m.respondConflict(c)
	}

	legacyResponse, err := m.legacyReader.ReadLegacyResponse(ctx, keys.legacy)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: failed to read bridge legacy replay response", log.Err(err))

		return m.onStoreError(c, err, true)
	}

	if !bytes.Equal(legacyResponse, currentRecord.Response) {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: bridge replay responses disagree")

		return m.respondUnavailable(c)
	}

	return m.replay(c, currentRecord.Response)
}

func (m *Middleware) handleBridgeAcquired(
	c fiber.Ctx,
	keys bridgeKeyPair,
	processing bridgeRecordPair,
	record storeRecord,
	ttl time.Duration,
) error {
	handlerErr := c.Next()

	postCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), m.redisTimeout)
	defer cancel()

	statusCode := c.Response().StatusCode()
	if handlerErr != nil || statusCode >= http.StatusInternalServerError {
		if err := m.releaseBridge(postCtx, keys, processing, record.Owner,
			"idempotency: legacy bridge release failed"); err != nil {
			return m.respondPostHandlerStoreError(c)
		}

		return handlerErr
	}

	if statusCode >= http.StatusBadRequest && m.clientErrorPolicy == ClientErrorPolicyRelease {
		if err := m.releaseBridge(postCtx, keys, processing, record.Owner,
			"idempotency: legacy bridge client-error cleanup failed"); err != nil {
			return m.respondPostHandlerStoreError(c)
		}

		return handlerErr
	}

	response, err := m.captureResponsePlaintext(c)
	if err != nil {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: failed to capture legacy bridge response", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	// Mirror captureResponse's bound on the whole envelope: headers can push a
	// small body past the replay limit, and a stored-but-unreplayable record
	// would return 503 on every retry until the key expires.
	if len(response) == 0 || len(response) > m.maxEncodedResponseBytes() {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: legacy bridge replay response exceeds the size limit",
			log.Int("response_size", len(response)),
			log.Int("max_encoded_bytes", m.maxEncodedResponseBytes()),
		)

		return m.respondPostHandlerStoreError(c)
	}

	record.State = keyStateComplete
	record.Response = response

	canonicalCompleted, err := json.Marshal(record)
	if err != nil {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: failed to marshal bridge completed record", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	completed := bridgeRecordPair{
		legacy:    []byte(keyStateComplete + stateSeparator + record.Fingerprint),
		canonical: canonicalCompleted,
	}

	applied, err := m.bridgeStore.CompleteBridge(
		postCtx, keys, processing, completed, response, record.Owner, ttl,
	)
	if err != nil {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: legacy bridge completion failed", log.Err(err))

		return m.respondPostHandlerStoreError(c)
	}

	if !applied {
		m.logger.Log(postCtx, log.LevelWarn, "idempotency: legacy bridge completion rejected stale owner")

		return m.respondPostHandlerStoreError(c)
	}

	return handlerErr
}

func (m *Middleware) releaseBridge(
	ctx context.Context,
	keys bridgeKeyPair,
	processing bridgeRecordPair,
	owner, failureMessage string,
) error {
	applied, err := m.bridgeStore.ReleaseBridge(ctx, keys, processing, owner)
	if err != nil {
		m.logger.Log(ctx, log.LevelWarn, failureMessage, log.Err(err))

		return err
	}

	if !applied {
		m.logger.Log(ctx, log.LevelWarn, "idempotency: legacy bridge release rejected stale owner")

		return errInvalidStoreResult
	}

	return nil
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
	plaintext, err := m.captureResponsePlaintext(c)
	if err != nil {
		return nil, err
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

func (m *Middleware) captureResponsePlaintext(c fiber.Ctx) ([]byte, error) {
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

	return plaintext, nil
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

	return m.replayPlaintext(c, plaintext)
}

func (m *Middleware) replayPlaintext(c fiber.Ctx, plaintext []byte) error {
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
