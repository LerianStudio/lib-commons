//go:build unit

package transaction

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// decPtr returns a pointer to a decimal value parsed from a string.
func decPtr(t *testing.T, s string) *decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	require.NoError(t, err, "decPtr: invalid decimal string %q", s)
	return &d
}

// intDecPtr returns a pointer to a decimal created from an int64.
func intDecPtr(v int64) *decimal.Decimal {
	d := decimal.NewFromInt(v)
	return &d
}

// assertDomainError extracts a DomainError from err, verifies the error code,
// and returns it for additional assertions.
func assertDomainError(t *testing.T, err error, expectedCode ErrorCode) DomainError {
	t.Helper()

	require.Error(t, err)

	var domainErr DomainError
	require.True(t, errors.As(err, &domainErr), "expected DomainError, got %T: %v", err, err)
	assert.Equal(t, expectedCode, domainErr.Code)

	return domainErr
}

// simplePlan creates a valid IntentPlan with the given asset, total, and
// single source/destination postings using the provided status.
func simplePlan(asset string, total decimal.Decimal, status TransactionStatus) IntentPlan {
	op := OperationDebit
	dstOp := OperationCredit

	return IntentPlan{
		Asset: asset,
		Total: total,
		Sources: []Posting{{
			Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src-bal"},
			Asset:     asset,
			Amount:    total,
			Operation: op,
			Status:    status,
		}},
		Destinations: []Posting{{
			Target:    LedgerTarget{AccountID: "dst-acc", BalanceID: "dst-bal"},
			Asset:     asset,
			Amount:    total,
			Operation: dstOp,
			Status:    status,
		}},
	}
}

// ---------------------------------------------------------------------------
// DomainError type tests
// ---------------------------------------------------------------------------

func TestDomainError_ErrorString(t *testing.T) {
	t.Parallel()

	t.Run("with field", func(t *testing.T) {
		t.Parallel()

		de := DomainError{Code: ErrorInvalidInput, Field: "total", Message: "must be positive"}
		assert.Equal(t, "1001: must be positive (total)", de.Error())
	})

	t.Run("without field", func(t *testing.T) {
		t.Parallel()

		de := DomainError{Code: ErrorInsufficientFunds, Message: "not enough funds"}
		assert.Equal(t, "0018: not enough funds", de.Error())
	})
}

func TestNewDomainError_Implements_error(t *testing.T) {
	t.Parallel()

	err := NewDomainError(ErrorInvalidInput, "field", "message")
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidInput, de.Code)
	assert.Equal(t, "field", de.Field)
	assert.Equal(t, "message", de.Message)
}

// ---------------------------------------------------------------------------
// LedgerTarget.validate
// ---------------------------------------------------------------------------

