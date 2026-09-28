//go:build unit

package http

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPositiveDecimalValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  decimal.Decimal
		wantErr bool
	}{
		{name: "positive amount is valid", amount: decimal.NewFromFloat(100.50), wantErr: false},
		{name: "zero is invalid", amount: decimal.Zero, wantErr: true},
		{name: "negative is invalid", amount: decimal.NewFromFloat(-50.00), wantErr: true},
		{name: "small positive is valid", amount: decimal.NewFromFloat(0.01), wantErr: false},
		{name: "17 integer digits at scale 8 is valid", amount: decimal.RequireFromString("12345678901234567.12345678"), wantErr: false},
		{name: "positive with exponent far below bound is invalid", amount: decimal.New(1, -2000000000), wantErr: true},
		{name: "positive with exponent far above bound is invalid", amount: decimal.New(1, 2000000000), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testPositiveDecimalPayload{Amount: tc.amount}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "amount")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPositiveAmountValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{name: "positive amount is valid", amount: "100.50", wantErr: false},
		{name: "zero is invalid", amount: "0", wantErr: true},
		{name: "negative is invalid", amount: "-50.00", wantErr: true},
		{name: "empty string is valid (let required handle it)", amount: "", wantErr: false},
		{name: "invalid decimal string", amount: "not-a-number", wantErr: true},
		{name: "small positive is valid", amount: "0.01", wantErr: false},
		{name: "17 integer digits at scale 8 is valid", amount: "12345678901234567.12345678", wantErr: false},
		{name: "small positive exponent is valid", amount: "1e3", wantErr: false},
		{name: "exponent far below bound is invalid", amount: "1e-2000000000", wantErr: true},
		{name: "exponent far above bound is invalid", amount: "1e2000000000", wantErr: true},
		{name: "text over length bound is invalid", amount: strings.Repeat("1", 2048), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testPositiveAmountPayload{Amount: tc.amount}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNonNegativeAmountValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{name: "positive amount is valid", amount: "100.50", wantErr: false},
		{name: "zero is valid", amount: "0", wantErr: false},
		{name: "negative is invalid", amount: "-50.00", wantErr: true},
		{name: "empty string is valid (let required handle it)", amount: "", wantErr: false},
		{name: "invalid decimal string", amount: "not-a-number", wantErr: true},
		{name: "zero with exponent far below bound is invalid", amount: "0e-2000000000", wantErr: true},
		{name: "exponent far above bound is invalid", amount: "1e2000000000", wantErr: true},
		{name: "text over length bound is invalid", amount: strings.Repeat("1", 2048), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testNonNegativeAmountPayload{Amount: tc.amount}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "amount")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
