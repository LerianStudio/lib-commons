//go:build unit

package safe

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errSink error

func TestParseDecimal_AcceptsValuesWithinBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "zero", input: "0"},
		{name: "cents", input: "0.01"},
		{name: "amount with scale 2", input: "12500.50"},
		{name: "negative amount", input: "-12500.50"},
		{name: "17 integer digits at scale 8", input: "12345678901234567.12345678"},
		{name: "18 decimal places", input: "0.000000000000000001"},
		{name: "small positive exponent", input: "1e3"},
		{name: "largest exponent", input: "1e" + strconv.Itoa(MaxDecimalExponent)},
		{name: "smallest exponent", input: "1e-" + strconv.Itoa(MaxDecimalExponent)},
		{name: "smallest exponent in plain notation", input: "0." + strings.Repeat("0", MaxDecimalExponent-1) + "1"},
		{name: "longest coefficient", input: strings.Repeat("9", MaxDecimalDigits)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want, err := decimal.NewFromString(tc.input)
			require.NoError(t, err)

			got, err := ParseDecimal(tc.input)
			require.NoError(t, err)
			assert.True(t, want.Equal(got))
			assert.Equal(t, want.Exponent(), got.Exponent())
		})
	}
}

func TestParseDecimal_RefusesValuesOutsideBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "exponent far below bound", input: "1e-2000000000"},
		{name: "exponent far above bound", input: "1e2000000000"},
		{name: "zero with exponent far below bound", input: "0e-2000000000"},
		{name: "exponent just below bound", input: "1e-" + strconv.Itoa(MaxDecimalExponent+1)},
		{name: "exponent just above bound", input: "1e" + strconv.Itoa(MaxDecimalExponent+1)},
		{name: "plain notation just below bound", input: "0." + strings.Repeat("0", MaxDecimalExponent) + "1"},
		{name: "coefficient over digit bound", input: strings.Repeat("9", MaxDecimalDigits+1)},
		{name: "text over length bound", input: strings.Repeat("0", MaxDecimalTextLength) + "1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseDecimal(tc.input)
			require.ErrorIs(t, err, ErrDecimalOutOfBounds)
			assert.True(t, got.IsZero())
		})
	}
}

func TestParseDecimal_MalformedTextIsNotABoundsError(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "abc", "1.2.3", " 1"} {
		_, err := ParseDecimal(input)
		require.Error(t, err, input)
		assert.NotErrorIs(t, err, ErrDecimalOutOfBounds, input)
	}
}

func TestCheckDecimal(t *testing.T) {
	t.Parallel()

	assert.NoError(t, CheckDecimal(decimal.Decimal{}))
	assert.NoError(t, CheckDecimal(decimal.New(1, -MaxDecimalExponent)))
	assert.NoError(t, CheckDecimal(decimal.New(1, MaxDecimalExponent)))
	assert.ErrorIs(t, CheckDecimal(decimal.New(1, -MaxDecimalExponent-1)), ErrDecimalOutOfBounds)
	assert.ErrorIs(t, CheckDecimal(decimal.New(1, MaxDecimalExponent+1)), ErrDecimalOutOfBounds)
	assert.ErrorIs(t, CheckDecimal(decimal.New(0, -2000000000)), ErrDecimalOutOfBounds)
}

// Not parallel: runtime.ReadMemStats reads process-wide allocation counters.
func TestParseDecimal_RefusalIsAllocationBounded(t *testing.T) {
	var before, after runtime.MemStats

	for _, input := range []string{"1e-2000000000", "1e2000000000", strings.Repeat("9", 1<<20)} {
		_, err := ParseDecimal(input) // also warms fmt's printer pool before measuring
		require.ErrorIs(t, err, ErrDecimalOutOfBounds)

		runtime.ReadMemStats(&before)
		_, errSink = ParseDecimal(input)
		runtime.ReadMemStats(&after)

		assert.LessOrEqual(t, after.TotalAlloc-before.TotalAlloc, uint64(1024))
	}
}
