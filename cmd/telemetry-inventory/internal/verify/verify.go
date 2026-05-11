// Package verify implements telemetry dictionary drift comparison.
package verify

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/orchestrator"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/renderer"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

var (
	generatedAtLinePattern = regexp.MustCompile(`(?m)^generated_at: .*\n?`)
	schemaVersionPattern   = regexp.MustCompile(`(?m)^schema_version: "([^"]+)"`)
)

// Result is the drift-check outcome. Code follows CLI exit-code semantics.
type Result struct {
	Code    int
	Message string
	Diff    string
}

// Run regenerates a dictionary for target and compares it against the
// supplied committed bytes. File I/O is the caller's responsibility — the
// CLI layer opens the committed dictionary inside an os.Root for path
// containment before invoking Run.
func Run(target string, committed []byte) (Result, error) {
	dict, err := orchestrator.Inventory(target)
	if err != nil {
		return Result{}, err
	}

	generated := renderer.Render(dict)

	return Compare(string(committed), generated), nil
}

// Compare compares committed and generated Markdown dictionaries.
func Compare(committed, generated string) Result {
	committedVersion := schemaVersion(committed)
	generatedVersion := schemaVersion(generated)

	if committedVersion == "" {
		return Result{Code: 2, Message: "telemetry dictionary missing schema_version; run make telemetry-dictionary to regenerate"}
	}

	if generatedVersion != "" && committedVersion != generatedVersion {
		return Result{Code: 2, Message: fmt.Sprintf("telemetry dictionary schema mismatch: committed=%s generated=%s; regenerate against CLI schema %s", committedVersion, generatedVersion, schema.SchemaVersion)}
	}

	left := redactGeneratedAt(committed)

	right := redactGeneratedAt(generated)
	if left == right {
		return Result{}
	}

	return Result{Code: 1, Message: "telemetry dictionary drift detected; run make telemetry-dictionary and stage the result", Diff: simpleDiff(left, right)}
}

func schemaVersion(markdown string) string {
	matches := schemaVersionPattern.FindStringSubmatch(markdown)
	if len(matches) != 2 {
		return ""
	}

	return matches[1]
}

func redactGeneratedAt(markdown string) string {
	return generatedAtLinePattern.ReplaceAllString(markdown, "generated_at: \"<redacted>\"\n")
}

// simpleDiff renders a per-line +/- listing of changed lines. Not a real unified
// diff (no hunk merging, no context lines) — it is just enough for `make ci` to
// surface which lines changed when the dictionary drifts. Run `make telemetry-dictionary`
// to regenerate authoritatively.
func simpleDiff(committed, generated string) string {
	left := strings.Split(committed, "\n")
	right := strings.Split(generated, "\n")

	var sb strings.Builder

	maxLines := max(len(left), len(right))

	for i := range maxLines {
		var l, r string
		if i < len(left) {
			l = left[i]
		}

		if i < len(right) {
			r = right[i]
		}

		if l == r {
			continue
		}

		if i < len(left) {
			sb.WriteString("- ")
			sb.WriteString(l)
			sb.WriteByte('\n')
		}

		if i < len(right) {
			sb.WriteString("+ ")
			sb.WriteString(r)
			sb.WriteByte('\n')
		}
	}

	return sb.String()
}
