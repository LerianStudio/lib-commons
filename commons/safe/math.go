package safe

import (
	"errors"

	"github.com/shopspring/decimal"
)

// ErrDivisionByZero is returned when attempting to divide by zero.
var ErrDivisionByZero = errors.New("division by zero")

// percentageMultiplier is the multiplier for percentage calculations.
const percentageMultiplier = 100

// hundredDecimal is the pre-allocated decimal multiplier for percentage calculations.
var hundredDecimal = decimal.NewFromInt(percentageMultiplier)

// Divide performs decimal division with zero check.
// Returns ErrDivisionByZero if denominator is zero.
//
// Example:
//
//	result, err := safe.Divide(numerator, denominator)
//	if err != nil {
//	    return fmt.Errorf("calculate ratio: %w", err)
//	}
func Divide(numerator, denominator decimal.Decimal) (decimal.Decimal, error) {
	if denominator.IsZero() {
		return decimal.Zero, ErrDivisionByZero
	}

	return numerator.Div(denominator), nil
}

// DivideRound performs decimal division with rounding and zero check.
// Returns ErrDivisionByZero if denominator is zero.
//
// Example:
//
//	result, err := safe.DivideRound(numerator, denominator, 2)
//	if err != nil {
//	    return fmt.Errorf("calculate percentage: %w", err)
//	}
func DivideRound(numerator, denominator decimal.Decimal, places int32) (decimal.Decimal, error) {
	if denominator.IsZero() {
		return decimal.Zero, ErrDivisionByZero
	}

	return numerator.DivRound(denominator, places), nil
}

// DivideOrZero performs decimal division, returning zero if denominator is zero.
// Use when zero is an acceptable fallback (e.g., percentage calculations where
// zero total means zero percentage).
//
// Example:
//
//	percentage := safe.DivideOrZero(matched, total).Mul(hundred)
func DivideOrZero(numerator, denominator decimal.Decimal) decimal.Decimal {
	if denominator.IsZero() {
		return decimal.Zero
	}

	return numerator.Div(denominator)
}

// DivideOrDefault performs decimal division, returning defaultValue if denominator is zero.
// Use when a specific fallback value is needed.
//
// Example:
//
//	rate := safe.DivideOrDefault(resolved, total, decimal.NewFromInt(100))
func DivideOrDefault(numerator, denominator, defaultValue decimal.Decimal) decimal.Decimal {
	if denominator.IsZero() {
		return defaultValue
	}

	return numerator.Div(denominator)
}

// Percentage calculates (numerator / denominator) * 100 with zero check.
// Returns ErrDivisionByZero if denominator is zero.
//
// Example:
//
//	pct, err := safe.Percentage(matched, total)
//	if err != nil {
//	    return fmt.Errorf("calculate match rate: %w", err)
//	}
func Percentage(numerator, denominator decimal.Decimal) (decimal.Decimal, error) {
	if denominator.IsZero() {
		return decimal.Zero, ErrDivisionByZero
	}

	return numerator.Div(denominator).Mul(hundredDecimal), nil
}

// PercentageOrZero calculates (numerator / denominator) * 100, returning zero if
// denominator is zero. This is the common pattern for rate calculations.
//
// Example:
//
//	matchRate := safe.PercentageOrZero(matched, total)
func PercentageOrZero(numerator, denominator decimal.Decimal) decimal.Decimal {
	if denominator.IsZero() {
		return decimal.Zero
	}

	return numerator.Div(denominator).Mul(hundredDecimal)
}

// DivideFloat64 performs float64 division with zero check.
// Returns ErrDivisionByZero if denominator is zero.
//
// Example:
//
//	ratio, err := safe.DivideFloat64(failures, total)
//	if err != nil {
//	    return fmt.Errorf("calculate failure ratio: %w", err)
//	}
func DivideFloat64(numerator, denominator float64) (float64, error) {
	if denominator == 0 {
		return 0, ErrDivisionByZero
	}

	return numerator / denominator, nil
}

// DivideFloat64OrZero performs float64 division, returning zero if denominator is zero.
//
// Example:
//
//	ratio := safe.DivideFloat64OrZero(failures, total)
func DivideFloat64OrZero(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}

	return numerator / denominator
}
