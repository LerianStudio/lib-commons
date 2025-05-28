package transaction

import (
	"context"
	"math"
	"testing"
	"time"

	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitAlias(t *testing.T) {
	tests := []struct {
		name  string
		alias string
		want  string
	}{
		{
			name:  "alias with hash",
			alias: "0#account1",
			want:  "account1",
		},
		{
			name:  "alias without hash",
			alias: "account1",
			want:  "account1",
		},
		{
			name:  "alias with multiple hashes",
			alias: "0#account#with#hash",
			want:  "account#with#hash",
		},
		{
			name:  "empty alias",
			alias: "",
			want:  "",
		},
		{
			name:  "only hash",
			alias: "#",
			want:  "",
		},
		{
			name:  "hash at the end",
			alias: "account#",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitAlias(tt.alias)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConcatAlias(t *testing.T) {
	tests := []struct {
		name  string
		index int
		alias string
		want  string
	}{
		{
			name:  "basic concatenation",
			index: 0,
			alias: "account1",
			want:  "0#account1",
		},
		{
			name:  "negative index",
			index: -1,
			alias: "account1",
			want:  "-1#account1",
		},
		{
			name:  "large index",
			index: 999999,
			alias: "account1",
			want:  "999999#account1",
		},
		{
			name:  "empty alias",
			index: 5,
			alias: "",
			want:  "5#",
		},
		{
			name:  "alias with special characters",
			index: 10,
			alias: "@user:domain.com",
			want:  "10#@user:domain.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConcatAlias(tt.index, tt.alias)
			assert.Equal(t, tt.want, got)
		})
	}
}

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

func TestAppendIfNotExist(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		toAppend []string
		want     []string
	}{
		{
			name:     "append new elements",
			slice:    []string{"a", "b"},
			toAppend: []string{"c", "d"},
			want:     []string{"a", "b", "c", "d"},
		},
		{
			name:     "append with duplicates",
			slice:    []string{"a", "b", "c"},
			toAppend: []string{"b", "d", "c", "e"},
			want:     []string{"a", "b", "c", "d", "e"},
		},
		{
			name:     "append to empty slice",
			slice:    []string{},
			toAppend: []string{"a", "b"},
			want:     []string{"a", "b"},
		},
		{
			name:     "append empty slice",
			slice:    []string{"a", "b"},
			toAppend: []string{},
			want:     []string{"a", "b"},
		},
		{
			name:     "all duplicates",
			slice:    []string{"a", "b", "c"},
			toAppend: []string{"a", "b", "c"},
			want:     []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppendIfNotExist(tt.slice, tt.toAppend)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateSendSourceAndDistribute(t *testing.T) {
	tests := []struct {
		name        string
		transaction Transaction
		want        *Responses
		wantErr     bool
		errMsg      string
	}{
		{
			name: "valid simple transaction",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 10000,
					Scale: 2,
					Source: Source{
						From: []FromTo{
							{Account: "0#source1", Amount: &Amount{Asset: "USD", Value: 10000, Scale: 2}},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{Account: "0#dest1", Amount: &Amount{Asset: "USD", Value: 10000, Scale: 2}},
						},
					},
				},
			},
			want: &Responses{
				Total: 10000,
				Asset: "USD",
				From: map[string]Amount{
					"0#source1": {Asset: "USD", Value: 10000, Scale: 2},
				},
				To: map[string]Amount{
					"0#dest1": {Asset: "USD", Value: 10000, Scale: 2},
				},
				Sources:      []string{"source1"},
				Destinations: []string{"dest1"},
				Aliases:      []string{"source1", "dest1"},
			},
			wantErr: false,
		},
		{
			name: "value mismatch - sources don't match send value",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 10000,
					Scale: 2,
					Source: Source{
						From: []FromTo{
							{Account: "0#source1", Amount: &Amount{Asset: "USD", Value: 5000, Scale: 2}},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{Account: "0#dest1", Amount: &Amount{Asset: "USD", Value: 10000, Scale: 2}},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  constant.ErrTransactionValueMismatch.Error(),
		},
		{
			name: "value mismatch - sources and destinations don't match",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 10000,
					Scale: 2,
					Source: Source{
						From: []FromTo{
							{Account: "0#source1", Amount: &Amount{Asset: "USD", Value: 10000, Scale: 2}},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{Account: "0#dest1", Amount: &Amount{Asset: "USD", Value: 5000, Scale: 2}},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  constant.ErrTransactionValueMismatch.Error(),
		},
		{
			name: "ambiguous transaction - same account in source and destination",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 10000,
					Scale: 2,
					Source: Source{
						From: []FromTo{
							{Account: "0#account1", Amount: &Amount{Asset: "USD", Value: 10000, Scale: 2}},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{Account: "0#account1", Amount: &Amount{Asset: "USD", Value: 10000, Scale: 2}},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  constant.ErrTransactionAmbiguous.Error(),
		},
		{
			name: "multiple sources and destinations with shares",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 10000,
					Scale: 2,
					Source: Source{
						From: []FromTo{
							{Account: "0#source1", Share: &Share{Percentage: 60}},
							{Account: "1#source2", Share: &Share{Percentage: 40}},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{Account: "0#dest1", Amount: &Amount{Asset: "USD", Value: 7000, Scale: 2}},
							{Account: "1#dest2", Amount: &Amount{Asset: "USD", Value: 3000, Scale: 2}},
						},
					},
				},
			},
			want: &Responses{
				Total: 10000,
				Asset: "USD",
				From: map[string]Amount{
					"0#source1": {Asset: "USD", Value: 6000, Scale: 2},
					"1#source2": {Asset: "USD", Value: 4000, Scale: 2},
				},
				To: map[string]Amount{
					"0#dest1": {Asset: "USD", Value: 7000, Scale: 2},
					"1#dest2": {Asset: "USD", Value: 3000, Scale: 2},
				},
				Sources:      []string{"source1", "source2"},
				Destinations: []string{"dest1", "dest2"},
				Aliases:      []string{"source1", "source2", "dest1", "dest2"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateSendSourceAndDistribute(tt.transaction)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want.Total, got.Total)
				assert.Equal(t, tt.want.Asset, got.Asset)
				assert.Equal(t, tt.want.From, got.From)
				assert.Equal(t, tt.want.To, got.To)
				assert.ElementsMatch(t, tt.want.Sources, got.Sources)
				assert.ElementsMatch(t, tt.want.Destinations, got.Destinations)
				assert.ElementsMatch(t, tt.want.Aliases, got.Aliases)
			}
		})
	}
}

func TestUpdateBalances(t *testing.T) {
	tests := []struct {
		name         string
		operation    string
		fromTo       map[string]Amount
		balances     []*Balance
		wantBalances []*Balance
		wantErr      bool
	}{
		{
			name:      "update balances with debit",
			operation: constant.DEBIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 50, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:             "id1",
					Alias:          "acc1",
					OrganizationID: "org1",
					LedgerID:       "ledger1",
					AssetCode:      "USD",
					Available:      100,
					Scale:          2,
					OnHold:         10,
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "standard",
					Version:        1,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
				},
			},
			wantBalances: []*Balance{
				{
					ID:             "id1",
					Alias:          "acc1",
					OrganizationID: "org1",
					LedgerID:       "ledger1",
					AssetCode:      "USD",
					Available:      50,
					Scale:          2,
					OnHold:         10,
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "standard",
					Version:        1,
				},
			},
			wantErr: false,
		},
		{
			name:      "update balances with credit",
			operation: constant.CREDIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 50, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 100,
					Scale:     2,
					OnHold:    10,
				},
			},
			wantBalances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 150,
					Scale:     2,
					OnHold:    10,
				},
			},
			wantErr: false,
		},
		{
			name:      "update multiple balances",
			operation: constant.CREDIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 50, Scale: 2},
				"acc2": {Value: 100, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 100,
					Scale:     2,
				},
				{
					ID:        "id2",
					Alias:     "acc2",
					Available: 200,
					Scale:     2,
				},
			},
			wantBalances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: 150,
					Scale:     2,
				},
				{
					ID:        "id2",
					Alias:     "acc2",
					Available: 300,
					Scale:     2,
				},
			},
			wantErr: false,
		},
		{
			name:      "overflow error",
			operation: constant.CREDIT,
			fromTo: map[string]Amount{
				"acc1": {Value: 2, Scale: 2},
			},
			balances: []*Balance{
				{
					ID:        "id1",
					Alias:     "acc1",
					Available: math.MaxInt64 - 1,
					Scale:     2,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultChan := make(chan []*Balance, 1)
			errChan := make(chan error, 1)

			go UpdateBalances(tt.operation, tt.fromTo, tt.balances, resultChan, errChan)

			select {
			case err := <-errChan:
				if !tt.wantErr {
					t.Errorf("unexpected error: %v", err)
				}
			case result := <-resultChan:
				if tt.wantErr {
					t.Error("expected error but got none")
				} else {
					assert.Len(t, result, len(tt.wantBalances))
					for i, bal := range result {
						assert.Equal(t, tt.wantBalances[i].ID, bal.ID)
						assert.Equal(t, tt.wantBalances[i].Alias, bal.Alias)
						assert.Equal(t, tt.wantBalances[i].Available, bal.Available)
						assert.Equal(t, tt.wantBalances[i].Scale, bal.Scale)
						assert.Equal(t, tt.wantBalances[i].OnHold, bal.OnHold)
					}
				}
			case <-time.After(1 * time.Second):
				t.Error("test timed out")
			}
		})
	}
}

