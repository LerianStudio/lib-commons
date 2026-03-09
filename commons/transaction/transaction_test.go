//go:build unit

package transaction

import (
	"errors"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ResolveOperation -- exhaustive state matrix
// ---------------------------------------------------------------------------

func TestResolveOperation(t *testing.T) {
	tests := []struct {
		name      string
		pending   bool
		isSource  bool
		status    TransactionStatus
		expected  Operation
		errorCode ErrorCode
	}{
		// Pending transactions
		{name: "pending source PENDING", pending: true, isSource: true, status: StatusPending, expected: OperationOnHold},
		{name: "pending destination PENDING", pending: true, isSource: false, status: StatusPending, expected: OperationCredit},
		{name: "pending source CANCELED", pending: true, isSource: true, status: StatusCanceled, expected: OperationRelease},
		{name: "pending destination CANCELED", pending: true, isSource: false, status: StatusCanceled, expected: OperationDebit},
		{name: "pending source APPROVED", pending: true, isSource: true, status: StatusApproved, expected: OperationDebit},
		{name: "pending destination APPROVED", pending: true, isSource: false, status: StatusApproved, expected: OperationCredit},

		// Non-pending transactions
		{name: "non-pending source CREATED", pending: false, isSource: true, status: StatusCreated, expected: OperationDebit},
		{name: "non-pending destination CREATED", pending: false, isSource: false, status: StatusCreated, expected: OperationCredit},

		// Invalid statuses
		{name: "non-pending source APPROVED", pending: false, isSource: true, status: StatusApproved, errorCode: ErrorInvalidStateTransition},
		{name: "non-pending destination APPROVED", pending: false, isSource: false, status: StatusApproved, errorCode: ErrorInvalidStateTransition},
		{name: "non-pending source PENDING", pending: false, isSource: true, status: StatusPending, errorCode: ErrorInvalidStateTransition},
		{name: "non-pending destination CANCELED", pending: false, isSource: false, status: StatusCanceled, errorCode: ErrorInvalidStateTransition},
		{name: "pending source CREATED", pending: true, isSource: true, status: StatusCreated, errorCode: ErrorInvalidStateTransition},
		{name: "pending destination CREATED", pending: true, isSource: false, status: StatusCreated, errorCode: ErrorInvalidStateTransition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveOperation(tt.pending, tt.isSource, tt.status)

			if tt.errorCode != "" {
				require.Error(t, err)

				var domainErr DomainError
				require.True(t, errors.As(err, &domainErr))
				assert.Equal(t, tt.errorCode, domainErr.Code)
				assert.Equal(t, "status", domainErr.Field)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// ---------------------------------------------------------------------------
// ApplyPosting -- happy path operations
// ---------------------------------------------------------------------------

func TestApplyPosting(t *testing.T) {
	balance := Balance{
		ID:             "balance-1",
		AccountID:      "account-1",
		Asset:          "USD",
		Available:      decimal.NewFromInt(100),
		OnHold:         decimal.NewFromInt(20),
		Version:        7,
		AllowSending:   true,
		AllowReceiving: true,
	}

	tests := []struct {
		name      string
		posting   Posting
		expected  Balance
		errorCode ErrorCode
	}{
		{
			name: "ON_HOLD moves available to onHold",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(30),
				Operation: OperationOnHold,
				Status:    StatusPending,
			},
			expected: Balance{Available: decimal.NewFromInt(70), OnHold: decimal.NewFromInt(50), Version: 8},
		},
		{
			name: "RELEASE moves onHold to available",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(10),
				Operation: OperationRelease,
				Status:    StatusCanceled,
			},
			expected: Balance{Available: decimal.NewFromInt(110), OnHold: decimal.NewFromInt(10), Version: 8},
		},
		{
			name: "DEBIT APPROVED deducts from onHold",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(10),
				Operation: OperationDebit,
				Status:    StatusApproved,
			},
			expected: Balance{Available: decimal.NewFromInt(100), OnHold: decimal.NewFromInt(10), Version: 8},
		},
		{
			name: "DEBIT CREATED deducts from available",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(50),
				Operation: OperationDebit,
				Status:    StatusCreated,
			},
			expected: Balance{Available: decimal.NewFromInt(50), OnHold: decimal.NewFromInt(20), Version: 8},
		},
		{
			name: "CREDIT CREATED adds to available",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(40),
				Operation: OperationCredit,
				Status:    StatusCreated,
			},
			expected: Balance{Available: decimal.NewFromInt(140), OnHold: decimal.NewFromInt(20), Version: 8},
		},
		{
			name: "CREDIT APPROVED adds to available",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(25),
				Operation: OperationCredit,
				Status:    StatusApproved,
			},
			expected: Balance{Available: decimal.NewFromInt(125), OnHold: decimal.NewFromInt(20), Version: 8},
		},
		{
			name: "CREDIT PENDING adds to available",
			posting: Posting{
				Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(40),
				Operation: OperationCredit,
				Status:    StatusPending,
			},
			expected: Balance{Available: decimal.NewFromInt(140), OnHold: decimal.NewFromInt(20), Version: 8},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ApplyPosting(balance, tt.posting)

			if tt.errorCode != "" {
				require.Error(t, err)

				var domainErr DomainError
				require.True(t, errors.As(err, &domainErr))
				assert.Equal(t, tt.errorCode, domainErr.Code)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, balance.ID, got.ID)
			assert.Equal(t, balance.AccountID, got.AccountID)
			assert.Equal(t, balance.Asset, got.Asset)
			assert.True(t, tt.expected.Available.Equal(got.Available),
				"available: want=%s got=%s", tt.expected.Available, got.Available)
			assert.True(t, tt.expected.OnHold.Equal(got.OnHold),
				"onHold: want=%s got=%s", tt.expected.OnHold, got.OnHold)
			assert.Equal(t, tt.expected.Version, got.Version)
		})
	}
}

