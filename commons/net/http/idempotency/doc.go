// Package idempotency provides Fiber middleware for at-most-once request semantics
// backed by Redis.
//
// The middleware uses the X-Idempotency request header combined with the tenant ID
// (from tenant-manager context) to form a composite Redis key, ensuring per-tenant
// isolation. Duplicate requests receive the original response with the
// X-Idempotency-Replayed header set to "true".
//
// # Quick start
//
//	conn, _ := redis.New(ctx, cfg)
//	idem := idempotency.New(conn)
//	app.Post("/orders", idem.Check(), createOrderHandler)
//
// # Behavior
//
//   - GET/OPTIONS requests pass through (idempotency is irrelevant for reads).
//   - Absent header: request proceeds normally (idempotency is opt-in).
//   - Header exceeds MaxKeyLength (default 256): request rejected with 400.
//   - Duplicate key: cached response replayed with X-Idempotency-Replayed: true.
//   - Redis failure: request proceeds (fail-open for availability).
//   - Handler success: response cached; handler failure: key deleted (client can retry).
//
// # Redis key namespace convention
//
// Keys follow the pattern: <prefix><tenantID>:<idempotencyKey>
// with a companion response key at <prefix><tenantID>:<idempotencyKey>:response.
//
// The default prefix is "idempotency:" and can be overridden via WithKeyPrefix.
// This namespacing convention is consistent with other lib-commons packages that
// use Redis (e.g., rate limiting uses "ratelimit:<tenantID>:..."). Per-tenant
// isolation is enforced by embedding the tenant ID into the key rather than
// using separate Redis databases or key-space notifications, which keeps the
// implementation topology-agnostic (standalone, sentinel, and cluster all behave
// identically with this approach).
//
// # Nil safety
//
// A nil *Middleware returns a pass-through handler from Check().
package idempotency
