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
// When a tenant ID is present, keys are scoped per-tenant to prevent
// cross-tenant collisions. When no tenant is in context (e.g., non-tenant-scoped
// routes), the key becomes <prefix>:<idempotencyKey> in the global namespace.
//
// A companion response key at <prefix><tenantID>:<idempotencyKey>:response stores
// the cached response body and headers for replay.
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
//   - Header exceeds [WithMaxKeyLength] (default 256): request is passed to the
//     configured [WithRejectedHandler]. When no custom handler is set, a 400 JSON
//     response with code "VALIDATION_ERROR" is returned.
//   - Redis unavailable (GetClient, SetNX, or Get failures): request proceeds
//     without idempotency enforcement (fail-open), logged at WARN level.
//   - Duplicate key with cached response: the original response is replayed
//     faithfully — status code, headers (including Location, ETag, Set-Cookie),
//     content type, and body — with [constants.IdempotencyReplayed] set to "true".
//   - Duplicate key still in "processing" state (in-flight): 409 Conflict with
//     code "IDEMPOTENCY_CONFLICT" is returned.
//   - Duplicate key in "complete" state but no cached body (e.g., body exceeded
//     [WithMaxBodyCache]): 200 OK with code "IDEMPOTENT" and detail "request
//     already processed" is returned.
//   - Handler success: response (status, headers, body) is cached via a Redis
//     pipeline and the key is marked "complete".
//   - Handler failure: both the lock key and response key are deleted so the
//     client can retry with the same idempotency key.
//
// # Nil safety
//
// [New] returns nil when conn is nil. A nil [*Middleware] returns a pass-through
// handler from [Middleware.Check].
package idempotency
