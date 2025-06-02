package transaction

import (
	"math"
	"testing"

	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScale(t *testing.T) {
	tests := []struct {
		name string
		v    int64
		s0   int64
		s1   int64
		want int64
	}{
		{
			name: "scale up by 2",
			v:    100,
			s0:   0,
			s1:   2,
			want: 10000,
		},
		{
			name: "scale down by 2",
			v:    10000,
			s0:   2,
			s1:   0,
			want: 100,
		},
		{
			name: "same scale",
			v:    100,
			s0:   2,
			s1:   2,
			want: 100,
		},
		{
			name: "scale from 2 to 4",
			v:    100,
			s0:   2,
			s1:   4,
			want: 10000,
		},
		{
			name: "scale from 4 to 2",
			v:    10000,
			s0:   4,
			s1:   2,
			want: 100,
		},
		{
			name: "zero value",
			v:    0,
			s0:   0,
			s1:   5,
			want: 0,
		},
		{
			name: "negative value scale up",
			v:    -100,
			s0:   0,
			s1:   2,
			want: -10000,
		},
		{
			name: "large scale difference",
			v:    1,
			s0:   0,
			s1:   10,
			want: 10000000000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scale(tt.v, tt.s0, tt.s1)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUndoScale(t *testing.T) {
	tests := []struct {
		name string
		v    float64
		s    int64
		want int64
	}{
		{
			name: "undo scale 2",
			v:    100.50,
			s:    2,
			want: 10050,
		},
		{
			name: "undo scale 0",
			v:    100,
			s:    0,
			want: 100,
		},
		{
			name: "undo scale 4",
			v:    12.3456,
			s:    4,
			want: 123456,
		},
		{
			name: "zero value",
			v:    0,
			s:    3,
			want: 0,
		},
		{
			name: "negative value",
			v:    -25.75,
			s:    2,
			want: -2575,
		},
		{
			name: "large scale",
			v:    1.0,
			s:    10,
			want: 10000000000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UndoScale(tt.v, tt.s)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindScale(t *testing.T) {
	tests := []struct {
		name  string
		asset string
		v     float64
		s     int64
		want  Amount
	}{
		{
			name:  "integer value",
			asset: "USD",
			v:     100,
			s:     2,
			want:  Amount{Asset: "USD", Value: 100, Scale: 2},
		},
		{
			name:  "decimal value",
			asset: "EUR",
			v:     100.50,
			s:     2,
			want:  Amount{Asset: "EUR", Value: 10050, Scale: 4},
		},
		{
			name:  "decimal with trailing zeros",
			asset: "GBP",
			v:     100.00,
			s:     2,
			want:  Amount{Asset: "GBP", Value: 100, Scale: 2},
		},
		{
			name:  "multiple decimal places",
			asset: "BTC",
			v:     0.00012345,
			s:     8,
			want:  Amount{Asset: "BTC", Value: 12345, Scale: 16},
		},
		{
			name:  "zero value",
			asset: "JPY",
			v:     0,
			s:     0,
			want:  Amount{Asset: "JPY", Value: 0, Scale: 0},
		},
		{
			name:  "negative decimal",
			asset: "CHF",
			v:     -50.25,
			s:     2,
			want:  Amount{Asset: "CHF", Value: -5025, Scale: 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindScale(tt.asset, tt.v, tt.s)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name          string
		total         Amount
		amount        Amount
		remaining     Amount
		expectedTotal Amount
		expectedRem   Amount
	}{
		{
			name:          "same scale",
			total:         Amount{Value: 100, Scale: 2},
			amount:        Amount{Value: 50, Scale: 2},
			remaining:     Amount{Value: 200, Scale: 2},
			expectedTotal: Amount{Value: 150, Scale: 2},
			expectedRem:   Amount{Value: 150, Scale: 2},
		},
		{
			name:          "amount has higher scale",
			total:         Amount{Value: 100, Scale: 2},
			amount:        Amount{Value: 5000, Scale: 4},
			remaining:     Amount{Value: 200, Scale: 2},
			expectedTotal: Amount{Value: 15000, Scale: 4},
			expectedRem:   Amount{Value: 15000, Scale: 4},
		},
		{
			name:          "total has higher scale",
			total:         Amount{Value: 10000, Scale: 4},
			amount:        Amount{Value: 50, Scale: 2},
			remaining:     Amount{Value: 20000, Scale: 4},
			expectedTotal: Amount{Value: 15000, Scale: 4},
			expectedRem:   Amount{Value: 15000, Scale: 4},
		},
		{
			name:          "zero total",
			total:         Amount{Value: 0, Scale: 0},
			amount:        Amount{Value: 100, Scale: 2},
			remaining:     Amount{Value: 500, Scale: 2},
			expectedTotal: Amount{Value: 100, Scale: 2},
			expectedRem:   Amount{Value: 400, Scale: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := tt.total
			amount := tt.amount
			remaining := tt.remaining

			Normalize(&total, &amount, &remaining)

			assert.Equal(t, tt.expectedTotal.Value, total.Value)
			assert.Equal(t, tt.expectedTotal.Scale, total.Scale)
			assert.Equal(t, tt.expectedRem.Value, remaining.Value)
			assert.Equal(t, tt.expectedRem.Scale, remaining.Scale)
		})
	}
}

func TestWillOverflow(t *testing.T) {
	tests := []struct {
		name  string
		a     int64
		b     int64
		scale int64
		want  bool
	}{
		{
			name:  "no overflow positive",
			a:     100,
			b:     200,
			scale: 2,
			want:  false,
		},
		{
			name:  "no overflow negative",
			a:     -100,
			b:     -200,
			scale: 2,
			want:  false,
		},
		{
			name:  "positive overflow",
			a:     math.MaxInt64 - 1,
			b:     2,
			scale: 2,
			want:  true,
		},
		{
			name:  "negative overflow",
			a:     math.MinInt64 + 1,
			b:     -2,
			scale: 2,
			want:  true,
		},
		{
			name:  "scale overflow",
			a:     100,
			b:     200,
			scale: 19,
			want:  true,
		},
		{
			name:  "edge case - MaxInt64",
			a:     math.MaxInt64,
			b:     0,
			scale: 2,
			want:  false,
		},
		{
			name:  "edge case - MinInt64",
			a:     math.MinInt64,
			b:     0,
			scale: 2,
			want:  false,
		},
		{
			name:  "large positive numbers no overflow",
			a:     1000000000000,
			b:     2000000000000,
			scale: 2,
			want:  false,
		},
		{
			name:  "scale exactly 18",
			a:     100,
			b:     200,
			scale: 18,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WillOverflow(tt.a, tt.b, tt.scale)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOperateBalances(t *testing.T) {
	tests := []struct {
		name      string
		amount    Amount
		balance   Balance
		operation string
		want      Balance
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "debit with same scale",
			amount:    Amount{Value: 50, Scale: 2},
			balance:   Balance{Available: 100, Scale: 2, OnHold: 10},
			operation: constant.DEBIT,
			want:      Balance{Available: 50, Scale: 2, OnHold: 10},
			wantErr:   false,
		},
		{
			name:      "credit with same scale",
			amount:    Amount{Value: 50, Scale: 2},
			balance:   Balance{Available: 100, Scale: 2, OnHold: 10},
			operation: constant.CREDIT,
			want:      Balance{Available: 150, Scale: 2, OnHold: 10},
			wantErr:   false,
		},
		{
			name:      "debit with amount higher scale",
			amount:    Amount{Value: 5000, Scale: 4},
			balance:   Balance{Available: 100, Scale: 2, OnHold: 0},
			operation: constant.DEBIT,
			want:      Balance{Available: 5000, Scale: 4, OnHold: 0},
			wantErr:   false,
		},
		{
			name:      "credit with balance higher scale",
			amount:    Amount{Value: 50, Scale: 2},
			balance:   Balance{Available: 10000, Scale: 4, OnHold: 0},
			operation: constant.CREDIT,
			want:      Balance{Available: 15000, Scale: 4, OnHold: 0},
			wantErr:   false,
		},
		{
			name:      "debit resulting in negative",
			amount:    Amount{Value: 150, Scale: 2},
			balance:   Balance{Available: 100, Scale: 2, OnHold: 0},
			operation: constant.DEBIT,
			want:      Balance{Available: -50, Scale: 2, OnHold: 0},
			wantErr:   false,
		},
		{
			name:      "debit overflow",
			amount:    Amount{Value: 2, Scale: 2},
			balance:   Balance{Available: math.MinInt64 + 1, Scale: 2, OnHold: 0},
			operation: constant.DEBIT,
			want:      Balance{},
			wantErr:   true,
			errMsg:    constant.ErrOverFlowInt64.Error(),
		},
		{
			name:      "credit overflow",
			amount:    Amount{Value: 2, Scale: 2},
			balance:   Balance{Available: math.MaxInt64 - 1, Scale: 2, OnHold: 0},
			operation: constant.CREDIT,
			want:      Balance{},
			wantErr:   true,
			errMsg:    constant.ErrOverFlowInt64.Error(),
		},
		{
			name:      "zero amount debit",
			amount:    Amount{Value: 0, Scale: 2},
			balance:   Balance{Available: 100, Scale: 2, OnHold: 5},
			operation: constant.DEBIT,
			want:      Balance{Available: 100, Scale: 2, OnHold: 5},
			wantErr:   false,
		},
		{
			name:      "zero amount credit",
			amount:    Amount{Value: 0, Scale: 2},
			balance:   Balance{Available: 100, Scale: 2, OnHold: 5},
			operation: constant.CREDIT,
			want:      Balance{Available: 100, Scale: 2, OnHold: 5},
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OperateBalances(tt.amount, tt.balance, tt.operation)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want.Available, got.Available)
				assert.Equal(t, tt.want.Scale, got.Scale)
				assert.Equal(t, tt.want.OnHold, got.OnHold)
			}
		})
	}
}

func TestCalculateTotal(t *testing.T) {
	tests := []struct {
		name       string
		fromTos    []FromTo
		send       Send
		wantTotal  int64
		wantFromTo map[string]Amount
		wantScdt   []string
	}{
		{
			name: "multiple amounts",
			fromTos: []FromTo{
				{Account: "0#acc1", Amount: &Amount{Asset: "USD", Value: 3000, Scale: 2}},
				{Account: "1#acc2", Amount: &Amount{Asset: "USD", Value: 7000, Scale: 2}},
			},
			send:      Send{Asset: "USD", Value: 10000, Scale: 2},
			wantTotal: 10000,
			wantFromTo: map[string]Amount{
				"0#acc1": {Asset: "USD", Value: 3000, Scale: 2},
				"1#acc2": {Asset: "USD", Value: 7000, Scale: 2},
			},
			wantScdt: []string{"acc1", "acc2"},
		},
		{
			name: "shares calculation",
			fromTos: []FromTo{
				{Account: "0#acc1", Share: &Share{Percentage: 60, PercentageOfPercentage: 100}},
				{Account: "1#acc2", Share: &Share{Percentage: 40, PercentageOfPercentage: 100}},
			},
			send:      Send{Asset: "USD", Value: 10000, Scale: 2},
			wantTotal: 10000,
			wantFromTo: map[string]Amount{
				"0#acc1": {Asset: "USD", Value: 6000, Scale: 2},
				"1#acc2": {Asset: "USD", Value: 4000, Scale: 2},
			},
			wantScdt: []string{"acc1", "acc2"},
		},
		{
			name: "shares with percentage of percentage",
			fromTos: []FromTo{
				{Account: "0#acc1", Share: &Share{Percentage: 50, PercentageOfPercentage: 50}},
				{Account: "1#acc2", Share: &Share{Percentage: 50, PercentageOfPercentage: 50}},
			},
			send:      Send{Asset: "USD", Value: 10000, Scale: 2},
			wantTotal: 5000,
			wantFromTo: map[string]Amount{
				"0#acc1": {Asset: "USD", Value: 2500, Scale: 2},
				"1#acc2": {Asset: "USD", Value: 2500, Scale: 2},
			},
			wantScdt: []string{"acc1", "acc2"},
		},
		{
			name: "remaining amount",
			fromTos: []FromTo{
				{Account: "0#acc1", Amount: &Amount{Asset: "USD", Value: 3000, Scale: 2}},
				{Account: "1#acc2", Remaining: "remaining"},
			},
			send:      Send{Asset: "USD", Value: 10000, Scale: 2},
			wantTotal: 10000,
			wantFromTo: map[string]Amount{
				"0#acc1": {Asset: "USD", Value: 3000, Scale: 2},
				"1#acc2": {Asset: "USD", Value: 7000, Scale: 2},
			},
			wantScdt: []string{"acc1", "acc2"},
		},
		{
			name: "mixed amounts and shares",
			fromTos: []FromTo{
				{Account: "0#acc1", Amount: &Amount{Asset: "USD", Value: 2000, Scale: 2}},
				{Account: "1#acc2", Share: &Share{Percentage: 50, PercentageOfPercentage: 100}},
				{Account: "2#acc3", Remaining: "remaining"},
			},
			send:      Send{Asset: "USD", Value: 10000, Scale: 2},
			wantTotal: 10000,
			wantFromTo: map[string]Amount{
				"0#acc1": {Asset: "USD", Value: 2000, Scale: 2},
				"1#acc2": {Asset: "USD", Value: 5000, Scale: 2},
				"2#acc3": {Asset: "USD", Value: 3000, Scale: 2},
			},
			wantScdt: []string{"acc1", "acc2", "acc3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			totalChan := make(chan int64, 1)
			fromToChan := make(chan map[string]Amount, 1)
			scdtChan := make(chan []string, 1)

			go CalculateTotal(tt.fromTos, tt.send, totalChan, fromToChan, scdtChan)

			gotTotal := <-totalChan
			gotFromTo := <-fromToChan
			gotScdt := <-scdtChan

			assert.Equal(t, tt.wantTotal, gotTotal)
			assert.Equal(t, tt.wantFromTo, gotFromTo)
			assert.Equal(t, tt.wantScdt, gotScdt)
		})
	}
}
