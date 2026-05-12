//go:build unit

package analyzers_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestSpanAnalyzer_Balanced(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.SpanAnalyzer, "span-balanced")
}

func TestSpanAnalyzer_Leak(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.SpanAnalyzer, "span-leak")
}
