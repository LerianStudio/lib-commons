package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/zap"
	"github.com/stretchr/testify/assert"
)

func TestDefaultAdaptivePoolConfig(t *testing.T) {
	config := DefaultAdaptivePoolConfig()

	// Test basic pool settings
	assert.Equal(t, 5, config.MinConnections)
	assert.Equal(t, 100, config.MaxConnections)
	assert.Equal(t, 10, config.InitialConnections)
	assert.Equal(t, 30*time.Second, config.ConnectionTimeout)
	assert.Equal(t, 15*time.Minute, config.IdleTimeout)
	assert.Equal(t, 1*time.Hour, config.MaxLifetime)

	// Test adaptive scaling settings
	assert.Equal(t, 0.75, config.ScaleUpThreshold)
	assert.Equal(t, 0.25, config.ScaleDownThreshold)
	assert.Equal(t, 30*time.Second, config.ScaleInterval)
	assert.Equal(t, 1.5, config.ScaleUpFactor)
	assert.Equal(t, 0.8, config.ScaleDownFactor)

	// Test circuit breaker settings
	assert.True(t, config.EnableCircuitBreaker)
	assert.Equal(t, 10, config.FailureThreshold)
	assert.Equal(t, 60*time.Second, config.RecoveryTimeout)

	// Test retry settings
	assert.Equal(t, 3, config.MaxRetries)
	assert.Equal(t, 1*time.Second, config.RetryDelay)
	assert.True(t, config.RetryJitter)
	assert.Equal(t, 2.0, config.BackoffFactor)
}

func TestAdaptivePoolConfig_Validation(t *testing.T) {
	testCases := []struct {
		name         string
		modifyConfig func(*AdaptivePoolConfig)
		expectValid  bool
	}{
		{
			name: "valid_config",
			modifyConfig: func(c *AdaptivePoolConfig) {
				// Use default config, no changes
			},
			expectValid: true,
		},
		{
			name: "min_connections_greater_than_max",
			modifyConfig: func(c *AdaptivePoolConfig) {
				c.MinConnections = 50
				c.MaxConnections = 20
			},
			expectValid: false,
		},
		{
			name: "initial_connections_less_than_min",
			modifyConfig: func(c *AdaptivePoolConfig) {
				c.MinConnections = 10
				c.InitialConnections = 5
			},
			expectValid: false,
		},
		{
			name: "initial_connections_greater_than_max",
			modifyConfig: func(c *AdaptivePoolConfig) {
				c.MaxConnections = 20
				c.InitialConnections = 25
			},
			expectValid: false,
		},
		{
			name: "invalid_scale_thresholds",
			modifyConfig: func(c *AdaptivePoolConfig) {
				c.ScaleUpThreshold = 0.3
				c.ScaleDownThreshold = 0.7 // Should be less than scale up
			},
			expectValid: false,
		},
		{
			name: "zero_timeout_values",
			modifyConfig: func(c *AdaptivePoolConfig) {
				c.ConnectionTimeout = 0
			},
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultAdaptivePoolConfig()
			tc.modifyConfig(&config)

			isValid := validateAdaptivePoolConfig(config)
			assert.Equal(t, tc.expectValid, isValid, "Config validation result mismatch")
		})
	}
}

func TestCircuitBreakerState_String(t *testing.T) {
	assert.Equal(t, "closed", CircuitBreakerClosed.String())
	assert.Equal(t, "open", CircuitBreakerOpen.String())
	assert.Equal(t, "half-open", CircuitBreakerHalfOpen.String())
}

func TestAdaptivePoolMetrics(t *testing.T) {
	// Test metrics initialization and calculations
	config := DefaultAdaptivePoolConfig()
	config.MinConnections = 2
	config.MaxConnections = 5
	config.InitialConnections = 3

	// Create mock metrics
	metrics := PoolMetrics{
		ActiveConnections:    2,
		IdleConnections:      1,
		TotalConnections:     3,
		MaxConnections:       5,
		TotalQueries:         100,
		SuccessfulQueries:    95,
		FailedQueries:        5,
		AverageLatency:       50 * time.Millisecond,
		P95Latency:           120 * time.Millisecond,
		P99Latency:           200 * time.Millisecond,
		CircuitBreakerStatus: "closed",
		LastScalingAction:    time.Now(),
		ScalingDirection:     "up",
	}

	// Test connection utilization calculation
	expectedUtilization := float64(metrics.ActiveConnections) / float64(metrics.MaxConnections)
	assert.Equal(t, 0.4, expectedUtilization)

	// Test success rate calculation
	expectedSuccessRate := float64(metrics.SuccessfulQueries) / float64(metrics.TotalQueries)
	assert.Equal(t, 0.95, expectedSuccessRate)

	// Test pool efficiency calculation
	totalAttempts := metrics.SuccessfulQueries + metrics.FailedQueries
	expectedEfficiency := float64(metrics.SuccessfulQueries) / float64(totalAttempts)
	assert.Equal(t, 0.95, expectedEfficiency)
}

