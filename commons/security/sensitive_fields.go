package security

import (
	"regexp"
	"strings"
	"sync"
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
	"private_key",
	"privatekey",
	"clientid",
	"client_id",
	"clientsecret",
	"client_secret",
}

var (
	sensitiveFieldsMapOnce sync.Once
	sensitiveFieldsMap     map[string]bool
)

func DefaultSensitiveFields() []string {
	return defaultSensitiveFields
}

// DefaultSensitiveFieldsMap provides a cached map version of DefaultSensitiveFields
// for efficient lookup operations. All field names are lowercase for
// case-insensitive matching. The map is initialized only once.
func DefaultSensitiveFieldsMap() map[string]bool {
	sensitiveFieldsMapOnce.Do(func() {
		sensitiveFieldsMap = make(map[string]bool, len(defaultSensitiveFields))
		for _, field := range defaultSensitiveFields {
			sensitiveFieldsMap[field] = true
		}
	})

	return sensitiveFieldsMap
}

// shortSensitiveTokens contains tokens that are too short or generic for
// substring matching and require exact token matching instead.
var shortSensitiveTokens = map[string]bool{
	"key":  true,
	"auth": true,
}

// tokenSplitRegex splits field names by non-alphanumeric characters.
var tokenSplitRegex = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// IsSensitiveField checks if a field name is considered sensitive based on
// the default sensitive fields list. The check is case-insensitive.
// Short tokens (like "key", "auth") use exact token matching to avoid false
// positives, while longer patterns use word-boundary matching.
func IsSensitiveField(fieldName string) bool {
	lowerField := strings.ToLower(fieldName)

	if DefaultSensitiveFieldsMap()[lowerField] {
		return true
	}

	tokens := tokenSplitRegex.Split(lowerField, -1)

	for _, sensitive := range defaultSensitiveFields {
		if shortSensitiveTokens[sensitive] {
			for _, token := range tokens {
				if token == sensitive {
					return true
				}
			}
		} else {
			if matchesWordBoundary(lowerField, sensitive) {
				return true
			}
		}
	}

	return false
}

// matchesWordBoundary checks if the pattern appears in the field with word boundaries.
// A word boundary is either the start/end of string or a non-alphanumeric character.
func matchesWordBoundary(field, pattern string) bool {
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
