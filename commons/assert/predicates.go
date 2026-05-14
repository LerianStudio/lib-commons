package assert

import (
	"time"

	libobsassert "github.com/LerianStudio/lib-observability/assert"
	"github.com/shopspring/decimal"
)

// Positive returns true if n > 0.
var Positive = libobsassert.Positive

// NonNegative returns true if n >= 0.
var NonNegative = libobsassert.NonNegative

// NotZero returns true if n != 0.
var NotZero = libobsassert.NotZero

// InRange returns true if min <= n <= max.
var InRange = libobsassert.InRange

// ValidUUID returns true if s is a valid UUID string.
var ValidUUID = libobsassert.ValidUUID

// ValidAmount returns true if the decimal's exponent is within reasonable bounds.
var ValidAmount = libobsassert.ValidAmount

// ValidScale returns true if scale is in the range [0, 18].
var ValidScale = libobsassert.ValidScale

// PositiveDecimal returns true if amount > 0.
var PositiveDecimal = libobsassert.PositiveDecimal

// NonNegativeDecimal returns true if amount >= 0.
var NonNegativeDecimal = libobsassert.NonNegativeDecimal

// ValidPort returns true if port is a valid network port number (1-65535).
var ValidPort = libobsassert.ValidPort

// ValidSSLMode returns true if mode is a valid PostgreSQL SSL mode.
var ValidSSLMode = libobsassert.ValidSSLMode

// PositiveInt returns true if n > 0.
var PositiveInt = libobsassert.PositiveInt

// InRangeInt returns true if min <= n <= max.
var InRangeInt = libobsassert.InRangeInt

// DebitsEqualCredits returns true if debits and credits are exactly equal.
var DebitsEqualCredits = libobsassert.DebitsEqualCredits

// NonZeroTotals returns true if both debits and credits are non-zero.
var NonZeroTotals = libobsassert.NonZeroTotals

// ValidTransactionStatus returns true if status is a valid transaction status.
var ValidTransactionStatus = libobsassert.ValidTransactionStatus

// TransactionCanTransitionTo returns true if transitioning from current to target is valid.
var TransactionCanTransitionTo = libobsassert.TransactionCanTransitionTo

// TransactionCanBeReverted returns true if a transaction can be reverted.
var TransactionCanBeReverted = libobsassert.TransactionCanBeReverted

// BalanceSufficientForRelease returns true if the available on-hold balance is sufficient.
var BalanceSufficientForRelease = libobsassert.BalanceSufficientForRelease

// DateNotInFuture returns true if the date is not in the future.
func DateNotInFuture(date time.Time) bool {
	return libobsassert.DateNotInFuture(date)
}

// DateAfter returns true if date is strictly after reference time.
func DateAfter(date, reference time.Time) bool {
	return libobsassert.DateAfter(date, reference)
}

// BalanceIsZero returns true if both available and onHold balances are exactly zero.
func BalanceIsZero(available, onHold decimal.Decimal) bool {
	return libobsassert.BalanceIsZero(available, onHold)
}

// TransactionHasOperations returns true if the transaction has operations.
var TransactionHasOperations = libobsassert.TransactionHasOperations

// TransactionOperationsContain returns true if every element in operations is contained in the allowed set.
var TransactionOperationsContain = libobsassert.TransactionOperationsContain

// TransactionOperationsMatch is a deprecated alias for TransactionOperationsContain.
var TransactionOperationsMatch = libobsassert.TransactionOperationsMatch
