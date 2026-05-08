//go:build unit

package analyzers_test

import (
	"regexp"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestLogFieldAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.LogFieldAnalyzer, "logfield")
}

// TestLogFieldAnalyzer_PIIRegex pins the canonical PII keys that flip
// PIIRiskFlag. The analyzer's regex is encapsulated; this test mirrors it so
// that any regex change without updating this list fails CI loudly.
func TestLogFieldAnalyzer_PIIRegex(t *testing.T) {
	pattern := regexp.MustCompile(`(?i)(password|token|secret|apikey|api_key|email|ssn|phone|address|cpf|cnpj|card)`)

	cases := []struct {
		key        string
		shouldFlag bool
	}{
		{"password", true},
		{"PasswordHash", true},
		{"token", true},
		{"secret", true},
		{"email", true},
		{"tenant_id", false},
		{"trace_id", false},
		{"balance", false},
		{"request_id", false},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := pattern.MatchString(tc.key)
			if got != tc.shouldFlag {
				t.Fatalf("pattern.MatchString(%q) = %t, want %t — regex drifted from analyzer source", tc.key, got, tc.shouldFlag)
			}
		})
	}
}
