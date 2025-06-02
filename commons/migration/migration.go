// Package migration provides automated database migration management with rollback capabilities
// for PostgreSQL and MongoDB databases, supporting schema versioning and migration tracking.
package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// MigrationDirection represents the direction of migration
type MigrationDirection string

const (
	MigrationDirectionUp   MigrationDirection = "up"
	MigrationDirectionDown MigrationDirection = "down"
)

// MigrationStatus represents the status of a migration
type MigrationStatus string

const (
	MigrationStatusPending    MigrationStatus = "pending"
	MigrationStatusRunning    MigrationStatus = "running"
	MigrationStatusCompleted  MigrationStatus = "completed"
	MigrationStatusFailed     MigrationStatus = "failed"
	MigrationStatusRolledBack MigrationStatus = "rolled_back"
)

// DatabaseType represents the type of database
type DatabaseType string

const (
	DatabaseTypePostgreSQL DatabaseType = "postgresql"
	DatabaseTypeMongoDB    DatabaseType = "mongodb"
)

// Migration represents a single database migration
type Migration struct {
	Version      int64                  `json:"version"      bson:"version"`
	Name         string                 `json:"name"         bson:"name"`
	Description  string                 `json:"description"  bson:"description"`
	UpScript     string                 `json:"up_script"    bson:"up_script"`
	DownScript   string                 `json:"down_script"  bson:"down_script"`
	Checksum     string                 `json:"checksum"     bson:"checksum"`
	Dependencies []int64                `json:"dependencies" bson:"dependencies"`
	Tags         []string               `json:"tags"         bson:"tags"`
	Metadata     map[string]interface{} `json:"metadata"     bson:"metadata"`
}

// MigrationRecord represents a migration execution record
type MigrationRecord struct {
	Version      int64              `json:"version"                 bson:"version"`
	Name         string             `json:"name"                    bson:"name"`
	Status       MigrationStatus    `json:"status"                  bson:"status"`
	Direction    MigrationDirection `json:"direction"               bson:"direction"`
	Checksum     string             `json:"checksum"                bson:"checksum"`
	ExecutedAt   time.Time          `json:"executed_at"             bson:"executed_at"`
	CompletedAt  *time.Time         `json:"completed_at,omitempty"  bson:"completed_at,omitempty"`
	Duration     time.Duration      `json:"duration"                bson:"duration"`
	ErrorMessage string             `json:"error_message,omitempty" bson:"error_message,omitempty"`
	ExecutedBy   string             `json:"executed_by"             bson:"executed_by"`
	SessionID    string             `json:"session_id"              bson:"session_id"`
	BatchID      string             `json:"batch_id,omitempty"      bson:"batch_id,omitempty"`
}

// MigrationConfig defines configuration for the migration manager
type MigrationConfig struct {
	// Database settings
	DatabaseType         DatabaseType `json:"database_type"`
	MigrationsTable      string       `json:"migrations_table"`
	MigrationsCollection string       `json:"migrations_collection"`
	LockTable            string       `json:"lock_table"`
	LockCollection       string       `json:"lock_collection"`

	// Execution settings
	EnableTransactions bool          `json:"enable_transactions"`
	TransactionTimeout time.Duration `json:"transaction_timeout"`
	LockTimeout        time.Duration `json:"lock_timeout"`
	MaxRetries         int           `json:"max_retries"`
	RetryDelay         time.Duration `json:"retry_delay"`

	// Safety settings
	EnableChecksumValidation bool `json:"enable_checksum_validation"`
	AllowOutOfOrder          bool `json:"allow_out_of_order"`
	DryRun                   bool `json:"dry_run"`
	BackupBeforeMigration    bool `json:"backup_before_migration"`

	// Monitoring settings
	EnableDetailedLogging bool   `json:"enable_detailed_logging"`
	LogMigrationContent   bool   `json:"log_migration_content"`
	NotificationWebhook   string `json:"notification_webhook"`
	MetricsEnabled        bool   `json:"metrics_enabled"`

	// Rollback settings
	AutoRollbackOnFailure bool `json:"auto_rollback_on_failure"`
	MaxRollbackDepth      int  `json:"max_rollback_depth"`
	KeepFailedMigrations  bool `json:"keep_failed_migrations"`
}

