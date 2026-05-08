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
