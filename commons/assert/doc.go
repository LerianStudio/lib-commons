// Package assert provides always-on runtime assertions for detecting programming bugs.
//
// Unlike test assertions, these assertions are intended to remain enabled in production
// code. They are designed for detecting invariant violations, programming errors, and
// impossible states - NOT for input validation or expected error conditions.
//
// # Design Philosophy
//
// Assertions are for catching bugs, not for handling user input:
//
//   - Use assertions for conditions that should NEVER be false if the code is correct
//   - Use error returns for conditions that CAN legitimately fail (I/O, user input, etc.)
//   - Assertions return errors so callers can stop execution immediately
//
// Good assertion usage:
//
//	a := assert.New(ctx, logger, "transaction", "create")
//	if err := a.NotNil(ctx, config, "config must be loaded before server starts"); err != nil {
//	    return err
//	}
//	if err := a.That(ctx, len(items) > 0, "processItems called with empty slice"); err != nil {
//	    return err
//	}
//
// Bad assertion usage (use error returns instead):
//
//	// DON'T: User input validation
//	_ = a.That(ctx, email != "", "email is required") // Use validation errors
//
//	// DON'T: I/O that can fail
//	_ = a.NoError(ctx, file.Read(), "file must read") // Use proper error handling
//
// # Core Assertion Methods
//
// The package provides five core assertion methods on Asserter:
//
//	a.That(ctx context.Context, ok bool, msg string, kv ...any) error
//	    Returns an error if ok is false. General-purpose assertion.
//
//	a.NotNil(ctx context.Context, v any, msg string, kv ...any) error
//	    Returns an error if v is nil. Handles both untyped nil and typed nil (nil interface
//	    values with concrete types).
//
//	a.NotEmpty(ctx context.Context, s string, msg string, kv ...any) error
//	    Returns an error if s is an empty string.
//
//	a.NoError(ctx context.Context, err error, msg string, kv ...any) error
//	    Returns an error if err is not nil. Automatically includes the error in context.
//
//	a.Never(ctx context.Context, msg string, kv ...any) error
//	    Always returns an error. Use for unreachable code paths.
//
// # Key-Value Context
//
// All assertion methods accept optional key-value pairs to provide context
// in logs and errors:
//
//	if err := a.That(ctx, balance >= 0, "balance must not be negative",
//	    "account_id", accountID,
//	    "balance", balance,
//	); err != nil {
//	    return err
//	}
//
// The error message will include:
//
//	assertion failed: balance must not be negative
//	    assertion=That
//	    account_id=550e8400-e29b-41d4-a716-446655440000
//	    balance=-100
//
// Odd numbers of key-value arguments are handled gracefully with a "MISSING_VALUE" marker.
//
// # Domain Predicates
//
// The package includes predicate functions for common domain validations:
//
//	// Numeric predicates
//	assert.Positive(n int64) bool        // n > 0
//	assert.NonNegative(n int64) bool     // n >= 0
//	assert.NotZero(n int64) bool         // n != 0
//	assert.InRange(n, min, max int64) bool // min <= n <= max
//
//	// String predicates
//	assert.ValidUUID(s string) bool      // valid UUID format
//
//	// Financial predicates (using shopspring/decimal)
//	assert.ValidAmount(d decimal.Decimal) bool    // exponent in [-18, 18]
//	assert.ValidScale(scale int) bool             // scale in [0, 18]
//	assert.PositiveDecimal(d decimal.Decimal) bool    // d > 0
//	assert.NonNegativeDecimal(d decimal.Decimal) bool // d >= 0
//
// Use predicates with Asserter:
//
//	if err := a.That(ctx, assert.Positive(count), "count must be positive", "count", count); err != nil {
//	    return err
//	}
//	if err := a.That(ctx, assert.ValidUUID(id), "invalid UUID", "id", id); err != nil {
//	    return err
//	}
//
// # Usage Examples
//
// Pre-conditions (validate inputs at function entry):
//
//	func ProcessTransaction(ctx context.Context, tx *Transaction) error {
//	    a := assert.New(ctx, logger, "transaction", "process")
//	    if err := a.NotNil(ctx, tx, "transaction must not be nil"); err != nil {
//	        return err
//	    }
//	    if err := a.NotEmpty(ctx, tx.ID, "transaction must have ID", "tx", tx); err != nil {
//	        return err
//	    }
//	    // ... rest of function
//	}
//
// Post-conditions (validate outputs before return):
//
//	func CreateAccount(ctx context.Context, name string) (*Account, error) {
//	    a := assert.New(ctx, logger, "account", "create")
//	    acc := &Account{ID: uuid.New(), Name: name}
//	    if err := a.NotNil(ctx, acc.ID, "created account must have ID"); err != nil {
//	        return nil, err
//	    }
//	    return acc, nil
//	}
//
// Unreachable code:
//
//	switch status {
//	case Active:
//	    return handleActive()
//	case Inactive:
//	    return handleInactive()
//	case Deleted:
//	    return handleDeleted()
//	default:
//	    return a.Never(ctx, "unhandled status", "status", status)
//	}
//
// # Goroutine Halting
//
// In goroutines, use Halt to stop execution after a failed assertion:
//
//	go func() {
//	    a := assert.New(ctx, logger, "transaction", "sync")
//	    if err := a.That(ctx, ready, "sync not ready"); err != nil {
//	        a.Halt(err)
//	    }
//	    // ... rest of goroutine
//	}()
//
// # Observability Integration
//
// Failed assertions emit telemetry signals:
//
//  1. Metrics: Records assertion_failed_total with component/operation/assertion labels.
//     Initialize with InitAssertionMetrics(metricsFactory).
//
//  2. Tracing: Records assertion.failed span events (with stack traces in non-prod).
//     Automatically uses the span from the context.
//
// # Stack Traces
//
// Stack traces are included in logs and trace events only in non-production
// environments (ENV != production and GO_ENV != production).
package assert
