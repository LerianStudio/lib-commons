//go:build unit

package analyzers

import (
	"go/ast"
	"go/token"
	"testing"
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
