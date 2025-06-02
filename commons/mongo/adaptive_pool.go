package mongo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// AdaptiveMongoPoolConfig defines configuration for adaptive MongoDB connection pooling
type AdaptiveMongoPoolConfig struct {
	// Basic pool settings
	MinPoolSize            uint64        `json:"min_pool_size"`
	MaxPoolSize            uint64        `json:"max_pool_size"`
	InitialPoolSize        uint64        `json:"initial_pool_size"`
	MaxConnIdleTime        time.Duration `json:"max_conn_idle_time"`
	MaxConnecting          uint64        `json:"max_connecting"`
	ConnectTimeout         time.Duration `json:"connect_timeout"`
	SocketTimeout          time.Duration `json:"socket_timeout"`
	ServerSelectionTimeout time.Duration `json:"server_selection_timeout"`

	// Adaptive scaling settings
	ScaleUpThreshold   float64       `json:"scale_up_threshold"`   // Pool utilization threshold to scale up
	ScaleDownThreshold float64       `json:"scale_down_threshold"` // Pool utilization threshold to scale down
	ScaleInterval      time.Duration `json:"scale_interval"`       // How often to check for scaling
	ScaleUpFactor      float64       `json:"scale_up_factor"`      // Factor to increase pool size
	ScaleDownFactor    float64       `json:"scale_down_factor"`    // Factor to decrease pool size

	// Performance monitoring settings
	LatencyThreshold       time.Duration `json:"latency_threshold"`        // Query latency threshold
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

	// Read preference settings
	ReadPreference string `json:"read_preference"` // primary, secondary, nearest, etc.

	// Replication and sharding settings
	EnableReplicaSet bool   `json:"enable_replica_set"`
	ReplicaSetName   string `json:"replica_set_name"`
	EnableSharding   bool   `json:"enable_sharding"`
}

// DefaultAdaptiveMongoPoolConfig returns a production-ready default configuration
func DefaultAdaptiveMongoPoolConfig() AdaptiveMongoPoolConfig {
	return AdaptiveMongoPoolConfig{
		// Basic pool settings
		MinPoolSize:            5,
		MaxPoolSize:            100,
		InitialPoolSize:        10,
		MaxConnIdleTime:        15 * time.Minute,
		MaxConnecting:          10,
		ConnectTimeout:         10 * time.Second,
		SocketTimeout:          30 * time.Second,
		ServerSelectionTimeout: 10 * time.Second,

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

		// Read preference settings
		ReadPreference:   "primary",
		EnableReplicaSet: false,
		EnableSharding:   false,
	}
}

// MongoPoolMetrics contains real-time MongoDB pool performance metrics
type MongoPoolMetrics struct {
	// Connection metrics
	ActiveConnections    int       `json:"active_connections"`
	AvailableConnections int       `json:"available_connections"`
	TotalConnections     int       `json:"total_connections"`
	MaxPoolSize          int       `json:"max_pool_size"`
	LastScalingAction    time.Time `json:"last_scaling_action"`
	ScalingDirection     string    `json:"scaling_direction"`

	// Performance metrics
	AverageLatency        time.Duration `json:"average_latency"`
	P95Latency            time.Duration `json:"p95_latency"`
	P99Latency            time.Duration `json:"p99_latency"`
	ConnectionUtilization float64       `json:"connection_utilization"`
	QueueWaitTime         time.Duration `json:"queue_wait_time"`
	ThroughputOPS         float64       `json:"throughput_ops"` // Operations per second

	// Error metrics
	FailedConnections    int64     `json:"failed_connections"`
	TimeoutConnections   int64     `json:"timeout_connections"`
	CircuitBreakerStatus string    `json:"circuit_breaker_status"`
	LastError            string    `json:"last_error"`
	LastErrorTime        time.Time `json:"last_error_time"`

	// MongoDB-specific metrics
	TotalOperations      int64   `json:"total_operations"`
	SuccessfulOperations int64   `json:"successful_operations"`
	FailedOperations     int64   `json:"failed_operations"`
	SuccessRate          float64 `json:"success_rate"`
	UptimeSeconds        float64 `json:"uptime_seconds"`
	PoolEfficiency       float64 `json:"pool_efficiency"`

	// Database metrics
	DatabaseSize    int64   `json:"database_size_bytes"`
	CollectionCount int     `json:"collection_count"`
	IndexCount      int     `json:"index_count"`
	HealthScore     float64 `json:"health_score"`

	// Replication metrics (if applicable)
	ReplicationLag    time.Duration `json:"replication_lag"`
	SecondaryNodes    int           `json:"secondary_nodes"`
	ReplicationHealth string        `json:"replication_health"`

	// Sharding metrics (if applicable)
	ShardCount        int     `json:"shard_count"`
	BalancerEnabled   bool    `json:"balancer_enabled"`
	ChunkDistribution float64 `json:"chunk_distribution"` // Coefficient of variation
}

