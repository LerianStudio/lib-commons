package transaction

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestBalance_IsEmpty(t *testing.T) {
	tests := []struct {
		name string
		rate Rate
		want bool
	}{
		{
			name: "Empty rate",
			rate: Rate{},
			want: true,
		},
		{
			name: "Non-empty rate",
			rate: Rate{
				From:       "BRL",
				To:         "USD",
				Value:      decimal.NewFromInt(100),
				ExternalID: "00000000-0000-0000-0000-000000000000",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rate.IsEmpty()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFromTo_SplitAlias(t *testing.T) {
	tests := []struct {
		name         string
		accountAlias string
		want         string
	}{
		{
			name:         "Alias without index",
			accountAlias: "@person1",
			want:         "@person1",
		},
		{
			name:         "Alias with index",
			accountAlias: "1#@person1",
			want:         "@person1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := FromTo{
				AccountAlias: tt.accountAlias,
			}
			got := ft.SplitAlias()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFromTo_ConcatAlias(t *testing.T) {
	tests := []struct {
		name         string
		accountAlias string
		balanceKey   string
		index        int
		want         string
	}{
		{
			name:         "Concat index with alias and balance key",
			accountAlias: "@person1",
			balanceKey:   "savings",
			index:        1,
			want:         "1#@person1#savings",
		},
		{
			name:         "Concat index with alias and empty balance key",
			accountAlias: "@person2",
			balanceKey:   "",
			index:        0,
			want:         "0#@person2#",
		},
		{
			name:         "Concat index with alias and default balance key",
			accountAlias: "@person3",
			balanceKey:   "default",
			index:        2,
			want:         "2#@person3#default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := FromTo{
				AccountAlias: tt.accountAlias,
				BalanceKey:   tt.balanceKey,
			}
			got := ft.ConcatAlias(tt.index)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTransaction_IsEmpty(t *testing.T) {
	tests := []struct {
		name        string
		transaction Transaction
		want        bool
	}{
		{
			name: "Empty transaction",
			transaction: Transaction{
				Send: Send{
					Asset: "",
					Value: decimal.NewFromInt(0),
				},
			},
			want: true,
		},
		{
			name: "Non-empty transaction with asset",
			transaction: Transaction{
				Send: Send{
					Asset: "BRL",
					Value: decimal.NewFromInt(0),
				},
			},
			want: false,
		},
		{
			name: "Non-empty transaction with value",
			transaction: Transaction{
				Send: Send{
					Asset: "",
					Value: decimal.NewFromInt(100),
				},
			},
			want: false,
		},
		{
			name: "Complete non-empty transaction",
			transaction: Transaction{
				Send: Send{
					Asset: "BRL",
					Value: decimal.NewFromInt(100),
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.transaction.IsEmpty()
			assert.Equal(t, tt.want, got)
		})
	}
}
