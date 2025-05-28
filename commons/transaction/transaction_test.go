package transaction

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRate_IsEmpty(t *testing.T) {
	tests := []struct {
		name string
		rate Rate
		want bool
	}{
		{
			name: "empty rate",
			rate: Rate{},
			want: true,
		},
		{
			name: "rate with only ExternalID",
			rate: Rate{ExternalID: "123e4567-e89b-12d3-a456-426614174000"},
			want: false,
		},
		{
			name: "rate with only From",
			rate: Rate{From: "USD"},
			want: false,
		},
		{
			name: "rate with only To",
			rate: Rate{To: "EUR"},
			want: false,
		},
		{
			name: "rate with only Value",
			rate: Rate{Value: 100},
			want: false,
		},
		{
			name: "complete rate",
			rate: Rate{
				ExternalID: "123e4567-e89b-12d3-a456-426614174000",
				From:       "USD",
				To:         "EUR",
				Value:      110,
				Scale:      2,
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
		name   string
		fromTo FromTo
		want   string
	}{
		{
			name:   "account with hash",
			fromTo: FromTo{Account: "0#account1"},
			want:   "account1",
		},
		{
			name:   "account without hash",
			fromTo: FromTo{Account: "account1"},
			want:   "account1",
		},
		{
			name:   "accountAlias with hash",
			fromTo: FromTo{AccountAlias: "1#alias1"},
			want:   "alias1",
		},
		{
			name:   "accountAlias without hash",
			fromTo: FromTo{AccountAlias: "alias1"},
			want:   "alias1",
		},
		{
			name:   "both account and accountAlias - account takes precedence",
			fromTo: FromTo{Account: "0#account1", AccountAlias: "1#alias1"},
			want:   "account1",
		},
		{
			name:   "empty account, use accountAlias",
			fromTo: FromTo{Account: "", AccountAlias: "alias1"},
			want:   "alias1",
		},
		{
			name:   "multiple hashes - split by first hash",
			fromTo: FromTo{Account: "0#account#with#hash"},
			want:   "account#with#hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fromTo.SplitAlias()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFromTo_ConcatAlias(t *testing.T) {
	tests := []struct {
		name   string
		fromTo FromTo
		index  int
		want   string
	}{
		{
			name:   "concat with account",
			fromTo: FromTo{Account: "account1"},
			index:  0,
			want:   "0#account1",
		},
		{
			name:   "concat with accountAlias when account is empty",
			fromTo: FromTo{AccountAlias: "alias1"},
			index:  1,
			want:   "1#alias1",
		},
		{
			name:   "concat with account when both are present",
			fromTo: FromTo{Account: "account1", AccountAlias: "alias1"},
			index:  2,
			want:   "2#account1",
		},
		{
			name:   "concat with large index",
			fromTo: FromTo{Account: "account1"},
			index:  999,
			want:   "999#account1",
		},
		{
			name:   "concat with negative index",
			fromTo: FromTo{Account: "account1"},
			index:  -1,
			want:   "-1#account1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fromTo.ConcatAlias(tt.index)
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
			name:        "empty transaction",
			transaction: Transaction{},
			want:        true,
		},
		{
			name: "transaction with only asset",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
				},
			},
			want: false,
		},
		{
			name: "transaction with only value",
			transaction: Transaction{
				Send: Send{
					Value: 100,
				},
			},
			want: false,
		},
		{
			name: "transaction with only scale",
			transaction: Transaction{
				Send: Send{
					Scale: 2,
				},
			},
			want: false,
		},
		{
			name: "transaction with all send fields",
			transaction: Transaction{
				Send: Send{
					Asset: "USD",
					Value: 100,
					Scale: 2,
				},
			},
			want: false,
		},
		{
			name: "transaction with metadata and description but empty send",
			transaction: Transaction{
				Description: "Test transaction",
				Metadata:    map[string]any{"key": "value"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.transaction.IsEmpty()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBalanceStructure(t *testing.T) {
	// Test Balance structure initialization and field access
	now := time.Now()
	deletedAt := time.Now().Add(24 * time.Hour)
	
	balance := Balance{
		ID:             "123e4567-e89b-12d3-a456-426614174000",
		OrganizationID: "223e4567-e89b-12d3-a456-426614174000",
		LedgerID:       "323e4567-e89b-12d3-a456-426614174000",
		AccountID:      "423e4567-e89b-12d3-a456-426614174000",
		Alias:          "@person1",
		AssetCode:      "USD",
		Available:      1500,
		OnHold:         500,
		Scale:          2,
		Version:        1,
		AccountType:    "creditCard",
		AllowSending:   true,
		AllowReceiving: true,
		CreatedAt:      now,
		UpdatedAt:      now,
		DeletedAt:      &deletedAt,
		Metadata:       map[string]any{"category": "premium"},
	}

	assert.Equal(t, "123e4567-e89b-12d3-a456-426614174000", balance.ID)
	assert.Equal(t, "@person1", balance.Alias)
	assert.Equal(t, int64(1500), balance.Available)
	assert.Equal(t, int64(500), balance.OnHold)
	assert.True(t, balance.AllowSending)
	assert.True(t, balance.AllowReceiving)
	assert.NotNil(t, balance.DeletedAt)
	assert.Equal(t, "premium", balance.Metadata["category"])
}

func TestResponsesStructure(t *testing.T) {
	// Test Responses structure
	responses := Responses{
		Total: 1000,
		Asset: "USD",
		From: map[string]Amount{
			"account1": {Asset: "USD", Value: 500, Scale: 2},
			"account2": {Asset: "USD", Value: 500, Scale: 2},
		},
		To: map[string]Amount{
			"account3": {Asset: "USD", Value: 1000, Scale: 2},
		},
		Sources:      []string{"account1", "account2"},
		Destinations: []string{"account3"},
		Aliases:      []string{"@user1", "@user2", "@user3"},
	}

	assert.Equal(t, int64(1000), responses.Total)
	assert.Equal(t, "USD", responses.Asset)
	assert.Len(t, responses.From, 2)
	assert.Len(t, responses.To, 1)
	assert.Len(t, responses.Sources, 2)
	assert.Len(t, responses.Destinations, 1)
	assert.Len(t, responses.Aliases, 3)
}

func TestAmountStructure(t *testing.T) {
	// Test Amount structure with different operations
	tests := []struct {
		name   string
		amount Amount
		checks func(t *testing.T, a Amount)
	}{
		{
			name: "debit operation",
			amount: Amount{
				Asset:     "EUR",
				Value:     2500,
				Scale:     2,
				Operation: "debit",
			},
			checks: func(t *testing.T, a Amount) {
				assert.Equal(t, "EUR", a.Asset)
				assert.Equal(t, int64(2500), a.Value)
				assert.Equal(t, int64(2), a.Scale)
				assert.Equal(t, "debit", a.Operation)
			},
		},
		{
			name: "credit operation",
			amount: Amount{
				Asset:     "GBP",
				Value:     10000,
				Scale:     4,
				Operation: "credit",
			},
			checks: func(t *testing.T, a Amount) {
				assert.Equal(t, "GBP", a.Asset)
				assert.Equal(t, int64(10000), a.Value)
				assert.Equal(t, int64(4), a.Scale)
				assert.Equal(t, "credit", a.Operation)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.checks(t, tt.amount)
		})
	}
}

func TestShareStructure(t *testing.T) {
	// Test Share structure
	share := Share{
		Percentage:             75,
		PercentageOfPercentage: 50,
	}

	assert.Equal(t, int64(75), share.Percentage)
	assert.Equal(t, int64(50), share.PercentageOfPercentage)
}

func TestSendStructure(t *testing.T) {
	// Test Send structure with nested components
	send := Send{
		Asset: "BRL",
		Value: 50000,
		Scale: 2,
		Source: Source{
			Remaining: "remaining",
			From: []FromTo{
				{
					Account: "@source1",
					Amount:  &Amount{Asset: "BRL", Value: 30000, Scale: 2},
				},
				{
					Account: "@source2",
					Share:   &Share{Percentage: 40},
				},
			},
		},
		Distribute: Distribute{
			To: []FromTo{
				{
					Account: "@dest1",
					Amount:  &Amount{Asset: "BRL", Value: 50000, Scale: 2},
				},
			},
		},
	}

	assert.Equal(t, "BRL", send.Asset)
	assert.Equal(t, int64(50000), send.Value)
	assert.Equal(t, int64(2), send.Scale)
	assert.Len(t, send.Source.From, 2)
	assert.Len(t, send.Distribute.To, 1)
}

func TestComplexTransactionStructure(t *testing.T) {
	// Test a complex transaction with all fields populated
	transaction := Transaction{
		ChartOfAccountsGroupName: "1000",
		Description:              "Complex payment transaction",
		Code:                     "TRX-001",
		Pending:                  false,
		Metadata: map[string]any{
			"reference": "INV-2024-001",
			"category":  "payment",
		},
		Send: Send{
			Asset: "USD",
			Value: 100000, // $1000.00
			Scale: 2,
			Source: Source{
				From: []FromTo{
					{
						Account:         "@payer1",
						AccountAlias:    "@payer1",
						Amount:          &Amount{Asset: "USD", Value: 60000, Scale: 2},
						Description:     "Payment from payer1",
						ChartOfAccounts: "1100",
						Metadata:        map[string]any{"type": "primary"},
						IsFrom:          true,
					},
					{
						Account:      "@payer2",
						AccountAlias: "@payer2",
						Share:        &Share{Percentage: 40, PercentageOfPercentage: 100},
						Description:  "Payment from payer2",
						Metadata:     map[string]any{"type": "secondary"},
						IsFrom:       true,
					},
				},
			},
			Distribute: Distribute{
				To: []FromTo{
					{
						Account:         "@merchant",
						AccountAlias:    "@merchant",
						Amount:          &Amount{Asset: "USD", Value: 95000, Scale: 2},
						Description:     "Payment to merchant",
						ChartOfAccounts: "2100",
						Metadata:        map[string]any{"merchant_id": "M123"},
					},
					{
						Account:      "@fee_collector",
						AccountAlias: "@fee_collector",
						Amount:       &Amount{Asset: "USD", Value: 5000, Scale: 2},
						Description:  "Transaction fee",
						Metadata:     map[string]any{"fee_type": "processing"},
					},
				},
			},
		},
	}

	// Verify transaction structure
	assert.Equal(t, "Complex payment transaction", transaction.Description)
	assert.Equal(t, "TRX-001", transaction.Code)
	assert.False(t, transaction.Pending)
	assert.Equal(t, "payment", transaction.Metadata["category"])

	// Verify send structure
	assert.Equal(t, "USD", transaction.Send.Asset)
	assert.Equal(t, int64(100000), transaction.Send.Value)

	// Verify sources
	assert.Len(t, transaction.Send.Source.From, 2)
	assert.Equal(t, "@payer1", transaction.Send.Source.From[0].Account)
	assert.Equal(t, int64(60000), transaction.Send.Source.From[0].Amount.Value)
	assert.Equal(t, int64(40), transaction.Send.Source.From[1].Share.Percentage)

	// Verify destinations
	assert.Len(t, transaction.Send.Distribute.To, 2)
	assert.Equal(t, "@merchant", transaction.Send.Distribute.To[0].Account)
	assert.Equal(t, int64(95000), transaction.Send.Distribute.To[0].Amount.Value)
	assert.Equal(t, "@fee_collector", transaction.Send.Distribute.To[1].Account)
	assert.Equal(t, int64(5000), transaction.Send.Distribute.To[1].Amount.Value)
}

func TestFromToHelperFunctions(t *testing.T) {
	// Test both instance methods and their usage
	testCases := []struct {
		name          string
		account       string
		accountAlias  string
		index         int
		expectedSplit string
		expectedConcat string
	}{
		{
			name:          "account with index",
			account:       "5#mainAccount",
			accountAlias:  "",
			index:         3,
			expectedSplit: "mainAccount",
			expectedConcat: "3#5#mainAccount",
		},
		{
			name:          "alias without index",
			account:       "",
			accountAlias:  "userAlias",
			index:         0,
			expectedSplit: "userAlias",
			expectedConcat: "0#userAlias",
		},
		{
			name:          "complex alias with special chars",
			account:       "10#user@domain.com",
			accountAlias:  "",
			index:         99,
			expectedSplit: "user@domain.com",
			expectedConcat: "99#10#user@domain.com",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ft := FromTo{
				Account:      tc.account,
				AccountAlias: tc.accountAlias,
			}

			// Test SplitAlias
			splitResult := ft.SplitAlias()
			assert.Equal(t, tc.expectedSplit, splitResult)

			// Test ConcatAlias
			concatResult := ft.ConcatAlias(tc.index)
			assert.Equal(t, tc.expectedConcat, concatResult)
		})
	}
}

func TestIndexHandling(t *testing.T) {
	// Test edge cases for index handling
	tests := []struct {
		name     string
		index    int
		account  string
		expected string
	}{
		{
			name:     "zero index",
			index:    0,
			account:  "account",
			expected: "0#account",
		},
		{
			name:     "negative index",
			index:    -100,
			account:  "account",
			expected: "-100#account",
		},
		{
			name:     "large index",
			index:    999999,
			account:  "account",
			expected: "999999#account",
		},
		{
			name:     "max int index",
			index:    int(^uint(0) >> 1), // max int
			account:  "account",
			expected: strconv.Itoa(int(^uint(0)>>1)) + "#account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := FromTo{Account: tt.account}
			result := ft.ConcatAlias(tt.index)
			assert.Equal(t, tt.expected, result)
		})
	}
}