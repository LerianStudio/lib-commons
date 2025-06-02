package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// AdaptivePoolConfig defines configuration for adaptive connection pooling
type AdaptivePoolConfig struct {
	// Basic pool settings
	MinConnections     int           `json:"min_connections"`
	MaxConnections     int           `json:"max_connections"`
	InitialConnections int           `json:"initial_connections"`
	ConnectionTimeout  time.Duration `json:"connection_timeout"`
	IdleTimeout        time.Duration `json:"idle_timeout"`
	MaxLifetime        time.Duration `json:"max_lifetime"`

	// Adaptive scaling settings
	ScaleUpThreshold   float64       `json:"scale_up_threshold"`   // CPU utilization threshold to scale up (0.0-1.0)
	ScaleDownThreshold float64       `json:"scale_down_threshold"` // CPU utilization threshold to scale down (0.0-1.0)
	ScaleInterval      time.Duration `json:"scale_interval"`       // How often to check for scaling
	ScaleUpFactor      float64       `json:"scale_up_factor"`      // Factor to increase connections (e.g., 1.5 = 50% increase)
	ScaleDownFactor    float64       `json:"scale_down_factor"`    // Factor to decrease connections (e.g., 0.8 = 20% decrease)

	// Performance monitoring settings
	LatencyThreshold       time.Duration `json:"latency_threshold"`        // Query latency threshold for scaling decisions
	ConnectionUtilization  float64       `json:"connection_utilization"`   // Connection utilization threshold
	QueueWaitThreshold     time.Duration `json:"queue_wait_threshold"`     // Max acceptable queue wait time
	HealthCheckInterval    time.Duration `json:"health_check_interval"`    // Health check frequency
	MetricsRetentionPeriod time.Duration `json:"metrics_retention_period"` // How long to keep metrics

	// Circuit breaker settings
	EnableCircuitBreaker bool          `json:"enable_circuit_breaker"`
	FailureThreshold     int           `json:"failure_threshold"`
	RecoveryTimeout      time.Duration `json:"recovery_timeout"`

	// Retry settings
	MaxRetries    int           `json:"max_retries"`
	RetryDelay    time.Duration `json:"retry_delay"`
	RetryJitter   bool          `json:"retry_jitter"`
	BackoffFactor float64       `json:"backoff_factor"`
}

// DefaultAdaptivePoolConfig returns a production-ready default configuration
func DefaultAdaptivePoolConfig() AdaptivePoolConfig {
	return AdaptivePoolConfig{
		// Basic pool settings
		MinConnections:     5,
		MaxConnections:     100,
		InitialConnections: 10,
		ConnectionTimeout:  30 * time.Second,
		IdleTimeout:        15 * time.Minute,
		MaxLifetime:        1 * time.Hour,

		// Adaptive scaling settings
		ScaleUpThreshold:   0.75, // Scale up when 75% utilized
		ScaleDownThreshold: 0.25, // Scale down when 25% utilized
		ScaleInterval:      30 * time.Second,
		ScaleUpFactor:      1.5, // 50% increase
		ScaleDownFactor:    0.8, // 20% decrease

		// Performance monitoring settings
		LatencyThreshold:       100 * time.Millisecond,
		ConnectionUtilization:  0.8,
		QueueWaitThreshold:     5 * time.Second,
		HealthCheckInterval:    1 * time.Minute,
		MetricsRetentionPeriod: 24 * time.Hour,

		// Circuit breaker settings
		EnableCircuitBreaker: true,
		FailureThreshold:     10,
		RecoveryTimeout:      60 * time.Second,

		// Retry settings
		MaxRetries:    3,
		RetryDelay:    1 * time.Second,
		RetryJitter:   true,
		BackoffFactor: 2.0,
	}
}

