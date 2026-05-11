package analyzers

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// otelPkgPath is the canonical lib-commons opentelemetry helper package; matched by
// errorAttributionFindings so that opentelemetry.HandleSpanError is recognized as
// satisfying both span.RecordError and span.SetStatus.
const otelPkgPath = "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"

// CrossCutAnalyzer detects consistency gaps across metrics, spans, and logs.
var CrossCutAnalyzer = &analysis.Analyzer{
	Name: "crosscut",
	Doc:  "Detects tenant, error-attribution, and trace-correlation telemetry gaps.",
	Run:  runCrossCut,
	Requires: []*analysis.Analyzer{
		inspect.Analyzer,
		CounterAnalyzer,
		HistogramAnalyzer,
		GaugeAnalyzer,
		SpanAnalyzer,
		LogFieldAnalyzer,
	},
	ResultType: reflect.TypeFor[*CrossCutFindings](),
}

type functionRange struct {
	name      string
	file      string
	startLine int
	endLine   int
}

type primitiveSite struct {
	kind   string
	name   string
	site   schema.EmissionSite
	labels []string
}

type functionBundle struct {
	fn         functionRange
	metrics    []primitiveSite
	histograms []primitiveSite
	spans      []primitiveSite
	logs       []primitiveSite
}

//nolint:gocognit,gocyclo // Cross-cut aggregation is explicit so each dependency mapping stays auditable.
func runCrossCut(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok || insp == nil {
		return &CrossCutFindings{}, nil
	}

	functions := collectFunctionRanges(pass, insp)
	functionsByFile := indexFunctionsByFile(functions)
	bundles := map[string]*functionBundle{}
	getBundle := func(site schema.EmissionSite) *functionBundle {
		fn := enclosingFunction(functionsByFile, site)
		if fn.name == "" {
			return nil
		}

		key := fn.file + ":" + fn.name

		b := bundles[key]
		if b == nil {
			b = &functionBundle{fn: fn}
			bundles[key] = b
		}

		return b
	}

	if counters, ok := pass.ResultOf[CounterAnalyzer].(*CounterFindings); ok {
		for _, c := range counters.Counters {
			for _, site := range c.EmissionSites {
				if b := getBundle(site); b != nil {
					b.metrics = append(b.metrics, primitiveSite{kind: instrumentCounter, name: c.Name, site: site, labels: c.Labels})
				}
			}
		}
	}

	if histograms, ok := pass.ResultOf[HistogramAnalyzer].(*HistogramFindings); ok {
		for _, h := range histograms.Histograms {
			for _, site := range h.EmissionSites {
				if b := getBundle(site); b != nil {
					p := primitiveSite{kind: instrumentHistogram, name: h.Name, site: site, labels: h.Labels}
					b.metrics = append(b.metrics, p)
					b.histograms = append(b.histograms, p)
				}
			}
		}
	}

	if gauges, ok := pass.ResultOf[GaugeAnalyzer].(*GaugeFindings); ok {
		for _, g := range gauges.Gauges {
			for _, site := range g.EmissionSites {
				if b := getBundle(site); b != nil {
					b.metrics = append(b.metrics, primitiveSite{kind: instrumentGauge, name: g.Name, site: site, labels: g.Labels})
				}
			}
		}
	}

	if spans, ok := pass.ResultOf[SpanAnalyzer].(*SpanFindings); ok {
		for _, s := range spans.Spans {
			for _, site := range s.EmissionSites {
				if b := getBundle(site); b != nil {
					b.spans = append(b.spans, primitiveSite{kind: "span", name: s.Name, site: site, labels: s.Attributes})
				}
			}
		}
	}

	if fields, ok := pass.ResultOf[LogFieldAnalyzer].(*LogFieldFindings); ok {
		for _, f := range fields.Fields {
			for _, site := range f.EmissionSites {
				if b := getBundle(site); b != nil {
					b.logs = append(b.logs, primitiveSite{kind: "log_field", name: f.Name, site: site, labels: []string{f.Name}})
				}
			}
		}
	}

	out := &CrossCutFindings{}
	for _, b := range bundles {
		out.Issues = append(out.Issues, tenantConsistencyFindings(pass, b)...)
		out.Issues = append(out.Issues, traceCorrelationFindings(pass, b)...)
	}

	out.Issues = append(out.Issues, errorAttributionFindings(pass, functionsByFile)...)

	return out, nil
}

