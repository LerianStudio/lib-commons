//go:build unit

package renderer_test

import (
	"math/rand"
	"os"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/renderer"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

// TestRender_Idempotent shuffles the input dictionary slices with a fixed seed
// and asserts the rendered output is identical to the unshuffled render. This
// verifies that Render does its own deterministic ordering instead of leaning
// on input order.
func TestRender_Idempotent(t *testing.T) {
	dict := buildSampleDictionary()
	a := renderer.Render(dict)

	shuffled := buildSampleDictionary()
	rng := rand.New(rand.NewSource(0xC0DE)) //nolint:gosec // deterministic shuffle for test reproducibility
	rng.Shuffle(len(shuffled.Counters), func(i, j int) {
		shuffled.Counters[i], shuffled.Counters[j] = shuffled.Counters[j], shuffled.Counters[i]
	})
	rng.Shuffle(len(shuffled.Histograms), func(i, j int) {
		shuffled.Histograms[i], shuffled.Histograms[j] = shuffled.Histograms[j], shuffled.Histograms[i]
	})
	rng.Shuffle(len(shuffled.Gauges), func(i, j int) {
		shuffled.Gauges[i], shuffled.Gauges[j] = shuffled.Gauges[j], shuffled.Gauges[i]
	})
	rng.Shuffle(len(shuffled.Spans), func(i, j int) {
		shuffled.Spans[i], shuffled.Spans[j] = shuffled.Spans[j], shuffled.Spans[i]
	})
	rng.Shuffle(len(shuffled.LogFields), func(i, j int) {
		shuffled.LogFields[i], shuffled.LogFields[j] = shuffled.LogFields[j], shuffled.LogFields[i]
	})

	b := renderer.Render(shuffled)
	if a != b {
		t.Fatalf("render not idempotent under input shuffle\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

// TestRender_Golden compares the rendered sample dictionary to the committed
// golden. Set UPDATE_GOLDEN=1 to rewrite the golden. The env-var gate
// (instead of a -flag) prevents CI from accidentally rewriting goldens when
// a flag is forwarded.
func TestRender_Golden(t *testing.T) {
	dict := buildSampleDictionary()
	got := renderer.Render(dict)
	goldenPath := "testdata/golden/sample.md"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("renderer output diverged from golden. Run: UPDATE_GOLDEN=1 go test -tags=unit -run TestRender_Golden ./cmd/telemetry-inventory/internal/renderer\n--- want ---\n%s\n--- got ---\n%s", string(want), got)
	}
}

func buildSampleDictionary() *schema.Dictionary {
	return &schema.Dictionary{
		Meta: schema.Meta{SchemaVersion: schema.SchemaVersion, GeneratedAt: "2026-05-07T00:00:00Z", Target: "fixture"},
		Counters: []schema.CounterPrimitive{
			{Name: "z_counter", InstrumentType: "counter", Unit: "1", Labels: []string{"tenant_id", "result"}, LabelCardinality: 2, TenantScoped: true, EmissionSites: []schema.EmissionSite{{File: "b.go", Line: 20, Tier: 2}}},
			{Name: "a_counter", InstrumentType: "counter", Unit: "1", EmissionSites: []schema.EmissionSite{{File: "a.go", Line: 10, Tier: 1}}},
		},
		Histograms: []schema.HistogramPrimitive{{Name: "latency_ms", InstrumentType: "histogram", Unit: "ms", Buckets: []float64{10, 1, 5}, Labels: []string{"tenant_id"}, LabelCardinality: 1, TenantScoped: true, EmissionSites: []schema.EmissionSite{{File: "c.go", Line: 30, Tier: 1}}}},
		Gauges:     []schema.GaugePrimitive{{Name: "queue_depth", InstrumentType: "gauge", Unit: "1", EmissionSites: []schema.EmissionSite{{File: "d.go", Line: 40, Tier: 3}}}},
		Spans:      []schema.SpanPrimitive{{Name: "Operation", Attributes: []string{"tenant_id"}, EmissionSites: []schema.EmissionSite{{File: "e.go", Line: 50}}, RecordOnError: true, StatusOnError: true}},
		LogFields:  []schema.LogFieldPrimitive{{Name: "tenant_id", LevelDistribution: map[string]int{"info": 2, "error": 1}, EmissionSites: []schema.EmissionSite{{File: "f.go", Line: 60}}}},
		Frameworks: []schema.FrameworkInstrumentation{{Framework: "net/http/otelhttp", AutoMetrics: []string{"http.server.duration"}, AutoSpans: []string{"HTTP route"}, EmissionSites: []schema.EmissionSite{{File: "g.go", Line: 70}}}},
		CrossCut:   []schema.CrossCutFinding{{Kind: "tenant_consistency", Function: "Handle", Detail: "span lacks tenant_id", Site: schema.EmissionSite{File: "h.go", Line: 80}}},
	}
}
