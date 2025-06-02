package transaction

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/commons"
	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/LerianStudio/lib-commons/commons/opentelemetry"
)

// CurrencyPair represents a currency conversion pair with rates
type CurrencyPair struct {
	FromAsset string        `json:"from_asset"`
	ToAsset   string        `json:"to_asset"`
	Rate      float64       `json:"rate"`
	Timestamp time.Time     `json:"timestamp"`
	TTL       time.Duration `json:"ttl"`
}

// TransactionLimits defines limits for different account types and operations
type TransactionLimits struct {
	DailyLimit     int64            `json:"daily_limit"`
	MonthlyLimit   int64            `json:"monthly_limit"`
	MaxTransaction int64            `json:"max_transaction"`
	MinTransaction int64            `json:"min_transaction"`
	AssetLimits    map[string]int64 `json:"asset_limits"`
	AccountTypes   map[string]int64 `json:"account_types"`
}

// ValidationRules contains comprehensive validation rules for transactions
type ValidationRules struct {
	EnforceDoubleEntry          bool                         `json:"enforce_double_entry"`
	AllowNegativeBalances       bool                         `json:"allow_negative_balances"`
	RequireAssetConsistency     bool                         `json:"require_asset_consistency"`
	MaxDecimalPlaces            int                          `json:"max_decimal_places"`
	MinTransactionAmount        int64                        `json:"min_transaction_amount"`
	MaxTransactionAmount        int64                        `json:"max_transaction_amount"`
	AllowedAssets               []string                     `json:"allowed_assets"`
	RestrictedAssets            []string                     `json:"restricted_assets"`
	CurrencyConversionRules     map[string]CurrencyPair      `json:"currency_conversion_rules"`
	AccountTypeLimits           map[string]TransactionLimits `json:"account_type_limits"`
	RequireMetadata             []string                     `json:"require_metadata"`
	AllowCrossAssetTransactions bool                         `json:"allow_cross_asset_transactions"`
}

// TransactionContext provides additional context for validation
type TransactionContext struct {
	UserID            string                 `json:"user_id"`
	SessionID         string                 `json:"session_id"`
	IPAddress         string                 `json:"ip_address"`
	UserAgent         string                 `json:"user_agent"`
	TransactionSource string                 `json:"transaction_source"`
	BusinessRules     map[string]interface{} `json:"business_rules"`
	RiskAssessment    *RiskAssessment        `json:"risk_assessment"`
	ComplianceFlags   []string               `json:"compliance_flags"`
}

// RiskAssessment contains risk evaluation data
type RiskAssessment struct {
	RiskScore          float64   `json:"risk_score"`
	RiskLevel          string    `json:"risk_level"` // LOW, MEDIUM, HIGH, CRITICAL
	FraudProbability   float64   `json:"fraud_probability"`
	GeographicRisk     string    `json:"geographic_risk"`
	LastTransactionAt  time.Time `json:"last_transaction_at"`
	VelocityViolations []string  `json:"velocity_violations"`
}

