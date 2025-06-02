// Package main demonstrates advanced saga pattern error recovery with retry policies,
// compensation timeouts, circuit breakers, and comprehensive error handling.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/commons/saga"
)

func main() {
	fmt.Println("Saga Pattern Error Recovery Demo")
	fmt.Println("===============================")

	// Example 1: Basic error recovery with retries
	fmt.Println("\n1. Basic Error Recovery with Retries")
	basicErrorRecoveryExample()

	// Example 2: Custom error classification
	fmt.Println("\n2. Custom Error Classification")
	customErrorClassificationExample()

	// Example 3: Circuit breaker integration
	fmt.Println("\n3. Circuit Breaker Integration")
	circuitBreakerExample()

	// Example 4: Compensation with timeouts
	fmt.Println("\n4. Compensation with Timeouts")
	compensationTimeoutExample()

	// Example 5: Partial compensation mode
	fmt.Println("\n5. Partial Compensation Mode")
	partialCompensationExample()

	// Example 6: Real-world e-commerce scenario
	fmt.Println("\n6. Real-World E-commerce Scenario")
	ecommerceScenarioExample()

	// Example 7: Dead letter queue and metrics
	fmt.Println("\n7. Dead Letter Queue and Metrics")
	deadLetterQueueExample()
}

// basicErrorRecoveryExample demonstrates basic error recovery with retries
func basicErrorRecoveryExample() {
	fmt.Println("Setting up saga with error recovery...")

	// Create recovery policy
	policy := saga.RecoveryPolicy{
		MaxRetries:              3,
		InitialRetryDelay:       500 * time.Millisecond,
		MaxRetryDelay:           5 * time.Second,
		RetryDelayMultiplier:    2.0,
		RetryJitter:             true,
		CompensationTimeout:     30 * time.Second,
		CompensationRetries:     2,
		PartialCompensationMode: true,
		AbortOnCriticalError:    false,
		DeadLetterQueue:         true,
	}

	// Create recoverable saga
	recoverableSaga := saga.NewRecoverableSaga("payment-processing", policy)

	// Add steps with potential failures
	recoverableSaga.AddStep(&UnreliablePaymentStep{failureRate: 0.7})
	recoverableSaga.AddStep(&UnreliableInventoryStep{failureRate: 0.3})
	recoverableSaga.AddStep(&UnreliableShippingStep{failureRate: 0.2})

	// Execute saga
	ctx := context.Background()
	orderData := map[string]interface{}{
		"order_id":      "order-12345",
		"customer_id":   "customer-67890",
		"amount":        100.00,
		"items":         []string{"item-1", "item-2"},
		"shipping_addr": "123 Main St",
	}

	fmt.Printf("🔄 Executing saga with order data: %+v\n", orderData)

	start := time.Now()
	err := recoverableSaga.Execute(ctx, orderData)
	duration := time.Since(start)

	if err != nil {
		fmt.Printf("❌ Saga execution failed after %v: %v\n", duration, err)
	} else {
		fmt.Printf("✅ Saga completed successfully in %v\n", duration)
	}

	// Display recovery metrics
	metrics := recoverableSaga.GetRecoveryMetrics()
	fmt.Printf("📊 Recovery Metrics:\n")
	fmt.Printf("   Total Attempts: %d\n", metrics.TotalAttempts)
	fmt.Printf("   Successful Retries: %d\n", metrics.SuccessfulRetries)
	fmt.Printf("   Failed Retries: %d\n", metrics.FailedRetries)
	fmt.Printf("   Dead Letter Count: %d\n", metrics.DeadLetterCount)
	fmt.Printf("   Error Distribution: %+v\n", metrics.ErrorDistribution)

	fmt.Println("✅ Basic error recovery example completed")
}

