// Package sanitize provides shared redaction helpers used by logging,
// telemetry, and error-message handling across lib-commons.
//
// Today it exposes a single package-level constant (SecretRedactionMarker)
// that packages use when replacing sensitive tokens (passwords, SASL
// credentials, API keys) in user-facing messages. Centralizing the marker
// keeps log-stream shapes uniform so operators can grep for the same
// literal across every Lerian service.
package sanitize

// SecretRedactionMarker is the canonical replacement literal used by
// lib-commons packages when redacting sensitive values in strings.
//
// The literal is intentionally four asterisks — short enough to keep error
// messages legible, long enough to stand out in logs. Do not shorten or
// extend without updating every call site in the same change; the marker
// is an implicit operator contract (dashboards and SIEM rules may key off
// it).
const SecretRedactionMarker = "****"
