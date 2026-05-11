package analyzers

import (
	"go/ast"
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"github.com/LerianStudio/lib-commons/v5/commons/security"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// LogFieldAnalyzer detects structured log field keys and their level distribution.
var LogFieldAnalyzer = &analysis.Analyzer{
	Name:       "logfield",
	Doc:        "Detects structured log field keys and possible PII fields.",
	Run:        runLogField,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeFor[*LogFieldFindings](),
}

// IsPIIField reports whether key denotes a sensitive (PII) field. It
// delegates to commons/security.IsSensitiveField — the same predicate used
// by the production redactor — so the audit tool and the runtime
// sanitizer share a single source of truth. Adding a new sensitive field
// in commons/security automatically widens what the analyzer flags.
func IsPIIField(key string) bool {
	return security.IsSensitiveField(key)
}

func runLogField(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok || insp == nil {
		return &LogFieldFindings{}, nil
	}

	fields := map[string]*schema.LogFieldPrimitive{}

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call, _ := n.(*ast.CallExpr)
		if call == nil {
			return
		}

		level, fieldArgs, ok := matchLogCall(pass, call)
		if !ok {
			return
		}

		for _, key := range extractLogFieldKeys(pass, fieldArgs) {
			p := fields[key]
			if p == nil {
				p = &schema.LogFieldPrimitive{
					Name:              key,
					LevelDistribution: map[string]int{},
					PIIRiskFlag:       IsPIIField(key),
				}
				fields[key] = p
			}

			p.EmissionSites = append(p.EmissionSites, siteFor(pass, call.Pos(), 0))
			p.LevelDistribution[level]++
			pass.Reportf(call.Pos(), "log field %q level=%s", key, level)
		}
	})

	out := &LogFieldFindings{Fields: make([]schema.LogFieldPrimitive, 0, len(fields))}
	for _, field := range fields {
		out.Fields = append(out.Fields, *field)
	}

	return out, nil
}

// matchLogCall recognizes structured log calls against the canonical
// commons/log.Logger interface (5 methods: Log, With, WithGroup, Enabled,
// Sync). Only Log and With carry field arguments worth harvesting; the rest
// do not surface log field keys. Convenience methods like Debug/Info/Warn/
// Error are NOT on commons/log.Logger and the commons/zap shape
// (msg string, fields ...Field) does not put fields where this matcher
// expects them — see logfield.go's commit history for the prior dead branch.
func matchLogCall(pass *analysis.Pass, call *ast.CallExpr) (string, []ast.Expr, bool) {
	sel, ok := selectorCall(call)
	if !ok {
		return "", nil, false
	}

	switch sel.Sel.Name {
	case logMethodName:
		if len(call.Args) < 4 || !isLogger(pass, sel.X) {
			return "", nil, false
		}

		return logLevelFromExpr(pass, call.Args[1]), call.Args[3:], true
	case "With":
		if !isLogger(pass, sel.X) {
			return "", nil, false
		}

		return "with", call.Args, true
	default:
		return "", nil, false
	}
}

func logLevelFromExpr(pass *analysis.Pass, expr ast.Expr) string {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if selectorFromPackage(pass, sel, logPkgPath) {
			switch sel.Sel.Name {
			case "LevelDebug":
				return "debug"
			case "LevelInfo":
				return "info"
			case "LevelWarn":
				return "warn"
			case "LevelError":
				return "error"
			}
		}
	}

	return "unknown"
}