// customErrorClassificationExample demonstrates custom error classification
func customErrorClassificationExample() {
	fmt.Println("Setting up saga with custom error classification...")

	// Create custom error classifier
	customClassifier := &EcommerceErrorClassifier{}

	// Create recovery policy
	policy := saga.RecoveryPolicy{
		MaxRetries:              2,
		InitialRetryDelay:       1 * time.Second,
		RetryDelayMultiplier:    1.5,
		CompensationTimeout:     20 * time.Second,
		PartialCompensationMode: true,
		CriticalErrorCodes:      []string{"PAYMENT_FRAUD", "ACCOUNT_SUSPENDED"},
		DeadLetterQueue:         true,
	}

	// Create saga with custom classifier
	recoverableSaga := saga.NewRecoverableSaga("custom-classification", policy)
	recoverableSaga.SetErrorClassifier(customClassifier)

	// Add steps that generate different types of errors
	recoverableSaga.AddStep(&ClassifiedPaymentStep{})
	recoverableSaga.AddStep(&ClassifiedInventoryStep{})
	recoverableSaga.AddStep(&ClassifiedFraudCheckStep{})

	// Execute saga
	ctx := context.Background()
	orderData := map[string]interface{}{
		"order_id":      "order-67890",
		"amount":        250.00,
		"risk_score":    0.8,
		"customer_tier": "premium",
	}

	fmt.Printf("🔄 Executing saga with custom error classification\n")

	err := recoverableSaga.Execute(ctx, orderData)

	if err != nil {
		fmt.Printf("❌ Saga failed: %v\n", err)

		// Check error type
		if recErr, ok := err.(*saga.RecoverableError); ok {
			fmt.Printf("   Error Classification: %s\n", recErr.Classification)
			fmt.Printf("   Recovery Strategy: %s\n", recErr.RecoveryStrategy)
			fmt.Printf("   Retryable: %t\n", recErr.Retryable)
		}
	} else {
		fmt.Printf("✅ Saga completed successfully\n")
	}

	fmt.Println("✅ Custom error classification example completed")
}

// circuitBreakerExample demonstrates circuit breaker integration
func circuitBreakerExample() {
	fmt.Println("Setting up saga with circuit breaker protection...")

	// Create policy with circuit breaker enabled
	policy := saga.RecoveryPolicy{
		MaxRetries:              5,
		InitialRetryDelay:       200 * time.Millisecond,
		CircuitBreakerEnabled:   true,
		CircuitBreakerThreshold: 3,
		DeadLetterQueue:         true,
	}

	// Create saga
	recoverableSaga := saga.NewRecoverableSaga("circuit-breaker-demo", policy)

	// Add a consistently failing step to trigger circuit breaker
	recoverableSaga.AddStep(&ConsistentlyFailingStep{name: "external-api-call"})
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "internal-process"})

	// Try to execute saga multiple times to trigger circuit breaker
	ctx := context.Background()
	data := map[string]interface{}{"test": true}

	for attempt := 1; attempt <= 10; attempt++ {
		fmt.Printf("\n🔄 Attempt %d:\n", attempt)

		err := recoverableSaga.Execute(ctx, data)
		if err != nil {
			fmt.Printf("❌ Attempt %d failed: %v\n", attempt, err)
		} else {
			fmt.Printf("✅ Attempt %d succeeded\n", attempt)
		}

		// Check circuit breaker status
		status := recoverableSaga.GetRecoveryMetrics().CircuitBreakerStats
		for stepName, state := range status {
			fmt.Printf("   Circuit Breaker [%s]: %s\n", stepName, state)
		}

		// Add delay between attempts
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Println("✅ Circuit breaker example completed")
}

// compensationTimeoutExample demonstrates compensation with timeouts
func compensationTimeoutExample() {
	fmt.Println("Setting up saga with compensation timeouts...")

	// Create policy with short compensation timeout
	policy := saga.RecoveryPolicy{
		MaxRetries:          1,
		CompensationTimeout: 2 * time.Second,
		CompensationRetries: 1,
		DeadLetterQueue:     true,
	}

	// Create saga
	recoverableSaga := saga.NewRecoverableSaga("compensation-timeout", policy)

	// Add steps where one will fail and require compensation
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "step-1"})
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "step-2"})
	recoverableSaga.AddStep(&AlwaysFailingStep{name: "step-3"}) // This will fail
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "step-4"})

	// Replace step-2 with one that has slow compensation
	recoverableSaga = saga.NewRecoverableSaga("compensation-timeout", policy)
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "step-1"})
	recoverableSaga.AddStep(
		&SlowCompensationStep{name: "step-2", compensationDelay: 3 * time.Second},
	)
	recoverableSaga.AddStep(&AlwaysFailingStep{name: "step-3"}) // This will fail

	// Execute saga
	ctx := context.Background()
	data := map[string]interface{}{"test": "compensation-timeout"}

	fmt.Printf("🔄 Executing saga that will require compensation...\n")

	start := time.Now()
	err := recoverableSaga.Execute(ctx, data)
	duration := time.Since(start)

	if err != nil {
		fmt.Printf("❌ Saga failed after %v: %v\n", duration, err)
	} else {
		fmt.Printf("✅ Saga completed in %v\n", duration)
	}

	fmt.Println("✅ Compensation timeout example completed")
}

