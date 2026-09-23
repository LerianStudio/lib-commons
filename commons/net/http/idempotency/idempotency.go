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
	"slices"
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
	// outcomeNotReplayable is terminal for the RECEIPT and for nothing else:
	// the handler ran to completion, the client received its response
	// unchanged, and only the stored copy is missing because it exceeded
	// [WithMaxBodyCache]. A body size is not a fault, so this is not a fence —
	// the record is a real completion — but the key still cannot answer a
	// resend with a replay it never stored, so a duplicate is refused by
	// respondReplayUnavailable rather than executing the handler again.
	//
	// It marks a rejection the same way it marks a success, because the status
	// is not what decides whether a 4xx may re-execute: [ClientErrorPolicy] is,
	// and it is consulted before the capture ever runs.
	//
	// It rides the same field as outcomeUnrecorded, and for the same
	// compatibility reason spelled out above: a reader that predates this value
	// finds a completed record with no replay response and refuses it through
	// the post-handler seam. It never re-executes.
	outcomeNotReplayable = "not-replayable"
	retryAfterSeconds    = "1"
)

var (
	errInvalidTTL            = errors.New("idempotency TTL must be positive")
	errResponseTooLarge      = errors.New("idempotency replay response exceeds configured limit")
	errEmptyEncodedResponse  = errors.New("idempotency response codec produced no bytes")
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

// TenantProvider resolves the tenant the current request's record is rooted
// at, replacing the tenant-manager context read. See [WithTenantProvider].
// Providers must be safe for concurrent use.
type TenantProvider func(c fiber.Ctx) (string, error)

// ClientErrorPolicy controls whether successful handler returns with a 4xx
// status are replayed or release their owned idempotency record.
type ClientErrorPolicy uint8

const (
	// ClientErrorPolicyCache preserves the default behavior and replays 4xx
	// responses exactly. A 4xx whose body exceeded [WithMaxBodyCache] has no
	// stored copy to replay, so a duplicate is refused with 409
	// [RefusalCodeReplayUnavailable] instead — still without re-executing,
	// which is what this policy buys.
	ClientErrorPolicyCache ClientErrorPolicy = iota
	// ClientErrorPolicyRelease removes the owned processing record for 4xx
	// responses, allowing corrected requests to reuse the same key.
	ClientErrorPolicyRelease
)

// ClientErrorPolicyFunc decides the [ClientErrorPolicy] for one response,
// consulted only for a 4xx the handler chain wrote. See
// [WithClientErrorPolicyFunc] for what status carries and what it does not.
// Functions must be safe for concurrent use.
type ClientErrorPolicyFunc func(c fiber.Ctx, status int) ClientErrorPolicy

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

// ServerErrorPolicyFunc decides the [ServerErrorPolicy] for one response,
// consulted for a handler failure or a 5xx. err is the error the handler
// returned, nil when it only wrote the status; status is the effective status
// described in [WithServerErrorPolicyFunc]. Functions must be safe for
// concurrent use.
type ServerErrorPolicyFunc func(c fiber.Ctx, status int, err error) ServerErrorPolicy

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

	// RefusalCodeReplayUnavailable is the fourth refusal, and deliberately not
	// one of the three above: it reports a KNOWN outcome whose response exceeded
	// [WithMaxBodyCache] and was therefore never stored. It travels in a 409
	// body, is answered by [WithReplayUnavailableHandler] rather than
	// [WithTerminalRefusalHandler], and is exported for the same reason the
	// others are — a client routes on the code, so it must be nameable.
	RefusalCodeReplayUnavailable = "IDEMPOTENCY_REPLAY_UNAVAILABLE"
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
	processingTTLProvider    TTLProvider
	fingerprintScopeProvider FingerprintScopeProvider
	fingerprintProvider      FingerprintProvider
	tenantProvider           TenantProvider
	responseCodec            ResponseCodec
	clientErrorPolicy        ClientErrorPolicy
	serverErrorPolicy        ServerErrorPolicy
	clientErrorPolicyFunc    ClientErrorPolicyFunc
	serverErrorPolicyFunc    ServerErrorPolicyFunc
	onRejected               func(c fiber.Ctx) error
	onConflict               fiber.Handler
	onKeyReuse               fiber.Handler
	// onReplayUnavailable answers a duplicate whose key holds a real
	// completion with no stored receipt, because the response exceeded
	// maxBodyCache. Unset, the built-in 409 stands.
	onReplayUnavailable fiber.Handler
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

// WithProcessingTTLProvider resolves the in-flight lease for every request, the
// way [WithTTLProvider] resolves the retention, so one middleware instance can
// follow hot-reloaded application policy — a service whose retry window moves
// at runtime cannot make the fixed [WithProcessingTTL] follow it. When set it
// takes precedence over [WithProcessingTTL].
//
// It is evaluated before each acquisition ATTEMPT, including attempts that turn
// out to be duplicates and are answered with a conflict or a replay, and it
// runs above the store deadline alongside the other application providers, so
// its own I/O is never charged to [WithRedisTimeout]. A lease already written
// into the store keeps the value it was taken with: a later change applies to
// the next acquisition and never re-sizes a lease under a handler still running
// behind it.
//
// Unlike [WithTTLProvider], a provider error or a non-positive value does NOT
// refuse the request: it falls back to exactly [WithProcessingTTL], and to the
// retention TTL when that option is unset. The asymmetry is deliberate — an
// unresolvable RETENTION breaks the replay contract in the unsafe direction, so
// it fails closed, while a request whose lease cannot be resolved can still run
// safely under the lease the route already declared.
//
// That fallback is a size the OPERATOR chose, not a safe one the library
// picked. Configuring both options means [WithProcessingTTL] is what an
// unresolvable provider lands on, however short: a 50ms constant behind a
// provider that normally returns 30 minutes yields a 50ms lease the moment the
// provider cannot answer, and with it the mid-flight lapse and double execution
// [WithProcessingTTL] documents at length. Size that constant to cover the
// handler and its completion on its own, or leave it unset — the fallback is
// then the retention TTL, which is the lease an unconfigured middleware takes.
//
// Everything that option says about SIZING the lease applies unchanged: the
// value must cover the handler plus response capture, encoding and the store
// round-trip, with margin, whatever resolved it.
//
// The provider runs on the request goroutine and must be safe for concurrent
// use.
func WithProcessingTTLProvider(provider TTLProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.processingTTLProvider = provider
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
// Because nothing here reads the body, a LARGE request this middleware answers
// itself — a replay, or any refusal — leaves the upload unread, and that
// response carries "Connection: close" so the next request on the connection is
// not parsed from the middle of this one's body. A pooled client dials again and
// loses no request. A body fasthttp had already buffered in full before the
// chain started keeps its connection instead; the package documentation states
// the rule and the bound in full.
//
// A provider error refuses the request with 503 "IDEMPOTENCY_UNAVAILABLE", or
// the [WithUnavailableHandler] document: nothing has run, so retrying is the
// correct instruction, and a request whose identity cannot be established must
// not run unprotected.
//
// TURNING THIS ON CHANGES THE DIGEST of the same logical request, so a retry
// that straddles the deploy is refused "IDEMPOTENCY_KEY_REUSE": the original
// was fingerprinted by a pod that hashed the body, the retry by one that hashes
// the provider's bytes, and the fingerprint gate reads that as a different
// request under a spent key. Nothing executes twice — the refusal is the safe
// side — but a caller retrying a large upload is turned away until the original
// record expires. Either give the route a retention TTL shorter than the
// rollout (see [WithKeyTTL] and [WithTTLProvider]) or accept one retention
// window of that refusal on retries crossing the deploy. Changing the bytes an
// existing provider returns has exactly the same effect, for the same reason.
func WithFingerprintProvider(provider FingerprintProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.fingerprintProvider = provider
		}
	}
}