// ---------------------------------------------------------------------------
// ApplyPosting -- validation errors
// ---------------------------------------------------------------------------

func TestApplyPosting_MissingTargetAccountID(t *testing.T) {
	t.Parallel()

	balance := Balance{ID: "b1", AccountID: "a1", Asset: "USD", Available: decimal.NewFromInt(100)}
	posting := Posting{
		Target:    LedgerTarget{AccountID: "", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidInput, de.Code)
	assert.Contains(t, de.Field, "accountId")
}

func TestApplyPosting_MissingTargetBalanceID(t *testing.T) {
	t.Parallel()

	balance := Balance{ID: "b1", AccountID: "a1", Asset: "USD", Available: decimal.NewFromInt(100)}
	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: ""},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidInput, de.Code)
	assert.Contains(t, de.Field, "balanceId")
}

func TestApplyPosting_RejectsMismatchedBalanceID(t *testing.T) {
	t.Parallel()

	balance := Balance{ID: "balance-A", AccountID: "account-1", Asset: "USD", Available: decimal.NewFromInt(100)}
	posting := Posting{
		Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-B"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorAccountIneligibility, de.Code)
	assert.Contains(t, de.Field, "balanceId")
}

func TestApplyPosting_RejectsMismatchedAccountID(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
		OnHold:    decimal.Zero,
	}

	posting := Posting{
		Target: LedgerTarget{
			AccountID: "account-2",
			BalanceID: "balance-1",
		},
		Asset:     "USD",
		Operation: OperationDebit,
		Status:    StatusCreated,
		Amount:    decimal.NewFromInt(10),
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var domainErr DomainError
	require.True(t, errors.As(err, &domainErr))
	assert.Equal(t, ErrorAccountIneligibility, domainErr.Code)
	assert.Contains(t, domainErr.Field, "accountId")
}

func TestApplyPosting_RejectsAssetMismatch(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
		Asset:     "EUR",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorAssetCodeNotFound, de.Code)
	assert.Equal(t, "posting.asset", de.Field)
}

func TestApplyPosting_RejectsZeroAmount(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
		Asset:     "USD",
		Amount:    decimal.Zero,
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidInput, de.Code)
	assert.Contains(t, de.Message, "posting amount must be greater than zero")
}

func TestApplyPosting_RejectsNegativeAmount(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(-5),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidInput, de.Code)
}

func TestApplyPosting_RejectsUnsupportedOperation(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: Operation("UNKNOWN_OP"),
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidInput, de.Code)
	assert.Equal(t, "posting.operation", de.Field)
}

