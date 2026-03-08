package security

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
)

var defaultSensitiveFields = []string{
	"password",
	"newpassword",
	"oldpassword",
	"passwordsalt",
	"token",
	"secret",
	"key",
	"authorization",
	"auth",
	"credential",
	"credentials",
	"apikey",
	"api_key",
	"access_token",
	"accesstoken",
	"refresh_token",
	"refreshtoken",
	"bearer",
	"jwt",
	"session_id",
	"sessionid",
	"cookie",
	"private_key",
	"privatekey",
	"clientid",
	"client_id",
	"clientsecret",
	"client_secret",
	"passwd",
	"passphrase",
	"card_number",
	"cardnumber",
	"cvv",
	"cvc",
	"ssn",
	"social_security",
	"pin",
	"otp",
	"account_number",
	"accountnumber",
	"routing_number",
	"routingnumber",
	"iban",
	"swift",
	"swift_code",
	"bic",
	"pan",
	"expiry",
	"expiry_date",
	"expiration_date",
	"card_expiry",
	"date_of_birth",
	"dob",
	"tax_id",
	"taxid",
	"tin",
	"national_id",
	"sort_code",
	"bsb",
	"security_answer",
	"security_question",
	"mother_maiden_name",
	"mfa_code",
	"totp",
	"biometric",
	"fingerprint",
	"certificate",
	"connection_string",
	"database_url",
	// PII fields
	"email",
	"phone",
	"phone_number",
	"address",
	"street",
	"city",
	"zip",
	"postal_code",
}

var (
	sensitiveFieldsMapOnce sync.Once
	sensitiveFieldsMap     map[string]bool
)

// DefaultSensitiveFields returns a copy of the default sensitive field names.
// The returned slice is a clone — callers cannot mutate shared state.
func DefaultSensitiveFields() []string {
	clone := make([]string, len(defaultSensitiveFields))
	copy(clone, defaultSensitiveFields)

	return clone
}

// ensureSensitiveFieldsMap returns the internal map directly (no clone).
// For internal use only where we just need read access.
func ensureSensitiveFieldsMap() map[string]bool {
	sensitiveFieldsMapOnce.Do(func() {
		sensitiveFieldsMap = make(map[string]bool, len(defaultSensitiveFields))
		for _, field := range defaultSensitiveFields {
			sensitiveFieldsMap[field] = true
		}
	})

	return sensitiveFieldsMap
}

// DefaultSensitiveFieldsMap provides a map version of DefaultSensitiveFields
// for lookup operations. All field names are lowercase for
// case-insensitive matching. The underlying cache is initialized only once;
// each call returns a shallow clone so callers cannot mutate shared state.
func DefaultSensitiveFieldsMap() map[string]bool {
	m := ensureSensitiveFieldsMap()
	clone := make(map[string]bool, len(m))
	maps.Copy(clone, m)

	return clone
}

// shortSensitiveTokens contains tokens that are too short or generic for
// substring matching and require exact token matching instead.
var shortSensitiveTokens = map[string]bool{
	"key":  true,
	"auth": true,
	"pin":  true,
	"otp":  true,
	"cvv":  true,
	"cvc":  true,
	"ssn":  true,
	"pan":  true,
	"bic":  true,
	"bsb":  true,
	"dob":  true,
	"tin":  true,
	"jwt":  true,
	"zip":  true,
	"city": true,
}

// tokenSplitRegex splits field names by non-alphanumeric characters.
var tokenSplitRegex = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// normalizeFieldName converts camelCase and PascalCase field names into
// underscore-delimited lowercase tokens. For example, "sessionToken" becomes
// "session_token" and "APIKey" becomes "api_key". This ensures that sensitive
// field detection works for camelCase naming conventions.
func normalizeFieldName(fieldName string) string {
	var b strings.Builder

	runes := []rune(fieldName)

	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]

			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}

			if unicode.IsUpper(r) &&
				(unicode.IsLower(prev) || unicode.IsDigit(prev) ||
					(unicode.IsUpper(prev) && next != 0 && unicode.IsLower(next))) {
				b.WriteByte('_')
			}
		}

		b.WriteRune(r)
	}

	return strings.ToLower(b.String())
}

// IsSensitiveField checks if a field name is considered sensitive based on
// the default sensitive fields list. The check is case-insensitive and handles
// camelCase field names by normalizing them to underscore-delimited tokens.
// Short tokens (like "key", "auth") use exact token matching to avoid false
// positives, while longer patterns use word-boundary matching.
func IsSensitiveField(fieldName string) bool {
	m := ensureSensitiveFieldsMap()
	lowerField := strings.ToLower(fieldName)

	// Check exact match with lowercase
	if m[lowerField] {
		return true
	}

	// Also check with camelCase normalization (e.g., "sessionToken" -> "session_token")
	normalized := normalizeFieldName(fieldName)
	if normalized != lowerField && m[normalized] {
		return true
	}

	// Merge tokens from both representations for token matching
	tokens := tokenSplitRegex.Split(normalized, -1)

	for _, sensitive := range defaultSensitiveFields {
		if shortSensitiveTokens[sensitive] {
			if slices.Contains(tokens, sensitive) {
				return true
			}
		} else {
			if matchesWordBoundary(normalized, sensitive) {
				return true
			}

			if normalized != lowerField && matchesWordBoundary(lowerField, sensitive) {
				return true
			}
		}
	}

	return false
}

// matchesWordBoundary checks if the pattern appears in the field with word boundaries.
// A word boundary is either the start/end of string or a non-alphanumeric character.
func matchesWordBoundary(field, pattern string) bool {
	if len(pattern) == 0 {
		return false
	}

	idx := strings.Index(field, pattern)
	if idx == -1 {
		return false
	}

	for idx != -1 {
		start := idx
		end := idx + len(pattern)

		startOk := start == 0 || !isAlphanumeric(field[start-1])
		endOk := end == len(field) || !isAlphanumeric(field[end])

		if startOk && endOk {
			return true
		}

		if end >= len(field) {
			break
		}

		nextIdx := strings.Index(field[end:], pattern)
		if nextIdx == -1 {
			break
		}

		idx = end + nextIdx
	}

	return false
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
