package validators

// Hard-coded error codes for existing errors
const (
	// Existing error codes from commons/constants/errors.go
	ErrInsufficientFunds                   = "0018"
	ErrAccountIneligibility                = "0019"
	ErrAccountStatusTransactionRestriction = "0024"
	ErrAssetCodeNotFound                   = "0034"
	ErrTransactionValueMismatch            = "0073"
	ErrTransactionAmbiguous                = "0090"
)

func init() {
	// Add existing error messages to our map
	additionalErrorMessages := map[string]string{
		ErrInsufficientFunds:                   "Insufficient funds for transaction",
		ErrAccountIneligibility:                "Account is ineligible for this operation",
		ErrAccountStatusTransactionRestriction: "Account status restricts this transaction",
		ErrAssetCodeNotFound:                   "Asset code not found",
		ErrTransactionValueMismatch:            "Transaction value mismatch between operations",
		ErrTransactionAmbiguous:                "Transaction is ambiguous or invalid",
	}

	// Add these to our main error messages map
	for code, message := range additionalErrorMessages {
		ErrorMessages[code] = message
	}
}