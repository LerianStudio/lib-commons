package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	libHTTP "github.com/LerianStudio/lib-commons/v7/commons/net/http"
	libRedis "github.com/LerianStudio/lib-commons/v7/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

const (
	fingerprintScopeDomain = "lib-commons:idempotency:fingerprint-scope:v1\x00"
	keyStateProcessing     = "processing"
	keyStateComplete       = "complete"
	// outcomeUnrecorded is TERMINAL: the key was spent by a request that left no
	// recorded outcome. It is written where the middleware would otherwise leave
	// the key free — a completion that failed after the handler already ran, and
	// (opt-in) a handler failure or 5xx that the route declares ambiguous. It
	// holds the RETENTION TTL, not the in-flight lease, so the fence outlives
	// the lease that used to lapse into a re-execution.
	//
	// It is a FIELD on the record rather than a third State value, and that is
	// load-bearing rather than stylistic. A new State value is unknown to every
	// reader that predates it, and the unknown-state branch below ends at
	// onStoreError, which for the shipped fail-open default calls c.Next() and
	// RE-EXECUTES the mutation. Measured against the pre-change reader: a third
	// state answered 201 and ran the handler again, while the encoding used here
	// answered 503 "reconcile the original request first" and ran nothing. So
	// the record keeps keyStateComplete for readers that route on State, and
	// those that know this field route on it first. A pre-change reader finds a
	// completed record with no replay response, which it already refuses through
	// the post-handler seam — the same instruction, from the same seam.
	outcomeUnrecorded = "unrecorded"
	retryAfterSeconds = "1"
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
	sum := sha256.Sum256(requestFingerprintInput(method, path, body))

	return hex.EncodeToString(sum[:])
}

func requestFingerprintWithScope(scope, method, path string, body []byte) string {
	var scopeLength [8]byte
	binary.BigEndian.PutUint64(scopeLength[:], uint64(len(scope)))

	legacyInput := requestFingerprintInput(method, path, body)
	input := make([]byte, 0, len(fingerprintScopeDomain)+len(scopeLength)+len(scope)+len(legacyInput))
	input = append(input, fingerprintScopeDomain...)
	input = append(input, scopeLength[:]...)
	input = append(input, scope...)
	input = append(input, legacyInput...)

	sum := sha256.Sum256(input)

	return hex.EncodeToString(sum[:])
}

func requestFingerprintInput(method, path string, body []byte) []byte {
	input := make([]byte, 0, len(method)+len(path)+len(body)+2)
	input = append(input, method...)
	input = append(input, '\n')
	input = append(input, path...)
	input = append(input, '\n')
	input = append(input, body...)

	return input
}

// Option configures the idempotency middleware.
type Option func(*Middleware)

// TTLProvider resolves the retention window for the current request. It is
// evaluated for every keyed mutating request, allowing one middleware instance
// to follow hot-reloaded application policy.
type TTLProvider func(c fiber.Ctx) (time.Duration, error)

// FingerprintScopeProvider resolves an application-defined namespace for the
// current request fingerprint. The scope changes fingerprint comparison only;
// it never changes the storage key. A configured provider opts into scoped
// fingerprinting even when it returns an empty string. Providers must be safe
// for concurrent use.
type FingerprintScopeProvider func(c fiber.Ctx) string

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

// ServerErrorPolicy controls what a handler failure or a 5xx response does to
// the owned idempotency record.
type ServerErrorPolicy uint8

const (
	// ServerErrorPolicyRelease preserves the shipped behavior: the record is
	// compare-safely released, so the same key may be retried immediately.
	ServerErrorPolicyRelease ServerErrorPolicy = iota
	// ServerErrorPolicyFence replaces the record with a terminal
	// outcome-unknown record held for the retention TTL, so the same key is
	// refused for the whole retry window instead of freed.
	ServerErrorPolicyFence
)

