package analyzers

import (
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	libmetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// CounterAnalyzer detects counter primitives across all three telemetry tiers.
var CounterAnalyzer = &analysis.Analyzer{
	Name:       "counter",
	Doc:        "Detects counter metric primitives across Tier 1, 2, and 3.",
	Run:        runCounter,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeFor[*CounterFindings](),
}

func runCounter(pass *analysis.Pass) (any, error) {
	metrics := collectMetricFindings(pass, metricKindConfig{
		name:           instrumentCounter,
		instrumentType: instrumentCounter,
		tier1Constructors: map[string]bool{
			"Int64Counter":   true,
			"Float64Counter": true,
		},
		tier2Method: "Counter",
		helperType:  libmetrics.Counter,
		recordMethods: map[string]bool{
			"Add":    true,
			"AddOne": true,
		},
	})

	out := &CounterFindings{Counters: make([]schema.CounterPrimitive, 0, len(metrics))}
	for _, m := range metrics {
		out.Counters = append(out.Counters, schema.CounterPrimitive{
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
