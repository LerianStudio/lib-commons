package analyzers

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"sort"
	"strconv"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	libmetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"golang.org/x/tools/go/analysis"
)

const (
	attributePkgPath = "go.opentelemetry.io/otel/attribute"
	metricPkgPath    = "go.opentelemetry.io/otel/metric"
	tracePkgPath     = "go.opentelemetry.io/otel/trace"
	metricsPkgPath   = "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	logPkgPath       = "github.com/LerianStudio/lib-commons/v5/commons/log"
	zapPkgPath       = "github.com/LerianStudio/lib-commons/v5/commons/zap"

	instrumentCounter   = "counter"
	instrumentHistogram = "histogram"
	instrumentGauge     = "gauge"

	tenantIDLabel = "tenant_id"

	withLabelsMethod     = "WithLabels"
	withAttributesMethod = "WithAttributes"
	logMethodName        = "Log"
)

// helperByMethod is the GoFunctionName→HelperSpec lookup table consumed by
// Tier-3 metric matching. It is populated eagerly at package init using the
// libmetrics.Helpers slice. ValidateHelperRegistry repeats the build with
// duplicate detection so the binary entry point (main.run) can refuse to
// start if the registry is malformed; the metrics package's
// TestHelpers_NoDuplicateGoFunctionName enforces the same invariant at
// `make ci` time.
var helperByMethod = buildHelperRegistry()

// buildHelperRegistry copies libmetrics.Helpers into a map keyed by
// GoFunctionName. Duplicates are not detected here — ValidateHelperRegistry
// is the explicit validator. Both production startup and the test suite
// already reject duplicates, so this is the unambiguous fast path.
func buildHelperRegistry() map[string]libmetrics.HelperSpec {
	out := make(map[string]libmetrics.HelperSpec, len(libmetrics.Helpers))
	for _, h := range libmetrics.Helpers {
		out[h.GoFunctionName] = h
	}

	return out
}

// ValidateHelperRegistry returns an error if libmetrics.Helpers declares the
// same Go method name more than once — a malformed registry would silently
// overwrite entries and produce wrong analyzer output. main.run calls this
// once before subcommand dispatch and exits non-zero on error.
func ValidateHelperRegistry() error {
	seen := make(map[string]struct{}, len(libmetrics.Helpers))
	for _, h := range libmetrics.Helpers {
		if _, exists := seen[h.GoFunctionName]; exists {
			return fmt.Errorf("duplicate helper registry entry: %s", h.GoFunctionName)
		}

		seen[h.GoFunctionName] = struct{}{}
	}

	return nil
}

// ErrHelperRegistryNotInitialized is reserved for callers that need to
// distinguish an unbuilt registry from a missing helper. The current
// package-level helperByMethod is built at init, so lookups never hit the
// uninitialized branch — the sentinel exists for future plumbing.
var ErrHelperRegistryNotInitialized = errors.New("analyzers: helper registry not initialized")

// lookupHelperByMethod returns the HelperSpec for a given Go method name.
// The second return is false when the method is not a registered helper.
func lookupHelperByMethod(name string) (libmetrics.HelperSpec, bool) {
	spec, ok := helperByMethod[name]

	return spec, ok
}

func siteFor(pass *analysis.Pass, pos token.Pos, tier int) schema.EmissionSite {
	p := pass.Fset.Position(pos)
	return schema.EmissionSite{File: p.Filename, Line: p.Line, Tier: tier}
}

func stringLitValue(pass *analysis.Pass, expr ast.Expr) string {
	if tv, ok := pass.TypesInfo.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value)
	}

	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}

	out, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}

	return out
}

func numericLitValue(pass *analysis.Pass, expr ast.Expr) (float64, bool) {
	if tv, ok := pass.TypesInfo.Types[expr]; ok && tv.Value != nil {
		v, exact := constant.Float64Val(tv.Value)
		return v, exact
	}

	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return 0, false
	}

	v, err := strconv.ParseFloat(lit.Value, 64)
	if err != nil {
		return 0, false
	}

	return v, true
}

func selectorCall(call *ast.CallExpr) (*ast.SelectorExpr, bool) {
	if call == nil {
		return nil, false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)

	return sel, ok
}

func importedPkgPath(pass *analysis.Pass, expr ast.Expr) string {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return ""
	}

	obj, ok := pass.TypesInfo.Uses[id].(*types.PkgName)
	if !ok || obj.Imported() == nil {
		return ""
	}

	return obj.Imported().Path()
}

func selectorFromPackage(pass *analysis.Pass, sel *ast.SelectorExpr, pkgPath string) bool {
	return importedPkgPath(pass, sel.X) == pkgPath
}

func namedType(t types.Type) *types.Named {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}

	named, _ := t.(*types.Named)

	return named
}

func isNamedType(t types.Type, pkgPath, name string) bool {
	named := namedType(t)

	return named != nil && named.Obj() != nil && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == pkgPath && named.Obj().Name() == name
}