// DefaultMigrationConfig returns a production-ready migration configuration
func DefaultMigrationConfig(dbType DatabaseType) MigrationConfig {
	config := MigrationConfig{
		DatabaseType:             dbType,
		EnableTransactions:       true,
		TransactionTimeout:       5 * time.Minute,
		LockTimeout:              10 * time.Minute,
		MaxRetries:               3,
		RetryDelay:               5 * time.Second,
		EnableChecksumValidation: true,
		AllowOutOfOrder:          false,
		DryRun:                   false,
		BackupBeforeMigration:    true,
		EnableDetailedLogging:    true,
		LogMigrationContent:      false, // Security: don't log sensitive migration content
		MetricsEnabled:           true,
		AutoRollbackOnFailure:    true,
		MaxRollbackDepth:         10,
		KeepFailedMigrations:     true,
	}

	switch dbType {
	case DatabaseTypePostgreSQL:
		config.MigrationsTable = "schema_migrations"
		config.LockTable = "schema_migrations_lock"
	case DatabaseTypeMongoDB:
		config.MigrationsCollection = "schema_migrations"
		config.LockCollection = "schema_migrations_lock"
	}

	return config
}

// MigrationManager handles database migrations with rollback capabilities
type MigrationManager struct {
	config MigrationConfig
	logger log.Logger

	// Database connections
	postgresDB *sql.DB
	mongoDB    *mongo.Database

	// Migration state
	migrations      []Migration
	migrationsMutex sync.RWMutex

	// Execution state
	isLocked       bool
	lockMutex      sync.Mutex
	currentSession string

	// Metrics
	metrics      MigrationMetrics
	metricsMutex sync.RWMutex
}

// MigrationMetrics contains metrics for migration operations
type MigrationMetrics struct {
	TotalMigrations      int64         `json:"total_migrations"`
	SuccessfulMigrations int64         `json:"successful_migrations"`
	FailedMigrations     int64         `json:"failed_migrations"`
	RolledBackMigrations int64         `json:"rolled_back_migrations"`
	TotalExecutionTime   time.Duration `json:"total_execution_time"`
	AverageExecutionTime time.Duration `json:"average_execution_time"`
	LastMigrationTime    time.Time     `json:"last_migration_time"`
	CurrentSchemaVersion int64         `json:"current_schema_version"`
	PendingMigrations    int64         `json:"pending_migrations"`
}

// NewMigrationManager creates a new migration manager
func NewMigrationManager(config MigrationConfig, logger log.Logger) *MigrationManager {
	return &MigrationManager{
		config:         config,
		logger:         logger,
		migrations:     make([]Migration, 0),
		currentSession: generateSessionID(),
		metrics:        MigrationMetrics{},
	}
}

// SetPostgreSQLConnection sets the PostgreSQL database connection
func (mm *MigrationManager) SetPostgreSQLConnection(db *sql.DB) error {
	if mm.config.DatabaseType != DatabaseTypePostgreSQL {
		return fmt.Errorf("manager is configured for %s, not PostgreSQL", mm.config.DatabaseType)
	}

	mm.postgresDB = db
	return mm.initializePostgreSQLTables()
}

// SetMongoDBConnection sets the MongoDB database connection
func (mm *MigrationManager) SetMongoDBConnection(db *mongo.Database) error {
	if mm.config.DatabaseType != DatabaseTypeMongoDB {
		return fmt.Errorf("manager is configured for %s, not MongoDB", mm.config.DatabaseType)
	}

	mm.mongoDB = db
	return mm.initializeMongoDBCollections()
}

