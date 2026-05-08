// Package orchestrator runs telemetry analyzers against a target Go module.
package orchestrator

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/analyzers"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// ErrNoPackages is returned when packages.Load yields zero packages.
var ErrNoPackages = errors.New("no Go packages found at target")

var allAnalyzers = []*analysis.Analyzer{
	analyzers.CounterAnalyzer,
	analyzers.HistogramAnalyzer,
	analyzers.GaugeAnalyzer,
	analyzers.SpanAnalyzer,
	analyzers.LogFieldAnalyzer,
	analyzers.FrameworkAnalyzer,
	analyzers.CrossCutAnalyzer,
}

// Inventory loads target as a Go module, runs all analyzers, and returns a Dictionary.
func Inventory(target string) (*schema.Dictionary, error) {
	displayTarget := filepath.ToSlash(filepath.Clean(target))

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("resolve target %q: %w", target, err)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedModule | packages.NeedTypesSizes,
		Dir: absTarget,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages at %q: %w", target, err)
	}

	if len(pkgs) == 0 {
		return nil, ErrNoPackages
	}

	if errs := collectPackageErrors(pkgs); len(errs) > 0 {
		return nil, fmt.Errorf("package load errors: %w", errors.Join(errs...))
	}

	results, err := runAnalyzers(pkgs, allAnalyzers)
	if err != nil {
		return nil, err
	}

	dict := &schema.Dictionary{
		Meta: schema.Meta{
			SchemaVersion: schema.SchemaVersion,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Target:        displayTarget,
		},
	}
	mergeResults(dict, results, absTarget)

	return dict, nil
}

func collectPackageErrors(pkgs []*packages.Package) []error {
	var errs []error

	for _, p := range pkgs {
		for _, e := range p.Errors {
			errs = append(errs, fmt.Errorf("%s: %s", p.PkgPath, e.Msg))
		}
	}

	return errs
}