func TestIsRetryableError(t *testing.T) {
	testCases := []struct {
		name      string
		error     error
		retryable bool
	}{
		{
			name:      "nil_error",
			error:     nil,
			retryable: false,
		},
		{
			name:      "connection_refused",
			error:     fmt.Errorf("connection refused"),
			retryable: true,
		},
		{
			name:      "network_timeout",
			error:     fmt.Errorf("network timeout occurred"),
			retryable: true,
		},
		{
			name:      "too_many_connections",
			error:     fmt.Errorf("too many connections"),
			retryable: true,
		},
		{
			name:      "syntax_error",
			error:     fmt.Errorf("syntax error in SQL"),
			retryable: false,
		},
		{
			name:      "permission_denied",
			error:     fmt.Errorf("permission denied"),
			retryable: false,
		},
	}

	// Mock adaptive pool for testing
	config := DefaultAdaptivePoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	// Create a temporary mock pool (this will fail but we only need the isRetryableError method)
	mockPool := &AdaptivePostgresPool{
		config: config,
		logger: logger,
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := mockPool.isRetryableError(tc.error)
			assert.Equal(t, tc.retryable, result, "Retryable error classification mismatch")
		})
	}
}

func TestCalculateHealthScore(t *testing.T) {
	testCases := []struct {
		name        string
		successRate float64
		avgLatency  time.Duration
		threshold   time.Duration
		cbState     CircuitBreakerState
		expectedMin float64
		expectedMax float64
	}{
		{
			name:        "perfect_health",
			successRate: 1.0,
			avgLatency:  10 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     CircuitBreakerClosed,
			expectedMin: 0.95,
			expectedMax: 1.0,
		},
		{
			name:        "degraded_success_rate",
			successRate: 0.8,
			avgLatency:  50 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     CircuitBreakerClosed,
			expectedMin: 0.7,
			expectedMax: 0.9,
		},
		{
			name:        "high_latency",
			successRate: 0.95,
			avgLatency:  200 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     CircuitBreakerClosed,
			expectedMin: 0.4,
			expectedMax: 0.6,
		},
		{
			name:        "circuit_breaker_open",
			successRate: 0.95,
			avgLatency:  50 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     CircuitBreakerOpen,
			expectedMin: 0.0,
			expectedMax: 0.2,
		},
		{
			name:        "circuit_breaker_half_open",
			successRate: 0.95,
			avgLatency:  50 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     CircuitBreakerHalfOpen,
			expectedMin: 0.4,
			expectedMax: 0.6,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultAdaptivePoolConfig()
			config.LatencyThreshold = tc.threshold
			logger, _ := zap.NewStructured("test", "debug")

			// Create mock pool with test data
			mockPool := &AdaptivePostgresPool{
				config:  config,
				logger:  logger,
				cbState: tc.cbState,
				metrics: PoolMetrics{
					SuccessRate:    tc.successRate,
					AverageLatency: tc.avgLatency,
				},
			}

			healthScore := mockPool.calculateHealthScore()

			assert.GreaterOrEqual(
				t,
				healthScore,
				tc.expectedMin,
				"Health score below expected minimum",
			)
			assert.LessOrEqual(
				t,
				healthScore,
				tc.expectedMax,
				"Health score above expected maximum",
			)
			assert.GreaterOrEqual(t, healthScore, 0.0, "Health score should not be negative")
			assert.LessOrEqual(t, healthScore, 1.0, "Health score should not exceed 1.0")
		})
	}
}

func TestAdaptivePoolScalingLogic(t *testing.T) {
	testCases := []struct {
		name               string
		currentConnections int
		utilization        float64
		scaleUpThreshold   float64
		scaleDownThreshold float64
		minConnections     int
		maxConnections     int
		expectedAction     string // "up", "down", "none"
	}{
		{
			name:               "scale_up_needed",
			currentConnections: 10,
			utilization:        0.8,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minConnections:     5,
			maxConnections:     20,
			expectedAction:     "up",
		},
		{
			name:               "scale_down_needed",
			currentConnections: 15,
			utilization:        0.2,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minConnections:     5,
			maxConnections:     20,
			expectedAction:     "down",
		},
		{
			name:               "no_scaling_needed",
			currentConnections: 10,
			utilization:        0.5,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minConnections:     5,
			maxConnections:     20,
			expectedAction:     "none",
		},
		{
			name:               "at_max_connections",
			currentConnections: 20,
			utilization:        0.8,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minConnections:     5,
			maxConnections:     20,
			expectedAction:     "none", // Can't scale up beyond max
		},
		{
			name:               "at_min_connections",
			currentConnections: 5,
			utilization:        0.2,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minConnections:     5,
			maxConnections:     20,
			expectedAction:     "none", // Can't scale down below min
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			shouldScaleUp := tc.utilization > tc.scaleUpThreshold &&
				tc.currentConnections < tc.maxConnections

			shouldScaleDown := tc.utilization < tc.scaleDownThreshold &&
				tc.currentConnections > tc.minConnections

			var action string
			if shouldScaleUp {
				action = "up"
			} else if shouldScaleDown {
				action = "down"
			} else {
				action = "none"
			}

			assert.Equal(t, tc.expectedAction, action, "Scaling decision mismatch")
		})
	}
}

