package assert

import (
	"time"

	libobsassert "github.com/LerianStudio/lib-observability/assert"
	"github.com/shopspring/decimal"
)

// Positive returns true if n > 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.Positive instead.
func Positive(n int64) bool {
	return libobsassert.Positive(n)
}

// NonNegative returns true if n >= 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NonNegative instead.
func NonNegative(n int64) bool {
	return libobsassert.NonNegative(n)
}

// NotZero returns true if n != 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NotZero instead.
func NotZero(n int64) bool {
	return libobsassert.NotZero(n)
}

// InRange returns true if min <= n <= max.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.InRange instead.
func InRange(n, minVal, maxVal int64) bool {
	return libobsassert.InRange(n, minVal, maxVal)
}

// ValidUUID returns true if s is a valid UUID string.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidUUID instead.
func ValidUUID(s string) bool {
	return libobsassert.ValidUUID(s)
}

// ValidAmount returns true if the decimal's exponent is within reasonable bounds.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidAmount instead.
func ValidAmount(amount decimal.Decimal) bool {
	return libobsassert.ValidAmount(amount)
}

// ValidScale returns true if scale is in the range [0, 18].
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidScale instead.
func ValidScale(scale int) bool {
	return libobsassert.ValidScale(scale)
}

// PositiveDecimal returns true if amount > 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.PositiveDecimal instead.
func PositiveDecimal(amount decimal.Decimal) bool {
	return libobsassert.PositiveDecimal(amount)
}

// NonNegativeDecimal returns true if amount >= 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NonNegativeDecimal instead.
func NonNegativeDecimal(amount decimal.Decimal) bool {
	return libobsassert.NonNegativeDecimal(amount)
}

// ValidPort returns true if port is a valid network port number (1-65535).
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidPort instead.
func ValidPort(port string) bool {
	return libobsassert.ValidPort(port)
}

// ValidSSLMode returns true if mode is a valid PostgreSQL SSL mode.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidSSLMode instead.
func ValidSSLMode(mode string) bool {
	return libobsassert.ValidSSLMode(mode)
}

// PositiveInt returns true if n > 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.PositiveInt instead.
func PositiveInt(n int) bool {
	return libobsassert.PositiveInt(n)
}

// InRangeInt returns true if min <= n <= max.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.InRangeInt instead.
func InRangeInt(n, minVal, maxVal int) bool {
	return libobsassert.InRangeInt(n, minVal, maxVal)
}

// DebitsEqualCredits returns true if debits and credits are exactly equal.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.DebitsEqualCredits instead.
func DebitsEqualCredits(debits, credits decimal.Decimal) bool {
	return libobsassert.DebitsEqualCredits(debits, credits)
}

// NonZeroTotals returns true if both debits and credits are non-zero.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NonZeroTotals instead.
func NonZeroTotals(debits, credits decimal.Decimal) bool {
	return libobsassert.NonZeroTotals(debits, credits)
}

// ValidTransactionStatus returns true if status is a valid transaction status.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidTransactionStatus instead.
func ValidTransactionStatus(status string) bool {
	return libobsassert.ValidTransactionStatus(status)
}

// TransactionCanTransitionTo returns true if transitioning from current to target is valid.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionCanTransitionTo instead.
func TransactionCanTransitionTo(current, target string) bool {
	return libobsassert.TransactionCanTransitionTo(current, target)
}

// TransactionCanBeReverted returns true if a transaction can be reverted.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionCanBeReverted instead.
func TransactionCanBeReverted(status string, hasParent bool) bool {
	return libobsassert.TransactionCanBeReverted(status, hasParent)
}

// BalanceSufficientForRelease returns true if the available on-hold balance is sufficient.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.BalanceSufficientForRelease instead.
func BalanceSufficientForRelease(onHold, releaseAmount decimal.Decimal) bool {
	return libobsassert.BalanceSufficientForRelease(onHold, releaseAmount)
}

// DateNotInFuture returns true if the date is not in the future.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.DateNotInFuture instead.
func DateNotInFuture(date time.Time) bool {
	return libobsassert.DateNotInFuture(date)
}

// DateAfter returns true if date is strictly after reference time.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.DateAfter instead.
func DateAfter(date, reference time.Time) bool {
	return libobsassert.DateAfter(date, reference)
}

// BalanceIsZero returns true if both available and onHold balances are exactly zero.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.BalanceIsZero instead.
func BalanceIsZero(available, onHold decimal.Decimal) bool {
	return libobsassert.BalanceIsZero(available, onHold)
}

// TransactionHasOperations returns true if the transaction has operations.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionHasOperations instead.
func TransactionHasOperations(operations []string) bool {
	return libobsassert.TransactionHasOperations(operations)
}

// TransactionOperationsContain returns true if every element in operations is contained in the allowed set.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionOperationsContain instead.
func TransactionOperationsContain(operations, allowed []string) bool {
	return libobsassert.TransactionOperationsContain(operations, allowed)
}

// TransactionOperationsMatch is a deprecated alias for TransactionOperationsContain.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionOperationsContain instead.
func TransactionOperationsMatch(operations, allowed []string) bool {
	return libobsassert.TransactionOperationsContain(operations, allowed)
}
