//go:build unit

package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
)

// sqlStateCodes used across the table tests. They mirror the unexported
// constants in errors.go but are duplicated here to keep the test independent
// of internal naming.
const (
	codeUnique     = "23505"
	codeForeignKey = "23503"
	codeCheck      = "23514"
	codeUndefTable = "42P01"
	codeUnrelated  = "08006" // connection_failure — matches none of the predicates
)

// pgxErr builds a pgx *pgconn.PgError carrying the given SQLSTATE code.
func pgxErr(code string) error {
	return &pgconn.PgError{
		Code:           code,
		Message:        "pgx driver message",
		ConstraintName: "pgx_constraint",
	}
}

// pqErr builds a lib/pq *pq.Error carrying the given SQLSTATE code.
func pqErr(code string) error {
	return &pq.Error{
		Code:       pq.ErrorCode(code),
		Message:    "pq driver message",
		Constraint: "pq_constraint",
	}
}

// driverCase describes one driver flavour for table-driven coverage.
type driverCase struct {
	name           string
	build          func(code string) error
	wantConstraint string
	wantMessage    string
}

var driverCases = []driverCase{
	{
		name:           "pgx",
		build:          pgxErr,
		wantConstraint: "pgx_constraint",
		wantMessage:    "pgx driver message",
	},
	{
		name:           "lib/pq",
		build:          pqErr,
		wantConstraint: "pq_constraint",
		wantMessage:    "pq driver message",
	},
}

// wrapVariants returns the error itself and a wrapped chain so each assertion
// runs against both a direct error and one nested via fmt.Errorf("%w").
func wrapVariants(err error) []struct {
	name string
	err  error
} {
	return []struct {
		name string
		err  error
	}{
		{name: "direct", err: err},
		{name: "wrapped", err: fmt.Errorf("ctx: %w", err)},
	}
}

func TestPredicates_MatchingCode(t *testing.T) {
	t.Parallel()

	predicates := []struct {
		name string
		code string
		fn   func(error) bool
	}{
		{"IsUniqueViolation", codeUnique, IsUniqueViolation},
		{"IsForeignKeyViolation", codeForeignKey, IsForeignKeyViolation},
		{"IsCheckViolation", codeCheck, IsCheckViolation},
		{"IsUndefinedTable", codeUndefTable, IsUndefinedTable},
	}

	for _, dc := range driverCases {
		for _, p := range predicates {
			for _, v := range wrapVariants(dc.build(p.code)) {
				t.Run(dc.name+"/"+p.name+"/match/"+v.name, func(t *testing.T) {
					t.Parallel()
					assert.True(t, p.fn(v.err), "predicate must be true on its matching code")
				})
			}
		}
	}
}

func TestPredicates_NonMatchingCode(t *testing.T) {
	t.Parallel()

	predicates := []struct {
		name string
		fn   func(error) bool
	}{
		{"IsUniqueViolation", IsUniqueViolation},
		{"IsForeignKeyViolation", IsForeignKeyViolation},
		{"IsCheckViolation", IsCheckViolation},
		{"IsUndefinedTable", IsUndefinedTable},
	}

	for _, dc := range driverCases {
		for _, p := range predicates {
			for _, v := range wrapVariants(dc.build(codeUnrelated)) {
				t.Run(dc.name+"/"+p.name+"/nomatch/"+v.name, func(t *testing.T) {
					t.Parallel()
					assert.False(t, p.fn(v.err), "predicate must be false on a non-matching code")
				})
			}
		}
	}
}

// TestPredicates_CrossCodeRejection pins predicate exclusivity: each predicate
// must return false when handed a *different* recognized violation code, not just
// an unrelated one. The SQLSTATE constants are visually adjacent (23505 / 23503 /
// 23514), so a copy/paste bug pointing a predicate at the wrong const would pass
// TestPredicates_NonMatchingCode (which only uses the far-miss codeUnrelated) yet
// be caught here.
func TestPredicates_CrossCodeRejection(t *testing.T) {
	t.Parallel()

	predicates := []struct {
		name    string
		ownCode string
		fn      func(error) bool
	}{
		{"IsUniqueViolation", codeUnique, IsUniqueViolation},
		{"IsForeignKeyViolation", codeForeignKey, IsForeignKeyViolation},
		{"IsCheckViolation", codeCheck, IsCheckViolation},
		{"IsUndefinedTable", codeUndefTable, IsUndefinedTable},
	}
	otherCodes := []string{codeUnique, codeForeignKey, codeCheck, codeUndefTable}

	for _, dc := range driverCases {
		for _, p := range predicates {
			for _, code := range otherCodes {
				if code == p.ownCode {
					continue
				}

				for _, v := range wrapVariants(dc.build(code)) {
					t.Run(dc.name+"/"+p.name+"/rejects/"+code+"/"+v.name, func(t *testing.T) {
						t.Parallel()
						assert.False(t, p.fn(v.err),
							"predicate must be false for a different recognized violation code")
					})
				}
			}
		}
	}
}