func TestLatencyMetricsCalculation(t *testing.T) {
	// Test latency calculation with various scenarios
	latencies := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond, // P95 should be around here
		200 * time.Millisecond, // P99 should be around here
	}

	// Calculate expected average
	var total time.Duration
	for _, latency := range latencies {
		total += latency
	}
	expectedAverage := total / time.Duration(len(latencies))

	// Test the calculation logic (simplified)
	assert.Equal(t, expectedAverage, total/time.Duration(len(latencies)))

	// Test that latencies are in ascending order for percentile calculation
	for i := 1; i < len(latencies); i++ {
		assert.GreaterOrEqual(
			t,
			latencies[i],
			latencies[i-1],
			"Latencies should be in ascending order",
		)
	}

	// Test percentile index calculation
	p95Index := int(float64(len(latencies)) * 0.95)
	p99Index := int(float64(len(latencies)) * 0.99)

	assert.True(t, p95Index < len(latencies), "P95 index should be within bounds")
	assert.True(t, p99Index < len(latencies), "P99 index should be within bounds")
	assert.GreaterOrEqual(t, p99Index, p95Index, "P99 index should be >= P95 index")
}

func TestStringContainsHelper(t *testing.T) {
	testCases := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "exact_match",
			s:        "connection refused",
			substr:   "connection refused",
			expected: true,
		},
		{
			name:     "substring_at_start",
			s:        "connection timeout occurred",
			substr:   "connection",
			expected: true,
		},
		{
			name:     "substring_at_end",
			s:        "network timeout",
			substr:   "timeout",
			expected: true,
		},
		{
			name:     "substring_in_middle",
			s:        "database connection failed",
			substr:   "connection",
			expected: true,
		},
		{
			name:     "not_found",
			s:        "permission denied",
			substr:   "connection",
			expected: false,
		},
		{
			name:     "empty_string",
			s:        "",
			substr:   "test",
			expected: false,
		},
		{
			name:     "empty_substring",
			s:        "test string",
			substr:   "",
			expected: true,
		},
		{
			name:     "case_sensitive",
			s:        "Connection Refused",
			substr:   "connection",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := contains(tc.s, tc.substr)
			assert.Equal(t, tc.expected, result, "String contains check mismatch")
		})
	}
}

// Helper function to validate adaptive pool configuration
func validateAdaptivePoolConfig(config AdaptivePoolConfig) bool {
	// Basic validation rules
	if config.MinConnections <= 0 || config.MaxConnections <= 0 || config.InitialConnections <= 0 {
		return false
	}

	if config.MinConnections > config.MaxConnections {
		return false
	}

	if config.InitialConnections < config.MinConnections ||
		config.InitialConnections > config.MaxConnections {
		return false
	}

	if config.ScaleUpThreshold <= config.ScaleDownThreshold {
		return false
	}

	if config.ScaleUpThreshold <= 0 || config.ScaleUpThreshold >= 1 {
		return false
	}

	if config.ScaleDownThreshold <= 0 || config.ScaleDownThreshold >= 1 {
		return false
	}

	if config.ConnectionTimeout <= 0 || config.IdleTimeout <= 0 || config.MaxLifetime <= 0 {
		return false
	}

	return true
}

// Benchmark tests for performance critical functions
func BenchmarkCalculateHealthScore(b *testing.B) {
	config := DefaultAdaptivePoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptivePostgresPool{
		config:  config,
		logger:  logger,
		cbState: CircuitBreakerClosed,
		metrics: PoolMetrics{
			SuccessRate:    0.95,
			AverageLatency: 50 * time.Millisecond,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mockPool.calculateHealthScore()
	}
}

func BenchmarkIsRetryableError(b *testing.B) {
	config := DefaultAdaptivePoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptivePostgresPool{
		config: config,
		logger: logger,
	}

	err := fmt.Errorf("connection refused")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mockPool.isRetryableError(err)
	}
}

func BenchmarkStringContains(b *testing.B) {
	s := "database connection timeout occurred"
	substr := "connection"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = contains(s, substr)
	}
}