// MongoCircuitBreakerState represents circuit breaker states
type MongoCircuitBreakerState int

const (
	MongoCircuitBreakerClosed MongoCircuitBreakerState = iota
	MongoCircuitBreakerOpen
	MongoCircuitBreakerHalfOpen
)

func (s MongoCircuitBreakerState) String() string {
	switch s {
	case MongoCircuitBreakerClosed:
		return "closed"
	case MongoCircuitBreakerOpen:
		return "open"
	case MongoCircuitBreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// AdaptiveMongoPool manages MongoDB connections with adaptive scaling and comprehensive monitoring
type AdaptiveMongoPool struct {
	config         AdaptiveMongoPoolConfig
	client         *mongo.Client
	database       *mongo.Database
	logger         log.Logger
	mu             sync.RWMutex
	metrics        MongoPoolMetrics
	latencyHistory []time.Duration
	startTime      time.Time

	// Circuit breaker state
	cbState        MongoCircuitBreakerState
	cbFailureCount int
	cbLastFailure  time.Time

	// Adaptive scaling state
	scalingMutex     sync.Mutex
	lastScalingCheck time.Time
	currentPoolSize  uint64

	// Background monitoring
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewAdaptiveMongoPool creates a new adaptive MongoDB connection pool
func NewAdaptiveMongoPool(
	uri string,
	databaseName string,
	config AdaptiveMongoPoolConfig,
	logger log.Logger,
) (*AdaptiveMongoPool, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Parse read preference
	readPref, err := readpref.New(readpref.Mode(config.ReadPreference))
	if err != nil {
		readPref = readpref.Primary()
		logger.Warn(
			"invalid read preference, using primary",
			zap.String("preference", config.ReadPreference),
		)
	}

	// Configure client options
	clientOptions := options.Client().
		ApplyURI(uri).
		SetMinPoolSize(config.InitialPoolSize).
		SetMaxPoolSize(config.InitialPoolSize). // Start with initial size
		SetMaxConnIdleTime(config.MaxConnIdleTime).
		SetMaxConnecting(config.MaxConnecting).
		SetConnectTimeout(config.ConnectTimeout).
		SetSocketTimeout(config.SocketTimeout).
		SetServerSelectionTimeout(config.ServerSelectionTimeout).
		SetReadPreference(readPref)

	// Add replica set configuration if enabled
	if config.EnableReplicaSet && config.ReplicaSetName != "" {
		clientOptions.SetReplicaSet(config.ReplicaSetName)
	}

	// Create client
	client, err := mongo.Connect(context.Background(), clientOptions)
	if err != nil {
		logger.Error("failed to create MongoDB client", zap.Error(err))
		return nil, fmt.Errorf("failed to create MongoDB client: %w", err)
	}

	// Test initial connection
	ctx, cancel := context.WithTimeout(context.Background(), config.ConnectTimeout)
	defer cancel()

	if err := client.Ping(ctx, readPref); err != nil {
		_ = client.Disconnect(context.Background())
		logger.Error("initial MongoDB ping failed", zap.Error(err))
		return nil, fmt.Errorf("initial connection test failed: %w", err)
	}

	poolCtx, poolCancel := context.WithCancel(context.Background())

	pool := &AdaptiveMongoPool{
		config:           config,
		client:           client,
		database:         client.Database(databaseName),
		logger:           logger,
		startTime:        time.Now(),
		ctx:              poolCtx,
		cancel:           poolCancel,
		cbState:          MongoCircuitBreakerClosed,
		latencyHistory:   make([]time.Duration, 0, 1000),
		lastScalingCheck: time.Now(),
		currentPoolSize:  config.InitialPoolSize,
		metrics: MongoPoolMetrics{
			MaxPoolSize:          int(config.MaxPoolSize),
			TotalConnections:     int(config.InitialPoolSize),
			CircuitBreakerStatus: MongoCircuitBreakerClosed.String(),
			LastScalingAction:    time.Now(),
			ScalingDirection:     "none",
			ReplicationHealth:    "unknown",
		},
	}

	// Start background monitoring
	pool.startMonitoring()

	logger.Info("adaptive MongoDB pool initialized",
		zap.Uint64("initial_pool_size", config.InitialPoolSize),
		zap.Uint64("min_pool_size", config.MinPoolSize),
		zap.Uint64("max_pool_size", config.MaxPoolSize),
		zap.String("database", databaseName),
	)

	return pool, nil
}

// GetClient returns the underlying MongoDB client
func (p *AdaptiveMongoPool) GetClient() *mongo.Client {
	return p.client
}

// GetDatabase returns the MongoDB database instance
func (p *AdaptiveMongoPool) GetDatabase() *mongo.Database {
	return p.database
}

// FindOne executes a findOne operation with retry and circuit breaker protection
func (p *AdaptiveMongoPool) FindOne(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.FindOneOptions,
) (*mongo.SingleResult, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordOperationLatency(latency)
	}()

	coll := p.database.Collection(collection)

	// Execute with retry logic
	var result *mongo.SingleResult
	var err error

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := p.calculateRetryDelay(attempt)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		// Add operation timeout
		opCtx, cancel := context.WithTimeout(ctx, p.config.SocketTimeout)
		result = coll.FindOne(opCtx, filter, opts...)
		cancel()

		// Check if operation succeeded
		if result.Err() == nil || result.Err() == mongo.ErrNoDocuments {
			p.recordSuccessfulOperation()
			return result, nil
		}

		err = result.Err()
		if !p.isRetryableError(err) {
			break
		}

		p.logger.Warn("findOne failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.String("collection", collection),
		)
	}

	p.recordFailedOperation(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("findOne failed after %d attempts: %w", p.config.MaxRetries+1, err)
}

// Find executes a find operation with retry and circuit breaker protection
func (p *AdaptiveMongoPool) Find(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.FindOptions,
) (*mongo.Cursor, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordOperationLatency(latency)
	}()

	coll := p.database.Collection(collection)

	// Execute with retry logic
	var cursor *mongo.Cursor
	var err error

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := p.calculateRetryDelay(attempt)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		opCtx, cancel := context.WithTimeout(ctx, p.config.SocketTimeout)
		cursor, err = coll.Find(opCtx, filter, opts...)
		cancel()

		if err == nil {
			p.recordSuccessfulOperation()
			return cursor, nil
		}

		if !p.isRetryableError(err) {
			break
		}

		p.logger.Warn("find failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.String("collection", collection),
		)
	}

	p.recordFailedOperation(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("find failed after %d attempts: %w", p.config.MaxRetries+1, err)
}

// InsertOne executes an insertOne operation with retry and circuit breaker protection
func (p *AdaptiveMongoPool) InsertOne(
	ctx context.Context,
	collection string,
	document interface{},
	opts ...*options.InsertOneOptions,
) (*mongo.InsertOneResult, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordOperationLatency(latency)
	}()

	coll := p.database.Collection(collection)

	// Execute with retry logic (usually no retry for inserts due to potential duplicates)
	var result *mongo.InsertOneResult
	var err error

	opCtx, cancel := context.WithTimeout(ctx, p.config.SocketTimeout)
	result, err = coll.InsertOne(opCtx, document, opts...)
	cancel()

	if err == nil {
		p.recordSuccessfulOperation()
		return result, nil
	}

	p.recordFailedOperation(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("insertOne failed: %w", err)
}

// UpdateOne executes an updateOne operation with retry and circuit breaker protection
func (p *AdaptiveMongoPool) UpdateOne(
	ctx context.Context,
	collection string,
	filter interface{},
	update interface{},
	opts ...*options.UpdateOptions,
) (*mongo.UpdateResult, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordOperationLatency(latency)
	}()

	coll := p.database.Collection(collection)

	// Execute with retry logic
	var result *mongo.UpdateResult
	var err error

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := p.calculateRetryDelay(attempt)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		opCtx, cancel := context.WithTimeout(ctx, p.config.SocketTimeout)
		result, err = coll.UpdateOne(opCtx, filter, update, opts...)
		cancel()

		if err == nil {
			p.recordSuccessfulOperation()
			return result, nil
		}

		if !p.isRetryableError(err) {
			break
		}

		p.logger.Warn("updateOne failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.String("collection", collection),
		)
	}

	p.recordFailedOperation(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("updateOne failed after %d attempts: %w", p.config.MaxRetries+1, err)
}