// ValidateAdvancedTransaction performs comprehensive validation with business rules
func ValidateAdvancedTransaction(
	ctx context.Context,
	transaction Transaction,
	rules ValidationRules,
	txContext TransactionContext,
	balances []*Balance,
) error {
	logger := commons.NewLoggerFromContext(ctx)
	tracer := commons.NewTracerFromContext(ctx)

	_, span := tracer.Start(ctx, "validations.validate_advanced_transaction")
	defer span.End()

	// Phase 1: Basic structure validation
	if err := validateTransactionStructure(transaction, rules); err != nil {
		opentelemetry.HandleSpanError(&span, "validate_transaction_structure", err)
		logger.Errorf("Transaction structure validation failed: %v", err)
		return err
	}

	// Phase 2: Double-entry accounting validation
	if rules.EnforceDoubleEntry {
		if err := validateDoubleEntryAccounting(transaction); err != nil {
			opentelemetry.HandleSpanError(&span, "validate_double_entry", err)
			logger.Errorf("Double-entry validation failed: %v", err)
			return err
		}
	}

	// Phase 3: Asset and currency validation
	if err := validateAssetConsistency(transaction, rules); err != nil {
		opentelemetry.HandleSpanError(&span, "validate_asset_consistency", err)
		logger.Errorf("Asset consistency validation failed: %v", err)
		return err
	}

	// Phase 4: Multi-currency transaction validation
	if err := validateMultiCurrencyTransaction(transaction, rules); err != nil {
		opentelemetry.HandleSpanError(&span, "validate_multi_currency", err)
		logger.Errorf("Multi-currency validation failed: %v", err)
		return err
	}

	// Phase 5: Balance and account validation
	if err := validateAdvancedBalanceRules(transaction, balances, rules, txContext); err != nil {
		opentelemetry.HandleSpanError(&span, "validate_advanced_balance_rules", err)
		logger.Errorf("Advanced balance validation failed: %v", err)
		return err
	}

	// Phase 6: Business rules and compliance validation
	if err := validateBusinessRules(transaction, rules, txContext); err != nil {
		opentelemetry.HandleSpanError(&span, "validate_business_rules", err)
		logger.Errorf("Business rules validation failed: %v", err)
		return err
	}

	// Phase 7: Risk assessment validation
	if txContext.RiskAssessment != nil {
		if err := validateRiskAssessment(transaction, txContext.RiskAssessment, rules); err != nil {
			opentelemetry.HandleSpanError(&span, "validate_risk_assessment", err)
			logger.Errorf("Risk assessment validation failed: %v", err)
			return err
		}
	}

	// Phase 8: Precision and overflow validation
	if err := validatePrecisionAndOverflow(transaction, rules); err != nil {
		opentelemetry.HandleSpanError(&span, "validate_precision_overflow", err)
		logger.Errorf("Precision and overflow validation failed: %v", err)
		return err
	}

	logger.Infof("Advanced transaction validation completed successfully for transaction")
	return nil
}

// validateTransactionStructure validates basic transaction structure
func validateTransactionStructure(transaction Transaction, rules ValidationRules) error {
	// Validate required fields
	if transaction.Send.Asset == "" {
		return commons.ValidateBusinessError(
			constant.ErrAssetCodeNotFound,
			"validateTransactionStructure",
		)
	}

	if transaction.Send.Value <= 0 {
		return commons.ValidateBusinessError(
			"INVALID_AMOUNT",
			"Transaction amount must be positive",
		)
	}

	if transaction.Send.Scale < 0 || transaction.Send.Scale > 18 {
		return commons.ValidateBusinessError(
			"INVALID_SCALE",
			"Transaction scale must be between 0 and 18",
		)
	}

	// Validate decimal places
	if rules.MaxDecimalPlaces > 0 {
		actualDecimals := calculateDecimalPlaces(transaction.Send.Value, transaction.Send.Scale)
		if actualDecimals > rules.MaxDecimalPlaces {
			return commons.ValidateBusinessError("EXCESSIVE_DECIMAL_PLACES",
				fmt.Sprintf("Transaction has %d decimal places, maximum allowed is %d",
					actualDecimals, rules.MaxDecimalPlaces))
		}
	}

	// Validate transaction amount limits
	if rules.MinTransactionAmount > 0 && transaction.Send.Value < rules.MinTransactionAmount {
		return commons.ValidateBusinessError("AMOUNT_TOO_SMALL",
			fmt.Sprintf("Transaction amount %d is below minimum %d",
				transaction.Send.Value, rules.MinTransactionAmount))
	}

	if rules.MaxTransactionAmount > 0 && transaction.Send.Value > rules.MaxTransactionAmount {
		return commons.ValidateBusinessError("AMOUNT_TOO_LARGE",
			fmt.Sprintf("Transaction amount %d exceeds maximum %d",
				transaction.Send.Value, rules.MaxTransactionAmount))
	}

	// Validate source and destination accounts exist
	if len(transaction.Send.Source.From) == 0 {
		return commons.ValidateBusinessError(
			"NO_SOURCE_ACCOUNTS",
			"Transaction must have at least one source account",
		)
	}

	if len(transaction.Send.Distribute.To) == 0 {
		return commons.ValidateBusinessError(
			"NO_DESTINATION_ACCOUNTS",
			"Transaction must have at least one destination account",
		)
	}

	// Validate required metadata
	for _, requiredField := range rules.RequireMetadata {
		if transaction.Metadata == nil || transaction.Metadata[requiredField] == nil {
			return commons.ValidateBusinessError("MISSING_REQUIRED_METADATA",
				fmt.Sprintf("Required metadata field '%s' is missing", requiredField))
		}
	}

	return nil
}

