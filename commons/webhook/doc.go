// Package webhook provides a secure webhook delivery engine with SSRF protection,
// HMAC-SHA256 payload signing, and exponential backoff retries.
//
// # Security features
//
//   - Two-layer SSRF protection: pre-resolution IP validation + DNS-pinned delivery
//   - DNS rebinding prevention via resolved IP pinning
//   - Blocks private, loopback, and link-local IP ranges
//   - HMAC-SHA256 signature in X-Webhook-Signature header (sha256=HEX)
//   - URL query parameters stripped from log output to prevent credential leakage
//
// # Delivery model
//
//   - Concurrent delivery with configurable semaphore (default: 20 goroutines)
//   - Exponential backoff with jitter (1s, 2s, 4s, ...)
//   - Per-endpoint retry with configurable max attempts (default: 3)
//
// # HMAC signature scope
//
// The X-Webhook-Signature header carries HMAC-SHA256 computed over the raw
// payload bytes only (not the timestamp). X-Webhook-Timestamp is sent as a
// separate header for informational purposes. Receivers who need replay
// protection should validate that X-Webhook-Timestamp is within an acceptable
// window (e.g., ±5 minutes) independently of the signature check.
// Changing the signature scope to include the timestamp would be a breaking
// change for all existing consumers.
package webhook