func TestLedgerTarget_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		target    LedgerTarget
		expectErr bool
		field     string
	}{
		{name: "valid", target: LedgerTarget{AccountID: "a", BalanceID: "b"}, expectErr: false},
		{name: "empty accountId", target: LedgerTarget{AccountID: "", BalanceID: "b"}, expectErr: true, field: "t.accountId"},
		{name: "whitespace accountId", target: LedgerTarget{AccountID: "   ", BalanceID: "b"}, expectErr: true, field: "t.accountId"},
		{name: "empty balanceId", target: LedgerTarget{AccountID: "a", BalanceID: ""}, expectErr: true, field: "t.balanceId"},
		{name: "whitespace balanceId", target: LedgerTarget{AccountID: "a", BalanceID: "  "}, expectErr: true, field: "t.balanceId"},
		{name: "both empty", target: LedgerTarget{}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.target.validate("t")
			if tt.expectErr {
				require.Error(t, err)
				assertDomainError(t, err, ErrorInvalidInput)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildIntentPlan -- Input validation
// ---------------------------------------------------------------------------

func TestBuildIntentPlan(t *testing.T) {
	amount30 := decimal.NewFromInt(30)
	share50 := decimal.NewFromInt(50)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Pending: false,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "acc-1", BalanceID: "bal-1"}, Amount: &amount30},
			{Target: LedgerTarget{AccountID: "acc-2", BalanceID: "bal-2"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "acc-3", BalanceID: "bal-3"}, Share: &share50},
			{Target: LedgerTarget{AccountID: "acc-4", BalanceID: "bal-4"}, Share: &share50},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	assert.NoError(t, err)
	assert.Equal(t, decimal.NewFromInt(100), plan.Total)
	assert.Equal(t, "USD", plan.Asset)
	assert.Len(t, plan.Sources, 2)
	assert.Len(t, plan.Destinations, 2)
	assert.Equal(t, decimal.NewFromInt(30), plan.Sources[0].Amount)
	assert.Equal(t, decimal.NewFromInt(70), plan.Sources[1].Amount)
	assert.Equal(t, OperationDebit, plan.Sources[0].Operation)
	assert.Equal(t, OperationCredit, plan.Destinations[0].Operation)
}

func TestBuildIntentPlan_EmptyAsset(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	for _, asset := range []string{"", "  ", "   "} {
		input := TransactionIntentInput{
			Asset: asset,
			Total: decimal.NewFromInt(100),
			Sources: []Allocation{
				{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
			},
			Destinations: []Allocation{
				{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
			},
		}

		_, err := BuildIntentPlan(input, StatusCreated)
		de := assertDomainError(t, err, ErrorInvalidInput)
		assert.Equal(t, "asset", de.Field)
	}
}

func TestBuildIntentPlan_ZeroTotal(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(0)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.Zero,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Equal(t, "total", de.Field)
}

func TestBuildIntentPlan_NegativeTotal(t *testing.T) {
	t.Parallel()

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(-50),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Remainder: true},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Equal(t, "total", de.Field)
}

func TestBuildIntentPlan_EmptySources(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Sources: []Allocation{},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Equal(t, "sources", de.Field)
}

func TestBuildIntentPlan_EmptyDestinations(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Equal(t, "destinations", de.Field)
}

func TestBuildIntentPlan_NilSources(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Sources: nil,
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Equal(t, "sources", de.Field)
}

// ---------------------------------------------------------------------------
// Self-referencing (source == destination balance)
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_RejectsAmbiguousSourceDestination(t *testing.T) {
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Pending: false,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "acc-1", BalanceID: "shared"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "acc-2", BalanceID: "shared"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assert.Error(t, err)
	var domainErr DomainError
	assert.ErrorAs(t, err, &domainErr)
	assert.Equal(t, ErrorTransactionAmbiguous, domainErr.Code)
}

func TestBuildIntentPlan_SelfReferencing_DifferentAccounts(t *testing.T) {
	t.Parallel()

	// Even if account IDs differ, same balance ID triggers ambiguity.
	amount := decimal.NewFromInt(50)

	input := TransactionIntentInput{
		Asset: "BRL",
		Total: decimal.NewFromInt(50),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "account-A", BalanceID: "shared-balance"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "account-B", BalanceID: "shared-balance"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorTransactionAmbiguous)
	assert.Equal(t, "destinations", de.Field)
}

// ---------------------------------------------------------------------------
// Value mismatch
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_RejectsValueMismatch(t *testing.T) {
	amount90 := decimal.NewFromInt(90)
	amount100 := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Pending: false,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "acc-1", BalanceID: "bal-1"}, Amount: &amount90},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "acc-2", BalanceID: "bal-2"}, Amount: &amount100},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assert.Error(t, err)
	var domainErr DomainError
	assert.ErrorAs(t, err, &domainErr)
	assert.Equal(t, ErrorTransactionValueMismatch, domainErr.Code)
}

func TestBuildIntentPlan_SourceTotalDoesNotMatchTransaction(t *testing.T) {
	t.Parallel()

	amount60 := decimal.NewFromInt(60)
	amount100 := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b1"}, Amount: &amount60},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d1"}, Amount: &amount100},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorTransactionValueMismatch)
}

// ---------------------------------------------------------------------------
// Allocation strategy validation
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_NoStrategy(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			// No Amount, Share, or Remainder
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Message, "exactly one strategy")
}