// ---------------------------------------------------------------------------
// ApplyPosting -- invalid state transitions
// ---------------------------------------------------------------------------

func TestApplyPosting_OnHold_RequiresPendingStatus(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
	}

	invalidStatuses := []TransactionStatus{StatusCreated, StatusApproved, StatusCanceled}
	for _, status := range invalidStatuses {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			posting := Posting{
				Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(10),
				Operation: OperationOnHold,
				Status:    status,
			}

			_, err := ApplyPosting(balance, posting)
			require.Error(t, err)

			var de DomainError
			require.True(t, errors.As(err, &de))
			assert.Equal(t, ErrorInvalidStateTransition, de.Code)
			assert.Contains(t, de.Message, "ON_HOLD requires PENDING status")
		})
	}
}

func TestApplyPosting_Release_RequiresCanceledStatus(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(50),
		OnHold:    decimal.NewFromInt(50),
	}

	invalidStatuses := []TransactionStatus{StatusCreated, StatusApproved, StatusPending}
	for _, status := range invalidStatuses {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			posting := Posting{
				Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(10),
				Operation: OperationRelease,
				Status:    status,
			}

			_, err := ApplyPosting(balance, posting)
			require.Error(t, err)

			var de DomainError
			require.True(t, errors.As(err, &de))
			assert.Equal(t, ErrorInvalidStateTransition, de.Code)
			assert.Contains(t, de.Message, "RELEASE requires CANCELED status")
		})
	}
}

func TestApplyPosting_Debit_InvalidStatus(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
		OnHold:    decimal.NewFromInt(100),
	}

	// Only PENDING is invalid for DEBIT now; CANCELED is valid for pending destination cancellations.
	invalidStatuses := []TransactionStatus{StatusPending}
	for _, status := range invalidStatuses {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			posting := Posting{
				Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
				Asset:     "USD",
				Amount:    decimal.NewFromInt(10),
				Operation: OperationDebit,
				Status:    status,
			}

			_, err := ApplyPosting(balance, posting)
			require.Error(t, err)

			var de DomainError
			require.True(t, errors.As(err, &de))
			assert.Equal(t, ErrorInvalidStateTransition, de.Code)
			assert.Contains(t, de.Message, "DEBIT only supports CREATED, APPROVED, or CANCELED status")
		})
	}
}

func TestApplyPosting_Debit_Canceled_PendingDestinationCancellation(t *testing.T) {
	t.Parallel()

	// When a pending transaction is canceled, the destination that received a CREDIT
	// during PENDING must have that credit reversed via DEBIT+CANCELED.
	balance := Balance{
		ID:        "dst-bal",
		AccountID: "dst-acc",
		Asset:     "USD",
		Available: decimal.NewFromInt(50),
		OnHold:    decimal.Zero,
		Version:   1,
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "dst-acc", BalanceID: "dst-bal"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(30),
		Operation: OperationDebit,
		Status:    StatusCanceled,
	}

	result, err := ApplyPosting(balance, posting)
	require.NoError(t, err)
	assert.True(t, result.Available.Equal(decimal.NewFromInt(20)),
		"expected 20 after cancellation debit, got %s", result.Available)
	assert.Equal(t, int64(2), result.Version)
}

func TestApplyPosting_Credit_InvalidStatus(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationCredit,
		Status:    StatusCanceled,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	var de DomainError
	require.True(t, errors.As(err, &de))
	assert.Equal(t, ErrorInvalidStateTransition, de.Code)
	assert.Contains(t, de.Message, "CREDIT only supports CREATED, APPROVED, or PENDING status")
}

// ---------------------------------------------------------------------------
// ApplyPosting -- insufficient funds (negative result guards)
// ---------------------------------------------------------------------------

