package schema

// Meta carries metadata about a generated dictionary.
type Meta struct {
	SchemaVersion string `json:"schema_version"`
	GeneratedAt   string `json:"generated_at,omitempty"`
	Target        string `json:"target,omitempty"`
}

// Dictionary is the canonical telemetry inventory output.
type Dictionary struct {
	Meta       Meta                       `json:"_meta"`
	Counters   []CounterPrimitive         `json:"counters"`
	Histograms []HistogramPrimitive       `json:"histograms"`
	Gauges     []GaugePrimitive           `json:"gauges"`
	Spans      []SpanPrimitive            `json:"spans"`
	LogFields  []LogFieldPrimitive        `json:"log_fields"`
	Frameworks []FrameworkInstrumentation `json:"frameworks"`
	CrossCut   []CrossCutFinding          `json:"cross_cutting"`
}