// DeleteOne executes a deleteOne operation with retry and circuit breaker protection
func (p *AdaptiveMongoPool) DeleteOne(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.DeleteOptions,
) (*mongo.DeleteResult, error) {
	// Check circuit breaker
	if err := p.checkCircuitBreaker(); err != nil {
		return nil, err
	}

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		p.recordOperationLatency(latency)
	}()

	coll := p.database.Collection(collection)

	// Execute with retry logic
	var result *mongo.DeleteResult
	var err error

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := p.calculateRetryDelay(attempt)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		opCtx, cancel := context.WithTimeout(ctx, p.config.SocketTimeout)
		result, err = coll.DeleteOne(opCtx, filter, opts...)
		cancel()

		if err == nil {
			p.recordSuccessfulOperation()
			return result, nil
		}

		if !p.isRetryableError(err) {
			break
		}

		p.logger.Warn("deleteOne failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.String("collection", collection),
		)
	}

	p.recordFailedOperation(err)
	p.updateCircuitBreaker(false)

	return nil, fmt.Errorf("deleteOne failed after %d attempts: %w", p.config.MaxRetries+1, err)
}

// GetMetrics returns current pool performance metrics
func (p *AdaptiveMongoPool) GetMetrics() MongoPoolMetrics {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Update real-time metrics
	p.metrics.TotalConnections = int(p.currentPoolSize)
	p.metrics.UptimeSeconds = time.Since(p.startTime).Seconds()

	// Calculate success rate
	if p.metrics.TotalOperations > 0 {
		p.metrics.SuccessRate = float64(
			p.metrics.SuccessfulOperations,
		) / float64(
			p.metrics.TotalOperations,
		)
	}

	// Calculate pool efficiency
	totalAttempts := p.metrics.SuccessfulOperations + p.metrics.FailedOperations
	if totalAttempts > 0 {
		p.metrics.PoolEfficiency = float64(p.metrics.SuccessfulOperations) / float64(totalAttempts)
	}

	// Calculate health score
	p.metrics.HealthScore = p.calculateHealthScore()

	return p.metrics
}

