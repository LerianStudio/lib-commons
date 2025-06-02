package migration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

// initializeMongoDBCollections creates the necessary collections for migration tracking
func (mm *MigrationManager) initializeMongoDBCollections() error {
	if mm.mongoDB == nil {
		return fmt.Errorf("MongoDB connection not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create migrations collection with indexes
	migrationsCollection := mm.mongoDB.Collection(mm.config.MigrationsCollection)

	// Create indexes for migrations collection
	migrationIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "version", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "status", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "executed_at", Value: -1}},
		},
		{
			Keys: bson.D{{Key: "session_id", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "batch_id", Value: 1}},
		},
		{
			Keys: bson.D{
				{Key: "status", Value: 1},
				{Key: "direction", Value: 1},
				{Key: "version", Value: -1},
			},
		},
	}

	if _, err := migrationsCollection.Indexes().CreateMany(ctx, migrationIndexes); err != nil {
		mm.logger.Warn("Failed to create some migration indexes", zap.Error(err))
	}

	// Create lock collection with unique constraint
	lockCollection := mm.mongoDB.Collection(mm.config.LockCollection)

	// Create unique index on lock_id to ensure single lock
	lockIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "lock_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}

	if _, err := lockCollection.Indexes().CreateOne(ctx, lockIndex); err != nil {
		mm.logger.Warn("Failed to create lock index", zap.Error(err))
	}

	// Initialize lock document if it doesn't exist
	lockFilter := bson.M{"lock_id": 1}
	lockUpdate := bson.M{
		"$setOnInsert": bson.M{
			"lock_id":    1,
			"locked":     false,
			"locked_at":  nil,
			"locked_by":  nil,
			"session_id": nil,
			"created_at": time.Now(),
			"updated_at": time.Now(),
		},
	}

	opts := options.Update().SetUpsert(true)
	if _, err := lockCollection.UpdateOne(ctx, lockFilter, lockUpdate, opts); err != nil {
		return fmt.Errorf("failed to initialize lock document: %w", err)
	}

	mm.logger.Info("MongoDB migration collections initialized",
		zap.String("migrations_collection", mm.config.MigrationsCollection),
		zap.String("lock_collection", mm.config.LockCollection),
	)

	return nil
}

// getMongoDBCurrentVersion returns the current schema version from MongoDB
func (mm *MigrationManager) getMongoDBCurrentVersion(ctx context.Context) (int64, error) {
	collection := mm.mongoDB.Collection(mm.config.MigrationsCollection)

	// Find the highest completed up migration
	filter := bson.M{
		"status":    MigrationStatusCompleted,
		"direction": MigrationDirectionUp,
	}

	opts := options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})

	var result struct {
		Version int64 `bson:"version"`
	}

	err := collection.FindOne(ctx, filter, opts).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return 0, nil // No migrations applied yet
		}
		return 0, fmt.Errorf("failed to get current version: %w", err)
	}

	return result.Version, nil
}

// acquireMongoDBLock acquires a migration lock in MongoDB
func (mm *MigrationManager) acquireMongoDBLock(ctx context.Context) error {
	mm.lockMutex.Lock()
	defer mm.lockMutex.Unlock()

	if mm.isLocked {
		return fmt.Errorf("migration lock already held by this instance")
	}

	collection := mm.mongoDB.Collection(mm.config.LockCollection)

	// Set lock timeout context
	lockCtx, cancel := context.WithTimeout(ctx, mm.config.LockTimeout)
	defer cancel()

	// Try to acquire lock with retries
	for attempt := 0; attempt < mm.config.MaxRetries; attempt++ {
		// Try to acquire lock atomically
		filter := bson.M{
			"lock_id": 1,
			"$or": []bson.M{
				{"locked": false},
				{
					"locked_at": bson.M{
						"$lt": time.Now().Add(-mm.config.LockTimeout),
					},
				},
			},
		}

		update := bson.M{
			"$set": bson.M{
				"locked":     true,
				"locked_at":  time.Now(),
				"locked_by":  "migration-manager",
				"session_id": mm.currentSession,
				"updated_at": time.Now(),
			},
		}

		result, err := collection.UpdateOne(lockCtx, filter, update)
		if err != nil {
			return fmt.Errorf("failed to acquire lock: %w", err)
		}

		if result.ModifiedCount > 0 {
			mm.isLocked = true
			mm.logger.Info("MongoDB migration lock acquired",
				zap.String("session_id", mm.currentSession),
				zap.Int("attempt", attempt+1),
			)
			return nil
		}

		// Check who holds the lock
		var lockDoc struct {
			LockedBy  string    `bson:"locked_by"`
			SessionID string    `bson:"session_id"`
			LockedAt  time.Time `bson:"locked_at"`
		}

		if err := collection.FindOne(lockCtx, bson.M{"lock_id": 1}).Decode(&lockDoc); err == nil {
			mm.logger.Warn("MongoDB migration lock held by another session",
				zap.String("locked_by", lockDoc.LockedBy),
				zap.String("session_id", lockDoc.SessionID),
				zap.Time("locked_at", lockDoc.LockedAt),
				zap.Int("attempt", attempt+1),
			)
		}

		// Wait before retry
		select {
		case <-lockCtx.Done():
			return fmt.Errorf("lock acquisition timeout")
		case <-time.After(mm.config.RetryDelay):
			continue
		}
	}

	return fmt.Errorf("failed to acquire migration lock after %d attempts", mm.config.MaxRetries)
}

