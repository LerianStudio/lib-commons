// Package retry provides retry functionality with various jitter algorithms and backoff strategies.
// It includes implementations of full jitter, equal jitter, decorrelated jitter, and exponential
// backoff to prevent thundering herd problems and improve system resilience.
package retry

import (
	"math"
	"math/rand"
	"time"
)

// JitterType defines different types of jitter algorithms
type JitterType int

const (
	// NoJitter applies no jitter to the delay
	NoJitter JitterType = iota

	// FullJitter applies full jitter (0 to base_delay)
	FullJitter

	// EqualJitter applies equal jitter (base_delay/2 + random(base_delay/2))
	EqualJitter

	// DecorrelatedJitter applies decorrelated jitter (prevents synchronized retries)
	DecorrelatedJitter

	// ExponentialJitter applies exponential backoff with jitter
	ExponentialJitter
)

// JitterConfig holds configuration for jitter calculations
type JitterConfig struct {
	Type       JitterType
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Multiplier float64
	RandomSeed int64
	rand       *rand.Rand
}

// DefaultJitterConfig returns a sensible default jitter configuration
func DefaultJitterConfig() *JitterConfig {
	return &JitterConfig{
		Type:       EqualJitter,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
		Multiplier: 2.0,
		RandomSeed: time.Now().UnixNano(),
		// #nosec G404 - Using math/rand for jitter timing is appropriate, not for security
		rand:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// NewJitterConfig creates a new jitter configuration
func NewJitterConfig(jitterType JitterType, baseDelay, maxDelay time.Duration) *JitterConfig {
	return &JitterConfig{
		Type:       jitterType,
		BaseDelay:  baseDelay,
		MaxDelay:   maxDelay,
		Multiplier: 2.0,
		RandomSeed: time.Now().UnixNano(),
		// #nosec G404 - Using math/rand for jitter timing is appropriate, not for security
		rand:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// CalculateDelay calculates the delay for a given attempt with jitter
func (jc *JitterConfig) CalculateDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return jc.BaseDelay
	}

	switch jc.Type {
	case NoJitter:
		return jc.calculateExponentialDelay(attempt)

	case FullJitter:
		return jc.calculateFullJitter(attempt)

	case EqualJitter:
		return jc.calculateEqualJitter(attempt)

	case DecorrelatedJitter:
		return jc.calculateDecorrelatedJitter(attempt)

	case ExponentialJitter:
		return jc.calculateExponentialJitter(attempt)

	default:
		return jc.calculateEqualJitter(attempt)
	}
}

// calculateExponentialDelay calculates exponential backoff without jitter
func (jc *JitterConfig) calculateExponentialDelay(attempt int) time.Duration {
	delay := time.Duration(float64(jc.BaseDelay) * math.Pow(jc.Multiplier, float64(attempt-1)))
	if delay > jc.MaxDelay {
		delay = jc.MaxDelay
	}

	return delay
}

// calculateFullJitter implements full jitter: random(0, base_delay * 2^attempt)
func (jc *JitterConfig) calculateFullJitter(attempt int) time.Duration {
	baseDelay := jc.calculateExponentialDelay(attempt)
	jitteredDelay := time.Duration(jc.rand.Int63n(int64(baseDelay)))

	if jitteredDelay > jc.MaxDelay {
		jitteredDelay = jc.MaxDelay
	}

	return jitteredDelay
}

// calculateEqualJitter implements equal jitter: base_delay/2 + random(0, base_delay/2)
func (jc *JitterConfig) calculateEqualJitter(attempt int) time.Duration {
	baseDelay := jc.calculateExponentialDelay(attempt)
	halfDelay := baseDelay / 2
	jitter := time.Duration(jc.rand.Int63n(int64(halfDelay)))
	jitteredDelay := halfDelay + jitter

	if jitteredDelay > jc.MaxDelay {
		jitteredDelay = jc.MaxDelay
	}

	return jitteredDelay
}

// calculateDecorrelatedJitter implements decorrelated jitter to prevent synchronization
func (jc *JitterConfig) calculateDecorrelatedJitter(attempt int) time.Duration {
	// Start with base delay for first attempt
	if attempt == 1 {
		return jc.BaseDelay
	}

	// For subsequent attempts, use random delay between base and 3 * previous delay
	prevDelay := jc.CalculateDelay(attempt - 1)
	maxRandomDelay := time.Duration(float64(prevDelay) * 3.0)

	if maxRandomDelay > jc.MaxDelay {
		maxRandomDelay = jc.MaxDelay
	}

	// Random delay between base delay and max random delay
	delayRange := maxRandomDelay - jc.BaseDelay
	if delayRange <= 0 {
		return jc.BaseDelay
	}

	jitteredDelay := jc.BaseDelay + time.Duration(jc.rand.Int63n(int64(delayRange)))

	if jitteredDelay > jc.MaxDelay {
		jitteredDelay = jc.MaxDelay
	}

	return jitteredDelay
}

// calculateExponentialJitter implements exponential backoff with random jitter
func (jc *JitterConfig) calculateExponentialJitter(attempt int) time.Duration {
	baseDelay := jc.calculateExponentialDelay(attempt)

	// Add up to 50% jitter to the exponential delay
	maxJitter := time.Duration(float64(baseDelay) * 0.5)
	jitter := time.Duration(jc.rand.Int63n(int64(maxJitter)))

	jitteredDelay := baseDelay + jitter

	if jitteredDelay > jc.MaxDelay {
		jitteredDelay = jc.MaxDelay
	}

	return jitteredDelay
}

// JitteredRetryFunc represents a function that implements retry logic with jitter
type JitteredRetryFunc func(attempt int, config *JitterConfig) time.Duration

// ExecuteWithJitter wraps a retry function with jitter configuration
func ExecuteWithJitter(retryFunc func() error, config *JitterConfig, maxAttempts int) error {
	if config == nil {
		config = DefaultJitterConfig()
	}

	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := retryFunc()
		if err == nil {
			return nil // Success
		}

		lastErr = err

		// Don't sleep after the last attempt
		if attempt == maxAttempts {
			break
		}

		// Calculate delay with jitter and sleep
		delay := config.CalculateDelay(attempt)
		time.Sleep(delay)
	}

	return lastErr
}

// CalculateJitteredDelay is a convenience function for calculating jittered delays
func CalculateJitteredDelay(jitterType JitterType, baseDelay, maxDelay time.Duration, attempt int) time.Duration {
	config := NewJitterConfig(jitterType, baseDelay, maxDelay)
	return config.CalculateDelay(attempt)
}

// JitterOption allows customization of jitter configuration
type JitterOption func(*JitterConfig)

// WithJitterType sets the jitter type
func WithJitterType(jitterType JitterType) JitterOption {
	return func(config *JitterConfig) {
		config.Type = jitterType
	}
}

// WithDelays sets the base and max delays
func WithDelays(baseDelay, maxDelay time.Duration) JitterOption {
	return func(config *JitterConfig) {
		config.BaseDelay = baseDelay
		config.MaxDelay = maxDelay
	}
}

// WithMultiplier sets the exponential backoff multiplier
func WithMultiplier(multiplier float64) JitterOption {
	return func(config *JitterConfig) {
		config.Multiplier = multiplier
	}
}

// WithRandomSeed sets a specific random seed (useful for testing)
func WithRandomSeed(seed int64) JitterOption {
	return func(config *JitterConfig) {
		config.RandomSeed = seed
		// #nosec G404 - Using math/rand for jitter timing is appropriate, not for security
		config.rand = rand.New(rand.NewSource(seed))
	}
}

// NewJitterConfigWithOptions creates a jitter configuration with options
func NewJitterConfigWithOptions(opts ...JitterOption) *JitterConfig {
	config := DefaultJitterConfig()

	for _, opt := range opts {
		opt(config)
	}

	return config
}
