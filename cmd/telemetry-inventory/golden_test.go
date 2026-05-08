//go:build unit

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/orchestrator"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/renderer"
)

// TestE2E_GoldenDictionaries renders a dictionary for each fixture service and
// compares it to the committed golden file. Set UPDATE_GOLDEN=1 to rewrite
// the goldens. The env-var gate (instead of a -flag) prevents CI from
// accidentally rewriting goldens when a flag is forwarded.
func TestE2E_GoldenDictionaries(t *testing.T) {
	updateGoldens := os.Getenv("UPDATE_GOLDEN") == "1"

	services := []string{"pure-tier1", "pure-tier2", "mixed-realistic", "broken-crosscut", "leak-spans"}
	for _, service := range services {
		t.Run(service, func(t *testing.T) {
			dict, err := orchestrator.Inventory(filepath.Join("testdata", "services", service))
			if err != nil {
				t.Fatalf("inventory failed: %v", err)
			}
			dict.Meta.GeneratedAt = "2026-05-07T00:00:00Z"
			first := renderer.Render(dict)
			second := renderer.Render(dict)
			if first != second {
				t.Fatalf("non-idempotent render for %s", service)
			}
			goldenPath := filepath.Join("testdata", "golden", service+".md")
			if updateGoldens {
				if err := os.WriteFile(goldenPath, []byte(first), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if first != string(golden) {
				t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", service, string(golden), first)
			}
		})
	}
}
