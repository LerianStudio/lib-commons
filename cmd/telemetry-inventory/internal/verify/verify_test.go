//go:build unit

package verify_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/verify"
)

func TestCompare_IgnoresGeneratedAt(t *testing.T) {
	committed := sampleDictionary("2026-01-01T00:00:00Z", "accounts_created")
	generated := sampleDictionary("2026-05-07T00:00:00Z", "accounts_created")
	result := verify.Compare(committed, generated)
	if result.Code != 0 {
		t.Fatalf("code = %d, want 0: %s", result.Code, result.Message)
	}
}

func TestCompare_Drift(t *testing.T) {
	result := verify.Compare(sampleDictionary("now", "accounts_created"), sampleDictionary("now", "accounts_total"))
	if result.Code != 1 {
		t.Fatalf("code = %d, want 1", result.Code)
	}
	if !strings.Contains(result.Diff, "+ ### accounts_total") {
		t.Fatalf("diff missing `+ ### accounts_total`; full diff:\n%s", result.Diff)
	}
	if !strings.Contains(result.Diff, "- ### accounts_created") {
		t.Fatalf("diff missing `- ### accounts_created`; full diff:\n%s", result.Diff)
	}
	if !strings.Contains(result.Message, "telemetry dictionary drift") {
		t.Fatalf("message missing 'telemetry dictionary drift': %q", result.Message)
	}
}

func TestCompare_SchemaMismatch(t *testing.T) {
	committed := strings.Replace(sampleDictionary("now", "accounts_created"), `schema_version: "1.0.0"`, `schema_version: "0.9.0"`, 1)
	result := verify.Compare(committed, sampleDictionary("now", "accounts_created"))
	if result.Code != 2 {
		t.Fatalf("code = %d, want 2", result.Code)
	}
}

// TestCompare_MissingSchemaVersion covers the case where the committed file
// has no schema_version line at all (corrupt or hand-edited dictionary).
// Code=2 with a distinct message routes the operator to regenerate rather
// than leaving them to debug a confusing schema-mismatch error.
func TestCompare_MissingSchemaVersion(t *testing.T) {
	committed := strings.Replace(sampleDictionary("now", "accounts_created"), "schema_version: \"1.0.0\"\n", "", 1)
	result := verify.Compare(committed, sampleDictionary("now", "accounts_created"))
	if result.Code != 2 {
		t.Fatalf("code = %d, want 2", result.Code)
	}
	if !strings.Contains(result.Message, "missing schema_version") {
		t.Fatalf("message missing 'missing schema_version': %q", result.Message)
	}
}

func TestRun_MissingCommittedFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")
	_, err := verify.Run("../../testdata/services/pure-tier1", missing)
	if err == nil {
		t.Fatalf("expected error reading missing committed file")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error did not mention missing path %q: %v", missing, err)
	}
}

// TestRun_TargetWithNoPackages exercises the orchestrator-error path: an empty
// directory yields ErrNoPackages and Run must surface it back to the CLI.
func TestRun_TargetWithNoPackages(t *testing.T) {
	emptyDir := t.TempDir()
	_, err := verify.Run(emptyDir, "../../docs/dashboards/telemetry-dictionary.md")
	if err == nil {
		t.Fatalf("expected error inventorying empty directory")
	}
}

// TestRedactGeneratedAt_OnlyMatchesMetaBlock is an adversarial-input test:
// a multi-line YAML continuation with `generated_at:` showing up in a quoted
// description must not be replaced. The regex anchors on `^generated_at: `
// at the start of a line, so embedded mentions stay intact.
func TestRedactGeneratedAt_OnlyMatchesMetaBlock(t *testing.T) {
	committed := strings.Replace(sampleDictionary("2026-01-01T00:00:00Z", "accounts_created"),
		"description: \"Measures the number of accounts created by the server.\"",
		"description: \"first generated_at: 2025-01-01\\nsecond generated_at: 2025-02-01\"", 1)
	committed = strings.Replace(committed, "schema_version", "extra: true\nschema_version", 1)

	generated := strings.Replace(sampleDictionary("2026-05-07T00:00:00Z", "accounts_created"),
		"description: \"Measures the number of accounts created by the server.\"",
		"description: \"first generated_at: 2025-01-01\\nsecond generated_at: 2025-02-01\"", 1)
	generated = strings.Replace(generated, "schema_version", "extra: true\nschema_version", 1)

	result := verify.Compare(committed, generated)
	if result.Code != 0 {
		t.Fatalf("expected redaction to ignore non-meta generated_at occurrences; code=%d msg=%q diff=%s", result.Code, result.Message, result.Diff)
	}
}

func sampleDictionary(generatedAt, metric string) string {
	return "# Telemetry Dictionary\n\n## _meta\n\n```yaml\ngenerated_at: \"" + generatedAt + "\"\nschema_version: \"1.0.0\"\ntarget: \"fixture\"\ntool: \"telemetry-inventory\"\n```\n\n## Counters\n\n### " + metric + "\n\n```yaml\ndescription: \"Measures the number of accounts created by the server.\"\nemission_sites: []\ninstrument_type: \"counter\"\nlabels: []\nunit: \"1\"\n```\n"
}
