package orchestrator

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis"
)

func mergeResults(dict *schema.Dictionary, results map[string]map[*analysis.Analyzer]any, target string) {
	for _, byAnalyzer := range results {
		if v, ok := byAnalyzer[analyzers.CounterAnalyzer].(*analyzers.CounterFindings); ok {
			dict.Counters = append(dict.Counters, v.Counters...)
		}

		if v, ok := byAnalyzer[analyzers.HistogramAnalyzer].(*analyzers.HistogramFindings); ok {
			dict.Histograms = append(dict.Histograms, v.Histograms...)
		}

		if v, ok := byAnalyzer[analyzers.GaugeAnalyzer].(*analyzers.GaugeFindings); ok {
			dict.Gauges = append(dict.Gauges, v.Gauges...)
		}

		if v, ok := byAnalyzer[analyzers.SpanAnalyzer].(*analyzers.SpanFindings); ok {
			dict.Spans = append(dict.Spans, v.Spans...)
		}

		if v, ok := byAnalyzer[analyzers.LogFieldAnalyzer].(*analyzers.LogFieldFindings); ok {
			dict.LogFields = append(dict.LogFields, v.Fields...)
		}

		if v, ok := byAnalyzer[analyzers.FrameworkAnalyzer].(*analyzers.FrameworkFindings); ok {
			dict.Frameworks = append(dict.Frameworks, v.Frameworks...)
		}

		if v, ok := byAnalyzer[analyzers.CrossCutAnalyzer].(*analyzers.CrossCutFindings); ok {
			dict.CrossCut = append(dict.CrossCut, v.Issues...)
		}
	}

	normalizeAndMerge(dict, target)
}

func normalizeAndMerge(dict *schema.Dictionary, target string) {
	for i := range dict.Counters {
		dict.Counters[i].EmissionSites = normalizeSites(dict.Counters[i].EmissionSites, target)
	}

	for i := range dict.Histograms {
		dict.Histograms[i].EmissionSites = normalizeSites(dict.Histograms[i].EmissionSites, target)
	}

	for i := range dict.Gauges {
		dict.Gauges[i].EmissionSites = normalizeSites(dict.Gauges[i].EmissionSites, target)
	}

	for i := range dict.Spans {
		dict.Spans[i].EmissionSites = normalizeSites(dict.Spans[i].EmissionSites, target)
	}

	for i := range dict.LogFields {
		dict.LogFields[i].EmissionSites = normalizeSites(dict.LogFields[i].EmissionSites, target)
	}

	for i := range dict.Frameworks {
		dict.Frameworks[i].EmissionSites = normalizeSites(dict.Frameworks[i].EmissionSites, target)
	}

	for i := range dict.CrossCut {
		dict.CrossCut[i].Site = normalizeSite(dict.CrossCut[i].Site, target)
	}

	dict.Counters = mergeCounters(dict.Counters)
	dict.Histograms = mergeHistograms(dict.Histograms)
	dict.Gauges = mergeGauges(dict.Gauges)
	dict.Spans = mergeSpans(dict.Spans)
	dict.LogFields = mergeLogFields(dict.LogFields)

	dict.Frameworks = mergeFrameworks(dict.Frameworks)
	if dict.CrossCut == nil {
		dict.CrossCut = []schema.CrossCutFinding{}
	}

	sortCrossCut(dict.CrossCut)
}

func normalizeSites(sites []schema.EmissionSite, target string) []schema.EmissionSite {
	out := make([]schema.EmissionSite, 0, len(sites))
	seen := map[schema.EmissionSite]bool{}

	for _, site := range sites {
		normalized := normalizeSite(site, target)
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}

	sortSites(out)

	return out
}

func normalizeSite(site schema.EmissionSite, target string) schema.EmissionSite {
	if rel, err := filepath.Rel(target, site.File); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
		site.File = rel
	}

	site.File = filepath.ToSlash(site.File)

	return site
}

func sortSites(sites []schema.EmissionSite) {
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}

		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}

		return sites[i].Tier < sites[j].Tier
	})
}