// validateDoubleEntryAccounting ensures strict double-entry accounting principles
func validateDoubleEntryAccounting(transaction Transaction) error {
	// Calculate total debits (sources)
	totalDebits := int64(0)
	for _, source := range transaction.Send.Source.From {
		if source.Amount != nil {
			// Normalize to transaction scale for comparison
			normalizedValue := normalizeToScale(
				source.Amount.Value,
				source.Amount.Scale,
				transaction.Send.Scale,
			)
			totalDebits += normalizedValue
		}
	}

	// Calculate total credits (destinations)
	totalCredits := int64(0)
	for _, dest := range transaction.Send.Distribute.To {
		if dest.Amount != nil {
			// Normalize to transaction scale for comparison
			normalizedValue := normalizeToScale(
				dest.Amount.Value,
				dest.Amount.Scale,
				transaction.Send.Scale,
			)
			totalCredits += normalizedValue
		}
	}

	// In double-entry accounting, debits must equal credits
	if totalDebits != totalCredits {
		return commons.ValidateBusinessError("DOUBLE_ENTRY_VIOLATION",
			fmt.Sprintf("Debits (%d) do not equal credits (%d)", totalDebits, totalCredits))
	}

	// Validate that transaction total matches both debits and credits
	if totalDebits != transaction.Send.Value {
		return commons.ValidateBusinessError("TRANSACTION_TOTAL_MISMATCH",
			fmt.Sprintf("Transaction total (%d) does not match sum of sources (%d)",
				transaction.Send.Value, totalDebits))
	}

	return nil
}

// validateAssetConsistency ensures all assets in transaction are valid and consistent
func validateAssetConsistency(transaction Transaction, rules ValidationRules) error {
	mainAsset := transaction.Send.Asset

	// Validate main asset is allowed
	if len(rules.AllowedAssets) > 0 && !contains(rules.AllowedAssets, mainAsset) {
		return commons.ValidateBusinessError("ASSET_NOT_ALLOWED",
			fmt.Sprintf("Asset '%s' is not in allowed assets list", mainAsset))
	}

	// Validate main asset is not restricted
	if contains(rules.RestrictedAssets, mainAsset) {
		return commons.ValidateBusinessError("ASSET_RESTRICTED",
			fmt.Sprintf("Asset '%s' is restricted", mainAsset))
	}

	// If asset consistency is required, validate all amounts use the same asset
	if rules.RequireAssetConsistency {
		// Check source assets
		for _, source := range transaction.Send.Source.From {
			if source.Amount != nil && source.Amount.Asset != mainAsset {
				return commons.ValidateBusinessError("ASSET_INCONSISTENCY",
					fmt.Sprintf("Source account asset '%s' does not match transaction asset '%s'",
						source.Amount.Asset, mainAsset))
			}
		}

		// Check destination assets
		for _, dest := range transaction.Send.Distribute.To {
			if dest.Amount != nil && dest.Amount.Asset != mainAsset {
				return commons.ValidateBusinessError(
					"ASSET_INCONSISTENCY",
					fmt.Sprintf(
						"Destination account asset '%s' does not match transaction asset '%s'",
						dest.Amount.Asset,
						mainAsset,
					),
				)
			}
		}
	}

	// Validate asset format (e.g., ISO 4217 for currencies)
	if err := validateAssetFormat(mainAsset); err != nil {
		return err
	}

	return nil
}

// validateMultiCurrencyTransaction handles currency conversion validation
func validateMultiCurrencyTransaction(transaction Transaction, rules ValidationRules) error {
	if !rules.AllowCrossAssetTransactions {
		// If cross-asset transactions are not allowed, ensure all assets are the same
		mainAsset := transaction.Send.Asset

		for _, source := range transaction.Send.Source.From {
			if source.Amount != nil && source.Amount.Asset != mainAsset {
				return commons.ValidateBusinessError("CROSS_ASSET_NOT_ALLOWED",
					"Cross-asset transactions are not permitted")
			}
		}

		for _, dest := range transaction.Send.Distribute.To {
			if dest.Amount != nil && dest.Amount.Asset != mainAsset {
				return commons.ValidateBusinessError("CROSS_ASSET_NOT_ALLOWED",
					"Cross-asset transactions are not permitted")
			}
		}

		return nil
	}

	// If cross-asset transactions are allowed, validate currency conversions
	mainAsset := transaction.Send.Asset

	for _, source := range transaction.Send.Source.From {
		if source.Amount != nil && source.Amount.Asset != mainAsset {
			// Validate currency conversion rate exists and is current
			pairKey := fmt.Sprintf("%s-%s", source.Amount.Asset, mainAsset)
			if pair, exists := rules.CurrencyConversionRules[pairKey]; exists {
				// Check if rate is still valid (not expired)
				if time.Since(pair.Timestamp) > pair.TTL {
					return commons.ValidateBusinessError("CONVERSION_RATE_EXPIRED",
						fmt.Sprintf("Currency conversion rate for %s is expired", pairKey))
				}

				// Validate conversion calculation
				expectedValue := int64(float64(source.Amount.Value) * pair.Rate)
				tolerance := int64(float64(expectedValue) * 0.001) // 0.1% tolerance

				if math.Abs(float64(expectedValue-transaction.Send.Value)) > float64(tolerance) {
					return commons.ValidateBusinessError("CONVERSION_RATE_MISMATCH",
						fmt.Sprintf("Currency conversion calculation mismatch for %s", pairKey))
				}
			} else {
				return commons.ValidateBusinessError("CONVERSION_RATE_NOT_FOUND",
					fmt.Sprintf("No conversion rate found for %s", pairKey))
			}
		}
	}

	return nil
}