func TestApplyPosting_RejectsNegativeResultingBalances(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(50),
		OnHold:    decimal.NewFromInt(5),
	}

	t.Run("debit over available", func(t *testing.T) {
		t.Parallel()

		posting := Posting{
			Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(100),
			Status:    StatusCreated,
			Operation: OperationDebit,
		}

		_, err := ApplyPosting(balance, posting)
		require.Error(t, err)

		var domainErr DomainError
		require.True(t, errors.As(err, &domainErr))
		assert.Equal(t, ErrorInsufficientFunds, domainErr.Code)
		assert.Contains(t, domainErr.Message, "negative available balance")
	})

	t.Run("release over on hold", func(t *testing.T) {
		t.Parallel()

		posting := Posting{
			Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(10),
			Status:    StatusCanceled,
			Operation: OperationRelease,
		}

		_, err := ApplyPosting(balance, posting)
		require.Error(t, err)

		var domainErr DomainError
		require.True(t, errors.As(err, &domainErr))
		assert.Equal(t, ErrorInsufficientFunds, domainErr.Code)
		assert.Contains(t, domainErr.Message, "negative on-hold balance")
	})

	t.Run("on hold over available", func(t *testing.T) {
		t.Parallel()

		posting := Posting{
			Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(51),
			Status:    StatusPending,
			Operation: OperationOnHold,
		}

		_, err := ApplyPosting(balance, posting)
		require.Error(t, err)

		var domainErr DomainError
		require.True(t, errors.As(err, &domainErr))
		assert.Equal(t, ErrorInsufficientFunds, domainErr.Code)
		assert.Contains(t, domainErr.Message, "negative available balance")
	})

	t.Run("debit approved over onHold", func(t *testing.T) {
		t.Parallel()

		posting := Posting{
			Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
			Asset:     "USD",
			Amount:    decimal.NewFromInt(6),
			Status:    StatusApproved,
			Operation: OperationDebit,
		}

		_, err := ApplyPosting(balance, posting)
		require.Error(t, err)

		var domainErr DomainError
		require.True(t, errors.As(err, &domainErr))
		assert.Equal(t, ErrorInsufficientFunds, domainErr.Code)
		assert.Contains(t, domainErr.Message, "negative on-hold balance")
	})
}

func TestApplyPosting_AllowsPendingCredit(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "balance-1",
		AccountID: "account-1",
		Asset:     "USD",
		Available: decimal.NewFromInt(0),
		OnHold:    decimal.NewFromInt(0),
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "account-1", BalanceID: "balance-1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(25),
		Status:    StatusPending,
		Operation: OperationCredit,
	}

	updated, err := ApplyPosting(balance, posting)
	require.NoError(t, err)
	assert.True(t, updated.Available.Equal(decimal.NewFromInt(25)))
	assert.Equal(t, int64(1), updated.Version)
}

// ---------------------------------------------------------------------------
// ApplyPosting -- idempotency / immutability
// ---------------------------------------------------------------------------

func TestApplyPosting_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
		OnHold:    decimal.NewFromInt(50),
		Version:   3,
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(30),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	// Save copies of the original values.
	origAvailable := balance.Available
	origOnHold := balance.OnHold
	origVersion := balance.Version

	result, err := ApplyPosting(balance, posting)
	require.NoError(t, err)

	// Original balance must not be mutated.
	assert.True(t, balance.Available.Equal(origAvailable),
		"input balance available mutated from %s to %s", origAvailable, balance.Available)
	assert.True(t, balance.OnHold.Equal(origOnHold),
		"input balance onHold mutated from %s to %s", origOnHold, balance.OnHold)
	assert.Equal(t, origVersion, balance.Version,
		"input balance version mutated from %d to %d", origVersion, balance.Version)

	// Result should reflect the operation.
	assert.True(t, result.Available.Equal(decimal.NewFromInt(70)))
	assert.Equal(t, int64(4), result.Version)
}

func TestApplyPosting_DoesNotMutateInputOnError(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
		OnHold:    decimal.NewFromInt(50),
		Version:   3,
	}

	// Save copies of the original values.
	origAvailable := balance.Available
	origOnHold := balance.OnHold
	origVersion := balance.Version

	// Asset mismatch should cause an error.
	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "EUR",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	_, err := ApplyPosting(balance, posting)
	require.Error(t, err)

	// Original balance must not be mutated even on error.
	assert.True(t, balance.Available.Equal(origAvailable),
		"input balance available mutated from %s to %s", origAvailable, balance.Available)
	assert.True(t, balance.OnHold.Equal(origOnHold),
		"input balance onHold mutated from %s to %s", origOnHold, balance.OnHold)
	assert.Equal(t, origVersion, balance.Version,
		"input balance version mutated from %d to %d", origVersion, balance.Version)
}

