// Package main demonstrates financial transaction processing using lib-commons
// with double-entry accounting, validation, and enterprise-grade patterns.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/commons/transaction"
	"github.com/LerianStudio/lib-commons/commons/validation"
)

func main() {
	fmt.Println("Financial Transaction Processing Examples")
	fmt.Println("========================================")

	// Example 1: Basic transfer transaction
	fmt.Println("\n1. Basic Transfer Transaction")
	basicTransferExample()

	// Example 2: Multi-asset transaction
	fmt.Println("\n2. Multi-Asset Transaction")
	multiAssetExample()

	// Example 3: Complex business transaction with validation
	fmt.Println("\n3. Complex Business Transaction")
	complexBusinessTransactionExample()

	// Example 4: External account handling
	fmt.Println("\n4. External Account Handling")
	externalAccountExample()

	// Example 5: Transaction validation and error handling
	fmt.Println("\n5. Transaction Validation")
	transactionValidationExample()

	// Example 6: Double-entry accounting verification
	fmt.Println("\n6. Double-Entry Accounting")
	doubleEntryAccountingExample()

	// Example 7: Balance operations and constraints
	fmt.Println("\n7. Balance Operations")
	balanceOperationsExample()

	// Example 8: Financial reporting and audit trails
	fmt.Println("\n8. Financial Reporting")
	financialReportingExample()
}

// basicTransferExample demonstrates a simple transfer between two accounts
func basicTransferExample() {
	fmt.Println("Creating a basic transfer transaction...")

	// Create a simple transfer transaction
	// Transfer $100 from Alice's account to Bob's account
	transferTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 10000, // $100.00 (scale 2)
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "alice@bank.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 10000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "bob@bank.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 10000,
							Scale: 2,
						},
					},
				},
			},
		},
	}

	// Validate the transaction structure
	ctx := context.Background()
	validator := validation.NewValidator()

	if err := validator.Validate(transferTx); err != nil {
		fmt.Printf("❌ Transaction validation failed: %v\n", err)
		return
	}

	fmt.Println("✅ Basic transfer transaction created successfully")
	fmt.Printf("   From: %s\n", transferTx.Send.Source.From[0].Account)
	fmt.Printf("   To: %s\n", transferTx.Send.Distribute.To[0].Account)
	fmt.Printf("   Amount: $%.2f %s\n",
		float64(transferTx.Send.Value)/100, transferTx.Send.Asset)

	// Process the transaction (in a real system)
	processTransaction(ctx, transferTx, "Basic Transfer")
}

// multiAssetExample demonstrates handling multiple currencies/assets
func multiAssetExample() {
	fmt.Println("Creating multi-asset transaction...")

	// Example: Currency exchange transaction
	// Convert $100 USD to EUR at 0.85 rate
	currencyExchangeTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 10000, // $100.00
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "customer@exchange.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 10000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "exchange@fees.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 100, // $1.00 fee
							Scale: 2,
						},
					},
					{
						Account: "exchange@treasury.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 9900, // $99.00 for conversion
							Scale: 2,
						},
					},
				},
			},
		},
	}

	// The EUR side would be a separate transaction
	eurCreditTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "EUR",
			Value: 8415, // €84.15 (99 * 0.85)
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "exchange@treasury.com",
						Amount: &transaction.Amount{
							Asset: "EUR",
							Value: 8415,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "customer@exchange.com",
						Amount: &transaction.Amount{
							Asset: "EUR",
							Value: 8415,
							Scale: 2,
						},
					},
				},
			},
		},
	}

	fmt.Println("✅ Multi-asset exchange transactions created")
	fmt.Printf("   USD Transaction: $%.2f → Exchange (with $%.2f fee)\n",
		float64(currencyExchangeTx.Send.Value)/100,
		float64(currencyExchangeTx.Send.Distribute.To[0].Amount.Value)/100)
	fmt.Printf("   EUR Transaction: €%.2f → Customer\n",
		float64(eurCreditTx.Send.Value)/100)

	ctx := context.Background()
	processTransaction(ctx, currencyExchangeTx, "Currency Exchange (USD)")
	processTransaction(ctx, eurCreditTx, "Currency Exchange (EUR)")
}

