package schema

// EmissionSite is a file/line reference to where a primitive is constructed or recorded.
type EmissionSite struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Tier int    `json:"tier,omitempty"`
}

// CounterPrimitive describes one observed counter metric.
type CounterPrimitive struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Unit             string         `json:"unit,omitempty"`
	InstrumentType   string         `json:"instrument_type"`
	EmissionSites    []EmissionSite `json:"emission_sites"`
	Labels           []string       `json:"labels"`
	LabelCardinality int            `json:"label_cardinality_estimate,omitempty"`
	TenantScoped     bool           `json:"tenant_scoped,omitempty"`
}

// HistogramPrimitive describes one observed histogram metric.
type HistogramPrimitive struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Unit             string         `json:"unit,omitempty"`
	InstrumentType   string         `json:"instrument_type"`
	EmissionSites    []EmissionSite `json:"emission_sites"`
	Labels           []string       `json:"labels"`
	LabelCardinality int            `json:"label_cardinality_estimate,omitempty"`
	Buckets          []float64      `json:"buckets,omitempty"`
	TenantScoped     bool           `json:"tenant_scoped,omitempty"`
}

// GaugePrimitive describes one observed gauge metric.
type GaugePrimitive struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Unit             string         `json:"unit,omitempty"`
	InstrumentType   string         `json:"instrument_type"`
	EmissionSites    []EmissionSite `json:"emission_sites"`
	Labels           []string       `json:"labels"`
	LabelCardinality int            `json:"label_cardinality_estimate,omitempty"`
	TenantScoped     bool           `json:"tenant_scoped,omitempty"`
}

// SpanPrimitive describes one observed tracer.Start invocation.
type SpanPrimitive struct {
	Name          string         `json:"name"`
	EmissionSites []EmissionSite `json:"emission_sites"`
	Attributes    []string       `json:"attributes"`
	UnboundedSpan bool           `json:"unbounded_span,omitempty"`
	StatusOnError bool           `json:"status_on_error_observed"`
	RecordOnError bool           `json:"record_on_error_observed"`
}

// LogFieldPrimitive describes one structured log field observed in code.
type LogFieldPrimitive struct {
	Name              string         `json:"name"`
	EmissionSites     []EmissionSite `json:"emission_sites"`
	LevelDistribution map[string]int `json:"level_distribution"`
	PIIRiskFlag       bool           `json:"pii_risk_flag,omitempty"`
}

// FrameworkInstrumentation describes a detected auto-instrumentation integration.
type FrameworkInstrumentation struct {
	Framework     string         `json:"framework"`
	EmissionSites []EmissionSite `json:"emission_sites"`
	AutoMetrics   []string       `json:"auto_metrics,omitempty"`
	AutoSpans     []string       `json:"auto_spans,omitempty"`
}

// CrossCutFinding is a per-site cross-cutting consistency issue.
type CrossCutFinding struct {
	Kind     string       `json:"kind"`
	Site     EmissionSite `json:"site"`
	Function string       `json:"function"`
	Detail   string       `json:"detail"`
}
