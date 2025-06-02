package migration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/commons/zap"
)

func TestDefaultMigrationConfig(t *testing.T) {
	testCases := []struct {
		name               string
		dbType             DatabaseType
		expectedTable      string
		expectedCollection string
	}{
		{
			name:          "postgresql_config",
			dbType:        DatabaseTypePostgreSQL,
			expectedTable: "schema_migrations",
		},
		{
			name:               "mongodb_config",
			dbType:             DatabaseTypeMongoDB,
			expectedCollection: "schema_migrations",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultMigrationConfig(tc.dbType)

			// Test basic settings
			assert.Equal(t, tc.dbType, config.DatabaseType)
			assert.True(t, config.EnableTransactions)
			assert.Equal(t, 5*time.Minute, config.TransactionTimeout)
			assert.Equal(t, 10*time.Minute, config.LockTimeout)
			assert.Equal(t, 3, config.MaxRetries)
			assert.Equal(t, 5*time.Second, config.RetryDelay)

			// Test safety settings
			assert.True(t, config.EnableChecksumValidation)
			assert.False(t, config.AllowOutOfOrder)
			assert.False(t, config.DryRun)
			assert.True(t, config.BackupBeforeMigration)

			// Test monitoring settings
			assert.True(t, config.EnableDetailedLogging)
			assert.False(t, config.LogMigrationContent) // Security: should be false by default
			assert.True(t, config.MetricsEnabled)

			// Test rollback settings
			assert.True(t, config.AutoRollbackOnFailure)
			assert.Equal(t, 10, config.MaxRollbackDepth)
			assert.True(t, config.KeepFailedMigrations)

			// Test database-specific settings
			if tc.dbType == DatabaseTypePostgreSQL {
				assert.Equal(t, tc.expectedTable, config.MigrationsTable)
				assert.Equal(t, "schema_migrations_lock", config.LockTable)
			} else {
				assert.Equal(t, tc.expectedCollection, config.MigrationsCollection)
				assert.Equal(t, "schema_migrations_lock", config.LockCollection)
			}
		})
	}
}

func TestNewMigrationManager(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	assert.NotNil(t, manager)
	assert.Equal(t, config, manager.config)
	assert.NotNil(t, manager.logger)
	assert.Empty(t, manager.migrations)
	assert.False(t, manager.isLocked)
	assert.NotEmpty(t, manager.currentSession)

	// Test metrics initialization
	metrics := manager.GetMetrics()
	assert.Equal(t, int64(0), metrics.TotalMigrations)
	assert.Equal(t, int64(0), metrics.SuccessfulMigrations)
	assert.Equal(t, int64(0), metrics.FailedMigrations)
}

func TestMigrationValidation(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	testCases := []struct {
		name        string
		migration   Migration
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid_migration",
			migration: Migration{
				Version:      1,
				Name:         "create_users_table",
				Description:  "Create users table",
				UpScript:     "CREATE TABLE users (id SERIAL PRIMARY KEY);",
				DownScript:   "DROP TABLE users;",
				Dependencies: []int64{},
			},
			expectError: false,
		},
		{
			name: "zero_version",
			migration: Migration{
				Version:    0,
				Name:       "invalid",
				UpScript:   "SELECT 1;",
				DownScript: "",
			},
			expectError: true,
			errorMsg:    "version must be positive",
		},
		{
			name: "negative_version",
			migration: Migration{
				Version:    -1,
				Name:       "invalid",
				UpScript:   "SELECT 1;",
				DownScript: "",
			},
			expectError: true,
			errorMsg:    "version must be positive",
		},
		{
			name: "empty_name",
			migration: Migration{
				Version:    1,
				Name:       "",
				UpScript:   "SELECT 1;",
				DownScript: "",
			},
			expectError: true,
			errorMsg:    "name cannot be empty",
		},
		{
			name: "empty_up_script",
			migration: Migration{
				Version:    1,
				Name:       "test",
				UpScript:   "",
				DownScript: "SELECT 1;",
			},
			expectError: true,
			errorMsg:    "up script cannot be empty",
		},
		{
			name: "invalid_dependency_version",
			migration: Migration{
				Version:      2,
				Name:         "test",
				UpScript:     "SELECT 1;",
				DownScript:   "",
				Dependencies: []int64{3}, // Dependency version higher than migration version
			},
			expectError: true,
			errorMsg:    "dependency version 3 must be less than migration version 2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.AddMigration(tc.migration)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Len(t, manager.migrations, 1)
				assert.NotEmpty(t, manager.migrations[0].Checksum)
			}
		})
	}
}