// complexBusinessTransactionExample demonstrates a complex business scenario
func complexBusinessTransactionExample() {
	fmt.Println("Creating complex business transaction...")

	// Scenario: E-commerce order with payment processing
	// - Customer pays $150 for order
	// - $5 goes to payment processor fee
	// - $15 goes to platform commission
	// - $130 goes to merchant
	ecommerceTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 15000, // $150.00
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "customer@ecommerce.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 15000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "payment-processor@fees.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 500, // $5.00 payment processing fee
							Scale: 2,
						},
					},
					{
						Account: "platform@commission.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 1500, // $15.00 platform commission
							Scale: 2,
						},
					},
					{
						Account: "merchant@store.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 13000, // $130.00 to merchant
							Scale: 2,
						},
					},
				},
			},
		},
		Metadata: map[string]interface{}{
			"order_id":         "ORD-12345",
			"customer_id":      "CUST-67890",
			"merchant_id":      "MERCH-ABC123",
			"payment_method":   "credit_card",
			"processing_time":  time.Now().Unix(),
			"platform_version": "v2.1.0",
		},
	}

	// Validate business rules
	ctx := context.Background()
	validator := validation.NewValidator()

	if err := validator.Validate(ecommerceTx); err != nil {
		fmt.Printf("❌ Business transaction validation failed: %v\n", err)
		return
	}

	// Verify the transaction balances
	responses, err := transaction.ValidateSendSourceAndDistribute(ecommerceTx)
	if err != nil {
		fmt.Printf("❌ Balance validation failed: %v\n", err)
		return
	}

	fmt.Println("✅ Complex business transaction created successfully")
	fmt.Printf("   Order ID: %s\n", ecommerceTx.Metadata["order_id"])
	fmt.Printf("   Total Amount: $%.2f\n", float64(responses.Total)/100)
	fmt.Printf("   Payment Fee: $%.2f\n",
		float64(ecommerceTx.Send.Distribute.To[0].Amount.Value)/100)
	fmt.Printf("   Platform Commission: $%.2f\n",
		float64(ecommerceTx.Send.Distribute.To[1].Amount.Value)/100)
	fmt.Printf("   Merchant Payout: $%.2f\n",
		float64(ecommerceTx.Send.Distribute.To[2].Amount.Value)/100)

	processTransaction(ctx, ecommerceTx, "E-commerce Order")
}

// externalAccountExample demonstrates external account handling
func externalAccountExample() {
	fmt.Println("Creating external account transaction...")

	// External accounts can have negative balances (for bank integrations, etc.)
	// Example: Customer withdrawal from bank account
	withdrawalTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 50000, // $500.00
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "external@bank-account.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 50000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "customer@wallet.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 49500, // $495.00 (after $5 fee)
							Scale: 2,
						},
					},
					{
						Account: "bank@fees.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 500, // $5.00 withdrawal fee
							Scale: 2,
						},
					},
				},
			},
		},
	}

	// Create mock balances for validation
	balances := []*transaction.Balance{
		{
			ID:           "bal-ext-001",
			Alias:        "external@bank-account.com",
			AssetCode:    "USD",
			Available:    1000000, // $10,000 available
			Scale:        2,
			AllowSending: true,
			AccountType:  "external", // External accounts have special rules
		},
		{
			ID:             "bal-cust-001",
			Alias:          "customer@wallet.com",
			AssetCode:      "USD",
			Available:      50000, // $500 current balance
			Scale:          2,
			AllowReceiving: true,
			AccountType:    "standard",
		},
		{
			ID:             "bal-fee-001",
			Alias:          "bank@fees.com",
			AssetCode:      "USD",
			Available:      0,
			Scale:          2,
			AllowReceiving: true,
			AccountType:    "standard",
		},
	}

	// Validate transaction with balance rules
	ctx := context.Background()
	responses, err := transaction.ValidateSendSourceAndDistribute(withdrawalTx)
	if err != nil {
		fmt.Printf("❌ Transaction validation failed: %v\n", err)
		return
	}

	err = transaction.ValidateBalancesRules(ctx, withdrawalTx, *responses, balances)
	if err != nil {
		fmt.Printf("❌ Balance validation failed: %v\n", err)
		return
	}

	fmt.Println("✅ External account transaction validated successfully")
	fmt.Printf("   Withdrawal Amount: $%.2f\n", float64(withdrawalTx.Send.Value)/100)
	fmt.Printf("   Customer Receives: $%.2f\n",
		float64(withdrawalTx.Send.Distribute.To[0].Amount.Value)/100)
	fmt.Printf("   Bank Fee: $%.2f\n",
		float64(withdrawalTx.Send.Distribute.To[1].Amount.Value)/100)

	processTransaction(ctx, withdrawalTx, "Bank Withdrawal")
}

