package analyzers

import (
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	libmetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// HistogramAnalyzer detects histogram primitives across all telemetry tiers.
var HistogramAnalyzer = &analysis.Analyzer{
	Name:       "histogram",
	Doc:        "Detects histogram metric primitives across Tier 1, 2, and 3.",
	Run:        runHistogram,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeFor[*HistogramFindings](),
}

func runHistogram(pass *analysis.Pass) (any, error) {
	metrics := collectMetricFindings(pass, metricKindConfig{
		name:           instrumentHistogram,
		instrumentType: instrumentHistogram,
		tier1Constructors: map[string]bool{
			"Int64Histogram":   true,
			"Float64Histogram": true,
		},
		tier2Method: "Histogram",
		helperType:  libmetrics.Histogram,
		recordMethods: map[string]bool{
			"Record": true,
		},
	})

	out := &HistogramFindings{Histograms: make([]schema.HistogramPrimitive, 0, len(metrics))}
	for _, m := range metrics {
		out.Histograms = append(out.Histograms, schema.HistogramPrimitive{
			Name:             m.Name,
			Description:      m.Description,
			Unit:             m.Unit,
			InstrumentType:   m.InstrumentType,
			EmissionSites:    m.EmissionSites,
			Labels:           m.Labels,
			LabelCardinality: len(m.Labels),
			Buckets:          m.Buckets,
			TenantScoped:     m.TenantScoped,
		})
	}

	return out, nil
}
