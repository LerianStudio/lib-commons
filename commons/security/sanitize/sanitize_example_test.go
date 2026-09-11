//go:build unit

package sanitize_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/security/sanitize"
	"github.com/jackc/pgx/v5/pgconn"
)

// A driver echoes the DSN it failed to dial. The message is what reaches the log
// line, so it is the message that gets scrubbed — the host stays, the password
// does not.
func ExampleString() {
	fmt.Println(sanitize.String("dial postgres://svc_user:s3cr3t@db.internal:5432/ledger?sslmode=require: refused"))

	// Output:
	// dial postgres://****:****@db.internal:5432/ledger?sslmode=require: refused
}

// Error is the logging-boundary wrapper: the message is redacted, and the chain
// underneath is untouched so the caller can still classify what happened.
func ExampleError() {
	pgErr := &pgconn.PgError{Code: "28P01", Message: `password authentication failed for user "svc"`}
	wrapped := fmt.Errorf("connect postgres://svc:s3cr3t@db.internal/ledger: %w", pgErr)

	safe := sanitize.Error(wrapped)

	fmt.Println("logged:", safe)

	// The cause is still reachable, which is the point: classify with it.
	var found *pgconn.PgError

	fmt.Println("still classifiable:", errors.As(safe, &found), found.Code)

	// Output:
	// logged: connect postgres://****:****@db.internal/ledger: : password authentication failed for user "svc" (SQLSTATE 28P01)
	// still classifiable: true 28P01
}

// A message with nothing to redact comes back verbatim, so wrapping every error
// at the boundary costs nothing in legibility.
func ExampleError_clean() {
	fmt.Println(sanitize.Error(errors.New("connection refused")))

	// Output:
	// connection refused
}
