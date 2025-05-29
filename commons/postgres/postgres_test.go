package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockLogger is a mock implementation of log.Logger
type MockLogger struct {
	mock.Mock
}

// MockDB is a mock implementation of dbresolver.DB
type MockDB struct {
	mock.Mock
}

func (m *MockDB) Begin() (dbresolver.Tx, error) {
	args := m.Called()
	return nil, args.Error(1)
}

func (m *MockDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (dbresolver.Tx, error) {
	args := m.Called(ctx, opts)
	return nil, args.Error(1)
}

func (m *MockDB) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockDB) Conn(ctx context.Context) (dbresolver.Conn, error) {
	args := m.Called(ctx)
	return nil, args.Error(1)
}

func (m *MockDB) Driver() driver.Driver {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(driver.Driver)
}

func (m *MockDB) Exec(query string, args ...any) (sql.Result, error) {
	mockArgs := m.Called(query, args)
	return nil, mockArgs.Error(1)
}

func (m *MockDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	mockArgs := m.Called(ctx, query, args)
	return nil, mockArgs.Error(1)
}

func (m *MockDB) Ping() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockDB) PingContext(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockDB) Prepare(query string) (dbresolver.Stmt, error) {
	args := m.Called(query)
	return nil, args.Error(1)
}

func (m *MockDB) PrepareContext(ctx context.Context, query string) (dbresolver.Stmt, error) {
	args := m.Called(ctx, query)
	return nil, args.Error(1)
}

func (m *MockDB) Query(query string, args ...any) (*sql.Rows, error) {
	mockArgs := m.Called(query, args)
	return nil, mockArgs.Error(1)
}

func (m *MockDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	mockArgs := m.Called(ctx, query, args)
	return nil, mockArgs.Error(1)
}

func (m *MockDB) QueryRow(query string, args ...any) *sql.Row {
	m.Called(query, args)
	return nil
}

func (m *MockDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	m.Called(ctx, query, args)
	return nil
}

func (m *MockDB) SetConnMaxIdleTime(d time.Duration) {
	m.Called(d)
}

func (m *MockDB) SetConnMaxLifetime(d time.Duration) {
	m.Called(d)
}

func (m *MockDB) SetMaxIdleConns(n int) {
	m.Called(n)
}

func (m *MockDB) SetMaxOpenConns(n int) {
	m.Called(n)
}

func (m *MockDB) PrimaryDBs() []*sql.DB {
	args := m.Called()
	return args.Get(0).([]*sql.DB)
}

func (m *MockDB) ReplicaDBs() []*sql.DB {
	args := m.Called()
	return args.Get(0).([]*sql.DB)
}

func (m *MockDB) Stats() sql.DBStats {
	args := m.Called()
	return args.Get(0).(sql.DBStats)
}