func TestBuildIntentPlan_MultipleStrategies(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(50)
	share := decimal.NewFromInt(50)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{
				Target:    LedgerTarget{AccountID: "a", BalanceID: "b"},
				Amount:    &amount,
				Share:     &share,
				Remainder: false,
			},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Message, "exactly one strategy")
}

func TestBuildIntentPlan_AmountAndRemainder(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(50)

	input := TransactionIntentInput{
		Asset: "EUR",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{
				Target:    LedgerTarget{AccountID: "a", BalanceID: "b"},
				Amount:    &amount,
				Remainder: true,
			},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Remainder: true},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorInvalidInput)
}

func TestBuildIntentPlan_DuplicateRemainder(t *testing.T) {
	t.Parallel()

	input := TransactionIntentInput{
		Asset: "BRL",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a1", BalanceID: "b1"}, Remainder: true},
			{Target: LedgerTarget{AccountID: "a2", BalanceID: "b2"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Remainder: true},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Message, "only one remainder")
}

// ---------------------------------------------------------------------------
// Zero and negative amount allocations
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_ZeroAmountAllocation(t *testing.T) {
	t.Parallel()

	zero := decimal.Zero
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &zero},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Message, "amount must be greater than zero")
}

func TestBuildIntentPlan_NegativeAmountAllocation(t *testing.T) {
	t.Parallel()

	neg := decimal.NewFromInt(-10)
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &neg},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Field, "amount")
}

// ---------------------------------------------------------------------------
// Share validation
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_ShareZero(t *testing.T) {
	t.Parallel()

	zero := decimal.Zero
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Share: &zero},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Field, "share")
}

func TestBuildIntentPlan_ShareNegative(t *testing.T) {
	t.Parallel()

	neg := decimal.NewFromInt(-10)
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Share: &neg},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorInvalidInput)
}

func TestBuildIntentPlan_ShareOver100(t *testing.T) {
	t.Parallel()

	over := decimal.NewFromInt(101)
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Share: &over},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorInvalidInput)
}

func TestBuildIntentPlan_ShareExactly100(t *testing.T) {
	t.Parallel()

	share100 := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(500),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Share: &share100},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Share: &share100},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.True(t, plan.Sources[0].Amount.Equal(decimal.NewFromInt(500)))
	assert.True(t, plan.Destinations[0].Amount.Equal(decimal.NewFromInt(500)))
}

// ---------------------------------------------------------------------------
// Remainder edge cases
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_RemainderIsEntireAmount(t *testing.T) {
	t.Parallel()

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(250),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Remainder: true},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.True(t, plan.Sources[0].Amount.Equal(decimal.NewFromInt(250)))
	assert.True(t, plan.Destinations[0].Amount.Equal(decimal.NewFromInt(250)))
}

func TestBuildIntentPlan_RemainderBecomeZeroOrNegative(t *testing.T) {
	t.Parallel()

	// Allocating 100 via amount and having remainder when total is 100 leaves zero remainder.
	amount := decimal.NewFromInt(100)
	dstAmt := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a1", BalanceID: "b1"}, Amount: &amount},
			{Target: LedgerTarget{AccountID: "a2", BalanceID: "b2"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &dstAmt},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorTransactionValueMismatch)
}

func TestBuildIntentPlan_RemainderNegative_OverAllocated(t *testing.T) {
	t.Parallel()

	// Allocating more than total leaves a negative remainder.
	amount := decimal.NewFromInt(120)
	dstAmt := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a1", BalanceID: "b1"}, Amount: &amount},
			{Target: LedgerTarget{AccountID: "a2", BalanceID: "b2"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &dstAmt},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorTransactionValueMismatch)
}

// ---------------------------------------------------------------------------
// Decimal precision edge cases
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_HighPrecisionDecimals(t *testing.T) {
	t.Parallel()

	// 0.001 precision
	amt := decPtr(t, "0.001")

	input := TransactionIntentInput{
		Asset: "BTC",
		Total: *decPtr(t, "0.001"),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: amt},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: amt},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.True(t, plan.Total.Equal(*decPtr(t, "0.001")))
}

