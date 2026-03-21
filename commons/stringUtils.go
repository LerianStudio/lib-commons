package commons

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var uuidPattern = regexp.MustCompile(`[0-9a-fA-F-]{36}`)

// RemoveAccents removes accents of a given word and returns it
func RemoveAccents(word string) (string, error) {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

	s, _, err := transform.String(t, word)
	if err != nil {
		return "", err
	}

	return s, nil
}

// RemoveSpaces removes spaces of a given word and returns it
func RemoveSpaces(word string) string {
	rr := make([]rune, 0, len(word))

	for _, r := range word {
		if !unicode.IsSpace(r) {
			rr = append(rr, r)
		}
	}

	return string(rr)
}

// IsNilOrEmpty returns a boolean indicating if a *string is nil or empty.
// It's use TrimSpace so, a string "  " and "" and "null" and "nil" will be considered empty
func IsNilOrEmpty(s *string) bool {
	return s == nil || strings.TrimSpace(*s) == "" || strings.TrimSpace(*s) == "null" || strings.TrimSpace(*s) == "nil"
}

// CamelToSnakeCase converts a given camelCase string to snake_case format.
func CamelToSnakeCase(str string) string {
	var buffer bytes.Buffer

	for i, character := range str {
		if unicode.IsUpper(character) {
			if i > 0 {
				buffer.WriteString("_")
			}

			buffer.WriteRune(unicode.ToLower(character))
		} else {
			buffer.WriteRune(character)
		}
	}

	return buffer.String()
}

// RegexIgnoreAccents receives a regex, then, for each char it's adds the accents variations to expression
// Ex: Given "a" -> "aáàãâ"
// Ex: Given "c" -> "ç"
func RegexIgnoreAccents(regex string) string {
	m1 := map[string]string{
		"a": "[aáàãâ]",
		"e": "[eéèê]",
		"i": "[iíìî]",
		"o": "[oóòõô]",
		"u": "[uùúû]",
		"c": "[cç]",
		"A": "[AÁÀÃÂ]",
		"E": "[EÉÈÊ]",
		"I": "[IÍÌÎ]",
		"O": "[OÓÒÕÔ]",
		"U": "[UÙÚÛ]",
		"C": "[CÇ]",
	}
	m2 := map[string]string{
		"a": "a",
		"á": "a",
		"à": "a",
		"ã": "a",
		"â": "a",
		"e": "e",
		"é": "e",
		"è": "e",
		"ê": "e",
		"i": "i",
		"í": "i",
		"ì": "i",
		"î": "i",
		"o": "o",
		"ó": "o",
		"ò": "o",
		"õ": "o",
		"ô": "o",
		"u": "u",
		"ù": "u",
		"ú": "u",
		"û": "u",
		"c": "c",
		"ç": "c",
		"A": "A",
		"Á": "A",
		"À": "A",
		"Ã": "A",
		"Â": "A",
		"E": "E",
		"É": "E",
		"È": "E",
		"Ê": "E",
		"I": "I",
		"Í": "I",
		"Ì": "I",
		"Î": "I",
		"O": "O",
		"Ó": "O",
		"Ò": "O",
		"Õ": "O",
		"Ô": "O",
		"U": "U",
		"Ù": "U",
		"Ú": "U",
		"Û": "U",
		"C": "C",
		"Ç": "C",
	}

	var b strings.Builder
	b.Grow(len(regex) * 2) // Pre-allocate: rough estimate, builder will grow if needed

	for _, ch := range regex {
		c := string(ch)
		if v1, found := m2[c]; found {
			if v2, found2 := m1[v1]; found2 {
				b.WriteString(v2)
				continue
			}
		}

		b.WriteRune(ch)
	}

	return b.String()
}

// RemoveChars from a string
func RemoveChars(str string, chars map[string]bool) string {
	var b strings.Builder

	for _, ch := range str {
		c := string(ch)
		if _, found := chars[c]; found {
			continue
		}

		b.WriteRune(ch)
	}

	return b.String()
}

// ReplaceUUIDWithPlaceholder replaces UUIDs with a placeholder in a given path string.
func ReplaceUUIDWithPlaceholder(path string) string {
	return uuidPattern.ReplaceAllString(path, ":id")
}

// ValidateServerAddress checks if the value is a valid host:port address.
// It accepts IPv4 ("host:port"), IPv6 ("[::1]:port"), and hostname forms.
// The port must be numeric and in the range [1, 65535].
// Returns the original value when valid, or "" when invalid.
func ValidateServerAddress(value string) string {
	host, portStr, err := net.SplitHostPort(value)
	if err != nil {
		return ""
	}

	if host == "" {
		return ""
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return ""
	}

	return value
}

// HashSHA256 generate a hash sha-256 to create idempotency on redis
func HashSHA256(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// StringToInt converts a string to an int, returning 100 on failure.
//
// Deprecated: Use StringToIntOrDefault for explicit default values.
func StringToInt(s string) int {
	return StringToIntOrDefault(s, 100)
}

// StringToIntOrDefault converts a string to an int, returning defaultVal on parse failure.
func StringToIntOrDefault(s string, defaultVal int) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}

	return i
}