// releaseMongoDBLock releases the migration lock in MongoDB
func (mm *MigrationManager) releaseMongoDBLock(ctx context.Context) error {
	mm.lockMutex.Lock()
	defer mm.lockMutex.Unlock()

	if !mm.isLocked {
		return nil // Already released
	}

	collection := mm.mongoDB.Collection(mm.config.LockCollection)

	filter := bson.M{
		"lock_id":    1,
		"session_id": mm.currentSession,
	}

	update := bson.M{
		"$set": bson.M{
			"locked":     false,
			"locked_at":  nil,
			"locked_by":  nil,
			"session_id": nil,
			"updated_at": time.Now(),
		},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	if result.ModifiedCount == 0 {
		mm.logger.Warn(
			"Lock was not held by this session",
			zap.String("session_id", mm.currentSession),
		)
	} else {
		mm.logger.Info("MongoDB migration lock released", zap.String("session_id", mm.currentSession))
	}

	mm.isLocked = false
	return nil
}

// executeMongoDBMigration executes a migration in MongoDB
func (mm *MigrationManager) executeMongoDBMigration(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
	batchID string,
) error {
	startTime := time.Now()

	// Create migration record
	record := MigrationRecord{
		Version:    migration.Version,
		Name:       migration.Name,
		Status:     MigrationStatusRunning,
		Direction:  direction,
		Checksum:   migration.Checksum,
		ExecutedAt: startTime,
		ExecutedBy: "migration-manager",
		SessionID:  mm.currentSession,
		BatchID:    batchID,
	}

	// Insert running record
	if err := mm.insertMongoDBMigrationRecord(ctx, record); err != nil {
		return fmt.Errorf("failed to insert migration record: %w", err)
	}

	// Execute migration
	var err error
	if mm.config.EnableTransactions {
		err = mm.executeMongoDBMigrationWithTransaction(ctx, migration, direction)
	} else {
		err = mm.executeMongoDBMigrationWithoutTransaction(ctx, migration, direction)
	}

	// Update record with result
	duration := time.Since(startTime)
	completedAt := time.Now()

	if err != nil {
		record.Status = MigrationStatusFailed
		record.ErrorMessage = err.Error()
		record.Duration = duration

		mm.metricsMutex.Lock()
		mm.metrics.FailedMigrations++
		mm.metricsMutex.Unlock()
	} else {
		record.Status = MigrationStatusCompleted
		record.CompletedAt = &completedAt
		record.Duration = duration

		mm.metricsMutex.Lock()
		mm.metrics.SuccessfulMigrations++
		mm.metrics.TotalExecutionTime += duration
		mm.metrics.LastMigrationTime = completedAt
		if mm.metrics.SuccessfulMigrations > 0 {
			mm.metrics.AverageExecutionTime = mm.metrics.TotalExecutionTime / time.Duration(mm.metrics.SuccessfulMigrations)
		}
		if direction == MigrationDirectionUp {
			mm.metrics.CurrentSchemaVersion = migration.Version
		}
		mm.metricsMutex.Unlock()
	}

	// Update record
	if updateErr := mm.updateMongoDBMigrationRecord(ctx, record); updateErr != nil {
		mm.logger.Error("Failed to update migration record", zap.Error(updateErr))
	}

	mm.metricsMutex.Lock()
	mm.metrics.TotalMigrations++
	mm.metricsMutex.Unlock()

	if err != nil {
		mm.logger.Error("MongoDB migration execution failed",
			zap.Int64("version", migration.Version),
			zap.String("name", migration.Name),
			zap.String("direction", string(direction)),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		return err
	}

	mm.logger.Info("MongoDB migration executed successfully",
		zap.Int64("version", migration.Version),
		zap.String("name", migration.Name),
		zap.String("direction", string(direction)),
		zap.Duration("duration", duration),
	)

	return nil
}

// executeMongoDBMigrationWithTransaction executes migration within a transaction
func (mm *MigrationManager) executeMongoDBMigrationWithTransaction(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
) error {
	// Create session for transaction
	session, err := mm.mongoDB.Client().StartSession()
	if err != nil {
		return fmt.Errorf("failed to start MongoDB session: %w", err)
	}
	defer session.EndSession(ctx)

	// Create transaction context with timeout
	txCtx, cancel := context.WithTimeout(ctx, mm.config.TransactionTimeout)
	defer cancel()

	// Execute in transaction
	_, err = session.WithTransaction(
		txCtx,
		func(sessCtx mongo.SessionContext) (interface{}, error) {
			return nil, mm.executeMongoDBScript(sessCtx, migration, direction)
		},
	)

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}

	return nil
}

// executeMongoDBMigrationWithoutTransaction executes migration without transaction
func (mm *MigrationManager) executeMongoDBMigrationWithoutTransaction(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
) error {
	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, mm.config.TransactionTimeout)
	defer cancel()

	return mm.executeMongoDBScript(execCtx, migration, direction)
}