// PoolMetrics contains real-time pool performance metrics
type PoolMetrics struct {
	// Connection metrics
	ActiveConnections int       `json:"active_connections"`
	IdleConnections   int       `json:"idle_connections"`
	TotalConnections  int       `json:"total_connections"`
	MaxConnections    int       `json:"max_connections"`
	LastScalingAction time.Time `json:"last_scaling_action"`
	ScalingDirection  string    `json:"scaling_direction"` // "up", "down", or "none"

	// Performance metrics
	AverageLatency        time.Duration `json:"average_latency"`
	P95Latency            time.Duration `json:"p95_latency"`
	P99Latency            time.Duration `json:"p99_latency"`
	ConnectionUtilization float64       `json:"connection_utilization"`
	QueueWaitTime         time.Duration `json:"queue_wait_time"`
	ThroughputQPS         float64       `json:"throughput_qps"`

	// Error metrics
	FailedConnections    int64     `json:"failed_connections"`
	TimeoutConnections   int64     `json:"timeout_connections"`
	CircuitBreakerStatus string    `json:"circuit_breaker_status"`
	LastError            string    `json:"last_error"`
	LastErrorTime        time.Time `json:"last_error_time"`

	// Operational metrics
	TotalQueries        int64   `json:"total_queries"`
	SuccessfulQueries   int64   `json:"successful_queries"`
	FailedQueries       int64   `json:"failed_queries"`
	SuccessRate         float64 `json:"success_rate"`
	UptimeSeconds       float64 `json:"uptime_seconds"`
	PoolEfficiency      float64 `json:"pool_efficiency"`      // Successful connections / Total attempts
	ResourceUtilization float64 `json:"resource_utilization"` // CPU/Memory utilization
	ConnectionTurnover  float64 `json:"connection_turnover"`  // Connection churn rate
	HealthScore         float64 `json:"health_score"`         // Overall health (0.0-1.0)
}

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerOpen
	CircuitBreakerHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerOpen:
		return "open"
	case CircuitBreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// AdaptivePostgresPool manages database connections with adaptive scaling and comprehensive monitoring
type AdaptivePostgresPool struct {
	config         AdaptivePoolConfig
	db             *sql.DB
	logger         log.Logger
	mu             sync.RWMutex
	metrics        PoolMetrics
	latencyHistory []time.Duration
	startTime      time.Time

	// Circuit breaker state
	cbState        CircuitBreakerState
	cbFailureCount int
	cbLastFailure  time.Time

	// Adaptive scaling state
	scalingMutex     sync.Mutex
	lastScalingCheck time.Time

	// Background monitoring
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewAdaptivePostgresPool creates a new adaptive PostgreSQL connection pool
func NewAdaptivePostgresPool(
	dsn string,
	config AdaptivePoolConfig,
	logger log.Logger,
) (*AdaptivePostgresPool, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		logger.Error("failed to open database connection", zap.Error(err))
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure initial pool settings
	db.SetMaxOpenConns(config.InitialConnections)
	db.SetMaxIdleConns(config.MinConnections)
	db.SetConnMaxLifetime(config.MaxLifetime)
	db.SetConnMaxIdleTime(config.IdleTimeout)

	// Test initial connection
	ctx, cancel := context.WithTimeout(context.Background(), config.ConnectionTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		logger.Error("initial database ping failed", zap.Error(err))
		return nil, fmt.Errorf("initial connection test failed: %w", err)
	}

	poolCtx, poolCancel := context.WithCancel(context.Background())

	pool := &AdaptivePostgresPool{
		config:           config,
		db:               db,
		logger:           logger,
		startTime:        time.Now(),
		ctx:              poolCtx,
		cancel:           poolCancel,
		cbState:          CircuitBreakerClosed,
		latencyHistory:   make([]time.Duration, 0, 1000),
		lastScalingCheck: time.Now(),
		metrics: PoolMetrics{
			MaxConnections:       config.MaxConnections,
			TotalConnections:     config.InitialConnections,
			CircuitBreakerStatus: CircuitBreakerClosed.String(),
			LastScalingAction:    time.Now(),
			ScalingDirection:     "none",
		},
	}

	// Start background monitoring
	pool.startMonitoring()

	logger.Info("adaptive PostgreSQL pool initialized",
		zap.Int("initial_connections", config.InitialConnections),
		zap.Int("min_connections", config.MinConnections),
		zap.Int("max_connections", config.MaxConnections),
	)

	return pool, nil
}

// GetDB returns the underlying database connection for querying
func (p *AdaptivePostgresPool) GetDB() *sql.DB {
	return p.db
}

// ExecuteQuery executes a query with automatic retry and circuit breaker protection
func (p *AdaptivePostgresPool) ExecuteQuery(
	ctx context.Context,
	query string,
	args ...interface{},
) (*sql.Rows, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordQueryLatency(latency)
	}()

	// Execute with retry logic
	var rows *sql.Rows
	var err error

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Calculate retry delay with jitter
			delay := time.Duration(float64(p.config.RetryDelay) *
				float64(attempt) * p.config.BackoffFactor)

			if p.config.RetryJitter {
				jitter := time.Duration(float64(delay) * 0.1) // 10% jitter
				delay += jitter
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		// Add query timeout
		queryCtx, cancel := context.WithTimeout(ctx, p.config.ConnectionTimeout)
		rows, err = p.db.QueryContext(queryCtx, query, args...)
		cancel()

		if err == nil {
			p.recordSuccessfulQuery()
			return rows, nil
		}

		// Check if error is retryable
		if !p.isRetryableError(err) {
			break
		}

		p.logger.Warn("query failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", p.config.MaxRetries),
		)
	}

	// Record failure and update circuit breaker
	p.recordFailedQuery(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("query failed after %d attempts: %w", p.config.MaxRetries+1, err)
}

