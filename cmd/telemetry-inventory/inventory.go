package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/orchestrator"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/renderer"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

const (
	generatedDirPerm  = 0o750
	generatedFilePerm = 0o644
)

func runInventory(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		outputMD       = fs.String("output-md", "./docs/dashboards/telemetry-dictionary.md", "Markdown dictionary output path")
		outputJSON     = fs.String("output-json", "", "Optional JSON intermediate output path")
		schemaVersion  = fs.String("schema-version", "", "Pin schema version (default: latest)")
		emitJSONStdout = fs.Bool("json", false, "Emit JSON to stdout instead of writing files")
		debug          = fs.Bool("debug", false, "Verbose internal diagnostics")
		verbose        = fs.Bool("verbose", false, "Verbose progress output")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if *schemaVersion != "" && *schemaVersion != schema.SchemaVersion {
		return fmt.Errorf("unsupported schema version %q: current schema is %s", *schemaVersion, schema.SchemaVersion)
	}

	target, err := resolveTargetArg(fs, "inventory")
	if err != nil {
		return err
	}

	if *verbose {
		fmt.Fprintf(stderr, "inventory target=%s\n", target)
	}

	dict, err := orchestrator.Inventory(target)
	if err != nil {
		return err
	}

	if *emitJSONStdout {
		payload, err := renderer.JSON(dict)
		if err != nil {
			return err
		}

		_, err = stdout.Write(payload)

		return err
	}

	if *outputMD != "" {
		if err := writeFileCreatingParents(*outputMD, []byte(renderer.Render(dict))); err != nil {
			return err
		}
	}

	if *outputJSON != "" {
		payload, err := renderer.JSON(dict)
		if err != nil {
			return err
		}

		if err := writeFileCreatingParents(*outputJSON, payload); err != nil {
			return err
		}
	}

	if *debug {
		fmt.Fprintf(stderr, "inventory completed: counters=%d histograms=%d gauges=%d spans=%d log_fields=%d frameworks=%d cross_cutting=%d\n",
			len(dict.Counters), len(dict.Histograms), len(dict.Gauges), len(dict.Spans), len(dict.LogFields), len(dict.Frameworks), len(dict.CrossCut))
	}

	return nil
}

// resolveTargetArg returns the single positional argument as the target
// path, defaulting to ".". A second positional arg is rejected so that a
// CLI typo cannot silently inventory the wrong directory.
func resolveTargetArg(fs *flag.FlagSet, subcommand string) (string, error) {
	if fs.NArg() > 1 {
		return "", fmt.Errorf("%s accepts at most one target path; got %d", subcommand, fs.NArg())
	}

	if fs.NArg() == 1 {
		return fs.Arg(0), nil
	}

	return ".", nil
}

func writeFileCreatingParents(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, generatedDirPerm); err != nil {
			return fmt.Errorf("create parent directory for %q: %w", path, err)
		}
	}

	if err := os.WriteFile(path, data, generatedFilePerm); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}

	return nil
}
