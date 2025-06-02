// Package constant defines common constants used across the application.
// It includes error codes, status values, and other shared constant definitions.
package constant

import "errors"

var (
	// ErrInsufficientFunds indicates account has insufficient funds for transaction.
	ErrInsufficientFunds = errors.New("0018")
	// ErrAccountIneligibility indicates account is not eligible for transaction.
	ErrAccountIneligibility = errors.New("0019")
	// ErrAccountStatusTransactionRestriction indicates account status prevents transactions.
	ErrAccountStatusTransactionRestriction = errors.New("0024")
	// ErrAssetCodeNotFound indicates the specified asset code does not exist.
	ErrAssetCodeNotFound = errors.New("0034")
	// ErrTransactionValueMismatch indicates transaction values do not match expected amounts.
	ErrTransactionValueMismatch = errors.New("0073")
	// ErrTransactionAmbiguous indicates transaction details are ambiguous or unclear.
	ErrTransactionAmbiguous = errors.New("0090")
	// ErrOverFlowInt64 indicates an integer overflow occurred during calculation.
	ErrOverFlowInt64 = errors.New("0097")
)
