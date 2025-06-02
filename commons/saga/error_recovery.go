package saga

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"
)

// secureFloat64 generates a secure random float64 between 0.0 and 1.0
func secureFloat64() float64 {
	var buf [8]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		// Fallback to timestamp-based value for production continuity
		return float64(time.Now().UnixNano()%1000) / 1000.0
	}
	return float64(binary.BigEndian.Uint64(buf[:])) / float64(^uint64(0))
}

// RecoveryPolicy defines the error recovery policy for saga execution
type RecoveryPolicy struct {
	MaxRetries              int           `json:"max_retries"`
	InitialRetryDelay       time.Duration `json:"initial_retry_delay"`
	MaxRetryDelay           time.Duration `json:"max_retry_delay"`
	RetryDelayMultiplier    float64       `json:"retry_delay_multiplier"`
	RetryJitter             bool          `json:"retry_jitter"`
	CompensationTimeout     time.Duration `json:"compensation_timeout"`
	CompensationRetries     int           `json:"compensation_retries"`
	PartialCompensationMode bool          `json:"partial_compensation_mode"`
	AbortOnCriticalError    bool          `json:"abort_on_critical_error"`
	CriticalErrorCodes      []string      `json:"critical_error_codes"`
	DeadLetterQueue         bool          `json:"dead_letter_queue"`
	CircuitBreakerEnabled   bool          `json:"circuit_breaker_enabled"`
	CircuitBreakerThreshold int           `json:"circuit_breaker_threshold"`
}

// DefaultRecoveryPolicy returns a default recovery policy
func DefaultRecoveryPolicy() RecoveryPolicy {
	return RecoveryPolicy{
		MaxRetries:              3,
		InitialRetryDelay:       1 * time.Second,
		MaxRetryDelay:           30 * time.Second,
		RetryDelayMultiplier:    2.0,
		RetryJitter:             true,
		CompensationTimeout:     5 * time.Minute,
		CompensationRetries:     2,
		PartialCompensationMode: true,
		AbortOnCriticalError:    true,
		CriticalErrorCodes:      []string{"FATAL", "AUTHORIZATION_FAILED", "INVALID_STATE"},
		DeadLetterQueue:         true,
		CircuitBreakerEnabled:   false,
		CircuitBreakerThreshold: 5,
	}
}

// ErrorClassification represents different types of errors and their recovery strategies
type ErrorClassification string

const (
	ErrorTransient     ErrorClassification = "transient"    // Temporary errors that should be retried
	ErrorPermanent     ErrorClassification = "permanent"    // Permanent errors that should not be retried
	ErrorCritical      ErrorClassification = "critical"     // Critical errors that should abort the saga
	ErrorCompensation  ErrorClassification = "compensation" // Errors during compensation
	ErrorTimeout       ErrorClassification = "timeout"      // Timeout errors
	ErrorResourceLimit ErrorClassification = "resource"     // Resource limit errors
)

// RecoverableError wraps an error with recovery metadata
type RecoverableError struct {
	OriginalError    error                  `json:"original_error"`
	Classification   ErrorClassification    `json:"classification"`
	Retryable        bool                   `json:"retryable"`
	RetryAfter       time.Duration          `json:"retry_after"`
	Context          map[string]interface{} `json:"context"`
	OccurredAt       time.Time              `json:"occurred_at"`
	StepName         string                 `json:"step_name"`
	AttemptNumber    int                    `json:"attempt_number"`
	RecoveryStrategy string                 `json:"recovery_strategy"`
}

// Error implements the error interface
func (re *RecoverableError) Error() string {
	return fmt.Sprintf("%s error in step '%s' (attempt %d): %v",
		re.Classification, re.StepName, re.AttemptNumber, re.OriginalError)
}

// Unwrap returns the original error
func (re *RecoverableError) Unwrap() error {
	return re.OriginalError
}

// ErrorRecoveryManager manages error recovery for saga execution
type ErrorRecoveryManager struct {
	policy          RecoveryPolicy
	errorClassifier ErrorClassifier
	retryManager    *RetryManager
	circuitBreakers map[string]*StepCircuitBreaker
	deadLetterQueue []RecoverableError
	mu              sync.RWMutex
}

// ErrorClassifier defines how to classify errors for recovery
type ErrorClassifier interface {
	ClassifyError(err error, stepName string, attemptNumber int) *RecoverableError
}

