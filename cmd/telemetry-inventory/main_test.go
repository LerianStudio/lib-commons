//go:build unit

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

// TestRunInventory_JSONStdout exercises the -json flag: payload must be
// written to stdout (not a file) and must parse as a Dictionary with the
// current SchemaVersion. Pins the contract that stdout output is structured
// JSON, not the human-readable Markdown.
func TestRunInventory_JSONStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"-json", "testdata/services/pure-tier1"}
	if err := runInventory(args, &stdout, &stderr); err != nil {
		t.Fatalf("runInventory: %v (stderr=%s)", err, stderr.String())
	}

	var got schema.Dictionary
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}

	if got.Meta.SchemaVersion != schema.SchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got.Meta.SchemaVersion, schema.SchemaVersion)
	}

	// Ensure the actual primitive showed up so we know analyzers ran.
	var found bool
	for _, c := range got.Counters {
		if c.Name == "ledger_transactions_total" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ledger_transactions_total counter; got %+v", got.Counters)
	}
}

// TestRunInventory_WritesMarkdownAndJSON exercises the dual-output path:
// both -output-md and -output-json must produce on-disk files, and stdout
// must stay empty (no JSON dump when -json is not set).
func TestRunInventory_WritesMarkdownAndJSON(t *testing.T) {
	tmp := t.TempDir()
	mdPath := filepath.Join(tmp, "out", "telemetry-dictionary.md")
	jsonPath := filepath.Join(tmp, "out", "telemetry-dictionary.json")

	var stdout, stderr bytes.Buffer
	args := []string{
		"-output-md", mdPath,
		"-output-json", jsonPath,
		"testdata/services/pure-tier1",
	}
	if err := runInventory(args, &stdout, &stderr); err != nil {
		t.Fatalf("runInventory: %v (stderr=%s)", err, stderr.String())
	}

	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout when writing files; got %s", stdout.String())
	}

	mdBytes, err := os.ReadFile(mdPath) //nolint:gosec // G304: path under t.TempDir()
	if err != nil {
		t.Fatalf("read markdown: %v", err)
	}
	if !strings.Contains(string(mdBytes), "schema_version: \"1.0.0\"") {
		t.Fatalf("markdown missing schema_version line; got:\n%s", string(mdBytes))
	}

	jsonBytes, err := os.ReadFile(jsonPath) //nolint:gosec // G304: path under t.TempDir()
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var dict schema.Dictionary
	if err := json.Unmarshal(jsonBytes, &dict); err != nil {
		t.Fatalf("Unmarshal json: %v", err)
	}
	if dict.Meta.SchemaVersion != schema.SchemaVersion {
		t.Fatalf("json SchemaVersion = %q, want %q", dict.Meta.SchemaVersion, schema.SchemaVersion)
	}
}

// TestRunInventory_SchemaVersionMismatchReturnsError pins the explicit
// version-pinning contract: passing -schema-version=<other> than the
// current schema must surface a non-nil error rather than silently
// generating output for a stale schema.
func TestRunInventory_SchemaVersionMismatchReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"-schema-version", "0.9.0", "testdata/services/pure-tier1"}
	err := runInventory(args, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error for schema mismatch; stdout=%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "unsupported schema version") {
		t.Fatalf("error = %q, want it to mention 'unsupported schema version'", err.Error())
	}
}

// TestRun_UnknownSubcommandReturnsErrUsage pins the help-routing contract:
// an unknown subcommand surfaces errUsage so main() can exit(2) and print
// the canonical usage block.
func TestRun_UnknownSubcommandReturnsErrUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"not-a-real-subcommand"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected errUsage, got nil")
	}
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errors.Is(err, errUsage)", err)
	}
}

// TestRun_NoArgsReturnsErrUsage pins the empty-args path: no subcommand at
// all must be treated as a usage error too.
func TestRun_NoArgsReturnsErrUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected errUsage for empty args, got nil")
	}
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errors.Is(err, errUsage)", err)
	}
}

// TestRunVerify_HappyPath drives runVerify against a freshly-generated
// committed dictionary. The dictionary is generated via runInventory into a
// temp file, then runVerify is pointed at that exact file — drift must be
// zero and the call must return nil.
func TestRunVerify_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	committed := filepath.Join(tmp, "telemetry-dictionary.md")

	// Step 1: generate the committed dictionary from the same fixture.
	var stdout, stderr bytes.Buffer
	if err := runInventory(
		[]string{"-output-md", committed, "testdata/services/pure-tier1"},
		&stdout, &stderr,
	); err != nil {
		t.Fatalf("runInventory (setup): %v (stderr=%s)", err, stderr.String())
	}

	// Step 2: verify against itself — drift must be zero.
	stdout.Reset()
	stderr.Reset()
	if err := runVerify(
		[]string{"-committed", committed, "-quiet", "testdata/services/pure-tier1"},
		&stdout, &stderr,
	); err != nil {
		t.Fatalf("runVerify (happy path): %v (stderr=%s)", err, stderr.String())
	}
}

// TestRunVerify_DriftReturnsExitCode1 exercises the drift path: runVerify
// must surface an *exitError with code=1 when the committed dictionary
// differs from the generated one.
func TestRunVerify_DriftReturnsExitCode1(t *testing.T) {
	tmp := t.TempDir()
	committed := filepath.Join(tmp, "telemetry-dictionary.md")

	// Generate against pure-tier1, then verify against pure-tier2 — they
	// must differ.
	var stdout, stderr bytes.Buffer
	if err := runInventory(
		[]string{"-output-md", committed, "testdata/services/pure-tier1"},
		&stdout, &stderr,
	); err != nil {
		t.Fatalf("runInventory (setup): %v (stderr=%s)", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := runVerify(
		[]string{"-committed", committed, "-quiet", "testdata/services/pure-tier2"},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatalf("expected drift error, got nil")
	}

	var coded *exitError
	if !errors.As(err, &coded) {
		t.Fatalf("err is not *exitError: %T %v", err, err)
	}
	if coded.code != 1 {
		t.Fatalf("exit code = %d, want 1", coded.code)
	}
}

// TestRunVerify_SchemaMismatchReturnsExitCode2 pins the schema-mismatch
// branch of verify.Compare: a committed file with a different
// schema_version line must surface as code=2 (not code=1).
func TestRunVerify_SchemaMismatchReturnsExitCode2(t *testing.T) {
	tmp := t.TempDir()
	committed := filepath.Join(tmp, "telemetry-dictionary.md")

	var stdout, stderr bytes.Buffer
	if err := runInventory(
		[]string{"-output-md", committed, "testdata/services/pure-tier1"},
		&stdout, &stderr,
	); err != nil {
		t.Fatalf("runInventory (setup): %v", err)
	}

	// Mutate the committed file's schema_version to force a 2 result.
	mutated, err := os.ReadFile(committed) //nolint:gosec // G304: path under t.TempDir()
	if err != nil {
		t.Fatalf("read committed: %v", err)
	}
	mutated = bytes.ReplaceAll(mutated, []byte(`schema_version: "1.0.0"`), []byte(`schema_version: "0.9.0"`))
	if err := os.WriteFile(committed, mutated, 0o644); err != nil {
		t.Fatalf("write committed: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	err = runVerify(
		[]string{"-committed", committed, "-quiet", "testdata/services/pure-tier1"},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatalf("expected schema-mismatch error, got nil")
	}

	var coded *exitError
	if !errors.As(err, &coded) {
		t.Fatalf("err is not *exitError: %T %v", err, err)
	}
	if coded.code != 2 {
		t.Fatalf("exit code = %d, want 2", coded.code)
	}
}
