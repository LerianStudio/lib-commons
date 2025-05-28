package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// Operation is a function that can be retried
type Operation func() error

// OperationWithResult is a function that returns a result and can be retried
type OperationWithResult[T any] func() (T, error)

// config holds retry configuration
type config struct {
	maxRetries  int
	delay       time.Duration
	maxDelay    time.Duration
	multiplier  float64
	jitter      float64
	retryIf     func(error) bool
	onRetry     func(n int, err error)
}

// Option configures retry behavior
type Option func(*config)

// WithMaxRetries sets the maximum number of retry attempts
func WithMaxRetries(n int) Option {
	return func(c *config) {
		c.maxRetries = n
	}
}

// WithDelay sets the delay between retries
func WithDelay(d time.Duration) Option {
	return func(c *config) {
		c.delay = d
	}
}

// WithMaxDelay sets the maximum delay between retries
func WithMaxDelay(d time.Duration) Option {
	return func(c *config) {
		c.maxDelay = d
	}
}

// WithExponentialBackoff enables exponential backoff with the given multiplier
func WithExponentialBackoff(initialDelay time.Duration, multiplier float64) Option {
	return func(c *config) {
		c.delay = initialDelay
		c.multiplier = multiplier
	}
}

// WithJitter adds randomness to retry delays (0.0 to 1.0)
func WithJitter(factor float64) Option {
	return func(c *config) {
		c.jitter = factor
	}
}

// WithRetryIf sets a custom function to determine if an error is retryable
func WithRetryIf(fn func(error) bool) Option {
	return func(c *config) {
		c.retryIf = fn
	}
}

// WithOnRetry sets a callback function called before each retry
func WithOnRetry(fn func(n int, err error)) Option {
	return func(c *config) {
		c.onRetry = fn
	}
}

// newDefaultConfig returns default retry configuration
func newDefaultConfig() *config {
	return &config{
		maxRetries: 3,
		delay:      1 * time.Second,
		maxDelay:   0, // no limit by default
		multiplier: 1.0,
		jitter:     0.0,
		retryIf: func(err error) bool {
			// By default, retry all errors
			return true
		},
		onRetry: func(n int, err error) {
			// No-op by default
		},
	}
}

// Do executes the operation with retry logic
func Do(ctx context.Context, operation Operation, opts ...Option) error {
	cfg := newDefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	var err error
	delay := cfg.delay

	for attempt := 0; attempt <= cfg.maxRetries; attempt++ {
		// Check context before attempting
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Execute the operation
		err = operation()
		if err == nil {
			return nil
		}

		// Check if we should retry
		if !cfg.retryIf(err) {
			return err
		}

		// If this was the last attempt, return the error
		if attempt == cfg.maxRetries {
			return fmt.Errorf("operation failed after %d attempts: %w", cfg.maxRetries+1, err)
		}

		// Call onRetry callback
		cfg.onRetry(attempt+1, err)

		// Calculate delay with jitter
		actualDelay := delay
		if cfg.jitter > 0 {
			jitterAmount := float64(delay) * cfg.jitter
			actualDelay = time.Duration(float64(delay) + (rand.Float64()*2-1)*jitterAmount)
		}

		// Wait before next retry
		timer := time.NewTimer(actualDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		// Apply exponential backoff for next iteration
		if cfg.multiplier > 1.0 {
			delay = time.Duration(float64(delay) * cfg.multiplier)
			if cfg.maxDelay > 0 && delay > cfg.maxDelay {
				delay = cfg.maxDelay
			}
		}
	}

	return err
}

// DoWithResult executes an operation that returns a result with retry logic
func DoWithResult[T any](ctx context.Context, operation OperationWithResult[T], opts ...Option) (T, error) {
	var result T
	err := Do(ctx, func() error {
		var opErr error
		result, opErr = operation()
		return opErr
	}, opts...)
	return result, err
}

// Permanent wraps an error to indicate it should not be retried
type Permanent struct {
	Err error
}

func (e Permanent) Error() string {
	return e.Err.Error()
}

func (e Permanent) Unwrap() error {
	return e.Err
}

// IsPermanent checks if an error is marked as permanent (non-retryable)
func IsPermanent(err error) bool {
	var permanent Permanent
	return errors.As(err, &permanent)
}

// MarkPermanent marks an error as permanent (non-retryable)
func MarkPermanent(err error) error {
	if err == nil {
		return nil
	}
	return Permanent{Err: err}
}

// DefaultRetryIf is a retry condition that retries all errors except permanent ones
func DefaultRetryIf(err error) bool {
	return !IsPermanent(err)
}

// RetryableError interface for errors that can indicate if they're retryable
type RetryableError interface {
	error
	Retryable() bool
}

// IsRetryable checks if an error implements RetryableError and is retryable
func IsRetryable(err error) bool {
	var re RetryableError
	if errors.As(err, &re) {
		return re.Retryable()
	}
	return true // Default to retryable if not specified
}