func collectFunctionRanges(pass *analysis.Pass, insp *inspector.Inspector) []functionRange {
	var ranges []functionRange

	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn, _ := n.(*ast.FuncDecl)
		if fn == nil || fn.Body == nil {
			return
		}

		start := pass.Fset.Position(fn.Pos())
		end := pass.Fset.Position(fn.End())
		ranges = append(ranges, functionRange{name: fn.Name.Name, file: start.Filename, startLine: start.Line, endLine: end.Line})
	})
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].file != ranges[j].file {
			return ranges[i].file < ranges[j].file
		}

		return ranges[i].startLine < ranges[j].startLine
	})

	return ranges
}

// indexFunctionsByFile buckets the flat function-range slice produced by
// collectFunctionRanges into a per-file map. Each per-file slice is sorted by
// startLine (collectFunctionRanges already sorts globally by (file, startLine),
// so the per-file order survives the partition). Callers use enclosingFunction
// for O(log F) lookup per site instead of the prior O(S·F) linear scan.
func indexFunctionsByFile(functions []functionRange) map[string][]functionRange {
	out := make(map[string][]functionRange)
	for _, fn := range functions {
		out[fn.file] = append(out[fn.file], fn)
	}

	return out
}

// enclosingFunction binary-searches the per-file bucket for the FuncDecl
// whose [startLine, endLine] range contains site.Line. collectFunctionRanges
// only walks *ast.FuncDecl nodes (Go FuncDecls cannot nest — only FuncLits
// can), so each line in a file is covered by at most one FuncDecl; the
// walk-back from sort.Search yields either zero or one match.
func enclosingFunction(functionsByFile map[string][]functionRange, site schema.EmissionSite) functionRange {
	bucket := functionsByFile[site.File]
	if len(bucket) == 0 {
		return functionRange{}
	}

	// sort.Search returns the smallest index where startLine > site.Line; the
	// candidate enclosing FuncDecl, if any, is at index idx-1.
	idx := sort.Search(len(bucket), func(i int) bool {
		return bucket[i].startLine > site.Line
	})

	// Walk back to find the FuncDecl whose range contains site.Line. At most
	// one match exists per file because FuncDecls cannot overlap; the loop
	// returns as soon as it finds it.
	for i := idx - 1; i >= 0; i-- {
		fn := bucket[i]
		if fn.startLine <= site.Line && site.Line <= fn.endLine {
			return fn
		}
	}

	return functionRange{}
}

func tenantConsistencyFindings(pass *analysis.Pass, b *functionBundle) []schema.CrossCutFinding {
	metricTenant := false

	for _, p := range b.metrics {
		if containsString(p.labels, tenantIDLabel) {
			metricTenant = true
			break
		}
	}

	if !metricTenant {
		return nil
	}

	var out []schema.CrossCutFinding

	for _, span := range b.spans {
		if !containsString(span.labels, tenantIDLabel) {
			out = append(out, crossCut(pass, "tenant_consistency", span.site, b.fn.name, "metric is tenant-scoped but span lacks tenant_id attribute"))
		}
	}

	logHasTenant := false

	for _, logField := range b.logs {
		if logField.name == tenantIDLabel {
			logHasTenant = true
			break
		}
	}

	if len(b.logs) > 0 && !logHasTenant {
		out = append(out, crossCut(pass, "tenant_consistency", b.logs[0].site, b.fn.name, "metric is tenant-scoped but logs lack tenant_id field"))
	}

	return out
}

func traceCorrelationFindings(pass *analysis.Pass, b *functionBundle) []schema.CrossCutFinding {
	if len(b.spans) > 0 {
		return nil
	}

	var out []schema.CrossCutFinding
	for _, logField := range b.logs {
		out = append(out, crossCut(pass, "trace_correlation", logField.site, b.fn.name, "log field emitted without an active span in the same function"))
	}

	for _, histogram := range b.histograms {
		out = append(out, crossCut(pass, "trace_correlation", histogram.site, b.fn.name, "histogram emitted without an active span in the same function"))
	}

	return out
}