// AddMigration adds a migration to the manager
func (mm *MigrationManager) AddMigration(migration Migration) error {
	mm.migrationsMutex.Lock()
	defer mm.migrationsMutex.Unlock()

	// Validate migration
	if err := mm.validateMigration(migration); err != nil {
		return fmt.Errorf("invalid migration: %w", err)
	}

	// Calculate checksum
	migration.Checksum = mm.calculateChecksum(migration)

	// Check for duplicate versions
	for _, existing := range mm.migrations {
		if existing.Version == migration.Version {
			return fmt.Errorf("migration with version %d already exists", migration.Version)
		}
	}

	mm.migrations = append(mm.migrations, migration)

	// Sort migrations by version
	sort.Slice(mm.migrations, func(i, j int) bool {
		return mm.migrations[i].Version < mm.migrations[j].Version
	})

	mm.logger.Info("Migration added",
		zap.Int64("version", migration.Version),
		zap.String("name", migration.Name),
		zap.String("checksum", migration.Checksum),
	)

	return nil
}

// MigrateUp runs all pending migrations
func (mm *MigrationManager) MigrateUp(ctx context.Context) error {
	return mm.migrateToVersion(ctx, -1, MigrationDirectionUp)
}

// MigrateDown rolls back to a specific version
func (mm *MigrationManager) MigrateDown(ctx context.Context, targetVersion int64) error {
	return mm.migrateToVersion(ctx, targetVersion, MigrationDirectionDown)
}

// MigrateToVersion migrates to a specific version
func (mm *MigrationManager) MigrateToVersion(ctx context.Context, targetVersion int64) error {
	currentVersion, err := mm.getCurrentVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	if targetVersion > currentVersion {
		return mm.migrateToVersion(ctx, targetVersion, MigrationDirectionUp)
	} else if targetVersion < currentVersion {
		return mm.migrateToVersion(ctx, targetVersion, MigrationDirectionDown)
	}

	mm.logger.Info("Already at target version", zap.Int64("version", targetVersion))
	return nil
}

// GetCurrentVersion returns the current schema version
func (mm *MigrationManager) GetCurrentVersion(ctx context.Context) (int64, error) {
	return mm.getCurrentVersion(ctx)
}

// GetPendingMigrations returns all pending migrations
func (mm *MigrationManager) GetPendingMigrations(ctx context.Context) ([]Migration, error) {
	currentVersion, err := mm.getCurrentVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current version: %w", err)
	}

	mm.migrationsMutex.RLock()
	defer mm.migrationsMutex.RUnlock()

	var pending []Migration
	for _, migration := range mm.migrations {
		if migration.Version > currentVersion {
			pending = append(pending, migration)
		}
	}

	return pending, nil
}

// GetMigrationHistory returns the migration history
func (mm *MigrationManager) GetMigrationHistory(ctx context.Context) ([]MigrationRecord, error) {
	switch mm.config.DatabaseType {
	case DatabaseTypePostgreSQL:
		return mm.getPostgreSQLMigrationHistory(ctx)
	case DatabaseTypeMongoDB:
		return mm.getMongoDBMigrationHistory(ctx)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", mm.config.DatabaseType)
	}
}