func TestApplyPosting_SequentialPostings_VersionIncrements(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(1000),
		OnHold:    decimal.Zero,
		Version:   0,
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	// Apply 10 sequential postings.
	current := balance
	for i := 0; i < 10; i++ {
		var err error
		current, err = ApplyPosting(current, posting)
		require.NoError(t, err)
		assert.Equal(t, int64(i+1), current.Version)
	}

	// After 10 debits of 10 from 1000, available should be 900.
	assert.True(t, current.Available.Equal(decimal.NewFromInt(900)))
}

// ---------------------------------------------------------------------------
// ApplyPosting -- decimal precision in operations
// ---------------------------------------------------------------------------

func TestApplyPosting_DecimalPrecision(t *testing.T) {
	t.Parallel()

	d, _ := decimal.NewFromString("100.005")
	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "BTC",
		Available: d,
		OnHold:    decimal.Zero,
		Version:   0,
	}

	amt, _ := decimal.NewFromString("0.001")

	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "BTC",
		Amount:    amt,
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	result, err := ApplyPosting(balance, posting)
	require.NoError(t, err)

	expected, _ := decimal.NewFromString("100.004")
	assert.True(t, result.Available.Equal(expected),
		"expected %s, got %s", expected, result.Available)
}

func TestApplyPosting_VerySmallAmount(t *testing.T) {
	t.Parallel()

	avail, _ := decimal.NewFromString("0.000000000000000002")
	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "ETH",
		Available: avail,
		OnHold:    decimal.Zero,
	}

	amt, _ := decimal.NewFromString("0.000000000000000001")
	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "ETH",
		Amount:    amt,
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	result, err := ApplyPosting(balance, posting)
	require.NoError(t, err)

	expected, _ := decimal.NewFromString("0.000000000000000001")
	assert.True(t, result.Available.Equal(expected))
}

func TestApplyPosting_LargeAmount(t *testing.T) {
	t.Parallel()

	avail, _ := decimal.NewFromString("999999999999999.99")
	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: avail,
		OnHold:    decimal.Zero,
	}

	amt, _ := decimal.NewFromString("0.01")
	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    amt,
		Operation: OperationCredit,
		Status:    StatusCreated,
	}

	result, err := ApplyPosting(balance, posting)
	require.NoError(t, err)

	expected, _ := decimal.NewFromString("1000000000000000.00")
	assert.True(t, result.Available.Equal(expected),
		"expected %s, got %s", expected, result.Available)
}

// ---------------------------------------------------------------------------
// ApplyPosting -- full lifecycle: Created -> OnHold -> Release (cancel)
// ---------------------------------------------------------------------------

func TestApplyPosting_FullPendingLifecycle_Approved(t *testing.T) {
	t.Parallel()

	// Start with source balance: 100 available, 0 on hold.
	source := Balance{
		ID:        "src",
		AccountID: "src-acc",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
		OnHold:    decimal.Zero,
		Version:   0,
	}

	// Step 1: ON_HOLD (PENDING) - source holds 30.
	afterHold, err := ApplyPosting(source, Posting{
		Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(30),
		Operation: OperationOnHold,
		Status:    StatusPending,
	})
	require.NoError(t, err)
	assert.True(t, afterHold.Available.Equal(decimal.NewFromInt(70)))
	assert.True(t, afterHold.OnHold.Equal(decimal.NewFromInt(30)))
	assert.Equal(t, int64(1), afterHold.Version)

	// Step 2: DEBIT (APPROVED) - settlement moves from on-hold.
	afterDebit, err := ApplyPosting(afterHold, Posting{
		Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(30),
		Operation: OperationDebit,
		Status:    StatusApproved,
	})
	require.NoError(t, err)
	assert.True(t, afterDebit.Available.Equal(decimal.NewFromInt(70)))
	assert.True(t, afterDebit.OnHold.Equal(decimal.Zero))
	assert.Equal(t, int64(2), afterDebit.Version)
}

