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
// key and break isolation.
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
//     opt-in per request).
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
//     ownership so callers reconcile instead of retrying under a new key.
//   - Handler 4xx: cached and replayed by default. Use
//     [WithClientErrorPolicy] with [ClientErrorPolicyRelease] to compare-safely
//     release the record and allow a corrected request to reuse the key.
//   - Handler failure or 5xx: the acquisition is compare-safely released only
//     by its owner, allowing a retry without deleting a replacement lock.
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
// Every rejection branch has a callback seam so consumers can write their own
// error format, including RFC 9457 problem details: [WithRejectedHandler] for an
// oversized key, [WithUnavailableHandler] for fail-closed Redis failures,
// [WithConflictHandler] for an in-flight duplicate, and [WithKeyReuseHandler]
// for the same key used by a different request. Existing consumers that omit
// these options retain the built-in response bodies and status codes.
//
// # Nil safety
//
// [New] returns nil when conn is nil. A nil [*Middleware] returns a pass-through
// handler from [Middleware.Check]. [NewWithStore] returns a non-nil middleware
// even for a nil or typed-nil store so keyed mutations fail closed instead of
// silently bypassing idempotency.
package idempotency