// validateAdvancedBalanceRules performs sophisticated balance validation
func validateAdvancedBalanceRules(
	transaction Transaction,
	balances []*Balance,
	rules ValidationRules,
	txContext TransactionContext,
) error {
	for _, balance := range balances {
		// Validate account type specific rules
		if limits, exists := rules.AccountTypeLimits[balance.AccountType]; exists {
			if err := validateAccountLimits(transaction, balance, limits, txContext); err != nil {
				return err
			}
		}

		// Validate asset-specific limits
		if assetLimit, exists := limits.AssetLimits[balance.AssetCode]; exists {
			if transaction.Send.Value > assetLimit {
				return commons.ValidateBusinessError("ASSET_LIMIT_EXCEEDED",
					fmt.Sprintf("Transaction amount %d exceeds asset limit %d for %s",
						transaction.Send.Value, assetLimit, balance.AssetCode))
			}
		}

		// Enhanced negative balance validation
		if !rules.AllowNegativeBalances && balance.AccountType != constant.ExternalAccountType {
			// Calculate balance after transaction
			var amountChange int64
			for _, source := range transaction.Send.Source.From {
				if balance.ID == source.Account || balance.Alias == source.Account {
					if source.Amount != nil {
						amountChange -= normalizeToScale(
							source.Amount.Value,
							source.Amount.Scale,
							balance.Scale,
						)
					}
				}
			}

			for _, dest := range transaction.Send.Distribute.To {
				if balance.ID == dest.Account || balance.Alias == dest.Account {
					if dest.Amount != nil {
						amountChange += normalizeToScale(
							dest.Amount.Value,
							dest.Amount.Scale,
							balance.Scale,
						)
					}
				}
			}

			projectedBalance := balance.Available + amountChange
			if projectedBalance < 0 {
				return commons.ValidateBusinessError(
					constant.ErrInsufficientFunds,
					fmt.Sprintf(
						"Transaction would result in negative balance for account %s",
						balance.Alias,
					),
				)
			}
		}

		// Validate account status and restrictions
		if err := validateAccountRestrictions(balance, transaction, txContext); err != nil {
			return err
		}
	}

	return nil
}

// validateAccountLimits checks transaction limits for specific account types
func validateAccountLimits(
	transaction Transaction,
	balance *Balance,
	limits TransactionLimits,
	txContext TransactionContext,
) error {
	// Validate single transaction limit
	if limits.MaxTransaction > 0 && transaction.Send.Value > limits.MaxTransaction {
		return commons.ValidateBusinessError("TRANSACTION_LIMIT_EXCEEDED",
			fmt.Sprintf("Transaction amount %d exceeds maximum %d for account type %s",
				transaction.Send.Value, limits.MaxTransaction, balance.AccountType))
	}

	if limits.MinTransaction > 0 && transaction.Send.Value < limits.MinTransaction {
		return commons.ValidateBusinessError("TRANSACTION_BELOW_MINIMUM",
			fmt.Sprintf("Transaction amount %d is below minimum %d for account type %s",
				transaction.Send.Value, limits.MinTransaction, balance.AccountType))
	}

	// Note: Daily and monthly limits would require additional data about historical transactions
	// This would typically be implemented with a separate service that tracks transaction history

	return nil
}

