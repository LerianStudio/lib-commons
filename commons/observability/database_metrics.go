package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DatabaseMetrics provides database-specific metrics collection
type DatabaseMetrics struct {
	meter metric.Meter

	// Query metrics
	queryCounter      metric.Int64Counter
	queryDuration     metric.Float64Histogram
	queryErrorCounter metric.Int64Counter
	slowQueryCounter  metric.Int64Counter

	// Connection metrics
	connectionCounter  metric.Int64Counter
	activeConnections  metric.Int64UpDownCounter
	connectionPoolSize metric.Int64ObservableGauge
	connectionPoolUsed metric.Int64ObservableGauge
	connectionDuration metric.Float64Histogram
	connectionErrors   metric.Int64Counter

	// Transaction metrics
	transactionCounter      metric.Int64Counter
	transactionDuration     metric.Float64Histogram
	transactionErrorCounter metric.Int64Counter
	rollbackCounter         metric.Int64Counter

	// Lock metrics
	lockWaitDuration metric.Float64Histogram
	deadlockCounter  metric.Int64Counter

	// Specific database metrics
	postgresMetrics *PostgreSQLMetrics
	mongoMetrics    *MongoDBMetrics
}

// PostgreSQLMetrics provides PostgreSQL-specific metrics
type PostgreSQLMetrics struct {
	meter metric.Meter

	// Statement metrics
	statementCounter   metric.Int64Counter
	preparedStatements metric.Int64ObservableGauge

	// Index usage
	indexScans      metric.Int64Counter
	sequentialScans metric.Int64Counter

	// Cache metrics
	bufferHitRatio metric.Float64ObservableGauge

	// Replication metrics
	replicationLag metric.Float64ObservableGauge
}

// MongoDBMetrics provides MongoDB-specific metrics
type MongoDBMetrics struct {
	meter metric.Meter

	// Operation metrics
	operationCounter metric.Int64Counter
	commandCounter   metric.Int64Counter

	// Collection metrics
	collectionSize metric.Int64ObservableGauge
	documentCount  metric.Int64ObservableGauge

	// Index metrics
	indexUsage metric.Int64Counter
	indexSize  metric.Int64ObservableGauge

	// Replica set metrics
	replicaSetStatus metric.Int64ObservableGauge
	oplogSize        metric.Int64ObservableGauge
}