// errorAttributionCounts walks the body of an `if err != nil { ... }` block
// and returns whether each of the three canonical error-attribution actions
// is observed: span.RecordError, span.SetStatus, and a structured log call.
// commons/opentelemetry.HandleSpanError counts as both Record and SetStatus.
func errorAttributionCounts(pass *analysis.Pass, body *ast.BlockStmt) (hasRecord, hasStatus, hasLog bool) {
	ast.Inspect(body, func(child ast.Node) bool {
		call, ok := child.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := selectorCall(call)
		if !ok {
			return true
		}

		switch sel.Sel.Name {
		case "RecordError":
			hasRecord = true
		case "SetStatus":
			hasStatus = true
		case "HandleSpanError":
			if selectorFromPackage(pass, sel, otelPkgPath) {
				hasRecord = true
				hasStatus = true
			}
		case logMethodName, "Error", "Debug", "Info", "Warn":
			if isLogger(pass, sel.X) {
				hasLog = true
			}
		}

		return true
	})

	return hasRecord, hasStatus, hasLog
}

func errorAttributionFindings(pass *analysis.Pass, functionsByFile map[string][]functionRange) []schema.CrossCutFinding {
	var out []schema.CrossCutFinding

	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			stmt, ok := n.(*ast.IfStmt)
			if !ok || !isErrNotNil(stmt.Cond) {
				return true
			}

			hasRecord, hasStatus, hasLog := errorAttributionCounts(pass, stmt.Body)

			if hasRecord && hasStatus && hasLog {
				return true
			}

			pos := pass.Fset.Position(stmt.Pos())
			fn := enclosingFunction(functionsByFile, schema.EmissionSite{File: pos.Filename, Line: pos.Line})

			missing := []string{}
			if !hasRecord {
				missing = append(missing, "span.RecordError")
			}

			if !hasStatus {
				missing = append(missing, "span.SetStatus")
			}

			if !hasLog {
				missing = append(missing, "error log")
			}

			out = append(out, crossCut(pass, "error_attribution", schema.EmissionSite{File: pos.Filename, Line: pos.Line}, fn.name, "error branch missing "+strings.Join(missing, ", ")))

			return true
		})
	}

	return out
}

// isErrNotNil reports whether expr is exactly `err != nil` (in either
// operand order). The match is intentionally narrow:
//
//   - Compound conditions like `err != nil && shouldLog` do not match.
//   - Differently-named error variables (`myErr != nil`) do not match.
//   - Pointer comparisons (`x != nil` for non-error x) do not match.
//
// This is the right scope for error-attribution analysis: the analyzer is
// looking for canonical idiomatic error handling at the if-statement guard
// level, not for every possible error path. A wider matcher would surface
// false positives that drown out the real signal.
func isErrNotNil(expr ast.Expr) bool {
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok || binary.Op != token.NEQ {
		return false
	}

	left, leftOK := binary.X.(*ast.Ident)
	right, rightOK := binary.Y.(*ast.Ident)

	return (leftOK && left.Name == "err" && rightOK && right.Name == "nil") ||
		(rightOK && right.Name == "err" && leftOK && left.Name == "nil")
}

func crossCut(_ *analysis.Pass, kind string, site schema.EmissionSite, function, detail string) schema.CrossCutFinding {
	return schema.CrossCutFinding{Kind: kind, Site: site, Function: function, Detail: detail}
}

// isLogger returns true when expr's resolved type is the lib-commons
// commons/log.Logger interface (or implements it). Used to gate selector-name
// matches on Log/Error/Debug/Info/Warn so that builtin .Error() on the
// stdlib error interface is not misread as a structured log call.
func isLogger(pass *analysis.Pass, expr ast.Expr) bool {
	t := exprType(pass, expr)
	if t == nil {
		return false
	}

	if isNamedType(t, logPkgPath, "Logger") {
		return true
	}

	// Accept any type that implements commons/log.Logger (e.g. concrete
	// adapters like *zap.Logger). We do this by structurally checking that
	// the type has the canonical Logger methods — interface-implementation
	// is too expensive to compute without the full type-checker package
	// machinery, so we fall back to method-name set matching.
	named := namedType(t)
	if named == nil {
		return false
	}

	required := []string{"Log", "With", "WithGroup", "Enabled", "Sync"}
	missing := 0

	for _, m := range required {
		obj, _, _ := types.LookupFieldOrMethod(t, true, named.Obj().Pkg(), m)
		if obj == nil {
			missing++
		}
	}

	return missing == 0
}