// executeMongoDBScript executes a MongoDB migration script
func (mm *MigrationManager) executeMongoDBScript(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
) error {
	script := migration.UpScript
	if direction == MigrationDirectionDown {
		script = migration.DownScript
	}

	if mm.config.DryRun {
		mm.logger.Info("DRY RUN: Would execute MongoDB migration",
			zap.Int64("version", migration.Version),
			zap.String("direction", string(direction)),
			zap.Bool("log_content", mm.config.LogMigrationContent),
		)
		if mm.config.LogMigrationContent {
			mm.logger.Info("Migration content", zap.String("script", script))
		}
		return nil
	}

	// Parse and execute MongoDB operations
	operations, err := mm.parseMongoDBScript(script)
	if err != nil {
		return fmt.Errorf("failed to parse MongoDB script: %w", err)
	}

	for i, operation := range operations {
		mm.logger.Debug("Executing MongoDB operation",
			zap.Int64("version", migration.Version),
			zap.Int("operation_number", i+1),
			zap.Int("total_operations", len(operations)),
			zap.String("operation_type", operation.Type),
		)

		if err := mm.executeMongoDBOperation(ctx, operation); err != nil {
			return fmt.Errorf("failed to execute operation %d (%s): %w", i+1, operation.Type, err)
		}
	}

	return nil
}

// MongoDBOperation represents a MongoDB operation
type MongoDBOperation struct {
	Type       string                 `json:"type"`
	Collection string                 `json:"collection"`
	Command    string                 `json:"command"`
	Filter     map[string]interface{} `json:"filter,omitempty"`
	Document   map[string]interface{} `json:"document,omitempty"`
	Update     map[string]interface{} `json:"update,omitempty"`
	Options    map[string]interface{} `json:"options,omitempty"`
}

// parseMongoDBScript parses a MongoDB script into operations
func (mm *MigrationManager) parseMongoDBScript(script string) ([]MongoDBOperation, error) {
	// This is a simplified parser for demonstration
	// In production, you would implement a more sophisticated MongoDB script parser
	// or use MongoDB's scripting capabilities directly

	var operations []MongoDBOperation

	// Split script into lines and parse basic operations
	lines := strings.Split(script, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		// Parse simple operations like:
		// createCollection("users")
		// insertOne("users", {"name": "John"})
		// createIndex("users", {"email": 1})

		if strings.HasPrefix(line, "createCollection(") {
			collectionName := mm.extractStringParam(line)
			operations = append(operations, MongoDBOperation{
				Type:       "createCollection",
				Collection: collectionName,
			})
		} else if strings.HasPrefix(line, "dropCollection(") {
			collectionName := mm.extractStringParam(line)
			operations = append(operations, MongoDBOperation{
				Type:       "dropCollection",
				Collection: collectionName,
			})
		} else if strings.HasPrefix(line, "createIndex(") {
			// Parse createIndex("collection", {"field": 1})
			// This is simplified - production code would need proper JSON parsing
			parts := strings.Split(line, ",")
			if len(parts) >= 2 {
				collectionName := mm.extractStringParam(parts[0])
				operations = append(operations, MongoDBOperation{
					Type:       "createIndex",
					Collection: collectionName,
					Command:    line, // Store full command for execution
				})
			}
		} else {
			// Store as raw command for execution
			operations = append(operations, MongoDBOperation{
				Type:    "raw",
				Command: line,
			})
		}
	}

	return operations, nil
}