func exprType(pass *analysis.Pass, expr ast.Expr) types.Type {
	if tv, ok := pass.TypesInfo.Types[expr]; ok {
		return tv.Type
	}

	return nil
}

func isOTelMeter(pass *analysis.Pass, expr ast.Expr) bool {
	return isNamedType(exprType(pass, expr), metricPkgPath, "Meter")
}

func isOTelTracer(pass *analysis.Pass, expr ast.Expr) bool {
	return isNamedType(exprType(pass, expr), tracePkgPath, "Tracer")
}

func isMetricsFactory(pass *analysis.Pass, expr ast.Expr) bool {
	return isNamedType(exprType(pass, expr), metricsPkgPath, "MetricsFactory")
}

func extractAttributeKeys(pass *analysis.Pass, args []ast.Expr) []string {
	keys := make([]string, 0, len(args))
	for _, arg := range args {
		keys = append(keys, extractAttributeKeysFromExpr(pass, arg)...)
	}

	return uniqueSorted(keys)
}

func extractAttributeKeysFromExpr(pass *analysis.Pass, expr ast.Expr) []string {
	values := extractAttributeKeyValuesFromExpr(pass, expr)

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}

	return uniqueSorted(keys)
}

func extractAttributeKeyValuesFromExpr(pass *analysis.Pass, expr ast.Expr) map[string]string {
	out := map[string]string{}

	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return out
	}

	sel, ok := selectorCall(call)
	if !ok {
		return out
	}

	if sel.Sel.Name == withAttributesMethod || sel.Sel.Name == "SetAttributes" {
		for _, arg := range call.Args {
			maps.Copy(out, extractAttributeKeyValuesFromExpr(pass, arg))
		}

		return out
	}

	if sel.Sel.Name == withLabelsMethod {
		for _, arg := range call.Args {
			for _, k := range extractMapStringKeys(pass, arg) {
				out[k] = ""
			}
		}

		return out
	}

	if selectorFromPackage(pass, sel, attributePkgPath) && len(call.Args) > 0 {
		key := stringLitValue(pass, call.Args[0])
		if key == "" {
			return out
		}

		value := ""
		if len(call.Args) > 1 {
			value = stringLitValue(pass, call.Args[1])
		}

		out[key] = value
	}

	return out
}

// extractMapStringKeys harvests literal string keys from a map[string]…
// composite literal. lib-commons builders expose .WithLabels(map[string]string)
// alongside .WithAttributes(...attribute.KeyValue); both carry label keys we
// want to surface.
func extractMapStringKeys(pass *analysis.Pass, expr ast.Expr) []string {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}

	if mapType, ok := lit.Type.(*ast.MapType); ok {
		if id, ok := mapType.Key.(*ast.Ident); !ok || id.Name != "string" {
			return nil
		}
	}

	keys := make([]string, 0, len(lit.Elts))

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		if k := stringLitValue(pass, kv.Key); k != "" {
			keys = append(keys, k)
		}
	}

	return keys
}

func extractLogFieldKeys(pass *analysis.Pass, args []ast.Expr) []string {
	keys := make([]string, 0)

	for _, arg := range args {
		call, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}

		sel, ok := selectorCall(call)
		if !ok || len(call.Args) == 0 {
			continue
		}

		pkg := importedPkgPath(pass, sel.X)
		if pkg != logPkgPath && pkg != zapPkgPath {
			continue
		}

		if sel.Sel.Name == "Err" || sel.Sel.Name == "ErrorField" {
			keys = append(keys, "error")
			continue
		}

		if k := stringLitValue(pass, call.Args[0]); k != "" {
			keys = append(keys, k)
		}
	}

	return uniqueSorted(keys)
}

func baseIdentAndLabels(pass *analysis.Pass, expr ast.Expr) (string, []string) {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name, nil
	case *ast.CallExpr:
		sel, ok := selectorCall(x)
		if !ok || (sel.Sel.Name != withAttributesMethod && sel.Sel.Name != withLabelsMethod) {
			return "", nil
		}

		base, nested := baseIdentAndLabels(pass, sel.X)
		labels := append([]string{}, nested...)

		if sel.Sel.Name == withLabelsMethod {
			for _, arg := range x.Args {
				labels = append(labels, extractMapStringKeys(pass, arg)...)
			}
		} else {
			labels = append(labels, extractAttributeKeys(pass, x.Args)...)
		}

		return base, uniqueSorted(labels)
	default:
		return "", nil
	}
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := map[string]bool{}

	for _, v := range values {
		if v != "" {
			seen[v] = true
		}
	}

	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

func mergeStrings(existing []string, next ...string) []string {
	return uniqueSorted(append(existing, next...))
}

func containsString(values []string, needle string) bool {
	return slices.Contains(values, needle)
}
