// Copyright 2025 Lerian Studio.

package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureVersion controls the HMAC signing format used for X-Webhook-Signature.
type SignatureVersion int

const (
	// SignatureV0 produces legacy payload-only signatures: "sha256=<hex(HMAC(payload))>".
	// This is the default for backward compatibility with existing consumers.
	SignatureV0 SignatureVersion = iota

	// SignatureV1 produces versioned timestamp-bound signatures:
	// "v1,sha256=<hex(HMAC(v1:<timestamp>.<payload>))>".
	// The timestamp is included in the HMAC input to prevent replay attacks.
	// Receivers must verify freshness (e.g., reject timestamps older than 5 minutes).
	SignatureV1
)

// computeHMAC returns the hex-encoded HMAC-SHA256 of payload using the given secret.
// This is the legacy (v0) format that signs the raw payload only.
//
// Design note — timestamp not included in v0 signature (by intent):
// The signature covers the raw payload only, not the X-Webhook-Timestamp value.
// This format is maintained for backward compatibility. New deployments should
// prefer SignatureV1 (via WithSignatureVersion) which binds the timestamp into
// the HMAC input to enable replay protection.
func computeHMAC(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)

	return hex.EncodeToString(mac.Sum(nil))
}

// computeHMACv1 returns a versioned signature string:
//
//	"v1,sha256=<hex(HMAC-SHA256("v1:<timestamp>.<payload>", secret))>"
//
// The version prefix "v1:" followed by the decimal timestamp and a dot separator
// are prepended to the payload before computing the HMAC, binding the timestamp
// to the signature. Receivers must parse the "v1," prefix, extract the timestamp
// from the X-Webhook-Timestamp header, reconstruct the signing input, and verify
// the HMAC before accepting the webhook.
func computeHMACv1(payload []byte, timestamp int64, secret string) string {
	ts := strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v1:"))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)

	return "v1,sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature verifies a webhook signature by auto-detecting the version
// from the signature string format. Returns nil on valid signature, or an error
// describing the mismatch.
//
// Supported formats:
//   - "sha256=<hex>"          — v0 (payload-only)
//   - "v1,sha256=<hex>"       — v1 (timestamp-bound)
//
// For v1 signatures, the timestamp parameter is required to reconstruct the
// signing input. For v0 signatures, the timestamp is ignored.
func VerifySignature(payload []byte, timestamp int64, secret, signature string) error {
	switch {
	case strings.HasPrefix(signature, "v1,"):
		expected := computeHMACv1(payload, timestamp, secret)
		if !hmac.Equal([]byte(signature), []byte(expected)) {
			return errors.New("webhook: v1 signature mismatch")
		}

		return nil

	case strings.HasPrefix(signature, "sha256="):
		expected := "sha256=" + computeHMAC(payload, secret)
		if !hmac.Equal([]byte(signature), []byte(expected)) {
			return errors.New("webhook: v0 signature mismatch")
		}

		return nil

	default:
		return errors.New("webhook: unrecognized signature format")
	}
}

// VerifySignatureWithFreshness verifies a v1 webhook signature and additionally
// checks that the timestamp is within the given tolerance window from now.
// This provides replay protection: even if an attacker captures a valid
// payload+signature pair, it becomes invalid after the tolerance window expires.
//
// For v0 ("sha256=...") signatures, freshness cannot be enforced because the
// timestamp is not covered by the HMAC. Callers receiving v0 signatures should
// use VerifySignature and implement replay protection independently (e.g.,
// idempotency keys or event-ID tracking).
func VerifySignatureWithFreshness(payload []byte, timestamp int64, secret, signature string, tolerance time.Duration) error {
	if err := VerifySignature(payload, timestamp, secret, signature); err != nil {
		return err
	}

	// Freshness check only applies to v1 where the timestamp is signed.
	if strings.HasPrefix(signature, "v1,") {
		eventTime := time.Unix(timestamp, 0)
		delta := time.Since(eventTime)

		if delta < 0 {
			delta = -delta
		}

		if delta > tolerance {
			return fmt.Errorf("webhook: timestamp outside tolerance window (%s > %s)", delta.Truncate(time.Second), tolerance)
		}
	}

	return nil
}
