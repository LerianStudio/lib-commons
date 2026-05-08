package orchestrator

// This file implements an in-process analyzer runner with a topological
// sort over Analyzer.Requires. Known limitation: it does NOT use
// golang.org/x/tools/go/analysis/checker.Load (the upstream multipackage
// runner) because that framework introduces fact-import/export semantics
// we don't need and adds packages-mode coupling we don't want. The
// trade-off is that fact-passing analyzers won't work here — none of our
// current analyzers use facts. Revisit if a future analyzer requires
// facts or cross-package fact propagation.

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

func runAnalyzers(pkgs []*packages.Package, analyzers []*analysis.Analyzer) (map[string]map[*analysis.Analyzer]any, error) {
	order, err := topoSort(analyzers)
	if err != nil {
		return nil, err
	}

	out := make(map[string]map[*analysis.Analyzer]any, len(pkgs))
	for _, pkg := range pkgs {
		if pkg == nil || pkg.IllTyped {
			continue
		}

		results := make(map[*analysis.Analyzer]any, len(order))

		out[pkg.PkgPath] = results
		for _, analyzer := range order {
			res, runErr := runOne(pkg, analyzer, results)
			if runErr != nil {
				return nil, fmt.Errorf("analyzer %s on %s: %w", analyzer.Name, pkg.PkgPath, runErr)
			}

			results[analyzer] = res
		}
	}

	return out, nil
}

func runOne(pkg *packages.Package, analyzer *analysis.Analyzer, prior map[*analysis.Analyzer]any) (any, error) {
	pass := &analysis.Pass{
		Analyzer:   analyzer,
		Fset:       pkg.Fset,
		Files:      pkg.Syntax,
		Pkg:        pkg.Types,
		TypesInfo:  pkg.TypesInfo,
		TypesSizes: pkg.TypesSizes,
		ResultOf:   prior,
		Report:     func(analysis.Diagnostic) {},
		ImportObjectFact: func(types.Object, analysis.Fact) bool {
			return false
		},
		ImportPackageFact: func(*types.Package, analysis.Fact) bool {
			return false
		},
		ExportObjectFact:  func(types.Object, analysis.Fact) {},
		ExportPackageFact: func(analysis.Fact) {},
		AllObjectFacts: func() []analysis.ObjectFact {
			return nil
		},
		AllPackageFacts: func() []analysis.PackageFact {
			return nil
		},
	}

	return analyzer.Run(pass)
}

func topoSort(roots []*analysis.Analyzer) ([]*analysis.Analyzer, error) {
	visited := map[*analysis.Analyzer]bool{}
	stack := map[*analysis.Analyzer]bool{}

	var order []*analysis.Analyzer

	var visit func(*analysis.Analyzer) error

	visit = func(analyzer *analysis.Analyzer) error {
		if analyzer == nil {
			return nil
		}

		if stack[analyzer] {
			return fmt.Errorf("analyzer cycle through %s", analyzer.Name)
		}

		if visited[analyzer] {
			return nil
		}

		stack[analyzer] = true
		for _, required := range analyzer.Requires {
			if err := visit(required); err != nil {
				return err
			}
		}

		stack[analyzer] = false
		visited[analyzer] = true
		order = append(order, analyzer)

		return nil
	}

	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}

	return order, nil
}
