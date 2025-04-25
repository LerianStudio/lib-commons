// Package validators provides validation utilities for the Midaz ecosystem.
//
// This package contains reusable validation functions that can be used
// by both the backend services and the SDK. The goal is to ensure
// consistent validation behavior across all components.
package validators

import (
	"regexp"
)

// TypeValidator defines the interface for validating asset types
type TypeValidator interface {
	// ValidateType validates if an asset type is valid
	ValidateType(assetType string) error
}

// AccountTypeValidator defines the interface for validating account types
type AccountTypeValidator interface {
	// ValidateAccountType validates if an account type is valid
	ValidateAccountType(accountType string) error
}

// CurrencyValidator defines the interface for validating currency codes
type CurrencyValidator interface {
	// ValidateCurrency validates if a currency code is valid
	ValidateCurrency(code string) error
}

// CountryValidator defines the interface for validating country codes
type CountryValidator interface {
	// ValidateCountryAddress validates if a country code is valid
	ValidateCountryAddress(code string) error
}

// BasicValidator defines a validator that supports the core validation operations
type BasicValidator interface {
	TypeValidator
	AccountTypeValidator
	CurrencyValidator
	CountryValidator
}

// Common validation patterns
var (
	// ExternalAccountPattern is the regex pattern for external account references
	ExternalAccountPattern = regexp.MustCompile(`^@external/([A-Z]{3,4})$`)

	// AccountAliasPattern is the regex pattern for account aliases
	AccountAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,50}$`)

	// AssetCodePattern is the regex pattern for asset codes
	AssetCodePattern = regexp.MustCompile(`^[A-Z]{3,4}$`)

	// TransactionCodePattern is the regex pattern for transaction codes
	TransactionCodePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
)