// DefaultErrorClassifier provides default error classification
type DefaultErrorClassifier struct{}

// ClassifyError implements ErrorClassifier interface
func (dec *DefaultErrorClassifier) ClassifyError(
	err error,
	stepName string,
	attemptNumber int,
) *RecoverableError {
	classification := ErrorTransient
	retryable := true
	retryAfter := time.Second

	errorStr := err.Error()

	// Classify based on error content
	switch {
	case isTimeoutError(err):
		classification = ErrorTimeout
		retryAfter = 2 * time.Second
	case isResourceLimitError(errorStr):
		classification = ErrorResourceLimit
		retryAfter = 5 * time.Second
	case isCriticalError(errorStr):
		classification = ErrorCritical
		retryable = false
	case isPermanentError(errorStr):
		classification = ErrorPermanent
		retryable = false
	}

	return &RecoverableError{
		OriginalError:    err,
		Classification:   classification,
		Retryable:        retryable,
		RetryAfter:       retryAfter,
		Context:          make(map[string]interface{}),
		OccurredAt:       time.Now(),
		StepName:         stepName,
		AttemptNumber:    attemptNumber,
		RecoveryStrategy: string(classification),
	}
}

// RetryManager handles retry logic for saga steps
type RetryManager struct {
	policy RecoveryPolicy
}

// NewRetryManager creates a new retry manager
func NewRetryManager(policy RecoveryPolicy) *RetryManager {
	return &RetryManager{
		policy: policy,
	}
}

// ExecuteWithRetry executes a step with retry logic
func (rm *RetryManager) ExecuteWithRetry(
	ctx context.Context,
	step Step,
	data interface{},
	classifier ErrorClassifier,
) error {
	var lastError error

	for attempt := 0; attempt <= rm.policy.MaxRetries; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Execute the step
		err := step.Execute(ctx, data)
		if err == nil {
			return nil // Success
		}

		// Classify the error
		recoverableErr := classifier.ClassifyError(err, step.Name(), attempt+1)
		lastError = recoverableErr

		// Check if error should abort retries
		if !recoverableErr.Retryable || recoverableErr.Classification == ErrorCritical {
			return recoverableErr
		}

		// Don't retry on last attempt
		if attempt == rm.policy.MaxRetries {
			break
		}

		// Calculate retry delay
		delay := rm.calculateRetryDelay(attempt, recoverableErr.RetryAfter)

		// Wait before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	return lastError
}

// calculateRetryDelay calculates the delay before the next retry attempt
func (rm *RetryManager) calculateRetryDelay(
	attempt int,
	suggestedDelay time.Duration,
) time.Duration {
	// Start with initial delay or suggested delay
	delay := rm.policy.InitialRetryDelay
	if suggestedDelay > delay {
		delay = suggestedDelay
	}

	// Apply exponential backoff
	if rm.policy.RetryDelayMultiplier > 1.0 {
		multiplier := math.Pow(rm.policy.RetryDelayMultiplier, float64(attempt))
		delay = time.Duration(float64(delay) * multiplier)
	}

	// Apply jitter if enabled
	if rm.policy.RetryJitter {
		jitter := time.Duration(secureFloat64() * float64(delay) * 0.1) // 10% jitter
		delay += jitter
	}

	// Respect maximum delay
	if delay > rm.policy.MaxRetryDelay {
		delay = rm.policy.MaxRetryDelay
	}

	return delay
}

