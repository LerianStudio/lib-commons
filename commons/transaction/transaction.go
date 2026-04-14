package transaction

import (
	"fmt"
	"strings"
	"time"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/shopspring/decimal"
)

// Operation represents the posting operation applied to a balance.
type Operation string

const (
	// OperationDebit decreases available balance from a source.
	OperationDebit Operation = Operation(constant.DEBIT)
	// OperationCredit increases available balance on a destination.
	OperationCredit Operation = Operation(constant.CREDIT)
	// OperationOnHold moves value from available to on-hold.
	OperationOnHold Operation = Operation(constant.ONHOLD)
	// OperationRelease moves value from on-hold back to available.
	OperationRelease Operation = Operation(constant.RELEASE)
)

// TransactionStatus represents the lifecycle state of a transaction intent.
//
// Semantics:
//   - CREATED: intent recorded but not yet submitted for processing.
//   - APPROVED: intent approved for execution but not yet applied.
//   - PENDING: intent currently being processed (balance updates in flight).
//   - CANCELED: intent rejected or rolled back; terminal state.
//
// Typical transitions:
//
//	CREATED → APPROVED | CANCELED
//	APPROVED → PENDING | CANCELED
//	PENDING → (terminal; see associated Posting status for settlement)
type TransactionStatus string

const (
	// StatusCreated marks an intent as recorded but not yet approved.
	StatusCreated TransactionStatus = TransactionStatus(constant.CREATED)
	// StatusApproved marks an intent as approved for processing.
	StatusApproved TransactionStatus = TransactionStatus(constant.APPROVED)
	// StatusPending marks an intent as currently being processed.
	StatusPending TransactionStatus = TransactionStatus(constant.PENDING)
	// StatusCanceled marks an intent as rejected or rolled back.
	StatusCanceled TransactionStatus = TransactionStatus(constant.CANCELED)
)

// AccountType classifies balances by ownership boundary.
type AccountType string

const (
	// AccountTypeInternal identifies balances owned within the platform.
	AccountTypeInternal AccountType = "internal"
	// AccountTypeExternal identifies balances owned outside the platform.
	AccountTypeExternal AccountType = "external"
)

// ErrorCode is a domain error code used by transaction validations.
type ErrorCode string

const (
	// ErrorInsufficientFunds indicates the source balance cannot cover the amount.
	ErrorInsufficientFunds ErrorCode = ErrorCode(constant.CodeInsufficientFunds)
	// ErrorAccountIneligibility indicates the account cannot participate in the transaction.
	ErrorAccountIneligibility ErrorCode = ErrorCode(constant.CodeAccountIneligibility)
	// ErrorAccountStatusTransactionRestriction indicates account status blocks this transaction.
	ErrorAccountStatusTransactionRestriction ErrorCode = ErrorCode(constant.CodeAccountStatusTransactionRestriction)
	// ErrorAssetCodeNotFound indicates the requested asset was not found.
	ErrorAssetCodeNotFound ErrorCode = ErrorCode(constant.CodeAssetCodeNotFound)
	// ErrorTransactionValueMismatch indicates allocations do not match transaction total.
	ErrorTransactionValueMismatch ErrorCode = ErrorCode(constant.CodeTransactionValueMismatch)
	// ErrorTransactionAmbiguous indicates transaction routing cannot be determined uniquely.
	ErrorTransactionAmbiguous ErrorCode = ErrorCode(constant.CodeTransactionAmbiguous)
	// ErrorOnHoldExternalAccount indicates on-hold operations are not allowed for external accounts.
	ErrorOnHoldExternalAccount ErrorCode = ErrorCode(constant.CodeOnHoldExternalAccount)
	// ErrorDataCorruption indicates persisted transaction data is inconsistent.
	ErrorDataCorruption ErrorCode = "0099"
	// ErrorInvalidInput indicates request payload validation failed.
	ErrorInvalidInput ErrorCode = "1001"
	// ErrorInvalidStateTransition indicates an invalid transaction state transition was requested.
	ErrorInvalidStateTransition ErrorCode = "1002"
	// ErrorCrossScope indicates balances from different organizations or ledgers are mixed.
	ErrorCrossScope ErrorCode = "1003"
)

// DomainError represents a structured transaction domain validation error.
type DomainError struct {
	Code    ErrorCode
	Field   string
	Message string
}

// Error returns the formatted domain error string.
func (e DomainError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}

	return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Field)
}

// NewDomainError creates a domain error with code, field, and message.
func NewDomainError(code ErrorCode, field, message string) error {
	return DomainError{Code: code, Field: field, Message: message}
}

// Balance contains the balance state used during intent planning and posting.
type Balance struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	LedgerID       string          `json:"ledgerId"`
	AccountID      string          `json:"accountId"`
	Asset          string          `json:"asset"`
	Available      decimal.Decimal `json:"available"`
	OnHold         decimal.Decimal `json:"onHold"`
	Version        int64           `json:"version"`
	AccountType    AccountType     `json:"accountType"`
	AllowSending   bool            `json:"allowSending"`
	AllowReceiving bool            `json:"allowReceiving"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	DeletedAt      *time.Time      `json:"deletedAt"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
}

// LedgerTarget identifies the account and balance affected by a posting.
type LedgerTarget struct {
	AccountID string `json:"accountId"`
	BalanceID string `json:"balanceId"`
}

func (t LedgerTarget) validate(field string) error {
	if strings.TrimSpace(t.AccountID) == "" {
		return NewDomainError(ErrorInvalidInput, field+".accountId", "accountId is required")
	}

	if strings.TrimSpace(t.BalanceID) == "" {
		return NewDomainError(ErrorInvalidInput, field+".balanceId", "balanceId is required")
	}

	return nil
}

// Allocation defines how part of the transaction total is assigned.
type Allocation struct {
	Target    LedgerTarget     `json:"target"`
	Amount    *decimal.Decimal `json:"amount,omitempty"`
	Share     *decimal.Decimal `json:"share,omitempty"`
	Remainder bool             `json:"remainder"`
	Route     string           `json:"route,omitempty"`
}

// TransactionIntentInput is the user input used to build a deterministic plan.
type TransactionIntentInput struct {
	Asset        string          `json:"asset"`
	Total        decimal.Decimal `json:"total"`
	Pending      bool            `json:"pending"`
	Sources      []Allocation    `json:"sources"`
	Destinations []Allocation    `json:"destinations"`
}

// Posting is a concrete operation to apply against a target balance.
type Posting struct {
	Target    LedgerTarget      `json:"target"`
	Asset     string            `json:"asset"`
	Amount    decimal.Decimal   `json:"amount"`
	Operation Operation         `json:"operation"`
	Status    TransactionStatus `json:"status"`
	Route     string            `json:"route,omitempty"`
}

// IntentPlan is the validated and expanded representation of a transaction intent.
type IntentPlan struct {
	Asset        string          `json:"asset"`
	Total        decimal.Decimal `json:"total"`
	Pending      bool            `json:"pending"`
	Sources      []Posting       `json:"sources"`
	Destinations []Posting       `json:"destinations"`
}
