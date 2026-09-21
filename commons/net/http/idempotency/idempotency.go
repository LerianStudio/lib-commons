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
	"strconv"
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

// KeyProvider resolves the idempotency key for the current request from
// somewhere other than the X-Idempotency header — request-scoped state, an
// authenticated principal, a derived namespace. Returning an empty string means
// the request carries no key and takes the unkeyed branch, exactly as a missing
// header does. Providers must be safe for concurrent use.
type KeyProvider func(c fiber.Ctx) (string, error)

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

// FingerprintProvider resolves the bytes that identify the current request,
// replacing the raw body in the fingerprint. See [WithFingerprintProvider] for
// what those bytes must satisfy. Providers must be safe for concurrent use.
type FingerprintProvider func(c fiber.Ctx) ([]byte, error)

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

// The three terminal refusals answered BEFORE the protected handler runs, when
// a duplicate's key holds a record this version cannot act on. Each is the code
// in the built-in 422 body, and the value handed to
// [WithTerminalRefusalHandler] so one handler can tell the three apart.
const (
	// RefusalCodeStateUnrecognised: the record decodes, but carries a state
	// this version does not know. Version skew, pointed forward.
	RefusalCodeStateUnrecognised = "IDEMPOTENCY_STATE_UNRECOGNISED"
	// RefusalCodeRecordUnreadable: the bytes decode as neither the current
	// record format nor the legacy one. Damage, which is not version skew.
	RefusalCodeRecordUnreadable = "IDEMPOTENCY_RECORD_UNREADABLE"
	// RefusalCodeOutcomeUnrecorded: an earlier request under this exact key ran
	// without leaving a recorded outcome, so this one must not run.
	RefusalCodeOutcomeUnrecorded = "IDEMPOTENCY_OUTCOME_UNRECORDED"
)

