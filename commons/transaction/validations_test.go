package transaction

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/commons"
	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
)

func TestValidateBalancesRules(t *testing.T) {
	// Create a context with logger and tracer
	ctx := context.Background()
	logger := &log.GoLogger{Level: log.InfoLevel}
	ctx = commons.ContextWithLogger(ctx, logger)
	tracer := otel.Tracer("test")
	ctx = commons.ContextWithTracer(ctx, tracer)

	tests := []struct {
		name        string
		transaction Transaction
		validate    Responses
		balances    []*Balance
		expectError bool
		errorCode   string
	}{
		{
			name: "valid balances - simple transfer",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{AccountAlias: "@account1"},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{AccountAlias: "@account2"},
						},
					},
				},
			},
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"@account1": {Value: decimal.NewFromInt(100), Operation: constant.DEBIT, TransactionType: constant.CREATED},
				},
				To: map[string]Amount{
					"@account2": {Value: decimal.NewFromInt(100), Operation: constant.CREDIT, TransactionType: constant.CREATED},
				},
			},
			balances: []*Balance{
				{
					ID:             "123",
					Alias:          "@account1",
					AssetCode:      "USD",
					Available:      decimal.NewFromInt(200),
					OnHold:         decimal.NewFromInt(0),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "internal",
				},
				{
					ID:             "456",
					Alias:          "@account2",
					AssetCode:      "USD",
					Available:      decimal.NewFromInt(50),
					OnHold:         decimal.NewFromInt(0),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "internal",
				},
			},
			expectError: false,
		},
		{
			name: "invalid - insufficient funds",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{AccountAlias: "@account1"},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{AccountAlias: "@account2"},
						},
					},
				},
			},
			validate: Responses{
				Asset: "USD",
				From: map[string]Amount{
					"@account1": {Value: decimal.NewFromInt(100), Operation: constant.DEBIT, TransactionType: constant.CREATED},
				},
				To: map[string]Amount{
					"@account2": {Value: decimal.NewFromInt(100), Operation: constant.CREDIT, TransactionType: constant.CREATED},
				},
			},
			balances: []*Balance{
				{
					ID:             "123",
					Alias:          "@account1",
					AssetCode:      "USD",
					Available:      decimal.NewFromInt(50), // Insufficient funds
					OnHold:         decimal.NewFromInt(0),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "internal",
				},
				{
					ID:             "456",
					Alias:          "@account2",
					AssetCode:      "USD",
					Available:      decimal.NewFromInt(50),
					OnHold:         decimal.NewFromInt(0),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    "internal",
				},
			},
			expectError: true,
			errorCode:   "0018", // ErrInsufficientFunds
		},
		{
			name:        "invalid - wrong number of balances",
			transaction: Transaction{},
			validate: Responses{
				From: map[string]Amount{
					"@account1": {Value: decimal.NewFromInt(100), Operation: constant.DEBIT, TransactionType: constant.CREATED},
				},
				To: map[string]Amount{
					"@account2": {Value: decimal.NewFromInt(100), Operation: constant.CREDIT, TransactionType: constant.CREATED},
				},
			},
			balances:    []*Balance{}, // Empty balances
			expectError: true,
			errorCode:   "0019", // ErrAccountIneligibility
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBalancesRules(ctx, tt.transaction, tt.validate, tt.balances)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCode != "" {
					// Check if the error is a Response type and contains the error code
					if respErr, ok := err.(commons.Response); ok {
						assert.Equal(t, tt.errorCode, respErr.Code)
					} else {
						assert.Contains(t, err.Error(), tt.errorCode)
					}
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateFromBalances(t *testing.T) {
	tests := []struct {
		name        string
		balance     *Balance
		from        map[string]Amount
		asset       string
		expectError bool
		errorCode   string
	}{
		{
			name: "valid from balance",
			balance: &Balance{
				ID:           "123",
				Alias:        "@account1",
				AssetCode:    "USD",
				Available:    decimal.NewFromInt(100),
				AllowSending: true,
				AccountType:  "internal",
			},
			from: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: false,
		},
		{
			name: "invalid - wrong asset code",
			balance: &Balance{
				ID:           "123",
				Alias:        "@account1",
				AssetCode:    "EUR",
				Available:    decimal.NewFromInt(100),
				AllowSending: true,
				AccountType:  "internal",
			},
			from: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: true,
			errorCode:   "0034", // ErrAssetCodeNotFound
		},
		{
			name: "invalid - sending not allowed",
			balance: &Balance{
				ID:           "123",
				Alias:        "@account1",
				AssetCode:    "USD",
				Available:    decimal.NewFromInt(100),
				AllowSending: false,
				AccountType:  "internal",
			},
			from: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: true,
			errorCode:   "0024", // ErrAccountStatusTransactionRestriction
		},
		{
			name: "invalid - zero balance for internal account",
			balance: &Balance{
				ID:           "123",
				Alias:        "@account1",
				AssetCode:    "USD",
				Available:    decimal.NewFromInt(0),
				AllowSending: true,
				AccountType:  "internal",
			},
			from: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: true,
			errorCode:   "0018", // ErrInsufficientFunds
		},
		{
			name: "valid - external account with zero balance",
			balance: &Balance{
				ID:           "123",
				Alias:        "@external",
				AssetCode:    "USD",
				Available:    decimal.NewFromInt(0),
				AllowSending: true,
				AccountType:  constant.ExternalAccountType,
			},
			from: map[string]Amount{
				"@external": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFromBalances(tt.balance, tt.from, tt.asset, false)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCode != "" {
					// Check if the error is a Response type and contains the error code
					if respErr, ok := err.(commons.Response); ok {
						assert.Equal(t, tt.errorCode, respErr.Code)
					} else {
						assert.Contains(t, err.Error(), tt.errorCode)
					}
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateToBalances(t *testing.T) {
	tests := []struct {
		name        string
		balance     *Balance
		to          map[string]Amount
		asset       string
		expectError bool
		errorCode   string
	}{
		{
			name: "valid to balance",
			balance: &Balance{
				ID:             "123",
				Alias:          "@account1",
				AssetCode:      "USD",
				Available:      decimal.NewFromInt(100),
				AllowReceiving: true,
				AccountType:    "internal",
			},
			to: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: false,
		},
		{
			name: "invalid - wrong asset code",
			balance: &Balance{
				ID:             "123",
				Alias:          "@account1",
				AssetCode:      "EUR",
				Available:      decimal.NewFromInt(100),
				AllowReceiving: true,
				AccountType:    "internal",
			},
			to: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: true,
			errorCode:   "0034", // ErrAssetCodeNotFound
		},
		{
			name: "invalid - receiving not allowed",
			balance: &Balance{
				ID:             "123",
				Alias:          "@account1",
				AssetCode:      "USD",
				Available:      decimal.NewFromInt(100),
				AllowReceiving: false,
				AccountType:    "internal",
			},
			to: map[string]Amount{
				"@account1": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: true,
			errorCode:   "0024", // ErrAccountStatusTransactionRestriction
		},
		{
			name: "invalid - external account with positive balance",
			balance: &Balance{
				ID:             "123",
				Alias:          "@external",
				AssetCode:      "USD",
				Available:      decimal.NewFromInt(100),
				AllowReceiving: true,
				AccountType:    constant.ExternalAccountType,
			},
			to: map[string]Amount{
				"@external": {Value: decimal.NewFromInt(50)},
			},
			asset:       "USD",
			expectError: true,
			errorCode:   "0018", // ErrInsufficientFunds
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToBalances(tt.balance, tt.to, tt.asset)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCode != "" {
					// Check if the error is a Response type and contains the error code
					if respErr, ok := err.(commons.Response); ok {
						assert.Equal(t, tt.errorCode, respErr.Code)
					} else {
						assert.Contains(t, err.Error(), tt.errorCode)
					}
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOperateBalances(t *testing.T) {
	tests := []struct {
		name        string
		amount      Amount
		balance     Balance
		operation   string
		expected    Balance
		expectError bool
	}{
		{
			name: "debit operation",
			amount: Amount{
				Value:           decimal.NewFromInt(50),
				Operation:       constant.DEBIT,
				TransactionType: constant.CREATED,
			},
			balance: Balance{
				Available: decimal.NewFromInt(100),
				OnHold:    decimal.NewFromInt(10),
			},
			expected: Balance{
				Available: decimal.NewFromInt(50), // 100 - 50 = 50
				OnHold:    decimal.NewFromInt(10),
			},
			expectError: false,
		},
		{
			name: "credit operation",
			amount: Amount{
				Value:           decimal.NewFromInt(50),
				Operation:       constant.CREDIT,
				TransactionType: constant.CREATED,
			},
			balance: Balance{
				Available: decimal.NewFromInt(100),
				OnHold:    decimal.NewFromInt(10),
			},
			expected: Balance{
				Available: decimal.NewFromInt(150), // 100 + 50 = 150
				OnHold:    decimal.NewFromInt(10),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := OperateBalances(tt.amount, tt.balance)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected.Available.String(), result.Available.String())
				assert.Equal(t, tt.expected.OnHold.String(), result.OnHold.String())
			}
		})
	}
}

func TestSplitAlias(t *testing.T) {
	tests := []struct {
		name  string
		alias string
		want  string
	}{
		{
			name:  "alias without index",
			alias: "@person1",
			want:  "@person1",
		},
		{
			name:  "alias with index",
			alias: "1#@person1",
			want:  "@person1",
		},
		{
			name:  "alias with zero index",
			alias: "0#@person1",
			want:  "@person1",
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
			name:  "concat with positive index",
			index: 1,
			alias: "@person1",
			want:  "1#@person1",
		},
		{
			name:  "concat with zero index",
			index: 0,
			alias: "@person2",
			want:  "0#@person2",
		},
		{
			name:  "concat with large index",
			index: 999,
			alias: "@person3",
			want:  "999#@person3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConcatAlias(tt.index, tt.alias)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAppendIfNotExist(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     []string
		want  []string
	}{
		{
			name:  "append new elements",
			slice: []string{"a", "b"},
			s:     []string{"c", "d"},
			want:  []string{"a", "b", "c", "d"},
		},
		{
			name:  "skip existing elements",
			slice: []string{"a", "b"},
			s:     []string{"b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "all elements exist",
			slice: []string{"a", "b", "c"},
			s:     []string{"a", "b"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "empty initial slice",
			slice: []string{},
			s:     []string{"a", "b"},
			want:  []string{"a", "b"},
		},
		{
			name:  "empty append slice",
			slice: []string{"a", "b"},
			s:     []string{},
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppendIfNotExist(tt.slice, tt.s)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateSendSourceAndDistribute(t *testing.T) {
	tests := []struct {
		name        string
		transaction Transaction
		want        *Responses
		expectError bool
		errorCode   string
	}{
		{
			name: "valid - simple source and distribute",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{
								AccountAlias: "@account1",
								Amount: &Amount{
									Asset: "USD",
									Value: decimal.NewFromInt(100),
								},
							},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{
								AccountAlias: "@account2",
								Amount: &Amount{
									Asset: "USD",
									Value: decimal.NewFromInt(100),
								},
							},
						},
					},
				},
			},
			expectError: false, // Now expects success after fixing CalculateTotal
		},
		{
			name: "valid - multiple sources and distributes",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{
								AccountAlias: "@account1",
								Amount: &Amount{
									Asset: "USD",
									Value: decimal.NewFromInt(50),
								},
							},
							{
								AccountAlias: "@account2",
								Amount: &Amount{
									Asset: "USD",
									Value: decimal.NewFromInt(50),
								},
							},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{
								AccountAlias: "@account3",
								Amount: &Amount{
									Asset: "USD",
									Value: decimal.NewFromInt(60),
								},
							},
							{
								AccountAlias: "@account4",
								Amount: &Amount{
									Asset: "USD",
									Value: decimal.NewFromInt(40),
								},
							},
						},
					},
				},
			},
			expectError: false, // Now expects success after fixing CalculateTotal
		},
		{
			name: "valid transaction with shares",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{
								AccountAlias: "@account1",
								Share: &Share{
									Percentage: 60,
								},
							},
							{
								AccountAlias: "@account2",
								Share: &Share{
									Percentage: 40,
								},
							},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{
								AccountAlias: "@account3",
								Share: &Share{
									Percentage: 100,
								},
							},
						},
					},
				},
			},
			want: &Responses{
				Asset: "USD",
				From: map[string]Amount{
					"@account1": {Value: decimal.NewFromInt(60)},
					"@account2": {Value: decimal.NewFromInt(40)},
				},
				To: map[string]Amount{
					"@account3": {Value: decimal.NewFromInt(100)},
				},
			},
			expectError: false,
		},
		{
			name: "valid transaction with amounts",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{
								AccountAlias: "@account1",
								Amount: &Amount{
									Value: decimal.NewFromInt(60),
								},
							},
							{
								AccountAlias: "@account2",
								Amount: &Amount{
									Value: decimal.NewFromInt(40),
								},
							},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{
								AccountAlias: "@account3",
								Amount: &Amount{
									Value: decimal.NewFromInt(100),
								},
							},
						},
					},
				},
			},
			want: &Responses{
				Asset: "USD",
				From: map[string]Amount{
					"@account1": {Value: decimal.NewFromInt(60)},
					"@account2": {Value: decimal.NewFromInt(40)},
				},
				To: map[string]Amount{
					"@account3": {Value: decimal.NewFromInt(100)},
				},
			},
			expectError: false,
		},
		{
			name: "invalid - total mismatch",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: decimal.NewFromInt(100),
					Source: Source{
						From: []FromTo{
							{
								AccountAlias: "@account1",
								Amount: &Amount{
									Value: decimal.NewFromInt(60),
								},
							},
							{
								AccountAlias: "@account2",
								Amount: &Amount{
									Value: decimal.NewFromInt(30), // Total is 90, not 100
								},
							},
						},
					},
					Distribute: Distribute{
						To: []FromTo{
							{
								AccountAlias: "@account3",
								Amount: &Amount{
									Value: decimal.NewFromInt(100),
								},
							},
						},
					},
				},
			},
			expectError: true,
			errorCode:   "0073", // ErrTransactionValueMismatch
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateSendSourceAndDistribute(tt.transaction, constant.CREATED)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCode != "" {
					// Check if the error is a Response type and contains the error code
					if respErr, ok := err.(commons.Response); ok {
						assert.Equal(t, tt.errorCode, respErr.Code)
					} else {
						assert.Contains(t, err.Error(), tt.errorCode)
					}
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
				if tt.want != nil && got != nil {
					assert.Equal(t, tt.want.Asset, got.Asset)
					assert.Equal(t, len(tt.want.From), len(got.From))
					assert.Equal(t, len(tt.want.To), len(got.To))
				}
			}
		})
	}
}
