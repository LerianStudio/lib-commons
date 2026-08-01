// Package idempotency provides Fiber middleware for best-effort idempotency
// backed by Redis.
//
// The middleware enforces at-most-once semantics when Redis is available. On
// Redis outages, it fails open to preserve service availability — duplicate
// requests may execute more than once. Callers that require strict at-most-once
// guarantees must pair this middleware with application-level safeguards.
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
// A companion response key at <prefix><tenantID>:<idempotencyKey>:response stores
// the cached response body and headers for replay.
//
// # Payload fingerprint
//
// An idempotency key alone does not identify a request — it identifies the
// caller's claim that two requests are the same one. So the marker value carries
// a fingerprint of the request beside its state:
//
//	<state>:<sha256 of method, path and raw body>
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
// Marker values written before fingerprints existed carry no separator. Those
// replay on the key alone, as they did, and log at WARN — refusing them would
// turn a legitimate in-flight retry into a 422 for the length of a deploy. The
// exposure is bounded: legacy records expire with their TTL and none are created.
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
//   - Redis unavailable (GetClient, SetNX, or Get failures): request proceeds
//     without idempotency enforcement (fail-open), logged at WARN level.
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
//     "IDEMPOTENCY_CONFLICT" when no custom handler is configured.
//   - Duplicate key in "complete" state but no cached body (e.g., body exceeded
//     [WithMaxBodyCache]): 200 OK with code "IDEMPOTENT" and detail "request
//     already processed" is returned.
//   - Handler success: response (status, headers, body) is cached via a Redis
//     pipeline and the key is marked "complete".
//   - Handler failure: both the lock key and response key are deleted so the
//     client can retry with the same idempotency key.
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
// handler from [Middleware.Check].
package idempotency
