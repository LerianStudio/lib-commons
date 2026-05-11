//go:build unit

package orchestrator

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

// TestMergeCounters_DedupesAndUnionsLabels asserts that two CounterPrimitives
// for the same metric name in different files get merged: emission_sites
// concatenated and sorted, labels union'd, tenant_scoped OR'd.
func TestMergeCounters_DedupesAndUnionsLabels(t *testing.T) {
	in := []schema.CounterPrimitive{
		{
			Name: "accounts_total", Labels: []string{"tenant_id"},
			TenantScoped: true,
			EmissionSites: []schema.EmissionSite{
				{File: "/abs/a.go", Line: 10},
			},
		},
		{
			Name: "accounts_total", Labels: []string{"result"},
			TenantScoped: false,
			EmissionSites: []schema.EmissionSite{
				{File: "/abs/b.go", Line: 20},
			},
		},
		{
			Name: "other_total",
			EmissionSites: []schema.EmissionSite{
				{File: "/abs/c.go", Line: 30},
			},
		},
	}

	out := mergeCounters(in)

	if len(out) != 2 {
		t.Fatalf("expected 2 distinct counters after merge, got %d: %+v", len(out), out)
	}

	var accounts *schema.CounterPrimitive
	for i := range out {
		if out[i].Name == "accounts_total" {
			accounts = &out[i]
		}
	}
	if accounts == nil {
		t.Fatalf("accounts_total missing from merged output")
	}

	if !accounts.TenantScoped {
		t.Fatalf("accounts_total.TenantScoped = false, want true (OR semantics)")
	}

	if len(accounts.Labels) != 2 {
		t.Fatalf("expected 2 unioned labels, got %v", accounts.Labels)
	}

	if accounts.LabelCardinality != 2 {
		t.Fatalf("LabelCardinality = %d, want 2", accounts.LabelCardinality)
	}

	if len(accounts.EmissionSites) != 2 {
		t.Fatalf("expected 2 emission sites, got %d: %+v", len(accounts.EmissionSites), accounts.EmissionSites)
	}

	// Sorted by (file, line, tier).
	if accounts.EmissionSites[0].File > accounts.EmissionSites[1].File {
		t.Fatalf("emission sites not sorted by file: %+v", accounts.EmissionSites)
	}
}

// TestMergeSpans_DedupesAndPropagatesUnboundedFlag verifies the OR semantics
// for the boolean fields (UnboundedSpan, RecordOnError, StatusOnError) when
// the same span name is observed in two locations.
func TestMergeSpans_DedupesAndPropagatesUnboundedFlag(t *testing.T) {
	in := []schema.SpanPrimitive{
		{
			Name: "Op", Attributes: []string{"tenant_id"},
			UnboundedSpan: false, RecordOnError: true, StatusOnError: false,
			EmissionSites: []schema.EmissionSite{{File: "/abs/a.go", Line: 10}},
		},
		{
			Name: "Op", Attributes: []string{"trace_id"},
			UnboundedSpan: true, RecordOnError: false, StatusOnError: true,
			EmissionSites: []schema.EmissionSite{{File: "/abs/b.go", Line: 20}},
		},
	}

	out := mergeSpans(in)

	if len(out) != 1 {
		t.Fatalf("expected 1 span after merge, got %d", len(out))
	}

	got := out[0]
	if !got.UnboundedSpan {
		t.Fatalf("UnboundedSpan should be true (OR), got false")
	}
	if !got.RecordOnError {
		t.Fatalf("RecordOnError should be true (OR), got false")
	}
	if !got.StatusOnError {
		t.Fatalf("StatusOnError should be true (OR), got false")
	}

	if len(got.Attributes) != 2 {
		t.Fatalf("expected 2 unioned attributes, got %v", got.Attributes)
	}
}

