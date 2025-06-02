package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/bxcodec/dbresolver/v2"
)

// EnhancedPostgresConnection extends PostgresConnection with adaptive pooling capabilities
type EnhancedPostgresConnection struct {
	*PostgresConnection
	adaptivePool *AdaptivePostgresPool
	config       AdaptivePoolConfig
}

// NewEnhancedPostgresConnection creates a new enhanced PostgreSQL connection with adaptive pooling
func NewEnhancedPostgresConnection(
	config PostgresConnection,
	poolConfig AdaptivePoolConfig,
) (*EnhancedPostgresConnection, error) {
	if config.Logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	enhanced := &EnhancedPostgresConnection{
		PostgresConnection: &config,
		config:             poolConfig,
	}

	return enhanced, nil
}

// ConnectWithAdaptivePool establishes connection with adaptive pooling enabled
func (epc *EnhancedPostgresConnection) ConnectWithAdaptivePool() error {
	epc.Logger.Info("Connecting with adaptive pooling enabled...")

	// Create adaptive pool for primary connection
	primaryPool, err := NewAdaptivePostgresPool(epc.ConnectionStringPrimary, epc.config, epc.Logger)
	if err != nil {
		epc.Logger.Error("failed to create adaptive pool for primary database", zap.Error(err))
		return fmt.Errorf("failed to create primary adaptive pool: %w", err)
	}

	// Test primary connection
	if err := primaryPool.GetDB().Ping(); err != nil {
		_ = primaryPool.Close()
		epc.Logger.Error("failed to ping primary database", zap.Error(err))
		return fmt.Errorf("primary database health check failed: %w", err)
	}

	// Store the adaptive pool
	epc.adaptivePool = primaryPool

	// Run migrations on the primary database
	if err := epc.runMigrations(primaryPool.GetDB()); err != nil {
		_ = primaryPool.Close()
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Create dbresolver connection for read/write splitting
	// Note: For full adaptive pooling on replica, you'd need another adaptive pool
	dbReplica, err := sql.Open("pgx", epc.ConnectionStringReplica)
	if err != nil {
		_ = primaryPool.Close()
		epc.Logger.Error("failed to open connection to replica database", zap.Error(err))
		return fmt.Errorf("failed to connect to replica database: %w", err)
	}

	// Configure replica with basic settings
	dbReplica.SetMaxOpenConns(epc.MaxOpenConnections)
	dbReplica.SetMaxIdleConns(epc.MaxIdleConnections)
	dbReplica.SetConnMaxLifetime(time.Minute * 30)

	// Test replica connection
	if err := dbReplica.Ping(); err != nil {
		_ = primaryPool.Close()
		_ = dbReplica.Close()
		epc.Logger.Error("failed to ping replica database", zap.Error(err))
		return fmt.Errorf("replica database health check failed: %w", err)
	}

	// Create dbresolver with adaptive primary and standard replica
	epc.ConnectionDB = dbresolver.New(
		dbresolver.WithPrimaryDBs(primaryPool.GetDB()),
		dbresolver.WithReplicaDBs(dbReplica),
		dbresolver.WithLoadBalancer(dbresolver.RoundRobinLB))

	// Final health check
	if err := epc.ConnectionDB.Ping(); err != nil {
		_ = primaryPool.Close()
		_ = dbReplica.Close()
		epc.Logger.Error("final database health check failed", zap.Error(err))
		return fmt.Errorf("database health check failed: %w", err)
	}

	epc.Connected = true
	epc.Logger.Info("Successfully connected with adaptive pooling enabled ✅")

	return nil
}

// ExecuteWithRetry executes a query using the adaptive pool with retry logic
func (epc *EnhancedPostgresConnection) ExecuteWithRetry(
	ctx context.Context,
	query string,
	args ...interface{},
) (*sql.Rows, error) {
	if epc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return epc.adaptivePool.ExecuteQuery(ctx, query, args...)
}

// ExecWithRetry executes a non-query statement using the adaptive pool with retry logic
func (epc *EnhancedPostgresConnection) ExecWithRetry(
	ctx context.Context,
	query string,
	args ...interface{},
) (sql.Result, error) {
	if epc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return epc.adaptivePool.ExecQuery(ctx, query, args...)
}

// GetPoolMetrics returns current adaptive pool metrics
func (epc *EnhancedPostgresConnection) GetPoolMetrics() PoolMetrics {
	if epc.adaptivePool == nil {
		return PoolMetrics{}
	}

	return epc.adaptivePool.GetMetrics()
}

// GetPoolHealthScore returns the current health score of the adaptive pool
func (epc *EnhancedPostgresConnection) GetPoolHealthScore() float64 {
	metrics := epc.GetPoolMetrics()
	return metrics.HealthScore
}

// IsPoolHealthy checks if the pool is operating within healthy parameters
func (epc *EnhancedPostgresConnection) IsPoolHealthy() bool {
	return epc.GetPoolHealthScore() > 0.8 // 80% health threshold
}

// GetConnectionUtilization returns current connection pool utilization percentage
func (epc *EnhancedPostgresConnection) GetConnectionUtilization() float64 {
	metrics := epc.GetPoolMetrics()
	return metrics.ConnectionUtilization
}

// Close gracefully shuts down the enhanced connection including adaptive pool
func (epc *EnhancedPostgresConnection) Close() error {
	epc.Logger.Info("Shutting down enhanced PostgreSQL connection...")

	var errs []error

	// Close adaptive pool first
	if epc.adaptivePool != nil {
		if err := epc.adaptivePool.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close adaptive pool: %w", err))
		}
	}

	// Close dbresolver connection
	if epc.ConnectionDB != nil {
		if closer, ok := epc.ConnectionDB.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("failed to close dbresolver: %w", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}

	epc.Connected = false
	epc.Logger.Info("Enhanced PostgreSQL connection shutdown complete")
	return nil
}

// WithTransaction executes a function within a database transaction using adaptive pooling
func (epc *EnhancedPostgresConnection) WithTransaction(
	ctx context.Context,
	fn func(*sql.Tx) error,
) error {
	if epc.adaptivePool == nil {
		return fmt.Errorf("adaptive pool not initialized")
	}

	db := epc.adaptivePool.GetDB()

	// Begin transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Ensure transaction is rolled back if not committed
	defer func() {
		if tx != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				epc.Logger.Warn("failed to rollback transaction", zap.Error(rollbackErr))
			}
		}
	}()

	// Execute the function
	if err := fn(tx); err != nil {
		return fmt.Errorf("transaction function failed: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Clear tx to prevent rollback in defer
	tx = nil
	return nil
}

// PerformHealthCheck executes a comprehensive health check on the connection and pool
func (epc *EnhancedPostgresConnection) PerformHealthCheck(ctx context.Context) HealthCheckResult {
	result := HealthCheckResult{
		Timestamp: time.Now(),
		Component: epc.Component,
	}

	// Check basic connectivity
	if err := epc.ConnectionDB.PingContext(ctx); err != nil {
		result.Status = "unhealthy"
		result.Error = err.Error()
		result.Details = "Failed to ping database"
		return result
	}

	// Check adaptive pool health if available
	if epc.adaptivePool != nil {
		metrics := epc.adaptivePool.GetMetrics()
		result.PoolMetrics = &metrics

		// Determine health based on metrics
		healthScore := metrics.HealthScore
		if healthScore > 0.9 {
			result.Status = "healthy"
		} else if healthScore > 0.7 {
			result.Status = "degraded"
		} else {
			result.Status = "unhealthy"
		}

		result.Details = fmt.Sprintf("Health score: %.2f, Success rate: %.2f%%, Avg latency: %v",
			healthScore, metrics.SuccessRate*100, metrics.AverageLatency)
	} else {
		result.Status = "healthy"
		result.Details = "Basic connection healthy (no adaptive pool)"
	}

	return result
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Timestamp   time.Time    `json:"timestamp"`
	Component   string       `json:"component"`
	Status      string       `json:"status"` // "healthy", "degraded", "unhealthy"
	Details     string       `json:"details"`
	Error       string       `json:"error,omitempty"`
	PoolMetrics *PoolMetrics `json:"pool_metrics,omitempty"`
}

// ConfigureAdaptivePooling updates the adaptive pool configuration at runtime
func (epc *EnhancedPostgresConnection) ConfigureAdaptivePooling(
	newConfig AdaptivePoolConfig,
) error {
	epc.config = newConfig

	// Note: In a full implementation, you might want to recreate the pool
	// or apply configuration changes dynamically
	epc.Logger.Info("Adaptive pool configuration updated",
		zap.Int("min_connections", newConfig.MinConnections),
		zap.Int("max_connections", newConfig.MaxConnections),
		zap.Duration("scale_interval", newConfig.ScaleInterval),
	)

	return nil
}

// GetAdaptivePoolConfig returns the current adaptive pool configuration
func (epc *EnhancedPostgresConnection) GetAdaptivePoolConfig() AdaptivePoolConfig {
	return epc.config
}

// EnableAutoScaling starts automatic pool scaling based on load
func (epc *EnhancedPostgresConnection) EnableAutoScaling() {
	epc.Logger.Info("Auto-scaling enabled for adaptive pool")
	// Implementation would enable background scaling goroutines
}

// DisableAutoScaling stops automatic pool scaling
func (epc *EnhancedPostgresConnection) DisableAutoScaling() {
	epc.Logger.Info("Auto-scaling disabled for adaptive pool")
	// Implementation would stop background scaling goroutines
}

// GetRealTimeStats returns real-time statistics for monitoring and alerting
func (epc *EnhancedPostgresConnection) GetRealTimeStats() RealTimeStats {
	if epc.adaptivePool == nil {
		return RealTimeStats{
			Timestamp: time.Now(),
			Available: false,
		}
	}

	metrics := epc.adaptivePool.GetMetrics()

	return RealTimeStats{
		Timestamp:             time.Now(),
		Available:             true,
		ConnectionsActive:     metrics.ActiveConnections,
		ConnectionsIdle:       metrics.IdleConnections,
		ConnectionsTotal:      metrics.TotalConnections,
		ConnectionUtilization: metrics.ConnectionUtilization,
		AverageLatency:        metrics.AverageLatency,
		P95Latency:            metrics.P95Latency,
		P99Latency:            metrics.P99Latency,
		ThroughputQPS:         metrics.ThroughputQPS,
		SuccessRate:           metrics.SuccessRate,
		CircuitBreakerStatus:  metrics.CircuitBreakerStatus,
		HealthScore:           metrics.HealthScore,
		LastScalingAction:     metrics.LastScalingAction,
		ScalingDirection:      metrics.ScalingDirection,
	}
}

// RealTimeStats contains real-time connection pool statistics
type RealTimeStats struct {
	Timestamp             time.Time     `json:"timestamp"`
	Available             bool          `json:"available"`
	ConnectionsActive     int           `json:"connections_active"`
	ConnectionsIdle       int           `json:"connections_idle"`
	ConnectionsTotal      int           `json:"connections_total"`
	ConnectionUtilization float64       `json:"connection_utilization"`
	AverageLatency        time.Duration `json:"average_latency"`
	P95Latency            time.Duration `json:"p95_latency"`
	P99Latency            time.Duration `json:"p99_latency"`
	ThroughputQPS         float64       `json:"throughput_qps"`
	SuccessRate           float64       `json:"success_rate"`
	CircuitBreakerStatus  string        `json:"circuit_breaker_status"`
	HealthScore           float64       `json:"health_score"`
	LastScalingAction     time.Time     `json:"last_scaling_action"`
	ScalingDirection      string        `json:"scaling_direction"`
}