// validateAccountRestrictions checks account-specific restrictions
func validateAccountRestrictions(
	balance *Balance,
	transaction Transaction,
	txContext TransactionContext,
) error {
	// Validate account can send/receive based on transaction direction
	for _, source := range transaction.Send.Source.From {
		if balance.ID == source.Account || balance.Alias == source.Account {
			if !balance.AllowSending {
				return commons.ValidateBusinessError(
					constant.ErrAccountStatusTransactionRestriction,
					fmt.Sprintf("Account %s is not allowed to send funds", balance.Alias),
				)
			}
		}
	}

	for _, dest := range transaction.Send.Distribute.To {
		if balance.ID == dest.Account || balance.Alias == dest.Account {
			if !balance.AllowReceiving {
				return commons.ValidateBusinessError(
					constant.ErrAccountStatusTransactionRestriction,
					fmt.Sprintf("Account %s is not allowed to receive funds", balance.Alias),
				)
			}
		}
	}

	// Additional account restrictions based on context
	if err := validateContextualRestrictions(balance, txContext); err != nil {
		return err
	}

	return nil
}

// validateBusinessRules applies custom business logic validation
func validateBusinessRules(
	transaction Transaction,
	rules ValidationRules,
	txContext TransactionContext,
) error {
	// Validate compliance flags
	for _, flag := range txContext.ComplianceFlags {
		switch flag {
		case "AML_REVIEW_REQUIRED":
			return commons.ValidateBusinessError("AML_REVIEW_PENDING",
				"Transaction requires AML review before processing")
		case "ACCOUNT_FROZEN":
			return commons.ValidateBusinessError("ACCOUNT_FROZEN",
				"Account is frozen and cannot process transactions")
		case "SANCTIONS_CHECK_FAILED":
			return commons.ValidateBusinessError("SANCTIONS_VIOLATION",
				"Transaction violates sanctions requirements")
		}
	}

	// Validate business-specific rules from context
	if txContext.BusinessRules != nil {
		if err := validateCustomBusinessRules(transaction, txContext.BusinessRules); err != nil {
			return err
		}
	}

	return nil
}

// validateRiskAssessment checks risk-based validation rules
func validateRiskAssessment(
	transaction Transaction,
	risk *RiskAssessment,
	rules ValidationRules,
) error {
	// High-risk transactions require additional validation
	switch risk.RiskLevel {
	case "CRITICAL":
		return commons.ValidateBusinessError("RISK_TOO_HIGH",
			"Transaction risk level is critical and cannot be processed")
	case "HIGH":
		// High-risk transactions might have amount limits
		maxHighRiskAmount := int64(100000) // Could be configurable
		if transaction.Send.Value > maxHighRiskAmount {
			return commons.ValidateBusinessError("HIGH_RISK_AMOUNT_EXCEEDED",
				"High-risk transactions are limited in amount")
		}
	}

	// Validate fraud probability
	if risk.FraudProbability > 0.8 {
		return commons.ValidateBusinessError("FRAUD_PROBABILITY_HIGH",
			"Transaction has high fraud probability")
	}

	// Validate velocity violations
	if len(risk.VelocityViolations) > 0 {
		return commons.ValidateBusinessError("VELOCITY_VIOLATIONS",
			fmt.Sprintf("Transaction violates velocity rules: %v", risk.VelocityViolations))
	}

	return nil
}

// validatePrecisionAndOverflow checks for precision issues and potential overflows
func validatePrecisionAndOverflow(transaction Transaction, rules ValidationRules) error {
	// Check for potential overflow in calculations
	if WillOverflow(transaction.Send.Value, 0, transaction.Send.Scale) {
		return commons.ValidateBusinessError(constant.ErrOverFlowInt64,
			"Transaction value would cause overflow")
	}

	// Validate precision consistency across all amounts
	for _, source := range transaction.Send.Source.From {
		if source.Amount != nil {
			if err := validateAmountPrecision(source.Amount, rules.MaxDecimalPlaces); err != nil {
				return err
			}
		}
	}

	for _, dest := range transaction.Send.Distribute.To {
		if dest.Amount != nil {
			if err := validateAmountPrecision(dest.Amount, rules.MaxDecimalPlaces); err != nil {
				return err
			}
		}
	}

	return nil
}

// Helper functions

func calculateDecimalPlaces(value, scale int64) int {
	if scale == 0 {
		return 0
	}

	// Convert to float to check actual decimal places
	floatValue := float64(value) / math.Pow(10, float64(scale))
	valueString := strconv.FormatFloat(floatValue, 'f', -1, 64)

	if !strings.Contains(valueString, ".") {
		return 0
	}

	parts := strings.Split(valueString, ".")
	return len(strings.TrimRight(parts[1], "0"))
}

