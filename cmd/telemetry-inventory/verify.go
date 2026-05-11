package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	verifycmd "github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/verify"
)

func runVerify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		committed = fs.String("committed", "./docs/dashboards/telemetry-dictionary.md", "Path to committed dictionary")
		quiet     = fs.Bool("quiet", false, "Suppress diff output")
		debug     = fs.Bool("debug", false, "Verbose internal diagnostics")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	target, err := resolveTargetArg(fs, "verify")
	if err != nil {
		return err
	}

	data, err := readCommittedDictionary(*committed)
	if err != nil {
		return err
	}

	result, err := verifycmd.Run(target, data)
	if err != nil {
		return err
	}

	if result.Code == 0 {
		if *debug {
			fmt.Fprintln(stderr, "telemetry dictionary is in sync")
		}

		return nil
	}

	if !*quiet && result.Diff != "" {
		fmt.Fprint(stdout, result.Diff)
	}

	return &exitError{code: result.Code, err: errors.New(result.Message)}
}

// readCommittedDictionary reads the committed dictionary inside an os.Root
// rooted at the file's parent directory. os.Root (Go 1.24+) provides path
// containment so a crafted --committed value cannot escape the directory it
// names via symlink traversal.
func readCommittedDictionary(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("read committed dictionary %q: %w", path, err)
	}
	defer root.Close()

	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("read committed dictionary %q: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read committed dictionary %q: %w", path, err)
	}

	return data, nil
}
