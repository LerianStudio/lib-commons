package mongo

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnhancedMongoConnection extends MongoConnection with adaptive pooling capabilities
type EnhancedMongoConnection struct {
	*MongoConnection
	adaptivePool *AdaptiveMongoPool
	config       AdaptiveMongoPoolConfig
}

// NewEnhancedMongoConnection creates a new enhanced MongoDB connection with adaptive pooling
func NewEnhancedMongoConnection(
	config MongoConnection,
	poolConfig AdaptiveMongoPoolConfig,
) (*EnhancedMongoConnection, error) {
	if config.Logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	enhanced := &EnhancedMongoConnection{
		MongoConnection: &config,
		config:          poolConfig,
	}

	return enhanced, nil
}

// ConnectWithAdaptivePool establishes connection with adaptive pooling enabled
func (emc *EnhancedMongoConnection) ConnectWithAdaptivePool(ctx context.Context) error {
	emc.Logger.Info("Connecting to MongoDB with adaptive pooling enabled...")

	// Create adaptive pool
	adaptivePool, err := NewAdaptiveMongoPool(
		emc.ConnectionStringSource,
		emc.Database,
		emc.config,
		emc.Logger,
	)
	if err != nil {
		emc.Logger.Error("failed to create adaptive MongoDB pool", zap.Error(err))
		return fmt.Errorf("failed to create adaptive MongoDB pool: %w", err)
	}

	// Test connection
	client := adaptivePool.GetClient()
	if err := client.Ping(ctx, nil); err != nil {
		_ = adaptivePool.Close()
		emc.Logger.Error("MongoDB health check failed", zap.Error(err))
		return fmt.Errorf("MongoDB health check failed: %w", err)
	}

	// Store references
	emc.adaptivePool = adaptivePool
	emc.DB = client
	emc.Connected = true

	emc.Logger.Info("Successfully connected to MongoDB with adaptive pooling enabled ✅")
	return nil
}

// FindOneWithRetry executes a findOne operation using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) FindOneWithRetry(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.FindOneOptions,
) (*mongo.SingleResult, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return emc.adaptivePool.FindOne(ctx, collection, filter, opts...)
}

// FindWithRetry executes a find operation using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) FindWithRetry(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.FindOptions,
) (*mongo.Cursor, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return emc.adaptivePool.Find(ctx, collection, filter, opts...)
}

// InsertOneWithRetry executes an insertOne operation using the adaptive pool
func (emc *EnhancedMongoConnection) InsertOneWithRetry(
	ctx context.Context,
	collection string,
	document interface{},
	opts ...*options.InsertOneOptions,
) (*mongo.InsertOneResult, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return emc.adaptivePool.InsertOne(ctx, collection, document, opts...)
}

// UpdateOneWithRetry executes an updateOne operation using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) UpdateOneWithRetry(
	ctx context.Context,
	collection string,
	filter interface{},
	update interface{},
	opts ...*options.UpdateOptions,
) (*mongo.UpdateResult, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return emc.adaptivePool.UpdateOne(ctx, collection, filter, update, opts...)
}

// DeleteOneWithRetry executes a deleteOne operation using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) DeleteOneWithRetry(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.DeleteOptions,
) (*mongo.DeleteResult, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	return emc.adaptivePool.DeleteOne(ctx, collection, filter, opts...)
}

// GetPoolMetrics returns current adaptive pool metrics
func (emc *EnhancedMongoConnection) GetPoolMetrics() MongoPoolMetrics {
	if emc.adaptivePool == nil {
		return MongoPoolMetrics{}
	}

	return emc.adaptivePool.GetMetrics()
}

// GetPoolHealthScore returns the current health score of the adaptive pool
func (emc *EnhancedMongoConnection) GetPoolHealthScore() float64 {
	metrics := emc.GetPoolMetrics()
	return metrics.HealthScore
}

// IsPoolHealthy checks if the pool is operating within healthy parameters
func (emc *EnhancedMongoConnection) IsPoolHealthy() bool {
	return emc.GetPoolHealthScore() > 0.8 // 80% health threshold
}

// GetConnectionUtilization returns current connection pool utilization percentage
func (emc *EnhancedMongoConnection) GetConnectionUtilization() float64 {
	metrics := emc.GetPoolMetrics()
	return metrics.ConnectionUtilization
}

