package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

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

	target := "."
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}

	result, err := verifycmd.Run(target, *committed)
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