// transactionValidationExample demonstrates comprehensive validation
func transactionValidationExample() {
	fmt.Println("Demonstrating transaction validation...")

	// Example 1: Invalid transaction (unbalanced)
	fmt.Println("\n📋 Testing invalid transaction (unbalanced):")
	invalidTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 10000, // $100.00
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "alice@test.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 10000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "bob@test.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 9000, // Only $90 - unbalanced!
							Scale: 2,
						},
					},
				},
			},
		},
	}

	_, err := transaction.ValidateSendSourceAndDistribute(invalidTx)
	if err != nil {
		fmt.Printf("✅ Correctly detected invalid transaction: %v\n", err)
	} else {
		fmt.Println("❌ Failed to detect invalid transaction")
	}

	// Example 2: Valid transaction with proper validation
	fmt.Println("\n📋 Testing valid transaction:")
	validTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 10000,
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "alice@test.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 10000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "bob@test.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 10000, // Properly balanced
							Scale: 2,
						},
					},
				},
			},
		},
	}

	responses, err := transaction.ValidateSendSourceAndDistribute(validTx)
	if err != nil {
		fmt.Printf("❌ Valid transaction failed validation: %v\n", err)
	} else {
		fmt.Printf("✅ Valid transaction passed validation\n")
		fmt.Printf("   Total: $%.2f\n", float64(responses.Total)/100)
		fmt.Printf("   Sources: %v\n", responses.Sources)
		fmt.Printf("   Destinations: %v\n", responses.Destinations)
	}
}

// doubleEntryAccountingExample demonstrates double-entry principles
func doubleEntryAccountingExample() {
	fmt.Println("Demonstrating double-entry accounting principles...")

	// In double-entry accounting, every transaction affects at least two accounts
	// and the total debits must equal the total credits

	// Example: Business expense payment
	// - Decrease Cash account (credit)
	// - Increase Expense account (debit)
	expenseTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 25000, // $250.00 office supplies
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "business@cash-account.com", // Cash decreases (credit)
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 25000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "business@office-supplies-expense.com", // Expense increases (debit)
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 25000,
							Scale: 2,
						},
					},
				},
			},
		},
		Metadata: map[string]interface{}{
			"transaction_type": "expense",
			"category":         "office_supplies",
			"description":      "Monthly office supplies purchase",
			"vendor":           "Office Supply Co.",
			"invoice_number":   "INV-2024-001",
		},
	}

	// Validate double-entry principles
	responses, err := transaction.ValidateSendSourceAndDistribute(expenseTx)
	if err != nil {
		fmt.Printf("❌ Double-entry validation failed: %v\n", err)
		return
	}

	fmt.Println("✅ Double-entry accounting validated")
	fmt.Printf("   Debit (Expense): $%.2f\n", float64(responses.Total)/100)
	fmt.Printf("   Credit (Cash): $%.2f\n", float64(responses.Total)/100)
	fmt.Printf("   Balance: $%.2f (must be 0)\n", 0.0) // Always balanced

	// Example: Revenue recognition
	// - Increase Cash account (debit)
	// - Increase Revenue account (credit)
	revenueTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 100000, // $1,000.00 service revenue
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "customer@payment.com", // Customer pays (decreases their cash)
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 100000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "business@cash-account.com", // Cash increases (debit)
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 100000,
							Scale: 2,
						},
					},
				},
			},
		},
		Metadata: map[string]interface{}{
			"transaction_type": "revenue",
			"category":         "service_revenue",
			"description":      "Consulting services rendered",
			"customer":         "ACME Corp",
			"invoice_number":   "INV-2024-002",
		},
	}

	fmt.Println("\n💰 Revenue transaction example:")
	processTransaction(context.Background(), revenueTx, "Service Revenue")
}