func TestApplyPosting_FullPendingLifecycle_Canceled(t *testing.T) {
	t.Parallel()

	source := Balance{
		ID:        "src",
		AccountID: "src-acc",
		Asset:     "USD",
		Available: decimal.NewFromInt(100),
		OnHold:    decimal.Zero,
		Version:   0,
	}

	// Step 1: ON_HOLD (PENDING).
	afterHold, err := ApplyPosting(source, Posting{
		Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(30),
		Operation: OperationOnHold,
		Status:    StatusPending,
	})
	require.NoError(t, err)
	assert.True(t, afterHold.Available.Equal(decimal.NewFromInt(70)))
	assert.True(t, afterHold.OnHold.Equal(decimal.NewFromInt(30)))

	// Step 2: RELEASE (CANCELED) - funds return to available.
	afterRelease, err := ApplyPosting(afterHold, Posting{
		Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(30),
		Operation: OperationRelease,
		Status:    StatusCanceled,
	})
	require.NoError(t, err)
	assert.True(t, afterRelease.Available.Equal(decimal.NewFromInt(100)),
		"expected 100, got %s", afterRelease.Available)
	assert.True(t, afterRelease.OnHold.Equal(decimal.Zero))
	assert.Equal(t, int64(2), afterRelease.Version)
}

// ---------------------------------------------------------------------------
// ApplyPosting -- debit exactly to zero
// ---------------------------------------------------------------------------

func TestApplyPosting_DebitToExactlyZero(t *testing.T) {
	t.Parallel()

	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(42),
		OnHold:    decimal.Zero,
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(42),
		Operation: OperationDebit,
		Status:    StatusCreated,
	}

	result, err := ApplyPosting(balance, posting)
	require.NoError(t, err)
	assert.True(t, result.Available.Equal(decimal.Zero),
		"expected zero, got %s", result.Available)
}

// ---------------------------------------------------------------------------
// Concurrent posting safety
// ---------------------------------------------------------------------------

func TestApplyPosting_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	// ApplyPosting is a pure function (takes value, returns value),
	// so concurrent calls should never interfere with each other.
	balance := Balance{
		ID:        "b1",
		AccountID: "a1",
		Asset:     "USD",
		Available: decimal.NewFromInt(1000),
		OnHold:    decimal.Zero,
		Version:   0,
	}

	posting := Posting{
		Target:    LedgerTarget{AccountID: "a1", BalanceID: "b1"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(10),
		Operation: OperationCredit,
		Status:    StatusCreated,
	}

	const goroutines = 100

	var wg sync.WaitGroup

	wg.Add(goroutines)

	results := make([]Balance, goroutines)
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = ApplyPosting(balance, posting)
		}(i)
	}

	wg.Wait()

	// Every goroutine should succeed and produce the same deterministic result.
	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d failed", i)
		assert.True(t, results[i].Available.Equal(decimal.NewFromInt(1010)),
			"goroutine %d: expected 1010, got %s", i, results[i].Available)
		assert.Equal(t, int64(1), results[i].Version)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: Build plan, validate eligibility, apply postings
// ---------------------------------------------------------------------------

func TestEndToEnd_FullTransactionFlow(t *testing.T) {
	t.Parallel()

	total := decimal.NewFromInt(200)
	amount := decimal.NewFromInt(200)

	// 1. Build intent plan.
	input := TransactionIntentInput{
		Asset: "BRL",
		Total: total,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "alice-acc", BalanceID: "alice-bal"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "bob-acc", BalanceID: "bob-bal"}, Amount: &amount},
		},
	}

	plan, err := BuildIntentPlan(input, StatusCreated)
	require.NoError(t, err)

	// 2. Validate eligibility.
	balances := map[string]Balance{
		"alice-bal": {
			ID:           "alice-bal",
			AccountID:    "alice-acc",
			Asset:        "BRL",
			Available:    decimal.NewFromInt(500),
			OnHold:       decimal.Zero,
			AllowSending: true,
			AccountType:  AccountTypeInternal,
		},
		"bob-bal": {
			ID:             "bob-bal",
			AccountID:      "bob-acc",
			Asset:          "BRL",
			Available:      decimal.NewFromInt(100),
			AllowReceiving: true,
			AccountType:    AccountTypeInternal,
		},
	}

	err = ValidateBalanceEligibility(plan, balances)
	require.NoError(t, err)

	// 3. Apply source debit.
	aliceBalance := balances["alice-bal"]
	aliceAfter, err := ApplyPosting(aliceBalance, plan.Sources[0])
	require.NoError(t, err)
	assert.True(t, aliceAfter.Available.Equal(decimal.NewFromInt(300)),
		"expected 300, got %s", aliceAfter.Available)

	// 4. Apply destination credit.
	bobBalance := balances["bob-bal"]
	bobAfter, err := ApplyPosting(bobBalance, plan.Destinations[0])
	require.NoError(t, err)
	assert.True(t, bobAfter.Available.Equal(decimal.NewFromInt(300)),
		"expected 300, got %s", bobAfter.Available)
}

