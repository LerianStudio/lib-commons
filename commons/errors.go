package commons

import (
	"errors"
	"fmt"
	"strings"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/security"
)

// Response represents a business error with code, title, and message.
type Response struct {
	EntityType string `json:"entityType,omitempty"`
	Title      string `json:"title,omitempty"`
	Message    string `json:"message,omitempty"`
	Code       string `json:"code,omitempty"`
	Err        error  `json:"err,omitempty"`
}

// Error returns the business-facing message and satisfies the error interface.
func (e Response) Error() string {
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
func ValidateBusinessError(err error, entityType string, args ...any) error {
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
		constant.ErrOnHoldExternalAccount: Response{
			EntityType: entityType,
			Code:       constant.ErrOnHoldExternalAccount.Error(),
			Title:      "Invalid Pending Transaction",
			Message:    "External accounts cannot be used for pending transactions in source operations. Please check the accounts and try again.",
		},
	}
	// Use errors.Is to match wrapped sentinels instead of exact map identity.
	for sentinel, mappedError := range errorMap {
		if !errors.Is(err, sentinel) {
			continue
		}

		var response Response
		if !errors.As(mappedError, &response) {
			return mappedError
		}

		if len(args) > 0 {
			parts := make([]string, 0, len(args))

			for _, arg := range args {
				s := fmt.Sprint(arg)
				// Redact arguments that look like sensitive fields (credentials, PII)
				// to prevent leaking them to external API consumers.
				if looksLikeSensitiveArg(s) {
					continue
				}

				parts = append(parts, s)
			}

			if len(parts) > 0 {
				response.Message = fmt.Sprintf("%s (%s)", response.Message, strings.Join(parts, ", "))
			}
		}

		return response
	}

	return err
}

// looksLikeSensitiveArg checks whether a stringified argument contains a key=value
// pair where the key is a known sensitive field name.
func looksLikeSensitiveArg(s string) bool {
	if idx := strings.IndexByte(s, '='); idx > 0 {
		key := s[:idx]
		if security.IsSensitiveField(key) {
			return true
		}
	}

	return false
}