// balanceOperationsExample demonstrates balance management
func balanceOperationsExample() {
	fmt.Println("Demonstrating balance operations...")

	// Create sample balances
	balances := []*transaction.Balance{
		{
			ID:             "bal-001",
			Alias:          "customer@checking.com",
			AssetCode:      "USD",
			Available:      150000, // $1,500.00
			Scale:          2,
			OnHold:         5000, // $50.00 on hold
			AllowSending:   true,
			AllowReceiving: true,
			AccountType:    "standard",
		},
		{
			ID:             "bal-002",
			Alias:          "merchant@account.com",
			AssetCode:      "USD",
			Available:      75000, // $750.00
			Scale:          2,
			OnHold:         0,
			AllowSending:   true,
			AllowReceiving: true,
			AccountType:    "standard",
		},
	}

	fmt.Println("📊 Initial balances:")
	for _, balance := range balances {
		fmt.Printf("   %s: $%.2f available, $%.2f on hold\n",
			balance.Alias,
			float64(balance.Available)/100,
			float64(balance.OnHold)/100)
	}

	// Create transaction
	purchaseTx := transaction.Transaction{
		Send: transaction.Send{
			Asset: "USD",
			Value: 8000, // $80.00
			Scale: 2,
			Source: transaction.Source{
				From: []transaction.FromTo{
					{
						Account: "customer@checking.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 8000,
							Scale: 2,
						},
					},
				},
			},
			Distribute: transaction.Distribute{
				To: []transaction.FromTo{
					{
						Account: "merchant@account.com",
						Amount: &transaction.Amount{
							Asset: "USD",
							Value: 8000,
							Scale: 2,
						},
					},
				},
			},
		},
	}

	// Validate with current balances
	ctx := context.Background()
	responses, err := transaction.ValidateSendSourceAndDistribute(purchaseTx)
	if err != nil {
		fmt.Printf("❌ Transaction validation failed: %v\n", err)
		return
	}

	err = transaction.ValidateBalancesRules(ctx, purchaseTx, *responses, balances)
	if err != nil {
		fmt.Printf("❌ Balance validation failed: %v\n", err)
		return
	}

	fmt.Println("\n✅ Transaction validated against balances")
	fmt.Printf("   Transaction: $%.2f\n", float64(purchaseTx.Send.Value)/100)

	// Calculate new balances after transaction
	fmt.Println("\n📊 Balances after transaction:")
	for _, balance := range balances {
		newBalance := *balance // Copy

		if balance.Alias == "customer@checking.com" {
			newBalance.Available -= 8000 // Decrease customer balance
		} else if balance.Alias == "merchant@account.com" {
			newBalance.Available += 8000 // Increase merchant balance
		}

		fmt.Printf("   %s: $%.2f available (was $%.2f)\n",
			newBalance.Alias,
			float64(newBalance.Available)/100,
			float64(balance.Available)/100)
	}
}