// ExecQuery executes a non-query statement with retry and monitoring
func (p *AdaptivePostgresPool) ExecQuery(
	ctx context.Context,
	query string,
	args ...interface{},
) (sql.Result, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordQueryLatency(latency)
	}()

	// Execute with retry logic
	var result sql.Result
	var err error

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(float64(p.config.RetryDelay) *
				float64(attempt) * p.config.BackoffFactor)

			if p.config.RetryJitter {
				jitter := time.Duration(float64(delay) * 0.1)
				delay += jitter
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		queryCtx, cancel := context.WithTimeout(ctx, p.config.ConnectionTimeout)
		result, err = p.db.ExecContext(queryCtx, query, args...)
		cancel()

		if err == nil {
			p.recordSuccessfulQuery()
			return result, nil
		}

		if !p.isRetryableError(err) {
			break
		}

		p.logger.Warn("exec failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
		)
	}

	p.recordFailedQuery(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("exec failed after %d attempts: %w", p.config.MaxRetries+1, err)
}

// GetMetrics returns current pool performance metrics
func (p *AdaptivePostgresPool) GetMetrics() PoolMetrics {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Update real-time metrics
	stats := p.db.Stats()
	p.metrics.ActiveConnections = stats.InUse
	p.metrics.IdleConnections = stats.Idle
	p.metrics.TotalConnections = stats.OpenConnections
	p.metrics.ConnectionUtilization = float64(stats.InUse) / float64(stats.MaxOpenConnections)
	p.metrics.UptimeSeconds = time.Since(p.startTime).Seconds()

	// Calculate success rate
	if p.metrics.TotalQueries > 0 {
		p.metrics.SuccessRate = float64(
			p.metrics.SuccessfulQueries,
		) / float64(
			p.metrics.TotalQueries,
		)
	}

	// Calculate pool efficiency
	totalAttempts := p.metrics.SuccessfulQueries + p.metrics.FailedQueries
	if totalAttempts > 0 {
		p.metrics.PoolEfficiency = float64(p.metrics.SuccessfulQueries) / float64(totalAttempts)
	}

	// Calculate health score (composite metric)
	p.metrics.HealthScore = p.calculateHealthScore()

	return p.metrics
}

// startMonitoring starts background monitoring and adaptive scaling
func (p *AdaptivePostgresPool) startMonitoring() {
	p.wg.Add(2)

	// Health monitoring goroutine
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.config.HealthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.performHealthCheck()
			}
		}
	}()

	// Adaptive scaling goroutine
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.config.ScaleInterval)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.checkAndScale()
			}
		}
	}()
}

// Close gracefully shuts down the connection pool
func (p *AdaptivePostgresPool) Close() error {
	p.logger.Info("shutting down adaptive PostgreSQL pool")

	// Cancel background monitoring
	p.cancel()

	// Wait for monitoring goroutines to finish
	p.wg.Wait()

	// Close database connections
	if err := p.db.Close(); err != nil {
		p.logger.Error("failed to close database connections", zap.Error(err))
		return err
	}

	p.logger.Info("adaptive PostgreSQL pool shutdown complete")
	return nil
}

// Helper methods

func (p *AdaptivePostgresPool) recordQueryLatency(latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Add to latency history (with size limit)
	p.latencyHistory = append(p.latencyHistory, latency)
	if len(p.latencyHistory) > 1000 {
		p.latencyHistory = p.latencyHistory[100:] // Remove oldest 100 entries
	}

	// Update latency metrics
	p.updateLatencyMetrics()
}

func (p *AdaptivePostgresPool) updateLatencyMetrics() {
	if len(p.latencyHistory) == 0 {
		return
	}

	// Calculate average latency
	var total time.Duration
	for _, latency := range p.latencyHistory {
		total += latency
	}
	p.metrics.AverageLatency = total / time.Duration(len(p.latencyHistory))

	// Calculate percentiles (simplified)
	if len(p.latencyHistory) >= 20 {
		sorted := make([]time.Duration, len(p.latencyHistory))
		copy(sorted, p.latencyHistory)

		// Simple sort for percentiles
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[i] > sorted[j] {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}

		p95Index := int(float64(len(sorted)) * 0.95)
		p99Index := int(float64(len(sorted)) * 0.99)

		p.metrics.P95Latency = sorted[p95Index]
		p.metrics.P99Latency = sorted[p99Index]
	}
}

func (p *AdaptivePostgresPool) recordSuccessfulQuery() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.metrics.TotalQueries++
	p.metrics.SuccessfulQueries++
	p.updateCircuitBreaker(true)
}