// partialCompensationExample demonstrates partial compensation mode
func partialCompensationExample() {
	fmt.Println("Setting up saga with partial compensation mode...")

	// Create policy with partial compensation enabled
	policy := saga.RecoveryPolicy{
		MaxRetries:              1,
		CompensationTimeout:     5 * time.Second,
		CompensationRetries:     1,
		PartialCompensationMode: true,
		DeadLetterQueue:         true,
	}

	// Create saga
	recoverableSaga := saga.NewRecoverableSaga("partial-compensation", policy)

	// Add steps where some will succeed and others will fail compensation
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "reserve-inventory"})
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "charge-payment"})
	recoverableSaga.AddStep(&FailingCompensationStep{name: "send-notification"})
	recoverableSaga.AddStep(&SimpleSuccessStep{name: "update-analytics"})
	recoverableSaga.AddStep(
		&AlwaysFailingStep{name: "external-webhook"},
	) // This will trigger compensation

	// Execute saga
	ctx := context.Background()
	data := map[string]interface{}{
		"order_id": "order-partial-comp",
		"amount":   75.00,
	}

	fmt.Printf("🔄 Executing saga with partial compensation scenario...\n")

	err := recoverableSaga.Execute(ctx, data)

	if err != nil {
		fmt.Printf("❌ Saga failed: %v\n", err)

		// Show partial compensation results
		fmt.Printf("📋 Saga Status:\n")
		fmt.Printf("   Status: %s\n", recoverableSaga.Status())
		fmt.Printf("   Completed Steps: %v\n", recoverableSaga.GetCompletedSteps())
		fmt.Printf("   Compensated Steps: %v\n", recoverableSaga.GetCompensatedSteps())
	}

	fmt.Println("✅ Partial compensation example completed")
}

// ecommerceScenarioExample demonstrates a comprehensive e-commerce scenario
func ecommerceScenarioExample() {
	fmt.Println("Setting up comprehensive e-commerce saga...")

	// Create realistic recovery policy
	policy := saga.RecoveryPolicy{
		MaxRetries:              3,
		InitialRetryDelay:       1 * time.Second,
		MaxRetryDelay:           10 * time.Second,
		RetryDelayMultiplier:    2.0,
		RetryJitter:             true,
		CompensationTimeout:     1 * time.Minute,
		CompensationRetries:     2,
		PartialCompensationMode: true,
		AbortOnCriticalError:    true,
		CriticalErrorCodes:      []string{"FRAUD_DETECTED", "ACCOUNT_BANNED"},
		CircuitBreakerEnabled:   true,
		CircuitBreakerThreshold: 5,
		DeadLetterQueue:         true,
	}

	// Create e-commerce saga
	ecommerceSaga := saga.NewRecoverableSaga("ecommerce-order-processing", policy)

	// Add realistic e-commerce steps
	ecommerceSaga.AddStep(&ValidateOrderStep{})
	ecommerceSaga.AddStep(&CheckInventoryStep{})
	ecommerceSaga.AddStep(&ReserveInventoryStep{})
	ecommerceSaga.AddStep(&ProcessPaymentStep{})
	ecommerceSaga.AddStep(&CreateShipmentStep{})
	ecommerceSaga.AddStep(&SendConfirmationEmailStep{})
	ecommerceSaga.AddStep(&UpdateAnalyticsStep{})

	// Execute with realistic order data
	ctx := context.Background()
	orderData := map[string]interface{}{
		"order_id":       "ORD-2024-001",
		"customer_id":    "CUST-12345",
		"customer_email": "customer@example.com",
		"items": []map[string]interface{}{
			{"sku": "PROD-001", "quantity": 2, "price": 29.99},
			{"sku": "PROD-002", "quantity": 1, "price": 49.99},
		},
		"total_amount":   109.97,
		"payment_method": "credit_card",
		"shipping_address": map[string]string{
			"street": "123 Main St",
			"city":   "Anytown",
			"zip":    "12345",
		},
		"priority": "standard",
	}

	fmt.Printf("🛒 Processing e-commerce order: %s\n", orderData["order_id"])
	fmt.Printf("   Customer: %s\n", orderData["customer_id"])
	fmt.Printf("   Total: $%.2f\n", orderData["total_amount"])
	fmt.Printf("   Items: %d\n", len(orderData["items"].([]map[string]interface{})))

	start := time.Now()
	err := ecommerceSaga.Execute(ctx, orderData)
	duration := time.Since(start)

	if err != nil {
		fmt.Printf("❌ Order processing failed after %v: %v\n", duration, err)
		fmt.Printf("   Order Status: Failed with partial completion\n")
	} else {
		fmt.Printf("✅ Order processed successfully in %v\n", duration)
		fmt.Printf("   Order Status: Completed\n")
	}

	// Show detailed metrics
	metrics := ecommerceSaga.GetRecoveryMetrics()
	fmt.Printf("\n📊 Detailed Processing Metrics:\n")
	fmt.Printf("   Total Processing Attempts: %d\n", metrics.TotalAttempts)
	fmt.Printf("   Successful Retries: %d\n", metrics.SuccessfulRetries)
	fmt.Printf("   Failed Operations: %d\n", metrics.FailedRetries)
	fmt.Printf("   Compensation Attempts: %d\n", metrics.CompensationAttempts)
	fmt.Printf("   Error Classifications: %+v\n", metrics.ErrorDistribution)
	fmt.Printf("   Circuit Breaker Status: %+v\n", metrics.CircuitBreakerStats)

	fmt.Println("✅ E-commerce scenario example completed")
}