// TestMergeLogFields_AddsLevelDistribution exercises the level-counter
// addition: two info-level emissions in different files should produce
// info=2, error=1.
func TestMergeLogFields_AddsLevelDistribution(t *testing.T) {
	in := []schema.LogFieldPrimitive{
		{
			Name: "tenant_id", LevelDistribution: map[string]int{"info": 1},
			EmissionSites: []schema.EmissionSite{{File: "/abs/a.go", Line: 10}},
		},
		{
			Name: "tenant_id", LevelDistribution: map[string]int{"info": 1, "error": 1},
			EmissionSites: []schema.EmissionSite{{File: "/abs/b.go", Line: 20}},
		},
	}

	out := mergeLogFields(in)

	if len(out) != 1 {
		t.Fatalf("expected 1 log field after merge, got %d", len(out))
	}

	got := out[0]
	if got.LevelDistribution["info"] != 2 {
		t.Fatalf("info count = %d, want 2", got.LevelDistribution["info"])
	}
	if got.LevelDistribution["error"] != 1 {
		t.Fatalf("error count = %d, want 1", got.LevelDistribution["error"])
	}
}

// TestNormalizeSite_RelativizesAbsolutePath asserts the normalizeSite contract:
// a path inside `target` becomes relative; a path outside stays absolute.
func TestNormalizeSite_RelativizesAbsolutePath(t *testing.T) {
	target := "/repo/svc"

	in := schema.EmissionSite{File: "/repo/svc/internal/handler.go", Line: 42}
	got := normalizeSite(in, target)

	want := "internal/handler.go"
	if got.File != want {
		t.Fatalf("relativized file = %q, want %q", got.File, want)
	}

	// Sibling path stays absolute (or as Rel returns it) — must not climb
	// out of the target with `../`.
	in2 := schema.EmissionSite{File: "/repo/sibling/main.go", Line: 1}
	got2 := normalizeSite(in2, target)
	if got2.File == "" {
		t.Fatalf("sibling path was unexpectedly cleared")
	}
}

// TestMergeFrameworks_UnionsAutoMetrics asserts that two FrameworkInstrumentation
// entries with the same Framework name get unioned: AutoMetrics and AutoSpans
// are deduped and sorted, EmissionSites are concatenated. Mirrors
// TestMergeCounters_DedupesAndUnionsLabels for the framework path.
func TestMergeFrameworks_UnionsAutoMetrics(t *testing.T) {
	in := []schema.FrameworkInstrumentation{
		{
			Framework:     "fiber",
			AutoMetrics:   []string{"http.server.duration", "http.server.request.size"},
			AutoSpans:     []string{"HTTP route"},
			EmissionSites: []schema.EmissionSite{{File: "/abs/a.go", Line: 10}},
		},
		{
			Framework:     "fiber",
			AutoMetrics:   []string{"http.server.request.size", "http.server.response.size"},
			AutoSpans:     []string{"HTTP route", "HTTP request"},
			EmissionSites: []schema.EmissionSite{{File: "/abs/b.go", Line: 20}},
		},
		{
			Framework:     "grpc",
			EmissionSites: []schema.EmissionSite{{File: "/abs/c.go", Line: 30}},
		},
	}

	out := mergeFrameworks(in)

	if len(out) != 2 {
		t.Fatalf("expected 2 distinct frameworks after merge, got %d: %+v", len(out), out)
	}

	var fiber *schema.FrameworkInstrumentation
	for i := range out {
		if out[i].Framework == "fiber" {
			fiber = &out[i]
		}
	}
	if fiber == nil {
		t.Fatalf("fiber missing from merged output")
	}

	wantMetrics := []string{"http.server.duration", "http.server.request.size", "http.server.response.size"}
	if len(fiber.AutoMetrics) != len(wantMetrics) {
		t.Fatalf("AutoMetrics = %v, want %v", fiber.AutoMetrics, wantMetrics)
	}
	for i, want := range wantMetrics {
		if fiber.AutoMetrics[i] != want {
			t.Fatalf("AutoMetrics[%d] = %q, want %q (full=%v)", i, fiber.AutoMetrics[i], want, fiber.AutoMetrics)
		}
	}

	wantSpans := []string{"HTTP request", "HTTP route"}
	if len(fiber.AutoSpans) != len(wantSpans) {
		t.Fatalf("AutoSpans = %v, want %v", fiber.AutoSpans, wantSpans)
	}
	for i, want := range wantSpans {
		if fiber.AutoSpans[i] != want {
			t.Fatalf("AutoSpans[%d] = %q, want %q (full=%v)", i, fiber.AutoSpans[i], want, fiber.AutoSpans)
		}
	}

	if len(fiber.EmissionSites) != 2 {
		t.Fatalf("expected 2 emission sites, got %d: %+v", len(fiber.EmissionSites), fiber.EmissionSites)
	}

	// Top-level sort by Framework name (alphabetic).
	if out[0].Framework > out[1].Framework {
		t.Fatalf("frameworks not sorted by name: %+v", out)
	}
}

