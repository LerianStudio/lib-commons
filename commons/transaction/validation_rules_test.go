package transaction

import (
	"context"
	"testing"

	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
