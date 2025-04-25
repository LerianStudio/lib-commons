package constant

import "errors"

// Validation error codes - defined in validators package
// These are defined here to avoid import cycles
const (
	// Asset type validation errors
	ErrAssetTypeRequired = "0040"
	ErrInvalidAssetType  = "0041"

	// Account type validation errors
	ErrAccountTypeRequired = "0066"
	ErrInvalidAccountType  = "0067"

	// Currency validation errors
	ErrCurrencyRequired = "0005"
	ErrInvalidCurrency  = "0006"

	// Country validation errors
	ErrCountryCodeRequired = "0032"
	ErrInvalidCountry      = "0033"

	// Transaction validation errors
	ErrTransactionNil        = "0050"
	ErrAssetRequired         = "0051"
	ErrInvalidAssetFormat    = "0052"
	ErrNegativeAmount        = "0053"
	ErrNoSourceAccounts      = "0054"
	ErrNoDestAccounts        = "0055"
	ErrEmptyAccountReference = "0056"
	ErrInvalidExternalFormat = "0057"
	ErrInvalidExternalAsset  = "0058"
	ErrAssetMismatch         = "0059"
)

// ValidationErrors provides error objects for validation errors
var (
	ValidationAssetTypeRequired = errors.New(ErrAssetTypeRequired)
	ValidationInvalidAssetType  = errors.New(ErrInvalidAssetType)
	ValidationAccountTypeRequired = errors.New(ErrAccountTypeRequired)
	ValidationInvalidAccountType  = errors.New(ErrInvalidAccountType)
	ValidationCurrencyRequired = errors.New(ErrCurrencyRequired)
	ValidationInvalidCurrency  = errors.New(ErrInvalidCurrency)
	ValidationCountryRequired  = errors.New(ErrCountryCodeRequired)
	ValidationInvalidCountry   = errors.New(ErrInvalidCountry)
)