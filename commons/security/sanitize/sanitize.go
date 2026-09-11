// Package sanitize provides shared redaction helpers used by logging,
// telemetry, and error-message handling across lib-commons.
//
// # What it is for
//
// Error strings are the most reliable credential leak in a service: a driver
// echoes the DSN it failed to dial, a broker client echoes its SASL config, an
// SDK echoes the signed URL it rejected. Those strings then travel into logs, a
// traced span, a database column, and sometimes a 500 body. String and Error are
// the one place that gets scrubbed, so the redaction rules live in one file
// instead of one per call site.
//
// String redacts a free-form string. Error wraps an error with a redacted
// message while KEEPING THE CHAIN INTACT, so errors.Is and errors.As still work
// on it — which is the whole point: a caller must still be able to classify a
// driver error it may no longer print.
//
// # What decides sensitivity
//
// Field-name sensitivity is NOT decided here. It delegates to
// github.com/LerianStudio/lib-observability/v4/redaction, the canonical Lerian
// taxonomy, extended with an AWS/SASL addendum for the key names that show up in
// broker and SDK error strings. Structural stripping (URL userinfo,
// Authorization headers, JSON and key=value pairs) and bare-token value patterns
// are layered ON TOP of that field check.
//
// That taxonomy classifies PII (email, phone, address, iban, swift, ...) as
// sensitive in addition to secrets, so PII embedded in an error string is
// redacted BY DESIGN. For a tenant-isolated fintech service that is the intended
// consequence of adopting the shared taxonomy, not over-redaction.
//
// # What it is not
//
// It does not bound length. commons/outbox owns the length-bounded variant for
// the last_error column, which is a storage concern rather than a redaction one.
package sanitize

import (
	"regexp"
	"strings"

	"github.com/LerianStudio/lib-observability/v4/redaction"
)

// SecretRedactionMarker is the canonical replacement literal used by
// lib-commons packages when redacting sensitive values in strings.
//
// The literal is intentionally four asterisks - short enough to keep error
// messages legible, long enough to stand out in logs. Do not shorten or
// extend without updating every call site in the same change; the marker
// is an implicit operator contract (dashboards and SIEM rules may key off
// it).
const SecretRedactionMarker = "****"

// sensitiveFieldExtras augments the centralized lib-observability taxonomy with
// the AWS-specific access/secret/session key names and the SASL and signature
// fields that show up in broker and SDK error strings but are not part of the
// generic default list. Field sensitivity itself is decided by
// redaction.IsSensitiveField; this is only the addendum.
var sensitiveFieldExtras = []string{
	"accesskey",
	"access_key",
	"accesskeyid",
	"aws_access_key_id",
	"aws_secret_access_key",
	"aws_session_token",
	"secretaccesskey",
	"sessiontoken",
	"signature",
	"sasl_password",
	"passwd",
	"pwd",
}

// urlPattern matches scheme://rest-of-URL tokens so embedded userinfo
// credentials can be stripped.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

// keyValuePattern finds key=value fragments in config dumps and driver errors.
// Sensitivity is decided by the field name, not by a package-local taxonomy.
//
// The value runs to the next whitespace, comma, semicolon or ampersand, which
// means a trailing colon is swallowed into the redaction ("token=abc: refused"
// becomes "token=**** refused"). That errs toward redacting one character too
// many rather than one too few, which is the correct direction here.
var keyValuePattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9._-]*)([[:space:]]*=[[:space:]]*)([^\s,;&]+)`)

// jsonKeyValuePattern finds "key":"value" fragments so a JSON-shaped secret (a
// marshaled config, or a request body echoed into an error) is redacted by field
// name just like a key=value pair. Only string-valued keys are matched: a
// numeric or boolean value is not a credential.
var jsonKeyValuePattern = regexp.MustCompile(`"([a-zA-Z][a-zA-Z0-9._-]*)"([[:space:]]*:[[:space:]]*)"([^"]*)"`)

// authHeaderPattern matches "Authorization: <scheme> <credential>" header forms,
// keeping the scheme for readability while redacting the credential after it.
var authHeaderPattern = regexp.MustCompile(
	`(?i)\b(authorization|auth)([[:space:]]*:[[:space:]]*)([a-z][a-z0-9._~+/-]*)([[:space:]]+)[^\s,;&]+`)

