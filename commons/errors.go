package commons

import (
	constant "github.com/LerianStudio/lib-commons/commons/constants"
)

// Response represents a business error with code, title, and message.
type Response struct {
	EntityType string `json:"entityType,omitempty"`
	Title      string `json:"title,omitempty"`
	Message    string `json:"message,omitempty"`
	Code       string `json:"code,omitempty"`
	Err        error  `json:"err,omitempty"`
}

func (e Response) Error() string {
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}

	return e.Message
}

// ValidateBusinessError validates the error and returns the appropriate business error code, title, and message.
//
// Parameters:
//   - err: The error to be validated (ref: https://github.com/LerianStudio/midaz/common/constant/errors.go).
//   - entityType: The type of the entity related to the error.
//   - args: Additional arguments for formatting error messages.
//
// Returns:
//   - error: The appropriate business error with code, title, and message.
func ValidateBusinessError(err error, entityType string, _ ...any) error {
	errorMap := map[error]error{
		constant.ErrAccountIneligibility: Response{
			EntityType: entityType,
			Code:       constant.ErrAccountIneligibility.Error(),
			Title:      "Account Ineligibility Response",
			Message:    "One or more accounts listed in the transaction are not eligible to participate. Please review the account statuses and try again.",
		},
		constant.ErrInsufficientFunds: Response{
			EntityType: entityType,
			Code:       constant.ErrInsufficientFunds.Error(),
			Title:      "Insufficient Funds Response",
			Message:    "The transaction could not be completed due to insufficient funds in the account. Please add sufficient funds to your account and try again.",
		},
		constant.ErrAssetCodeNotFound: Response{
			EntityType: entityType,
			Code:       constant.ErrAssetCodeNotFound.Error(),
			Title:      "Asset Code Not Found",
			Message:    "The provided asset code does not exist in our records. Please verify the asset code and try again.",
		},
		constant.ErrAccountStatusTransactionRestriction: Response{
			EntityType: entityType,
			Code:       constant.ErrAccountStatusTransactionRestriction.Error(),
			Title:      "Account Status Transaction Restriction",
			Message:    "The current statuses of the source and/or destination accounts do not permit transactions. Change the account status(es) and try again.",
		},
		constant.ErrOverFlowInt64: Response{
			EntityType: entityType,
			Code:       constant.ErrOverFlowInt64.Error(),
			Title:      "Overflow Error",
			Message:    "The request could not be completed due to an overflow. Please check the values, and try again.",
		},
	}

	if mappedError, found := errorMap[err]; found {
		return mappedError
	}

	return err
}
