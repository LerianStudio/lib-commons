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
// payload bytes only. X-Webhook-Timestamp is sent as a separate informational
// header but is NOT covered by the HMAC — an attacker who captures a valid
// payload+signature pair can replay it with a fresh timestamp.
//
// Receivers who need replay protection must implement it independently, for
// example by tracking event IDs or embedding a nonce in the payload itself.
// Timestamp-window checks alone are insufficient because the timestamp is
// unsigned. Including the timestamp in the HMAC would be a breaking change
// for existing consumers.
package webhook