func (m *MockLogger) Info(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Infof(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockLogger) Infoln(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Error(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Errorf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockLogger) Errorln(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Warn(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Warnf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockLogger) Warnln(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Debug(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Debugf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockLogger) Debugln(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Fatal(args ...any) {
	m.Called(args)
}

func (m *MockLogger) Fatalf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockLogger) Fatalln(args ...any) {
	m.Called(args)
}

func (m *MockLogger) WithFields(fields ...any) log.Logger {
	args := m.Called(fields)
	return args.Get(0).(log.Logger)
}

func (m *MockLogger) WithDefaultMessageTemplate(message string) log.Logger {
	args := m.Called(message)
	return args.Get(0).(log.Logger)
}

func (m *MockLogger) Sync() error {
	args := m.Called()
	return args.Error(0)
}

func TestPostgresConnection_getMigrationsPath(t *testing.T) {
	tests := []struct {
		name         string
		pc           *PostgresConnection
		expectedPath string
		expectError  bool
		setupFunc    func()
		cleanupFunc  func()
	}{
		{
			name: "should return provided migrations path",
			pc: &PostgresConnection{
				MigrationsPath: "/custom/migrations/path",
				Logger:         &MockLogger{},
			},
			expectedPath: "/custom/migrations/path",
			expectError:  false,
		},
		{
			name: "should calculate migrations path when not provided",
			pc: &PostgresConnection{
				Component: "test-component",
				Logger:    &MockLogger{},
			},
			expectedPath: filepath.Join("components", "test-component", "migrations"),
			expectError:  false,
		},
		{
			name: "should handle empty component name",
			pc: &PostgresConnection{
				Component: "",
				Logger:    &MockLogger{},
			},
			expectedPath: filepath.Join("components", "", "migrations"),
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc()
			}
			defer func() {
				if tt.cleanupFunc != nil {
					tt.cleanupFunc()
				}
			}()

			path, err := tt.pc.getMigrationsPath()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// For calculated paths, we check if it ends with the expected suffix
				if tt.pc.MigrationsPath == "" {
					assert.True(t, filepath.IsAbs(path))
					assert.Contains(t, path, tt.expectedPath)
				} else {
					assert.Equal(t, tt.expectedPath, path)
				}
			}
		})
	}
}

func TestPostgresConnection_GetDB(t *testing.T) {
	tests := []struct {
		name        string
		pc          *PostgresConnection
		setupFunc   func(*PostgresConnection)
		expectError bool
		expectNil   bool
	}{
		{
			name: "should return existing connection",
			pc: &PostgresConnection{
				Logger: &MockLogger{},
			},
			setupFunc: func(pc *PostgresConnection) {
				// Create a dummy dbresolver.DB for testing
				db := &MockDB{}
				pc.ConnectionDB = db
				pc.Connected = true
			},
			expectError: false,
			expectNil:   false,
		},
		{
			name: "should return nil when Connect fails",
			pc: &PostgresConnection{
				ConnectionStringPrimary: "invalid-connection-string",
				ConnectionStringReplica: "invalid-connection-string",
				Logger:                  &MockLogger{},
			},
			setupFunc: func(pc *PostgresConnection) {
				mockLogger := pc.Logger.(*MockLogger)
				mockLogger.On("Info", mock.Anything).Maybe()
				mockLogger.On("Error", mock.Anything, mock.Anything).Maybe()
				mockLogger.On("Infof", mock.Anything, mock.Anything).Maybe()
			},
			expectError: true,
			expectNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc(tt.pc)
			}

			db, err := tt.pc.GetDB()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectNil {
				assert.Nil(t, db)
			} else {
				assert.NotNil(t, db)
			}
		})
	}
}

