package assert

import (
	"time"

	libobsassert "github.com/LerianStudio/lib-observability/assert"
	"github.com/shopspring/decimal"
)

// Positive returns true if n > 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.Positive instead.
var Positive = libobsassert.Positive

// NonNegative returns true if n >= 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NonNegative instead.
var NonNegative = libobsassert.NonNegative

// NotZero returns true if n != 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NotZero instead.
var NotZero = libobsassert.NotZero

// InRange returns true if min <= n <= max.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.InRange instead.
var InRange = libobsassert.InRange

// ValidUUID returns true if s is a valid UUID string.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidUUID instead.
var ValidUUID = libobsassert.ValidUUID

// ValidAmount returns true if the decimal's exponent is within reasonable bounds.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidAmount instead.
var ValidAmount = libobsassert.ValidAmount

// ValidScale returns true if scale is in the range [0, 18].
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidScale instead.
var ValidScale = libobsassert.ValidScale

// PositiveDecimal returns true if amount > 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.PositiveDecimal instead.
var PositiveDecimal = libobsassert.PositiveDecimal

// NonNegativeDecimal returns true if amount >= 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NonNegativeDecimal instead.
var NonNegativeDecimal = libobsassert.NonNegativeDecimal

// ValidPort returns true if port is a valid network port number (1-65535).
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidPort instead.
var ValidPort = libobsassert.ValidPort

// ValidSSLMode returns true if mode is a valid PostgreSQL SSL mode.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidSSLMode instead.
var ValidSSLMode = libobsassert.ValidSSLMode

// PositiveInt returns true if n > 0.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.PositiveInt instead.
var PositiveInt = libobsassert.PositiveInt

// InRangeInt returns true if min <= n <= max.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.InRangeInt instead.
var InRangeInt = libobsassert.InRangeInt

// DebitsEqualCredits returns true if debits and credits are exactly equal.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.DebitsEqualCredits instead.
var DebitsEqualCredits = libobsassert.DebitsEqualCredits

// NonZeroTotals returns true if both debits and credits are non-zero.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.NonZeroTotals instead.
var NonZeroTotals = libobsassert.NonZeroTotals

// ValidTransactionStatus returns true if status is a valid transaction status.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ValidTransactionStatus instead.
var ValidTransactionStatus = libobsassert.ValidTransactionStatus

// TransactionCanTransitionTo returns true if transitioning from current to target is valid.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionCanTransitionTo instead.
var TransactionCanTransitionTo = libobsassert.TransactionCanTransitionTo

// TransactionCanBeReverted returns true if a transaction can be reverted.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionCanBeReverted instead.
var TransactionCanBeReverted = libobsassert.TransactionCanBeReverted

// BalanceSufficientForRelease returns true if the available on-hold balance is sufficient.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.BalanceSufficientForRelease instead.
var BalanceSufficientForRelease = libobsassert.BalanceSufficientForRelease

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
var TransactionHasOperations = libobsassert.TransactionHasOperations

// TransactionOperationsContain returns true if every element in operations is contained in the allowed set.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionOperationsContain instead.
var TransactionOperationsContain = libobsassert.TransactionOperationsContain

// TransactionOperationsMatch is a deprecated alias for TransactionOperationsContain.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.TransactionOperationsContain instead.
var TransactionOperationsMatch = libobsassert.TransactionOperationsContain