func TestBuildIntentPlan_VeryLargeAmount(t *testing.T) {
	t.Parallel()

	amt := decPtr(t, "999999999999.99")

	input := TransactionIntentInput{
		Asset: "USD",
		Total: *decPtr(t, "999999999999.99"),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: amt},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: amt},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.True(t, plan.Sources[0].Amount.Equal(*decPtr(t, "999999999999.99")))
}

func TestBuildIntentPlan_ManyDecimalPlaces(t *testing.T) {
	t.Parallel()

	// 18 decimal places - crypto-level precision
	amt := decPtr(t, "0.000000000000000001")

	input := TransactionIntentInput{
		Asset: "ETH",
		Total: *decPtr(t, "0.000000000000000001"),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: amt},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: amt},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.True(t, plan.Sources[0].Amount.Equal(*decPtr(t, "0.000000000000000001")))
}

func TestBuildIntentPlan_ShareProducesDecimalAmount(t *testing.T) {
	t.Parallel()

	// 33.33% of 100 = 33.33; remainder picks up the rest.
	share := *decPtr(t, "33.33")

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a1", BalanceID: "b1"}, Share: &share},
			{Target: LedgerTarget{AccountID: "a2", BalanceID: "b2"}, Share: &share},
			{Target: LedgerTarget{AccountID: "a3", BalanceID: "b3"}, Remainder: true},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Remainder: true},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)

	// Verify total coverage
	srcTotal := decimal.Zero
	for _, s := range plan.Sources {
		srcTotal = srcTotal.Add(s.Amount)
	}

	assert.True(t, srcTotal.Equal(decimal.NewFromInt(100)),
		"source total should be exactly 100, got %s", srcTotal)
}

// ---------------------------------------------------------------------------
// Single source to multiple destinations
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_SingleSourceMultipleDestinations(t *testing.T) {
	t.Parallel()

	total := decimal.NewFromInt(300)
	srcAmt := decimal.NewFromInt(300)
	dstAmt := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: total,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &srcAmt},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c1", BalanceID: "d1"}, Amount: &dstAmt},
			{Target: LedgerTarget{AccountID: "c2", BalanceID: "d2"}, Amount: &dstAmt},
			{Target: LedgerTarget{AccountID: "c3", BalanceID: "d3"}, Amount: &dstAmt},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.Len(t, plan.Sources, 1)
	assert.Len(t, plan.Destinations, 3)

	for _, dst := range plan.Destinations {
		assert.True(t, dst.Amount.Equal(decimal.NewFromInt(100)))
		assert.Equal(t, OperationCredit, dst.Operation)
	}
}

// ---------------------------------------------------------------------------
// Multiple sources to single destination
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_MultipleSourcesSingleDestination(t *testing.T) {
	t.Parallel()

	total := decimal.NewFromInt(300)
	srcAmt := decimal.NewFromInt(100)
	dstAmt := decimal.NewFromInt(300)

	input := TransactionIntentInput{
		Asset: "BRL",
		Total: total,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a1", BalanceID: "b1"}, Amount: &srcAmt},
			{Target: LedgerTarget{AccountID: "a2", BalanceID: "b2"}, Amount: &srcAmt},
			{Target: LedgerTarget{AccountID: "a3", BalanceID: "b3"}, Amount: &srcAmt},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &dstAmt},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.Len(t, plan.Sources, 3)
	assert.Len(t, plan.Destinations, 1)

	for _, src := range plan.Sources {
		assert.True(t, src.Amount.Equal(decimal.NewFromInt(100)))
		assert.Equal(t, OperationDebit, src.Operation)
	}
}

// ---------------------------------------------------------------------------
// Allocation target validation
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_SourceMissingAccountID(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Field, "accountId")
}

func TestBuildIntentPlan_DestinationMissingBalanceID(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: ""}, Amount: &amount},
		},
	}

	_, err := BuildIntentPlan(input, StatusCreated)
	de := assertDomainError(t, err, ErrorInvalidInput)
	assert.Contains(t, de.Field, "balanceId")
}

// ---------------------------------------------------------------------------
// Valid minimum and complex plans
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_MinimumValidTransaction(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(1)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(1),
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.Equal(t, "USD", plan.Asset)
	assert.True(t, plan.Total.Equal(decimal.NewFromInt(1)))
	assert.Len(t, plan.Sources, 1)
	assert.Len(t, plan.Destinations, 1)
	assert.False(t, plan.Pending)
}

