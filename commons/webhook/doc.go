// Package webhook provides a secure webhook delivery engine with SSRF protection,
// HMAC-SHA256 payload signing, and exponential backoff retries.
//
// # Security features
//
//   - Two-layer SSRF protection: pre-resolution IP validation + DNS-pinned delivery
//   - DNS rebinding prevention via resolved IP pinning
//   - Blocks private, loopback, and link-local IP ranges
//   - HMAC-SHA256 signature in X-Webhook-Signature header (versioned format)
//   - URL query parameters and userinfo stripped from log output to prevent credential leakage
//
// WithAllowPrivateNetwork is an intentionally narrow non-production escape
// hatch. It only relaxes private/loopback blocking for explicit IP-literal
// targets such as 127.0.0.1 or 10.0.0.5, and only when the active security tier
// permits it or ALLOW_WEBHOOK_PRIVATE_NETWORK provides an explicit override
// reason. Hostnames that resolve to private addresses remain blocked, so
// public-looking names cannot bypass DNS-pinned SSRF validation.
//
// # Delivery model
//
//   - Concurrent delivery with configurable semaphore (default: 20 goroutines)
//   - Exponential backoff with jitter (1s, 2s, 4s, ...)
//   - Per-endpoint retry with configurable max attempts (default: 3)
//
// # HMAC signature versions
//
// The X-Webhook-Signature header supports two formats, selectable via
// WithSignatureVersion at Deliverer construction time:
//
// ## v0 (default — backward compatible)
//
// Format: "sha256=<hex(HMAC-SHA256(payload, secret))>"
//
// The HMAC covers the raw payload bytes only. X-Webhook-Timestamp is sent as
// a separate informational header but is NOT covered by the HMAC. An attacker
// who captures a valid payload+signature pair can replay it. Receivers must
// implement replay protection independently (e.g., event-ID tracking,
// idempotency keys).
//
// ## v1 (recommended for new deployments)
//
// Format: "v1,sha256=<hex(HMAC-SHA256("v1:<timestamp>.<payload>", secret))>"
//
// The HMAC input includes a version prefix, the decimal Unix-epoch timestamp
// from X-Webhook-Timestamp, and the raw payload — binding the timestamp to
// the signature. Receivers must:
//  1. Parse the "v1," prefix from X-Webhook-Signature.
//  2. Read X-Webhook-Timestamp and reconstruct the signing input as
//     "v1:<timestamp>.<payload>".
//  3. Compute HMAC-SHA256 with the shared secret and compare (constant-time).
//  4. Reject timestamps outside an acceptable clock-skew window (e.g., +/- 5 min)
//     or track event IDs / nonces to prevent replay.
//
// Use VerifySignature or VerifySignatureWithFreshness for receiver-side
// verification — both auto-detect the version from the signature string.
//
// # Migration from v0 to v1
//
// Because this is a library used by multiple services, the default remains v0
// to avoid breaking existing consumers. To migrate:
//  1. Update all webhook receivers to accept both v0 and v1 formats (use
//     VerifySignature which auto-detects the version).
//  2. Once all receivers are updated, switch senders to v1 by constructing
//     the Deliverer with WithSignatureVersion(SignatureV1).
//  3. After a transition period, receivers may optionally reject v0 signatures
//     to enforce replay protection.
package webhook