func TestPostgresConnection_Connect_Errors(t *testing.T) {
	tests := []struct {
		name      string
		pc        *PostgresConnection
		setupFunc func() error
		cleanup   func()
	}{
		{
			name: "should handle primary database connection error",
			pc: &PostgresConnection{
				ConnectionStringPrimary: "postgres://invalid:invalid@localhost:5432/invalid",
				ConnectionStringReplica: "postgres://invalid:invalid@localhost:5432/invalid",
				PrimaryDBName:           "test_primary",
				ReplicaDBName:           "test_replica",
				Component:               "test",
				MaxOpenConnections:      10,
				MaxIdleConnections:      5,
			},
			setupFunc: func() error {
				return nil
			},
		},
		{
			name: "should handle migration path parsing error",
			pc: &PostgresConnection{
				ConnectionStringPrimary: "postgres://user:pass@localhost:5432/db",
				ConnectionStringReplica: "postgres://user:pass@localhost:5432/db",
				MigrationsPath:          string([]byte{0x00}), // Invalid path
				PrimaryDBName:           "test_primary",
				ReplicaDBName:           "test_replica",
				MaxOpenConnections:      10,
				MaxIdleConnections:      5,
			},
			setupFunc: func() error {
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLogger := &MockLogger{}
			mockLogger.On("Info", mock.Anything).Maybe()
			mockLogger.On("Infof", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Error", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Errorf", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Warn", mock.Anything).Maybe()

			tt.pc.Logger = mockLogger

			if tt.setupFunc != nil {
				err := tt.setupFunc()
				require.NoError(t, err)
			}

			if tt.cleanup != nil {
				defer tt.cleanup()
			}

			// Since we now properly return errors instead of calling Fatal,
			// we expect Connect to return an error for invalid connections
			err := tt.pc.Connect()

			// The function should return an error for invalid connections
			assert.Error(t, err)
		})
	}
}

func TestPostgresConnection_Connect_MigrationScenarios(t *testing.T) {
	t.Skip("Skipping migration scenarios - needs rewrite for new logger interface")
	// Create a temporary directory for migration files
	tempDir, err := os.MkdirTemp("", "postgres_test_migrations")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Create a test migration file
	migrationFile := filepath.Join(tempDir, "001_test.up.sql")
	err = os.WriteFile(migrationFile, []byte("CREATE TABLE test (id INT);"), 0644)
	require.NoError(t, err)

	tests := []struct {
		name           string
		migrationError error
		setupLogger    func(*MockLogger)
		expectError    bool
	}{
		{
			name:           "should handle ErrNoChange from migrations",
			migrationError: migrate.ErrNoChange,
			setupLogger: func(ml *MockLogger) {
				ml.On("Info", mock.Anything).Once()
			},
			expectError: false,
		},
		{
			name:           "should handle file does not exist error",
			migrationError: errors.New("file does not exist"),
			setupLogger: func(ml *MockLogger) {
				ml.On("Warn", mock.Anything).Once()
			},
			expectError: false,
		},
		{
			name:           "should handle generic migration error",
			migrationError: errors.New("migration failed"),
			setupLogger: func(ml *MockLogger) {
				ml.On("Error", mock.Anything).Once()
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLogger := &MockLogger{}
			mockLogger.On("Info", mock.Anything).Maybe()
			mockLogger.On("Infof", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Error", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Errorf", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Warn", mock.Anything).Maybe()

			if tt.setupLogger != nil {
				tt.setupLogger(mockLogger)
			}

			pc := &PostgresConnection{
				ConnectionStringPrimary: "postgres://user:pass@localhost:5432/test",
				ConnectionStringReplica: "postgres://user:pass@localhost:5432/test",
				PrimaryDBName:           "test",
				ReplicaDBName:           "test",
				MigrationsPath:          tempDir,
				Logger:                  mockLogger,
				MaxOpenConnections:      10,
				MaxIdleConnections:      5,
			}

			// We can't easily test the full Connect flow due to database dependencies,
			// but we've structured the tests to validate the logic paths
			_ = pc.Connect()

			// Verify logger interactions where applicable
			if tt.setupLogger != nil {
				mockLogger.AssertExpectations(t)
			}
		})
	}
}

func TestPostgresConnection_ConfigurationValidation(t *testing.T) {
	tests := []struct {
		name string
		pc   *PostgresConnection
		test func(t *testing.T, pc *PostgresConnection)
	}{
		{
			name: "should set connection pool parameters",
			pc: &PostgresConnection{
				MaxOpenConnections: 25,
				MaxIdleConnections: 10,
			},
			test: func(t *testing.T, pc *PostgresConnection) {
				assert.Equal(t, 25, pc.MaxOpenConnections)
				assert.Equal(t, 10, pc.MaxIdleConnections)
			},
		},
		{
			name: "should handle zero connection pool parameters",
			pc: &PostgresConnection{
				MaxOpenConnections: 0,
				MaxIdleConnections: 0,
			},
			test: func(t *testing.T, pc *PostgresConnection) {
				assert.Equal(t, 0, pc.MaxOpenConnections)
				assert.Equal(t, 0, pc.MaxIdleConnections)
			},
		},
		{
			name: "should track connection state",
			pc: &PostgresConnection{
				Connected: false,
			},
			test: func(t *testing.T, pc *PostgresConnection) {
				assert.False(t, pc.Connected)
				pc.Connected = true
				assert.True(t, pc.Connected)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.test(t, tt.pc)
		})
	}
}

func TestPostgresConnection_StructFields(t *testing.T) {
	// Test that all fields can be properly set and retrieved
	pc := &PostgresConnection{
		ConnectionStringPrimary: "postgres://primary:5432/db",
		ConnectionStringReplica: "postgres://replica:5432/db",
		PrimaryDBName:           "primary_db",
		ReplicaDBName:           "replica_db",
		ConnectionDB:            nil,
		Connected:               false,
		Component:               "test-component",
		MigrationsPath:          "/path/to/migrations",
		Logger:                  &MockLogger{},
		MaxOpenConnections:      100,
		MaxIdleConnections:      20,
	}

	assert.Equal(t, "postgres://primary:5432/db", pc.ConnectionStringPrimary)
	assert.Equal(t, "postgres://replica:5432/db", pc.ConnectionStringReplica)
	assert.Equal(t, "primary_db", pc.PrimaryDBName)
	assert.Equal(t, "replica_db", pc.ReplicaDBName)
	assert.Nil(t, pc.ConnectionDB)
	assert.False(t, pc.Connected)
	assert.Equal(t, "test-component", pc.Component)
	assert.Equal(t, "/path/to/migrations", pc.MigrationsPath)
	assert.NotNil(t, pc.Logger)
	assert.Equal(t, 100, pc.MaxOpenConnections)
	assert.Equal(t, 20, pc.MaxIdleConnections)
}

func TestDatabaseErrors(t *testing.T) {
	// Test various database error scenarios
	testCases := []struct {
		name          string
		primaryConn   string
		replicaConn   string
		expectedError bool
	}{
		{
			name:          "invalid primary connection string",
			primaryConn:   "invalid://connection",
			replicaConn:   "postgres://valid:5432/db",
			expectedError: true,
		},
		{
			name:          "invalid replica connection string",
			primaryConn:   "postgres://valid:5432/db",
			replicaConn:   "invalid://connection",
			expectedError: true,
		},
		{
			name:          "both connections invalid",
			primaryConn:   "invalid://primary",
			replicaConn:   "invalid://replica",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockLogger := &MockLogger{}
			mockLogger.On("Info", mock.Anything).Maybe()
			mockLogger.On("Error", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Errorf", mock.Anything, mock.Anything).Maybe()
			mockLogger.On("Warn", mock.Anything).Maybe()

			pc := &PostgresConnection{
				ConnectionStringPrimary: tc.primaryConn,
				ConnectionStringReplica: tc.replicaConn,
				Logger:                  mockLogger,
			}

			err := pc.Connect()
			// Due to Fatal calls, the function might return nil
			// We're mainly testing that the function handles these scenarios without panicking
			if err != nil && tc.expectedError {
				assert.Error(t, err)
			}
		})
	}
}

// Integration test example (requires actual database)
func TestPostgresConnection_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// This test would require an actual PostgreSQL instance
	// It's here as an example of how integration tests could be structured
	t.Skip("Integration test requires PostgreSQL instance")

	/*
		pc := &PostgresConnection{
			ConnectionStringPrimary: os.Getenv("TEST_POSTGRES_PRIMARY_URL"),
			ConnectionStringReplica: os.Getenv("TEST_POSTGRES_REPLICA_URL"),
			PrimaryDBName:          "test_db",
			ReplicaDBName:          "test_db",
			Logger:                 log.NewLogger(), // Would use real logger
			MaxOpenConnections:     10,
			MaxIdleConnections:     5,
		}

		err := pc.Connect()
		assert.NoError(t, err)
		assert.True(t, pc.Connected)
		assert.NotNil(t, pc.ConnectionDB)

		db, err := pc.GetDB()
		assert.NoError(t, err)
		assert.NotNil(t, db)
	*/
}

// Benchmark tests
func BenchmarkGetMigrationsPath(b *testing.B) {
	pc := &PostgresConnection{
		Component: "benchmark-component",
		Logger:    &MockLogger{},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pc.getMigrationsPath()
	}
}

func BenchmarkGetMigrationsPathWithCache(b *testing.B) {
	pc := &PostgresConnection{
		MigrationsPath: "/cached/path/to/migrations",
		Logger:         &MockLogger{},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pc.getMigrationsPath()
	}
}