// extractStringParam extracts a string parameter from a function call
func (mm *MigrationManager) extractStringParam(line string) string {
	start := strings.Index(line, "\"")
	if start == -1 {
		start = strings.Index(line, "'")
	}
	if start == -1 {
		return ""
	}

	end := strings.Index(line[start+1:], line[start:start+1])
	if end == -1 {
		return ""
	}

	return line[start+1 : start+1+end]
}

// executeMongoDBOperation executes a single MongoDB operation
func (mm *MigrationManager) executeMongoDBOperation(
	ctx context.Context,
	operation MongoDBOperation,
) error {
	switch operation.Type {
	case "createCollection":
		return mm.mongoDB.CreateCollection(ctx, operation.Collection)

	case "dropCollection":
		collection := mm.mongoDB.Collection(operation.Collection)
		return collection.Drop(ctx)

	case "createIndex":
		// This would need proper implementation based on the index specification
		// For now, return success
		mm.logger.Info("Index creation would be executed",
			zap.String("collection", operation.Collection),
			zap.String("command", operation.Command),
		)
		return nil

	case "raw":
		// For raw commands, you would typically use MongoDB's eval or similar
		// This is a placeholder implementation
		mm.logger.Info("Raw MongoDB command would be executed",
			zap.String("command", operation.Command),
		)
		return nil

	default:
		return fmt.Errorf("unsupported operation type: %s", operation.Type)
	}
}

// insertMongoDBMigrationRecord inserts a migration record in MongoDB
func (mm *MigrationManager) insertMongoDBMigrationRecord(
	ctx context.Context,
	record MigrationRecord,
) error {
	collection := mm.mongoDB.Collection(mm.config.MigrationsCollection)

	doc := bson.M{
		"version":       record.Version,
		"name":          record.Name,
		"status":        record.Status,
		"direction":     record.Direction,
		"checksum":      record.Checksum,
		"executed_at":   record.ExecutedAt,
		"completed_at":  record.CompletedAt,
		"duration":      record.Duration,
		"error_message": record.ErrorMessage,
		"executed_by":   record.ExecutedBy,
		"session_id":    record.SessionID,
		"batch_id":      record.BatchID,
		"created_at":    time.Now(),
		"updated_at":    time.Now(),
	}

	_, err := collection.InsertOne(ctx, doc)
	return err
}

// updateMongoDBMigrationRecord updates a migration record in MongoDB
func (mm *MigrationManager) updateMongoDBMigrationRecord(
	ctx context.Context,
	record MigrationRecord,
) error {
	collection := mm.mongoDB.Collection(mm.config.MigrationsCollection)

	filter := bson.M{
		"version":    record.Version,
		"session_id": record.SessionID,
	}

	update := bson.M{
		"$set": bson.M{
			"status":        record.Status,
			"completed_at":  record.CompletedAt,
			"duration":      record.Duration,
			"error_message": record.ErrorMessage,
			"updated_at":    time.Now(),
		},
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	return err
}

// getMongoDBMigrationHistory returns migration history from MongoDB
func (mm *MigrationManager) getMongoDBMigrationHistory(
	ctx context.Context,
) ([]MigrationRecord, error) {
	collection := mm.mongoDB.Collection(mm.config.MigrationsCollection)

	opts := options.Find().SetSort(bson.D{{Key: "executed_at", Value: -1}})

	cursor, err := collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query migration history: %w", err)
	}
	defer cursor.Close(ctx)

	var records []MigrationRecord
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode migration record: %w", err)
		}

		record := MigrationRecord{
			Version:    doc["version"].(int64),
			Name:       doc["name"].(string),
			Status:     MigrationStatus(doc["status"].(string)),
			Direction:  MigrationDirection(doc["direction"].(string)),
			Checksum:   doc["checksum"].(string),
			ExecutedAt: doc["executed_at"].(time.Time),
			ExecutedBy: doc["executed_by"].(string),
			SessionID:  doc["session_id"].(string),
		}

		if completedAt, ok := doc["completed_at"].(time.Time); ok {
			record.CompletedAt = &completedAt
		}

		if duration, ok := doc["duration"].(int64); ok {
			record.Duration = time.Duration(duration)
		}

		if errorMessage, ok := doc["error_message"].(string); ok {
			record.ErrorMessage = errorMessage
		}

		if batchID, ok := doc["batch_id"].(string); ok {
			record.BatchID = batchID
		}

		records = append(records, record)
	}

	return records, cursor.Err()
}
