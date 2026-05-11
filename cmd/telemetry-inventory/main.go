// Command telemetry-inventory inventories OpenTelemetry primitives in a Go
// service and renders a deterministic telemetry dictionary.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
)

const usage = `telemetry-inventory - OpenTelemetry primitive inventory for Go services.

Usage:
  telemetry-inventory <subcommand> [flags] [target]

Subcommands:
  inventory   Sweep target and render Markdown or JSON dictionary.
  verify      Regenerate and byte-diff against a committed dictionary.
  version     Show CLI version and build info.

Run 'telemetry-inventory <subcommand> -h' for subcommand-specific flags.
`

var (
	errUsage = errors.New("usage")
	version  = "dev"
	commit   = "none"
	date     = "unknown"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	return e.err.Error()
}

func (e *exitError) Unwrap() error {
	return e.err
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}

		var coded *exitError
		if errors.As(err, &coded) {
			if coded.err != nil {
				fmt.Fprintln(os.Stderr, coded.err)
			}

			os.Exit(coded.code)
		}

		fmt.Fprintln(os.Stderr, "telemetry-inventory:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}

	if err := analyzers.ValidateHelperRegistry(); err != nil {
		return err
	}

	switch args[0] {
	case "inventory":
		return runInventory(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "telemetry-inventory %s (%s, %s)\n", version, commit, date)
		return nil
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown subcommand: %q\n\n", args[0])
		return errUsage
	}
}
