package outbox

import (
	"regexp"
	"strings"
)

// sanitizeErrorForStorage redacts sensitive values and enforces bounded length
// before storing error messages in the last_error database column (CWE-209).
const maxErrorLength = 512

const errorTruncatedSuffix = "... (truncated)"

const redactedValue = "[REDACTED]"

type sensitiveDataPattern struct {
	pattern     *regexp.Regexp
	replacement string
}

var sensitiveDataPatterns = []sensitiveDataPattern{
	{
		pattern:     regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^:\s/]+):([^@\s]+)@`),
		replacement: `$1:` + redactedValue + `@`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9\-._~+/]+=*\b`),
		replacement: "Bearer " + redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(authorization\s*:\s*basic\s+)[a-z0-9+/=]+`),
		replacement: `$1` + redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b`),
		replacement: redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\b(api[-_ ]?key|access[-_ ]?token|refresh[-_ ]?token|password|secret)\s*[:=]\s*([^\s,;]+)`),
		replacement: `$1=` + redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)([?&](?:password|pass|pwd|token|api[_-]?key|access[_-]?token|refresh[_-]?token)=)([^&\s]+)`),
		replacement: `$1` + redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`),
		replacement: redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\b(aws[_-]?secret[_-]?access[_-]?key|gcp[_-]?credentials|private[_-]?key|client[_-]?secret)\s*[:=]\s*([^\s,;]+)`),
		replacement: `$1=` + redactedValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`),
		replacement: redactedValue,
	},
}

var longNumericTokenPattern = regexp.MustCompile(`\b\d{12,19}\b`)

func sanitizeErrorForStorage(err error) string {
	if err == nil {
		return ""
	}

	return SanitizeErrorMessageForStorage(err.Error())
}

// SanitizeErrorMessageForStorage redacts sensitive values and enforces a bounded length.
func SanitizeErrorMessageForStorage(msg string) string {
	redacted := redactSensitiveData(strings.TrimSpace(msg))

	return truncateError(redacted, maxErrorLength, errorTruncatedSuffix)
}

func redactSensitiveData(msg string) string {
	redacted := msg

	for _, matcher := range sensitiveDataPatterns {
		redacted = matcher.pattern.ReplaceAllString(redacted, matcher.replacement)
	}

	redacted = redactLuhnNumberSequences(redacted)

	return redacted
}

func redactLuhnNumberSequences(msg string) string {
	return longNumericTokenPattern.ReplaceAllStringFunc(msg, func(candidate string) string {
		if !passesLuhn(candidate) {
			return candidate
		}

		return redactedValue
	})
}

func passesLuhn(number string) bool {
	if len(number) < 12 || len(number) > 19 {
		return false
	}

	sum := 0
	shouldDouble := false

	for i := len(number) - 1; i >= 0; i-- {
		digit := int(number[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}

		if shouldDouble {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}

		sum += digit
		shouldDouble = !shouldDouble
	}

	return sum%10 == 0
}

func truncateError(msg string, maxRunes int, suffix string) string {
	runes := []rune(msg)
	if len(runes) <= maxRunes {
		return msg
	}

	suffixRunes := []rune(suffix)
	if maxRunes <= len(suffixRunes) {
		return string(runes[:maxRunes])
	}

	trimmed := string(runes[:maxRunes-len(suffixRunes)])

	return trimmed + suffix
}
