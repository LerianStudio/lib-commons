//go:build unit

package analyzers_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestCounterAnalyzer_Tier1(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.CounterAnalyzer, "counter-tier1")
}

func TestCounterAnalyzer_Tier2(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.CounterAnalyzer, "counter-tier2")
}

func TestCounterAnalyzer_Tier3(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.CounterAnalyzer, "counter-tier3")
}

// TestCounterAnalyzer_Tier3Collision verifies the isMetricsFactory type guard:
// a user type with a method named RecordAccountCreated must NOT be detected as
// a Tier-3 counter. The fixture has zero `// want` directives, so any
// pass.Reportf from the analyzer would fail the test.
func TestCounterAnalyzer_Tier3Collision(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.CounterAnalyzer, "counter-tier3-collision")
}