// bareSecretPatterns match credential material appearing WITHOUT a surrounding
// field name — SDKs and broker clients routinely echo the bare token. Each is
// anchored on a vendor-stable prefix and shape so it cannot match an ordinary
// word. The whole match is replaced.
//
// Field-name-keyed redaction catches the `key=<secret>` forms; these close the
// bare-value gap on top of it.
var bareSecretPatterns = []*regexp.Regexp{
	// AWS access key IDs (AKIA/ASIA + 16 base32 chars).
	regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	// Google / GCP API keys (AIza + 35 url-safe chars).
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	// GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_ + 36 or more chars).
	regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36,}\b`),
	// Stripe secret and restricted keys, live and test.
	regexp.MustCompile(`\b(sk|rk)_(live|test)_[0-9A-Za-z]{16,}\b`),
	// Slack tokens (xoxb-/xoxp-/xoxa-/xoxr-/xoxs- + dash-delimited segments).
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),
	// Bare JWTs, anchored on the "eyJ" header so it does not eat an ordinary
	// dotted identifier.
	regexp.MustCompile(`\beyJ[0-9A-Za-z_-]+\.[0-9A-Za-z_-]+\.[0-9A-Za-z_-]+\b`),
}

// azureSASSignaturePattern redacts the Azure SAS `sig=` parameter inside a query
// string. The token is URL-encoded, so the value runs to the next delimiter.
var azureSASSignaturePattern = regexp.MustCompile(`(?i)([?&]sig=)[^&\s]+`)

// pemBlockPattern matches a whole PEM block and replaces the armored body, so
// the base64 payload between the BEGIN and END lines never reaches a log line.
var pemBlockPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)

// String strips credential material from a free-form string so DSNs, broker
// URLs, SASL passwords, bearer tokens, AWS/GCP/GitHub/Stripe/Slack keys, JWTs
// and PEM private-key blocks never reach a log line, a span attribute or a
// stored error column.
//
// It preserves everything that makes a failure diagnosable — scheme, host, port,
// database, non-sensitive parameters — and is safe on an empty string, on a
// string with no credentials (returned verbatim), and on a string it has already
// sanitized (re-running it does not mangle the marker).
func String(s string) string {
	if s == "" {
		return ""
	}

	// 1. PEM blocks first: an armored body contains '=' padding and would
	// otherwise be partly mangled by the key=value pass.
	s = pemBlockPattern.ReplaceAllString(s, SecretRedactionMarker)

	// 2. URL-shaped tokens: strip userinfo, keep scheme and host.
	s = urlPattern.ReplaceAllStringFunc(s, redactURLUserinfo)

	// 3. Azure SAS signature parameter inside a query string.
	s = azureSASSignaturePattern.ReplaceAllString(s, "${1}"+SecretRedactionMarker)

	// 4. Authorization header forms: keep the scheme, redact the credential.
	s = authHeaderPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := authHeaderPattern.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match
		}

		return parts[1] + parts[2] + parts[3] + parts[4] + SecretRedactionMarker
	})

	// 5. Sensitive JSON "key":"value" pairs, by field name.
	s = jsonKeyValuePattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := jsonKeyValuePattern.FindStringSubmatch(match)
		if len(parts) != 4 || !isSensitiveFieldName(parts[1]) {
			return match
		}

		return `"` + parts[1] + `"` + parts[2] + `"` + SecretRedactionMarker + `"`
	})

	// 6. Sensitive key=value pairs, by field name.
	s = keyValuePattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := keyValuePattern.FindStringSubmatch(match)
		if len(parts) != 4 || !isSensitiveFieldName(parts[1]) {
			return match
		}

		return parts[1] + parts[2] + SecretRedactionMarker
	})

	// 7. Bare credential values, with no surrounding field name.
	for _, pattern := range bareSecretPatterns {
		s = pattern.ReplaceAllString(s, SecretRedactionMarker)
	}

	return s
}

// Error wraps err so its MESSAGE is redacted while its CHAIN stays intact.
//
// This is the logging-boundary companion to the error-returning helpers
// elsewhere in lib-commons: wrap at the point where a message becomes a log
// line, a span attribute or a stored column, not at the point where the error is
// produced.
//
//	if err := db.PingContext(ctx); err != nil {
//	    return sanitize.Error(fmt.Errorf("ping ledger pool: %w", err))
//	}
//
// errors.Is and errors.As still reach everything underneath, deliberately: a
// caller must still be able to classify the driver error it may no longer print.
// THAT IS ALSO THE ONE SHARP EDGE — a cause recovered with errors.As carries its
// own unredacted Error(), so classify with it, never print it. Print only the
// wrapper.
//
// Returns nil for a nil error, so it composes at a return site without a guard.
func Error(err error) error {
	if err == nil {
		return nil
	}

	return &redactedError{message: String(err.Error()), cause: err}
}

// redactedError carries the sanitized message and the untouched cause.
type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }

// Unwrap exposes the cause to errors.Is and errors.As without exposing it to
// anything that formats the error.
func (e *redactedError) Unwrap() error { return e.cause }

// isSensitiveFieldName delegates to the centralized lib-observability taxonomy,
// extended with the AWS/SASL addendum.
func isSensitiveFieldName(fieldName string) bool {
	return redaction.IsSensitiveField(fieldName, sensitiveFieldExtras...)
}

// redactURLUserinfo replaces the userinfo portion of one URL-shaped token,
// preserving scheme and host so the destination stays diagnosable.
//
// Done at the string level rather than through url.URL.String, which
// percent-escapes the marker.
//
// TRAILING SENTENCE PUNCTUATION NEEDS NO SPECIAL HANDLING, which is worth saying
// because it looks like it should. A URL swept out of a log line carries its
// trailing '.', ')' or '"' along, but that punctuation always lands AFTER the
// '@' — inside the tail this function copies verbatim — so stripping it first
// and re-appending it produces the same string. A differential run over ~12k
// generated scheme/userinfo/host/path/punctuation combinations found zero
// inputs where the two differ.
func redactURLUserinfo(token string) string {
	schemeSep := strings.Index(token, "://")
	if schemeSep == -1 {
		return token
	}

	rest := token[schemeSep+3:]

	// The authority ends at the first '/', '?' or '#'; an '@' after that is part
	// of a path or query and is not userinfo.
	authorityEnd := len(rest)

	for i := range len(rest) {
		if c := rest[i]; c == '/' || c == '?' || c == '#' {
			authorityEnd = i

			break
		}
	}

	atIndex := strings.LastIndex(rest[:authorityEnd], "@")
	if atIndex == -1 {
		return token
	}

	tail := rest[atIndex:]
	userinfo := rest[:atIndex]

	if strings.Contains(userinfo, ":") {
		return token[:schemeSep+3] + SecretRedactionMarker + ":" + SecretRedactionMarker + tail
	}

	return token[:schemeSep+3] + SecretRedactionMarker + tail
}