// WithTenantProvider resolves the tenant a record is rooted at from somewhere
// other than the tenant-manager context. Unset, the middleware reads
// [tmcore.GetTenantIDContext], which is the shipped behaviour and stays
// byte-identical.
//
// It exists so a service whose tenant lives anywhere else — its own claim
// parsing, a different major of the tenant-manager package — is heard WITHOUT
// overwriting the request context. That overwrite is not cosmetic: every
// handler, emitter and audit write below the middleware reads the same
// context, so feeding the middleware that way hands all of them a tenant value
// the application did not choose for them. When set, the provider is the ONLY
// source: the middleware neither reads nor writes the tenant-manager context.
//
// The provider is called once per keyed request, after the key checks. The
// middleware roots the record at EXACTLY the string returned — the key is
// <prefix><tenant>:<key> — so the consumer owns canonicalisation: two
// spellings of one tenant are two namespaces, and a provider whose output
// changes shape across a deploy orphans every record written before it.
//
// An empty return or an error takes the absent-tenant branch, exactly as an
// empty tenant-manager context does: pass-through by default, the
// [WithRequireTenant] refusal when opted in. The error is logged; it is never a
// refusal of its own.
func WithTenantProvider(provider TenantProvider) Option {
	return func(m *Middleware) {
		if provider != nil {
			m.tenantProvider = provider
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

// WithClientErrorPolicyFunc decides the 4xx policy per response instead of per
// middleware. It is consulted after the handler chain returns, only for a 4xx,
// and when set it replaces [WithClientErrorPolicy] entirely so the two forms
// cannot disagree. A nil function leaves the enum in place, and a return value
// that is neither constant reads as the default [ClientErrorPolicyCache].
//
// The enum cannot serve a guard mounted ABOVE a rate limiter or a quota gate:
// some of the 4xx it observes were written below it and are not the handler's
// answer at all, so caching them spends the caller's key on a transient refusal
// and replays it for the whole retention window.
//
//	idempotency.WithClientErrorPolicyFunc(func(_ fiber.Ctx, status int) idempotency.ClientErrorPolicy {
//	    if status == fiber.StatusTooManyRequests || status == fiber.StatusPaymentRequired {
//	        return idempotency.ClientErrorPolicyRelease // refused below the guard: not an attempt
//	    }
//
//	    return idempotency.ClientErrorPolicyCache // the handler's own rejection
//	})
//
// # Only a 4xx that was WRITTEN reaches this function
//
// A handler or middleware can deliver a 4xx two ways, and only one of them is a
// client error as far as this middleware is concerned. Writing the status and
// returning nil reaches this function. RETURNING the 4xx instead — a
// fiber.NewError(fiber.StatusTooManyRequests, …) that the application's Fiber
// error handler will turn into a document later — takes the handler-failure
// branch, so it reaches [WithServerErrorPolicyFunc] instead, with a non-nil err
// and 429 as its status.
//
// A handler that WRITES a 4xx and ALSO returns an error takes that same
// handler-failure branch: the server seam receives the written 4xx as its
// status together with the non-nil err, and this function is not consulted.
//
// This is not hypothetical for the case this option exists for: lib-commons'
// own rate limiter writes its built-in 429 and returns nil, which arrives here,
// but under commons/net/http/ratelimit.WithExceededHandler the consumer's
// handler owns the return value and an error returned from it arrives at the
// server seam instead. A route that wants one rule for both shapes must install
// both functions.
//
// The function runs on the request goroutine with the response already written,
// so it may read the response the chain produced, and it must be safe for
// concurrent use.
func WithClientErrorPolicyFunc(fn ClientErrorPolicyFunc) Option {
	return func(m *Middleware) {
		if fn != nil {
			m.clientErrorPolicyFunc = fn
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

// WithServerErrorPolicyFunc decides the 5xx policy per response instead of per
// middleware. It is consulted after the handler chain returns, for a handler
// error or a 5xx response, and when set it replaces [WithServerErrorPolicy]
// entirely so the two forms cannot disagree. A nil function leaves the enum in
// place, and a return value that is neither constant reads as the default
// [ServerErrorPolicyRelease].
//
// The enum fences every 5xx as "may have been applied", and a route that KNOWS
// some of its failures did not apply — a downstream target declining before
// anything was written, reported under its own error code — then holds those
// keys for the whole retention window for nothing. This seam frees those and
// fences the rest.
//
//	idempotency.WithServerErrorPolicyFunc(func(_ fiber.Ctx, _ int, err error) idempotency.ServerErrorPolicy {
//	    if errors.Is(err, ErrTargetDeclined) {
//	        return idempotency.ServerErrorPolicyRelease // nothing was written
//	    }
//
//	    return idempotency.ServerErrorPolicyFence // may have been applied
//	})
//
// # What the two arguments mean
//
// err is the error the handler returned, and is nil when the handler only wrote
// the status. It is the only argument here that is a fact rather than a
// forecast, and a route that needs certainty reads it.
//
// status is the EFFECTIVE status: what the handler wrote, or — when the handler
// returned an error having written nothing — the code inside that error when it
// is a *fiber.Error. So a returned fiber.NewError(fiber.StatusTooManyRequests,
// …) arrives here as 429 rather than as the untouched 200 the response object
// still carries. That code has not been written by anything: the application's
// Fiber error handler has not run yet and may map, wrap or replace it. It is
// the best available forecast of the caller's status, not a promise.
//
// A returned error that is NOT a *fiber.Error leaves status at whatever the
// response holds, which for an untouched response is 200. A handler that wrote
// a status and ALSO returned an error reports the written status, because it
// wrote a response and that is the honest report. A handler that STREAMED a
// response counts as having written one for the same reason, and the stream is
// never read to establish that: reading it would buffer the whole body past
// [WithMaxBodyCache] to decide a forecast.
//
// This seam is also where a 4xx RETURNED as an error arrives: it has written no
// response, so the middleware sees a handler failure and never consults
// [WithClientErrorPolicyFunc] for it. A route delivering rejections that way
// must handle them here, or the fence will hold keys spent on client errors.
//
// Everything [WithServerErrorPolicy] says about the fence still applies to the
// responses this function fences, including the [constants.IdempotencyFenced]
// header obligation on an error handler that rewrites the 5xx.
//
// The function runs on the request goroutine and must be safe for concurrent
// use.
func WithServerErrorPolicyFunc(fn ServerErrorPolicyFunc) Option {
	return func(m *Middleware) {
		if fn != nil {
			m.serverErrorPolicyFunc = fn
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

// WithReplayUnavailableHandler sets a custom handler for a duplicate whose key
// holds a COMPLETED operation with no replayable receipt: the original response
// exceeded [WithMaxBodyCache], so it was delivered to its client and never
// stored. By default a 409 with code [RefusalCodeReplayUnavailable] is
// returned.
//
// It is its own seam because the case is its own thing, and every neighbouring
// document would misreport it. It is not the in-flight conflict: nothing is
// running, and Retry-After would invite a retry that can never succeed. It is
// not [WithKeyReuseHandler]'s refusal: the payload matches, this IS the same
// request. It is not [WithPostHandlerUnavailableHandler]'s 503: the store is
// healthy and the operation is not in doubt — it completed, and the client of
// the original request was told so. And it is emphatically not a replay: no
// body exists to send, so [constants.IdempotencyReplayed] stays unset.
//
// What the caller needs to learn is narrow: the operation under this key
// finished, its response cannot be handed out again, and resending will neither
// produce it nor run the operation a second time. A route that returns bodies
// over the bound on purpose can answer that in its own words here — or raise
// [WithMaxBodyCache] so the receipt fits and an ordinary replay answers instead.
func WithReplayUnavailableHandler(fn fiber.Handler) Option {
	return func(m *Middleware) {
		m.onReplayUnavailable = fn
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
// bounded to twice this value. Values <= 0 are ignored.
//
// A response exceeding either bound is DELIVERED TO ITS CLIENT UNCHANGED —
// status, headers and body, exactly as the handler wrote them — and its key
// completes carrying no receipt. A size is not a fault: the handler ran, so
// answering it with a failure would report a store problem for a request that
// was actually served, and the client would resend on it.
//
// What the key loses is the replay, not the protection. A duplicate inside the
// retention window is refused with 409 [RefusalCodeReplayUnavailable], or the
// [WithReplayUnavailableHandler] document, and the handler never runs a second
// time; a duplicate carrying a DIFFERENT payload is still the ordinary key-reuse
// refusal. Size the bound for the largest response you mean to hand out twice.
//
// This holds whatever the status was. A 4xx over the bound is not treated as a
// release: whether a rejection may re-execute is [WithClientErrorPolicy]'s
// question, answered before the response is ever captured, so a route on
// [ClientErrorPolicyRelease] has already released and one on the default
// [ClientErrorPolicyCache] keeps its key here too. Otherwise the length of a
// validation report would decide whether a rejection path runs twice.
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

// chainRanKey marks, for this request only, that the rest of the chain ran. Its
// zero-size private type cannot collide with an application's own locals.
type chainRanKey struct{}

// runChain hands the request on and records that it did, which is the one fact
// retireUnreadRequestStream needs: from here the handler, and not this
// middleware, owns the request body.
func (m *Middleware) runChain(c fiber.Ctx) error {
	c.Locals(chainRanKey{}, true)

	return c.Next()
}

// fasthttpStreamPreRead is how much of a declared-length body fasthttp lifts out
// of the connection before it hands over a stream: readBodyWithStreaming copies
// min(bodyLimit, Content-Length, 8 KiB) into the request buffer and then creates
// the stream whatever it copied, so a body at or under that bound reports as a
// stream with an empty reader behind it. The number is fasthttp's, not this
// package's, and no exported symbol carries it —
// TestRefusal_StreamedBodyBuffered_KeepsTheConnection walks the boundary over a
// real socket so a change to it fails loudly here instead of silently letting
// the next request be parsed from a remainder.
const fasthttpStreamPreRead = 8 << 10

// retireUnreadRequestStream ends the connection when this middleware answered a
// request whose body is still sitting unread in that connection.
//
// With a [WithFingerprintProvider] nothing here calls c.Body(), which is the
// whole point of the option: the handler owns the upload. But a refusal and a
// replay both answer WITHOUT running the handler, so on those nobody read the
// upload. fasthttp recycles the stream struct without draining the reader, so
// the next request on a keep-alive connection is parsed from the middle of this
// one's body and the connection is reset — the client retrying a large upload
// under the same key gets its replay and loses the pooled connection under it.
//
// Closing is what net/http does with an unread body and what a client's pool
// understands. Draining instead would mean reading up to the route's body limit,
// a gigabyte on the upload routes this option exists for, to answer a 409.
//
// The rule is the three checks below. It fires whenever this middleware answered
// WITHOUT running the handler and the connection still holds part of the body. A
// replay under a [WithFingerprintProvider] is one such answer; so is any refusal
// that returns before resolveFingerprint — an over-length key, a missing required
// key, a missing required tenant, a key provider that failed, an absent store —
// because the deferred call is registered above all of them and resolveFingerprint
// is the only site that calls c.Body(). A refusal with NO provider configured
// therefore retires the connection too, and correctly: nothing read that upload
// either.
//
// It stays silent on the three cases that leave nothing behind: the handler ran
// and owns the body, c.Body() already drained the stream, or fasthttp had
// already lifted the whole declared body out of the connection before the chain
// started (see [fasthttpStreamPreRead]) — a stream by fasthttp's accounting, but
// an empty one, and retiring there charges a pooled client a fresh handshake per
// duplicate while protecting nothing. A chunked body (Content-Length -1) is
// never that case: none of it is pre-read.
func (m *Middleware) retireUnreadRequestStream(c fiber.Ctx) {
	if ran, _ := c.Locals(chainRanKey{}).(bool); ran {
		return
	}

	if !c.Request().IsBodyStream() {
		return
	}

	contentLength := c.Request().Header.ContentLength()
	if contentLength >= 0 && contentLength <= fasthttpStreamPreRead &&
		contentLength <= c.App().Config().BodyLimit {
		return
	}

	c.Response().Header.SetConnectionClose()
}

// onStoreError decides how to respond to a transient store error. Callers must
// have already logged the underlying error.
func (m *Middleware) onStoreError(c fiber.Ctx) error {
	if !m.failClosed {
		return m.runChain(c)
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

// respondReplayUnavailable answers a duplicate whose key holds a COMPLETED
// operation whose response was never stored, because it exceeded maxBodyCache.
//
// This is the only refusal in the package that reports a KNOWN outcome. The
// original request's handler ran to completion and its response went to its
// client unchanged — the middleware simply has no copy to hand out again. So
// the document says "completed, not replayable" and not "reconcile": there is
// nothing ambiguous to reconcile, and telling an operator to go and find out
// would send them after an outcome that is already settled.
//
// What it must never say is which WAY it completed. The same branch answers an
// over-cap success and an over-cap rejection held by [ClientErrorPolicyCache],
// and "already completed successfully" would book a mutation the route itself
// refused. "Ran, and its answer was delivered" is true of both.
//
// It answers 409, unlike the other terminal refusals' 422. 422 says the request
// itself cannot be acted on — a spent key used for a different payload, a record
// nobody can read. Here the request is perfectly valid and would be answered
// from the store if a receipt existed; what conflicts is its arrival after an
// identical one already completed. It carries NO Retry-After for the same
// reason [respondOutcomeUnknown] carries none: no amount of waiting inside the
// retention window produces the missing body.
//
// It does not set [constants.IdempotencyReplayed]: nothing was replayed, and
// claiming otherwise would tell the client it just received the original
// response. It never falls through to [WithPostHandlerUnavailableHandler],
// whose instruction is the one this case must not give.
func (m *Middleware) respondReplayUnavailable(c fiber.Ctx) error {
	if m.onReplayUnavailable != nil {
		return m.onReplayUnavailable(c)
	}

	return libHTTP.RespondError(c, http.StatusConflict,
		RefusalCodeReplayUnavailable,
		"the request with this idempotency key already ran and its response was delivered, but that response "+
			"was too large to store for replay; resending under this key will not execute the operation again "+
			"and will not reproduce that response",
	)
}

func (m *Middleware) handle(c fiber.Ctx) error {
	// Answering instead of the handler leaves a streamed request body unread;
	// see retireUnreadRequestStream for what that costs the connection.
	defer m.retireUnreadRequestStream(c)

	// Idempotency only applies to mutating methods.
	switch c.Method() {
	case fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete:
		// Apply idempotency to mutating methods only.
	default:
		return m.runChain(c)
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

		return m.runChain(c)
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
	tenantID := m.resolveTenant(c)
	if tenantID == "" {
		if m.requireTenant {
			return m.respondTenantRequired(c)
		}

		// No tenant context — bypass idempotency to avoid collapsing all
		// tenant-less requests onto a shared key, which breaks isolation.
		// This is consistent with the middleware's fail-open philosophy.
		return m.runChain(c)
	}

	// Kept for the log lines below, so they report the tenant the record is
	// rooted at without asking the provider, or the context, a second time.
	c.Locals(recordTenantKey{}, tenantID)

	key := fmt.Sprintf("%s%s:%s", m.keyPrefix, tenantID, idempotencyKey)
	if nilcheck.Interface(m.store) {
		m.logger.Log(c.Context(), obs.LevelWarn, "idempotency: store unavailable")

		return m.onStoreError(c)
	}

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

	// Resolved here for the same reason the TTL above is: a
	// [WithProcessingTTLProvider] reading runtime configuration is the
	// application's own I/O and must not be charged to the store's budget.
	// Unlike the retention, an unresolvable lease never refuses the request;
	// see [WithProcessingTTLProvider].
	lease := m.resolveProcessingTTL(c)

	// The deadline opens HERE, after the application's providers have run and
	// not before them. [WithRedisTimeout] is a budget for the store, and a
	// [WithFingerprintProvider] that walks a multipart request or reads an
	// upload manifest is the application's own I/O: charging it to the store
	// left a slow provider's first store call timing out against a healthy
	// store, and under the fail-open default the mutation then ran with no key
	// held at all.
	ctx, cancel := context.WithTimeout(c.Context(), m.redisTimeout)
	defer cancel()

	return m.handleStore(ctx, c, key, fingerprint, ttl, lease)
}

// resolveKey reads the idempotency key for this request. Without a provider it
// is the X-Idempotency header, which is the shipped behaviour.
func (m *Middleware) resolveKey(c fiber.Ctx) (string, error) {
	if m.keyProvider != nil {
		return m.keyProvider(c)
	}

	return c.Get(chttp.IdempotencyKey), nil
}

// recordTenantKey carries the tenant resolved for this request, for the log
// lines that name it.
type recordTenantKey struct{}

// recordTenant is the tenant this request's record is rooted at.
func recordTenant(c fiber.Ctx) string {
	tenantID, _ := c.Locals(recordTenantKey{}).(string)

	return tenantID
}

// resolveTenant reads the tenant this request's record is rooted at. Without a
// provider it is the tenant-manager context, which is the shipped behaviour. A
// provider error is the absent tenant; see [WithTenantProvider].
func (m *Middleware) resolveTenant(c fiber.Ctx) string {
	if m.tenantProvider == nil {
		return tmcore.GetTenantIDContext(c.Context())
	}

	tenantID, err := m.tenantProvider(c)
	if err != nil {
		m.logger.Log(c.Context(), obs.LevelWarn,
			"idempotency: tenant provider failed; treating the request as tenant-less", "error", err)

		return ""
	}

	return tenantID
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

// resolveClientErrorPolicy is the single branch point for the 4xx policy: the
// per-response function when one is configured, the enum otherwise.
func (m *Middleware) resolveClientErrorPolicy(c fiber.Ctx, status int) ClientErrorPolicy {
	if m.clientErrorPolicyFunc != nil {
		return m.clientErrorPolicyFunc(c, status)
	}

	return m.clientErrorPolicy
}

// effectiveStatus is the status handed to the server policy seam. A handler
// that WROTE a response is reported by what it wrote. A handler that returned
// an error and wrote nothing leaves the response object at its default 200,
// which says nothing at all about the failure — so when that error is a
// *fiber.Error, its code is reported instead.
//
// That code is the status the application's Fiber error handler will MOST
// LIKELY write, not a status anything has written: the error handler has not
// run and may map, wrap or replace the code entirely. A consumer that needs
// certainty about the failure inspects err, which is the only thing here that
// is a fact rather than a forecast.
//
// It is only ever called once a server policy function is known to be
// configured, because deciding a forecast nobody asked for must not cost a look
// at the response.
func effectiveStatus(c fiber.Ctx, status int, err error) int {
	// "Wrote nothing" is the default status AND an empty body: a handler that
	// wrote a 200 document and then failed has written a response, and its own
	// status is the honest report.
	//
	// A STREAMED body counts as written WITHOUT being looked at, and the order
	// of these tests is the point. fasthttp's Response.Body() is not an
	// inspection when a body stream is set: it copies the entire stream into
	// memory and closes it. Measuring its length here would buffer an unbounded
	// response, past [WithMaxBodyCache], on a path that is about to discard it.
	if err == nil || status != http.StatusOK || c.Response().IsBodyStream() ||
		len(c.Response().Body()) > 0 {
		return status
	}

	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		return fiberErr.Code
	}

	return status
}

// resolveServerErrorPolicy is the single branch point for the handler-failure
// and 5xx policy: the per-response function when one is configured, the enum
// otherwise.
func (m *Middleware) resolveServerErrorPolicy(c fiber.Ctx, status int, err error) ServerErrorPolicy {
	// effectiveStatus is computed HERE and not at the call site: as an argument
	// it would be evaluated before this nil check, so a middleware with no
	// function installed would still pay for a forecast nothing consumes.
	if m.serverErrorPolicyFunc != nil {
		return m.serverErrorPolicyFunc(c, effectiveStatus(c, status, err), err)
	}

	return m.serverErrorPolicy
}

// resolveProcessingTTL is the single branch point for the in-flight lease: the
// per-request provider when one is configured, the constant otherwise. Called
// once per request, above the store deadline, so it takes the request context
// from c rather than the store-bounded one. A provider that cannot answer
// falls back to the constant rather than refusing the request; see
// [WithProcessingTTLProvider] for why this fails open where the retention
// provider fails closed, and for the obligation that fallback puts on an
// operator who configures both. A non-positive result reaches handleStore,
// which borrows the retention TTL for it exactly as an unset lease does.
func (m *Middleware) resolveProcessingTTL(c fiber.Ctx) time.Duration {
	if m.processingTTLProvider == nil {
		return m.processingTTL
	}

	lease, err := m.processingTTLProvider(c)
	if err != nil {
		m.logFallbackLease(c, "the processing TTL provider failed", err, 0)

		return m.processingTTL
	}

	// Logged as loudly as the error above, and for the same reason: both mean
	// the lease this request runs under is NOT the one the provider was
	// installed to supply, and a fallback shorter than the protected operation
	// is the mid-flight lapse that lets a redelivery execute it twice. A silent
	// non-positive return is the worse of the two, because nothing else reports
	// it at all.
	if lease <= 0 {
		m.logFallbackLease(c, "the processing TTL provider returned a non-positive lease", nil, lease)

		return m.processingTTL
	}

	return lease
}

// logFallbackLease reports a request running under the fallback lease rather
// than a resolved one. fallback_lease is what it fell back to; zero there means
// [WithProcessingTTL] is unset and the lease borrows the retention TTL, which
// resolveProcessingTTL cannot see from here.
func (m *Middleware) logFallbackLease(c fiber.Ctx, cause string, err error, returned time.Duration) {
	m.logger.Log(c.Context(), obs.LevelWarn,
		"idempotency: "+cause+"; falling back to the configured lease",
		"error", err,
		"provider_lease", returned,
		"fallback_lease", m.processingTTL,
		"tenant_id", recordTenant(c),
	)
}

func (m *Middleware) handleStore(
	ctx context.Context,
	c fiber.Ctx,
	key, fingerprint string,
	ttl, lease time.Duration,
) error {
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

	// ttl is the RETENTION window and stays with Complete below. lease was
	// resolved by the caller, above the store deadline, and takes the in-flight
	// lease here: it has to survive everything between this point and that
	// Complete — the handler, the response capture and encoding, and the store
	// round-trip. Nothing else holds the key for any of it.
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
				"tenant_id", recordTenant(c),
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
	switch current.Outcome {
	case outcomeUnrecorded:
		return m.respondOutcomeUnknown(c)
	case outcomeNotReplayable:
		// A real completion with no receipt: the original response was over the
		// body-cache bound and was delivered without being stored.
		return m.respondReplayUnavailable(c)
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
	// Taken BEFORE the handler so the capture can be reduced to the handler's
	// own contribution; see captureHeaderDelta for why the whole response is the
	// wrong thing to store.
	beforeHandler := snapshotResponseHeaders(c)

	handlerErr := m.runChain(c)

	postCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), m.redisTimeout)
	defer cancel()

	statusCode := c.Response().StatusCode()
	if handlerErr != nil || statusCode >= http.StatusInternalServerError {
		// Releasing here frees the key BEFORE the application's Fiber error
		// handler has even seen handlerErr, so a route that rewrites this into
		// "executed, receipt lost" is rewriting a refusal whose key is already
		// gone. Fencing skips the release entirely rather than trying to order
		// it after a seam the middleware does not own.
		if m.resolveServerErrorPolicy(c, statusCode, handlerErr) == ServerErrorPolicyFence {
			// The result is deliberately not turned into a document here. This
			// branch must return handlerErr so the application's error handler
			// runs and owns the response; authoring one would take that over.
			// The caller still learns the outcome: markOutcomeUnknown sets the
			// [constants.IdempotencyFenced] header either way, and a failed
			// fence logs at ERROR naming the key.
			m.markOutcomeUnknown(c, key, processing, record, ttl)

			return handlerErr
		}

		m.releaseOwned(postCtx, key, processing, "store release")

		return handlerErr
	}

	if statusCode >= http.StatusBadRequest && m.resolveClientErrorPolicy(c, statusCode) == ClientErrorPolicyRelease {
		m.releaseOwned(postCtx, key, processing, "client-error cleanup")

		return handlerErr
	}

	response, err := m.captureResponse(postCtx, c, key, beforeHandler)

	switch {
	// A response over the bound is not a fault. The handler ran, the client is
	// owed that response exactly as written — so this path neither authors a
	// document of its own nor fences the key. It COMPLETES the record, marked as
	// carrying no replayable receipt, which is what stops a resend from
	// executing the handler a second time once the in-flight lease lapses. The
	// alternative shipped before this was strictly worse: a committed mutation
	// was reported to its client as a 503 store failure.
	//
	// The status decides nothing here, and that is the point. Whether a 4xx may
	// re-execute is [ClientErrorPolicyCache] versus [ClientErrorPolicyRelease],
	// a question only the route owner can answer, and one it answers BEFORE this
	// switch — a route on the release policy has already released and returned.
	// Reaching for the release here would apply that policy to a route that
	// declined it, so a validation report one byte over the bound would
	// re-execute a rejection path the owner asked to have cached, while the
	// shorter report would not. A body size must not decide it.
	case errors.Is(err, errResponseTooLarge):
		// Named the same way as the fence logs, and for the same reason: this
		// branch makes the key permanently unreplayable for its whole retention,
		// so every resend under it is refused 409. An operator asked why has to
		// find the one record, and "a response was too large" with no key is not
		// an answer. The key travels as a digest; see logFenceFailure.
		m.logger.Log(postCtx, obs.LevelWarn,
			"idempotency: replay response exceeds the configured limit; completing the key without a replayable receipt",
			"error", err,
			"status_code", statusCode,
			"idempotency_key_digest", keyDigest(key),
			"tenant_id", recordTenant(c),
		)

		response = nil
		record.Outcome = outcomeNotReplayable
	case err != nil:
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

// releaseOwned compare-safely frees the key this request holds, so the next
// request under it runs. cause names the branch that asked for the release, and
// is the only thing that differs between the two ways it can go wrong: the
// store refusing the call, and the call landing on a key this request no longer
// owns.
func (m *Middleware) releaseOwned(ctx context.Context, key string, processing []byte, cause string) {
	applied, err := m.store.Release(ctx, key, processing)
	if err != nil {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: "+cause+" failed", "error", err)
	} else if !applied {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: "+cause+" rejected stale owner")
	}
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
		m.logFenceFailure(ctx, recordTenant(c), key, record.Owner, "failed to marshal the fenced record", err)

		return false
	}

	applied, err := m.store.Complete(ctx, key, processing, unknown, ttl)
	if err != nil {
		m.logFenceFailure(ctx, recordTenant(c), key, record.Owner, "the store rejected the fence write", err)

		return false
	}

	if !applied {
		m.logFenceFailure(ctx, recordTenant(c), key, record.Owner, "the fence write found a stale owner", nil)

		return false
	}

	fenced = true

	m.logger.Log(ctx, obs.LevelWarn, "idempotency: key fenced with an unrecorded outcome",
		"record_outcome", outcomeUnrecorded,
		"idempotency_key_digest", keyDigest(key),
		"tenant_id", recordTenant(c),
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
func (m *Middleware) logFenceFailure(ctx context.Context, tenantID, key, owner, cause string, err error) {
	m.logger.Log(ctx, obs.LevelError,
		"idempotency: key left UNFENCED after an unrecorded outcome; a resend may execute the operation again — "+cause,
		"idempotency_key_digest", keyDigest(key),
		"tenant_id", tenantID,
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

// captureResponse encodes what the replay will re-send. key is carried for the
// log line alone: the size branch below is the one place this function can end a
// key's replayability, and the caller's companion WARN names the same record.
func (m *Middleware) captureResponse(ctx context.Context, c fiber.Ctx, key string, beforeHandler headerSnapshot) ([]byte, error) {
	body := c.Response().Body()
	if len(body) > m.maxBodyCache {
		m.logger.Log(c.Context(), obs.LevelWarn,
			"idempotency: response body exceeds maxBodyCache, skipping cache",
			"body_size", len(body),
			"max_body_cache", m.maxBodyCache,
			"idempotency_key_digest", keyDigest(key),
			"tenant_id", recordTenant(c),
		)

		return nil, errResponseTooLarge
	}

	response := cachedResponse{
		StatusCode:  c.Response().StatusCode(),
		ContentType: string(c.Response().Header.ContentType()),
		Body:        append([]byte(nil), body...),
		Headers:     captureHeaderDelta(c, beforeHandler),
	}

	plaintext, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("marshal replay response: %w", err)
	}

	encoded, err := m.responseCodec.Encode(ctx, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encode replay response: %w", err)
	}

	// Two different conditions, deliberately two different errors. An empty
	// encoding is a MALFUNCTION of the codec — nothing about it is a size, and
	// folding it into the bound would complete every response on the route as
	// non-replayable while the log names a cap the operator can raise forever
	// without moving the symptom. It keeps the loud post-handler failure path.
	if len(encoded) == 0 {
		return nil, errEmptyEncodedResponse
	}

	if len(encoded) > m.maxEncodedResponseBytes() {
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

// headerSnapshot is the response header state immediately before the handler
// runs: whatever every middleware mounted above has already written.
//
// Set-Cookie is held apart and keyed by COOKIE name, because that is its unit
// of identity. Two Set-Cookie values are not two values of one header, they are
// two cookies, and rotating a session is a change to exactly one of them.
type headerSnapshot struct {
	headers map[string][]string
	cookies map[string]string
}

// isUncapturedHeader names what the capture never stores, on either side of the
// handler: the content type travels as its own field, and the other three
// describe the transfer of one particular response rather than its content.
//
// The snapshot honours the same list, so a name excluded from the post-handler
// walk can never look DELETED to the delta below.
func isUncapturedHeader(name string) bool {
	switch name {
	case "Content-Type", "Content-Length", "Transfer-Encoding", chttp.IdempotencyReplayed:
		return true
	default:
		return false
	}
}

func snapshotResponseHeaders(c fiber.Ctx) headerSnapshot {
	snapshot := headerSnapshot{
		headers: make(map[string][]string),
		cookies: make(map[string]string),
	}

	for hdrKey, value := range c.Response().Header.All() {
		name := string(hdrKey)
		if isUncapturedHeader(name) {
			continue
		}

		if name == fiber.HeaderSetCookie {
			if cookieName, ok := capturedCookieName(string(value)); ok {
				snapshot.cookies[cookieName] = string(value)
			}

			continue
		}

		snapshot.headers[name] = append(snapshot.headers[name], string(value))
	}

	return snapshot
}

// captureHeaderDelta keeps only the HANDLER's contribution to the response: the
// names whose value list differs from the snapshot taken before it ran, and the
// cookies it added or changed.
//
// Capturing the whole response instead stores a PER-REQUEST value minted above
// the middleware — a correlation id, a rotated session, a fresh CSRF token —
// and the replay, which replaces every name it holds, then hands the duplicate
// a value belonging to a different request. A stale CSRF token is worse than
// cosmetic: the user's next mutation is refused.
//
// The split is therefore by authorship, not by header name. What the handler
// set IS the receipt and replaces whatever is live on the replay, including a
// value it deliberately overrode (a helmet Cache-Control the handler turns into
// no-store is captured, because that is what the original response carried).
// Everything else on the replayed response belongs to this request.
func captureHeaderDelta(c fiber.Ctx, beforeHandler headerSnapshot) map[string][]string {
	live := make(map[string][]string)

	var cookies []string

	for hdrKey, value := range c.Response().Header.All() {
		name := string(hdrKey)
		if isUncapturedHeader(name) {
			continue
		}

		if name == fiber.HeaderSetCookie {
			// A value that is not shaped like a cookie has no identity to
			// compare, so it is treated as the handler's: captured and replayed.
			if cookieName, ok := capturedCookieName(string(value)); !ok || beforeHandler.cookies[cookieName] != string(value) {
				cookies = append(cookies, string(value))
			}

			continue
		}

		live[name] = append(live[name], string(value))
	}

	delta := make(map[string][]string)

	for name, values := range live {
		if !slices.Equal(values, beforeHandler.headers[name]) {
			delta[name] = values
		}
	}

	// Removing a name is a contribution too, and the loop above cannot see it:
	// a header the handler DELETED is absent from live, so nothing compares it
	// against the snapshot. Left unrecorded, the replay never clears it and a
	// duplicate receives a header the original response did not carry — helmet's
	// X-Frame-Options back on a receipt the handler deliberately made
	// embeddable, a Cache-Control the handler stripped. An empty value list is
	// the record of that: the replay clears the name and re-applies nothing.
	//
	// Cookie removals are deliberately NOT tracked. Set-Cookie is identified by
	// cookie name rather than header name, so a removal would have to store a
	// name with no value to re-apply, and every value in this map is also an Add
	// on replay. A handler that deletes a cookie another middleware minted keeps
	// the live one on the duplicate; doc.go says so.
	for name := range beforeHandler.headers {
		if _, stillLive := live[name]; !stillLive {
			delta[name] = nil
		}
	}

	if len(cookies) > 0 {
		delta[fiber.HeaderSetCookie] = cookies
	}

	return delta
}

// capturedCookieName reports the cookie name a Set-Cookie value carries, and
// whether the value is shaped like one at all.
func capturedCookieName(value string) (string, bool) {
	name, _, found := strings.Cut(value, "=")

	return strings.TrimSpace(name), found
}

// clearCapturedHeader removes what the capture is about to re-apply, and only
// that.
//
// Set-Cookie is the one captured name whose unit of identity is not the header
// name: fasthttp's ResponseHeader.Del("Set-Cookie") empties the WHOLE cookie
// jar, so clearing it that way also discards a cookie some other middleware
// minted on this request — a rotated session, a fresh CSRF token — and hands the
// client the captured one instead, whose next mutation the CSRF check then
// refuses. DelCookie removes only the cookie name the capture is replacing.
func clearCapturedHeader(c fiber.Ctx, name string, values []string) {
	if name != fiber.HeaderSetCookie {
		c.Response().Header.Del(name)

		return
	}

	for _, value := range values {
		if cookieName, ok := capturedCookieName(value); ok {
			c.Response().Header.DelCookie(cookieName)
		}
	}
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
		// The clearing is scoped to what the capture owns: a header the app set
		// on this request that the capture does not hold is left alone, and a
		// captured header with several values (two Set-Cookie, two Link) is
		// re-applied whole, in order.
		clearCapturedHeader(c, name, values)

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
