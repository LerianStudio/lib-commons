package analyzers

import (
	"go/ast"
	"go/token"
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// SpanAnalyzer detects tracer.Start sites and whether spans are ended in scope.
var SpanAnalyzer = &analysis.Analyzer{
	Name:       "span",
	Doc:        "Detects span primitives and unbounded spans.",
	Run:        runSpan,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeFor[*SpanFindings](),
}

func runSpan(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok || insp == nil {
		return &SpanFindings{}, nil
	}

	out := &SpanFindings{}

	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)}, func(n ast.Node) {
		var body *ast.BlockStmt

		switch fn := n.(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			body = fn.Body
		}

		if body == nil {
			return
		}

		out.Spans = append(out.Spans, collectSpansInBlock(pass, body)...)
	})

	return out, nil
}

//nolint:gocognit,gocyclo // The localized block walk keeps span pairing state easy to verify.
func collectSpansInBlock(pass *analysis.Pass, body *ast.BlockStmt) []schema.SpanPrimitive {
	type spanRef struct {
		name      string
		variable  string
		pos       token.Pos
		site      schema.EmissionSite
		attrs     []string
		ended     bool
		recorded  bool
		statusSet bool
	}

	var spans []*spanRef

	byVariable := map[string]*spanRef{}

	bindStart := func(call *ast.CallExpr, lhs []ast.Expr, rhsIndex int) {
		name, attrs, ok := matchTracerStart(pass, call)
		if !ok || name == "" {
			return
		}

		spanVar := secondReturnIdent(lhs, rhsIndex)
		ref := &spanRef{name: name, variable: spanVar, pos: call.Pos(), site: siteFor(pass, call.Pos(), 0), attrs: attrs}

		spans = append(spans, ref)
		if spanVar != "" {
			byVariable[spanVar] = ref
		}
	}

	ast.Inspect(body, func(n ast.Node) bool {
		// Skip nested function literals: runSpan already schedules every
		// *ast.FuncLit body for its own scan via Preorder, so descending
		// into one here would double-emit spans and let nested End calls
		// mutate the outer byVariable map.
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok || i >= len(x.Lhs) {
					continue
				}

				bindStart(call, x.Lhs, i)
			}
		case *ast.ValueSpec:
			// var ctx, span = tracer.Start(...) — same shape as AssignStmt
			// but expressed as a declaration. Without this, `var`-style span
			// bindings are silently skipped and produce false-positive
			// unbounded_span findings.
			lhs := make([]ast.Expr, len(x.Names))
			for i, name := range x.Names {
				lhs[i] = name
			}

			for i, value := range x.Values {
				call, ok := value.(*ast.CallExpr)
				if !ok || i >= len(lhs) {
					continue
				}

				bindStart(call, lhs, i)
			}
		case *ast.DeferStmt:
			if variable := matchSpanMethod(x.Call, "End"); variable != "" {
				if ref := byVariable[variable]; ref != nil {
					ref.ended = true
				}
			}
		case *ast.CallExpr:
			// Only `defer span.End()` counts as guaranteed end. Bare
			// span.End() in a conditional branch leaves the span unbounded
			// on early-return paths, so we do NOT mark ended=true here —
			// the caller still gets the unbounded_span finding and can
			// review whether the End() coverage is actually total.
			if variable := matchSpanMethod(x, "RecordError"); variable != "" {
				if ref := byVariable[variable]; ref != nil {
					ref.recorded = true
				}
			}

			if variable := matchSpanMethod(x, "SetStatus"); variable != "" {
				if ref := byVariable[variable]; ref != nil {
					ref.statusSet = true
				}
			}

			if variable := matchSpanMethod(x, "SetAttributes"); variable != "" {
				if ref := byVariable[variable]; ref != nil {
					ref.attrs = mergeStrings(ref.attrs, extractAttributeKeys(pass, x.Args)...)
				}
			}
		}

		return true
	})

	out := make([]schema.SpanPrimitive, 0, len(spans))
	for _, ref := range spans {
		primitive := schema.SpanPrimitive{
			Name:          ref.name,
			EmissionSites: []schema.EmissionSite{ref.site},
			Attributes:    uniqueSorted(ref.attrs),
			UnboundedSpan: !ref.ended,
			StatusOnError: ref.statusSet,
			RecordOnError: ref.recorded,
		}
		pass.Reportf(ref.pos, "span %q balanced=%v", primitive.Name, !primitive.UnboundedSpan)
		out = append(out, primitive)
	}

	return out
}

func matchTracerStart(pass *analysis.Pass, call *ast.CallExpr) (string, []string, bool) {
	sel, ok := selectorCall(call)
	if !ok || sel.Sel.Name != "Start" || !isOTelTracer(pass, sel.X) || len(call.Args) < 2 {
		return "", nil, false
	}

	name := stringLitValue(pass, call.Args[1])
	attrs := extractAttributeKeys(pass, call.Args[2:])

	return name, attrs, true
}

func secondReturnIdent(lhs []ast.Expr, rhsIndex int) string {
	if len(lhs) == 0 {
		return ""
	}

	idx := rhsIndex + 1
	if len(lhs) == 2 && rhsIndex == 0 {
		idx = 1
	}

	if idx >= len(lhs) {
		return ""
	}

	id, ok := lhs[idx].(*ast.Ident)
	if !ok || id.Name == "_" {
		return ""
	}

	return id.Name
}

func matchSpanMethod(call *ast.CallExpr, method string) string {
	if call == nil {
		return ""
	}

	sel, ok := selectorCall(call)
	if !ok || sel.Sel.Name != method {
		return ""
	}

	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}

	return id.Name
}