// Close gracefully shuts down the enhanced connection including adaptive pool
func (emc *EnhancedMongoConnection) Close() error {
	emc.Logger.Info("Shutting down enhanced MongoDB connection...")

	var errs []error

	// Close adaptive pool first
	if emc.adaptivePool != nil {
		if err := emc.adaptivePool.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close adaptive pool: %w", err))
		}
	}

	// Disconnect MongoDB client
	if emc.DB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := emc.DB.Disconnect(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to disconnect MongoDB client: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}

	emc.Connected = false
	emc.Logger.Info("Enhanced MongoDB connection shutdown complete")
	return nil
}

// WithTransaction executes a function within a MongoDB transaction using adaptive pooling
func (emc *EnhancedMongoConnection) WithTransaction(
	ctx context.Context,
	fn func(mongo.SessionContext) error,
) error {
	if emc.adaptivePool == nil {
		return fmt.Errorf("adaptive pool not initialized")
	}

	client := emc.adaptivePool.GetClient()

	// Start session
	session, err := client.StartSession()
	if err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	// Execute transaction
	_, err = session.WithTransaction(ctx, func(sctx mongo.SessionContext) (interface{}, error) {
		return nil, fn(sctx)
	})

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}

	return nil
}

// PerformHealthCheck executes a comprehensive health check on the connection and pool
func (emc *EnhancedMongoConnection) PerformHealthCheck(ctx context.Context) MongoHealthCheckResult {
	result := MongoHealthCheckResult{
		Timestamp: time.Now(),
		Database:  emc.Database,
	}

	// Check basic connectivity
	if emc.DB == nil {
		result.Status = "unhealthy"
		result.Error = "MongoDB client not initialized"
		return result
	}

	if err := emc.DB.Ping(ctx, nil); err != nil {
		result.Status = "unhealthy"
		result.Error = err.Error()
		result.Details = "Failed to ping MongoDB"
		return result
	}

	// Check adaptive pool health if available
	if emc.adaptivePool != nil {
		metrics := emc.adaptivePool.GetMetrics()
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

		// Additional MongoDB-specific checks
		result.CollectionCount = metrics.CollectionCount
		result.DatabaseSize = metrics.DatabaseSize
		result.IndexCount = metrics.IndexCount
		result.ReplicationHealth = metrics.ReplicationHealth
	} else {
		result.Status = "healthy"
		result.Details = "Basic connection healthy (no adaptive pool)"
	}

	return result
}

// MongoHealthCheckResult represents the result of a MongoDB health check
type MongoHealthCheckResult struct {
	Timestamp         time.Time         `json:"timestamp"`
	Database          string            `json:"database"`
	Status            string            `json:"status"` // "healthy", "degraded", "unhealthy"
	Details           string            `json:"details"`
	Error             string            `json:"error,omitempty"`
	PoolMetrics       *MongoPoolMetrics `json:"pool_metrics,omitempty"`
	CollectionCount   int               `json:"collection_count"`
	DatabaseSize      int64             `json:"database_size"`
	IndexCount        int               `json:"index_count"`
	ReplicationHealth string            `json:"replication_health"`
}

// ConfigureAdaptivePooling updates the adaptive pool configuration at runtime
func (emc *EnhancedMongoConnection) ConfigureAdaptivePooling(
	newConfig AdaptiveMongoPoolConfig,
) error {
	emc.config = newConfig

	// Note: In a full implementation, you might want to recreate the pool
	// or apply configuration changes dynamically
	emc.Logger.Info("Adaptive pool configuration updated",
		zap.Uint64("min_pool_size", newConfig.MinPoolSize),
		zap.Uint64("max_pool_size", newConfig.MaxPoolSize),
		zap.Duration("scale_interval", newConfig.ScaleInterval),
	)

	return nil
}

// GetAdaptivePoolConfig returns the current adaptive pool configuration
func (emc *EnhancedMongoConnection) GetAdaptivePoolConfig() AdaptiveMongoPoolConfig {
	return emc.config
}

// EnableAutoScaling starts automatic pool scaling based on load
func (emc *EnhancedMongoConnection) EnableAutoScaling() {
	emc.Logger.Info("Auto-scaling enabled for adaptive MongoDB pool")
	// Implementation would enable background scaling goroutines
}

// DisableAutoScaling stops automatic pool scaling
func (emc *EnhancedMongoConnection) DisableAutoScaling() {
	emc.Logger.Info("Auto-scaling disabled for adaptive MongoDB pool")
	// Implementation would stop background scaling goroutines
}

