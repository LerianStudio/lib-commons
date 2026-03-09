//go:build unit

package safe

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestDivide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		numerator   decimal.Decimal
		denominator decimal.Decimal
		want        decimal.Decimal
		wantErr     error
	}{
		{
			name:        "success",
			numerator:   decimal.NewFromInt(100),
			denominator: decimal.NewFromInt(4),
			want:        decimal.NewFromInt(25),
			wantErr:     nil,
		},
		{
			name:        "zero denominator",
			numerator:   decimal.NewFromInt(100),
			denominator: decimal.Zero,
			want:        decimal.Zero,
			wantErr:     ErrDivisionByZero,
		},
		{
			name:        "zero numerator",
			numerator:   decimal.Zero,
			denominator: decimal.NewFromInt(4),
			want:        decimal.Zero,
			wantErr:     nil,
		},
		{
			name:        "negative numbers",
			numerator:   decimal.NewFromInt(-100),
			denominator: decimal.NewFromInt(4),
			want:        decimal.NewFromInt(-25),
			wantErr:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := Divide(tt.numerator, tt.denominator)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}

			assert.True(t, result.Equal(tt.want), "expected %s, got %s", tt.want, result)
		})
	}
}

func TestDivideRound_Success(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.NewFromInt(3)

	result, err := DivideRound(numerator, denominator, 2)

	assert.NoError(t, err)
	expected := decimal.NewFromFloat(33.33)
	assert.True(t, result.Equal(expected), "expected %s, got %s", expected, result)
}

func TestDivideRound_ZeroDenominator(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.Zero

	result, err := DivideRound(numerator, denominator, 2)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrDivisionByZero)
	assert.True(t, result.IsZero())
}

func TestDivideOrZero_Success(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.NewFromInt(4)

	result := DivideOrZero(numerator, denominator)

	assert.True(t, result.Equal(decimal.NewFromInt(25)))
}

func TestDivideOrZero_ZeroDenominator(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.Zero

	result := DivideOrZero(numerator, denominator)

	assert.True(t, result.IsZero())
}

func TestDivideOrDefault_Success(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.NewFromInt(4)
	defaultVal := decimal.NewFromInt(999)

	result := DivideOrDefault(numerator, denominator, defaultVal)

	assert.True(t, result.Equal(decimal.NewFromInt(25)))
}

func TestDivideOrDefault_ZeroDenominator(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.Zero
	defaultVal := decimal.NewFromInt(999)

	result := DivideOrDefault(numerator, denominator, defaultVal)

	assert.True(t, result.Equal(defaultVal))
}

func TestPercentage_Success(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(25)
	denominator := decimal.NewFromInt(100)

	result, err := Percentage(numerator, denominator)

	assert.NoError(t, err)
	assert.True(t, result.Equal(decimal.NewFromInt(25)))
}

func TestPercentage_ZeroDenominator(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(25)
	denominator := decimal.Zero

	result, err := Percentage(numerator, denominator)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrDivisionByZero)
	assert.True(t, result.IsZero())
}

func TestPercentage_FullPercentage(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(100)
	denominator := decimal.NewFromInt(100)

	result, err := Percentage(numerator, denominator)

	assert.NoError(t, err)
	assert.True(t, result.Equal(decimal.NewFromInt(100)))
}

func TestPercentageOrZero_Success(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(50)
	denominator := decimal.NewFromInt(100)

	result := PercentageOrZero(numerator, denominator)

	assert.True(t, result.Equal(decimal.NewFromInt(50)))
}

func TestPercentageOrZero_ZeroDenominator(t *testing.T) {
	t.Parallel()

	numerator := decimal.NewFromInt(50)
	denominator := decimal.Zero

	result := PercentageOrZero(numerator, denominator)

	assert.True(t, result.IsZero())
}

func TestPercentageOrZero_ZeroNumerator(t *testing.T) {
	t.Parallel()

	numerator := decimal.Zero
	denominator := decimal.NewFromInt(100)

	result := PercentageOrZero(numerator, denominator)

	assert.True(t, result.IsZero())
}

func TestDivideFloat64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		numerator   float64
		denominator float64
		want        float64
		wantErr     error
	}{
		{
			name:        "success",
			numerator:   100,
			denominator: 4,
			want:        25,
			wantErr:     nil,
		},
		{
			name:        "zero denominator",
			numerator:   100,
			denominator: 0,
			want:        0,
			wantErr:     ErrDivisionByZero,
		},
		{
			name:        "zero numerator",
			numerator:   0,
			denominator: 4,
			want:        0,
			wantErr:     nil,
		},
		{
			name:        "negative numerator",
			numerator:   -100,
			denominator: 4,
			want:        -25,
			wantErr:     nil,
		},
		{
			name:        "negative denominator",
			numerator:   100,
			denominator: -4,
			want:        -25,
			wantErr:     nil,
		},
		{
			name:        "both negative",
			numerator:   -100,
			denominator: -4,
			want:        25,
			wantErr:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := DivideFloat64(tt.numerator, tt.denominator)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}

			assert.InDelta(t, tt.want, result, 1e-9)
		})
	}
}

func TestDivideFloat64OrZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		numerator   float64
		denominator float64
		want        float64
	}{
		{
			name:        "success",
			numerator:   100,
			denominator: 4,
			want:        25,
		},
		{
			name:        "zero denominator",
			numerator:   100,
			denominator: 0,
			want:        0,
		},
		{
			name:        "zero numerator",
			numerator:   0,
			denominator: 4,
			want:        0,
		},
		{
			name:        "negative numerator",
			numerator:   -100,
			denominator: 4,
			want:        -25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := DivideFloat64OrZero(tt.numerator, tt.denominator)

			assert.InDelta(t, tt.want, result, 1e-9)
		})
	}
}
