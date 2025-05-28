// Package constant defines common constants used across the application.
// It includes error codes, status values, and other shared constant definitions.
package constant

import "errors"

var (
	ErrInsufficientFunds                   = errors.New("0018")
	ErrAccountIneligibility                = errors.New("0019")
	ErrAccountStatusTransactionRestriction = errors.New("0024")
	ErrAssetCodeNotFound                   = errors.New("0034")
	ErrTransactionValueMismatch            = errors.New("0073")
	ErrTransactionAmbiguous                = errors.New("0090")
	ErrOverFlowInt64                       = errors.New("0097")
)
