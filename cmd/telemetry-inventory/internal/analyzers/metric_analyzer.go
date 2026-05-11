package analyzers

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	libmetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

type metricKindConfig struct {
	name              string
	instrumentType    string
	tier1Constructors map[string]bool
	tier2Method       string
	helperType        libmetrics.MetricType
	recordMethods     map[string]bool
}

type observedMetric struct {
	Name           string
	Description    string
	Unit           string
	InstrumentType string
	EmissionSites  []schema.EmissionSite
	Labels         []string
	Buckets        []float64
	TenantScoped   bool
}

func collectMetricFindings(pass *analysis.Pass, cfg metricKindConfig) []observedMetric {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok || insp == nil {
		return nil
	}

	var findings []*observedMetric

	nodeFilter := []ast.Node{(*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)}
	insp.Preorder(nodeFilter, func(n ast.Node) {
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

		findings = append(findings, collectMetricFindingsInBlock(pass, cfg, body)...)
	})

	out := make([]observedMetric, 0, len(findings))
	for _, f := range findings {
		if f == nil || f.Name == "" {
			continue
		}

		f.Labels = uniqueSorted(f.Labels)
		f.TenantScoped = containsString(f.Labels, tenantIDLabel)
		out = append(out, *f)
	}

	return out
}

func collectMetricFindingsInBlock(pass *analysis.Pass, cfg metricKindConfig, body *ast.BlockStmt) []*observedMetric {
	var findings []*observedMetric

	bound := map[string]*observedMetric{}
	processed := map[ast.Node]bool{}

	bindCall := func(call *ast.CallExpr, lhs []ast.Expr) {
		p := matchMetricConstructor(pass, cfg, call)
		if p == nil {
			return
		}

		// Register the metric exactly once per call site, even when the lhs
		// has multiple entries bound to the same call (tuple return shape:
		// `m, err := factory.Counter(...)`). Double-appending here would
		// double-count the metric in the findings slice.
		processed[call] = true

		findings = append(findings, p)
		reportMetricSite(pass, cfg, call.Pos(), p)

		// Every non-blank lhs ident becomes an alias for the metric so that
		// later record-site merging in mergeMetricRecordLabels can match
		// either name. For the common single-lhs case this is one entry; for
		// tuple returns it binds both.
		for _, item := range lhs {
			id, ok := item.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}

			bound[id.Name] = p
		}
	}

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			bindAssignStmt(x, bindCall)
		case *ast.ValueSpec:
			bindValueSpec(x, bindCall)
		case *ast.CallExpr:
			if processed[x] {
				return true
			}

			if p := matchMetricConstructor(pass, cfg, x); p != nil {
				findings = append(findings, p)
				reportMetricSite(pass, cfg, x.Pos(), p)

				return true
			}

			mergeMetricRecordLabels(pass, cfg, x, bound)
		}

		return true
	})

	return findings
}

// bindAssignStmt walks an assignment statement and dispatches each rhs call
// to the binder with the lhs ident(s) it should alias to. Two shapes:
//
//	x := makeMetric(...)             → bind call to [x]
//	x, y := makeMetric(...)          → tuple return: bind call to [x, y]
//	x, y := makeMetric(...), other() → positional: x↔makeMetric, y↔other
//
// The tuple-return case (`len(Lhs)==2 && len(Rhs)==1`) mirrors the pattern
// used by span.go's secondReturnIdent. Without binding both lhs entries, a
// future metric helper returning (Counter, error) would lose its second
// alias and miss subsequent record-site merging.
func bindAssignStmt(x *ast.AssignStmt, bind func(*ast.CallExpr, []ast.Expr)) {
	if len(x.Lhs) == 2 && len(x.Rhs) == 1 {
		call, ok := x.Rhs[0].(*ast.CallExpr)
		if !ok {
			return
		}

		bind(call, x.Lhs)

		return
	}

	for i, rhs := range x.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok || i >= len(x.Lhs) {
			continue
		}

		bind(call, []ast.Expr{x.Lhs[i]})
	}
}

func bindValueSpec(x *ast.ValueSpec, bind func(*ast.CallExpr, []ast.Expr)) {
	// Tuple-return `var a, b = factory.Counter(...)` mirrors the AssignStmt
	// case: a single rhs call paired with multiple lhs idents.
	if len(x.Names) >= 2 && len(x.Values) == 1 {
		call, ok := x.Values[0].(*ast.CallExpr)
		if !ok {
			return
		}

		lhs := make([]ast.Expr, len(x.Names))
		for i, name := range x.Names {
			lhs[i] = name
		}

		bind(call, lhs)

		return
	}

	for i, value := range x.Values {
		call, ok := value.(*ast.CallExpr)
		if !ok || i >= len(x.Names) {
			continue
		}

		bind(call, []ast.Expr{x.Names[i]})
	}
}