// Middleware provides at-most-once request semantics using an atomic [Store].
type Middleware struct {
	store                    Store
	logger                   obs.Logger
	keyProvider              KeyProvider
	keyPrefix                string
	keyTTL                   time.Duration
	processingTTL            time.Duration
	maxKeyLength             int
	maxBodyCache             int
	redisTimeout             time.Duration
	ttlProvider              TTLProvider
	fingerprintScopeProvider FingerprintScopeProvider
	fingerprintProvider      FingerprintProvider
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
	// onUnfenced answers the post-handler failure whose FENCE also failed, so
	// the key is not protected. Unset, that case keeps the built-in 503; it
	// never falls back to either seam above.
	onUnfenced func(c fiber.Ctx) error
	// onTerminalRefusal answers ONLY the three pre-handler refusals where the
	// key holds a record this version cannot act on. Unset, each keeps the
	// routing it had: onPostHandlerUnavailable, then the built-in 422.
	onTerminalRefusal func(c fiber.Ctx, code string) error
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

// WithKeyProvider resolves the idempotency key from somewhere other than the
// X-Idempotency header. Unset, the middleware reads the header, which is the
// shipped behaviour and stays byte-identical.
//
// It exists so a service can partition deduplication by something the library
// does not know — most often the authenticated principal — WITHOUT rewriting
// the published request header, which is otherwise the only lever available.
// That rewrite is not a stylistic problem: routes whose handler binds the
// caller's raw key (it becomes an upstream correlation id, or a persisted
// column) then need the original value put back before the handler runs, so one
// published header carries two different values at two different moments and is
// correct only while the chain is assembled in the right order. The middleware
// NEVER writes the request header, so the handler always sees exactly what the
// caller sent.
//
// The resolved value is the key for both the storage key and the fingerprint
// record. An empty return takes the unkeyed branch — pass-through, or the
// [WithRequireKey] refusal — and the value is still subject to
// [WithMaxKeyLength], which bounds the storage key regardless of its source. A
// provider error refuses the request with 503 "IDEMPOTENCY_UNAVAILABLE", or the
// [WithUnavailableHandler] document — the same pre-handler refusal a
// [WithTTLProvider] error takes: nothing has run, so retrying is the correct
// instruction and the caller must not be told to reconcile.
func WithKeyProvider(provider KeyProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.keyProvider = provider
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

// WithFingerprintProvider replaces the raw request body in the fingerprint with
// bytes the application supplies. Unset, the fingerprint covers the body
// exactly as received, which is the shipped behaviour and stays byte-identical
// — the raw body is the default because it is the strictest identity available.
//
// The provider must return the SAME bytes for two requests the application
// considers the same one, and different bytes for two it does not. For a
// multipart upload that is typically the declared part names, filenames and
// sizes plus whatever fields carry identity; for a streamed body it is whatever
// the application can read without consuming the stream. Anything the transport
// re-randomises per request must stay out of it.
//
// Two consumer facts make the raw body unusable on some routes, and both need
// this option. A route served with Fiber's StreamRequestBody hands the handler
// a live body stream; reading the body to fingerprint it drains that stream
// into memory and closes it, so every upload is buffered whole and the
// handler's streaming branch is unreachable. And a multipart encoder picks a
// fresh random boundary per request, so a byte-identical logical retry never
// matches its own stored fingerprint and is refused
// "IDEMPOTENCY_KEY_REUSE" — the published "retry with the same key" contract
// cannot be honoured on any multipart route.
//
// When set, the middleware NEVER calls c.Body(). The provider's bytes take the
// body's place in the digest, under the same method and path, and under the
// [WithFingerprintScopeProvider] scope when one is configured.
//
// A provider error refuses the request with 503 "IDEMPOTENCY_UNAVAILABLE", or
// the [WithUnavailableHandler] document: nothing has run, so retrying is the
// correct instruction, and a request whose identity cannot be established must
// not run unprotected.
func WithFingerprintProvider(provider FingerprintProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.fingerprintProvider = provider
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
//
// # This option puts one obligation on the consumer
//
// The fence is best-effort, so it can fail, and on THIS branch the middleware
// cannot tell you so in the body: it must return the handler's error for your
// own Fiber error handler to own the response. The single carrier is the
// [constants.IdempotencyFenced] response header, "true" or "false".
//
// An error handler that rewrites this 5xx into its own document — which is the
// reason most services adopt this option — MUST read that header and carry the
// distinction into what it writes. Rewriting both outcomes into one document
// puts back exactly the indistinguishability this reports: the client is told
// "this may have executed, reconcile" in both cases and cannot tell whether a
// resend under the same key will be refused or will execute the operation a
// second time. The library cannot discharge that for you without seizing your
// error handling, so it is stated here instead of assumed.
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

// WithUnfencedHandler sets a custom handler invoked when the store failed TWICE
// after the protected handler already ran: once on the replay receipt and once
// on the fence that would have held the key. Unset, that case keeps the
// built-in 503 "IDEMPOTENCY_UNFENCED". It never falls back to
// [WithUnavailableHandler] or [WithPostHandlerUnavailableHandler], which a
// service wires for cases where the key IS protected.
//
// It exists because this is the one branch the library cannot decide. The key
// is unprotected and a resend may execute the operation again, which is why the
// default refuses — but on a route whose handler already committed something
// irreversible, answering "failed" for an operation that succeeded is itself
// what makes the client resend. Only that route's owner can weigh the two, so
// the seam hands the answer over there and leaves every other route's 503
// exactly as it was.
//
// The middleware still sets [constants.IdempotencyFenced] to "false" whatever
// this handler answers, so a client can read that the key is unprotected even
// from a success.
func WithUnfencedHandler(fn func(c fiber.Ctx) error) Option {
	return func(m *Middleware) {
		m.onUnfenced = fn
	}
}

// WithTerminalRefusalHandler sets a custom handler invoked for the three
// terminal refusals answered BEFORE the protected handler runs, when a
// duplicate's key holds a record this version cannot act on: an unrecognised
// state, undecodable bytes, or a key already fenced with an unrecorded outcome.
// The code is passed as [RefusalCodeStateUnrecognised],
// [RefusalCodeRecordUnreadable] or [RefusalCodeOutcomeUnrecorded], so one
// handler can tell the three apart. Unset, each refusal keeps the routing it
// has — [WithPostHandlerUnavailableHandler] when that seam is set, then the
// built-in 422 — so existing callers are unchanged.
//
// It exists because those three currently share a seam with a case they do not
// belong with. [WithPostHandlerUnavailableHandler] answers them, and it also
// answers the post-handler receipt failure, where the mutation is COMMITTED. A
// service that wants only its own envelope on these three cannot take that seam
// without also rewriting the committed-mutation answer, turning a request that
// already moved money into one of these refusals.
//
// Nothing ran on any of the three: they are decided before the protected
// handler. The instruction is still "reconcile the original request", because
// an EARLIER request spent this key and may have committed — not this one.
//
// It never answers the post-handler receipt failure, nor [WithUnfencedHandler]'s
// case. Both of those belong to a request whose own handler already ran.
func WithTerminalRefusalHandler(fn func(c fiber.Ctx, code string) error) Option {
	return func(m *Middleware) {
		m.onTerminalRefusal = fn
	}
}

// WithMaxBodyCache sets the maximum raw response body size (in bytes) that can
// be persisted for exact replay (default: 1 MB). The encoded replay payload is
// bounded to twice this value. A response exceeding either bound fails closed
// with 503 after the handler returns; no generic success response is stored.
// Values <= 0 are ignored.
//
// Exceeding the bound also FENCES THE KEY for the retention TTL, because the
// handler already ran and its receipt is gone — see [WithServerErrorPolicy] for
// what a fenced key answers. This is unconditional, not gated by any option,
// and it applies to every route this middleware covers. A route that
// legitimately returns bodies over the bound therefore burns each idempotency
// key for the whole retention window, rather than failing and freeing it: size
// the bound for the largest response you mean to replay. The alternative is
// worse — before the fence such a route answered 503 forever AND re-executed
// the operation on every resend once the in-flight lease lapsed.
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

// respondUnrecognisedState answers a duplicate whose key holds a record written
// in a state this version does not know.
//
// It refuses REGARDLESS of [WithFailClosed], and that is the one place in the
// package where the fail-open default is overruled. Fail-open exists for the
// case where the middleware learned NOTHING — no store, a store error, no
// readable record — and letting the request through costs availability and
// risks a duplicate only if one was in flight. Here the middleware learned the
// opposite: the key demonstrably holds somebody's record. Proceeding is not
// "unprotected", it is executing on top of a request whose outcome is sitting
// in the store, unreadable only because it was written by a newer version.
//
// This is the mixed-version problem pointed FORWARD. The record encoding is
// built so a future reader is safe against what this version writes; this arm
// is what keeps this version safe against what a future one writes. Without it
// the next new state value reintroduces the exact double-execution this package
// was changed to prevent, on the older half of every rolling upgrade.
//
// Nothing is written, so the record survives untouched and a reader that does
// understand it — the upgraded pod next door, or this one after the upgrade —
// still answers from it correctly.
//
// The answer is terminal rather than a retry invitation: waiting does not teach
// this instance a state it does not have. [WithTerminalRefusalHandler] answers
// it when wired; otherwise it routes through
// [WithPostHandlerUnavailableHandler] when set, because the instruction is the
// one that seam already carries — an earlier request spent this key and may
// have committed, so reconcile it rather than resending.
//
// A store that actually errors is unchanged and keeps the configured policy:
// that path has no record to reason about.
func (m *Middleware) respondUnrecognisedState(c fiber.Ctx) error {
	if m.onTerminalRefusal != nil {
		return m.onTerminalRefusal(c, RefusalCodeStateUnrecognised)
	}

	if m.onPostHandlerUnavailable != nil {
		return m.onPostHandlerUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusUnprocessableEntity,
		RefusalCodeStateUnrecognised,
		"this idempotency key holds a record written by a newer version and cannot be interpreted here; "+
			"do not retry with a new key — reconcile the original request first",
	)
}

// respondUnreadableRecord answers a duplicate whose key holds bytes that decode
// as neither the current record format nor the legacy one.
//
// It refuses regardless of [WithFailClosed], for the reason spelled out on
// [Middleware.respondUnrecognisedState], and the reason transfers without
// weakening: the discriminator was never whether the bytes parse, it is whether
// the store handed back an existing value at all. Acquire returning
// acquired=false is proof the key is occupied by a live record with an
// unexpired TTL, and corrupt bytes say nothing about that. The key is spent and
// its outcome is unknowable — which is strictly less information than the
// unrecognised-state case, not more.
//
// Reading it the other way is the exact failure decodeLegacyRecord was written
// to prevent: unknown bytes granting permission to run a mutation a second
// time. That detector is deliberately closed; this is where its rejects land,
// and it would be self-defeating for them to land somewhere that executes.
//
// It carries its own code rather than reusing the unrecognised-state one. That
// message says "written by a newer version", which is true for version skew and
// false here: these bytes are damaged, truncated, or were written by something
// that is not this middleware, and an operator triaging the two needs to tell
// them apart. Nothing is written, so the bytes survive for inspection.
//
// [WithTerminalRefusalHandler] answers it when wired, and otherwise it routes
// through [WithPostHandlerUnavailableHandler] like the branch above.
func (m *Middleware) respondUnreadableRecord(c fiber.Ctx) error {
	if m.onTerminalRefusal != nil {
		return m.onTerminalRefusal(c, RefusalCodeRecordUnreadable)
	}

	if m.onPostHandlerUnavailable != nil {
		return m.onPostHandlerUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusUnprocessableEntity,
		RefusalCodeRecordUnreadable,
		"this idempotency key holds a record that cannot be decoded; "+
			"do not retry with a new key — reconcile the original request first",
	)
}

// respondUnfenced answers a request whose operation may already have committed
// and whose key could NOT be fenced: the store failed twice, once on the
// receipt and once on the fence.
//
// It is a separate document from respondPostHandlerStoreError on purpose. Both
// say "reconcile", but only this one says the key is unprotected, and that is
// the difference between a resend being refused and a resend arming the
// operation a second time. It keeps 503, because a store that failed twice IS
// unavailable, and carries no Retry-After: retrying is precisely what the
// caller must not do until it has reconciled.
//
// It does not route through [WithPostHandlerUnavailableHandler], nor through
// [WithUnavailableHandler]. A service that wired either one wired it for a case
// where the key IS protected, and answering this case with that document would
// put the indistinguishability straight back.
//
// [WithUnfencedHandler] is the seam for this case and this case alone, because
// a route whose handler already committed something irreversible may owe its
// client the committed outcome rather than a failure the client will resend,
// and only that route's owner can weigh it. Unset, the 503 below stands. Either
// way [constants.IdempotencyFenced] carries "false", so a service that wants
// one document for both cases can still produce it.
func (m *Middleware) respondUnfenced(c fiber.Ctx) error {
	if m.onUnfenced != nil {
		return m.onUnfenced(c)
	}

	return libHTTP.RespondError(c, http.StatusServiceUnavailable,
		"IDEMPOTENCY_UNFENCED",
		"request processing finished but neither its replay response nor a fence could be persisted; "+
			"this idempotency key is NOT protected and a resend may execute the operation again — "+
			"reconcile the original request before sending anything under this key",
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
// [WithTerminalRefusalHandler] answers it when wired, ahead of everything
// below, because it is one of the three refusals that seam exists for.
// Otherwise [WithPostHandlerUnavailableHandler] answers it when set, because
// the instruction is identical to the one that seam already exists for: the
// side effect is committed or unknown, reconcile it, do not retry under a new
// key. It deliberately does not fall through to [WithUnavailableHandler], which
// carries the opposite instruction — nothing ran, retry.
func (m *Middleware) respondOutcomeUnknown(c fiber.Ctx) error {
	if m.onTerminalRefusal != nil {
		return m.onTerminalRefusal(c, RefusalCodeOutcomeUnrecorded)
	}

	if m.onPostHandlerUnavailable != nil {
		return m.onPostHandlerUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusUnprocessableEntity,
		RefusalCodeOutcomeUnrecorded,
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

	idempotencyKey, err := m.resolveKey(c)
	if err != nil {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: key provider failed", "error", err)

		// Nothing has run yet: this refusal must not tell the caller to
		// reconcile a mutation that never happened.
		return m.respondUnavailable(c)
	}

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

	fingerprint, err := m.resolveFingerprint(c)
	if err != nil {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: fingerprint provider failed", "error", err)

		// Nothing has run yet, and the request's identity is unknown: it must
		// neither proceed unprotected nor be told to reconcile.
		return m.respondUnavailable(c)
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

// resolveKey reads the idempotency key for this request. Without a provider it
// is the X-Idempotency header, which is the shipped behaviour.
func (m *Middleware) resolveKey(c fiber.Ctx) (string, error) {
	if m.keyProvider != nil {
		return m.keyProvider(c)
	}

	return c.Get(chttp.IdempotencyKey), nil
}

// resolveFingerprint builds the digest that identifies WHICH request spent this
// key. Without a [WithFingerprintProvider] the identity bytes are the raw body,
// which is the shipped behaviour down to the legacy input layout used by the
// scoped form; with one, they are the provider's and c.Body() is never called.
func (m *Middleware) resolveFingerprint(c fiber.Ctx) (string, error) {
	var identity []byte

	if m.fingerprintProvider != nil {
		var err error

		identity, err = m.fingerprintProvider(c)
		if err != nil {
			return "", err
		}
	} else {
		// Only reachable without a provider: c.Body() drains and closes a
		// streamed request body, which is the defect the provider exists for.
		identity = c.Body()
	}

	if m.fingerprintScopeProvider != nil {
		return requestFingerprintWithScope(m.fingerprintScopeProvider(c), c.Method(), c.Path(), identity), nil
	}

	return requestFingerprint(c.Method(), c.Path(), identity), nil
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
			m.logger.Log(ctx, obs.LevelError,
				"idempotency: stored record could not be decoded; refusing the request",
				"idempotency_key_digest", keyDigest(key),
				"tenant_id", tmcore.GetTenantIDContext(ctx),
				"record_bytes", len(stored),
				"error", err,
			)

			return m.respondUnreadableRecord(c)
		}

		m.logger.Log(ctx, obs.LevelWarn,
			"idempotency: stored record predates the atomic record format, answering from it without replay",
			"record_state", legacy.State)

		return m.respondLegacy(c, legacy, fingerprint)
	}

	if current.Fingerprint != fingerprint {
		return m.respondKeyReuse(c)
	}

	// Before replay. The fenced record deliberately carries keyStateComplete so
	// that older readers refuse it, so this reader must consult Outcome before
	// it would hand the record to m.replay. (Checking it here rather than
	// inside the complete arm is placement, not the invariant: what must not
	// happen is a replay attempt on a record whose outcome was never recorded.)
	if current.Outcome == outcomeUnrecorded {
		return m.respondOutcomeUnknown(c)
	}

	switch current.State {
	case keyStateProcessing:
		return m.respondConflict(c)
	case keyStateComplete:
		return m.replay(c, current.Response)
	default:
		// Error, and it names the value: this is version skew, and an operator
		// needs to see which state nobody here understands.
		m.logger.Log(ctx, obs.LevelError,
			"idempotency: stored record holds a state this version does not recognise; refusing the request",
			"record_state", current.State,
			"record_outcome", current.Outcome,
			"fail_closed", m.failClosed,
		)

		return m.respondUnrecognisedState(c)
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
			// The result is deliberately not turned into a document here. This
			// branch must return handlerErr so the application's error handler
			// runs and owns the response; authoring one would take that over.
			// The caller still learns the outcome: markOutcomeUnknown sets the
			// [constants.IdempotencyFenced] header either way, and a failed
			// fence logs at ERROR naming the key.
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
//
// The two outcomes get two documents, and that split is the point. Answering
// the same 503 whether or not the fence landed tells the caller "reconcile"
// while hiding whether the key is actually held — so a client that resends is
// refused in one case and arms the operation a second time in the other, with
// nothing in the response to say which. The caller cannot be asked to guess
// that, so it is stated.
func (m *Middleware) failPostHandler(
	c fiber.Ctx,
	key string,
	processing []byte,
	record storeRecord,
	ttl time.Duration,
) error {
	if !m.markOutcomeUnknown(c, key, processing, record, ttl) {
		return m.respondUnfenced(c)
	}

	return m.respondPostHandlerStoreError(c)
}

// markOutcomeUnknown replaces this request's processing record with the
// terminal fenced record, held for the retention TTL, and REPORTS WHETHER THE
// FENCE LANDED.
//
// The return value is the whole point of the signature. This is best-effort by
// construction — on the completion-failure path it writes to the very store
// that just failed — so it closes a transient failure (a timeout, a dropped
// connection, a failover) and not a total store outage, during which nothing
// durable can be written under the key at all and the key still lapses with its
// lease. Best-effort is acceptable; being unable to tell which effort failed is
// not. A caller that cannot distinguish a held key from a free one answers the
// same document either way, and the client cannot know whether resending arms
// the operation a second time. So every exit reports, every failing exit logs at
// ERROR, and every log line names the key.
//
// It reuses Store.Complete rather than adding a fourth store operation: the
// compare-and-set it already provides is exactly the guard this needs. A write
// lands only while this request still owns the key, so a stale owner — the
// failure mode that brought us here in one of the four cases — leaves the
// current owner's record untouched instead of stamping a fence over it. That
// case reports false too: the key is held by somebody, but not by this request's
// fence, and this request's outcome is still unrecorded.
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
) bool {
	// A fresh deadline, not the caller's: the post-handler context may already
	// be spent by the store call that failed, and an expired context would make
	// this fence unwritable in precisely the timeout case it exists for.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), m.redisTimeout)
	defer cancel()

	record.State = keyStateComplete
	record.Outcome = outcomeUnrecorded
	record.Response = nil

	fenced := false

	defer func() {
		// Machine-readable on EVERY path that attempts a fence, including the
		// handler-failure one where the middleware writes no document of its
		// own and the application's error handler owns the response. A header
		// survives that, so "false" reaches the client either way.
		c.Set(chttp.IdempotencyFenced, strconv.FormatBool(fenced))
	}()

	unknown, err := json.Marshal(record)
	if err != nil {
		m.logFenceFailure(ctx, key, record.Owner, "failed to marshal the fenced record", err)

		return false
	}

	applied, err := m.store.Complete(ctx, key, processing, unknown, ttl)
	if err != nil {
		m.logFenceFailure(ctx, key, record.Owner, "the store rejected the fence write", err)

		return false
	}

	if !applied {
		m.logFenceFailure(ctx, key, record.Owner, "the fence write found a stale owner", nil)

		return false
	}

	fenced = true

	m.logger.Log(ctx, obs.LevelWarn, "idempotency: key fenced with an unrecorded outcome",
		"record_outcome", outcomeUnrecorded,
		"idempotency_key_digest", keyDigest(key),
		"tenant_id", tmcore.GetTenantIDContext(ctx),
		"owner", record.Owner,
	)

	return true
}

// logFenceFailure reports a key left UNFENCED after its operation may already
// have committed. ERROR, not warn: this is the one event in the package where a
// later resend can duplicate an effect and nothing durable will stop it.
//
// It names the key so the alert is actionable — an operator woken by this has
// to find the one money request to reconcile, and "the fence write failed" with
// no key is an alert nobody can act on. The key is logged as a SHA-256 DIGEST
// of the store key, never raw: the idempotency key is client-supplied and
// services put business references in it, so the raw value does not belong in
// shared log infrastructure. The digest is reproducible from the client's own
// key plus the tenant and prefix, which is exactly what an operator holds. The
// tenant ID and the acquisition owner are logged raw; neither is client-supplied
// and the owner is what correlates this line with the stored record.
func (m *Middleware) logFenceFailure(ctx context.Context, key, owner, cause string, err error) {
	m.logger.Log(ctx, obs.LevelError,
		"idempotency: key left UNFENCED after an unrecorded outcome; a resend may execute the operation again — "+cause,
		"idempotency_key_digest", keyDigest(key),
		"tenant_id", tmcore.GetTenantIDContext(ctx),
		"owner", owner,
		"error", err,
	)
}

// keyDigest hashes a store key for logging. See logFenceFailure for why the raw
// key never reaches a log line.
func keyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))

	return hex.EncodeToString(sum[:])
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
		// The capture is the authority for the names it holds, so clear each one
		// before re-applying it. Adding blind duplicates every header that a
		// globally mounted middleware (cors, helmet, the common app.Use shape)
		// has already put on THIS response and that the capture also holds. A
		// browser rejects a response whose Access-Control-Allow-Origin "contains
		// multiple values", so without this Del a double-clicked mutation that
		// committed is reported to the user as a network failure.
		//
		// Del is scoped to the captured names: a header the app set on this
		// request that the capture does not hold is left alone, and a captured
		// header with several values (two Set-Cookie, two Link) is re-applied
		// whole, in order.
		c.Response().Header.Del(name)

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
