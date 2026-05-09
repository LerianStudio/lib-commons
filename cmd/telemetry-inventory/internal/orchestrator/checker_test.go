//go:build unit

package orchestrator

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
)

// TestTopoSort_RejectsCycle pins the cycle-detection branch of topoSort.
// Two analyzers that mutually require each other must surface as an error
// rather than silently looping or panicking the orchestrator.
func TestTopoSort_RejectsCycle(t *testing.T) {
	a := &analysis.Analyzer{Name: "a"}
	b := &analysis.Analyzer{Name: "b"}
	a.Requires = []*analysis.Analyzer{b}
	b.Requires = []*analysis.Analyzer{a}

	_, err := topoSort([]*analysis.Analyzer{a})
	if err == nil {
		t.Fatalf("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error %q missing 'cycle'", err.Error())
	}
}

// TestTopoSort_OrdersDependencies verifies the happy path: an analyzer that
// requires another is emitted after its dependency. Pinning the order keeps
// runOne's prior-result map populated correctly.
func TestTopoSort_OrdersDependencies(t *testing.T) {
	dep := &analysis.Analyzer{Name: "dep"}
	root := &analysis.Analyzer{Name: "root", Requires: []*analysis.Analyzer{dep}}

	order, err := topoSort([]*analysis.Analyzer{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 2 {
		t.Fatalf("len(order) = %d, want 2: %+v", len(order), order)
	}

	if order[0] != dep || order[1] != root {
		t.Fatalf("order = [%s, %s], want [dep, root]", order[0].Name, order[1].Name)
	}
}