// CompensateWithRetry executes compensation with retry logic
func (rm *RetryManager) CompensateWithRetry(
	ctx context.Context,
	step Step,
	data interface{},
	classifier ErrorClassifier,
) error {
	// Create timeout context for compensation
	compensationCtx, cancel := context.WithTimeout(ctx, rm.policy.CompensationTimeout)
	defer cancel()

	var lastError error

	for attempt := 0; attempt <= rm.policy.CompensationRetries; attempt++ {
		// Check context cancellation
		select {
		case <-compensationCtx.Done():
			if compensationCtx.Err() == context.DeadlineExceeded {
				return fmt.Errorf(
					"compensation timeout for step %s: %w",
					step.Name(),
					ErrSagaTimedOut,
				)
			}
			return compensationCtx.Err()
		default:
		}

		// Execute compensation
		err := step.Compensate(compensationCtx, data)
		if err == nil {
			return nil // Success
		}

		// Classify the compensation error
		recoverableErr := classifier.ClassifyError(err, step.Name(), attempt+1)
		recoverableErr.Classification = ErrorCompensation
		lastError = recoverableErr

		// Don't retry on last attempt
		if attempt == rm.policy.CompensationRetries {
			break
		}

		// Calculate retry delay (shorter for compensation)
		delay := rm.calculateRetryDelay(attempt, time.Second) / 2

		// Wait before retry
		select {
		case <-compensationCtx.Done():
			return compensationCtx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	return lastError
}

// StepCircuitBreaker implements circuit breaker pattern for saga steps
type StepCircuitBreaker struct {
	name            string
	threshold       int
	timeout         time.Duration
	failureCount    int
	lastFailureTime time.Time
	state           CircuitBreakerState
	mu              sync.RWMutex
}

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState string

const (
	CircuitBreakerClosed   CircuitBreakerState = "closed"
	CircuitBreakerOpen     CircuitBreakerState = "open"
	CircuitBreakerHalfOpen CircuitBreakerState = "half-open"
)

// NewStepCircuitBreaker creates a new circuit breaker for a step
func NewStepCircuitBreaker(
	stepName string,
	threshold int,
	timeout time.Duration,
) *StepCircuitBreaker {
	return &StepCircuitBreaker{
		name:      stepName,
		threshold: threshold,
		timeout:   timeout,
		state:     CircuitBreakerClosed,
	}
}

// CanExecute checks if the circuit breaker allows execution
func (cb *StepCircuitBreaker) CanExecute() error {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case CircuitBreakerOpen:
		if time.Since(cb.lastFailureTime) > cb.timeout {
			return nil // Move to half-open (will be done in recordResult)
		}
		return fmt.Errorf("circuit breaker open for step %s", cb.name)
	case CircuitBreakerHalfOpen:
		return nil // Allow one test request
	default:
		return nil // Closed state
	}
}

// RecordSuccess records a successful execution
func (cb *StepCircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	cb.state = CircuitBreakerClosed
}

// RecordFailure records a failed execution
func (cb *StepCircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.failureCount >= cb.threshold {
		cb.state = CircuitBreakerOpen
	}
}

// GetState returns the current state of the circuit breaker
func (cb *StepCircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// NewErrorRecoveryManager creates a new error recovery manager
func NewErrorRecoveryManager(policy RecoveryPolicy) *ErrorRecoveryManager {
	return &ErrorRecoveryManager{
		policy:          policy,
		errorClassifier: &DefaultErrorClassifier{},
		retryManager:    NewRetryManager(policy),
		circuitBreakers: make(map[string]*StepCircuitBreaker),
		deadLetterQueue: make([]RecoverableError, 0),
	}
}

// SetErrorClassifier sets a custom error classifier
func (erm *ErrorRecoveryManager) SetErrorClassifier(classifier ErrorClassifier) {
	erm.errorClassifier = classifier
}

// ExecuteStepWithRecovery executes a step with full error recovery
func (erm *ErrorRecoveryManager) ExecuteStepWithRecovery(
	ctx context.Context,
	step Step,
	data interface{},
) error {
	stepName := step.Name()

	// Check circuit breaker if enabled
	if erm.policy.CircuitBreakerEnabled {
		cb := erm.getOrCreateCircuitBreaker(stepName)
		if err := cb.CanExecute(); err != nil {
			return fmt.Errorf("circuit breaker blocked execution: %w", err)
		}
	}

	// Execute with retry
	err := erm.retryManager.ExecuteWithRetry(ctx, step, data, erm.errorClassifier)

	// Update circuit breaker
	if erm.policy.CircuitBreakerEnabled {
		cb := erm.getOrCreateCircuitBreaker(stepName)
		if err != nil {
			cb.RecordFailure()
		} else {
			cb.RecordSuccess()
		}
	}

	// Handle dead letter queue
	if err != nil && erm.policy.DeadLetterQueue {
		if recoverableErr, ok := err.(*RecoverableError); ok {
			erm.addToDeadLetterQueue(*recoverableErr)
		}
	}

	return err
}

// CompensateStepWithRecovery executes compensation with error recovery
func (erm *ErrorRecoveryManager) CompensateStepWithRecovery(
	ctx context.Context,
	step Step,
	data interface{},
) error {
	return erm.retryManager.CompensateWithRetry(ctx, step, data, erm.errorClassifier)
}

// getOrCreateCircuitBreaker gets or creates a circuit breaker for a step
func (erm *ErrorRecoveryManager) getOrCreateCircuitBreaker(stepName string) *StepCircuitBreaker {
	erm.mu.Lock()
	defer erm.mu.Unlock()

	if cb, exists := erm.circuitBreakers[stepName]; exists {
		return cb
	}

	cb := NewStepCircuitBreaker(stepName, erm.policy.CircuitBreakerThreshold, 30*time.Second)
	erm.circuitBreakers[stepName] = cb
	return cb
}

// addToDeadLetterQueue adds an error to the dead letter queue
func (erm *ErrorRecoveryManager) addToDeadLetterQueue(err RecoverableError) {
	erm.mu.Lock()
	defer erm.mu.Unlock()

	erm.deadLetterQueue = append(erm.deadLetterQueue, err)

	// Keep dead letter queue size manageable
	if len(erm.deadLetterQueue) > 1000 {
		erm.deadLetterQueue = erm.deadLetterQueue[100:] // Remove oldest 100 entries
	}
}

// GetDeadLetterQueue returns a copy of the current dead letter queue
func (erm *ErrorRecoveryManager) GetDeadLetterQueue() []RecoverableError {
	erm.mu.RLock()
	defer erm.mu.RUnlock()

	result := make([]RecoverableError, len(erm.deadLetterQueue))
	copy(result, erm.deadLetterQueue)
	return result
}

// GetCircuitBreakerStatus returns the status of all circuit breakers
func (erm *ErrorRecoveryManager) GetCircuitBreakerStatus() map[string]CircuitBreakerState {
	erm.mu.RLock()
	defer erm.mu.RUnlock()

	status := make(map[string]CircuitBreakerState)
	for name, cb := range erm.circuitBreakers {
		status[name] = cb.GetState()
	}
	return status
}

// RecoveryMetrics contains metrics about error recovery
type RecoveryMetrics struct {
	TotalAttempts        int                            `json:"total_attempts"`
	SuccessfulRetries    int                            `json:"successful_retries"`
	FailedRetries        int                            `json:"failed_retries"`
	CompensationAttempts int                            `json:"compensation_attempts"`
	DeadLetterCount      int                            `json:"dead_letter_count"`
	CircuitBreakerStats  map[string]CircuitBreakerState `json:"circuit_breaker_stats"`
	ErrorDistribution    map[ErrorClassification]int    `json:"error_distribution"`
	AverageRetryCount    float64                        `json:"average_retry_count"`
	AverageRecoveryTime  time.Duration                  `json:"average_recovery_time"`
}

// GetRecoveryMetrics returns current recovery metrics
func (erm *ErrorRecoveryManager) GetRecoveryMetrics() RecoveryMetrics {
	erm.mu.RLock()
	defer erm.mu.RUnlock()

	metrics := RecoveryMetrics{
		CircuitBreakerStats: make(map[string]CircuitBreakerState),
		ErrorDistribution:   make(map[ErrorClassification]int),
	}

	// Circuit breaker stats
	for name, cb := range erm.circuitBreakers {
		metrics.CircuitBreakerStats[name] = cb.GetState()
	}

	// Dead letter queue count
	metrics.DeadLetterCount = len(erm.deadLetterQueue)

	// Error distribution
	for _, err := range erm.deadLetterQueue {
		metrics.ErrorDistribution[err.Classification]++
	}

	return metrics
}

// Enhanced Saga with Error Recovery Integration

// RecoverableSaga extends the basic saga with error recovery capabilities
type RecoverableSaga struct {
	*Saga
	recoveryManager *ErrorRecoveryManager
	policy          RecoveryPolicy
}

// NewRecoverableSaga creates a new saga with error recovery
func NewRecoverableSaga(name string, policy RecoveryPolicy) *RecoverableSaga {
	return &RecoverableSaga{
		Saga:            NewSaga(name),
		recoveryManager: NewErrorRecoveryManager(policy),
		policy:          policy,
	}
}

// Execute executes the saga with error recovery
func (rs *RecoverableSaga) Execute(ctx context.Context, data interface{}) error {
	rs.mu.Lock()
	rs.status = SagaStatusRunning
	rs.startTime = time.Now()
	rs.mu.Unlock()

	// Execute steps with recovery
	for i, step := range rs.steps {
		select {
		case <-ctx.Done():
			rs.setStatus(SagaStatusFailed)
			return rs.compensateWithRecovery(ctx, data, i-1)
		default:
		}

		// Execute step with error recovery
		if err := rs.recoveryManager.ExecuteStepWithRecovery(ctx, step, data); err != nil {
			rs.setStatus(SagaStatusFailed)
			rs.setError(err)

			// Check if this is a critical error that should abort compensation
			if recErr, ok := err.(*RecoverableError); ok {
				if recErr.Classification == ErrorCritical && rs.policy.AbortOnCriticalError {
					return fmt.Errorf("saga aborted due to critical error: %w", err)
				}
			}

			// Start compensation
			if compensationErr := rs.compensateWithRecovery(ctx, data, i-1); compensationErr != nil {
				return fmt.Errorf(
					"saga execution failed and compensation failed: original=%w, compensation=%w",
					err,
					compensationErr,
				)
			}

			rs.setStatus(SagaStatusCompensated)
			return fmt.Errorf("saga failed but compensated successfully: %w", err)
		}

		// Record successful step
		rs.mu.Lock()
		rs.completedSteps = append(rs.completedSteps, step.Name())
		rs.mu.Unlock()
	}

	rs.setStatus(SagaStatusCompleted)
	rs.setEndTime()
	return nil
}

// compensateWithRecovery performs compensation with error recovery
func (rs *RecoverableSaga) compensateWithRecovery(
	ctx context.Context,
	data interface{},
	lastExecutedStep int,
) error {
	rs.setStatus(SagaStatusCompensating)

	var compensationErrors []error
	compensatedSteps := make([]string, 0)

	// Compensate in reverse order
	for i := lastExecutedStep; i >= 0; i-- {
		step := rs.steps[i]

		if err := rs.recoveryManager.CompensateStepWithRecovery(ctx, step, data); err != nil {
			compensationErrors = append(
				compensationErrors,
				fmt.Errorf("compensation failed for step %s: %w", step.Name(), err),
			)

			// If partial compensation mode is disabled, fail fast
			if !rs.policy.PartialCompensationMode {
				return fmt.Errorf("compensation failed: %w", err)
			}
		} else {
			compensatedSteps = append(compensatedSteps, step.Name())
		}
	}

	// Update compensated steps
	rs.mu.Lock()
	rs.compensatedSteps = compensatedSteps
	rs.mu.Unlock()

	if len(compensationErrors) > 0 {
		return fmt.Errorf(
			"partial compensation completed with %d errors: %v",
			len(compensationErrors),
			compensationErrors,
		)
	}

	return nil
}

// GetRecoveryMetrics returns recovery metrics for this saga
func (rs *RecoverableSaga) GetRecoveryMetrics() RecoveryMetrics {
	return rs.recoveryManager.GetRecoveryMetrics()
}

// SetErrorClassifier sets a custom error classifier
func (rs *RecoverableSaga) SetErrorClassifier(classifier ErrorClassifier) {
	rs.recoveryManager.SetErrorClassifier(classifier)
}

// Helper functions for error classification

func isTimeoutError(err error) bool {
	// Check if error is a timeout error
	type timeoutError interface {
		Timeout() bool
	}

	if te, ok := err.(timeoutError); ok {
		return te.Timeout()
	}

	return false
}

func isResourceLimitError(errStr string) bool {
	// Common resource limit error patterns
	patterns := []string{
		"rate limit",
		"quota exceeded",
		"too many requests",
		"throttle",
		"capacity exceeded",
		"resource exhausted",
	}

	errLower := fmt.Sprintf("%s", errStr)
	for _, pattern := range patterns {
		if contains(errLower, pattern) {
			return true
		}
	}

	return false
}

func isCriticalError(errStr string) bool {
	// Critical error patterns that should not be retried
	patterns := []string{
		"fatal",
		"panic",
		"authorization failed",
		"access denied",
		"invalid state",
		"schema violation",
		"data corruption",
	}

	errLower := fmt.Sprintf("%s", errStr)
	for _, pattern := range patterns {
		if contains(errLower, pattern) {
			return true
		}
	}

	return false
}

func isPermanentError(errStr string) bool {
	// Permanent error patterns
	patterns := []string{
		"not found",
		"invalid request",
		"bad request",
		"malformed",
		"syntax error",
		"validation failed",
	}

	errLower := fmt.Sprintf("%s", errStr)
	for _, pattern := range patterns {
		if contains(errLower, pattern) {
			return true
		}
	}

	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