// ValidateMigrations validates all migrations for consistency
func (mm *MigrationManager) ValidateMigrations(ctx context.Context) error {
	mm.migrationsMutex.RLock()
	defer mm.migrationsMutex.RUnlock()

	// Get applied migrations
	history, err := mm.GetMigrationHistory(ctx)
	if err != nil {
		return fmt.Errorf("failed to get migration history: %w", err)
	}

	// Create map of applied migrations
	appliedMap := make(map[int64]MigrationRecord)
	for _, record := range history {
		if record.Status == MigrationStatusCompleted {
			appliedMap[record.Version] = record
		}
	}

	// Validate checksums for applied migrations
	if mm.config.EnableChecksumValidation {
		for _, migration := range mm.migrations {
			if record, exists := appliedMap[migration.Version]; exists {
				if record.Checksum != migration.Checksum {
					return fmt.Errorf("checksum mismatch for migration %d: expected %s, got %s",
						migration.Version, migration.Checksum, record.Checksum)
				}
			}
		}
	}

	// Validate dependencies
	for _, migration := range mm.migrations {
		for _, depVersion := range migration.Dependencies {
			if _, exists := appliedMap[depVersion]; !exists {
				// Check if dependency exists in migration list
				found := false
				for _, dep := range mm.migrations {
					if dep.Version == depVersion && dep.Version < migration.Version {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("migration %d depends on missing migration %d",
						migration.Version, depVersion)
				}
			}
		}
	}

	mm.logger.Info("Migration validation completed successfully",
		zap.Int("total_migrations", len(mm.migrations)),
		zap.Int("applied_migrations", len(appliedMap)),
	)

	return nil
}

// GetMetrics returns current migration metrics
func (mm *MigrationManager) GetMetrics() MigrationMetrics {
	mm.metricsMutex.RLock()
	defer mm.metricsMutex.RUnlock()
	return mm.metrics
}

// migrateToVersion performs the actual migration to a target version
func (mm *MigrationManager) migrateToVersion(
	ctx context.Context,
	targetVersion int64,
	direction MigrationDirection,
) error {
	// Acquire lock
	if err := mm.acquireLock(ctx); err != nil {
		return fmt.Errorf("failed to acquire migration lock: %w", err)
	}
	defer mm.releaseLock(ctx)

	// Get current version
	currentVersion, err := mm.getCurrentVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	// Get migrations to execute
	migrationsToExecute, err := mm.getMigrationsToExecute(currentVersion, targetVersion, direction)
	if err != nil {
		return fmt.Errorf("failed to get migrations to execute: %w", err)
	}

	if len(migrationsToExecute) == 0 {
		mm.logger.Info("No migrations to execute")
		return nil
	}

	mm.logger.Info("Starting migration",
		zap.String("direction", string(direction)),
		zap.Int64("current_version", currentVersion),
		zap.Int64("target_version", targetVersion),
		zap.Int("migrations_count", len(migrationsToExecute)),
	)

	// Execute migrations
	batchID := generateBatchID()
	for _, migration := range migrationsToExecute {
		if err := mm.executeMigration(ctx, migration, direction, batchID); err != nil {
			if mm.config.AutoRollbackOnFailure && direction == MigrationDirectionUp {
				mm.logger.Error("Migration failed, attempting rollback",
					zap.Int64("failed_version", migration.Version),
					zap.Error(err),
				)

				// Attempt rollback
				if rollbackErr := mm.rollbackLastMigration(ctx); rollbackErr != nil {
					mm.logger.Error("Rollback failed", zap.Error(rollbackErr))
					return fmt.Errorf(
						"migration failed and rollback failed: migration error: %w, rollback error: %v",
						err,
						rollbackErr,
					)
				}
			}
			return fmt.Errorf("migration %d failed: %w", migration.Version, err)
		}
	}

	mm.logger.Info("Migration completed successfully",
		zap.String("direction", string(direction)),
		zap.Int64("final_version", targetVersion),
		zap.Int("executed_migrations", len(migrationsToExecute)),
	)

	return nil
}

// validateMigration validates a migration for correctness
func (mm *MigrationManager) validateMigration(migration Migration) error {
	if migration.Version <= 0 {
		return fmt.Errorf("version must be positive")
	}

	if migration.Name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	if migration.UpScript == "" {
		return fmt.Errorf("up script cannot be empty")
	}

	// Validate dependencies
	for _, dep := range migration.Dependencies {
		if dep >= migration.Version {
			return fmt.Errorf(
				"dependency version %d must be less than migration version %d",
				dep,
				migration.Version,
			)
		}
	}

	return nil
}

// calculateChecksum calculates the checksum for a migration
func (mm *MigrationManager) calculateChecksum(migration Migration) string {
	content := fmt.Sprintf(
		"%d|%s|%s|%s",
		migration.Version,
		migration.Name,
		migration.UpScript,
		migration.DownScript,
	)
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

// generateSessionID generates a unique session ID
func generateSessionID() string {
	return fmt.Sprintf("session_%d_%s", time.Now().Unix(), generateRandomString(8))
}

// generateBatchID generates a unique batch ID
func generateBatchID() string {
	return fmt.Sprintf("batch_%d_%s", time.Now().Unix(), generateRandomString(8))
}

// generateRandomString generates a random string of specified length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}

// Helper functions for database-specific operations will be implemented in separate files
// to keep this file focused on the core migration logic