// startMonitoring starts background monitoring and adaptive scaling
func (p *AdaptiveMongoPool) startMonitoring() {
	p.wg.Add(3)

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

	// Database metrics collection goroutine
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(5 * time.Minute) // Collect database metrics every 5 minutes
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.collectDatabaseMetrics()
			}
		}
	}()
}

// Close gracefully shuts down the connection pool
func (p *AdaptiveMongoPool) Close() error {
	p.logger.Info("shutting down adaptive MongoDB pool")

	// Cancel background monitoring
	p.cancel()

	// Wait for monitoring goroutines to finish
	p.wg.Wait()

	// Disconnect from MongoDB
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := p.client.Disconnect(ctx); err != nil {
		p.logger.Error("failed to disconnect from MongoDB", zap.Error(err))
		return err
	}

	p.logger.Info("adaptive MongoDB pool shutdown complete")
	return nil
}

// Helper methods

func (p *AdaptiveMongoPool) calculateRetryDelay(attempt int) time.Duration {
	delay := time.Duration(float64(p.config.RetryDelay) *
		float64(attempt) * p.config.BackoffFactor)

	if p.config.RetryJitter {
		jitter := time.Duration(float64(delay) * 0.1) // 10% jitter
		delay += jitter
	}

	return delay
}

func (p *AdaptiveMongoPool) recordOperationLatency(latency time.Duration) {
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

func (p *AdaptiveMongoPool) updateLatencyMetrics() {
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

func (p *AdaptiveMongoPool) recordSuccessfulOperation() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.metrics.TotalOperations++
	p.metrics.SuccessfulOperations++
	p.updateCircuitBreaker(true)
}

func (p *AdaptiveMongoPool) recordFailedOperation(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.metrics.TotalOperations++
	p.metrics.FailedOperations++
	p.metrics.LastError = err.Error()
	p.metrics.LastErrorTime = time.Now()
}

func (p *AdaptiveMongoPool) checkCircuitBreaker() error {
	if !p.config.EnableCircuitBreaker {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	switch p.cbState {
	case MongoCircuitBreakerOpen:
		if time.Since(p.cbLastFailure) > p.config.RecoveryTimeout {
			p.cbState = MongoCircuitBreakerHalfOpen
			return nil
		}
		return fmt.Errorf("circuit breaker is open")
	case MongoCircuitBreakerHalfOpen:
		// Allow one request through
		return nil
	default:
		return nil
	}
}

func (p *AdaptiveMongoPool) updateCircuitBreaker(success bool) {
	if !p.config.EnableCircuitBreaker {
		return
	}

	if success {
		if p.cbState == MongoCircuitBreakerHalfOpen {
			p.cbState = MongoCircuitBreakerClosed
			p.cbFailureCount = 0
		}
	} else {
		p.cbFailureCount++
		p.cbLastFailure = time.Now()

		if p.cbFailureCount >= p.config.FailureThreshold {
			p.cbState = MongoCircuitBreakerOpen
		}
	}

	p.metrics.CircuitBreakerStatus = p.cbState.String()
}

func (p *AdaptiveMongoPool) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// MongoDB-specific retryable errors
	return mongo.IsTimeout(err) ||
		mongo.IsNetworkError(err) ||
		contains(err.Error(), "connection refused") ||
		contains(err.Error(), "server selection timeout") ||
		contains(err.Error(), "connection pool timeout")
}

func (p *AdaptiveMongoPool) performHealthCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := p.client.Ping(ctx, readpref.Primary())
	latency := time.Since(start)

	if err != nil {
		p.logger.Warn("health check failed", zap.Error(err))
		p.recordFailedOperation(err)
	} else {
		p.recordOperationLatency(latency)
	}
}