func normalizeToScale(value, fromScale, toScale int64) int64 {
	if fromScale == toScale {
		return value
	}

	if fromScale < toScale {
		return Scale(value, fromScale, toScale)
	}

	// When converting to a smaller scale, we need to divide
	factor := math.Pow(10, float64(fromScale-toScale))
	return int64(math.Round(float64(value) / factor))
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func validateAssetFormat(asset string) error {
	// Basic asset format validation
	if len(asset) < 2 || len(asset) > 10 {
		return commons.ValidateBusinessError("INVALID_ASSET_FORMAT",
			"Asset code must be between 2 and 10 characters")
	}

	// Asset code should be alphanumeric and uppercase
	matched, _ := regexp.MatchString("^[A-Z0-9]+$", asset)
	if !matched {
		return commons.ValidateBusinessError("INVALID_ASSET_FORMAT",
			"Asset code must contain only uppercase letters and numbers")
	}

	return nil
}

func validateContextualRestrictions(balance *Balance, txContext TransactionContext) error {
	// Example: Validate geographic restrictions
	if txContext.IPAddress != "" {
		// This would typically integrate with a geolocation service
		// For demo purposes, we'll check for known restricted patterns
		if strings.Contains(txContext.IPAddress, "192.168.") {
			// Local IP, might have different rules
		}
	}

	// Example: Validate user agent restrictions
	if strings.Contains(strings.ToLower(txContext.UserAgent), "bot") {
		return commons.ValidateBusinessError("AUTOMATED_ACCESS_RESTRICTED",
			"Automated access to financial transactions is restricted")
	}

	return nil
}

func validateCustomBusinessRules(
	transaction Transaction,
	businessRules map[string]interface{},
) error {
	// Example business rules
	if maxDailyAmount, exists := businessRules["max_daily_amount"]; exists {
		if amount, ok := maxDailyAmount.(float64); ok {
			if float64(transaction.Send.Value) > amount {
				return commons.ValidateBusinessError("DAILY_LIMIT_EXCEEDED",
					"Transaction exceeds daily limit")
			}
		}
	}

	if requireApproval, exists := businessRules["require_approval"]; exists {
		if require, ok := requireApproval.(bool); ok && require {
			return commons.ValidateBusinessError("APPROVAL_REQUIRED",
				"Transaction requires approval before processing")
		}
	}

	return nil
}

func validateAmountPrecision(amount *Amount, maxDecimalPlaces int) error {
	if maxDecimalPlaces <= 0 {
		return nil
	}

	actualDecimals := calculateDecimalPlaces(amount.Value, amount.Scale)
	if actualDecimals > maxDecimalPlaces {
		return commons.ValidateBusinessError("EXCESSIVE_DECIMAL_PLACES",
			fmt.Sprintf("Amount has %d decimal places, maximum allowed is %d",
				actualDecimals, maxDecimalPlaces))
	}

	return nil
}

// ValidateTransactionSequence validates a sequence of related transactions
func ValidateTransactionSequence(
	ctx context.Context,
	transactions []Transaction,
	rules ValidationRules,
) error {
	// Validate each transaction individually first
	for i, tx := range transactions {
		emptyContext := TransactionContext{}
		if err := ValidateAdvancedTransaction(ctx, tx, rules, emptyContext, nil); err != nil {
			return fmt.Errorf("transaction %d failed validation: %w", i, err)
		}
	}

	// Validate sequence-specific rules
	if err := validateSequenceConsistency(transactions); err != nil {
		return err
	}

	return nil
}

func validateSequenceConsistency(transactions []Transaction) error {
	// Check for conflicting transactions in the sequence
	accountBalances := make(map[string]int64)

	for i, tx := range transactions {
		// Track balance changes across the sequence
		for _, source := range tx.Send.Source.From {
			if source.Amount != nil {
				accountBalances[source.Account] -= source.Amount.Value
			}
		}

		for _, dest := range tx.Send.Distribute.To {
			if dest.Amount != nil {
				accountBalances[dest.Account] += dest.Amount.Value
			}
		}

		// Check for negative balances in sequence (if not allowed)
		for account, balance := range accountBalances {
			if balance < 0 {
				return commons.ValidateBusinessError(
					"SEQUENCE_NEGATIVE_BALANCE",
					fmt.Sprintf(
						"Transaction sequence would result in negative balance for account %s at transaction %d",
						account,
						i,
					),
				)
			}
		}
	}

	return nil
}