// deadLetterQueueExample demonstrates dead letter queue and monitoring
func deadLetterQueueExample() {
	fmt.Println("Setting up saga with dead letter queue monitoring...")

	// Create policy with dead letter queue
	policy := saga.RecoveryPolicy{
		MaxRetries:        2,
		InitialRetryDelay: 100 * time.Millisecond,
		DeadLetterQueue:   true,
	}

	// Create saga
	recoverableSaga := saga.NewRecoverableSaga("dead-letter-demo", policy)

	// Add steps that will generate various errors
	recoverableSaga.AddStep(&ErrorGeneratingStep{errorType: "transient"})
	recoverableSaga.AddStep(&ErrorGeneratingStep{errorType: "permanent"})
	recoverableSaga.AddStep(&ErrorGeneratingStep{errorType: "critical"})

	// Execute saga multiple times to populate dead letter queue
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		fmt.Printf("\n🔄 Execution %d:\n", i)

		data := map[string]interface{}{
			"execution_id": i,
			"test_mode":    true,
		}

		err := recoverableSaga.Execute(ctx, data)
		if err != nil {
			fmt.Printf("❌ Execution %d failed: %v\n", i, err)
		} else {
			fmt.Printf("✅ Execution %d succeeded\n", i)
		}
	}

	// Show dead letter queue contents
	fmt.Printf("\n📬 Dead Letter Queue Analysis:\n")
	deadLetterQueue := recoverableSaga.GetRecoveryMetrics()
	fmt.Printf("   Dead Letter Count: %d\n", deadLetterQueue.DeadLetterCount)
	fmt.Printf("   Error Distribution: %+v\n", deadLetterQueue.ErrorDistribution)

	// Show recovery metrics summary
	fmt.Printf("\n📈 Recovery Summary:\n")
	fmt.Printf("   Total Attempts: %d\n", deadLetterQueue.TotalAttempts)
	fmt.Printf("   Success Rate: %.2f%%\n",
		float64(deadLetterQueue.SuccessfulRetries)/float64(deadLetterQueue.TotalAttempts)*100)

	fmt.Println("✅ Dead letter queue example completed")
}

// Step implementations for examples

// UnreliablePaymentStep simulates an unreliable payment processing step
type UnreliablePaymentStep struct {
	failureRate float64
}

func (s *UnreliablePaymentStep) Name() string { return "process-payment" }

func (s *UnreliablePaymentStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   💳 Processing payment...\n")

	// Simulate processing time
	time.Sleep(100 * time.Millisecond)

	// Randomly fail based on failure rate
	if secureFloat64() < s.failureRate {
		return errors.New("payment service temporarily unavailable")
	}

	fmt.Printf("   ✅ Payment processed successfully\n")
	return nil
}