func reportMetricSite(pass *analysis.Pass, cfg metricKindConfig, pos token.Pos, p *observedMetric) {
	if len(p.EmissionSites) == 0 {
		return
	}

	pass.Reportf(pos, "%s %q tier=%d", cfg.name, p.Name, p.EmissionSites[0].Tier)
}

func matchMetricConstructor(pass *analysis.Pass, cfg metricKindConfig, call *ast.CallExpr) *observedMetric {
	if p := matchTier1Metric(pass, cfg, call); p != nil {
		return p
	}

	if p := matchTier2Metric(pass, cfg, call); p != nil {
		return p
	}

	return matchTier3Metric(pass, cfg, call)
}

func matchTier1Metric(pass *analysis.Pass, cfg metricKindConfig, call *ast.CallExpr) *observedMetric {
	sel, ok := selectorCall(call)
	if !ok || !cfg.tier1Constructors[sel.Sel.Name] || !isOTelMeter(pass, sel.X) || len(call.Args) == 0 {
		return nil
	}

	name := stringLitValue(pass, call.Args[0])
	if name == "" {
		return nil
	}

	desc, unit, buckets := extractMetricOptions(pass, call.Args[1:])

	return &observedMetric{
		Name:           name,
		Description:    desc,
		Unit:           unit,
		InstrumentType: cfg.instrumentType,
		EmissionSites:  []schema.EmissionSite{siteFor(pass, call.Pos(), 1)},
		Buckets:        buckets,
	}
}

func matchTier2Metric(pass *analysis.Pass, cfg metricKindConfig, call *ast.CallExpr) *observedMetric {
	sel, ok := selectorCall(call)
	if !ok || sel.Sel.Name != cfg.tier2Method || !isMetricsFactory(pass, sel.X) || len(call.Args) == 0 {
		return nil
	}

	fields := extractMetricStructFields(pass, call.Args[0])
	if fields.Name == "" {
		return nil
	}

	return &observedMetric{
		Name:           fields.Name,
		Description:    fields.Description,
		Unit:           fields.Unit,
		InstrumentType: cfg.instrumentType,
		EmissionSites:  []schema.EmissionSite{siteFor(pass, call.Pos(), 2)},
		Buckets:        fields.Buckets,
	}
}

func matchTier3Metric(pass *analysis.Pass, cfg metricKindConfig, call *ast.CallExpr) *observedMetric {
	sel, ok := selectorCall(call)
	if !ok || !isMetricsFactory(pass, sel.X) {
		return nil
	}

	spec, ok := lookupHelperByMethod(sel.Sel.Name)
	if !ok || spec.InstrumentType != cfg.helperType {
		return nil
	}

	labels := append([]string(nil), spec.DefaultLabels...)
	// Tier-3 helpers come in two shapes formalized by spec.SignatureKind:
	//   SignatureAttributesVariadic: (ctx, ...attribute.KeyValue) — trailing
	//     args carry real labels; harvest them alongside DefaultLabels.
	//   SignatureScalarValue: (ctx, value <numeric>) — no trailing labels.
	// Without the branch every Tier-3 site reports labels=DefaultLabels
	// (variadic case) or misclassifies the scalar arg as label material.
	if spec.SignatureKind == libmetrics.SignatureAttributesVariadic && len(call.Args) > 1 {
		labels = append(labels, extractAttributeKeys(pass, call.Args[1:])...)
	}

	return &observedMetric{
		Name:           spec.MetricName,
		Description:    spec.Description,
		Unit:           spec.Unit,
		InstrumentType: cfg.instrumentType,
		EmissionSites:  []schema.EmissionSite{siteFor(pass, call.Pos(), 3)},
		Labels:         labels,
	}
}

func mergeMetricRecordLabels(pass *analysis.Pass, cfg metricKindConfig, call *ast.CallExpr, bound map[string]*observedMetric) {
	sel, ok := selectorCall(call)
	if !ok || !cfg.recordMethods[sel.Sel.Name] {
		return
	}

	base, receiverLabels := baseIdentAndLabels(pass, sel.X)
	if base == "" {
		return
	}

	p := bound[base]
	if p == nil {
		return
	}

	labels := append([]string{}, receiverLabels...)
	labels = append(labels, extractAttributeKeys(pass, call.Args)...)

	labels = uniqueSorted(labels)
	if len(labels) == 0 {
		return
	}

	p.Labels = mergeStrings(p.Labels, labels...)
	pass.Reportf(call.Pos(), "%s record site labels=%v", cfg.name, labels)
}