func TestSQLState_Extract(t *testing.T) {
	t.Parallel()

	for _, dc := range driverCases {
		for _, v := range wrapVariants(dc.build(codeUnique)) {
			t.Run(dc.name+"/"+v.name, func(t *testing.T) {
				t.Parallel()

				code, ok := SQLState(v.err)
				assert.True(t, ok)
				assert.Equal(t, codeUnique, code)
			})
		}
	}
}

func TestConstraint_Extract(t *testing.T) {
	t.Parallel()

	for _, dc := range driverCases {
		for _, v := range wrapVariants(dc.build(codeUnique)) {
			t.Run(dc.name+"/"+v.name, func(t *testing.T) {
				t.Parallel()

				name, ok := Constraint(v.err)
				assert.True(t, ok)
				assert.Equal(t, dc.wantConstraint, name)
			})
		}
	}
}

func TestDriverMessage_Extract(t *testing.T) {
	t.Parallel()

	for _, dc := range driverCases {
		for _, v := range wrapVariants(dc.build(codeUnique)) {
			t.Run(dc.name+"/"+v.name, func(t *testing.T) {
				t.Parallel()

				msg, ok := DriverMessage(v.err)
				assert.True(t, ok)
				assert.Equal(t, dc.wantMessage, msg)
			})
		}
	}
}

func TestConstraint_EmptyIsAbsent(t *testing.T) {
	t.Parallel()

	pgx := &pgconn.PgError{Code: codeUnique}
	name, ok := Constraint(pgx)
	assert.False(t, ok, "empty pgx ConstraintName must report absent")
	assert.Empty(t, name)

	pqe := &pq.Error{Code: pq.ErrorCode(codeUnique)}
	name, ok = Constraint(pqe)
	assert.False(t, ok, "empty lib/pq Constraint must report absent")
	assert.Empty(t, name)
}

func TestDriverMessage_EmptyIsAbsent(t *testing.T) {
	t.Parallel()

	pgx := &pgconn.PgError{Code: codeUnique}
	msg, ok := DriverMessage(pgx)
	assert.False(t, ok, "empty pgx Message must report absent")
	assert.Empty(t, msg)

	pqe := &pq.Error{Code: pq.ErrorCode(codeUnique)}
	msg, ok = DriverMessage(pqe)
	assert.False(t, ok, "empty lib/pq Message must report absent")
	assert.Empty(t, msg)
}

func TestClassification_NilError(t *testing.T) {
	t.Parallel()

	assert.False(t, IsUniqueViolation(nil))
	assert.False(t, IsForeignKeyViolation(nil))
	assert.False(t, IsCheckViolation(nil))
	assert.False(t, IsUndefinedTable(nil))

	code, ok := SQLState(nil)
	assert.False(t, ok)
	assert.Empty(t, code)

	name, ok := Constraint(nil)
	assert.False(t, ok)
	assert.Empty(t, name)

	msg, ok := DriverMessage(nil)
	assert.False(t, ok)
	assert.Empty(t, msg)
}

func TestClassification_PlainError(t *testing.T) {
	t.Parallel()

	err := errors.New("boom")

	assert.False(t, IsUniqueViolation(err))
	assert.False(t, IsForeignKeyViolation(err))
	assert.False(t, IsCheckViolation(err))
	assert.False(t, IsUndefinedTable(err))

	code, ok := SQLState(err)
	assert.False(t, ok)
	assert.Empty(t, code)

	name, ok := Constraint(err)
	assert.False(t, ok)
	assert.Empty(t, name)

	msg, ok := DriverMessage(err)
	assert.False(t, ok)
	assert.Empty(t, msg)
}

// TestSQLState_DoesNotTraverseSanitizedError asserts — by design, NOT as a bug —
// that classification does not see through a *SanitizedError. newSanitizedError
// flattens its cause to a fresh errors.New(sanitizedMsg) (see sanitizedCause in
// postgres.go) precisely so the original connection error's type cannot re-expose
// the DSN through Unwrap. Because the driver error type is intentionally dropped,
// errors.As never finds the *pgconn.PgError, and IsUniqueViolation returns false.
// This is the credential-safety contract, not a classification gap: in practice
// SQLSTATE violations never flow through SanitizedError, which only wraps
// sql.Open connection errors (via newSanitizedError).
func TestSQLState_DoesNotTraverseSanitizedError(t *testing.T) {
	t.Parallel()

	pgErr := &pgconn.PgError{Code: codeUnique, ConstraintName: "uq_email", Message: "duplicate key"}
	se := newSanitizedError(pgErr, "open failed")

	assert.False(t, IsUniqueViolation(se),
		"classification must not traverse SanitizedError — the cause is flattened to a credential-free error by design")

	code, ok := SQLState(se)
	assert.False(t, ok, "SanitizedError carries no recoverable SQLSTATE by design")
	assert.Empty(t, code)
}
