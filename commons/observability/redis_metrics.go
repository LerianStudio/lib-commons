package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RedisMetrics provides Redis-specific metrics collection
type RedisMetrics struct {
	meter metric.Meter

	// Command metrics
	commandCounter      metric.Int64Counter
	commandDuration     metric.Float64Histogram
	commandErrorCounter metric.Int64Counter

	// Connection metrics
	connectionCounter  metric.Int64Counter
	activeConnections  metric.Int64UpDownCounter
	connectionPoolSize metric.Int64ObservableGauge
	connectionPoolUsed metric.Int64ObservableGauge
	connectionErrors   metric.Int64Counter

	// Cache metrics
	cacheHits     metric.Int64Counter
	cacheMisses   metric.Int64Counter
	cacheHitRatio metric.Float64ObservableGauge
	keyCount      metric.Int64ObservableGauge
	memoryUsage   metric.Int64ObservableGauge

	// Operation metrics
	getOperations    metric.Int64Counter
	setOperations    metric.Int64Counter
	deleteOperations metric.Int64Counter
	expiredKeys      metric.Int64Counter
	evictedKeys      metric.Int64Counter

	// Performance metrics
	slowLogCounter metric.Int64Counter
	blockedClients metric.Int64ObservableGauge

	// Cluster metrics (if applicable)
	clusterNodes      metric.Int64ObservableGauge
	clusterSlots      metric.Int64ObservableGauge
	clusterMigrations metric.Int64Counter

	// Persistence metrics
	rdbSaves         metric.Int64Counter
	aofRewrites      metric.Int64Counter
	lastSaveDuration metric.Float64ObservableGauge
}

