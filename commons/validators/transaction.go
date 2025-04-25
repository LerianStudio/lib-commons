package validators

import (
	"errors"
	"fmt"
	"strings"
)

// TransactionDSLValidator defines an interface for transaction DSL validation
type TransactionDSLValidator interface {
	GetAsset() string
	GetValue() float64
	GetSourceAccounts() []AccountReference
	GetDestinationAccounts() []AccountReference
	GetMetadata() map[string]any
}

// AccountReference defines an interface for account references in transactions
type AccountReference interface {
	GetAccount() string
}

// ValidateTransactionDSL performs pre-validation of transaction DSL input
// before sending to the API to catch common errors early
func ValidateTransactionDSL(input TransactionDSLValidator) error {
	if input == nil {
		return errors.New(ErrTransactionNil) // Transaction input cannot be nil
	}

	// Validate asset code
	asset := input.GetAsset()
	if asset == "" {
		return errors.New(ErrAssetRequired) // Asset code is required
	}

	if !AssetCodePattern.MatchString(asset) {
		return errors.New(ErrInvalidAssetFormat) // Invalid asset code format
	}

	// Validate amount
	if input.GetValue() <= 0 {
		return errors.New(ErrNegativeAmount) // Transaction amount must be greater than zero
	}

	// Validate source accounts
	sourceAccounts := input.GetSourceAccounts()
	if len(sourceAccounts) == 0 {
		return errors.New(ErrNoSourceAccounts) // At least one source account is required
	}

	for i, account := range sourceAccounts {
		if err := validateAccountReference(account.GetAccount(), asset); err != nil {
			return fmt.Errorf("invalid source account at index %d: %w", i, err)
		}
	}

	// Validate destination accounts
	destAccounts := input.GetDestinationAccounts()
	if len(destAccounts) == 0 {
		return errors.New(ErrNoDestAccounts) // At least one destination account is required
	}

	for i, account := range destAccounts {
		if err := validateAccountReference(account.GetAccount(), asset); err != nil {
			return fmt.Errorf("invalid destination account at index %d: %w", i, err)
		}
	}

	// Validate asset consistency across external accounts
	if err := validateAssetConsistency(input); err != nil {
		return err
	}

	// Validate metadata if present
	metadata := input.GetMetadata()
	if metadata != nil {
		if err := ValidateMetadata(metadata); err != nil {
			return fmt.Errorf("invalid metadata: %w", err)
		}
	}

	return nil
}

// validateAssetConsistency checks that all accounts in the transaction
// are using the same asset code
func validateAssetConsistency(input TransactionDSLValidator) error {
	for _, account := range input.GetSourceAccounts() {
		matches := ExternalAccountPattern.FindStringSubmatch(account.GetAccount())
		if len(matches) > 1 {
			externalAsset := matches[1]
			if externalAsset != input.GetAsset() {
				return fmt.Errorf("asset code mismatch: transaction uses %s but external account uses %s",
					input.GetAsset(), externalAsset)
			}
		}
	}

	for _, account := range input.GetDestinationAccounts() {
		matches := ExternalAccountPattern.FindStringSubmatch(account.GetAccount())
		if len(matches) > 1 {
			externalAsset := matches[1]
			if externalAsset != input.GetAsset() {
				return fmt.Errorf("asset code mismatch: transaction uses %s but external account uses %s",
					input.GetAsset(), externalAsset)
			}
		}
	}

	return nil
}

// validateAccountReference checks if an account reference is valid
// for both regular accounts and external accounts
func validateAccountReference(account string, transactionAsset string) error {
	if account == "" {
		return errors.New(ErrEmptyAccountReference) // Account reference cannot be empty
	}

	// Check if it's an external account reference
	if strings.HasPrefix(account, "@external") {
		// First check if it matches our expected pattern
		matches := ExternalAccountPattern.FindStringSubmatch(account)
		if len(matches) == 0 {
			return fmt.Errorf("invalid external account format: %s (must be @external/XXX where XXX is a valid asset code)", account)
		}

		externalAsset := matches[1]
		// Validate the external asset code format
		if !AssetCodePattern.MatchString(externalAsset) {
			return fmt.Errorf("invalid asset code in external account: %s (must be 3-4 uppercase letters)", externalAsset)
		}

		// Validate that the external asset matches the transaction asset
		if externalAsset != transactionAsset {
			return fmt.Errorf("external account asset (%s) must match transaction asset (%s)",
				externalAsset, transactionAsset)
		}
	}

	return nil
}

// GetExternalAccountReference creates a properly formatted external account reference
// for the given asset code
func GetExternalAccountReference(assetCode string) string {
	return "@external/" + assetCode
}