func TestMigrationSorting(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	// Add migrations in random order
	migrations := []Migration{
		{
			Version:    3,
			Name:       "third_migration",
			UpScript:   "CREATE TABLE orders (id SERIAL PRIMARY KEY);",
			DownScript: "DROP TABLE orders;",
		},
		{
			Version:    1,
			Name:       "first_migration",
			UpScript:   "CREATE TABLE users (id SERIAL PRIMARY KEY);",
			DownScript: "DROP TABLE users;",
		},
		{
			Version:    2,
			Name:       "second_migration",
			UpScript:   "CREATE TABLE products (id SERIAL PRIMARY KEY);",
			DownScript: "DROP TABLE products;",
		},
	}

	for _, migration := range migrations {
		err := manager.AddMigration(migration)
		require.NoError(t, err)
	}

	// Verify migrations are sorted by version
	assert.Len(t, manager.migrations, 3)
	assert.Equal(t, int64(1), manager.migrations[0].Version)
	assert.Equal(t, int64(2), manager.migrations[1].Version)
	assert.Equal(t, int64(3), manager.migrations[2].Version)
}

func TestDuplicateVersionDetection(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	// Add first migration
	migration1 := Migration{
		Version:    1,
		Name:       "first_migration",
		UpScript:   "CREATE TABLE users (id SERIAL PRIMARY KEY);",
		DownScript: "DROP TABLE users;",
	}

	err = manager.AddMigration(migration1)
	require.NoError(t, err)

	// Try to add migration with same version
	migration2 := Migration{
		Version:    1,
		Name:       "duplicate_migration",
		UpScript:   "CREATE TABLE customers (id SERIAL PRIMARY KEY);",
		DownScript: "DROP TABLE customers;",
	}

	err = manager.AddMigration(migration2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "migration with version 1 already exists")
	assert.Len(t, manager.migrations, 1) // Should still have only one migration
}

func TestChecksumCalculation(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	migration := Migration{
		Version:    1,
		Name:       "test_migration",
		UpScript:   "CREATE TABLE test (id SERIAL PRIMARY KEY);",
		DownScript: "DROP TABLE test;",
	}

	// Calculate checksum manually
	expectedChecksum := manager.calculateChecksum(migration)

	// Add migration and verify checksum is set
	err = manager.AddMigration(migration)
	require.NoError(t, err)

	assert.Equal(t, expectedChecksum, manager.migrations[0].Checksum)
	assert.NotEmpty(t, manager.migrations[0].Checksum)
	assert.Len(t, manager.migrations[0].Checksum, 64) // SHA256 hex length
}

func TestChecksumConsistency(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	migration1 := Migration{
		Version:    1,
		Name:       "test_migration",
		UpScript:   "CREATE TABLE test (id SERIAL PRIMARY KEY);",
		DownScript: "DROP TABLE test;",
	}

	migration2 := Migration{
		Version:    1,
		Name:       "test_migration",
		UpScript:   "CREATE TABLE test (id SERIAL PRIMARY KEY);",
		DownScript: "DROP TABLE test;",
	}

	// Same migration should produce same checksum
	checksum1 := manager.calculateChecksum(migration1)
	checksum2 := manager.calculateChecksum(migration2)
	assert.Equal(t, checksum1, checksum2)

	// Different migration should produce different checksum
	migration3 := Migration{
		Version:    1,
		Name:       "test_migration",
		UpScript:   "CREATE TABLE test (id SERIAL PRIMARY KEY, name VARCHAR(255));", // Different script
		DownScript: "DROP TABLE test;",
	}

	checksum3 := manager.calculateChecksum(migration3)
	assert.NotEqual(t, checksum1, checksum3)
}