// Middleware provides at-most-once request semantics using an atomic [Store].
type Middleware struct {
	store                    Store
	logger                   obs.Logger
	keyPrefix                string
	keyTTL                   time.Duration
	processingTTL            time.Duration
	maxKeyLength             int
	maxBodyCache             int
	redisTimeout             time.Duration
	ttlProvider              TTLProvider
	fingerprintScopeProvider FingerprintScopeProvider
	responseCodec            ResponseCodec
	clientErrorPolicy        ClientErrorPolicy
	serverErrorPolicy        ServerErrorPolicy
	onRejected               func(c fiber.Ctx) error
	onConflict               fiber.Handler
	onKeyReuse               fiber.Handler
	// requireKey and requireTenant are opt-in refusals. Default (false)
	// preserves the shipped bypasses: a request without the X-Idempotency
	// header, or without tenant context, proceeds unprotected.
	requireKey       bool
	requireTenant    bool
	onKeyRequired    func(c fiber.Ctx) error
	onTenantRequired func(c fiber.Ctx) error
	// failClosed inverts the transient-Redis-error behavior. Default (false)
	// fails open — requests proceed without idempotency coverage to preserve
	// availability. When true, transient Redis errors abort with 503 so a
	// mutation never runs without at-most-once protection.
	failClosed    bool
	onUnavailable func(c fiber.Ctx) error
	// onPostHandlerUnavailable answers only failures observed AFTER the
	// handler ran. Unset, the post-handler path falls back to onUnavailable.
	onPostHandlerUnavailable func(c fiber.Ctx) error
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
		logger:            obs.Nop(),
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
func WithLogger(l obs.Logger) Option {
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

// WithProcessingTTL sets how long the in-flight lease taken before the handler
// runs is held, independently of how long a completed record is retained for
// replay. Unset (the default), the lease borrows the retention TTL.
//
// The two are different lifetimes and only look like one. The lease must cover
// the ENTIRE protected operation, with margin — not just handler execution.
// The handler returning is not the end of it: the middleware then captures the
// response, serializes it, runs it through [WithResponseCodec], and only then
// calls Store.Complete, and the key is held by nothing but this lease for all
// of it. A lease sized to the handler alone can therefore lapse in that tail,
// after the mutation has already committed.
//
// Whenever it lapses, wherever it lapses, the damage is the same: a redelivery
// under the same key acquires the key again and the mutation runs a SECOND
// time, and the original request's completion is then rejected because the
// record it owned is gone. Size it against the slowest handler PLUS capture,
// encoding and the store round-trip, and leave headroom.
//
// Retention is unrelated — it is how long a client may still replay the
// receipt, and a caller that wants a short replay window (say five minutes)
// must not have that window silently cap the work it protects.
//
// The lease may therefore be longer than the retention, and usually is.
// Non-positive values are ignored, as in [WithKeyTTL].
func WithProcessingTTL(d time.Duration) Option {
	return func(m *Middleware) {
		if d > 0 {
			m.processingTTL = d
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

// WithFingerprintScopeProvider namespaces request fingerprints with a scope
// resolved for every keyed mutating request. The scope is domain-separated and
// length-prefixed before hashing. A nil provider leaves the legacy fingerprint
// bytes unchanged.
func WithFingerprintScopeProvider(provider FingerprintScopeProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.fingerprintScopeProvider = provider
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

// WithServerErrorPolicy controls what a handler failure or a 5xx response does
// to the owned record. The default is [ServerErrorPolicyRelease], the shipped
// behavior. Invalid values leave the default unchanged.
//
// Releasing reads "the handler refused, nothing happened, retry the same key".
// That inference is right for a route whose failures are refusals and wrong for
// one whose handler can commit before its response is written. A request cut off
// by a deadline is the clearest case: the framework answers 500 while the
// handler is still running, so the key is freed for a redelivery that executes
// the mutation a SECOND time, and the effect of the first is never reconciled.
//
// The middleware cannot tell those apart. At this point a handler error has not
// even reached the application's Fiber error handler yet, so the status the
// caller will finally see does not exist: only the route's owner knows whether a
// failure there means "nothing ran" or "this may have moved money". Mount
// [ServerErrorPolicyFence] on the routes where the answer is the second one:
//
//	money := idempotency.New(conn,
//	    idempotency.WithServerErrorPolicy(idempotency.ServerErrorPolicyFence),
//	    idempotency.WithKeyTTL(24*time.Hour), // how long the key stays fenced
//	)
//	app.Post("/payoffs", money.Check(), createPayoffHandler)
//
// Under it the key is neither released nor left to lapse: it holds a terminal
// outcome-unknown record for the retention TTL, and a resend inside that window
// is refused by the key rather than by the instruction in a refusal body. The
// window is therefore a deliberate choice — it is how long an operator or a
// reconciliation job has to clear the key before it frees itself.
func WithServerErrorPolicy(policy ServerErrorPolicy) Option {
	return func(m *Middleware) {
		if policy == ServerErrorPolicyRelease || policy == ServerErrorPolicyFence {
			m.serverErrorPolicy = policy
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

// WithRequireKey refuses a mutating request that carries no X-Idempotency
// header, before its handler runs. The default is off: an absent header lets
// the request proceed unprotected, which is per-request opt-in idempotency.
// Turn it on for routes where an unkeyed retry would duplicate a side effect
// that cannot be undone, such as a money movement. The refusal is a 400 with
// code "IDEMPOTENCY_KEY_REQUIRED"; use [WithKeyRequiredHandler] to change it.
func WithRequireKey() Option {
	return func(m *Middleware) {
		m.requireKey = true
	}
}

// WithKeyRequiredHandler sets a custom handler for requests refused by
// [WithRequireKey]. By default, a generic 400 JSON response is returned.
func WithKeyRequiredHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onKeyRequired = fn
	}
}

// WithRequireTenant refuses a keyed mutating request whose tenant context is
// empty, before its handler runs. The default is off: a tenant-less request
// bypasses idempotency rather than keying every tenant onto a shared namespace,
// which would break isolation. That reasoning still holds for callers that do
// not opt in; an opted-in caller refuses the request instead, and it is never
// keyed either way. The refusal is a 400 with code
// "IDEMPOTENCY_TENANT_REQUIRED"; use [WithTenantRequiredHandler] to change it.
//
// This check runs AFTER the header check, so on its own it never sees an
// unkeyed request: enabling it alone does NOT refuse every tenant-less
// mutation, because an unkeyed one takes the earlier bypass. Combine it with
// [WithRequireKey] to refuse both.
func WithRequireTenant() Option {
	return func(m *Middleware) {
		m.requireTenant = true
	}
}

// WithTenantRequiredHandler sets a custom handler for requests refused by
// [WithRequireTenant]. By default, a generic 400 JSON response is returned.
func WithTenantRequiredHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onTenantRequired = fn
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

// WithPostHandlerUnavailableHandler sets a custom handler invoked when the
// store fails AFTER the protected handler already ran: the mutation happened
// and only its replay receipt could not be persisted or decoded. Unset, the
// post-handler path falls back to [WithUnavailableHandler] and then to the
// built-in body.
//
// It exists because the two failures carry opposite instructions for the
// caller. Before the handler, nothing ran and retrying is correct. After it,
// the side effect is already committed and a retry under a new key duplicates
// it; the caller must reconcile the original request instead. A single
// [WithUnavailableHandler] override cannot tell an operator which of the two
// happened.
func WithPostHandlerUnavailableHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onPostHandlerUnavailable = fn
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

	return m.respondUnavailable(c)
}

// respondUnavailable answers a failure observed BEFORE the handler ran: nothing
// executed, so retrying the same request is the correct instruction. It always
// refuses; onStoreError owns the fail-open decision, this does not.
func (m *Middleware) respondUnavailable(c fiber.Ctx) error {
	if m.onUnavailable != nil {
		return m.onUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusServiceUnavailable,
		"IDEMPOTENCY_UNAVAILABLE",
		"idempotency store unavailable; request rejected to preserve at-most-once semantics",
	)
}

// respondKeyRequired answers a request refused by [WithRequireKey].
func (m *Middleware) respondKeyRequired(c fiber.Ctx) error {
	if m.onKeyRequired != nil {
		return m.onKeyRequired(c)
	}

	return libHTTP.RespondError(c, http.StatusBadRequest,
		"IDEMPOTENCY_KEY_REQUIRED",
		chttp.IdempotencyKey+" header is required for this request",
	)
}

// respondTenantRequired answers a request refused by [WithRequireTenant].
func (m *Middleware) respondTenantRequired(c fiber.Ctx) error {
	if m.onTenantRequired != nil {
		return m.onTenantRequired(c)
	}

	return libHTTP.RespondError(c, http.StatusBadRequest,
		"IDEMPOTENCY_TENANT_REQUIRED",
		"tenant context is required for this request",
	)
}

// respondPostHandlerStoreError answers a failure observed AFTER the handler
// ran, or one where a completed record exists and cannot be replayed: the
// mutation is committed and a retry under a new key would duplicate it.
func (m *Middleware) respondPostHandlerStoreError(c fiber.Ctx) error {
	if m.onPostHandlerUnavailable != nil {
		return m.onPostHandlerUnavailable(c)
	}

	if m.onUnavailable != nil {
		return m.onUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusServiceUnavailable,
		"IDEMPOTENCY_UNAVAILABLE",
		"request processing finished but its replay response could not be persisted; "+
			"do not retry with a new key — reconcile the original request first",
	)
}

// respondOutcomeUnknown answers a request whose key holds the terminal
// outcome-unknown record: an earlier request under this exact key ran without
// leaving a recorded outcome, and this one must not run.
//
// It answers 422, not 409 and not 503, following what the two statuses already
// mean here. 409 is the in-flight duplicate and carries Retry-After: 1 — an
// invitation to retry that this state can never satisfy, since no amount of
// waiting inside the retention window changes the answer. 503 says the store is
// unavailable, and it is not; the store is answering, the KEY is terminal.
// That leaves 422, which the package already spends on the other terminal
// "this key is spent, reconcile the original request" refusal
// ([respondKeyReuse]), and which no client retries on a timer.
//
// It does NOT replay a body. No response was ever stored for this key — on the
// completion-failure path capture or persistence is exactly what failed, and on
// the fenced-5xx path the handler never produced one. Fabricating a success
// document here would report an outcome nobody recorded.
//
// [WithPostHandlerUnavailableHandler] answers it when set, because the
// instruction is identical to the one that seam already exists for: the side
// effect is committed or unknown, reconcile it, do not retry under a new key.
// It deliberately does not fall through to [WithUnavailableHandler], which
// carries the opposite instruction — nothing ran, retry.
func (m *Middleware) respondOutcomeUnknown(c fiber.Ctx) error {
	if m.onPostHandlerUnavailable != nil {
		return m.onPostHandlerUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusUnprocessableEntity,
		"IDEMPOTENCY_OUTCOME_UNKNOWN",
		"an earlier request with this idempotency key ran without recording its outcome; "+
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
		if m.requireKey {
			return m.respondKeyRequired(c)
		}

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
		if m.requireTenant {
			return m.respondTenantRequired(c)
		}

		// No tenant context — bypass idempotency to avoid collapsing all
		// tenant-less requests onto a shared key, which breaks isolation.
		// This is consistent with the middleware's fail-open philosophy.
		return c.Next()
	}

	key := fmt.Sprintf("%s%s:%s", m.keyPrefix, tenantID, idempotencyKey)
	if nilcheck.Interface(m.store) {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: store unavailable")

		return m.onStoreError(c)
	}

	ctx, cancel := context.WithTimeout(c.Context(), m.redisTimeout)
	defer cancel()

	fingerprint := requestFingerprint(c.Method(), c.Path(), c.Body())
	if m.fingerprintScopeProvider != nil {
		fingerprint = requestFingerprintWithScope(
			m.fingerprintScopeProvider(c),
			c.Method(),
			c.Path(),
			c.Body(),
		)
	}

	ttl, err := m.resolveTTL(c)
	if err != nil {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: TTL provider failed", "error", err)

		// Nothing has run yet: this refusal must not tell the caller to
		// reconcile a mutation that never happened.
		return m.respondUnavailable(c)
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
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: failed to marshal processing record", "error", err)

		return m.onStoreError(c)
	}

	// ttl is the RETENTION window and stays with Complete below. Acquire takes
	// the in-flight lease, which has to survive everything between here and
	// that Complete: the handler, the response capture and encoding, and the
	// store round-trip. Nothing else holds the key for any of it.
	lease := m.processingTTL
	if lease <= 0 {
		lease = ttl
	}

	stored, acquired, err := m.store.Acquire(ctx, key, processing, lease)
	if err != nil {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: store acquire failed", "error", err)

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
			m.logger.Log(ctx, obs.LevelWarn, "idempotency: failed to unmarshal stored record", "error", err)

			return m.onStoreError(c)
		}

		m.logger.Log(ctx, obs.LevelWarn,
			"idempotency: stored record predates the atomic record format, answering from it without replay",
			"record_state", legacy.State)

		return m.respondLegacy(c, legacy, fingerprint)
	}

	if current.Fingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	// Before State, never after: the fenced record deliberately carries
	// keyStateComplete so older readers refuse it, and routing on State first
	// would replay this reader straight past its own fence.
	if current.Outcome == outcomeUnrecorded {
		return m.respondOutcomeUnknown(c)
	}

	switch current.State {
	case keyStateProcessing:
		return m.respondConflict(c)
	case keyStateComplete:
		return m.replay(c, current.Response)
	default:
		// Error, not warn, and it names the value. Under the fail-open default
		// this line is the ONLY trace that a mutation is about to run a second
		// time against a key that already holds something.
		m.logger.Log(ctx, obs.LevelError,
			"idempotency: store returned invalid record state; the request proceeds unprotected unless fail-closed",
			"record_state", current.State,
			"record_outcome", current.Outcome,
			"fail_closed", m.failClosed,
		)

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
		// Releasing here frees the key BEFORE the application's Fiber error
		// handler has even seen handlerErr, so a route that rewrites this into
		// "executed, receipt lost" is rewriting a refusal whose key is already
		// gone. Fencing skips the release entirely rather than trying to order
		// it after a seam the middleware does not own.
		if m.serverErrorPolicy == ServerErrorPolicyFence {
			m.markOutcomeUnknown(c, key, processing, record, ttl)

			return handlerErr
		}

		applied, err := m.store.Release(postCtx, key, processing)
		if err != nil {
			m.logger.Log(postCtx, obs.LevelWarn, "idempotency: store release failed", "error", err)
		} else if !applied {
			m.logger.Log(postCtx, obs.LevelWarn, "idempotency: store release rejected stale owner")
		}

		return handlerErr
	}

	if statusCode >= http.StatusBadRequest && m.clientErrorPolicy == ClientErrorPolicyRelease {
		applied, err := m.store.Release(postCtx, key, processing)
		if err != nil {
			m.logger.Log(postCtx, obs.LevelWarn, "idempotency: client-error cleanup failed", "error", err)
		} else if !applied {
			m.logger.Log(postCtx, obs.LevelWarn, "idempotency: client-error cleanup rejected stale owner")
		}

		return handlerErr
	}

	response, err := m.captureResponse(postCtx, c)
	if err != nil {
		m.logger.Log(postCtx, obs.LevelWarn, "idempotency: failed to capture replay response", "error", err)

		return m.failPostHandler(c, key, processing, record, ttl)
	}

	record.State = keyStateComplete
	record.Response = response

	completed, err := json.Marshal(record)
	if err != nil {
		m.logger.Log(postCtx, obs.LevelWarn, "idempotency: failed to marshal completed record", "error", err)

		return m.failPostHandler(c, key, processing, record, ttl)
	}

	applied, err := m.store.Complete(postCtx, key, processing, completed, ttl)
	if err != nil {
		m.logger.Log(postCtx, obs.LevelWarn, "idempotency: store completion failed", "error", err)

		return m.failPostHandler(c, key, processing, record, ttl)
	}

	if !applied {
		m.logger.Log(postCtx, obs.LevelWarn, "idempotency: store completion rejected stale owner")

		return m.failPostHandler(c, key, processing, record, ttl)
	}

	return handlerErr
}

// failPostHandler answers a failure observed after the handler already ran, and
// fences the key before answering.
//
// The handler committed its mutation and the only thing that failed is the
// receipt. Without the fence the key holds nothing but the processing record,
// which lapses with the in-flight lease and leaves the key free: a resend after
// the lease then executes the mutation a SECOND time under the same key, and
// the only thing standing between the two executions is the prose in this
// refusal. Fencing makes the key itself the refusal for the retention window.
//
// This is unconditional, unlike the handler-failure fence, because there is
// nothing to weigh: no route wants a key it has already spent to come back.
func (m *Middleware) failPostHandler(
	c fiber.Ctx,
	key string,
	processing []byte,
	record storeRecord,
	ttl time.Duration,
) error {
	m.markOutcomeUnknown(c, key, processing, record, ttl)

	return m.respondPostHandlerStoreError(c)
}

// markOutcomeUnknown replaces this request's processing record with the
// terminal outcome-unknown record, held for the retention TTL.
//
// BEST-EFFORT BY CONSTRUCTION, and the limit is nameable: on the
// completion-failure path this writes to the very store that just failed. It
// therefore closes the transient failure — a timeout, a dropped connection, a
// failover — and not a total store outage, during which nothing durable can be
// written under the key at all and the key still lapses with its lease. The
// transient case is the common one and the one that was measured.
//
// It reuses Store.Complete rather than adding a fourth store operation: the
// compare-and-set it already provides is exactly the guard this needs. A write
// lands only while this request still owns the key, so a stale owner — the
// failure mode that brought us here in one of the four cases — leaves the
// current owner's record untouched instead of stamping a fence over it.
//
// The record carries NO response. On this path capture or persistence is what
// failed, and on the fenced-handler-failure path no response exists; storing a
// body here would promise a replay the middleware cannot honour.
func (m *Middleware) markOutcomeUnknown(
	c fiber.Ctx,
	key string,
	processing []byte,
	record storeRecord,
	ttl time.Duration,
) {
	// A fresh deadline, not the caller's: the post-handler context may already
	// be spent by the store call that failed, and an expired context would make
	// this fence unwritable in precisely the timeout case it exists for.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), m.redisTimeout)
	defer cancel()

	record.State = keyStateComplete
	record.Outcome = outcomeUnrecorded
	record.Response = nil

	unknown, err := json.Marshal(record)
	if err != nil {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: failed to marshal outcome-unknown record", "error", err)

		return
	}

	applied, err := m.store.Complete(ctx, key, processing, unknown, ttl)
	if err != nil {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: failed to fence key after unrecorded outcome", "error", err)

		return
	}

	if !applied {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: outcome-unknown fence rejected stale owner")

		return
	}

	m.logger.Log(ctx, obs.LevelWarn, "idempotency: key fenced with an unrecorded outcome", "record_outcome", outcomeUnrecorded)
}

func (m *Middleware) captureResponse(ctx context.Context, c fiber.Ctx) ([]byte, error) {
	body := c.Response().Body()
	if len(body) > m.maxBodyCache {
		m.logger.Log(c.Context(), obs.LevelWarn,
			"idempotency: response body exceeds maxBodyCache, skipping cache",
			"body_size", len(body),
			"max_body_cache", m.maxBodyCache,
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
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: completed record has no replay response")

		return m.respondPostHandlerStoreError(c)
	}

	plaintext, err := m.responseCodec.Decode(c.Context(), encoded)
	if err != nil || len(plaintext) == 0 || len(plaintext) > m.maxEncodedResponseBytes() {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: failed to decode replay response", "error", err)

		return m.respondPostHandlerStoreError(c)
	}

	var response cachedResponse
	if err := json.Unmarshal(plaintext, &response); err != nil {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: failed to unmarshal replay response", "error", err)

		return m.respondPostHandlerStoreError(c)
	}

	if response.StatusCode < http.StatusContinue || response.StatusCode > 599 || len(response.Body) > m.maxBodyCache {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: decoded replay response is invalid", "error", errInvalidReplayResponse)

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
