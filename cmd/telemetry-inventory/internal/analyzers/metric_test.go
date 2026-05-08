//go:build unit

package analyzers_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestHistogramAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.HistogramAnalyzer, "histogram")
}

func TestGaugeAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzers.GaugeAnalyzer, "gauge")
}