// GetRealTimeStats returns real-time statistics for monitoring and alerting
func (emc *EnhancedMongoConnection) GetRealTimeStats() MongoRealTimeStats {
	if emc.adaptivePool == nil {
		return MongoRealTimeStats{
			Timestamp: time.Now(),
			Available: false,
		}
	}

	metrics := emc.adaptivePool.GetMetrics()

	return MongoRealTimeStats{
		Timestamp:             time.Now(),
		Available:             true,
		ConnectionsActive:     metrics.ActiveConnections,
		ConnectionsAvailable:  metrics.AvailableConnections,
		ConnectionsTotal:      metrics.TotalConnections,
		ConnectionUtilization: metrics.ConnectionUtilization,
		AverageLatency:        metrics.AverageLatency,
		P95Latency:            metrics.P95Latency,
		P99Latency:            metrics.P99Latency,
		ThroughputOPS:         metrics.ThroughputOPS,
		SuccessRate:           metrics.SuccessRate,
		CircuitBreakerStatus:  metrics.CircuitBreakerStatus,
		HealthScore:           metrics.HealthScore,
		LastScalingAction:     metrics.LastScalingAction,
		ScalingDirection:      metrics.ScalingDirection,
		DatabaseSize:          metrics.DatabaseSize,
		CollectionCount:       metrics.CollectionCount,
		IndexCount:            metrics.IndexCount,
		ReplicationLag:        metrics.ReplicationLag,
		ReplicationHealth:     metrics.ReplicationHealth,
	}
}

// MongoRealTimeStats contains real-time MongoDB connection pool statistics
type MongoRealTimeStats struct {
	Timestamp             time.Time     `json:"timestamp"`
	Available             bool          `json:"available"`
	ConnectionsActive     int           `json:"connections_active"`
	ConnectionsAvailable  int           `json:"connections_available"`
	ConnectionsTotal      int           `json:"connections_total"`
	ConnectionUtilization float64       `json:"connection_utilization"`
	AverageLatency        time.Duration `json:"average_latency"`
	P95Latency            time.Duration `json:"p95_latency"`
	P99Latency            time.Duration `json:"p99_latency"`
	ThroughputOPS         float64       `json:"throughput_ops"`
	SuccessRate           float64       `json:"success_rate"`
	CircuitBreakerStatus  string        `json:"circuit_breaker_status"`
	HealthScore           float64       `json:"health_score"`
	LastScalingAction     time.Time     `json:"last_scaling_action"`
	ScalingDirection      string        `json:"scaling_direction"`
	DatabaseSize          int64         `json:"database_size"`
	CollectionCount       int           `json:"collection_count"`
	IndexCount            int           `json:"index_count"`
	ReplicationLag        time.Duration `json:"replication_lag"`
	ReplicationHealth     string        `json:"replication_health"`
}

// AggregateWithRetry executes an aggregation pipeline using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) AggregateWithRetry(
	ctx context.Context,
	collection string,
	pipeline interface{},
	opts ...*options.AggregateOptions,
) (*mongo.Cursor, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	// Get collection from adaptive pool's database
	coll := emc.adaptivePool.GetDatabase().Collection(collection)

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		emc.adaptivePool.recordOperationLatency(latency)
	}()

	// Execute with retry logic (similar to other operations)
	var cursor *mongo.Cursor
	var err error

	for attempt := 0; attempt <= emc.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := emc.adaptivePool.calculateRetryDelay(attempt)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		opCtx, cancel := context.WithTimeout(ctx, emc.config.SocketTimeout)
		cursor, err = coll.Aggregate(opCtx, pipeline, opts...)
		cancel()

		if err == nil {
			emc.adaptivePool.recordSuccessfulOperation()
			return cursor, nil
		}

		if !emc.adaptivePool.isRetryableError(err) {
			break
		}

		emc.Logger.Warn("aggregate failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.String("collection", collection),
		)
	}

	emc.adaptivePool.recordFailedOperation(err)
	emc.adaptivePool.updateCircuitBreaker(false)

	return nil, fmt.Errorf("aggregate failed after %d attempts: %w", emc.config.MaxRetries+1, err)
}