func TestValidateBalancesRules(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		transaction Transaction
		validate    Responses
		balances    []*Balance
		wantErr     bool
		errMsg      string
	}{
		{
			name: "valid transaction with sufficient funds",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 50,
					Scale: 2,
					Source: Source{
						From: []FromTo{
							{Account: "acc1", Amount: &Amount{Value: 50, Scale: 2}},
						},
					},
				},
			},
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
				To: map[string]Amount{
					"acc2": {Value: 50, Scale: 2},
				},
			},
			balances: []*Balance{
				{
					ID:           "id1",
					Alias:        "acc1",
					AssetCode:    "USD",
					Available:    100,
					Scale:        2,
					AllowSending: true,
					AccountType:  "standard",
				},
				{
					ID:             "id2",
					Alias:          "acc2",
					AssetCode:      "USD",
					Available:      0,
					Scale:          2,
					AllowReceiving: true,
					AccountType:    "standard",
				},
			},
			wantErr: false,
		},
		{
			name: "insufficient balance count",
			validate: Responses{
				From: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
				To: map[string]Amount{
					"acc2": {Value: 50, Scale: 2},
				},
			},
			balances: []*Balance{
				{ID: "id1", Alias: "acc1"},
			},
			wantErr: true,
			errMsg:  constant.ErrAccountIneligibility.Error(),
		},
		{
			name: "insufficient funds",
			transaction: Transaction{
				Send: Send{
					Source: Source{
						From: []FromTo{
							{Account: "acc1", Amount: &Amount{Value: 200, Scale: 2}},
						},
					},
				},
			},
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"acc1": {Value: 200, Scale: 2},
				},
				To: map[string]Amount{},
			},
			balances: []*Balance{
				{
					ID:           "id1",
					Alias:        "acc1",
					AssetCode:    "USD",
					Available:    100,
					Scale:        2,
					AllowSending: true,
					AccountType:  "standard",
				},
			},
			wantErr: true,
			errMsg:  constant.ErrInsufficientFunds.Error(),
		},
		{
			name: "asset code mismatch in from account",
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
				To: map[string]Amount{},
			},
			balances: []*Balance{
				{
					ID:           "id1",
					Alias:        "acc1",
					AssetCode:    "EUR",
					Available:    100,
					AllowSending: true,
				},
			},
			wantErr: true,
			errMsg:  constant.ErrAssetCodeNotFound.Error(),
		},
		{
			name: "account not allowed to send",
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
				To: map[string]Amount{},
			},
			balances: []*Balance{
				{
					ID:           "id1",
					Alias:        "acc1",
					AssetCode:    "USD",
					Available:    100,
					AllowSending: false,
				},
			},
			wantErr: true,
			errMsg:  constant.ErrAccountStatusTransactionRestriction.Error(),
		},
		{
			name: "account not allowed to receive",
			validate: Responses{
				Asset: "USD",
				From:  map[string]Amount{},
				To: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
			},
			balances: []*Balance{
				{
					ID:             "id1",
					Alias:          "acc1",
					AssetCode:      "USD",
					Available:      0,
					AllowReceiving: false,
				},
			},
			wantErr: true,
			errMsg:  constant.ErrAccountStatusTransactionRestriction.Error(),
		},
		{
			name: "external account with positive balance trying to receive",
			validate: Responses{
				Asset: "USD",
				From:  map[string]Amount{},
				To: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
			},
			balances: []*Balance{
				{
					ID:             "id1",
					Alias:          "acc1",
					AssetCode:      "USD",
					Available:      100,
					AllowReceiving: true,
					AccountType:    constant.ExternalAccountType,
				},
			},
			wantErr: true,
			errMsg:  constant.ErrInsufficientFunds.Error(),
		},
		{
			name: "external account can go negative when sending",
			transaction: Transaction{
				Send: Send{
					Source: Source{
						From: []FromTo{
							{Account: "acc1", Amount: &Amount{Value: 200, Scale: 2}},
						},
					},
				},
			},
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"acc1": {Value: 200, Scale: 2},
				},
				To: map[string]Amount{},
			},
			balances: []*Balance{
				{
					ID:           "id1",
					Alias:        "acc1",
					AssetCode:    "USD",
					Available:    100,
					Scale:        2,
					AllowSending: true,
					AccountType:  constant.ExternalAccountType,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBalancesRules(ctx, tt.transaction, tt.validate, tt.balances)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateFromToOperation(t *testing.T) {
	tests := []struct {
		name        string
		ft          FromTo
		validate    Responses
		balance     *Balance
		wantAmount  Amount
		wantBalance Balance
		wantErr     bool
		errMsg      string
	}{
		{
			name: "valid from operation",
			ft: FromTo{
				Account: "acc1",
				IsFrom:  true,
			},
			validate: Responses{
				From: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
			},
			balance: &Balance{
				Alias:       "acc1",
				Available:   100,
				Scale:       2,
				OnHold:      10,
				AccountType: "standard",
			},
			wantAmount:  Amount{Value: 50, Scale: 2},
			wantBalance: Balance{Available: 50, Scale: 2, OnHold: 10},
			wantErr:     false,
		},
		{
			name: "valid to operation",
			ft: FromTo{
				Account: "acc1",
				IsFrom:  false,
			},
			validate: Responses{
				To: map[string]Amount{
					"acc1": {Value: 50, Scale: 2},
				},
			},
			balance: &Balance{
				Alias:       "acc1",
				Available:   100,
				Scale:       2,
				OnHold:      10,
				AccountType: "standard",
			},
			wantAmount:  Amount{Value: 50, Scale: 2},
			wantBalance: Balance{Available: 150, Scale: 2, OnHold: 10},
			wantErr:     false,
		},
		{
			name: "insufficient funds for from operation",
			ft: FromTo{
				Account: "acc1",
				IsFrom:  true,
			},
			validate: Responses{
				From: map[string]Amount{
					"acc1": {Value: 150, Scale: 2},
				},
			},
			balance: &Balance{
				Alias:       "acc1",
				Available:   100,
				Scale:       2,
				OnHold:      0,
				AccountType: "standard",
			},
			wantErr: true,
			errMsg:  constant.ErrInsufficientFunds.Error(),
		},
		{
			name: "external account can go negative",
			ft: FromTo{
				Account: "acc1",
				IsFrom:  true,
			},
			validate: Responses{
				From: map[string]Amount{
					"acc1": {Value: 150, Scale: 2},
				},
			},
			balance: &Balance{
				Alias:       "acc1",
				Available:   100,
				Scale:       2,
				OnHold:      0,
				AccountType: constant.ExternalAccountType,
			},
			wantAmount:  Amount{Value: 150, Scale: 2},
			wantBalance: Balance{Available: -50, Scale: 2, OnHold: 0},
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAmount, gotBalance, err := ValidateFromToOperation(tt.ft, tt.validate, tt.balance)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantAmount, gotAmount)
				assert.Equal(t, tt.wantBalance.Available, gotBalance.Available)
				assert.Equal(t, tt.wantBalance.Scale, gotBalance.Scale)
				assert.Equal(t, tt.wantBalance.OnHold, gotBalance.OnHold)
			}
		})
	}
}

