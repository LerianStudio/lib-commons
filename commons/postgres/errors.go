package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// PostgreSQL SQLSTATE codes recognized by the classification helpers.
// See https://www.postgresql.org/docs/current/errcodes-appendix.html.
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	checkViolation      = "23514"
	undefinedTable      = "42P01"
)

// SQLState returns the PostgreSQL SQLSTATE code carried by err, unwrapping
// both pgx (*pgconn.PgError) and lib/pq (*pq.Error) errors. The bool is false
// for nil errors and for errors that carry no SQLSTATE.
func SQLState(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code), true
	}

	return "", false
}

// Constraint returns the violated constraint name from err (pgx or lib/pq).
// The bool is false when nil / absent.
func Constraint(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.ConstraintName == "" {
			return "", false
		}

		return pgErr.ConstraintName, true
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		if pqErr.Constraint == "" {
			return "", false
		}

		return pqErr.Constraint, true
	}

	return "", false
}

// DriverMessage returns the raw driver message from err (pgx or lib/pq).
// The bool is false when nil / absent.
//
// The raw message can embed row or parameter values (e.g. the duplicated key
// in a unique_violation), which may be PII or financial data. Do NOT log it or
// return it to clients unredacted; prefer SQLState / Constraint for control flow.
func DriverMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Message == "" {
			return "", false
		}

		return pgErr.Message, true
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		if pqErr.Message == "" {
			return "", false
		}

		return pqErr.Message, true
	}

	return "", false
}

// matchesSQLState reports whether err carries the given SQLSTATE code.
func matchesSQLState(err error, code string) bool {
	state, ok := SQLState(err)

	return ok && state == code
}

// IsUniqueViolation reports whether err is a unique_violation (23505).
func IsUniqueViolation(err error) bool {
	return matchesSQLState(err, uniqueViolation)
}

// IsForeignKeyViolation reports whether err is a foreign_key_violation (23503).
func IsForeignKeyViolation(err error) bool {
	return matchesSQLState(err, foreignKeyViolation)
}

// IsCheckViolation reports whether err is a check_violation (23514).
func IsCheckViolation(err error) bool {
	return matchesSQLState(err, checkViolation)
}

// IsUndefinedTable reports whether err is an undefined_table (42P01).
func IsUndefinedTable(err error) bool {
	return matchesSQLState(err, undefinedTable)
}
