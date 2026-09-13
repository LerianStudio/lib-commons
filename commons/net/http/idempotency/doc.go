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
//   - A caller-provided store missing, errored, or returning an invalid state:
//     request receives 503 and the mutation handler does not run.
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
//   - Handler success: response status, headers, content type, and body are
//     compare-safely completed only by the acquisition owner. Capture, encoding,
//     persistence, or stale-owner failures return 503 and retain processing
//     ownership so callers reconcile instead of retrying under a new key. That
//     503 is the one branch [WithPostHandlerUnavailableHandler] answers, because
//     the mutation already happened.
//   - Handler 4xx: cached and replayed by default. Use
//     [WithClientErrorPolicy] with [ClientErrorPolicyRelease] to compare-safely
//     release the record and allow a corrected request to reuse the key.
//   - Handler failure or 5xx: the acquisition is compare-safely released only
//     by its owner, allowing a retry without deleting a replacement lock.
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
// ones observed AFTER it ran, [WithConflictHandler] for an in-flight duplicate,
// and [WithKeyReuseHandler] for the same key used by a different request.
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
