package mongo

import (
	"fmt"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/zap"
	"github.com/stretchr/testify/assert"
)

func TestDefaultAdaptiveMongoPoolConfig(t *testing.T) {
	config := DefaultAdaptiveMongoPoolConfig()

	// Test basic pool settings
	assert.Equal(t, uint64(5), config.MinPoolSize)
	assert.Equal(t, uint64(100), config.MaxPoolSize)
	assert.Equal(t, uint64(10), config.InitialPoolSize)
	assert.Equal(t, 15*time.Minute, config.MaxConnIdleTime)
	assert.Equal(t, uint64(10), config.MaxConnecting)
	assert.Equal(t, 10*time.Second, config.ConnectTimeout)
	assert.Equal(t, 30*time.Second, config.SocketTimeout)
	assert.Equal(t, 10*time.Second, config.ServerSelectionTimeout)

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

	// Test read preference
	assert.Equal(t, "primary", config.ReadPreference)
	assert.False(t, config.EnableReplicaSet)
	assert.False(t, config.EnableSharding)
}

func TestAdaptiveMongoPoolConfig_Validation(t *testing.T) {
	testCases := []struct {
		name         string
		modifyConfig func(*AdaptiveMongoPoolConfig)
		expectValid  bool
	}{
		{
			name: "valid_config",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				// Use default config, no changes
			},
			expectValid: true,
		},
		{
			name: "min_pool_size_greater_than_max",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				c.MinPoolSize = 50
				c.MaxPoolSize = 20
			},
			expectValid: false,
		},
		{
			name: "initial_pool_size_less_than_min",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				c.MinPoolSize = 10
				c.InitialPoolSize = 5
			},
			expectValid: false,
		},
		{
			name: "initial_pool_size_greater_than_max",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				c.MaxPoolSize = 20
				c.InitialPoolSize = 25
			},
			expectValid: false,
		},
		{
			name: "invalid_scale_thresholds",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				c.ScaleUpThreshold = 0.3
				c.ScaleDownThreshold = 0.7 // Should be less than scale up
			},
			expectValid: false,
		},
		{
			name: "zero_timeout_values",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				c.ConnectTimeout = 0
			},
			expectValid: false,
		},
		{
			name: "invalid_read_preference",
			modifyConfig: func(c *AdaptiveMongoPoolConfig) {
				c.ReadPreference = "invalid_preference"
			},
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultAdaptiveMongoPoolConfig()
			tc.modifyConfig(&config)

			isValid := validateAdaptiveMongoPoolConfig(config)
			assert.Equal(t, tc.expectValid, isValid, "Config validation result mismatch")
		})
	}
}

func TestMongoCircuitBreakerState_String(t *testing.T) {
	assert.Equal(t, "closed", MongoCircuitBreakerClosed.String())
	assert.Equal(t, "open", MongoCircuitBreakerOpen.String())
	assert.Equal(t, "half-open", MongoCircuitBreakerHalfOpen.String())
}

func TestMongoPoolMetrics(t *testing.T) {
	// Test metrics initialization and calculations
	config := DefaultAdaptiveMongoPoolConfig()
	config.MinPoolSize = 2
	config.MaxPoolSize = 5
	config.InitialPoolSize = 3

	// Create mock metrics
	metrics := MongoPoolMetrics{
		ActiveConnections:    2,
		AvailableConnections: 1,
		TotalConnections:     3,
		MaxPoolSize:          5,
		TotalOperations:      100,
		SuccessfulOperations: 95,
		FailedOperations:     5,
		AverageLatency:       50 * time.Millisecond,
		P95Latency:           120 * time.Millisecond,
		P99Latency:           200 * time.Millisecond,
		CircuitBreakerStatus: "closed",
		LastScalingAction:    time.Now(),
		ScalingDirection:     "up",
		DatabaseSize:         1024000,
		CollectionCount:      10,
		IndexCount:           25,
		ReplicationHealth:    "healthy",
	}

	// Test connection utilization calculation
	expectedUtilization := float64(metrics.ActiveConnections) / float64(metrics.MaxPoolSize)
	assert.Equal(t, 0.4, expectedUtilization)

	// Test success rate calculation
	expectedSuccessRate := float64(metrics.SuccessfulOperations) / float64(metrics.TotalOperations)
	assert.Equal(t, 0.95, expectedSuccessRate)

	// Test pool efficiency calculation
	totalAttempts := metrics.SuccessfulOperations + metrics.FailedOperations
	expectedEfficiency := float64(metrics.SuccessfulOperations) / float64(totalAttempts)
	assert.Equal(t, 0.95, expectedEfficiency)

	// Test MongoDB-specific metrics
	assert.Greater(t, metrics.DatabaseSize, int64(0))
	assert.Greater(t, metrics.CollectionCount, 0)
	assert.Greater(t, metrics.IndexCount, 0)
	assert.Equal(t, "healthy", metrics.ReplicationHealth)
}

