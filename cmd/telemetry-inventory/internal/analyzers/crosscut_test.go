//go:build unit

package analyzers

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis/analysistest"
)

// TestIsErrNotNil covers both `err != nil` and `nil != err` orientations and
// rejects unrelated binary expressions. Pinned because the receiver-type guard
// in errorAttributionFindings depends on this predicate firing exactly when an
// error-check branch is being inspected.
func TestIsErrNotNil(t *testing.T) {
	cases := []struct {
		name string
		expr ast.Expr
		want bool
	}{
		{
			name: "err_neq_nil",
			expr: &ast.BinaryExpr{
				X:  &ast.Ident{Name: "err"},
				Op: token.NEQ,
				Y:  &ast.Ident{Name: "nil"},
			},
			want: true,
		},
		{
			name: "nil_neq_err",
			expr: &ast.BinaryExpr{
				X:  &ast.Ident{Name: "nil"},
				Op: token.NEQ,
				Y:  &ast.Ident{Name: "err"},
			},
			want: true,
		},
		{
			name: "foo_neq_bar_unrelated",
			expr: &ast.BinaryExpr{
				X:  &ast.Ident{Name: "foo"},
				Op: token.NEQ,
				Y:  &ast.Ident{Name: "bar"},
			},
			want: false,
		},
		{
			name: "err_eq_nil_wrong_op",
			expr: &ast.BinaryExpr{
				X:  &ast.Ident{Name: "err"},
				Op: token.EQL,
				Y:  &ast.Ident{Name: "nil"},
			},
			want: false,
		},
		{
			name: "non_binary_expr",
			expr: &ast.Ident{Name: "err"},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isErrNotNil(tc.expr); got != tc.want {
				t.Fatalf("isErrNotNil(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// runCrossCutAndCollect runs the CrossCutAnalyzer against pkg under testdata
// and returns the collected findings. CrossCutAnalyzer does not Reportf — it
// returns its findings via ResultType — so analysistest's `// want` directive
// path doesn't apply. We inspect Result directly instead.
func runCrossCutAndCollect(t *testing.T, pkg string) []schema.CrossCutFinding {
	t.Helper()

	results := analysistest.Run(t, analysistest.TestData(), CrossCutAnalyzer, pkg)
	if len(results) == 0 {
		t.Fatalf("analysistest returned no results for package %q", pkg)
	}

	var collected []schema.CrossCutFinding

	for _, r := range results {
		findings, ok := r.Result.(*CrossCutFindings)
		if !ok || findings == nil {
			t.Fatalf("Result for %q is not *CrossCutFindings: %T", pkg, r.Result)
		}

		collected = append(collected, findings.Issues...)
	}

	return collected
}

// TestCrossCutAnalyzer_TenantConsistency exercises the tenant_consistency
// branch directly via a focused fixture: a function that emits a tenant-
// scoped counter alongside a span without tenant_id attribute.
func TestCrossCutAnalyzer_TenantConsistency(t *testing.T) {
	got := runCrossCutAndCollect(t, "crosscut-tenant")

	var hits int
	for _, f := range got {
		if f.Kind == "tenant_consistency" {
			hits++
		}
	}

	if hits == 0 {
		t.Fatalf("expected at least one tenant_consistency finding; got %+v", got)
	}
}

// TestCrossCutAnalyzer_TraceCorrelation exercises the trace_correlation
// branch: a function with a histogram (or log) but no enclosing span.
func TestCrossCutAnalyzer_TraceCorrelation(t *testing.T) {
	got := runCrossCutAndCollect(t, "crosscut-trace")

	var hits int
	for _, f := range got {
		if f.Kind == "trace_correlation" {
			hits++
		}
	}

	if hits == 0 {
		t.Fatalf("expected at least one trace_correlation finding; got %+v", got)
	}
}

// TestCrossCutAnalyzer_ErrorAttribution exercises the error_attribution
// branch: an `if err != nil { ... }` block missing SetStatus and the error
// log call. The analyzer must surface a finding listing both as missing.
func TestCrossCutAnalyzer_ErrorAttribution(t *testing.T) {
	got := runCrossCutAndCollect(t, "crosscut-error-attribution")

	var hit *schema.CrossCutFinding
	for i := range got {
		if got[i].Kind == "error_attribution" {
			hit = &got[i]
			break
		}
	}

	if hit == nil {
		t.Fatalf("expected an error_attribution finding; got %+v", got)
	}

	for _, want := range []string{"span.SetStatus", "error log"} {
		if !strings.Contains(hit.Detail, want) {
			t.Fatalf("expected error_attribution detail to mention %q; got %q", want, hit.Detail)
		}
	}
}
