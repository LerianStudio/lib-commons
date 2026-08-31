//go:build unit

// Package boundary_test guards the module boundary that commons/obs exists to
// create.
//
// lib-commons must not name a github.com/LerianStudio/lib-observability type
// anywhere on its public API. Go types have nominal identity, so a single
// exported field or parameter of a lib-observability type re-pins every
// consumer of lib-commons to whichever lib-observability major lib-commons
// chose. This test walks every non-test .go file in the module with go/ast and
// fails if any exported symbol mentions such a type.
//
// This test is the thing that prevents the regression. Do not weaken it. The
// allowlist below has exactly one entry, by design.
package boundary_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// libObservability is the module whose types must not reach the public API.
const libObservability = "github.com/LerianStudio/lib-observability"

// allowedPackages are the packages permitted to name a lib-observability type
// on their exported surface.
//
// It is empty, and that is the point. Since lib-observability v4 every
// parameter position of that library is declared with universal types, so
// commons/obs/obsbridge - the one package here that imports it - hands its
// values straight back as commons/obs contracts and names nothing. No package
// of lib-commons has an excuse. Adding an entry re-opens the coupling
// commons/obs exists to close.
var allowedPackages = map[string]bool{}

// violation is one exported symbol that mentions a lib-observability type.
type violation struct {
	file   string
	line   int
	symbol string
	typ    string
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d: exported %s references %s", v.file, v.line, v.symbol, v.typ)
}

func TestPublicAPI_DoesNotMentionLibObservability(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	var violations []violation

	err = filepath.WalkDir(filepath.Join(root, "commons"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		if allowedPackages[filepath.ToSlash(filepath.Dir(rel))] {
			return nil
		}

		// internal/ packages are not importable by consumers, so they are not
		// part of the public API.
		if strings.Contains(filepath.ToSlash(rel), "/internal/") {
			return nil
		}

		found, scanErr := scanFile(path, filepath.ToSlash(rel))
		if scanErr != nil {
			return scanErr
		}

		violations = append(violations, found...)

		return nil
	})
	require.NoError(t, err)

	if !assert.Empty(t, violations, "lib-observability types must not appear on the public API of lib-commons") {
		for _, v := range violations {
			t.Log(v.String())
		}
	}
}

// scanFile reports every exported declaration in path whose signature, fields
// or embedded types reference an alias bound to lib-observability.
func scanFile(path, rel string) ([]violation, error) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	aliases := observabilityAliases(file)
	if len(aliases) == 0 {
		return nil, nil
	}

	var violations []violation

	report := func(node ast.Node, symbol string) {
		for _, typ := range referencedAliases(node, aliases) {
			violations = append(violations, violation{
				file:   rel,
				line:   fset.Position(node.Pos()).Line,
				symbol: symbol,
				typ:    typ,
			})
		}
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !exportedFunc(d) {
				continue
			}

			report(d.Type, symbolName(d))
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}

					reportExportedFields(s, report)
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.IsExported() && s.Type != nil {
							report(s.Type, name.Name)
						}
					}
				}
			}
		}
	}

	return violations, nil
}

// reportExportedFields reports the exported fields and interface methods of an
// exported type. Unexported fields are not part of the API surface.
func reportExportedFields(spec *ast.TypeSpec, report func(ast.Node, string)) {
	switch t := spec.Type.(type) {
	case *ast.StructType:
		for _, field := range t.Fields.List {
			if len(field.Names) == 0 {
				report(field.Type, spec.Name.Name+" (embedded)")

				continue
			}

			for _, name := range field.Names {
				if name.IsExported() {
					report(field.Type, spec.Name.Name+"."+name.Name)
				}
			}
		}
	case *ast.InterfaceType:
		for _, method := range t.Methods.List {
			for _, name := range method.Names {
				if name.IsExported() {
					report(method.Type, spec.Name.Name+"."+name.Name)
				}
			}
		}
	default:
		report(spec.Type, spec.Name.Name)
	}
}

// exportedFunc reports whether decl is reachable from outside the package:
// an exported function, or an exported method on an exported receiver.
func exportedFunc(decl *ast.FuncDecl) bool {
	if !decl.Name.IsExported() {
		return false
	}

	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return true
	}

	return receiverIsExported(decl.Recv.List[0].Type)
}

func receiverIsExported(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverIsExported(t.X)
	case *ast.IndexExpr:
		return receiverIsExported(t.X)
	case *ast.IndexListExpr:
		return receiverIsExported(t.X)
	case *ast.Ident:
		return t.IsExported()
	default:
		return false
	}
}

func symbolName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}

	return "(receiver)." + decl.Name.Name
}

// observabilityAliases maps each local import alias to the lib-observability
// package path it refers to.
func observabilityAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}

	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasPrefix(path, libObservability) {
			continue
		}

		alias := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			alias = imp.Name.Name
		}

		aliases[alias] = path
	}

	return aliases
}

// referencedAliases returns the lib-observability package paths referenced
// anywhere inside node.
func referencedAliases(node ast.Node, aliases map[string]string) []string {
	var found []string

	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}

		if path, ok := aliases[ident.Name]; ok {
			found = append(found, path+"."+sel.Sel.Name)
		}

		return true
	})

	return found
}