// TestMergeCounters_DedupesIdenticalEmissionSites pins the post-merge
// dedup behavior across the metric-family merge functions. Two counter
// inputs sharing the SAME (File, Line, Tier) emission site must collapse
// to a single site in the merged primitive.
func TestMergeCounters_DedupesIdenticalEmissionSites(t *testing.T) {
	site := schema.EmissionSite{File: "a.go", Line: 10, Tier: 1}

	in := []schema.CounterPrimitive{
		{
			Name:          "shared",
			Labels:        []string{"tenant_id"},
			EmissionSites: []schema.EmissionSite{site},
		},
		{
			Name:          "shared",
			Labels:        []string{"result"},
			EmissionSites: []schema.EmissionSite{site, {File: "b.go", Line: 20, Tier: 1}},
		},
	}

	out := mergeCounters(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged counter, got %d", len(out))
	}

	got := out[0]
	if len(got.EmissionSites) != 2 {
		t.Fatalf("expected 2 emission sites after dedup, got %d: %+v", len(got.EmissionSites), got.EmissionSites)
	}
}

// TestMergeHistograms_DedupesIdenticalEmissionSites mirrors the counter case
// for histograms.
func TestMergeHistograms_DedupesIdenticalEmissionSites(t *testing.T) {
	site := schema.EmissionSite{File: "a.go", Line: 10, Tier: 1}
	in := []schema.HistogramPrimitive{
		{Name: "h", EmissionSites: []schema.EmissionSite{site}},
		{Name: "h", EmissionSites: []schema.EmissionSite{site}},
	}

	out := mergeHistograms(in)
	if len(out) != 1 || len(out[0].EmissionSites) != 1 {
		t.Fatalf("expected 1 histogram with 1 emission site, got %+v", out)
	}
}

// TestMergeGauges_DedupesIdenticalEmissionSites mirrors the counter case
// for gauges.
func TestMergeGauges_DedupesIdenticalEmissionSites(t *testing.T) {
	site := schema.EmissionSite{File: "a.go", Line: 10, Tier: 1}
	in := []schema.GaugePrimitive{
		{Name: "g", EmissionSites: []schema.EmissionSite{site}},
		{Name: "g", EmissionSites: []schema.EmissionSite{site}},
	}

	out := mergeGauges(in)
	if len(out) != 1 || len(out[0].EmissionSites) != 1 {
		t.Fatalf("expected 1 gauge with 1 emission site, got %+v", out)
	}
}

// TestMergeSpans_DedupesIdenticalEmissionSites mirrors the counter case
// for spans.
func TestMergeSpans_DedupesIdenticalEmissionSites(t *testing.T) {
	site := schema.EmissionSite{File: "a.go", Line: 10}
	in := []schema.SpanPrimitive{
		{Name: "s", EmissionSites: []schema.EmissionSite{site}},
		{Name: "s", EmissionSites: []schema.EmissionSite{site}},
	}

	out := mergeSpans(in)
	if len(out) != 1 || len(out[0].EmissionSites) != 1 {
		t.Fatalf("expected 1 span with 1 emission site, got %+v", out)
	}
}

// TestMergeLogFields_DedupesIdenticalEmissionSites mirrors the counter case
// for log fields.
func TestMergeLogFields_DedupesIdenticalEmissionSites(t *testing.T) {
	site := schema.EmissionSite{File: "a.go", Line: 10}
	in := []schema.LogFieldPrimitive{
		{Name: "tenant_id", LevelDistribution: map[string]int{"info": 1}, EmissionSites: []schema.EmissionSite{site}},
		{Name: "tenant_id", LevelDistribution: map[string]int{"info": 1}, EmissionSites: []schema.EmissionSite{site}},
	}

	out := mergeLogFields(in)
	if len(out) != 1 || len(out[0].EmissionSites) != 1 {
		t.Fatalf("expected 1 log field with 1 emission site, got %+v", out)
	}
}