func TestBuildIntentPlan_ComplexMultiPartyTransaction(t *testing.T) {
	t.Parallel()

	total := decimal.NewFromInt(1000)
	share60 := decimal.NewFromInt(60)
	share40 := decimal.NewFromInt(40)
	amount200 := decimal.NewFromInt(200)
	share30 := decimal.NewFromInt(30)

	input := TransactionIntentInput{
		Asset:   "BRL",
		Total:   total,
		Pending: false,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "src1", BalanceID: "bal-src1"}, Share: &share60},
			{Target: LedgerTarget{AccountID: "src2", BalanceID: "bal-src2"}, Share: &share40},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "dst1", BalanceID: "bal-dst1"}, Amount: &amount200},
			{Target: LedgerTarget{AccountID: "dst2", BalanceID: "bal-dst2"}, Share: &share30},
			{Target: LedgerTarget{AccountID: "dst3", BalanceID: "bal-dst3"}, Remainder: true},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)

	// Verify source amounts: 60% of 1000 = 600, 40% of 1000 = 400
	assert.True(t, plan.Sources[0].Amount.Equal(decimal.NewFromInt(600)))
	assert.True(t, plan.Sources[1].Amount.Equal(decimal.NewFromInt(400)))

	// Verify destination amounts: 200, 30% of 1000=300, remainder=500
	assert.True(t, plan.Destinations[0].Amount.Equal(decimal.NewFromInt(200)))
	assert.True(t, plan.Destinations[1].Amount.Equal(decimal.NewFromInt(300)))
	assert.True(t, plan.Destinations[2].Amount.Equal(decimal.NewFromInt(500)))

	// All operations
	for _, s := range plan.Sources {
		assert.Equal(t, OperationDebit, s.Operation)
		assert.Equal(t, StatusCreated, s.Status)
		assert.Equal(t, "BRL", s.Asset)
	}

	for _, d := range plan.Destinations {
		assert.Equal(t, OperationCredit, d.Operation)
		assert.Equal(t, StatusCreated, d.Status)
		assert.Equal(t, "BRL", d.Asset)
	}
}

// ---------------------------------------------------------------------------
// Pending transaction plan
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_PendingTransaction(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(75)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(75),
		Pending: true,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	plan, err := BuildIntentPlan(input, StatusPending)
	require.NoError(t, err)
	assert.True(t, plan.Pending)

	// Pending source = ON_HOLD, pending destination = CREDIT
	assert.Equal(t, OperationOnHold, plan.Sources[0].Operation)
	assert.Equal(t, OperationCredit, plan.Destinations[0].Operation)
}

// ---------------------------------------------------------------------------
// Route propagation
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_RoutePropagation(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Allocation{
			{
				Target: LedgerTarget{AccountID: "a", BalanceID: "b"},
				Amount: &amount,
				Route:  "wire-transfer",
			},
		},
		Destinations: []Allocation{
			{
				Target: LedgerTarget{AccountID: "c", BalanceID: "d"},
				Amount: &amount,
				Route:  "ach",
			},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.Equal(t, "wire-transfer", plan.Sources[0].Route)
	assert.Equal(t, "ach", plan.Destinations[0].Route)
}

// ---------------------------------------------------------------------------
// Invalid status for non-pending
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_InvalidStatusForNonPending(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Pending: false,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	// Non-pending only supports StatusCreated.
	_, err := BuildIntentPlan(input, StatusApproved)
	assertDomainError(t, err, ErrorInvalidStateTransition)
}

func TestBuildIntentPlan_InvalidStatusForPending(t *testing.T) {
	t.Parallel()

	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   decimal.NewFromInt(100),
		Pending: true,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "a", BalanceID: "b"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "c", BalanceID: "d"}, Amount: &amount},
		},
	}

	// Pending only supports PENDING, APPROVED, or CANCELED.
	_, err := BuildIntentPlan(input, StatusCreated)
	assertDomainError(t, err, ErrorInvalidStateTransition)
}

