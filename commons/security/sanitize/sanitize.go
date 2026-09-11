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
// sensitive in addition to secrets, so a PII field NAME is redacted BY DESIGN.
// For a tenant-isolated fintech service that is the intended consequence of
// adopting the shared taxonomy, not over-redaction.
//
// A field name only helps when there is one. Two kinds of PII show up in error
// strings BARE, with nothing around them to key on, and both are handled by
// value instead: an e-mail address, and a card number (12 to 19 digits, gated on
// Luhn so an order id or an epoch-millisecond timestamp of the same length stays
// readable). Nothing else is inferred from shape alone.
//
// # What it is not
//
// It does not bound length, deliberately. commons/outbox owns the length-bounded
// variant for the last_error column, which is a storage concern rather than a
// redaction one — and truncating BEFORE redaction is actively unsafe, because a
// cut landing mid-secret strands a readable prefix ("postgres://user:pas") that
// no later pattern can recognise. The patterns are RE2, so they are linear in
// the input: ten passes over even a multi-megabyte string is tens of
// milliseconds, which is not worth a correctness hazard.
package sanitize

import (
	"errors"
	"fmt"
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

// authHeaderPattern matches "Authorization: ..." header forms, including
// Proxy-Authorization, and redacts EVERYTHING after the colon to the end of the
// line (or to the first ',' or '"', which end a header value inside a JSON or
// struct dump).
//
// TWO THINGS ARE DELIBERATE HERE. The scheme is optional, because
// `Authorization: <bare-opaque-token>` is a real header and requiring a scheme
// let it through untouched unless some other pattern happened to catch it. And
// the scheme is matched from a CLOSED LIST rather than as "the first word",
// because "the first word" is indistinguishable from the first word OF an opaque
// token: `Authorization: sessiontoken abc123` would keep "sessiontoken" as a
// scheme and redact only what follows. An unrecognised leading word is treated
// as credential material, which is the safe reading.
//
// Running to end of line rather than to the next space redacts a word or two of
// surrounding prose when the header sits mid-sentence. That is the correct
// direction of error for a credential.
var authHeaderPattern = regexp.MustCompile(
	`(?i)\b((?:proxy-)?authorization|auth)([[:space:]]*:[[:space:]]*)` +
		`((?:bearer|basic|digest|negotiate|ntlm|token|apikey|hmac|aws4-hmac-sha256)[[:space:]]+)?` +
		`[^\r\n,"]+`)

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
	// Bare e-mail addresses. PII the taxonomy already calls sensitive by field
	// name, appearing with no field name — which is how a validator, an SMTP
	// client or a unique-constraint violation echoes it. Same expression
	// commons/outbox has redacted before storing last_error.
	regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`),
}

// cardNumberPattern is the CANDIDATE shape for a bare card number: an unbroken
// run of 12 to 19 digits. It is a candidate and not a verdict — see
// redactCardNumbers, where the Luhn check decides.
var cardNumberPattern = regexp.MustCompile(`\b\d{12,19}\b`)

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

	// 1. PEM BLOCKS FIRST, AND THE ORDER IS LOAD-BEARING. An armored block is
	// often the VALUE of a sensitive key ("private_key=-----BEGIN ..."), which is
	// how a config-loading error echoes one. Let the key=value pass run first and
	// it consumes "-----BEGIN" as that key's value, leaving no marker for this
	// rule to anchor on — and the entire base64 body survives into the log.
	s = pemBlockPattern.ReplaceAllString(s, SecretRedactionMarker)

	// 2. URL-shaped tokens: strip userinfo, keep scheme and host.
	s = urlPattern.ReplaceAllStringFunc(s, redactURLUserinfo)

	// 3. Azure SAS signature parameter inside a query string.
	s = azureSASSignaturePattern.ReplaceAllString(s, "${1}"+SecretRedactionMarker)

	// 4. Authorization header forms: keep the scheme, redact the credential.
	s = authHeaderPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := authHeaderPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}

		// parts[3] is the recognised scheme WITH its trailing space, or empty.
		return parts[1] + parts[2] + parts[3] + SecretRedactionMarker
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

	// 8. Bare card numbers, last, so a vendor token that happens to contain a
	// long digit run has already been consumed as a whole.
	return redactCardNumbers(s)
}

// redactCardNumbers redacts digit runs that pass the Luhn check, and ONLY those.
//
// THE GATE IS THE POINT. A bare 12-to-19-digit run is also an order number, a
// ledger id, an epoch-millisecond timestamp and half the correlation ids in a
// fintech error string. Redacting the shape unconditionally would empty out the
// messages this package exists to keep diagnosable, and it would do it silently.
// Luhn is what a card number satisfies and an arbitrary identifier satisfies
// only one time in ten.
func redactCardNumbers(s string) string {
	return cardNumberPattern.ReplaceAllStringFunc(s, func(candidate string) string {
		if !passesLuhn(candidate) {
			return candidate
		}

		return SecretRedactionMarker
	})
}

// passesLuhn reports whether the digits satisfy the Luhn checksum every payment
// card number carries.
//
// It is implemented here rather than shared with commons/outbox's copy because
// sanitize is a leaf package and outbox is not: importing outbox to reach one
// checksum would hang a broker, a dispatcher and two database adapters off every
// service that only wanted to redact a log line. Consolidating the two belongs
// with the replacement of the outbox redactor, not here.
func passesLuhn(number string) bool {
	sum := 0
	double := false

	for i := len(number) - 1; i >= 0; i-- {
		digit := int(number[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}

		if double {
			if digit *= 2; digit > 9 {
				digit -= 9
			}
		}

		sum += digit
		double = !double
	}

	return sum%10 == 0
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
// CLASSIFICATION SURVIVES, THE RAW TEXT DOES NOT. errors.Is and errors.As still
// reach everything underneath, because a caller must still be able to classify
// the driver error it may no longer print. What is deliberately NOT provided is
// Unwrap: with it, errors.Unwrap(sanitized).Error() hands any caller the raw DSN
// straight back, and every redaction above is decorative.
//
// errors.As remains the one door to a printable cause, and it is a narrow one: a
// caller must already know the concrete type it is asking for. Ask, classify, and
// print only the wrapper.
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

// Is and As delegate to the cause so errors.Is and errors.As keep classifying,
// while the absence of Unwrap means no caller can obtain the cause itself and
// print it. Both are the hooks errors.Is/As look for before they walk Unwrap.
func (e *redactedError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e *redactedError) As(target any) bool { return errors.As(e.cause, target) }

// GoString stops %#v from doing what Unwrap no longer allows. Without it, the
// default struct formatting prints the cause field, and for a cause whose
// concrete type is a struct VALUE that means printing its unredacted contents.
func (e *redactedError) GoString() string {
	return fmt.Sprintf("sanitize.redactedError{message: %q}", e.message)
}

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