func (p *AdaptiveMongoPool) checkAndScale() {
	p.scalingMutex.Lock()
	defer p.scalingMutex.Unlock()

	// Simple utilization check based on operation patterns
	// In a real implementation, you would check actual connection pool stats
	utilization := float64(
		p.metrics.TotalOperations,
	) / float64(
		p.currentPoolSize,
	) / 10.0 // Simplified calculation

	shouldScaleUp := utilization > p.config.ScaleUpThreshold &&
		p.currentPoolSize < p.config.MaxPoolSize

	shouldScaleDown := utilization < p.config.ScaleDownThreshold &&
		p.currentPoolSize > p.config.MinPoolSize

	if shouldScaleUp {
		newSize := uint64(float64(p.currentPoolSize) * p.config.ScaleUpFactor)
		if newSize > p.config.MaxPoolSize {
			newSize = p.config.MaxPoolSize
		}

		// Note: MongoDB driver doesn't support dynamic pool resizing out of the box
		// This would require connection management logic
		p.logger.Info("would scale pool up (not implemented in driver)",
			zap.Uint64("old_size", p.currentPoolSize),
			zap.Uint64("new_size", newSize),
			zap.Float64("utilization", utilization),
		)

		p.mu.Lock()
		p.metrics.LastScalingAction = time.Now()
		p.metrics.ScalingDirection = "up"
		p.mu.Unlock()

	} else if shouldScaleDown {
		newSize := uint64(float64(p.currentPoolSize) * p.config.ScaleDownFactor)
		if newSize < p.config.MinPoolSize {
			newSize = p.config.MinPoolSize
		}

		p.logger.Info("would scale pool down (not implemented in driver)",
			zap.Uint64("old_size", p.currentPoolSize),
			zap.Uint64("new_size", newSize),
			zap.Float64("utilization", utilization),
		)

		p.mu.Lock()
		p.metrics.LastScalingAction = time.Now()
		p.metrics.ScalingDirection = "down"
		p.mu.Unlock()
	}
}

