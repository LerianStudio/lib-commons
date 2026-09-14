// Package idempotency provides Fiber middleware for atomic, tenant-scoped
// idempotency backed by Redis or a caller-provided [Store].
//
// [New] preserves the shipped go-redis API and its fail-open default. Callers
// that require strict storage availability can still use [WithFailClosed].
// [NewWithStore] is always fail-closed: a missing store, store error, or invalid
// stored record rejects the mutation with 503 before its handler runs.
//
// # Key composition
//
// The middleware uses the X-Idempotency request header ([constants.IdempotencyKey])
// combined with the tenant ID (from tenant-manager context via
// [tmcore.GetTenantIDContext]) to form a composite Redis key:
//
//	<prefix><tenantID>:<idempotencyKey>
//
// Keys are scoped per-tenant to prevent cross-tenant collisions. When no tenant
// is in context, idempotency is BYPASSED entirely rather than falling back to a
// global namespace, which would collapse every tenant-less request onto a shared
// key and break isolation. [WithRequireTenant] refuses such a request instead of
// bypassing; it is never keyed either way. The tenant check sits AFTER the
// header check, so it only ever sees a KEYED request: an unkeyed one has already
// taken the branch above. Pair it with [WithRequireKey] to refuse both.
//
// The middleware encodes state, fingerprint, acquisition owner, and optional
// replay response into one opaque value stored atomically under that key. Store
// implementations preserve those bytes and provide only atomic acquisition and
// compare-safe replacement or deletion; they do not implement HTTP semantics.
//
// # Payload fingerprint
//
// An idempotency key alone does not identify a request — it identifies the
// caller's claim that two requests are the same one. Every processing and
// completed record therefore carries a SHA-256 fingerprint of method, path, and
// raw body. [WithFingerprintScopeProvider] can opt into an additional
// application-defined scope without changing the storage key. Scoped
// fingerprints use a versioned domain plus the scope's byte length and bytes;
// even an empty scope remains distinct from the legacy unscoped fingerprint.
//
// Every duplicate compares its own fingerprint against the stored one before any
// replay path is reachable. A match is a genuine retry and replays. A MISMATCH
// means the key was spent on a different request, and returns 422 with code
// "IDEMPOTENCY_KEY_REUSE" — because replaying there hands the caller another
// request's response: its own operation never ran, and it is told the operation
// succeeded. Nothing retries, since the status said success, and the absence is
// only discoverable at reconciliation.
//
// The digest covers the raw body bytes as received, never a re-serialization, so
// JSON key order and formatting cannot drift between a request and its own retry.
// The query string is excluded: clients append cache-busting parameters on retry,
// and that must not read as reuse.
//
// The default prefix is "idempotency:" and can be overridden via [WithKeyPrefix].
// This namespacing convention is consistent with other lib-commons packages that
// use Redis (e.g., rate limiting uses "ratelimit:<tenantID>:..."). Per-tenant
// isolation is enforced by embedding the tenant ID into the key rather than
// using separate Redis databases or key-space notifications, which keeps the
// implementation topology-agnostic (standalone, sentinel, and cluster all behave
// identically with this approach).
//
// # Quick start
//
//	conn, err := redis.New(ctx, cfg)
//	if err != nil {
//	    return err
//	}
//	idem := idempotency.New(conn)
//	app.Post("/orders", idem.Check(), createOrderHandler)
//
// A caller-provided backend implements Store and uses the fail-closed
// constructor:
//
//	idem := idempotency.NewWithStore(valkeyStore)
//	app.Post("/orders", idem.Check(), createOrderHandler)
//
// # Behavior branches
//
// The [Middleware.Check] handler evaluates requests through the following
// branches in order:
//
//   - GET, HEAD, and OPTIONS requests pass through unconditionally — idempotency
//     is not enforced for safe/idempotent HTTP methods.
//   - Absent X-Idempotency header: request proceeds normally (idempotency is
//     opt-in per request), unless [WithRequireKey] is set, in which case the
//     request is refused with 400 "IDEMPOTENCY_KEY_REQUIRED" before its handler
//     runs.
//   - Header exceeds [WithMaxKeyLength] (default 256 UTF-8 bytes): request is
//     passed to the configured [WithRejectedHandler]. When no custom handler is
//     set, a 400 JSON response with code "VALIDATION_ERROR" is returned.
//   - The built-in Redis store unavailable: request proceeds without idempotency
//     enforcement by default, or receives 503 with [WithFailClosed].
//   - A caller-provided store missing or errored: request receives 503 and the
//     mutation handler does not run.
//   - An EXISTING record whose state this version does not recognise: refused
//     with 422 "IDEMPOTENCY_STATE_UNRECOGNISED" — or the
//     [WithPostHandlerUnavailableHandler] document — REGARDLESS of
//     [WithFailClosed], and logged at ERROR naming the state. Nothing is
//     written, so the record survives for a reader that understands it. This is
//     the only branch where the fail-open default is overruled, and the reason
//     is in "Mixed versions during a rolling upgrade" below.
//   - Duplicate key whose stored fingerprint differs from this request's: request
//     is passed to [WithKeyReuseHandler], or receives 422 Unprocessable Content
//     with code "IDEMPOTENCY_KEY_REUSE" when no custom handler is configured.
//     Checked before every replay path below, so no branch can answer a different
//     payload with another request's result.
//   - Duplicate key with matching fingerprint and a cached response: the original
//     response is replayed faithfully — status code, headers (including Location,
//     ETag, Set-Cookie), content type, and body — with
//     [constants.IdempotencyReplayed] set to "true".
//   - Duplicate key still in "processing" state (in-flight): request is passed
//     to [WithConflictHandler], or receives 409 Conflict with code
//     "IDEMPOTENCY_CONFLICT" and Retry-After: 1 when no custom handler is configured.
//   - Duplicate key holding a canonical JSON record in "complete" state without
//     an exact replay response, or a response that [ResponseCodec] cannot decode:
//     503 "IDEMPOTENCY_UNAVAILABLE" is returned. For canonical records the
//     middleware never fabricates a generic success response.
//   - Exception to the rule above: a duplicate key holding the plain-text record
//     written by lib-commons v6.4.0
//     and earlier ("processing:<fingerprint>" / "complete:<fingerprint>"): treated
//     as an EXISTING record, never an absent one, so a legitimate retry is never
//     executed a second time during the rolling deploy that crosses the format
//     change. The same fingerprint gate applies; a matching "processing" record
//     returns 409, and a matching "complete" record returns 200 "IDEMPOTENT"
//     because v6.4.0 kept the response body in a sidecar key that [Store] cannot
//     read. Any other undecodable value keeps the store-error path above. This
//     branch is bounded and removable: no new legacy records are written and
//     existing ones expire with their TTL.
//   - Duplicate key whose record is marked with an unrecorded outcome: request
//     receives 422 Unprocessable Content with code
//     "IDEMPOTENCY_OUTCOME_UNRECORDED", or the
//     [WithPostHandlerUnavailableHandler] document when that seam is set. The
//     mark is checked before the state routing below, and no captured body is
//     replayed because none was ever stored.
//   - Handler success: response status, headers, content type, and body are
//     compare-safely completed only by the acquisition owner. Capture, encoding,
//     persistence, or stale-owner failures return 503, and the key is marked
//     terminal (see below) so callers reconcile instead of retrying under a new
//     key. That 503 is the one branch [WithPostHandlerUnavailableHandler]
//     answers, because the mutation already happened — but ONLY when the mark
//     was actually persisted. When it was not, the answer is 503
//     "IDEMPOTENCY_UNFENCED" instead, which says the key is unprotected and a
//     resend may execute the operation again.
//   - Handler 4xx: cached and replayed by default. Use
//     [WithClientErrorPolicy] with [ClientErrorPolicyRelease] to compare-safely
//     release the record and allow a corrected request to reuse the key.
//   - Handler failure or 5xx: the acquisition is compare-safely released only
//     by its owner, allowing a retry without deleting a replacement lock. Use
//     [WithServerErrorPolicy] with [ServerErrorPolicyFence] on routes where a
//     failure there may still have committed, so the key is fenced instead.
//
// # Lease and retention are two lifetimes
//
// The record under a key has two jobs with different clocks. From acquisition
// until the completed record is stored it is an in-flight LEASE, and it must
// cover that WHOLE span: the handler, then the response capture, serialization
// and [WithResponseCodec] encoding, and finally the Store.Complete round-trip.
// Nothing but the lease holds the key through any of it, so a lease sized to
// the handler alone can lapse in the tail after the handler already returned
// and the mutation already committed.
//
// Wherever in that span it lapses, the damage is identical: a redelivery under
// the same key acquires the key again and the mutation runs a SECOND time,
// while the original request's completion is rejected because the record it
// owned is gone. Once the completed record IS stored, the same key becomes a
// RETENTION window — how long the client may still replay the receipt — and
// that is a client-facing policy, not a budget for the work.
//
// [WithKeyTTL] and [WithTTLProvider] set the retention. [WithProcessingTTL] sets
// the lease independently; unset, the lease borrows the retention, which is the
// shipped behaviour. Set them apart whenever a caller wants a short replay
// window on a route whose protected operation can run longer than it, so the
// retention choice never silently caps that work. Size the lease against the
// slowest handler PLUS capture, encoding and the store round-trip, with
// headroom:
//
//	idem := idempotency.New(conn,
//	    idempotency.WithKeyTTL(5*time.Minute), // how long a client may replay
//	    // how long the handler AND its completion may take, with margin
//	    idempotency.WithProcessingTTL(30*time.Minute),
//	)
//
// [WithTTLProvider] resolves retention for each keyed mutation, allowing runtime
// policy changes without rebuilding middleware. [WithFingerprintScopeProvider]
// resolves a concurrency-safe application namespace for fingerprint comparison;
// callers that omit it retain byte-identical legacy fingerprints and Redis keys.
// [WithResponseCodec] transforms serialized replay responses before storage; use
// authenticated encryption for sensitive bodies. [WithMaxBodyCache] bounds raw
// response bodies, and encoded output is additionally bounded to twice that
// value.
//
// # A key whose outcome was never recorded
//
// Two branches used to leave a key FREE while its handler had already run, or
// might still be running. A completion that fails after the handler returned
// leaves nothing but the processing record, which lapses with the in-flight
// lease. A handler failure or 5xx releases the record outright. Either way a
// resend under the same key acquires it again and executes the mutation a
// SECOND time, and the only thing between the two executions is the
// instruction in a refusal body that no client is obliged to read.
//
// The record under the key is now MARKED terminal instead: the request that
// spent it left no recorded outcome, and the key says so for the RETENTION TTL
// rather than for the lease. The mark is a field ("outcome"), and the state
// field keeps "complete" — see "Mixed versions during a rolling upgrade" below
// for why that is load-bearing rather than cosmetic. A duplicate inside that
// window is refused by the key.
// Because the refusal is terminal it answers 422, not the 409 that carries
// Retry-After and invites a retry the state can never satisfy, and not 503,
// which says the store is unavailable when it is the key that is spent. It
// answers with a refusal document and never replays a captured success body,
// because none was ever stored: capture or persistence is exactly what failed,
// so replaying anything here would report an outcome nobody recorded.
//
// # Mixed versions during a rolling upgrade
//
// The terminal mark is a FIELD on the stored record, not a third state value,
// and that choice is load-bearing on a money path. During a rolling upgrade both
// versions share one store, so a record this version writes is read by one that
// predates it. A third STATE value is unknown to that reader, and the
// unknown-state branch ends at the fail-open default, which calls the handler:
// the duplicate would execute while the fence sat in the store unread. Measured
// against the pre-change reader, a third state answered 201 and ran the handler
// a second time.
//
// So the fenced record keeps "complete" in its state field, which a pre-change
// reader resolves as a completed record holding no replay response — a case it
// already refuses through [WithPostHandlerUnavailableHandler], with the same
// instruction this version gives. Readers that know the field route on it
// first. Both upgrade directions are therefore safe and no upgrade ordering is
// required, including a rollback: a record written by this version is refused,
// not executed, by the version before it.
//
// The cost is that the state field of a fenced record is a compatibility
// encoding rather than the plain truth, and anything reading these records
// outside this package must read the outcome field to tell a real completion
// from a fence.
//
// The same problem points FORWARD, and is closed the same way. This encoding
// protects a future reader from what this version writes; nothing in it
// protects THIS reader from what a future version writes. So an existing record
// whose state this version does not recognise is refused outright, regardless
// of [WithFailClosed] — see the branch list above. Fail-open is right when the
// middleware learned nothing; it is wrong once the middleware knows the key
// holds somebody's record, because proceeding there is not running unprotected,
// it is running on top of an outcome sitting in the store. Without that arm the
// next state value anyone adds would reintroduce this same double execution on
// the older half of the next rolling upgrade.
//
// One rollout wrinkle, and its remedy: mid-rollout the same key answers 422
// from an upgraded pod and 503 from one that is not. Neither executes, so this
// is a consistency wrinkle and not a safety one, and it disappears entirely for
// a service that wires [WithPostHandlerUnavailableHandler] — then both pods
// answer that service's own document. Wire the seam before rolling out if a
// uniform answer during the rollout matters; a client with a blanket
// retry-on-503 policy would otherwise retry and then meet a terminal 422.
//
// The completion-failure branch is unconditional — no route wants a key it has
// already spent to come back. The handler-failure branch is opt-in through
// [WithServerErrorPolicy], because releasing there is right for a route whose
// 5xx are refusals and wrong for one whose handler can commit before its
// response is written; only the route's owner knows which it is. At that point
// a handler error has not even reached the application's Fiber error handler,
// so the status the caller will finally see does not exist yet and the
// middleware cannot infer the meaning for itself.
//
// The fence is BEST-EFFORT by construction and the limit is nameable: it writes
// through Store.Complete to the same store whose failure brought it there. It
// therefore closes a transient failure — a timeout, a dropped connection, a
// failover — and not a total store outage, during which nothing durable can be
// written under the key at all and the key still lapses with its lease. The
// compare-and-set is not incidental either: a write lands only while the
// request still owns the key, so a stale owner leaves the current owner's
// record untouched instead of stamping a fence over it.
//
// # Saying whether the fence landed
//
// Best-effort is acceptable. Being unable to tell which effort failed is not:
// "reconcile the original request" answered for both outcomes hides whether a
// resend under this key would be refused or would arm the operation a second
// time, and no client can be asked to guess that. So the middleware says so,
// three ways:
//
//   - [constants.IdempotencyFenced] is set on every response where a fence was
//     attempted, "true" or "false". It is a header rather than a body field
//     because the handler-failure branch writes no body of its own — the
//     application's error handler owns that response — and a header reaches the
//     client through it either way.
//   - A completion failure whose fence did NOT land answers 503
//     "IDEMPOTENCY_UNFENCED" instead of the ordinary post-handler 503, and it
//     does not route through [WithPostHandlerUnavailableHandler]: a service
//     wired that seam for the fenced case, and reusing it here would put the
//     indistinguishability straight back. Neither answer carries Retry-After.
//   - A failed fence logs at ERROR naming the key, so the alert is actionable.
//     The key appears as a SHA-256 digest of the store key, never raw, because
//     an idempotency key is client-supplied and services put business
//     references in it; the digest is reproducible from the key the client
//     holds. The tenant ID and the acquisition owner are logged as they are.
//
// The retention TTL is therefore also an operational choice: it is how long an
// operator or a reconciliation job has before a fenced key frees itself.
//
// # Requiring a key or a tenant
//
// Both bypasses above — an absent header and an absent tenant — let a mutation
// run with no at-most-once protection at all. That is the right default for an
// opt-in header on a general-purpose API, and it is what every existing caller
// gets. It is the wrong default for a route where an unkeyed retry duplicates an
// irreversible side effect, such as a money movement: there the request should be
// refused, not silently unprotected.
//
// [WithRequireKey] and [WithRequireTenant] turn each bypass into a refusal, per
// middleware instance, so a caller can mount a strict middleware on money routes
// and the permissive default elsewhere. Both are off by default and neither
// changes any other branch. Each refuses with 400 before the handler runs, coded
// "IDEMPOTENCY_KEY_REQUIRED" and "IDEMPOTENCY_TENANT_REQUIRED", and each has its
// own callback seam:
//
//	idem := idempotency.New(conn,
//	    idempotency.WithRequireKey(),
//	    idempotency.WithRequireTenant(),
//	    idempotency.WithFailClosed(true),
//	)
//	app.Post("/transactions", idem.Check(), createTransactionHandler)
//
// The two are ORDERED, not independent guarantees: the header check runs first,
// so [WithRequireTenant] on its own never sees an unkeyed request and lets it
// through. Enabling only [WithRequireTenant] therefore does NOT mean "every
// tenant-less mutation is refused" — an unkeyed one still passes. A route that
// must never run a mutation without both takes both options, as above.
//
// Neither option survives a nil middleware. [New] returns nil for a nil
// connection and [Middleware.Check] on a nil receiver is an unconditional
// pass-through, so options are never applied and every request proceeds. A
// caller that cannot tolerate that must verify [New] returned non-nil, or use
// [NewWithStore], which returns a usable middleware even for a nil store.
//
// Every rejection branch has a callback seam so consumers can write their own
// error format, including RFC 9457 problem details: [WithRejectedHandler] for an
// oversized key, [WithKeyRequiredHandler] for a missing key under
// [WithRequireKey], [WithTenantRequiredHandler] for a missing tenant under
// [WithRequireTenant], [WithUnavailableHandler] for fail-closed store failures
// observed BEFORE the handler runs, [WithPostHandlerUnavailableHandler] for the
// ones observed AFTER it ran and for a duplicate that finds a record marked
// terminal or written in an unrecognised state, [WithConflictHandler] for an
// in-flight duplicate, and [WithKeyReuseHandler] for the same key used by a
// different request. The one refusal with no seam is 503
// "IDEMPOTENCY_UNFENCED": it exists precisely to be distinguishable from the
// seam a service already wired.
//
// The last two 503s are one status carrying opposite instructions, which is why
// they have separate seams. Before the handler, nothing ran and the caller
// should retry the same request. After it, the side effect is committed and a
// retry under a new key duplicates it; the caller must reconcile instead. The
// built-in bodies already say different things; an override of
// [WithUnavailableHandler] alone would answer both with one message, so the
// post-handler seam falls back to it only until it is set. Existing consumers that omit
// these options retain the built-in response bodies and status codes.
//
// # Nil safety
//
// [New] returns nil when conn is nil. A nil [*Middleware] returns a pass-through
// handler from [Middleware.Check]. [NewWithStore] returns a non-nil middleware
// even for a nil or typed-nil store so keyed mutations fail closed instead of
// silently bypassing idempotency.
package idempotency
