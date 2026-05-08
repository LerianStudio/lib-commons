package analyzers

import (
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	libmetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// GaugeAnalyzer detects gauge primitives across all telemetry tiers.
var GaugeAnalyzer = &analysis.Analyzer{
	Name:       "gauge",
	Doc:        "Detects gauge metric primitives across Tier 1, 2, and 3.",
	Run:        runGauge,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeFor[*GaugeFindings](),
}

func runGauge(pass *analysis.Pass) (any, error) {
	metrics := collectMetricFindings(pass, metricKindConfig{
		name:           instrumentGauge,
		instrumentType: instrumentGauge,
		tier1Constructors: map[string]bool{
			"Int64Gauge":   true,
			"Float64Gauge": true,
		},
		tier2Method: "Gauge",
		helperType:  libmetrics.Gauge,
		recordMethods: map[string]bool{
			"Record": true,
			"Set":    true,
		},
	})

	out := &GaugeFindings{Gauges: make([]schema.GaugePrimitive, 0, len(metrics))}
	for _, m := range metrics {
		out.Gauges = append(out.Gauges, schema.GaugePrimitive{
			Name:             m.Name,
			Description:      m.Description,
			Unit:             m.Unit,
			InstrumentType:   m.InstrumentType,
			EmissionSites:    m.EmissionSites,
			Labels:           m.Labels,
			LabelCardinality: len(m.Labels),
			TenantScoped:     m.TenantScoped,
		})
	}

	return out, nil
}