func TestEndToEnd_PendingTransactionFlow(t *testing.T) {
	t.Parallel()

	total := decimal.NewFromInt(50)
	amount := decimal.NewFromInt(50)

	// 1. Build pending plan.
	input := TransactionIntentInput{
		Asset:   "USD",
		Total:   total,
		Pending: true,
		Sources: []Allocation{
			{Target: LedgerTarget{AccountID: "src-acc", BalanceID: "src-bal"}, Amount: &amount},
		},
		Destinations: []Allocation{
			{Target: LedgerTarget{AccountID: "dst-acc", BalanceID: "dst-bal"}, Amount: &amount},
		},
	}

	plan, err := BuildIntentPlan(input, StatusPending)
	require.NoError(t, err)
	assert.Equal(t, OperationOnHold, plan.Sources[0].Operation)
	assert.Equal(t, OperationCredit, plan.Destinations[0].Operation)

	// 2. Validate eligibility.
	srcBal := Balance{
		ID:           "src-bal",
		AccountID:    "src-acc",
		Asset:        "USD",
		Available:    decimal.NewFromInt(200),
		OnHold:       decimal.Zero,
		AllowSending: true,
		AccountType:  AccountTypeInternal,
	}

	dstBal := Balance{
		ID:             "dst-bal",
		AccountID:      "dst-acc",
		Asset:          "USD",
		Available:      decimal.Zero,
		AllowReceiving: true,
		AccountType:    AccountTypeExternal,
	}

	balances := map[string]Balance{
		"src-bal": srcBal,
		"dst-bal": dstBal,
	}

	err = ValidateBalanceEligibility(plan, balances)
	require.NoError(t, err)

	// 3. Apply source ON_HOLD.
	srcAfterHold, err := ApplyPosting(srcBal, plan.Sources[0])
	require.NoError(t, err)
	assert.True(t, srcAfterHold.Available.Equal(decimal.NewFromInt(150)))
	assert.True(t, srcAfterHold.OnHold.Equal(decimal.NewFromInt(50)))

	// 4. Apply destination CREDIT.
	dstAfterCredit, err := ApplyPosting(dstBal, plan.Destinations[0])
	require.NoError(t, err)
	assert.True(t, dstAfterCredit.Available.Equal(decimal.NewFromInt(50)))

	// 5. Now approve: source gets DEBIT from onHold.
	approvePosting := Posting{
		Target:    LedgerTarget{AccountID: "src-acc", BalanceID: "src-bal"},
		Asset:     "USD",
		Amount:    decimal.NewFromInt(50),
		Operation: OperationDebit,
		Status:    StatusApproved,
	}

	srcAfterApproval, err := ApplyPosting(srcAfterHold, approvePosting)
	require.NoError(t, err)
	assert.True(t, srcAfterApproval.Available.Equal(decimal.NewFromInt(150)))
	assert.True(t, srcAfterApproval.OnHold.Equal(decimal.Zero))
}

// ---------------------------------------------------------------------------
// sumPostings
// ---------------------------------------------------------------------------

func TestSumPostings(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		result := sumPostings(nil)
		assert.True(t, result.Equal(decimal.Zero))
	})

	t.Run("single", func(t *testing.T) {
		t.Parallel()

		postings := []Posting{{Amount: decimal.NewFromInt(42)}}
		result := sumPostings(postings)
		assert.True(t, result.Equal(decimal.NewFromInt(42)))
	})

	t.Run("multiple", func(t *testing.T) {
		t.Parallel()

		d1, _ := decimal.NewFromString("33.33")
		d2, _ := decimal.NewFromString("33.33")
		d3, _ := decimal.NewFromString("33.34")

		postings := []Posting{{Amount: d1}, {Amount: d2}, {Amount: d3}}
		result := sumPostings(postings)
		assert.True(t, result.Equal(decimal.NewFromInt(100)),
			"expected 100, got %s", result)
	})
}