type metricStructFields struct {
	Name        string
	Description string
	Unit        string
	Buckets     []float64
}

func extractMetricStructFields(pass *analysis.Pass, expr ast.Expr) metricStructFields {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		// Lib-commons' canonical pattern is f.Counter(MetricSomething) where
		// MetricSomething is a package-level metrics.Metric var (see
		// commons/opentelemetry/metrics/system.go). Resolve the ident to its
		// declaring var and walk to its init composite literal.
		if id, ok := expr.(*ast.Ident); ok {
			if v, ok := pass.TypesInfo.Uses[id].(*types.Var); ok && v != nil {
				if resolved := resolveVarInitLit(pass, v); resolved != nil {
					lit = resolved
				}
			}
		}

		if lit == nil {
			return metricStructFields{}
		}
	}

	var fields metricStructFields

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "Name":
			fields.Name = stringLitValue(pass, kv.Value)
		case "Description":
			fields.Description = stringLitValue(pass, kv.Value)
		case "Unit":
			fields.Unit = stringLitValue(pass, kv.Value)
		case "Buckets":
			fields.Buckets = extractFloatSlice(pass, kv.Value)
		}
	}

	return fields
}

func extractMetricOptions(pass *analysis.Pass, args []ast.Expr) (string, string, []float64) {
	var (
		desc, unit string
		buckets    []float64
	)

	for _, arg := range args {
		call, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}

		sel, ok := selectorCall(call)
		if !ok || !selectorFromPackage(pass, sel, metricPkgPath) {
			continue
		}

		switch sel.Sel.Name {
		case "WithDescription":
			if len(call.Args) > 0 {
				desc = stringLitValue(pass, call.Args[0])
			}
		case "WithUnit":
			if len(call.Args) > 0 {
				unit = stringLitValue(pass, call.Args[0])
			}
		case "WithExplicitBucketBoundaries":
			buckets = extractFloatArgs(pass, call.Args)
		}
	}

	return desc, unit, buckets
}

func extractFloatArgs(pass *analysis.Pass, args []ast.Expr) []float64 {
	values := make([]float64, 0, len(args))

	for _, arg := range args {
		if v, ok := numericLitValue(pass, arg); ok {
			values = append(values, v)

			continue
		}

		// Slice expansion: WithExplicitBucketBoundaries(buckets...) where
		// buckets is a package-var []float64{...}. Resolve the ident and
		// recurse over the literal elements. Unresolvable idents (e.g.
		// cross-package) fall through silently.
		if id, ok := arg.(*ast.Ident); ok {
			if v, ok := pass.TypesInfo.Uses[id].(*types.Var); ok && v != nil {
				if resolved := resolveVarInitLit(pass, v); resolved != nil {
					values = append(values, extractFloatArgs(pass, resolved.Elts)...)
				}
			}
		}
	}

	sort.Float64s(values)

	return values
}

func extractFloatSlice(pass *analysis.Pass, expr ast.Expr) []float64 {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		// As with extractMetricStructFields, accept a package-var holding a
		// slice literal, e.g. var DefaultBuckets = []float64{1, 5, 10}.
		if id, ok := expr.(*ast.Ident); ok {
			if v, ok := pass.TypesInfo.Uses[id].(*types.Var); ok && v != nil {
				if resolved := resolveVarInitLit(pass, v); resolved != nil {
					lit = resolved
				}
			}
		}

		if lit == nil {
			return nil
		}
	}

	return extractFloatArgs(pass, lit.Elts)
}

// resolveVarInitLit walks the AST of pass.Files to find the *ast.ValueSpec
// where v was declared, and returns the *ast.CompositeLit assigned to v if
// any. Returns nil when v has no init value, the init is not a composite
// literal, or the declaring file is not in this pass (cross-package).
func resolveVarInitLit(pass *analysis.Pass, v *types.Var) *ast.CompositeLit {
	if v == nil {
		return nil
	}

	declPos := v.Pos()
	if declPos == token.NoPos {
		return nil
	}

	for _, file := range pass.Files {
		if file.Pos() > declPos || declPos > file.End() {
			continue
		}

		var found *ast.CompositeLit

		ast.Inspect(file, func(n ast.Node) bool {
			if found != nil {
				return false
			}

			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}

			for i, name := range spec.Names {
				if name.Pos() != declPos {
					continue
				}

				if i >= len(spec.Values) {
					return false
				}

				if lit, ok := spec.Values[i].(*ast.CompositeLit); ok {
					found = lit
				}

				return false
			}

			return true
		})

		if found != nil {
			return found
		}
	}

	return nil
}