func (s *UnreliablePaymentStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   💳 Refunding payment...\n")
	time.Sleep(50 * time.Millisecond)
	fmt.Printf("   ✅ Payment refunded\n")
	return nil
}

// UnreliableInventoryStep simulates inventory management
type UnreliableInventoryStep struct {
	failureRate float64
}

func (s *UnreliableInventoryStep) Name() string { return "reserve-inventory" }

func (s *UnreliableInventoryStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   📦 Reserving inventory...\n")
	time.Sleep(75 * time.Millisecond)

	if secureFloat64() < s.failureRate {
		return errors.New("insufficient inventory")
	}

	fmt.Printf("   ✅ Inventory reserved\n")
	return nil
}

func (s *UnreliableInventoryStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   📦 Releasing inventory reservation...\n")
	time.Sleep(25 * time.Millisecond)
	fmt.Printf("   ✅ Inventory released\n")
	return nil
}

// UnreliableShippingStep simulates shipping service
type UnreliableShippingStep struct {
	failureRate float64
}

func (s *UnreliableShippingStep) Name() string { return "create-shipment" }

func (s *UnreliableShippingStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   🚚 Creating shipment...\n")
	time.Sleep(120 * time.Millisecond)

	if secureFloat64() < s.failureRate {
		return errors.New("shipping service rate limit exceeded")
	}

	fmt.Printf("   ✅ Shipment created\n")
	return nil
}

func (s *UnreliableShippingStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   🚚 Canceling shipment...\n")
	time.Sleep(30 * time.Millisecond)
	fmt.Printf("   ✅ Shipment canceled\n")
	return nil
}

// EcommerceErrorClassifier provides custom error classification for e-commerce
type EcommerceErrorClassifier struct{}

func (ec *EcommerceErrorClassifier) ClassifyError(
	err error,
	stepName string,
	attemptNumber int,
) *saga.RecoverableError {
	errorStr := err.Error()

	var classification saga.ErrorClassification
	var retryable bool
	var retryAfter time.Duration
	var strategy string

	// Classify based on error content and step
	switch {
	case contains(errorStr, "fraud") || contains(errorStr, "suspicious"):
		classification = saga.ErrorCritical
		retryable = false
		strategy = "abort_immediately"
	case contains(errorStr, "payment declined") || contains(errorStr, "insufficient funds"):
		classification = saga.ErrorPermanent
		retryable = false
		strategy = "notify_customer"
	case contains(errorStr, "inventory") && contains(errorStr, "insufficient"):
		classification = saga.ErrorPermanent
		retryable = false
		strategy = "suggest_alternatives"
	case contains(errorStr, "rate limit") || contains(errorStr, "quota"):
		classification = saga.ErrorResourceLimit
		retryable = true
		retryAfter = time.Duration(attemptNumber*2) * time.Second
		strategy = "exponential_backoff"
	case contains(errorStr, "network") || contains(errorStr, "timeout"):
		classification = saga.ErrorTransient
		retryable = true
		retryAfter = 1 * time.Second
		strategy = "immediate_retry"
	default:
		classification = saga.ErrorTransient
		retryable = true
		retryAfter = 500 * time.Millisecond
		strategy = "default_retry"
	}

	return &saga.RecoverableError{
		OriginalError:    err,
		Classification:   classification,
		Retryable:        retryable,
		RetryAfter:       retryAfter,
		Context:          map[string]interface{}{"step_type": stepName},
		OccurredAt:       time.Now(),
		StepName:         stepName,
		AttemptNumber:    attemptNumber,
		RecoveryStrategy: strategy,
	}
}

// Additional step implementations for comprehensive examples

type ClassifiedPaymentStep struct{}

func (s *ClassifiedPaymentStep) Name() string { return "classified-payment" }
func (s *ClassifiedPaymentStep) Execute(ctx context.Context, data interface{}) error {
	// Simulate different types of payment errors
	errorTypes := []string{
		"payment service temporarily unavailable",
		"payment declined by bank",
		"fraud detected in transaction",
		"rate limit exceeded for payment processor",
	}
	return errors.New(errorTypes[secureIntn(len(errorTypes))])
}
func (s *ClassifiedPaymentStep) Compensate(ctx context.Context, data interface{}) error {
	return nil
}