func TestMigrationRecord(t *testing.T) {
	now := time.Now()
	completedAt := now.Add(1 * time.Second)

	record := MigrationRecord{
		Version:      1,
		Name:         "test_migration",
		Status:       MigrationStatusCompleted,
		Direction:    MigrationDirectionUp,
		Checksum:     "abc123",
		ExecutedAt:   now,
		CompletedAt:  &completedAt,
		Duration:     1 * time.Second,
		ErrorMessage: "",
		ExecutedBy:   "test-user",
		SessionID:    "session-123",
		BatchID:      "batch-456",
	}

	// Test record fields
	assert.Equal(t, int64(1), record.Version)
	assert.Equal(t, "test_migration", record.Name)
	assert.Equal(t, MigrationStatusCompleted, record.Status)
	assert.Equal(t, MigrationDirectionUp, record.Direction)
	assert.Equal(t, "abc123", record.Checksum)
	assert.Equal(t, now, record.ExecutedAt)
	assert.NotNil(t, record.CompletedAt)
	assert.Equal(t, completedAt, *record.CompletedAt)
	assert.Equal(t, 1*time.Second, record.Duration)
	assert.Empty(t, record.ErrorMessage)
	assert.Equal(t, "test-user", record.ExecutedBy)
	assert.Equal(t, "session-123", record.SessionID)
	assert.Equal(t, "batch-456", record.BatchID)
}

func TestMigrationStatuses(t *testing.T) {
	// Test all migration statuses are valid
	statuses := []MigrationStatus{
		MigrationStatusPending,
		MigrationStatusRunning,
		MigrationStatusCompleted,
		MigrationStatusFailed,
		MigrationStatusRolledBack,
	}

	expectedValues := []string{
		"pending",
		"running",
		"completed",
		"failed",
		"rolled_back",
	}

	for i, status := range statuses {
		assert.Equal(t, expectedValues[i], string(status))
	}
}

func TestMigrationDirections(t *testing.T) {
	// Test migration directions
	assert.Equal(t, "up", string(MigrationDirectionUp))
	assert.Equal(t, "down", string(MigrationDirectionDown))
}

func TestDatabaseTypes(t *testing.T) {
	// Test database types
	assert.Equal(t, "postgresql", string(DatabaseTypePostgreSQL))
	assert.Equal(t, "mongodb", string(DatabaseTypeMongoDB))
}

func TestSessionIDGeneration(t *testing.T) {
	sessionID1 := generateSessionID()
	sessionID2 := generateSessionID()

	// Session IDs should be unique
	assert.NotEqual(t, sessionID1, sessionID2)

	// Session IDs should have expected format
	assert.Contains(t, sessionID1, "session_")
	assert.Contains(t, sessionID2, "session_")

	// Should contain timestamp and random part
	assert.True(t, len(sessionID1) > 15) // "session_" + timestamp + "_" + random
	assert.True(t, len(sessionID2) > 15)
}

func TestBatchIDGeneration(t *testing.T) {
	batchID1 := generateBatchID()
	batchID2 := generateBatchID()

	// Batch IDs should be unique
	assert.NotEqual(t, batchID1, batchID2)

	// Batch IDs should have expected format
	assert.Contains(t, batchID1, "batch_")
	assert.Contains(t, batchID2, "batch_")

	// Should contain timestamp and random part
	assert.True(t, len(batchID1) > 13) // "batch_" + timestamp + "_" + random
	assert.True(t, len(batchID2) > 13)
}

