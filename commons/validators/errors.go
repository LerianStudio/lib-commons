package validators

import (
	"errors"
	"fmt"
)

// ErrorWithCode is an error with an associated error code
type ErrorWithCode struct {
	Code    string
	Message string
}

// Error implements the error interface
func (e ErrorWithCode) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// NewErrorWithCode creates an error with code and message
func NewErrorWithCode(code string, message string) ErrorWithCode {
	return ErrorWithCode{
		Code:    code,
		Message: message,
	}
}

// Error code constants for validation errors
const (
	ErrAssetTypeRequired      = "0040"
	ErrInvalidAssetType       = "0041"
	ErrAccountTypeRequired    = "0066"
	ErrInvalidAccountType     = "0067"
	ErrCurrencyRequired       = "0005"
	ErrInvalidCurrency        = "0006"
	ErrCountryCodeRequired    = "0032"
	ErrInvalidCountry         = "0033"
	ErrTransactionNil         = "0050"
	ErrAssetRequired          = "0051"
	ErrInvalidAssetFormat     = "0052"
	ErrNegativeAmount         = "0053"
	ErrNoSourceAccounts       = "0054"
	ErrNoDestAccounts         = "0055"
	ErrEmptyAccountReference  = "0056"
	ErrInvalidExternalFormat  = "0057"
	ErrInvalidExternalAsset   = "0058"
	ErrAssetMismatch          = "0059"
	ErrAddressNil             = "0060"
	ErrLine1Required          = "0061"
	ErrLine1TooLong           = "0062"
	ErrLine2TooLong           = "0063"
	ErrZipRequired            = "0064"
	ErrZipTooLong             = "0065"
	ErrCityRequired           = "0080" // Changed from 0066 (duplicate)
	ErrCityTooLong            = "0081" // Changed from 0067 (duplicate)
	ErrStateRequired          = "0068"
	ErrStateTooLong           = "0069"
	ErrAddressCountryRequired = "0070"
	ErrMetadataKeyEmpty       = "0071"
	ErrMetadataKeyTooLong     = "0072"
	ErrMetadataInvalidType    = "0078" // Changed from 0073 to avoid conflict with ErrTransactionValueMismatch
	ErrMetadataStringTooLong  = "0079" // Changed from 0074 to avoid potential conflicts
	ErrMetadataIntRange       = "0082" // Changed from 0075 to avoid potential conflicts
	ErrMetadataFloatRange     = "0083" // Changed from 0076 to avoid potential conflicts
	ErrMetadataSizeTooLarge   = "0084" // Changed from 0077 to avoid potential conflicts
)

// ErrorMessages maps error codes to human-readable messages
var ErrorMessages = map[string]string{
	ErrAssetTypeRequired:      "Asset type is required",
	ErrInvalidAssetType:       "Invalid asset type. Valid types are: crypto, currency, commodity, others",
	ErrAccountTypeRequired:    "Account type is required",
	ErrInvalidAccountType:     "Invalid account type. Valid types are: deposit, savings, loans, marketplace, creditCard",
	ErrCurrencyRequired:       "Currency code is required",
	ErrInvalidCurrency:        "Invalid currency code",
	ErrCountryCodeRequired:    "Country code is required",
	ErrInvalidCountry:         "Invalid country code. Must be a valid ISO 3166-1 alpha-2 code",
	ErrTransactionNil:         "Transaction input cannot be nil",
	ErrAssetRequired:          "Asset code is required",
	ErrInvalidAssetFormat:     "Invalid asset code format. Must be 3-4 uppercase letters",
	ErrNegativeAmount:         "Transaction amount must be greater than zero",
	ErrNoSourceAccounts:       "At least one source account is required",
	ErrNoDestAccounts:         "At least one destination account is required",
	ErrEmptyAccountReference:  "Account reference cannot be empty",
	ErrInvalidExternalFormat:  "Invalid external account format. Must be @external/XXX where XXX is a valid asset code",
	ErrInvalidExternalAsset:   "Invalid asset code in external account. Must be 3-4 uppercase letters",
	ErrAssetMismatch:          "External account asset must match transaction asset",
	ErrAddressNil:             "Address cannot be nil",
	ErrLine1Required:          "Address line 1 is required",
	ErrLine1TooLong:           "Address line 1 exceeds maximum length",
	ErrLine2TooLong:           "Address line 2 exceeds maximum length",
	ErrZipRequired:            "Zip code is required",
	ErrZipTooLong:             "Zip code exceeds maximum length",
	ErrCityRequired:           "City is required",
	ErrCityTooLong:            "City exceeds maximum length",
	ErrStateRequired:          "State is required",
	ErrStateTooLong:           "State exceeds maximum length",
	ErrAddressCountryRequired: "Country is required for address",
	ErrMetadataKeyEmpty:       "Metadata key cannot be empty",
	ErrMetadataKeyTooLong:     "Metadata key exceeds maximum length",
	ErrMetadataInvalidType:    "Unsupported metadata value type. Must be string, number, boolean, or nil",
	ErrMetadataStringTooLong:  "Metadata string value exceeds maximum length",
	ErrMetadataIntRange:       "Metadata integer value outside allowed range (-9999999999 to 9999999999)",
	ErrMetadataFloatRange:     "Metadata float value outside allowed range (-9999999999.0 to 9999999999.0)",
	ErrMetadataSizeTooLarge:   "Total metadata size exceeds maximum allowed size",
}

// GetErrorMessage returns a human-readable error message for an error code
func GetErrorMessage(code string) string {
	if message, ok := ErrorMessages[code]; ok {
		return message
	}

	return "Unknown error"
}

// GetCodeFromError extracts the error code from an error
func GetCodeFromError(err error) string {
	if codeErr, ok := err.(ErrorWithCode); ok {
		return codeErr.Code
	}
	// For existing lib-commons errors, they're just strings with codes
	return err.Error()
}

// ParseError parses an error and returns a structured error
func ParseError(err error) ErrorWithCode {
	code := GetCodeFromError(err)
	message := GetErrorMessage(code)

	return NewErrorWithCode(code, message)
}

// ValidateError validates if a given error code exists
func ValidateError(code string) error {
	if _, ok := ErrorMessages[code]; !ok {
		return errors.New("invalid error code")
	}

	return nil
}