// NewDatabaseMetrics creates a new database metrics instance
func NewDatabaseMetrics(meter metric.Meter) (*DatabaseMetrics, error) {
	dm := &DatabaseMetrics{meter: meter}

	// Initialize query metrics
	queryCounter, err := meter.Int64Counter(
		"database.queries.total",
		metric.WithDescription("Total number of database queries"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create query counter: %w", err)
	}
	dm.queryCounter = queryCounter

	queryDuration, err := meter.Float64Histogram(
		"database.query.duration",
		metric.WithDescription("Database query duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create query duration histogram: %w", err)
	}
	dm.queryDuration = queryDuration

	queryErrorCounter, err := meter.Int64Counter(
		"database.query.errors",
		metric.WithDescription("Total number of database query errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create query error counter: %w", err)
	}
	dm.queryErrorCounter = queryErrorCounter

	slowQueryCounter, err := meter.Int64Counter(
		"database.slow_queries.total",
		metric.WithDescription("Total number of slow database queries"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create slow query counter: %w", err)
	}
	dm.slowQueryCounter = slowQueryCounter

	// Initialize connection metrics
	connectionCounter, err := meter.Int64Counter(
		"database.connections.total",
		metric.WithDescription("Total number of database connections created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection counter: %w", err)
	}
	dm.connectionCounter = connectionCounter

	activeConnections, err := meter.Int64UpDownCounter(
		"database.connections.active",
		metric.WithDescription("Number of active database connections"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create active connections counter: %w", err)
	}
	dm.activeConnections = activeConnections

	connectionPoolSize, err := meter.Int64ObservableGauge(
		"database.connection_pool.size",
		metric.WithDescription("Database connection pool size"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool size gauge: %w", err)
	}
	dm.connectionPoolSize = connectionPoolSize

	connectionPoolUsed, err := meter.Int64ObservableGauge(
		"database.connection_pool.used",
		metric.WithDescription("Database connection pool used connections"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool used gauge: %w", err)
	}
	dm.connectionPoolUsed = connectionPoolUsed

	connectionDuration, err := meter.Float64Histogram(
		"database.connection.duration",
		metric.WithDescription("Database connection duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection duration histogram: %w", err)
	}
	dm.connectionDuration = connectionDuration

	connectionErrors, err := meter.Int64Counter(
		"database.connection.errors",
		metric.WithDescription("Total number of database connection errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection error counter: %w", err)
	}
	dm.connectionErrors = connectionErrors

	// Initialize transaction metrics
	transactionCounter, err := meter.Int64Counter(
		"database.transactions.total",
		metric.WithDescription("Total number of database transactions"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction counter: %w", err)
	}
	dm.transactionCounter = transactionCounter

	transactionDuration, err := meter.Float64Histogram(
		"database.transaction.duration",
		metric.WithDescription("Database transaction duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction duration histogram: %w", err)
	}
	dm.transactionDuration = transactionDuration

	transactionErrorCounter, err := meter.Int64Counter(
		"database.transaction.errors",
		metric.WithDescription("Total number of database transaction errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction error counter: %w", err)
	}
	dm.transactionErrorCounter = transactionErrorCounter

	rollbackCounter, err := meter.Int64Counter(
		"database.transactions.rollbacks",
		metric.WithDescription("Total number of database transaction rollbacks"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create rollback counter: %w", err)
	}
	dm.rollbackCounter = rollbackCounter

	// Initialize lock metrics
	lockWaitDuration, err := meter.Float64Histogram(
		"database.lock.wait_duration",
		metric.WithDescription("Database lock wait duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create lock wait duration histogram: %w", err)
	}
	dm.lockWaitDuration = lockWaitDuration

	deadlockCounter, err := meter.Int64Counter(
		"database.deadlocks.total",
		metric.WithDescription("Total number of database deadlocks"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create deadlock counter: %w", err)
	}
	dm.deadlockCounter = deadlockCounter

	// Initialize PostgreSQL-specific metrics
	pgMetrics, err := NewPostgreSQLMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL metrics: %w", err)
	}
	dm.postgresMetrics = pgMetrics

	// Initialize MongoDB-specific metrics
	mongoMetrics, err := NewMongoDBMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create MongoDB metrics: %w", err)
	}
	dm.mongoMetrics = mongoMetrics

	return dm, nil
}

// RecordQuery records database query metrics
func (dm *DatabaseMetrics) RecordQuery(
	ctx context.Context,
	database string,
	table string,
	operation string,
	duration time.Duration,
	rowsAffected int64,
	isSlowQuery bool,
) {
	attrs := []attribute.KeyValue{
		attribute.String("database", database),
		attribute.String("table", table),
		attribute.String("operation", operation),
	}

	dm.queryCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	dm.queryDuration.Record(
		ctx,
		float64(duration.Nanoseconds())/1e6,
		metric.WithAttributes(attrs...),
	)

	if rowsAffected > 0 {
		attrs = append(attrs, attribute.Int64("rows_affected", rowsAffected))
	}

	if isSlowQuery {
		dm.slowQueryCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordQueryError records database query error metrics
func (dm *DatabaseMetrics) RecordQueryError(
	ctx context.Context,
	database string,
	table string,
	operation string,
	errorType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("database", database),
		attribute.String("table", table),
		attribute.String("operation", operation),
		attribute.String("error_type", errorType),
	}

	dm.queryErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordConnection records database connection metrics
func (dm *DatabaseMetrics) RecordConnection(
	ctx context.Context,
	database string,
	action string, // "opened", "closed", "failed"
	duration time.Duration,
) {
	attrs := []attribute.KeyValue{
		attribute.String("database", database),
		attribute.String("action", action),
	}

	switch action {
	case "opened":
		dm.connectionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
		dm.activeConnections.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "closed":
		dm.activeConnections.Add(ctx, -1, metric.WithAttributes(attrs...))
		if duration > 0 {
			dm.connectionDuration.Record(
				ctx,
				float64(duration.Nanoseconds())/1e6,
				metric.WithAttributes(attrs...),
			)
		}
	case "failed":
		dm.connectionErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordTransaction records database transaction metrics
func (dm *DatabaseMetrics) RecordTransaction(
	ctx context.Context,
	database string,
	action string, // "begin", "commit", "rollback", "error"
	duration time.Duration,
) {
	attrs := []attribute.KeyValue{
		attribute.String("database", database),
		attribute.String("action", action),
	}

	switch action {
	case "begin":
		dm.transactionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "commit":
		if duration > 0 {
			dm.transactionDuration.Record(
				ctx,
				float64(duration.Nanoseconds())/1e6,
				metric.WithAttributes(attrs...),
			)
		}
	case "rollback":
		dm.rollbackCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	case "error":
		dm.transactionErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordLockWait records database lock wait metrics
func (dm *DatabaseMetrics) RecordLockWait(
	ctx context.Context,
	database string,
	table string,
	lockType string,
	waitDuration time.Duration,
) {
	attrs := []attribute.KeyValue{
		attribute.String("database", database),
		attribute.String("table", table),
		attribute.String("lock_type", lockType),
	}

	dm.lockWaitDuration.Record(
		ctx,
		float64(waitDuration.Nanoseconds())/1e6,
		metric.WithAttributes(attrs...),
	)
}

// RecordDeadlock records database deadlock metrics
func (dm *DatabaseMetrics) RecordDeadlock(
	ctx context.Context,
	database string,
	involvedTables []string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("database", database),
		attribute.Int("tables_involved", len(involvedTables)),
	}

	dm.deadlockCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// GetPostgreSQLMetrics returns PostgreSQL-specific metrics
func (dm *DatabaseMetrics) GetPostgreSQLMetrics() *PostgreSQLMetrics {
	return dm.postgresMetrics
}

// GetMongoDBMetrics returns MongoDB-specific metrics
func (dm *DatabaseMetrics) GetMongoDBMetrics() *MongoDBMetrics {
	return dm.mongoMetrics
}

// NewPostgreSQLMetrics creates PostgreSQL-specific metrics
func NewPostgreSQLMetrics(meter metric.Meter) (*PostgreSQLMetrics, error) {
	pm := &PostgreSQLMetrics{meter: meter}

	statementCounter, err := meter.Int64Counter(
		"postgresql.statements.total",
		metric.WithDescription("Total number of PostgreSQL statements executed"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create statement counter: %w", err)
	}
	pm.statementCounter = statementCounter

	preparedStatements, err := meter.Int64ObservableGauge(
		"postgresql.prepared_statements",
		metric.WithDescription("Number of prepared statements"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create prepared statements gauge: %w", err)
	}
	pm.preparedStatements = preparedStatements

	indexScans, err := meter.Int64Counter(
		"postgresql.index_scans.total",
		metric.WithDescription("Total number of index scans"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create index scans counter: %w", err)
	}
	pm.indexScans = indexScans

	sequentialScans, err := meter.Int64Counter(
		"postgresql.sequential_scans.total",
		metric.WithDescription("Total number of sequential scans"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create sequential scans counter: %w", err)
	}
	pm.sequentialScans = sequentialScans

	bufferHitRatio, err := meter.Float64ObservableGauge(
		"postgresql.buffer_hit_ratio",
		metric.WithDescription("PostgreSQL buffer cache hit ratio"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create buffer hit ratio gauge: %w", err)
	}
	pm.bufferHitRatio = bufferHitRatio

	replicationLag, err := meter.Float64ObservableGauge(
		"postgresql.replication_lag",
		metric.WithDescription("PostgreSQL replication lag"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create replication lag gauge: %w", err)
	}
	pm.replicationLag = replicationLag

	return pm, nil
}

// NewMongoDBMetrics creates MongoDB-specific metrics
func NewMongoDBMetrics(meter metric.Meter) (*MongoDBMetrics, error) {
	mm := &MongoDBMetrics{meter: meter}

	operationCounter, err := meter.Int64Counter(
		"mongodb.operations.total",
		metric.WithDescription("Total number of MongoDB operations"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create operation counter: %w", err)
	}
	mm.operationCounter = operationCounter

	commandCounter, err := meter.Int64Counter(
		"mongodb.commands.total",
		metric.WithDescription("Total number of MongoDB commands"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command counter: %w", err)
	}
	mm.commandCounter = commandCounter

	collectionSize, err := meter.Int64ObservableGauge(
		"mongodb.collection.size_bytes",
		metric.WithDescription("MongoDB collection size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create collection size gauge: %w", err)
	}
	mm.collectionSize = collectionSize

	documentCount, err := meter.Int64ObservableGauge(
		"mongodb.collection.documents",
		metric.WithDescription("Number of documents in MongoDB collection"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create document count gauge: %w", err)
	}
	mm.documentCount = documentCount

	indexUsage, err := meter.Int64Counter(
		"mongodb.index.usage",
		metric.WithDescription("MongoDB index usage count"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create index usage counter: %w", err)
	}
	mm.indexUsage = indexUsage

	indexSize, err := meter.Int64ObservableGauge(
		"mongodb.index.size_bytes",
		metric.WithDescription("MongoDB index size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create index size gauge: %w", err)
	}
	mm.indexSize = indexSize

	replicaSetStatus, err := meter.Int64ObservableGauge(
		"mongodb.replica_set.status",
		metric.WithDescription("MongoDB replica set member status"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create replica set status gauge: %w", err)
	}
	mm.replicaSetStatus = replicaSetStatus

	oplogSize, err := meter.Int64ObservableGauge(
		"mongodb.oplog.size_bytes",
		metric.WithDescription("MongoDB oplog size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create oplog size gauge: %w", err)
	}
	mm.oplogSize = oplogSize

	return mm, nil
}
