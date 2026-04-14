package assert

import (
	"strconv"
	"strings"
	"time"

	txn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Positive returns true if n > 0.
//
// Example:
//
//	a.That(ctx, assert.Positive(count), "count must be positive", "count", count)
func Positive(n int64) bool {
	return n > 0
}

// NonNegative returns true if n >= 0.
//
// Example:
//
//	a.That(ctx, assert.NonNegative(balance), "balance must not be negative", "balance", balance)
func NonNegative(n int64) bool {
	return n >= 0
}

// NotZero returns true if n != 0.
//
// Example:
//
//	a.That(ctx, assert.NotZero(divisor), "divisor must not be zero", "divisor", divisor)
func NotZero(n int64) bool {
	return n != 0
}

// InRange returns true if min <= n <= max.
//
// Note: If min > max (inverted range), always returns false. This is fail-safe
// behavior - callers should ensure min <= max for correct results.
//
// Example:
//
//	a.That(ctx, assert.InRange(page, 1, 1000), "page out of range", "page", page)
func InRange(n, minVal, maxVal int64) bool {
	return n >= minVal && n <= maxVal
}

// ValidUUID returns true if s is a valid UUID string.
//
// Note: Accepts both canonical (with hyphens) and non-canonical (without hyphens)
// UUID formats per RFC 4122. Empty strings return false.
//
// Example:
//
//	a.That(ctx, assert.ValidUUID(id), "invalid UUID format", "id", id)
func ValidUUID(s string) bool {
	if s == "" {
		return false
	}

	_, err := uuid.Parse(s)

	return err == nil
}

// ValidAmount returns true if the decimal's exponent is within reasonable bounds.
// The exponent must be in the range [-18, 18] to align with supported precision
// for financial calculations (scale up to 18 decimal places).
//
// Note: This validates exponent bounds only, not coefficient size. For user-facing
// validation, consider additional bounds checks on the coefficient.
//
// Example:
//
//	a.That(ctx, assert.ValidAmount(amount), "amount has invalid precision", "amount", amount)
func ValidAmount(amount decimal.Decimal) bool {
	exp := amount.Exponent()
	return exp >= -18 && exp <= 18
}

// ValidScale returns true if scale is in the range [0, 18].
// Scale represents the number of decimal places for financial amounts.
//
// Example:
//
//	a.That(ctx, assert.ValidScale(scale), "invalid scale", "scale", scale)
func ValidScale(scale int) bool {
	return scale >= 0 && scale <= 18
}

// PositiveDecimal returns true if amount > 0.
//
// Example:
//
//	a.That(ctx, assert.PositiveDecimal(price), "price must be positive", "price", price)
func PositiveDecimal(amount decimal.Decimal) bool {
	return amount.IsPositive()
}

// NonNegativeDecimal returns true if amount >= 0.
//
// Example:
//
//	a.That(ctx, assert.NonNegativeDecimal(balance), "balance must not be negative", "balance", balance)
func NonNegativeDecimal(amount decimal.Decimal) bool {
	return !amount.IsNegative()
}

// ValidPort returns true if port is a valid network port number (1-65535).
// The port must be a numeric string representing a value in the valid range.
//
// Note: Port 0 is invalid for configuration purposes (it's used for dynamic allocation).
// Empty strings, non-numeric values, and out-of-range values return false.
//
// Example:
//
//	a.That(ctx, assert.ValidPort(cfg.DBPort), "DB_PORT must be valid port", "port", cfg.DBPort)
func ValidPort(port string) bool {
	if port == "" {
		return false
	}

	p, err := strconv.Atoi(port)
	if err != nil {
		return false
	}

	return p > 0 && p <= 65535
}

// validSSLModes contains the valid PostgreSQL SSL modes.
// Package-level for zero-allocation lookups in ValidSSLMode.
var validSSLModes = map[string]bool{
	"":            true, // Empty uses PostgreSQL default
	"disable":     true,
	"allow":       true,
	"prefer":      true,
	"require":     true,
	"verify-ca":   true,
	"verify-full": true,
}

// ValidSSLMode returns true if mode is a valid PostgreSQL SSL mode.
// Valid modes are: disable, allow, prefer, require, verify-ca, verify-full.
// Empty string is also valid (uses PostgreSQL default).
//
// Note: SSL modes are case-sensitive per PostgreSQL documentation.
// Unknown modes will cause connection failures.
//
// Example:
//
//	a.That(ctx, assert.ValidSSLMode(cfg.DBSSLMode), "DB_SSLMODE invalid", "mode", cfg.DBSSLMode)
func ValidSSLMode(mode string) bool {
	return validSSLModes[mode]
}

// PositiveInt returns true if n > 0.
// This is the int variant of Positive (which uses int64).
//
// Example:
//
//	a.That(ctx, assert.PositiveInt(cfg.MaxWorkers), "MAX_WORKERS must be positive", "value", cfg.MaxWorkers)
func PositiveInt(n int) bool {
	return n > 0
}

// InRangeInt returns true if min <= n <= max.
// This is the int variant of InRange (which uses int64).
//
// Note: If min > max (inverted range), always returns false. This is fail-safe
// behavior - callers should ensure min <= max for correct results.
//
// Example:
//
//	a.That(ctx, assert.InRangeInt(cfg.PoolSize, 1, 100), "POOL_SIZE out of range", "value", cfg.PoolSize)
func InRangeInt(n, minVal, maxVal int) bool {
	return n >= minVal && n <= maxVal
}

// DebitsEqualCredits returns true if debits and credits are exactly equal.
// This validates the fundamental double-entry accounting invariant:
// for every transaction, total debits MUST equal total credits.
//
// Note: Uses decimal.Equal() for exact comparison without floating point issues.
// Even a tiny difference indicates a bug in amount calculation.
//
// Example:
//
//	a.That(ctx, assert.DebitsEqualCredits(debitTotal, creditTotal),
//	    "double-entry violation: debits must equal credits",
//	    "debits", debitTotal, "credits", creditTotal)
func DebitsEqualCredits(debits, credits decimal.Decimal) bool {
	return debits.Equal(credits)
}

// NonZeroTotals returns true if both debits and credits are non-zero.
// A transaction with zero totals is meaningless and indicates a bug.
//
// Example:
//
//	a.That(ctx, assert.NonZeroTotals(debitTotal, creditTotal),
//	    "transaction totals must be non-zero",
//	    "debits", debitTotal, "credits", creditTotal)
func NonZeroTotals(debits, credits decimal.Decimal) bool {
	return !debits.IsZero() && !credits.IsZero()
}

// validTransactionStatuses contains valid transaction status values.
// Package-level for zero-allocation lookups.
var validTransactionStatuses = map[string]bool{
	txn.CREATED:  true,
	txn.APPROVED: true,
	txn.PENDING:  true,
	txn.CANCELED: true,
	txn.NOTED:    true,
}

// ValidTransactionStatus returns true if status is a valid transaction status.
// Valid statuses are: CREATED, APPROVED, PENDING, CANCELED, NOTED.
//
// Note: Statuses are case-sensitive and must match exactly.
//
// Example:
//
//	a.That(ctx, assert.ValidTransactionStatus(tran.Status.Code),
//	    "invalid transaction status",
//	    "status", tran.Status.Code)
func ValidTransactionStatus(status string) bool {
	return validTransactionStatuses[status]
}

// validTransitions defines the allowed state machine transitions.
// Key: current state, Value: set of valid target states.
// Only PENDING transactions can be committed (APPROVED) or canceled (CANCELED).
var validTransitions = map[string]map[string]bool{
	txn.PENDING: {
		txn.APPROVED: true,
		txn.CANCELED: true,
	},
	// CREATED, APPROVED, CANCELED, NOTED are terminal states - no forward transitions
}

// TransactionCanTransitionTo returns true if transitioning from current to target is valid.
// The transaction state machine only allows: PENDING -> APPROVED or PENDING -> CANCELED.
//
// Note: This is for forward transitions only. Revert is a separate operation.
//
// Example:
//
//	a.That(ctx, assert.TransactionCanTransitionTo(current, next),
//	    "invalid status transition",
//	    "current", current,
//	    "next", next)
func TransactionCanTransitionTo(current, target string) bool {
	validTargets, exists := validTransitions[current]
	if !exists {
		return false
	}

	return validTargets[target]
}

// TransactionCanBeReverted returns true if a transaction can be reverted.
// The transaction can only be reverted if:
//   - Status is APPROVED
//   - It has no parent transaction (i.e., it is not a reversal of another transaction)
//
// This ensures only original transactions can be reverted, not reversals.
func TransactionCanBeReverted(status string, hasParent bool) bool {
	if status != txn.APPROVED {
		return false
	}

	return !hasParent
}

// BalanceSufficientForRelease returns true if the available on-hold balance
// is sufficient to release the specified amount.
func BalanceSufficientForRelease(onHold, releaseAmount decimal.Decimal) bool {
	if onHold.IsNegative() || releaseAmount.IsNegative() {
		return false
	}

	return onHold.GreaterThanOrEqual(releaseAmount)
}

// DateNotInFuture returns true if the date is not in the future (i.e., <= now).
// Zero time is considered valid (returns true).
func DateNotInFuture(date time.Time) bool {
	if date.IsZero() {
		return true
	}

	return !date.After(time.Now().UTC())
}

// DateAfter returns true if date is strictly after reference time.
func DateAfter(date, reference time.Time) bool {
	return date.After(reference)
}

// BalanceIsZero returns true if both available and onHold balances are exactly zero.
func BalanceIsZero(available, onHold decimal.Decimal) bool {
	return available.IsZero() && onHold.IsZero()
}

// TransactionHasOperations returns true if the transaction has operations.
func TransactionHasOperations(operations []string) bool {
	return len(operations) > 0
}

// TransactionOperationsContain returns true if every element in operations is
// contained in the allowed set (i.e. operations is a subset of allowed).
// Both empty operations and empty allowed return false.
func TransactionOperationsContain(operations, allowed []string) bool {
	if len(operations) == 0 || len(allowed) == 0 {
		return false
	}

	allowedSet := make(map[string]struct{}, len(allowed))
	for _, op := range allowed {
		allowedSet[strings.TrimSpace(op)] = struct{}{}
	}

	for _, op := range operations {
		if _, ok := allowedSet[strings.TrimSpace(op)]; !ok {
			return false
		}
	}

	return true
}

// TransactionOperationsMatch is a deprecated alias for TransactionOperationsContain.
// It checks subset containment: every operation must be in the allowed set.
//
// Deprecated: Use TransactionOperationsContain instead. The name "Match" implied
// full bidirectional equality, but the behavior is subset containment.
func TransactionOperationsMatch(operations, allowed []string) bool {
	return TransactionOperationsContain(operations, allowed)
}
