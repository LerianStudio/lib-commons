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
// PIIRiskFlag. It calls the exported analyzers.IsPIIField predicate so the
// production regex literal at logfield.go has exactly one source of truth —
// no second copy in the test that could drift independently.
func TestLogFieldAnalyzer_IsPIIField(t *testing.T) {
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
			got := analyzers.IsPIIField(tc.key)
			if got != tc.shouldFlag {
				t.Fatalf("analyzers.IsPIIField(%q) = %t, want %t", tc.key, got, tc.shouldFlag)
			}
		})
	}
}