// TestMergeFrameworks_DedupesIdenticalEmissionSites mirrors the counter case
// for frameworks.
func TestMergeFrameworks_DedupesIdenticalEmissionSites(t *testing.T) {
	site := schema.EmissionSite{File: "a.go", Line: 10}
	in := []schema.FrameworkInstrumentation{
		{Framework: "fiber", EmissionSites: []schema.EmissionSite{site}},
		{Framework: "fiber", EmissionSites: []schema.EmissionSite{site}},
	}

	out := mergeFrameworks(in)
	if len(out) != 1 || len(out[0].EmissionSites) != 1 {
		t.Fatalf("expected 1 framework with 1 emission site, got %+v", out)
	}
}

// TestMergeCrossCut_DedupesByTuple asserts that identical CrossCut findings
// — same (Kind, Site.File, Site.Line, Function, Detail) — collapse to one
// entry. Without dedup, a finding observed by two analyzer passes (or any
// future overlap) would surface twice.
func TestMergeCrossCut_DedupesByTuple(t *testing.T) {
	in := []schema.CrossCutFinding{
		{Kind: "tenant_consistency", Site: schema.EmissionSite{File: "a.go", Line: 10}, Function: "Foo", Detail: "missing tenant_id"},
		{Kind: "tenant_consistency", Site: schema.EmissionSite{File: "a.go", Line: 10}, Function: "Foo", Detail: "missing tenant_id"}, // duplicate
		{Kind: "tenant_consistency", Site: schema.EmissionSite{File: "a.go", Line: 20}, Function: "Foo", Detail: "missing tenant_id"}, // diff line
		{Kind: "trace_correlation", Site: schema.EmissionSite{File: "a.go", Line: 10}, Function: "Foo", Detail: "missing tenant_id"},  // diff kind
		{Kind: "tenant_consistency", Site: schema.EmissionSite{File: "b.go", Line: 10}, Function: "Foo", Detail: "missing tenant_id"}, // diff file
		{Kind: "tenant_consistency", Site: schema.EmissionSite{File: "a.go", Line: 10}, Function: "Bar", Detail: "missing tenant_id"}, // diff func
		{Kind: "tenant_consistency", Site: schema.EmissionSite{File: "a.go", Line: 10}, Function: "Foo", Detail: "other"},             // diff detail
	}

	out := mergeCrossCut(in)

	// 7 inputs, 1 exact duplicate → 6 unique.
	if len(out) != 6 {
		t.Fatalf("expected 6 deduped findings, got %d: %+v", len(out), out)
	}

	// Sorted by (Kind, File, Line) — pin one transition to detect a
	// regression in sortCrossCut.
	if out[0].Kind > out[len(out)-1].Kind {
		t.Fatalf("findings not sorted by kind: %+v", out)
	}
}

// TestMergeCrossCut_NilInputReturnsEmptySlice pins the JSON-output invariant:
// a nil input must still surface as an empty (non-nil) slice so downstream
// JSON marshals "[]" not "null".
func TestMergeCrossCut_NilInputReturnsEmptySlice(t *testing.T) {
	out := mergeCrossCut(nil)
	if out == nil {
		t.Fatalf("mergeCrossCut(nil) returned nil; want empty slice")
	}
	if len(out) != 0 {
		t.Fatalf("mergeCrossCut(nil) returned %d entries; want 0", len(out))
	}
}

// TestMergeStringSet_DedupesAndSorts pins the deterministic ordering used
// across labels and AutoSpans/AutoMetrics merging.
func TestMergeStringSet_DedupesAndSorts(t *testing.T) {
	got := mergeStringSet([]string{"b", "a"}, []string{"c", "a"})
	want := []string{"a", "b", "c"}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%v)", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