// ---------------------------------------------------------------------------
// Stress test: many allocations
// ---------------------------------------------------------------------------

func TestBuildIntentPlan_ManyAllocations(t *testing.T) {
	t.Parallel()

	count := 50
	share := decimal.NewFromInt(2) // 2% each = 100%
	total := decimal.NewFromInt(10000)

	sources := make([]Allocation, count)
	dests := make([]Allocation, count)

	for i := 0; i < count; i++ {
		s := share
		sources[i] = Allocation{
			Target: LedgerTarget{
				AccountID: "src-acc-" + strings.Repeat("x", 3),
				BalanceID: "src-bal-" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			},
			Share: &s,
		}

		d := share
		dests[i] = Allocation{
			Target: LedgerTarget{
				AccountID: "dst-acc-" + strings.Repeat("y", 3),
				BalanceID: "dst-bal-" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			},
			Share: &d,
		}
	}

	input := TransactionIntentInput{
		Asset:        "USD",
		Total:        total,
		Sources:      sources,
		Destinations: dests,
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)
	assert.Len(t, plan.Sources, count)
	assert.Len(t, plan.Destinations, count)

	// Verify total sums
	srcSum := decimal.Zero
	for _, s := range plan.Sources {
		srcSum = srcSum.Add(s.Amount)
	}

	assert.True(t, srcSum.Equal(total), "expected source sum %s, got %s", total, srcSum)
}

// ---------------------------------------------------------------------------
// ValidateBalanceEligibility
// ---------------------------------------------------------------------------

func TestValidateBalanceEligibility(t *testing.T) {
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   amount,
		Pending: true,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "source-account", BalanceID: "source-balance"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "destination-account", BalanceID: "destination-balance"}, Amount: &amount},
		},
	}

	plan, err := BuildIntentPlan(input, StatusPending)
	assert.NoError(t, err)

	balances := map[string]Balance{
		"source-balance": {
			ID:             "source-balance",
			AccountID:      "source-account",
			Asset:          "USD",
			Available:      decimal.NewFromInt(300),
			OnHold:         decimal.NewFromInt(0),
			AllowSending:   true,
			AllowReceiving: true,
			AccountType:    AccountTypeInternal,
		},
		"destination-balance": {
			ID:             "destination-balance",
			AccountID:      "destination-account",
			Asset:          "USD",
			Available:      decimal.NewFromInt(0),
			OnHold:         decimal.NewFromInt(0),
			AllowSending:   true,
			AllowReceiving: true,
			AccountType:    AccountTypeExternal,
		},
	}

	err = ValidateBalanceEligibility(plan, balances)
	assert.NoError(t, err)
}

func TestValidateBalanceEligibility_EmptyBalanceCatalog(t *testing.T) {
	t.Parallel()

	plan := simplePlan("USD", decimal.NewFromInt(100), StatusCreated)
	err := ValidateBalanceEligibility(plan, map[string]Balance{})
	de := assertDomainError(t, err, ErrorAccountIneligibility)
	assert.Equal(t, "balances", de.Field)
}

func TestValidateBalanceEligibility_NilBalanceCatalog(t *testing.T) {
	t.Parallel()

	plan := simplePlan("USD", decimal.NewFromInt(100), StatusCreated)
	err := ValidateBalanceEligibility(plan, nil)
	de := assertDomainError(t, err, ErrorAccountIneligibility)
	assert.Equal(t, "balances", de.Field)
}