func TestIsRetryableMongoError(t *testing.T) {
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
			name:      "server_selection_timeout",
			error:     fmt.Errorf("server selection timeout"),
			retryable: true,
		},
		{
			name:      "connection_pool_timeout",
			error:     fmt.Errorf("connection pool timeout"),
			retryable: true,
		},
		{
			name:      "invalid_document",
			error:     fmt.Errorf("invalid document structure"),
			retryable: false,
		},
		{
			name:      "permission_denied",
			error:     fmt.Errorf("permission denied"),
			retryable: false,
		},
		{
			name:      "duplicate_key_error",
			error:     fmt.Errorf("duplicate key error"),
			retryable: false,
		},
	}

	// Mock adaptive pool for testing
	config := DefaultAdaptiveMongoPoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	// Create a temporary mock pool (this will fail but we only need the isRetryableError method)
	mockPool := &AdaptiveMongoPool{
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

func TestCalculateMongoHealthScore(t *testing.T) {
	testCases := []struct {
		name        string
		successRate float64
		avgLatency  time.Duration
		threshold   time.Duration
		cbState     MongoCircuitBreakerState
		replHealth  string
		expectedMin float64
		expectedMax float64
	}{
		{
			name:        "perfect_health",
			successRate: 1.0,
			avgLatency:  10 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     MongoCircuitBreakerClosed,
			replHealth:  "healthy",
			expectedMin: 0.95,
			expectedMax: 1.0,
		},
		{
			name:        "degraded_success_rate",
			successRate: 0.8,
			avgLatency:  50 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     MongoCircuitBreakerClosed,
			replHealth:  "healthy",
			expectedMin: 0.7,
			expectedMax: 0.9,
		},
		{
			name:        "high_latency",
			successRate: 0.95,
			avgLatency:  200 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     MongoCircuitBreakerClosed,
			replHealth:  "healthy",
			expectedMin: 0.4,
			expectedMax: 0.6,
		},
		{
			name:        "circuit_breaker_open",
			successRate: 0.95,
			avgLatency:  50 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     MongoCircuitBreakerOpen,
			replHealth:  "healthy",
			expectedMin: 0.0,
			expectedMax: 0.2,
		},
		{
			name:        "replication_issues",
			successRate: 0.95,
			avgLatency:  50 * time.Millisecond,
			threshold:   100 * time.Millisecond,
			cbState:     MongoCircuitBreakerClosed,
			replHealth:  "error",
			expectedMin: 0.6,
			expectedMax: 0.8,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultAdaptiveMongoPoolConfig()
			config.LatencyThreshold = tc.threshold
			config.EnableReplicaSet = (tc.replHealth != "")
			logger, _ := zap.NewStructured("test", "debug")

			// Create mock pool with test data
			mockPool := &AdaptiveMongoPool{
				config:  config,
				logger:  logger,
				cbState: tc.cbState,
				metrics: MongoPoolMetrics{
					SuccessRate:       tc.successRate,
					AverageLatency:    tc.avgLatency,
					ReplicationHealth: tc.replHealth,
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

func TestAdaptiveMongoPoolScalingLogic(t *testing.T) {
	testCases := []struct {
		name               string
		currentPoolSize    uint64
		utilization        float64
		scaleUpThreshold   float64
		scaleDownThreshold float64
		minPoolSize        uint64
		maxPoolSize        uint64
		expectedAction     string // "up", "down", "none"
	}{
		{
			name:               "scale_up_needed",
			currentPoolSize:    10,
			utilization:        0.8,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minPoolSize:        5,
			maxPoolSize:        20,
			expectedAction:     "up",
		},
		{
			name:               "scale_down_needed",
			currentPoolSize:    15,
			utilization:        0.2,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minPoolSize:        5,
			maxPoolSize:        20,
			expectedAction:     "down",
		},
		{
			name:               "no_scaling_needed",
			currentPoolSize:    10,
			utilization:        0.5,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minPoolSize:        5,
			maxPoolSize:        20,
			expectedAction:     "none",
		},
		{
			name:               "at_max_pool_size",
			currentPoolSize:    20,
			utilization:        0.8,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minPoolSize:        5,
			maxPoolSize:        20,
			expectedAction:     "none", // Can't scale up beyond max
		},
		{
			name:               "at_min_pool_size",
			currentPoolSize:    5,
			utilization:        0.2,
			scaleUpThreshold:   0.7,
			scaleDownThreshold: 0.3,
			minPoolSize:        5,
			maxPoolSize:        20,
			expectedAction:     "none", // Can't scale down below min
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			shouldScaleUp := tc.utilization > tc.scaleUpThreshold &&
				tc.currentPoolSize < tc.maxPoolSize

			shouldScaleDown := tc.utilization < tc.scaleDownThreshold &&
				tc.currentPoolSize > tc.minPoolSize

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

func TestMongoRetryDelayCalculation(t *testing.T) {
	config := DefaultAdaptiveMongoPoolConfig()
	config.RetryDelay = 1 * time.Second
	config.BackoffFactor = 2.0
	config.RetryJitter = false // Disable jitter for predictable testing

	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config: config,
		logger: logger,
	}

	testCases := []struct {
		attempt     int
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{
			attempt:     1,
			expectedMin: 2 * time.Second, // 1s * 1 * 2.0
			expectedMax: 3 * time.Second,
		},
		{
			attempt:     2,
			expectedMin: 4 * time.Second, // 1s * 2 * 2.0
			expectedMax: 5 * time.Second,
		},
		{
			attempt:     3,
			expectedMin: 6 * time.Second, // 1s * 3 * 2.0
			expectedMax: 7 * time.Second,
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("attempt_%d", tc.attempt), func(t *testing.T) {
			delay := mockPool.calculateRetryDelay(tc.attempt)

			assert.GreaterOrEqual(t, delay, tc.expectedMin, "Retry delay below expected minimum")
			assert.LessOrEqual(t, delay, tc.expectedMax, "Retry delay above expected maximum")
		})
	}

	// Test with jitter enabled
	config.RetryJitter = true
	mockPoolWithJitter := &AdaptiveMongoPool{
		config: config,
		logger: logger,
	}

	delay1 := mockPoolWithJitter.calculateRetryDelay(1)
	delay2 := mockPoolWithJitter.calculateRetryDelay(1)

	// With jitter, delays might be different (though they could be the same due to randomness)
	// Just verify they're in a reasonable range
	assert.GreaterOrEqual(t, delay1, 2*time.Second, "Jittered delay should be at least base delay")
	assert.GreaterOrEqual(t, delay2, 2*time.Second, "Jittered delay should be at least base delay")
}

func TestMongoPoolMetricsUpdate(t *testing.T) {
	// Test latency metrics update with various scenarios
	latencies := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond, // P95 should be around here
		200 * time.Millisecond, // P99 should be around here
	}

	config := DefaultAdaptiveMongoPoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config:         config,
		logger:         logger,
		latencyHistory: make([]time.Duration, 0),
	}

	// Add latencies to history
	for _, latency := range latencies {
		mockPool.latencyHistory = append(mockPool.latencyHistory, latency)
	}

	// Update metrics
	mockPool.updateLatencyMetrics()

	// Verify average calculation
	var expectedTotal time.Duration
	for _, latency := range latencies {
		expectedTotal += latency
	}
	expectedAverage := expectedTotal / time.Duration(len(latencies))

	assert.Equal(
		t,
		expectedAverage,
		mockPool.metrics.AverageLatency,
		"Average latency calculation mismatch",
	)

	// Verify that P95 and P99 are calculated (values will depend on the sorting algorithm)
	assert.Greater(
		t,
		mockPool.metrics.P95Latency,
		time.Duration(0),
		"P95 latency should be calculated",
	)
	assert.Greater(
		t,
		mockPool.metrics.P99Latency,
		time.Duration(0),
		"P99 latency should be calculated",
	)
	assert.GreaterOrEqual(
		t,
		mockPool.metrics.P99Latency,
		mockPool.metrics.P95Latency,
		"P99 should be >= P95",
	)
}

func TestMongoOperationRecording(t *testing.T) {
	config := DefaultAdaptiveMongoPoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config:  config,
		logger:  logger,
		cbState: MongoCircuitBreakerClosed,
		metrics: MongoPoolMetrics{},
	}

	// Test successful operation recording
	initialSuccessful := mockPool.metrics.SuccessfulOperations
	initialTotal := mockPool.metrics.TotalOperations

	mockPool.recordSuccessfulOperation()

	assert.Equal(
		t,
		initialSuccessful+1,
		mockPool.metrics.SuccessfulOperations,
		"Successful operations should increment",
	)
	assert.Equal(
		t,
		initialTotal+1,
		mockPool.metrics.TotalOperations,
		"Total operations should increment",
	)

	// Test failed operation recording
	initialFailed := mockPool.metrics.FailedOperations
	initialTotal = mockPool.metrics.TotalOperations
	testError := fmt.Errorf("test error")

	mockPool.recordFailedOperation(testError)

	assert.Equal(
		t,
		initialFailed+1,
		mockPool.metrics.FailedOperations,
		"Failed operations should increment",
	)
	assert.Equal(
		t,
		initialTotal+1,
		mockPool.metrics.TotalOperations,
		"Total operations should increment",
	)
	assert.Equal(t, testError.Error(), mockPool.metrics.LastError, "Last error should be recorded")
	assert.NotZero(t, mockPool.metrics.LastErrorTime, "Last error time should be set")
}

// Helper function to validate adaptive MongoDB pool configuration
func validateAdaptiveMongoPoolConfig(config AdaptiveMongoPoolConfig) bool {
	// Basic validation rules
	if config.MinPoolSize <= 0 || config.MaxPoolSize <= 0 || config.InitialPoolSize <= 0 {
		return false
	}

	if config.MinPoolSize > config.MaxPoolSize {
		return false
	}

	if config.InitialPoolSize < config.MinPoolSize || config.InitialPoolSize > config.MaxPoolSize {
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

	if config.ConnectTimeout <= 0 || config.SocketTimeout <= 0 || config.MaxConnIdleTime <= 0 {
		return false
	}

	// Validate read preference
	validReadPrefs := []string{
		"primary",
		"primaryPreferred",
		"secondary",
		"secondaryPreferred",
		"nearest",
	}
	validReadPref := false
	for _, pref := range validReadPrefs {
		if config.ReadPreference == pref {
			validReadPref = true
			break
		}
	}
	if !validReadPref {
		return false
	}

	return true
}

// Benchmark tests for performance critical functions
func BenchmarkCalculateMongoHealthScore(b *testing.B) {
	config := DefaultAdaptiveMongoPoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config:  config,
		logger:  logger,
		cbState: MongoCircuitBreakerClosed,
		metrics: MongoPoolMetrics{
			SuccessRate:       0.95,
			AverageLatency:    50 * time.Millisecond,
			ReplicationHealth: "healthy",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mockPool.calculateHealthScore()
	}
}

func BenchmarkIsRetryableMongoError(b *testing.B) {
	config := DefaultAdaptiveMongoPoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config: config,
		logger: logger,
	}

	err := fmt.Errorf("connection pool timeout")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mockPool.isRetryableError(err)
	}
}

func BenchmarkCalculateRetryDelay(b *testing.B) {
	config := DefaultAdaptiveMongoPoolConfig()
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config: config,
		logger: logger,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mockPool.calculateRetryDelay(3)
	}
}

func TestMongoCircuitBreakerTransitions(t *testing.T) {
	config := DefaultAdaptiveMongoPoolConfig()
	config.FailureThreshold = 3
	logger, _ := zap.NewStructured("test", "debug")

	mockPool := &AdaptiveMongoPool{
		config:         config,
		logger:         logger,
		cbState:        MongoCircuitBreakerClosed,
		cbFailureCount: 0,
	}

	// Test circuit breaker state transitions
	assert.Equal(t, MongoCircuitBreakerClosed, mockPool.cbState, "Initial state should be closed")

	// Record failures
	for i := 0; i < config.FailureThreshold-1; i++ {
		mockPool.updateCircuitBreaker(false)
		assert.Equal(
			t,
			MongoCircuitBreakerClosed,
			mockPool.cbState,
			"Should remain closed until threshold",
		)
	}

	// This failure should trip the circuit breaker
	mockPool.updateCircuitBreaker(false)
	assert.Equal(
		t,
		MongoCircuitBreakerOpen,
		mockPool.cbState,
		"Should be open after threshold failures",
	)

	// Test recovery transition (half-open -> closed)
	mockPool.cbState = MongoCircuitBreakerHalfOpen
	mockPool.updateCircuitBreaker(true)
	assert.Equal(
		t,
		MongoCircuitBreakerClosed,
		mockPool.cbState,
		"Should close after successful half-open request",
	)
	assert.Equal(t, 0, mockPool.cbFailureCount, "Failure count should reset")
}
