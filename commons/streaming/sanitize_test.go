//go:build unit

package streaming

import (
	"strings"
	"testing"
)

// TestSanitizeBrokerURL verifies that credentials embedded in broker URLs,
// connection strings, and raw "password=" / "pass=" key/value pairs are
// stripped before any log output or error surfacing. Pattern mirrors the
// behavior of commons/rabbitmq/rabbitmq.go:129 and :470.
func TestSanitizeBrokerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		in             string
		mustNotContain []string
		mustContainAny []string // at least one of these substrings must be present
	}{
		{
			name:           "sasl url with userinfo password",
			in:             "failed to connect: sasl://user:hunter2@broker.example.com:9092/cluster",
			mustNotContain: []string{"hunter2", "user:hunter2"},
			mustContainAny: []string{"****", "xxxxx", "[REDACTED]"},
		},
		{
			name:           "kafka url with userinfo password",
			in:             "dial failed kafka://admin:supersecret@kafka:9092",
			mustNotContain: []string{"supersecret", "admin:supersecret"},
			mustContainAny: []string{"****", "xxxxx", "[REDACTED]"},
		},
		{
			name:           "password kv pair",
			in:             "client config password=hunter2 host=broker",
			mustNotContain: []string{"hunter2"},
			mustContainAny: []string{"password=****", "password=xxxxx", "password=[REDACTED]"},
		},
		{
			name:           "pass kv pair",
			in:             "config pass=opensesame and other stuff",
			mustNotContain: []string{"opensesame"},
			mustContainAny: []string{"pass=****", "pass=xxxxx", "pass=[REDACTED]"},
		},
		{
			name:           "no credentials to sanitize",
			in:             "ordinary error message with no credentials",
			mustNotContain: []string{},
			mustContainAny: []string{"ordinary error message with no credentials"},
		},
		{
			name:           "empty string",
			in:             "",
			mustNotContain: []string{},
			mustContainAny: []string{""},
		},
		{
			// Regression guard: an "@" inside a non-URL token (e.g. an email
			// address in a log message) must NOT be treated as userinfo
			// delimiter. fallbackRedact's URL-aware splitter only fires on
			// "://" presence, so an email-only message should pass through
			// untouched.
			name:           "email address in plain text is preserved",
			in:             "user message with email@domain.com",
			mustNotContain: []string{"****", "[REDACTED]"},
			mustContainAny: []string{"email@domain.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := sanitizeBrokerURL(tt.in)

			for _, s := range tt.mustNotContain {
				if strings.Contains(out, s) {
					t.Errorf("sanitizeBrokerURL(%q) = %q; must NOT contain %q", tt.in, out, s)
				}
			}

			if len(tt.mustContainAny) > 0 {
				found := false
				for _, s := range tt.mustContainAny {
					if strings.Contains(out, s) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("sanitizeBrokerURL(%q) = %q; expected at least one of %v", tt.in, out, tt.mustContainAny)
				}
			}
		})
	}
}