// financialReportingExample demonstrates reporting and audit capabilities
func financialReportingExample() {
	fmt.Println("Demonstrating financial reporting...")

	// Sample transactions for reporting
	transactions := []transaction.Transaction{
		// Revenue transaction
		{
			Send: transaction.Send{
				Asset: "USD",
				Value: 50000,
				Scale: 2,
				Source: transaction.Source{
					From: []transaction.FromTo{{
						Account: "customer@corp.com",
						Amount:  &transaction.Amount{Asset: "USD", Value: 50000, Scale: 2},
					}},
				},
				Distribute: transaction.Distribute{
					To: []transaction.FromTo{{
						Account: "business@revenue.com",
						Amount:  &transaction.Amount{Asset: "USD", Value: 50000, Scale: 2},
					}},
				},
			},
			Metadata: map[string]interface{}{
				"type":     "revenue",
				"category": "consulting",
				"date":     "2024-12-01",
			},
		},
		// Expense transaction
		{
			Send: transaction.Send{
				Asset: "USD",
				Value: 15000,
				Scale: 2,
				Source: transaction.Source{
					From: []transaction.FromTo{{
						Account: "business@cash.com",
						Amount:  &transaction.Amount{Asset: "USD", Value: 15000, Scale: 2},
					}},
				},
				Distribute: transaction.Distribute{
					To: []transaction.FromTo{{
						Account: "business@expenses.com",
						Amount:  &transaction.Amount{Asset: "USD", Value: 15000, Scale: 2},
					}},
				},
			},
			Metadata: map[string]interface{}{
				"type":     "expense",
				"category": "marketing",
				"date":     "2024-12-01",
			},
		},
	}

	// Generate financial report
	fmt.Println("\n📈 Financial Report Summary:")

	totalRevenue := int64(0)
	totalExpenses := int64(0)

	for _, tx := range transactions {
		txType := tx.Metadata["type"].(string)
		category := tx.Metadata["category"].(string)
		amount := float64(tx.Send.Value) / 100

		fmt.Printf("   %s (%s): $%.2f\n", txType, category, amount)

		if txType == "revenue" {
			totalRevenue += tx.Send.Value
		} else if txType == "expense" {
			totalExpenses += tx.Send.Value
		}
	}

	netIncome := totalRevenue - totalExpenses

	fmt.Println("\n💰 Summary:")
	fmt.Printf("   Total Revenue: $%.2f\n", float64(totalRevenue)/100)
	fmt.Printf("   Total Expenses: $%.2f\n", float64(totalExpenses)/100)
	fmt.Printf("   Net Income: $%.2f\n", float64(netIncome)/100)

	// Audit trail information
	fmt.Println("\n🔍 Audit Trail Features:")
	fmt.Println("   • Immutable transaction records")
	fmt.Println("   • Complete metadata tracking")
	fmt.Println("   • Balance validation at each step")
	fmt.Println("   • Double-entry accounting compliance")
	fmt.Println("   • Timestamp and user tracking")
	fmt.Println("   • Cryptographic transaction hashing")
}

// processTransaction simulates transaction processing
func processTransaction(ctx context.Context, tx transaction.Transaction, description string) {
	fmt.Printf("🔄 Processing: %s\n", description)

	// In a real system, this would:
	// 1. Validate the transaction
	// 2. Check account balances
	// 3. Apply business rules
	// 4. Update account balances atomically
	// 5. Record in audit log
	// 6. Send notifications

	// Simulate processing time
	time.Sleep(10 * time.Millisecond)

	// Validate transaction structure
	responses, err := transaction.ValidateSendSourceAndDistribute(tx)
	if err != nil {
		fmt.Printf("❌ Transaction processing failed: %v\n", err)
		return
	}

	fmt.Printf("✅ Transaction processed successfully (Amount: $%.2f)\n",
		float64(responses.Total)/100)

	// Log transaction details for audit
	if tx.Metadata != nil {
		fmt.Printf("   Metadata: %v\n", tx.Metadata)
	}
}