type ClassifiedInventoryStep struct{}

func (s *ClassifiedInventoryStep) Name() string { return "classified-inventory" }
func (s *ClassifiedInventoryStep) Execute(ctx context.Context, data interface{}) error {
	errorTypes := []string{
		"insufficient inventory for item",
		"inventory service network timeout",
		"inventory database connection lost",
	}
	return errors.New(errorTypes[secureIntn(len(errorTypes))])
}
func (s *ClassifiedInventoryStep) Compensate(ctx context.Context, data interface{}) error {
	return nil
}

type ClassifiedFraudCheckStep struct{}

func (s *ClassifiedFraudCheckStep) Name() string { return "fraud-check" }
func (s *ClassifiedFraudCheckStep) Execute(ctx context.Context, data interface{}) error {
	if secureFloat64() < 0.3 {
		return errors.New("suspicious activity detected - fraud investigation required")
	}
	return nil
}
func (s *ClassifiedFraudCheckStep) Compensate(ctx context.Context, data interface{}) error {
	return nil
}

type ConsistentlyFailingStep struct{ name string }

func (s *ConsistentlyFailingStep) Name() string { return s.name }
func (s *ConsistentlyFailingStep) Execute(ctx context.Context, data interface{}) error {
	return errors.New("external service permanently down")
}
func (s *ConsistentlyFailingStep) Compensate(ctx context.Context, data interface{}) error {
	return nil
}

type SimpleSuccessStep struct{ name string }

func (s *SimpleSuccessStep) Name() string { return s.name }
func (s *SimpleSuccessStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   ✅ %s completed\n", s.name)
	return nil
}
func (s *SimpleSuccessStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   ↩️  %s compensated\n", s.name)
	return nil
}

type AlwaysFailingStep struct{ name string }

func (s *AlwaysFailingStep) Name() string { return s.name }
func (s *AlwaysFailingStep) Execute(ctx context.Context, data interface{}) error {
	return errors.New("step designed to fail")
}
func (s *AlwaysFailingStep) Compensate(ctx context.Context, data interface{}) error {
	return nil
}

type SlowCompensationStep struct {
	name              string
	compensationDelay time.Duration
}

func (s *SlowCompensationStep) Name() string { return s.name }
func (s *SlowCompensationStep) Execute(ctx context.Context, data interface{}) error {
	return nil
}
func (s *SlowCompensationStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   ⏳ %s compensation (slow)...\n", s.name)
	time.Sleep(s.compensationDelay)
	fmt.Printf("   ✅ %s compensation completed\n", s.name)
	return nil
}

type FailingCompensationStep struct{ name string }

func (s *FailingCompensationStep) Name() string { return s.name }
func (s *FailingCompensationStep) Execute(ctx context.Context, data interface{}) error {
	return nil
}
func (s *FailingCompensationStep) Compensate(ctx context.Context, data interface{}) error {
	return errors.New("compensation failed - external service unavailable")
}

type ErrorGeneratingStep struct{ errorType string }

func (s *ErrorGeneratingStep) Name() string { return "error-generator-" + s.errorType }
func (s *ErrorGeneratingStep) Execute(ctx context.Context, data interface{}) error {
	switch s.errorType {
	case "transient":
		return errors.New("network timeout occurred")
	case "permanent":
		return errors.New("invalid request format")
	case "critical":
		return errors.New("fatal system error detected")
	default:
		return errors.New("unknown error")
	}
}
func (s *ErrorGeneratingStep) Compensate(ctx context.Context, data interface{}) error {
	return nil
}

// E-commerce specific steps

type ValidateOrderStep struct{}

func (s *ValidateOrderStep) Name() string { return "validate-order" }
func (s *ValidateOrderStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   📋 Validating order...\n")
	time.Sleep(50 * time.Millisecond)

	if secureFloat64() < 0.1 {
		return errors.New("invalid order data")
	}

	fmt.Printf("   ✅ Order validated\n")
	return nil
}
func (s *ValidateOrderStep) Compensate(ctx context.Context, data interface{}) error {
	return nil // No compensation needed for validation
}

type CheckInventoryStep struct{}

