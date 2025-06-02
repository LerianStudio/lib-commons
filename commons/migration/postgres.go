package migration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// escapeIdentifier safely escapes PostgreSQL identifiers to prevent SQL injection
func escapeIdentifier(identifier string) string {
	// Remove any existing quotes and escape internal quotes
	cleaned := strings.ReplaceAll(identifier, `"`, `""`)
	// Wrap in double quotes for safe identifier usage
	return `"` + cleaned + `"`
}

// initializePostgreSQLTables creates the necessary tables for migration tracking
func (mm *MigrationManager) initializePostgreSQLTables() error {
	if mm.postgresDB == nil {
		return fmt.Errorf("PostgreSQL connection not set")
	}

	// Create migrations table
	createMigrationsTable := `CREATE TABLE IF NOT EXISTS ` + escapeIdentifier(mm.config.MigrationsTable) + ` (
		version BIGINT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		status VARCHAR(50) NOT NULL,
		direction VARCHAR(10) NOT NULL,
		checksum VARCHAR(64) NOT NULL,
		executed_at TIMESTAMP WITH TIME ZONE NOT NULL,
		completed_at TIMESTAMP WITH TIME ZONE,
		duration INTERVAL,
		error_message TEXT,
		executed_by VARCHAR(255) NOT NULL,
		session_id VARCHAR(255) NOT NULL,
		batch_id VARCHAR(255),
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
	)`

	if _, err := mm.postgresDB.Exec(createMigrationsTable); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Create lock table
	createLockTable := `CREATE TABLE IF NOT EXISTS ` + escapeIdentifier(mm.config.LockTable) + ` (
		id INTEGER PRIMARY KEY DEFAULT 1,
		locked BOOLEAN NOT NULL DEFAULT FALSE,
		locked_at TIMESTAMP WITH TIME ZONE,
		locked_by VARCHAR(255),
		session_id VARCHAR(255),
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		CONSTRAINT single_lock CHECK (id = 1)
	)`

	if _, err := mm.postgresDB.Exec(createLockTable); err != nil {
		return fmt.Errorf("failed to create lock table: %w", err)
	}

	// Insert initial lock record if it doesn't exist
	insertLockRecord := `INSERT INTO ` + escapeIdentifier(mm.config.LockTable) + ` (id, locked) VALUES (1, FALSE) ON CONFLICT (id) DO NOTHING`

	if _, err := mm.postgresDB.Exec(insertLockRecord); err != nil {
		return fmt.Errorf("failed to initialize lock record: %w", err)
	}

	// Create indexes for better performance
	createIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_` + mm.config.MigrationsTable + `_version ON ` + escapeIdentifier(mm.config.MigrationsTable) + ` (version)`,
		`CREATE INDEX IF NOT EXISTS idx_` + mm.config.MigrationsTable + `_status ON ` + escapeIdentifier(mm.config.MigrationsTable) + ` (status)`,
		`CREATE INDEX IF NOT EXISTS idx_` + mm.config.MigrationsTable + `_executed_at ON ` + escapeIdentifier(mm.config.MigrationsTable) + ` (executed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_` + mm.config.MigrationsTable + `_session_id ON ` + escapeIdentifier(mm.config.MigrationsTable) + ` (session_id)`,
	}

	for _, indexSQL := range createIndexes {
		if _, err := mm.postgresDB.Exec(indexSQL); err != nil {
			mm.logger.Warn("Failed to create index", zap.String("sql", indexSQL), zap.Error(err))
		}
	}

	mm.logger.Info("PostgreSQL migration tables initialized",
		zap.String("migrations_table", mm.config.MigrationsTable),
		zap.String("lock_table", mm.config.LockTable),
	)

	return nil
}

// getCurrentVersion returns the current schema version from PostgreSQL
func (mm *MigrationManager) getCurrentVersion(ctx context.Context) (int64, error) {
	if mm.config.DatabaseType != DatabaseTypePostgreSQL {
		// This method is also used by MongoDB, so we delegate
		return mm.getMongoDBCurrentVersion(ctx)
	}

	query := `SELECT COALESCE(MAX(version), 0) FROM ` + escapeIdentifier(mm.config.MigrationsTable) + ` WHERE status = $1 AND direction = $2`

	var version int64
	err := mm.postgresDB.QueryRowContext(ctx, query, MigrationStatusCompleted, MigrationDirectionUp).
		Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("failed to get current version: %w", err)
	}

	return version, nil
}

// acquireLock acquires a migration lock in PostgreSQL
func (mm *MigrationManager) acquireLock(ctx context.Context) error {
	if mm.config.DatabaseType != DatabaseTypePostgreSQL {
		return mm.acquireMongoDBLock(ctx)
	}

	mm.lockMutex.Lock()
	defer mm.lockMutex.Unlock()

	if mm.isLocked {
		return fmt.Errorf("migration lock already held by this instance")
	}

	// Set lock timeout context
	lockCtx, cancel := context.WithTimeout(ctx, mm.config.LockTimeout)
	defer cancel()

	// Try to acquire lock with retries
	for attempt := 0; attempt < mm.config.MaxRetries; attempt++ {
		updateLock := `UPDATE ` + escapeIdentifier(mm.config.LockTable) + ` SET locked = TRUE, locked_at = NOW(), locked_by = $1, session_id = $2, updated_at = NOW() WHERE id = 1 AND (locked = FALSE OR locked_at < NOW() - INTERVAL '` + fmt.Sprintf("%d", int(mm.config.LockTimeout.Seconds())) + ` seconds')`

		result, err := mm.postgresDB.ExecContext(
			lockCtx,
			updateLock,
			"migration-manager",
			mm.currentSession,
		)
		if err != nil {
			return fmt.Errorf("failed to acquire lock: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to check lock acquisition: %w", err)
		}

		if rowsAffected > 0 {
			mm.isLocked = true
			mm.logger.Info("Migration lock acquired",
				zap.String("session_id", mm.currentSession),
				zap.Int("attempt", attempt+1),
			)
			return nil
		}

		// Check who holds the lock
		var lockedBy, sessionID string
		var lockedAt time.Time
		checkLock := `SELECT locked_by, session_id, locked_at FROM ` + escapeIdentifier(mm.config.LockTable) + ` WHERE id = 1`
		if err := mm.postgresDB.QueryRowContext(lockCtx, checkLock).Scan(&lockedBy, &sessionID, &lockedAt); err == nil {
			mm.logger.Warn("Migration lock held by another session",
				zap.String("locked_by", lockedBy),
				zap.String("session_id", sessionID),
				zap.Time("locked_at", lockedAt),
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

// releaseLock releases the migration lock in PostgreSQL
func (mm *MigrationManager) releaseLock(ctx context.Context) error {
	if mm.config.DatabaseType != DatabaseTypePostgreSQL {
		return mm.releaseMongoDBLock(ctx)
	}

	mm.lockMutex.Lock()
	defer mm.lockMutex.Unlock()

	if !mm.isLocked {
		return nil // Already released
	}

	releaseLock := `UPDATE ` + escapeIdentifier(mm.config.LockTable) + ` SET locked = FALSE, locked_at = NULL, locked_by = NULL, session_id = NULL, updated_at = NOW() WHERE id = 1 AND session_id = $1`

	result, err := mm.postgresDB.ExecContext(ctx, releaseLock, mm.currentSession)
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check lock release: %w", err)
	}

	if rowsAffected == 0 {
		mm.logger.Warn(
			"Lock was not held by this session",
			zap.String("session_id", mm.currentSession),
		)
	} else {
		mm.logger.Info("Migration lock released", zap.String("session_id", mm.currentSession))
	}

	mm.isLocked = false
	return nil
}

// executeMigration executes a migration in PostgreSQL
func (mm *MigrationManager) executeMigration(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
	batchID string,
) error {
	if mm.config.DatabaseType != DatabaseTypePostgreSQL {
		return mm.executeMongoDBMigration(ctx, migration, direction, batchID)
	}

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
	if err := mm.insertPostgreSQLMigrationRecord(ctx, record); err != nil {
		return fmt.Errorf("failed to insert migration record: %w", err)
	}

	// Execute migration
	var err error
	if mm.config.EnableTransactions {
		err = mm.executePostgreSQLMigrationWithTransaction(ctx, migration, direction)
	} else {
		err = mm.executePostgreSQLMigrationWithoutTransaction(ctx, migration, direction)
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
	if updateErr := mm.updatePostgreSQLMigrationRecord(ctx, record); updateErr != nil {
		mm.logger.Error("Failed to update migration record", zap.Error(updateErr))
	}

	mm.metricsMutex.Lock()
	mm.metrics.TotalMigrations++
	mm.metricsMutex.Unlock()

	if err != nil {
		mm.logger.Error("Migration execution failed",
			zap.Int64("version", migration.Version),
			zap.String("name", migration.Name),
			zap.String("direction", string(direction)),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		return err
	}

	mm.logger.Info("Migration executed successfully",
		zap.Int64("version", migration.Version),
		zap.String("name", migration.Name),
		zap.String("direction", string(direction)),
		zap.Duration("duration", duration),
	)

	return nil
}

// executePostgreSQLMigrationWithTransaction executes migration within a transaction
func (mm *MigrationManager) executePostgreSQLMigrationWithTransaction(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
) error {
	// Start transaction with timeout
	txCtx, cancel := context.WithTimeout(ctx, mm.config.TransactionTimeout)
	defer cancel()

	tx, err := mm.postgresDB.BeginTx(txCtx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				mm.logger.Error("Failed to rollback transaction", zap.Error(rollbackErr))
			}
		}
	}()

	// Execute migration script
	script := migration.UpScript
	if direction == MigrationDirectionDown {
		script = migration.DownScript
	}

	if mm.config.DryRun {
		mm.logger.Info("DRY RUN: Would execute migration",
			zap.Int64("version", migration.Version),
			zap.String("direction", string(direction)),
			zap.Bool("log_content", mm.config.LogMigrationContent),
		)
		if mm.config.LogMigrationContent {
			mm.logger.Info("Migration content", zap.String("script", script))
		}
		return nil
	}

	// Split script into individual statements
	statements := mm.splitPostgreSQLStatements(script)

	for i, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}

		mm.logger.Debug("Executing migration statement",
			zap.Int64("version", migration.Version),
			zap.Int("statement_number", i+1),
			zap.Int("total_statements", len(statements)),
		)

		if _, err := tx.ExecContext(txCtx, statement); err != nil {
			return fmt.Errorf("failed to execute statement %d: %w", i+1, err)
		}
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// executePostgreSQLMigrationWithoutTransaction executes migration without transaction
func (mm *MigrationManager) executePostgreSQLMigrationWithoutTransaction(
	ctx context.Context,
	migration Migration,
	direction MigrationDirection,
) error {
	script := migration.UpScript
	if direction == MigrationDirectionDown {
		script = migration.DownScript
	}

	if mm.config.DryRun {
		mm.logger.Info("DRY RUN: Would execute migration",
			zap.Int64("version", migration.Version),
			zap.String("direction", string(direction)),
		)
		return nil
	}

	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, mm.config.TransactionTimeout)
	defer cancel()

	statements := mm.splitPostgreSQLStatements(script)

	for i, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}

		if _, err := mm.postgresDB.ExecContext(execCtx, statement); err != nil {
			return fmt.Errorf("failed to execute statement %d: %w", i+1, err)
		}
	}

	return nil
}

// splitPostgreSQLStatements splits a PostgreSQL script into individual statements
func (mm *MigrationManager) splitPostgreSQLStatements(script string) []string {
	// Simple statement splitting by semicolon
	// TODO: Implement more sophisticated parsing to handle complex cases
	statements := strings.Split(script, ";")

	var result []string
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt != "" {
			result = append(result, stmt)
		}
	}

	return result
}

// insertPostgreSQLMigrationRecord inserts a migration record
func (mm *MigrationManager) insertPostgreSQLMigrationRecord(
	ctx context.Context,
	record MigrationRecord,
) error {
	query := `INSERT INTO ` + escapeIdentifier(mm.config.MigrationsTable) + ` (version, name, status, direction, checksum, executed_at, completed_at, duration, error_message, executed_by, session_id, batch_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	_, err := mm.postgresDB.ExecContext(ctx, query,
		record.Version, record.Name, record.Status, record.Direction, record.Checksum,
		record.ExecutedAt, record.CompletedAt, record.Duration, record.ErrorMessage,
		record.ExecutedBy, record.SessionID, record.BatchID,
	)

	return err
}

// updatePostgreSQLMigrationRecord updates a migration record
func (mm *MigrationManager) updatePostgreSQLMigrationRecord(
	ctx context.Context,
	record MigrationRecord,
) error {
	query := `UPDATE ` + escapeIdentifier(mm.config.MigrationsTable) + ` SET status = $1, completed_at = $2, duration = $3, error_message = $4, updated_at = NOW() WHERE version = $5 AND session_id = $6`

	_, err := mm.postgresDB.ExecContext(ctx, query,
		record.Status, record.CompletedAt, record.Duration, record.ErrorMessage,
		record.Version, record.SessionID,
	)

	return err
}

// getPostgreSQLMigrationHistory returns migration history from PostgreSQL
func (mm *MigrationManager) getPostgreSQLMigrationHistory(
	ctx context.Context,
) ([]MigrationRecord, error) {
	query := `SELECT version, name, status, direction, checksum, executed_at, completed_at, duration, error_message, executed_by, session_id, batch_id FROM ` + escapeIdentifier(mm.config.MigrationsTable) + ` ORDER BY executed_at DESC`

	rows, err := mm.postgresDB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query migration history: %w", err)
	}
	defer rows.Close()

	var records []MigrationRecord
	for rows.Next() {
		var record MigrationRecord
		var durationSeconds sql.NullFloat64
		var completedAt sql.NullTime
		var errorMessage, batchID sql.NullString

		err := rows.Scan(
			&record.Version, &record.Name, &record.Status, &record.Direction,
			&record.Checksum, &record.ExecutedAt, &completedAt, &durationSeconds,
			&errorMessage, &record.ExecutedBy, &record.SessionID, &batchID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan migration record: %w", err)
		}

		if completedAt.Valid {
			record.CompletedAt = &completedAt.Time
		}
		if durationSeconds.Valid {
			record.Duration = time.Duration(durationSeconds.Float64 * float64(time.Second))
		}
		if errorMessage.Valid {
			record.ErrorMessage = errorMessage.String
		}
		if batchID.Valid {
			record.BatchID = batchID.String
		}

		records = append(records, record)
	}

	return records, rows.Err()
}

// getMigrationsToExecute returns the migrations that need to be executed
func (mm *MigrationManager) getMigrationsToExecute(
	currentVersion, targetVersion int64,
	direction MigrationDirection,
) ([]Migration, error) {
	mm.migrationsMutex.RLock()
	defer mm.migrationsMutex.RUnlock()

	var migrationsToExecute []Migration

	if direction == MigrationDirectionUp {
		// For up migrations, execute all migrations after current version up to target
		for _, migration := range mm.migrations {
			if migration.Version > currentVersion {
				if targetVersion == -1 || migration.Version <= targetVersion {
					migrationsToExecute = append(migrationsToExecute, migration)
				}
			}
		}
	} else {
		// For down migrations, execute all migrations from current version down to target
		// Reverse order for rollbacks
		for i := len(mm.migrations) - 1; i >= 0; i-- {
			migration := mm.migrations[i]
			if migration.Version <= currentVersion && migration.Version > targetVersion {
				migrationsToExecute = append(migrationsToExecute, migration)
			}
		}
	}

	return migrationsToExecute, nil
}

// rollbackLastMigration rolls back the last applied migration
func (mm *MigrationManager) rollbackLastMigration(ctx context.Context) error {
	currentVersion, err := mm.getCurrentVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	if currentVersion == 0 {
		return fmt.Errorf("no migrations to rollback")
	}

	// Find the migration to rollback
	var migrationToRollback *Migration
	mm.migrationsMutex.RLock()
	for _, migration := range mm.migrations {
		if migration.Version == currentVersion {
			migrationToRollback = &migration
			break
		}
	}
	mm.migrationsMutex.RUnlock()

	if migrationToRollback == nil {
		return fmt.Errorf("migration with version %d not found", currentVersion)
	}

	if migrationToRollback.DownScript == "" {
		return fmt.Errorf("migration %d has no down script for rollback", currentVersion)
	}

	mm.logger.Info("Rolling back migration",
		zap.Int64("version", migrationToRollback.Version),
		zap.String("name", migrationToRollback.Name),
	)

	// Execute rollback
	batchID := generateBatchID()
	if err := mm.executeMigration(ctx, *migrationToRollback, MigrationDirectionDown, batchID); err != nil {
		return fmt.Errorf("failed to rollback migration %d: %w", migrationToRollback.Version, err)
	}

	mm.metricsMutex.Lock()
	mm.metrics.RolledBackMigrations++
	mm.metricsMutex.Unlock()

	return nil
}