func TestRandomStringGeneration(t *testing.T) {
	// Test different lengths
	lengths := []int{1, 5, 8, 16, 32}

	for _, length := range lengths {
		t.Run(fmt.Sprintf("length_%d", length), func(t *testing.T) {
			str1 := generateRandomString(length)
			str2 := generateRandomString(length)

			// Should have correct length
			assert.Equal(t, length, len(str1))
			assert.Equal(t, length, len(str2))

			// Should be different (with high probability)
			if length > 1 {
				assert.NotEqual(t, str1, str2)
			}

			// Should only contain valid characters
			validChars := "abcdefghijklmnopqrstuvwxyz0123456789"
			for _, char := range str1 {
				assert.Contains(t, validChars, string(char))
			}
		})
	}
}

func TestSetDatabaseConnections(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	t.Run("postgresql_connection_with_wrong_type", func(t *testing.T) {
		config := DefaultMigrationConfig(DatabaseTypeMongoDB) // Wrong type
		manager := NewMigrationManager(config, logger)

		err := manager.SetPostgreSQLConnection(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "manager is configured for mongodb, not PostgreSQL")
	})

	t.Run("mongodb_connection_with_wrong_type", func(t *testing.T) {
		config := DefaultMigrationConfig(DatabaseTypePostgreSQL) // Wrong type
		manager := NewMigrationManager(config, logger)

		err := manager.SetMongoDBConnection(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "manager is configured for postgresql, not MongoDB")
	})
}

func TestMigrationMetrics(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	// Initial metrics should be zero
	metrics := manager.GetMetrics()
	assert.Equal(t, int64(0), metrics.TotalMigrations)
	assert.Equal(t, int64(0), metrics.SuccessfulMigrations)
	assert.Equal(t, int64(0), metrics.FailedMigrations)
	assert.Equal(t, int64(0), metrics.RolledBackMigrations)
	assert.Equal(t, time.Duration(0), metrics.TotalExecutionTime)
	assert.Equal(t, time.Duration(0), metrics.AverageExecutionTime)
	assert.True(t, metrics.LastMigrationTime.IsZero())
	assert.Equal(t, int64(0), metrics.CurrentSchemaVersion)
	assert.Equal(t, int64(0), metrics.PendingMigrations)

	// Test metrics are thread-safe
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			manager.GetMetrics()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMigrationWithMetadata(t *testing.T) {
	logger, err := zap.NewStructured("test", "debug")
	require.NoError(t, err)

	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	migration := Migration{
		Version:     1,
		Name:        "migration_with_metadata",
		Description: "Test migration with metadata",
		UpScript:    "CREATE TABLE test (id SERIAL PRIMARY KEY);",
		DownScript:  "DROP TABLE test;",
		Tags:        []string{"schema", "table", "users"},
		Metadata: map[string]interface{}{
			"author":      "test-user",
			"jira_ticket": "PROJ-123",
			"env":         "development",
			"priority":    "high",
		},
	}

	err = manager.AddMigration(migration)
	require.NoError(t, err)

	addedMigration := manager.migrations[0]
	assert.Equal(t, []string{"schema", "table", "users"}, addedMigration.Tags)
	assert.Equal(t, "test-user", addedMigration.Metadata["author"])
	assert.Equal(t, "PROJ-123", addedMigration.Metadata["jira_ticket"])
	assert.Equal(t, "development", addedMigration.Metadata["env"])
	assert.Equal(t, "high", addedMigration.Metadata["priority"])
}

// Benchmark tests
func BenchmarkChecksumCalculation(b *testing.B) {
	logger, _ := zap.NewStructured("test", "debug")
	config := DefaultMigrationConfig(DatabaseTypePostgreSQL)
	manager := NewMigrationManager(config, logger)

	migration := Migration{
		Version:    1,
		Name:       "benchmark_migration",
		UpScript:   "CREATE TABLE large_table (id SERIAL PRIMARY KEY, data TEXT);",
		DownScript: "DROP TABLE large_table;",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.calculateChecksum(migration)
	}
}

func BenchmarkSessionIDGeneration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateSessionID()
	}
}

func BenchmarkBatchIDGeneration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateBatchID()
	}
}

func BenchmarkRandomStringGeneration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateRandomString(8)
	}
}