func TestValidateBalanceEligibility_Errors(t *testing.T) {
	amount := decimal.NewFromInt(100)

	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   amount,
		Pending: true,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "source-account", BalanceID: "source-balance"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "destination-account", BalanceID: "destination-balance"}, Amount: &amount},
		},
	}

	plan, err := BuildIntentPlan(input, StatusPending)
	assert.NoError(t, err)

	tests := []struct {
		name      string
		balances  map[string]Balance
		errorCode ErrorCode
		field     string
	}{
		{
			name: "missing source balance",
			balances: map[string]Balance{
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					AllowReceiving: true,
				},
			},
			errorCode: ErrorAccountIneligibility,
			field:     "sources",
		},
		{
			name: "source asset mismatch",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "EUR",
					Available:      decimal.NewFromInt(300),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
			},
			errorCode: ErrorAssetCodeNotFound,
			field:     "sources",
		},
		{
			name: "source cannot send",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(300),
					AllowSending:   false,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
			},
			errorCode: ErrorAccountStatusTransactionRestriction,
			field:     "sources",
		},
		{
			name: "pending source cannot be external",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(300),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeExternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
			},
			errorCode: ErrorOnHoldExternalAccount,
			field:     "sources",
		},
		{
			name: "missing destination balance",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(300),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
			},
			errorCode: ErrorAccountIneligibility,
			field:     "destinations",
		},
		{
			name: "destination asset mismatch",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(300),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "GBP",
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
			},
			errorCode: ErrorAssetCodeNotFound,
			field:     "destinations",
		},
		{
			name: "destination cannot receive",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(300),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					AllowSending:   true,
					AllowReceiving: false,
					AccountType:    AccountTypeInternal,
				},
			},
			errorCode: ErrorAccountStatusTransactionRestriction,
			field:     "destinations",
		},
		{
			name: "external destination with positive available",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(300),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(50),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeExternal,
				},
			},
			errorCode: ErrorInsufficientFunds,
			field:     "destinations",
		},
		{
			name: "source insufficient funds",
			balances: map[string]Balance{
				"source-balance": {
					ID:             "source-balance",
					AccountID:      "source-account",
					Asset:          "USD",
					Available:      decimal.NewFromInt(50),
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeInternal,
				},
				"destination-balance": {
					ID:             "destination-balance",
					AccountID:      "destination-account",
					Asset:          "USD",
					Available:      decimal.Zero,
					AllowSending:   true,
					AllowReceiving: true,
					AccountType:    AccountTypeExternal,
				},
			},
			errorCode: ErrorInsufficientFunds,
			field:     "sources",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBalanceEligibility(plan, tt.balances)
			de := assertDomainError(t, err, tt.errorCode)
			assert.Equal(t, tt.field, de.Field)
		})
	}
}

