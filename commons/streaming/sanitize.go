package streaming

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/security/sanitize"
)

// redactedMarker is the replacement string substituted for credentials.
// Delegates to the shared sanitize.SecretRedactionMarker so streaming,
// rabbitmq, and any future redaction caller emit the same literal.
const redactedMarker = sanitize.SecretRedactionMarker

// urlPattern matches scheme://rest-of-URL sequences. Kept intentionally simple
// to mirror commons/rabbitmq/rabbitmq.go:129. Credential redaction is applied
// per-match via url.Parse and a fallback regex.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

// kvCredentialPattern targets raw "password=..." and "pass=..." key/value
// pairs that may appear in config dumps or error messages outside of URLs.
// Case-insensitive; value stops at whitespace or common separators.
var kvCredentialPattern = regexp.MustCompile(`(?i)\b(password|pass)=[^\s,;&]+`)

// sanitizeBrokerURL strips credentials from broker URLs and "password=" /
// "pass=" key-value pairs. Returns the sanitized message.
//
// Mirrors the regex-driven strategy of commons/rabbitmq/rabbitmq.go:129
// (URL pattern) and the redaction token from rabbitmq/rabbitmq.go:124.
// Safe to call on empty strings and messages without credentials.
func sanitizeBrokerURL(s string) string {
	if s == "" {
		return ""
	}

	// Redact credentials inside URL-shaped substrings.
	s = urlPattern.ReplaceAllStringFunc(s, redactURLCandidate)

	// Redact bare "password=..." / "pass=..." KV pairs.
	s = kvCredentialPattern.ReplaceAllStringFunc(s, func(match string) string {
		eq := strings.Index(match, "=")
		if eq < 0 {
			return match
		}

		return match[:eq+1] + redactedMarker
	})

	return s
}

// redactURLCandidate applies URL-aware redaction to a single URL-shaped token.
// Uses a manual authority split so the redacted marker is emitted verbatim
// (url.URL.String() percent-escapes the userinfo, which would turn "****"
// into "%2A%2A%2A%2A"). url.Parse is used only to detect whether the
// candidate contains a password; the replacement is string-level.
func redactURLCandidate(candidate string) string {
	// Trim trailing punctuation that is not part of the URL.
	trimmed, suffix := splitTrailingPunctuation(candidate)

	// A fast-path via url.Parse tells us whether a password is present.
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.User == nil {
		return fallbackRedact(trimmed) + suffix
	}

	if _, hasPass := parsed.User.Password(); !hasPass {
		return trimmed + suffix
	}

	// Password present — delegate to the manual splitter so the marker is
	// emitted without URL-percent-encoding.
	return fallbackRedact(trimmed) + suffix
}

// splitTrailingPunctuation moves trailing sentence punctuation off the URL so
// it does not leak into the redacted form or confuse url.Parse.
func splitTrailingPunctuation(candidate string) (string, string) {
	end := len(candidate)

	for end > 0 {
		switch candidate[end-1] {
		case '.', ',', ';', ')', ']', '}', '"', '\'':
			end--
		default:
			return candidate[:end], candidate[end:]
		}
	}

	return "", candidate
}

// fallbackRedact scans for "://user:pass@" and replaces the password token.
// Used when url.Parse does not surface userinfo cleanly.
func fallbackRedact(token string) string {
	schemeSep := strings.Index(token, "://")
	if schemeSep == -1 {
		return token
	}

	rest := token[schemeSep+3:]
	authorityEnd := len(rest)

	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '/', '?', '#':
			authorityEnd = i
			i = len(rest) // break outer loop
		}
	}

	atIndex := strings.LastIndex(rest[:authorityEnd], "@")
	if atIndex == -1 {
		return token
	}

	userinfo := rest[:atIndex]
	tail := rest[atIndex:]

	username, _, found := strings.Cut(userinfo, ":")
	if !found {
		// No password separator — nothing to redact.
		return token
	}

	return token[:schemeSep+3] + username + ":" + redactedMarker + tail
}
