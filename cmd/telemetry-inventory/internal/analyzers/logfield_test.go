//go:build unit

package analyzers_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestLogFieldAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.LogFieldAnalyzer, "logfield")
}

// TestLogFieldAnalyzer_IsPIIField pins the canonical PII keys that flip
// PIIRiskFlag. analyzers.IsPIIField delegates to
// commons/security.IsSensitiveField — the same predicate used by the
// production redactor — so the audit tool and runtime sanitizer share a
// single source of truth. The cases below cover the camelCase/snake_case
// shapes the prior regex missed (accessToken, refresh_token, clientSecret,
// cardNumber, cvv, dob, mfa_code) to lock in the wider coverage.
func TestLogFieldAnalyzer_IsPIIField(t *testing.T) {
	cases := []struct {
		key        string
		shouldFlag bool
	}{
		// Snake-case classics
		{"password", true},
		{"token", true},
		{"secret", true},
		{"email", true},
		{"ssn", true},
		{"refresh_token", true},
		{"mfa_code", true},
		// camelCase / PascalCase — the prior regex caught these only by luck;
		// security.IsSensitiveField normalizes them.
		{"PasswordHash", true},
		{"accessToken", true},
		{"clientSecret", true},
		{"cardNumber", true},
		{"cvv", true},
		{"dob", true},
		// Safe (non-sensitive) fields
		{"tenant_id", false},
		{"trace_id", false},
		{"balance", false},
		{"request_id", false},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := analyzers.IsPIIField(tc.key)
			if got != tc.shouldFlag {
				t.Fatalf("analyzers.IsPIIField(%q) = %t, want %t", tc.key, got, tc.shouldFlag)
			}
		})
	}
}