// NewRedisMetrics creates a new Redis metrics instance
func NewRedisMetrics(meter metric.Meter) (*RedisMetrics, error) {
	rm := &RedisMetrics{meter: meter}

	// Initialize command metrics
	commandCounter, err := meter.Int64Counter(
		"redis.commands.total",
		metric.WithDescription("Total number of Redis commands executed"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command counter: %w", err)
	}
	rm.commandCounter = commandCounter

	commandDuration, err := meter.Float64Histogram(
		"redis.command.duration",
		metric.WithDescription("Redis command execution duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command duration histogram: %w", err)
	}
	rm.commandDuration = commandDuration

	commandErrorCounter, err := meter.Int64Counter(
		"redis.command.errors",
		metric.WithDescription("Total number of Redis command errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command error counter: %w", err)
	}
	rm.commandErrorCounter = commandErrorCounter

	// Initialize connection metrics
	connectionCounter, err := meter.Int64Counter(
		"redis.connections.total",
		metric.WithDescription("Total number of Redis connections created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection counter: %w", err)
	}
	rm.connectionCounter = connectionCounter

	activeConnections, err := meter.Int64UpDownCounter(
		"redis.connections.active",
		metric.WithDescription("Number of active Redis connections"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create active connections counter: %w", err)
	}
	rm.activeConnections = activeConnections

	connectionPoolSize, err := meter.Int64ObservableGauge(
		"redis.connection_pool.size",
		metric.WithDescription("Redis connection pool size"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool size gauge: %w", err)
	}
	rm.connectionPoolSize = connectionPoolSize

	connectionPoolUsed, err := meter.Int64ObservableGauge(
		"redis.connection_pool.used",
		metric.WithDescription("Redis connection pool used connections"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool used gauge: %w", err)
	}
	rm.connectionPoolUsed = connectionPoolUsed

	connectionErrors, err := meter.Int64Counter(
		"redis.connection.errors",
		metric.WithDescription("Total number of Redis connection errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection error counter: %w", err)
	}
	rm.connectionErrors = connectionErrors

	// Initialize cache metrics
	cacheHits, err := meter.Int64Counter(
		"redis.cache.hits",
		metric.WithDescription("Total number of cache hits"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache hits counter: %w", err)
	}
	rm.cacheHits = cacheHits

	cacheMisses, err := meter.Int64Counter(
		"redis.cache.misses",
		metric.WithDescription("Total number of cache misses"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache misses counter: %w", err)
	}
	rm.cacheMisses = cacheMisses

	cacheHitRatio, err := meter.Float64ObservableGauge(
		"redis.cache.hit_ratio",
		metric.WithDescription("Cache hit ratio"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache hit ratio gauge: %w", err)
	}
	rm.cacheHitRatio = cacheHitRatio

	keyCount, err := meter.Int64ObservableGauge(
		"redis.keys.count",
		metric.WithDescription("Number of keys in Redis"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create key count gauge: %w", err)
	}
	rm.keyCount = keyCount

	memoryUsage, err := meter.Int64ObservableGauge(
		"redis.memory.used_bytes",
		metric.WithDescription("Redis memory usage in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create memory usage gauge: %w", err)
	}
	rm.memoryUsage = memoryUsage

	// Initialize operation metrics
	getOperations, err := meter.Int64Counter(
		"redis.operations.get",
		metric.WithDescription("Total number of GET operations"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET operations counter: %w", err)
	}
	rm.getOperations = getOperations

	setOperations, err := meter.Int64Counter(
		"redis.operations.set",
		metric.WithDescription("Total number of SET operations"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create SET operations counter: %w", err)
	}
	rm.setOperations = setOperations

	deleteOperations, err := meter.Int64Counter(
		"redis.operations.delete",
		metric.WithDescription("Total number of DELETE operations"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create DELETE operations counter: %w", err)
	}
	rm.deleteOperations = deleteOperations

	expiredKeys, err := meter.Int64Counter(
		"redis.keys.expired",
		metric.WithDescription("Total number of expired keys"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create expired keys counter: %w", err)
	}
	rm.expiredKeys = expiredKeys

	evictedKeys, err := meter.Int64Counter(
		"redis.keys.evicted",
		metric.WithDescription("Total number of evicted keys"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create evicted keys counter: %w", err)
	}
	rm.evictedKeys = evictedKeys

	// Initialize performance metrics
	slowLogCounter, err := meter.Int64Counter(
		"redis.slow_log.count",
		metric.WithDescription("Total number of slow log entries"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create slow log counter: %w", err)
	}
	rm.slowLogCounter = slowLogCounter

	blockedClients, err := meter.Int64ObservableGauge(
		"redis.clients.blocked",
		metric.WithDescription("Number of blocked clients"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create blocked clients gauge: %w", err)
	}
	rm.blockedClients = blockedClients

	// Initialize cluster metrics
	clusterNodes, err := meter.Int64ObservableGauge(
		"redis.cluster.nodes",
		metric.WithDescription("Number of nodes in Redis cluster"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster nodes gauge: %w", err)
	}
	rm.clusterNodes = clusterNodes

	clusterSlots, err := meter.Int64ObservableGauge(
		"redis.cluster.slots",
		metric.WithDescription("Number of slots in Redis cluster"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster slots gauge: %w", err)
	}
	rm.clusterSlots = clusterSlots

	clusterMigrations, err := meter.Int64Counter(
		"redis.cluster.migrations",
		metric.WithDescription("Total number of cluster slot migrations"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster migrations counter: %w", err)
	}
	rm.clusterMigrations = clusterMigrations

	// Initialize persistence metrics
	rdbSaves, err := meter.Int64Counter(
		"redis.rdb.saves",
		metric.WithDescription("Total number of RDB saves"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create RDB saves counter: %w", err)
	}
	rm.rdbSaves = rdbSaves

	aofRewrites, err := meter.Int64Counter(
		"redis.aof.rewrites",
		metric.WithDescription("Total number of AOF rewrites"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create AOF rewrites counter: %w", err)
	}
	rm.aofRewrites = aofRewrites

	lastSaveDuration, err := meter.Float64ObservableGauge(
		"redis.last_save.duration",
		metric.WithDescription("Duration of last save operation"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create last save duration gauge: %w", err)
	}
	rm.lastSaveDuration = lastSaveDuration

	return rm, nil
}

// RecordCommand records Redis command metrics
func (rm *RedisMetrics) RecordCommand(
	ctx context.Context,
	command string,
	database int,
	duration time.Duration,
	keyCount int,
	isSlowLog bool,
) {
	attrs := []attribute.KeyValue{
		attribute.String("command", command),
		attribute.Int("database", database),
	}

	if keyCount > 0 {
		attrs = append(attrs, attribute.Int("key_count", keyCount))
	}

	rm.commandCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	rm.commandDuration.Record(
		ctx,
		float64(duration.Nanoseconds())/1e6,
		metric.WithAttributes(attrs...),
	)

	if isSlowLog {
		rm.slowLogCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordCommandError records Redis command error metrics
func (rm *RedisMetrics) RecordCommandError(
	ctx context.Context,
	command string,
	database int,
	errorType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("command", command),
		attribute.Int("database", database),
		attribute.String("error_type", errorType),
	}

	rm.commandErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordConnection records Redis connection metrics
func (rm *RedisMetrics) RecordConnection(
	ctx context.Context,
	action string, // "opened", "closed", "failed"
	clientType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("action", action),
		attribute.String("client_type", clientType),
	}

	switch action {
	case "opened":
		rm.connectionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
		rm.activeConnections.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "closed":
		rm.activeConnections.Add(ctx, -1, metric.WithAttributes(attrs...))
	case "failed":
		rm.connectionErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordCacheOperation records cache hit/miss metrics
func (rm *RedisMetrics) RecordCacheOperation(
	ctx context.Context,
	operation string, // "hit", "miss"
	keyType string,
	database int,
) {
	attrs := []attribute.KeyValue{
		attribute.String("key_type", keyType),
		attribute.Int("database", database),
	}

	switch operation {
	case "hit":
		rm.cacheHits.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "miss":
		rm.cacheMisses.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordKeyOperation records key-specific operations
func (rm *RedisMetrics) RecordKeyOperation(
	ctx context.Context,
	operation string, // "get", "set", "delete", "expired", "evicted"
	keyType string,
	database int,
	size int64,
) {
	attrs := []attribute.KeyValue{
		attribute.String("operation", operation),
		attribute.String("key_type", keyType),
		attribute.Int("database", database),
	}

	if size > 0 {
		attrs = append(attrs, attribute.Int64("size_bytes", size))
	}

	switch operation {
	case "get":
		rm.getOperations.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "set":
		rm.setOperations.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "delete":
		rm.deleteOperations.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "expired":
		rm.expiredKeys.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "evicted":
		rm.evictedKeys.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordClusterOperation records cluster-specific operations
func (rm *RedisMetrics) RecordClusterOperation(
	ctx context.Context,
	operation string, // "migration", "failover", "node_added", "node_removed"
	sourceNode string,
	targetNode string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("operation", operation),
	}

	if sourceNode != "" {
		attrs = append(attrs, attribute.String("source_node", sourceNode))
	}
	if targetNode != "" {
		attrs = append(attrs, attribute.String("target_node", targetNode))
	}

	switch operation {
	case "migration":
		rm.clusterMigrations.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordPersistenceOperation records persistence-related operations
func (rm *RedisMetrics) RecordPersistenceOperation(
	ctx context.Context,
	operation string, // "rdb_save", "aof_rewrite"
	duration time.Duration,
	success bool,
) {
	attrs := []attribute.KeyValue{
		attribute.String("operation", operation),
		attribute.Bool("success", success),
	}

	switch operation {
	case "rdb_save":
		rm.rdbSaves.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "aof_rewrite":
		rm.aofRewrites.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordConnectionPoolStats records connection pool statistics
func (rm *RedisMetrics) RecordConnectionPoolStats(
	ctx context.Context,
	poolName string,
	size int64,
	used int64,
	idle int64,
) {
	_ = []attribute.KeyValue{
		attribute.String("pool_name", poolName),
	}

	// These would typically be recorded via observable gauges
	// with callbacks, but this method allows manual recording
	// when pool stats are available
	_ = size
	_ = used
	_ = idle
}

// RecordMemoryStats records Redis memory statistics
func (rm *RedisMetrics) RecordMemoryStats(
	ctx context.Context,
	usedMemory int64,
	maxMemory int64,
	fragmentationRatio float64,
) {
	// These would typically be recorded via observable gauges
	// with callbacks from Redis INFO command
}