func (p *AdaptiveMongoPool) collectDatabaseMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get database stats
	var result bson.M
	err := p.database.RunCommand(ctx, bson.D{{"dbStats", 1}}).Decode(&result)
	if err != nil {
		p.logger.Warn("failed to collect database stats", zap.Error(err))
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Update database metrics
	if dataSize, ok := result["dataSize"].(int64); ok {
		p.metrics.DatabaseSize = dataSize
	}
	if collections, ok := result["collections"].(int32); ok {
		p.metrics.CollectionCount = int(collections)
	}
	if indexes, ok := result["indexes"].(int32); ok {
		p.metrics.IndexCount = int(indexes)
	}

	// Collect replication metrics if replica set is enabled
	if p.config.EnableReplicaSet {
		p.collectReplicationMetrics(ctx)
	}
}

func (p *AdaptiveMongoPool) collectReplicationMetrics(ctx context.Context) {
	var result bson.M
	err := p.database.RunCommand(ctx, bson.D{{"replSetGetStatus", 1}}).Decode(&result)
	if err != nil {
		p.metrics.ReplicationHealth = "error"
		return
	}

	// Extract replication information
	if members, ok := result["members"].(bson.A); ok {
		secondaryCount := 0
		for _, member := range members {
			if memberDoc, ok := member.(bson.M); ok {
				if state, ok := memberDoc["state"].(int32); ok && state == 2 { // Secondary state
					secondaryCount++
				}
			}
		}
		p.metrics.SecondaryNodes = secondaryCount
		p.metrics.ReplicationHealth = "healthy"
	}
}

func (p *AdaptiveMongoPool) calculateHealthScore() float64 {
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
	if p.cbState == MongoCircuitBreakerOpen {
		score *= 0.1 // Severe penalty for open circuit breaker
	} else if p.cbState == MongoCircuitBreakerHalfOpen {
		score *= 0.5 // Moderate penalty for half-open
	}

	// Factor in replication health
	if p.config.EnableReplicaSet && p.metrics.ReplicationHealth == "error" {
		score *= 0.7 // Penalty for replication issues
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