func TestValidateBalanceEligibility_NonPending_ExternalSourceAllowed(t *testing.T) {
	t.Parallel()

	// When not pending, external sources ARE allowed (only pending + external is prohibited).
	plan := IntentPlan{
		Asset: "USD",
		Total: decimal.NewFromInt(100),
		Sources: []Posting{{
			Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src-bal"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(100),
			Operation: OperationDebit,
			Status:    StatusCreated,
		}},
		Destinations: []Posting{{
			Target:    LedgerTarget{AccountID: "dst-acc", BalanceID: "dst-bal"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(100),
			Operation: OperationCredit,
			Status:    StatusCreated,
		}},
		Pending: false,
	}

	balances := map[string]Balance{
		"src-bal": {
			ID:           "src-bal",
			AccountID:    "src-acc",
			Asset:        "USD",
			Available:    decimal.NewFromInt(500),
			AllowSending: true,
			AccountType:  AccountTypeExternal,
		},
		"dst-bal": {
			ID:             "dst-bal",
			AccountID:      "dst-acc",
			Asset:          "USD",
			Available:      decimal.Zero,
			AllowReceiving: true,
			AccountType:    AccountTypeInternal,
		},
	}

	err := ValidateBalanceEligibility(plan, balances)
	require.NoError(t, err)
}

func TestValidateBalanceEligibility_ExternalDestinationWithZeroAvailable(t *testing.T) {
	t.Parallel()

	// External destination with zero available should pass.
	plan := IntentPlan{
		Asset: "USD",
		Total: decimal.NewFromInt(50),
		Sources: []Posting{{
			Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src-bal"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(50),
			Operation: OperationDebit,
			Status:    StatusCreated,
		}},
		Destinations: []Posting{{
			Target:    LedgerTarget{AccountID: "dst-acc", BalanceID: "dst-bal"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(50),
			Operation: OperationCredit,
			Status:    StatusCreated,
		}},
	}

	balances := map[string]Balance{
		"src-bal": {
			ID:           "src-bal",
			AccountID:    "src-acc",
			Asset:        "USD",
			Available:    decimal.NewFromInt(200),
			AllowSending: true,
			AccountType:  AccountTypeInternal,
		},
		"dst-bal": {
			ID:             "dst-bal",
			AccountID:      "dst-acc",
			Asset:          "USD",
			Available:      decimal.Zero,
			AllowReceiving: true,
			AccountType:    AccountTypeExternal,
		},
	}

	err := ValidateBalanceEligibility(plan, balances)
	require.NoError(t, err)
}

func TestValidateBalanceEligibility_ExternalDestinationNegativeAvailable(t *testing.T) {
	t.Parallel()

	// External destination with negative available should now fail (!IsZero returns true for negative).
	plan := IntentPlan{
		Asset: "USD",
		Total: decimal.NewFromInt(50),
		Sources: []Posting{{
			Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src-bal"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(50),
			Operation: OperationDebit,
			Status:    StatusCreated,
		}},
		Destinations: []Posting{{
			Target:    LedgerTarget{AccountID: "dst-acc", BalanceID: "dst-bal"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(50),
			Operation: OperationCredit,
			Status:    StatusCreated,
		}},
	}

	balances := map[string]Balance{
		"src-bal": {
			ID:           "src-bal",
			AccountID:    "src-acc",
			Asset:        "USD",
			Available:    decimal.NewFromInt(200),
			AllowSending: true,
			AccountType:  AccountTypeInternal,
		},
		"dst-bal": {
			ID:             "dst-bal",
			AccountID:      "dst-acc",
			Asset:          "USD",
			Available:      decimal.NewFromInt(-10),
			AllowReceiving: true,
			AccountType:    AccountTypeExternal,
		},
	}

	err := ValidateBalanceEligibility(plan, balances)
	de := assertDomainError(t, err, ErrorDataCorruption)
	assert.Equal(t, "balance", de.Field)
}

// ---------------------------------------------------------------------------
// Serialization round-trip (IntentPlan)
// ---------------------------------------------------------------------------

func TestIntentPlan_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := IntentPlan{
		Asset:   "BRL",
		Total:   *decPtr(t, "1234.56"),
		Pending: true,
		Sources: []Posting{{
			Target:    LedgerTarget{AccountID: "a", BalanceID: "b"},
			Asset:     "BRL",
			Amount:    *decPtr(t, "1234.56"),
			Operation: OperationOnHold,
			Status:    StatusPending,
			Route:     "pix",
		}},
		Destinations: []Posting{{
			Target:    LedgerTarget{AccountID: "c", BalanceID: "d"},
			Asset:     "BRL",
			Amount:    *decPtr(t, "1234.56"),
			Operation: OperationCredit,
			Status:    StatusPending,
		}},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored IntentPlan
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, original.Asset, restored.Asset)
	assert.True(t, original.Total.Equal(restored.Total))
	assert.Equal(t, original.Pending, restored.Pending)
	assert.Len(t, restored.Sources, 1)
	assert.Len(t, restored.Destinations, 1)
	assert.True(t, original.Sources[0].Amount.Equal(restored.Sources[0].Amount))
	assert.Equal(t, original.Sources[0].Operation, restored.Sources[0].Operation)
	assert.Equal(t, original.Sources[0].Route, restored.Sources[0].Route)
}

func TestBalance_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := Balance{
		ID:             "bal-123",
		OrganizationID: "org-1",
		LedgerID:       "led-1",
		AccountID:      "acc-1",
		Asset:          "BTC",
		Available:      *decPtr(t, "0.00123456"),
		OnHold:         *decPtr(t, "0.00000001"),
		Version:        42,
		AccountType:    AccountTypeInternal,
		AllowSending:   true,
		AllowReceiving: true,
		Metadata:       map[string]any{"key": "value"},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored Balance
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, original.ID, restored.ID)
	assert.Equal(t, original.OrganizationID, restored.OrganizationID)
	assert.True(t, original.Available.Equal(restored.Available))
	assert.True(t, original.OnHold.Equal(restored.OnHold))
	assert.Equal(t, original.Version, restored.Version)
	assert.Equal(t, original.AccountType, restored.AccountType)
	assert.Equal(t, "value", restored.Metadata["key"])
}