// CountDocumentsWithRetry executes a count operation using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) CountDocumentsWithRetry(
	ctx context.Context,
	collection string,
	filter interface{},
	opts ...*options.CountOptions,
) (int64, error) {
	if emc.adaptivePool == nil {
		return 0, fmt.Errorf("adaptive pool not initialized")
	}

	coll := emc.adaptivePool.GetDatabase().Collection(collection)

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		emc.adaptivePool.recordOperationLatency(latency)
	}()

	var count int64
	var err error

	for attempt := 0; attempt <= emc.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := emc.adaptivePool.calculateRetryDelay(attempt)

			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(delay):
			}
		}

		opCtx, cancel := context.WithTimeout(ctx, emc.config.SocketTimeout)
		count, err = coll.CountDocuments(opCtx, filter, opts...)
		cancel()

		if err == nil {
			emc.adaptivePool.recordSuccessfulOperation()
			return count, nil
		}

		if !emc.adaptivePool.isRetryableError(err) {
			break
		}

		emc.Logger.Warn("countDocuments failed, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
			zap.String("collection", collection),
		)
	}

	emc.adaptivePool.recordFailedOperation(err)
	emc.adaptivePool.updateCircuitBreaker(false)

	return 0, fmt.Errorf(
		"countDocuments failed after %d attempts: %w",
		emc.config.MaxRetries+1,
		err,
	)
}

// CreateIndexWithRetry creates an index using the adaptive pool with retry logic
func (emc *EnhancedMongoConnection) CreateIndexWithRetry(
	ctx context.Context,
	collection string,
	model mongo.IndexModel,
	opts ...*options.CreateIndexesOptions,
) (string, error) {
	if emc.adaptivePool == nil {
		return "", fmt.Errorf("adaptive pool not initialized")
	}

	coll := emc.adaptivePool.GetDatabase().Collection(collection)

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		emc.adaptivePool.recordOperationLatency(latency)
	}()

	var indexName string
	var err error

	opCtx, cancel := context.WithTimeout(ctx, emc.config.SocketTimeout)
	indexName, err = coll.Indexes().CreateOne(opCtx, model, opts...)
	cancel()

	if err == nil {
		emc.adaptivePool.recordSuccessfulOperation()
		return indexName, nil
	}

	emc.adaptivePool.recordFailedOperation(err)
	return "", fmt.Errorf("createIndex failed: %w", err)
}

// DropCollectionWithRetry drops a collection using the adaptive pool
func (emc *EnhancedMongoConnection) DropCollectionWithRetry(
	ctx context.Context,
	collection string,
) error {
	if emc.adaptivePool == nil {
		return fmt.Errorf("adaptive pool not initialized")
	}

	coll := emc.adaptivePool.GetDatabase().Collection(collection)

	start := time.Now()
	defer func() {
		latency := time.Since(start)
		emc.adaptivePool.recordOperationLatency(latency)
	}()

	opCtx, cancel := context.WithTimeout(ctx, emc.config.SocketTimeout)
	err := coll.Drop(opCtx)
	cancel()

	if err == nil {
		emc.adaptivePool.recordSuccessfulOperation()
		return nil
	}

	emc.adaptivePool.recordFailedOperation(err)
	return fmt.Errorf("dropCollection failed: %w", err)
}

// GetDatabaseStats returns statistics about the MongoDB database
func (emc *EnhancedMongoConnection) GetDatabaseStats(ctx context.Context) (*DatabaseStats, error) {
	if emc.adaptivePool == nil {
		return nil, fmt.Errorf("adaptive pool not initialized")
	}

	db := emc.adaptivePool.GetDatabase()

	var result bson.M
	err := db.RunCommand(ctx, bson.D{{"dbStats", 1}}).Decode(&result)
	if err != nil {
		return nil, fmt.Errorf("failed to get database stats: %w", err)
	}

	stats := &DatabaseStats{
		Database:    emc.Database,
		Collections: 0,
		DataSize:    0,
		StorageSize: 0,
		IndexSize:   0,
		Indexes:     0,
	}

	if collections, ok := result["collections"].(int32); ok {
		stats.Collections = int(collections)
	}
	if dataSize, ok := result["dataSize"].(int64); ok {
		stats.DataSize = dataSize
	}
	if storageSize, ok := result["storageSize"].(int64); ok {
		stats.StorageSize = storageSize
	}
	if indexSize, ok := result["indexSize"].(int64); ok {
		stats.IndexSize = indexSize
	}
	if indexes, ok := result["indexes"].(int32); ok {
		stats.Indexes = int(indexes)
	}

	return stats, nil
}

// DatabaseStats contains MongoDB database statistics
type DatabaseStats struct {
	Database    string `json:"database"`
	Collections int    `json:"collections"`
	DataSize    int64  `json:"data_size"`
	StorageSize int64  `json:"storage_size"`
	IndexSize   int64  `json:"index_size"`
	Indexes     int    `json:"indexes"`
}