func (p *AdaptivePostgresPool) recordFailedQuery(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.metrics.TotalQueries++
	p.metrics.FailedQueries++
	p.metrics.LastError = err.Error()
	p.metrics.LastErrorTime = time.Now()
}

func (p *AdaptivePostgresPool) checkCircuitBreaker() error {
	if !p.config.EnableCircuitBreaker {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	switch p.cbState {
	case CircuitBreakerOpen:
		if time.Since(p.cbLastFailure) > p.config.RecoveryTimeout {
			p.cbState = CircuitBreakerHalfOpen
			return nil
		}
		return fmt.Errorf("circuit breaker is open")
	case CircuitBreakerHalfOpen:
		// Allow one request through
		return nil
	default:
		return nil
	}
}

func (p *AdaptivePostgresPool) updateCircuitBreaker(success bool) {
	if !p.config.EnableCircuitBreaker {
		return
	}

	if success {
		if p.cbState == CircuitBreakerHalfOpen {
			p.cbState = CircuitBreakerClosed
			p.cbFailureCount = 0
		}
	} else {
		p.cbFailureCount++
		p.cbLastFailure = time.Now()

		if p.cbFailureCount >= p.config.FailureThreshold {
			p.cbState = CircuitBreakerOpen
		}
	}

	p.metrics.CircuitBreakerStatus = p.cbState.String()
}

func (p *AdaptivePostgresPool) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Common retryable PostgreSQL errors
	retryablePatterns := []string{
		"connection refused",
		"temporary failure",
		"timeout",
		"network error",
		"connection reset",
		"too many connections",
		"server is starting up",
	}

	for _, pattern := range retryablePatterns {
		if contains(errStr, pattern) {
			return true
		}
	}

	return false
}

func (p *AdaptivePostgresPool) performHealthCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := p.db.PingContext(ctx)
	latency := time.Since(start)

	if err != nil {
		p.logger.Warn("health check failed", zap.Error(err))
		p.recordFailedQuery(err)
	} else {
		p.recordQueryLatency(latency)
	}
}

func (p *AdaptivePostgresPool) checkAndScale() {
	p.scalingMutex.Lock()
	defer p.scalingMutex.Unlock()

	stats := p.db.Stats()
	utilization := float64(stats.InUse) / float64(stats.MaxOpenConnections)

	shouldScaleUp := utilization > p.config.ScaleUpThreshold &&
		stats.MaxOpenConnections < p.config.MaxConnections

	shouldScaleDown := utilization < p.config.ScaleDownThreshold &&
		stats.MaxOpenConnections > p.config.MinConnections

	if shouldScaleUp {
		newMax := int(float64(stats.MaxOpenConnections) * p.config.ScaleUpFactor)
		if newMax > p.config.MaxConnections {
			newMax = p.config.MaxConnections
		}

		p.db.SetMaxOpenConns(newMax)
		p.logger.Info("scaled pool up",
			zap.Int("old_max", stats.MaxOpenConnections),
			zap.Int("new_max", newMax),
			zap.Float64("utilization", utilization),
		)

		p.mu.Lock()
		p.metrics.LastScalingAction = time.Now()
		p.metrics.ScalingDirection = "up"
		p.mu.Unlock()

	} else if shouldScaleDown {
		newMax := int(float64(stats.MaxOpenConnections) * p.config.ScaleDownFactor)
		if newMax < p.config.MinConnections {
			newMax = p.config.MinConnections
		}

		p.db.SetMaxOpenConns(newMax)
		p.logger.Info("scaled pool down",
			zap.Int("old_max", stats.MaxOpenConnections),
			zap.Int("new_max", newMax),
			zap.Float64("utilization", utilization),
		)

		p.mu.Lock()
		p.metrics.LastScalingAction = time.Now()
		p.metrics.ScalingDirection = "down"
		p.mu.Unlock()
	}
}

func (p *AdaptivePostgresPool) calculateHealthScore() float64 {
	// Composite health score based on multiple factors
	var score float64 = 1.0

	// Factor in success rate
	if p.metrics.SuccessRate < 0.95 {
		score *= p.metrics.SuccessRate
	}

	// Factor in latency
	if p.metrics.AverageLatency > p.config.LatencyThreshold {
		latencyPenalty := float64(p.metrics.AverageLatency) / float64(p.config.LatencyThreshold)
		score /= latencyPenalty
	}

	// Factor in circuit breaker state
	if p.cbState == CircuitBreakerOpen {
		score *= 0.1 // Severe penalty for open circuit breaker
	} else if p.cbState == CircuitBreakerHalfOpen {
		score *= 0.5 // Moderate penalty for half-open
	}

	// Ensure score is between 0 and 1
	if score > 1.0 {
		score = 1.0
	} else if score < 0.0 {
		score = 0.0
	}

	return score
}

// Helper function for string contains check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