func TestEdgeCasesAndBoundaries(t *testing.T) {
	t.Run("Scale with extreme values", func(t *testing.T) {
		// Test with very large scale differences
		result := Scale(1, 0, 15)
		assert.Equal(t, int64(1000000000000000), result)

		// Test with negative scale (scaling down)
		result = Scale(1000000000000000, 15, 0)
		assert.Equal(t, int64(1), result)
	})

	t.Run("FindScale with very small decimals", func(t *testing.T) {
		amount := FindScale("BTC", 0.00000001, 8) // 1 satoshi
		assert.Equal(t, int64(1), amount.Value)
		assert.Equal(t, int64(8), amount.Scale)
	})

	t.Run("Normalize with extreme scale differences", func(t *testing.T) {
		total := Amount{Value: 1, Scale: 0}
		amount := Amount{Value: 1, Scale: 18}
		remaining := Amount{Value: 1000000000000000000, Scale: 18}

		Normalize(&total, &amount, &remaining)

		assert.Equal(t, int64(1000000000000000001), total.Value)
		assert.Equal(t, int64(18), total.Scale)
		assert.Equal(t, int64(999999999999999999), remaining.Value)
	})

	t.Run("OperateBalances with max values", func(t *testing.T) {
		// Test adding 1 to max value - 1
		amount := Amount{Value: 1, Scale: 0}
		balance := Balance{Available: math.MaxInt64 - 1, Scale: 0}

		result, err := OperateBalances(amount, balance, constant.CREDIT)
		assert.NoError(t, err)
		assert.Equal(t, int64(math.MaxInt64), result.Available)

		// Test adding 1 more should overflow
		balance.Available = math.MaxInt64
		_, err = OperateBalances(amount, balance, constant.CREDIT)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), constant.ErrOverFlowInt64.Error())
	})

	t.Run("CalculateTotal with zero percentage", func(t *testing.T) {
		fromTos := []FromTo{
			{Account: "0#acc1", Share: &Share{Percentage: 0}},
		}
		send := Send{Asset: "USD", Value: 10000, Scale: 2}

		totalChan := make(chan int64, 1)
		fromToChan := make(chan map[string]Amount, 1)
		scdtChan := make(chan []string, 1)

		go CalculateTotal(fromTos, send, totalChan, fromToChan, scdtChan)

		gotTotal := <-totalChan
		gotFromTo := <-fromToChan
		<-scdtChan

		assert.Equal(t, int64(0), gotTotal)
		assert.Equal(t, int64(0), gotFromTo["0#acc1"].Value)
	})
}