func mergeCounters(values []schema.CounterPrimitive) []schema.CounterPrimitive {
	byName := map[string]*schema.CounterPrimitive{}
	for _, v := range values {
		p := byName[v.Name]
		if p == nil {
			clone := v
			byName[v.Name] = &clone

			continue
		}

		p.EmissionSites = append(p.EmissionSites, v.EmissionSites...)
		p.Labels = mergeStringSet(p.Labels, v.Labels)
		p.TenantScoped = p.TenantScoped || v.TenantScoped
	}

	out := make([]schema.CounterPrimitive, 0, len(byName))
	for _, v := range byName {
		v.EmissionSites = normalizeSites(v.EmissionSites, ".")
		v.LabelCardinality = len(v.Labels)
		out = append(out, *v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func mergeHistograms(values []schema.HistogramPrimitive) []schema.HistogramPrimitive {
	byName := map[string]*schema.HistogramPrimitive{}
	for _, v := range values {
		p := byName[v.Name]
		if p == nil {
			clone := v
			byName[v.Name] = &clone

			continue
		}

		p.EmissionSites = append(p.EmissionSites, v.EmissionSites...)
		p.Labels = mergeStringSet(p.Labels, v.Labels)
		p.TenantScoped = p.TenantScoped || v.TenantScoped
	}

	out := make([]schema.HistogramPrimitive, 0, len(byName))
	for _, v := range byName {
		v.EmissionSites = normalizeSites(v.EmissionSites, ".")
		v.LabelCardinality = len(v.Labels)
		out = append(out, *v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func mergeGauges(values []schema.GaugePrimitive) []schema.GaugePrimitive {
	byName := map[string]*schema.GaugePrimitive{}
	for _, v := range values {
		p := byName[v.Name]
		if p == nil {
			clone := v
			byName[v.Name] = &clone

			continue
		}

		p.EmissionSites = append(p.EmissionSites, v.EmissionSites...)
		p.Labels = mergeStringSet(p.Labels, v.Labels)
		p.TenantScoped = p.TenantScoped || v.TenantScoped
	}

	out := make([]schema.GaugePrimitive, 0, len(byName))
	for _, v := range byName {
		v.EmissionSites = normalizeSites(v.EmissionSites, ".")
		v.LabelCardinality = len(v.Labels)
		out = append(out, *v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func mergeSpans(values []schema.SpanPrimitive) []schema.SpanPrimitive {
	byName := map[string]*schema.SpanPrimitive{}
	for _, v := range values {
		p := byName[v.Name]
		if p == nil {
			clone := v
			byName[v.Name] = &clone

			continue
		}

		p.EmissionSites = append(p.EmissionSites, v.EmissionSites...)
		p.Attributes = mergeStringSet(p.Attributes, v.Attributes)
		p.UnboundedSpan = p.UnboundedSpan || v.UnboundedSpan
		p.RecordOnError = p.RecordOnError || v.RecordOnError
		p.StatusOnError = p.StatusOnError || v.StatusOnError
	}

	out := make([]schema.SpanPrimitive, 0, len(byName))
	for _, v := range byName {
		v.EmissionSites = normalizeSites(v.EmissionSites, ".")
		out = append(out, *v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func mergeLogFields(values []schema.LogFieldPrimitive) []schema.LogFieldPrimitive {
	byName := map[string]*schema.LogFieldPrimitive{}
	for _, v := range values {
		p := byName[v.Name]
		if p == nil {
			clone := v
			if clone.LevelDistribution == nil {
				clone.LevelDistribution = map[string]int{}
			}

			byName[v.Name] = &clone

			continue
		}

		p.EmissionSites = append(p.EmissionSites, v.EmissionSites...)
		for level, count := range v.LevelDistribution {
			p.LevelDistribution[level] += count
		}

		p.PIIRiskFlag = p.PIIRiskFlag || v.PIIRiskFlag
	}

	out := make([]schema.LogFieldPrimitive, 0, len(byName))
	for _, v := range byName {
		v.EmissionSites = normalizeSites(v.EmissionSites, ".")
		out = append(out, *v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func mergeFrameworks(values []schema.FrameworkInstrumentation) []schema.FrameworkInstrumentation {
	byName := map[string]*schema.FrameworkInstrumentation{}
	for _, v := range values {
		p := byName[v.Framework]
		if p == nil {
			clone := v
			byName[v.Framework] = &clone

			continue
		}

		p.EmissionSites = append(p.EmissionSites, v.EmissionSites...)
		p.AutoMetrics = mergeStringSet(p.AutoMetrics, v.AutoMetrics)
		p.AutoSpans = mergeStringSet(p.AutoSpans, v.AutoSpans)
	}

	out := make([]schema.FrameworkInstrumentation, 0, len(byName))
	for _, v := range byName {
		v.EmissionSites = normalizeSites(v.EmissionSites, ".")
		out = append(out, *v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Framework < out[j].Framework })

	return out
}

func sortCrossCut(values []schema.CrossCutFinding) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Kind != values[j].Kind {
			return values[i].Kind < values[j].Kind
		}

		if values[i].Site.File != values[j].Site.File {
			return values[i].Site.File < values[j].Site.File
		}

		return values[i].Site.Line < values[j].Site.Line
	})
}

func mergeStringSet(a, b []string) []string {
	seen := map[string]bool{}

	for _, v := range a {
		if v != "" {
			seen[v] = true
		}
	}

	for _, v := range b {
		if v != "" {
			seen[v] = true
		}
	}

	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}
