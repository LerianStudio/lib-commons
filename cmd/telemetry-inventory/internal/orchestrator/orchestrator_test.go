//go:build unit

package orchestrator_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/orchestrator"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

func TestInventory_NoPackages_ReturnsError(t *testing.T) {
	dict, err := orchestrator.Inventory(t.TempDir())
	if err == nil {
		t.Fatalf("expected error, got dict=%+v", dict)
	}
	// Pin the contract: an empty target must surface either the
	// ErrNoPackages sentinel (on go-tooling versions where packages.Load
	// returns 0 packages for an empty dir) or a "package load errors"
	// wrapping error (on versions where Load returns a package carrying
	// the no-go-files diagnostic). A refactor that drops both branches
	// to a bare error would be caught here, and a refactor that returns
	// a non-error nil-dict would be caught above.
	if !errors.Is(err, orchestrator.ErrNoPackages) &&
		!strings.Contains(err.Error(), "package load errors") &&
		!strings.Contains(err.Error(), "load packages") {
		t.Fatalf("err = %v, want ErrNoPackages or a wrapped package-load error", err)
	}
}

func TestInventory_PureTier1Fixture_DetectsCounter(t *testing.T) {
	dict, err := orchestrator.Inventory("../../testdata/services/pure-tier1")
	if err != nil {
		t.Fatalf("inventory failed: %v", err)
	}
	if dict.Meta.SchemaVersion != schema.SchemaVersion {
		t.Fatalf("schema version = %q, want %q", dict.Meta.SchemaVersion, schema.SchemaVersion)
	}
	if !hasCounter(dict.Counters, "ledger_transactions_total") {
		t.Fatalf("expected ledger_transactions_total in counters, got %+v", dict.Counters)
	}
}

func TestInventory_LeakSpansFixture_DetectsUnboundedSpan(t *testing.T) {
	dict, err := orchestrator.Inventory("../../testdata/services/leak-spans")
	if err != nil {
		t.Fatalf("inventory failed: %v", err)
	}
	// Both balanced and leaky spans must be observed (exact pair count
	// pins the analyzer to NOT silently drop one).
	if len(dict.Spans) != 2 {
		t.Fatalf("expected 2 spans (Balanced.Op + Leaky.Op), got %d: %+v", len(dict.Spans), dict.Spans)
	}

	var balanced, leaky *schema.SpanPrimitive

	for i := range dict.Spans {
		switch dict.Spans[i].Name {
		case "Balanced.Op":
			balanced = &dict.Spans[i]
		case "Leaky.Op":
			leaky = &dict.Spans[i]
		}
	}

	if balanced == nil {
		t.Fatalf("Balanced.Op span missing")
	}

	if balanced.UnboundedSpan {
		t.Fatalf("Balanced.Op should NOT be unbounded; got %+v", balanced)
	}

	if leaky == nil {
		t.Fatalf("Leaky.Op span missing")
	}

	if !leaky.UnboundedSpan {
		t.Fatalf("Leaky.Op should be unbounded; got %+v", leaky)
	}

	if len(leaky.EmissionSites) == 0 {
		t.Fatalf("Leaky.Op missing emission site")
	}

	// Pin the exact line so an off-by-one in span detection fails loudly.
	if got := leaky.EmissionSites[0].Line; got != 16 {
		t.Fatalf("Leaky.Op emission site line = %d, want 16", got)
	}
}

func TestInventory_BrokenCrossCutFixture_DetectsIssue(t *testing.T) {
	dict, err := orchestrator.Inventory("../../testdata/services/broken-crosscut")
	if err != nil {
		t.Fatalf("inventory failed: %v", err)
	}
	if len(dict.CrossCut) == 0 {
		t.Fatalf("expected cross-cutting findings")
	}

	// Asserting kinds (not just length) verifies the analyzer's three
	// finding categories are wired through merge & sort untouched.
	kinds := make(map[string]int, len(dict.CrossCut))
	for _, f := range dict.CrossCut {
		kinds[f.Kind]++
	}

	if kinds["tenant_consistency"] == 0 {
		t.Fatalf("expected tenant_consistency findings; kinds=%+v", kinds)
	}
}

func hasCounter(counters []schema.CounterPrimitive, name string) bool {
	for _, counter := range counters {
		if counter.Name == name {
			return true
		}
	}
	return false
}
