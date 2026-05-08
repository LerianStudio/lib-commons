package analyzers

import (
	"go/ast"
	"reflect"
	"regexp"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
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

var piiFieldPattern = regexp.MustCompile(`(?i)(password|token|secret|apikey|api_key|email|ssn|phone|address|cpf|cnpj|card)`)

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
					PIIRiskFlag:       piiFieldPattern.MatchString(key),
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

func matchLogCall(pass *analysis.Pass, call *ast.CallExpr) (string, []ast.Expr, bool) {
	sel, ok := selectorCall(call)
	if !ok {
		return "", nil, false
	}

	method := sel.Sel.Name
	switch method {
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
	case "Debug", "Info", "Warn", "Error":
		if !isLogger(pass, sel.X) {
			return "", nil, false
		}

		if len(call.Args) < 2 {
			return strings.ToLower(method), nil, false
		}

		return strings.ToLower(method), call.Args[1:], true
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
