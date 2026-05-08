package renderer

import (
	"regexp"
	"sort"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

var trailingWhitespace = regexp.MustCompile(`[ \t]+\n`)

const (
	yamlDescription      = "description"
	yamlEmissionSites    = "emission_sites"
	yamlInstrumentType   = "instrument_type"
	yamlLabelCardinality = "label_cardinality_estimate"
	yamlLabels           = "labels"
	yamlTenantScoped     = "tenant_scoped"
	yamlUnit             = "unit"
)

// Render returns a deterministic Markdown representation of d.
func Render(d *schema.Dictionary) string {
	if d == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Telemetry Dictionary\n\n")
	sb.WriteString("## _meta\n\n")
	sb.WriteString("```yaml\n")
	writeYAMLBlock(&sb, 0, map[string]any{
		"generated_at":   d.Meta.GeneratedAt,
		"schema_version": d.Meta.SchemaVersion,
		"target":         d.Meta.Target,
		"tool":           "telemetry-inventory",
	})
	sb.WriteString("```\n\n")

	writeCounters(&sb, d.Counters)
	writeHistograms(&sb, d.Histograms)
	writeGauges(&sb, d.Gauges)
	writeSpans(&sb, d.Spans)
	writeLogFields(&sb, d.LogFields)
	writeFrameworks(&sb, d.Frameworks)
	writeCrossCut(&sb, d.CrossCut)

	out := trailingWhitespace.ReplaceAllString(sb.String(), "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}

	return out
}

func writeCounters(sb *strings.Builder, values []schema.CounterPrimitive) {
	sorted := append([]schema.CounterPrimitive(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	writeSection(sb, "Counters", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Name, map[string]any{
			yamlDescription:      value.Description,
			yamlEmissionSites:    value.EmissionSites,
			yamlInstrumentType:   value.InstrumentType,
			yamlLabelCardinality: value.LabelCardinality,
			yamlLabels:           value.Labels,
			yamlTenantScoped:     value.TenantScoped,
			yamlUnit:             value.Unit,
		})
	}
}

func writeHistograms(sb *strings.Builder, values []schema.HistogramPrimitive) {
	sorted := append([]schema.HistogramPrimitive(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	writeSection(sb, "Histograms", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Name, map[string]any{
			"buckets":            value.Buckets,
			yamlDescription:      value.Description,
			yamlEmissionSites:    value.EmissionSites,
			yamlInstrumentType:   value.InstrumentType,
			yamlLabelCardinality: value.LabelCardinality,
			yamlLabels:           value.Labels,
			yamlTenantScoped:     value.TenantScoped,
			yamlUnit:             value.Unit,
		})
	}
}

func writeGauges(sb *strings.Builder, values []schema.GaugePrimitive) {
	sorted := append([]schema.GaugePrimitive(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	writeSection(sb, "Gauges", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Name, map[string]any{
			yamlDescription:      value.Description,
			yamlEmissionSites:    value.EmissionSites,
			yamlInstrumentType:   value.InstrumentType,
			yamlLabelCardinality: value.LabelCardinality,
			yamlLabels:           value.Labels,
			yamlTenantScoped:     value.TenantScoped,
			yamlUnit:             value.Unit,
		})
	}
}

func writeSpans(sb *strings.Builder, values []schema.SpanPrimitive) {
	sorted := append([]schema.SpanPrimitive(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	writeSection(sb, "Spans", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Name, map[string]any{
			"attributes":               value.Attributes,
			yamlEmissionSites:          value.EmissionSites,
			"record_on_error_observed": value.RecordOnError,
			"status_on_error_observed": value.StatusOnError,
			"unbounded_span":           value.UnboundedSpan,
		})
	}
}

func writeLogFields(sb *strings.Builder, values []schema.LogFieldPrimitive) {
	sorted := append([]schema.LogFieldPrimitive(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	writeSection(sb, "Log Fields", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Name, map[string]any{
			yamlEmissionSites:    value.EmissionSites,
			"level_distribution": value.LevelDistribution,
			"pii_risk_flag":      value.PIIRiskFlag,
		})
	}
}

func writeFrameworks(sb *strings.Builder, values []schema.FrameworkInstrumentation) {
	sorted := append([]schema.FrameworkInstrumentation(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Framework < sorted[j].Framework })
	writeSection(sb, "Frameworks", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Framework, map[string]any{
			"auto_metrics":    value.AutoMetrics,
			"auto_spans":      value.AutoSpans,
			yamlEmissionSites: value.EmissionSites,
		})
	}
}

func writeCrossCut(sb *strings.Builder, values []schema.CrossCutFinding) {
	sorted := append([]schema.CrossCutFinding(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}

		if sorted[i].Site.File != sorted[j].Site.File {
			return sorted[i].Site.File < sorted[j].Site.File
		}

		return sorted[i].Site.Line < sorted[j].Site.Line
	})
	writeSection(sb, "Cross-Cutting", len(sorted))

	for _, value := range sorted {
		writePrimitive(sb, value.Kind+" at "+value.Site.File, map[string]any{
			"detail":   value.Detail,
			"function": value.Function,
			"kind":     value.Kind,
			"site":     value.Site,
		})
	}
}

func writeSection(sb *strings.Builder, name string, count int) {
	sb.WriteString("## ")
	sb.WriteString(name)
	sb.WriteString("\n\n")

	if count == 0 {
		sb.WriteString("_None detected._\n\n")
	}
}

func writePrimitive(sb *strings.Builder, name string, fields map[string]any) {
	sb.WriteString("### ")
	sb.WriteString(name)
	sb.WriteString("\n\n")
	sb.WriteString("```yaml\n")
	writeYAMLBlock(sb, 0, fields)
	sb.WriteString("```\n\n")
}
