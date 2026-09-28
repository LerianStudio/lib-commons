package safe

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Decimal bounds enforced by CheckDecimal and ParseDecimal.
const (
	// MaxDecimalExponent is the largest accepted exponent magnitude, positive
	// or negative: 1e1000 and 1e-1000 are accepted, 1e1001 and 1e-1001 are not.
	MaxDecimalExponent = 1000
	// MaxDecimalDigits is the largest accepted coefficient length, in digits.
	MaxDecimalDigits = 1000
	// MaxDecimalTextLength is the longest text ParseDecimal parses, in bytes.
	MaxDecimalTextLength = 1024
)

// ErrDecimalOutOfBounds is returned when a decimal exceeds MaxDecimalExponent
// or MaxDecimalDigits, or its text exceeds MaxDecimalTextLength.
var ErrDecimalOutOfBounds = errors.New("decimal out of bounds")

// CheckDecimal wraps ErrDecimalOutOfBounds when d exceeds MaxDecimalExponent
// (checked first) or MaxDecimalDigits (NumDigits, cost proportional to the
// coefficient in memory). Take JSON decimals as strings through ParseDecimal.
func CheckDecimal(d decimal.Decimal) error {
	if exp := d.Exponent(); exp > MaxDecimalExponent || exp < -MaxDecimalExponent {
		return fmt.Errorf("%w: exponent %d exceeds magnitude %d", ErrDecimalOutOfBounds, exp, MaxDecimalExponent)
	}

	if digits := d.NumDigits(); digits > MaxDecimalDigits {
		return fmt.Errorf("%w: %d digits exceed %d", ErrDecimalOutOfBounds, digits, MaxDecimalDigits)
	}

	return nil
}

// ParseDecimal parses s like decimal.NewFromString, refusing text longer than
// MaxDecimalTextLength and values CheckDecimal refuses (ErrDecimalOutOfBounds).
//
// Example:
//
//	amount, err := safe.ParseDecimal(req.Amount)
//	if err != nil {
//	    return fmt.Errorf("parse amount: %w", err)
//	}
func ParseDecimal(s string) (decimal.Decimal, error) {
	if len(s) > MaxDecimalTextLength {
		return decimal.Zero, fmt.Errorf("%w: text length %d exceeds %d", ErrDecimalOutOfBounds, len(s), MaxDecimalTextLength)
	}

	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, err
	}

	if err := CheckDecimal(d); err != nil {
		return decimal.Zero, err
	}

	return d, nil
}
