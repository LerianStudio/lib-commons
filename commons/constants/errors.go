package constant

import "errors"

// Error code string constants — single source of truth for numeric codes
// shared between sentinel errors (below) and domain ErrorCode types.
const (
	// CodeInsufficientFunds is the code for insufficient balance.
	CodeInsufficientFunds = "0018"
	// CodeAccountIneligibility is the code for account ineligibility.
	CodeAccountIneligibility = "0019"
	// CodeAccountStatusTransactionRestriction is the code for account status restrictions.
	CodeAccountStatusTransactionRestriction = "0024"
	// CodeAssetCodeNotFound is the code for missing asset.
	CodeAssetCodeNotFound = "0034"
	// CodeMetadataKeyLengthExceeded is the code for metadata key exceeding length limit.
	CodeMetadataKeyLengthExceeded = "0050"
	// CodeMetadataValueLengthExceeded is the code for metadata value exceeding length limit.
	CodeMetadataValueLengthExceeded = "0051"
	// CodeTransactionValueMismatch is the code for allocation vs total mismatch.
	CodeTransactionValueMismatch = "0073"
	// CodeTransactionAmbiguous is the code for ambiguous transaction routing.
	CodeTransactionAmbiguous = "0090"
	// CodeOverFlowInt64 is the code for int64 overflow.
	CodeOverFlowInt64 = "0097"
	// CodeOnHoldExternalAccount is the code for on-hold on external accounts.
	CodeOnHoldExternalAccount = "0098"
)

var (
	// ErrInsufficientFunds maps to transaction error code 0018.
	ErrInsufficientFunds = errors.New(CodeInsufficientFunds)
	// ErrAccountIneligibility maps to transaction error code 0019.
	ErrAccountIneligibility = errors.New(CodeAccountIneligibility)
	// ErrAccountStatusTransactionRestriction maps to transaction error code 0024.
	ErrAccountStatusTransactionRestriction = errors.New(CodeAccountStatusTransactionRestriction)
	// ErrAssetCodeNotFound maps to transaction error code 0034.
	ErrAssetCodeNotFound = errors.New(CodeAssetCodeNotFound)
	// ErrMetadataKeyLengthExceeded maps to metadata error code 0050.
	ErrMetadataKeyLengthExceeded = errors.New(CodeMetadataKeyLengthExceeded)
	// ErrMetadataValueLengthExceeded maps to metadata error code 0051.
	ErrMetadataValueLengthExceeded = errors.New(CodeMetadataValueLengthExceeded)
	// ErrTransactionValueMismatch maps to transaction error code 0073.
	ErrTransactionValueMismatch = errors.New(CodeTransactionValueMismatch)
	// ErrTransactionAmbiguous maps to transaction error code 0090.
	ErrTransactionAmbiguous = errors.New(CodeTransactionAmbiguous)
	// ErrOverFlowInt64 maps to transaction error code 0097.
	ErrOverFlowInt64 = errors.New(CodeOverFlowInt64)
	// ErrOnHoldExternalAccount maps to transaction error code 0098.
	ErrOnHoldExternalAccount = errors.New(CodeOnHoldExternalAccount)
)