func (s *CheckInventoryStep) Name() string { return "check-inventory" }
func (s *CheckInventoryStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   📦 Checking inventory availability...\n")
	time.Sleep(100 * time.Millisecond)

	if secureFloat64() < 0.2 {
		return errors.New("insufficient inventory")
	}

	fmt.Printf("   ✅ Inventory available\n")
	return nil
}
func (s *CheckInventoryStep) Compensate(ctx context.Context, data interface{}) error {
	return nil // No compensation needed for check
}

type ReserveInventoryStep struct{}

func (s *ReserveInventoryStep) Name() string { return "reserve-inventory" }
func (s *ReserveInventoryStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   🔒 Reserving inventory...\n")
	time.Sleep(75 * time.Millisecond)

	if secureFloat64() < 0.15 {
		return errors.New("inventory reservation failed")
	}

	fmt.Printf("   ✅ Inventory reserved\n")
	return nil
}
func (s *ReserveInventoryStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   🔓 Releasing inventory reservation...\n")
	time.Sleep(25 * time.Millisecond)
	fmt.Printf("   ✅ Inventory released\n")
	return nil
}

type ProcessPaymentStep struct{}

func (s *ProcessPaymentStep) Name() string { return "process-payment" }
func (s *ProcessPaymentStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   💳 Processing payment...\n")
	time.Sleep(200 * time.Millisecond)

	if secureFloat64() < 0.25 {
		return errors.New("payment processing failed")
	}

	fmt.Printf("   ✅ Payment processed\n")
	return nil
}
func (s *ProcessPaymentStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   💰 Refunding payment...\n")
	time.Sleep(100 * time.Millisecond)
	fmt.Printf("   ✅ Payment refunded\n")
	return nil
}

type CreateShipmentStep struct{}

func (s *CreateShipmentStep) Name() string { return "create-shipment" }
func (s *CreateShipmentStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   🚚 Creating shipment...\n")
	time.Sleep(150 * time.Millisecond)

	if secureFloat64() < 0.1 {
		return errors.New("shipment creation failed")
	}

	fmt.Printf("   ✅ Shipment created\n")
	return nil
}
func (s *CreateShipmentStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   📦 Canceling shipment...\n")
	time.Sleep(50 * time.Millisecond)
	fmt.Printf("   ✅ Shipment canceled\n")
	return nil
}

type SendConfirmationEmailStep struct{}

func (s *SendConfirmationEmailStep) Name() string { return "send-confirmation-email" }
func (s *SendConfirmationEmailStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   📧 Sending confirmation email...\n")
	time.Sleep(30 * time.Millisecond)

	if secureFloat64() < 0.05 {
		return errors.New("email service unavailable")
	}

	fmt.Printf("   ✅ Confirmation email sent\n")
	return nil
}
func (s *SendConfirmationEmailStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   📧 Sending cancellation email...\n")
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("   ✅ Cancellation email sent\n")
	return nil
}

type UpdateAnalyticsStep struct{}

func (s *UpdateAnalyticsStep) Name() string { return "update-analytics" }
func (s *UpdateAnalyticsStep) Execute(ctx context.Context, data interface{}) error {
	fmt.Printf("   📊 Updating analytics...\n")
	time.Sleep(25 * time.Millisecond)

	if secureFloat64() < 0.05 {
		return errors.New("analytics service timeout")
	}

	fmt.Printf("   ✅ Analytics updated\n")
	return nil
}
func (s *UpdateAnalyticsStep) Compensate(ctx context.Context, data interface{}) error {
	fmt.Printf("   📊 Removing analytics entry...\n")
	time.Sleep(15 * time.Millisecond)
	fmt.Printf("   ✅ Analytics entry removed\n")
	return nil
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findInString(s, substr)
}

func findInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Secure random number generator helpers

// secureFloat64 generates a secure random float64 between 0.0 and 1.0
func secureFloat64() float64 {
	var buf [8]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		// Fallback to timestamp-based seed for demo code
		return float64(time.Now().UnixNano()%1000) / 1000.0
	}
	// Convert to float64 between 0.0 and 1.0
	return float64(binary.BigEndian.Uint64(buf[:])) / float64(^uint64(0))
}

// secureIntn generates a secure random integer between 0 and n (exclusive)
func secureIntn(n int) int {
	if n <= 0 {
		return 0
	}
	var buf [4]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		// Fallback to timestamp-based value for demo code
		return int(time.Now().UnixNano()) % n
	}
	return int(binary.BigEndian.Uint32(buf[:])) % n
}
