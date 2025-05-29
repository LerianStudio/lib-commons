# PostgreSQL Migration Guide: From Fatal Calls to Commons-Go Patterns

## 🎯 Migration Overview

This guide provides step-by-step instructions for migrating PostgreSQL implementations across **Accounting** and **Smart-Templates** plugins from dangerous Fatal calls to robust commons-go patterns.

**Critical Impact**: Prevents application crashes and enables graceful error handling  
**Affected Components**: `plugins/accounting`, `plugins/smart-templates`  
**Risk Level**: LOW (with proper testing)  
**Estimated Time**: 2-4 hours per plugin  

## 📋 Pre-Migration Checklist

### Prerequisites
- [ ] Commons-go library updated to latest version with postgres package
- [ ] Test environment available for validation
- [ ] Database backup completed (if testing with real data)
- [ ] Team coordination for deployment timeline

### Risk Assessment
- **Current Risk**: HIGH - Fatal calls crash entire application
- **Migration Risk**: LOW - Error handling improvements are backwards compatible
- **Post-Migration Risk**: VERY LOW - Graceful error handling and recovery

## 🔧 Phase 1: Emergency Fatal Call Fix (30 minutes)

### Priority: CRITICAL - Immediate crash prevention

This phase can be deployed immediately to prevent application crashes without changing the architecture.

#### Step 1.1: Replace Fatal Calls with Error Returns

**File**: `plugins/accounting/pkg/postgres/postgres_connection.go`  
**File**: `plugins/smart-templates/pkg/postgres/postgres_connection.go`

```go
// Before (DANGEROUS):
func (pc *PostgresConnection) Connect() error {
    dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
    if err != nil {
        pc.Logger.Fatal("failed to open connect to primary database", zap.Error(err))
        return nil  // Never reached!
    }
    // ... more Fatal calls
}

// After (SAFE):
func (pc *PostgresConnection) Connect() error {
    dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
    if err != nil {
        pc.Logger.Error("failed to open connect to primary database", zap.Error(err))
        return fmt.Errorf("failed to open primary database connection: %w", err)
    }
    
    dbReadOnlyReplica, err := sql.Open("pgx", pc.ConnectionStringReplica)
    if err != nil {
        pc.Logger.Error("failed to open connect to replica database", zap.Error(err))
        return fmt.Errorf("failed to open replica database connection: %w", err)
    }
    
    migrationsPath, err := filepath.Abs("../../migrations")
    if err != nil {
        pc.Logger.Error("failed get filepath", zap.Error(err))
        return fmt.Errorf("failed to get migrations path: %w", err)
    }
    
    primaryURL, err := url.Parse(filepath.ToSlash(migrationsPath))
    if err != nil {
        pc.Logger.Error("failed parse url", zap.Error(err))
        return fmt.Errorf("failed to parse migrations URL: %w", err)
    }
    
    primaryDriver, err := postgres.WithInstance(dbPrimary, &postgres.Config{
        MultiStatementEnabled: true,
        DatabaseName:          pc.PrimaryDBName,
        SchemaName:            "public",
    })
    if err != nil {
        pc.Logger.Error("failed to open connect to database", zap.Error(err))
        return fmt.Errorf("failed to create postgres driver: %w", err)
    }
    
    m, err := migrate.NewWithDatabaseInstance(primaryURL.String(), pc.PrimaryDBName, primaryDriver)
    if err != nil {
        pc.Logger.Error("failed to get migrations", zap.Error(err))
        return fmt.Errorf("failed to create migration instance: %w", err)
    }
    
    if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
        pc.Logger.Error("failed to run migrations", zap.Error(err))
        return fmt.Errorf("failed to run migrations: %w", err)
    }
    
    if err := connectionDB.Ping(); err != nil {
        pc.Logger.Error("failed to ping database", zap.Error(err))
        return fmt.Errorf("failed to ping database: %w", err)
    }
    
    pc.Connected = true
    pc.ConnectionDB = &connectionDB
    pc.Logger.Info("Connected to postgres ✅")
    
    return nil
}
```

#### Step 1.2: Update Callers to Handle Errors

**Files**: Where `Connect()` is called (typically in bootstrap/initialization)

```go
// Before:
postgresConn.Connect()  // Ignored potential errors

// After:
if err := postgresConn.Connect(); err != nil {
    logger.Error("Failed to connect to postgres", "error", err)
    return fmt.Errorf("postgres initialization failed: %w", err)
}
```

#### Step 1.3: Quick Validation Test

```bash
# Test that the application starts and handles database connection errors gracefully
# Set invalid database credentials to test error handling
export POSTGRES_PRIMARY_URL="invalid://connection"
./your-app

# Should see error logs instead of application crash
# Expected: "postgres initialization failed: failed to open primary database connection"
# Not expected: Application exit/crash
```

**✅ Deploy Phase 1 Immediately** - This prevents production crashes with minimal risk.

## 🏗️ Phase 2: Commons-Go Integration (1-2 hours)

### Step 2.1: Add Commons-Go Dependency

```bash
# Update go.mod to use latest commons-go with postgres package
go get github.com/LerianStudio/lib-commons@latest
go mod tidy
```

#### Step 2.2: Replace Custom PostgresConnection

**Create new file**: `pkg/postgres/enhanced_connection.go`

```go
package postgres

import (
    "context"
    "fmt"
    "time"
    
    commonsPostgres "github.com/LerianStudio/lib-commons/commons/postgres"
    "github.com/LerianStudio/lib-commons/commons/log"
)

// Enhanced wrapper around commons-go PostgresConnection
type EnhancedPostgresConnection struct {
    *commonsPostgres.PostgresConnection
    config *Config
}

type Config struct {
    PrimaryConnectionString string `env:"POSTGRES_PRIMARY_URL"`
    ReplicaConnectionString string `env:"POSTGRES_REPLICA_URL"`
    PrimaryDBName          string `env:"POSTGRES_PRIMARY_DB"`
    ReplicaDBName          string `env:"POSTGRES_REPLICA_DB"`
    ComponentName          string `env:"COMPONENT_NAME"`
    MigrationsPath         string `env:"MIGRATIONS_PATH"`
    MaxOpenConnections     int    `env:"POSTGRES_MAX_OPEN_CONN" envDefault:"100"`
    MaxIdleConnections     int    `env:"POSTGRES_MAX_IDLE_CONN" envDefault:"100"`
}

func NewEnhancedPostgresConnection(config *Config, logger log.Logger) (*EnhancedPostgresConnection, error) {
    if err := config.Validate(); err != nil {
        return nil, fmt.Errorf("invalid postgres config: %w", err)
    }

    commonsConn := &commonsPostgres.PostgresConnection{
        ConnectionStringPrimary: config.PrimaryConnectionString,
        ConnectionStringReplica: config.ReplicaConnectionString,
        PrimaryDBName:          config.PrimaryDBName,
        ReplicaDBName:          config.ReplicaDBName,
        Component:              config.ComponentName,
        MigrationsPath:         config.MigrationsPath,
        Logger:                 logger,
        MaxOpenConnections:     config.MaxOpenConnections,
        MaxIdleConnections:     config.MaxIdleConnections,
    }

    return &EnhancedPostgresConnection{
        PostgresConnection: commonsConn,
        config:            config,
    }, nil
}

func (c *Config) Validate() error {
    if c.PrimaryConnectionString == "" {
        return errors.New("primary connection string is required")
    }
    if c.PrimaryDBName == "" {
        return errors.New("primary database name is required")
    }
    if c.ComponentName == "" {
        return errors.New("component name is required")
    }
    return nil
}

func (epc *EnhancedPostgresConnection) ConnectSafely(ctx context.Context) error {
    logger := epc.Logger.WithFields("component", "postgres", "operation", "connect")
    
    // Commons-go Connect() already handles errors properly (no Fatal calls)
    if err := epc.Connect(); err != nil {
        logger.Error("Failed to connect to postgres", "error", err)
        return fmt.Errorf("postgres connection failed: %w", err)
    }
    
    logger.Info("Successfully connected to postgres")
    return nil
}
```

#### Step 2.3: Update Bootstrap Configuration

**File**: `bootstrap/config.go` or initialization file

```go
// Before:
type Config struct {
    // ... existing fields
}

func InitServers() (*Service, error) {
    // ... existing code ...
    
    postgresConn := &postgres.PostgresConnection{
        ConnectionStringPrimary: cfg.PostgresPrimaryURL,
        // ... other fields
    }
}

// After:
type Config struct {
    // ... existing fields ...
    // Add postgres-specific configuration
    PostgresPrimaryURL     string `env:"POSTGRES_PRIMARY_URL"`
    PostgresReplicaURL     string `env:"POSTGRES_REPLICA_URL"`
    PostgresPrimaryDB      string `env:"POSTGRES_PRIMARY_DB"`
    PostgresReplicaDB      string `env:"POSTGRES_REPLICA_DB"`
    PostgresMaxOpenConn    int    `env:"POSTGRES_MAX_OPEN_CONN" envDefault:"100"`
    PostgresMaxIdleConn    int    `env:"POSTGRES_MAX_IDLE_CONN" envDefault:"100"`
}

func InitServers() (*Service, error) {
    // ... existing configuration loading ...
    
    postgresConfig := &postgres.Config{
        PrimaryConnectionString: cfg.PostgresPrimaryURL,
        ReplicaConnectionString: cfg.PostgresReplicaURL,
        PrimaryDBName:          cfg.PostgresPrimaryDB,
        ReplicaDBName:          cfg.PostgresReplicaDB,
        ComponentName:          "your-component-name",
        MigrationsPath:         "../../migrations",
        MaxOpenConnections:     cfg.PostgresMaxOpenConn,
        MaxIdleConnections:     cfg.PostgresMaxIdleConn,
    }
    
    postgresConn, err := postgres.NewEnhancedPostgresConnection(postgresConfig, logger)
    if err != nil {
        return nil, fmt.Errorf("failed to create postgres connection: %w", err)
    }
    
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    if err := postgresConn.ConnectSafely(ctx); err != nil {
        return nil, fmt.Errorf("failed to connect to postgres: %w", err)
    }
    
    // ... rest of initialization
}
```

#### Step 2.4: Update Repository Usage

**File**: Where the postgres connection is used

```go
// Before:
db, err := postgresConn.GetDB()
if err != nil {
    return err
}

// After (commons-go provides better interface):
db, err := postgresConn.GetDB()
if err != nil {
    return fmt.Errorf("failed to get database connection: %w", err)
}
// Usage remains the same - GetDB() interface is compatible
```

## 🛡️ Phase 3: Enhanced Reliability (30-60 minutes)

### Step 3.1: Add Circuit Breaker Protection

```go
import (
    "github.com/LerianStudio/lib-commons/commons/circuitbreaker"
)

type ResilientPostgresConnection struct {
    *EnhancedPostgresConnection
    circuitBreaker *circuitbreaker.CircuitBreaker
}

func NewResilientPostgresConnection(config *Config, logger log.Logger) (*ResilientPostgresConnection, error) {
    enhanced, err := NewEnhancedPostgresConnection(config, logger)
    if err != nil {
        return nil, err
    }

    cb := circuitbreaker.New("postgres-connection",
        circuitbreaker.WithThreshold(5),
        circuitbreaker.WithTimeout(30*time.Second),
        circuitbreaker.WithOnStateChange(func(change circuitbreaker.StateChange) {
            logger.Info("Postgres circuit breaker state changed",
                "from", change.From, "to", change.To)
        }),
    )

    return &ResilientPostgresConnection{
        EnhancedPostgresConnection: enhanced,
        circuitBreaker:            cb,
    }, nil
}

func (rpc *ResilientPostgresConnection) GetDBSafely() (dbresolver.DB, error) {
    var db dbresolver.DB
    err := rpc.circuitBreaker.Execute(func() error {
        var connErr error
        db, connErr = rpc.GetDB()
        return connErr
    })
    return db, err
}
```

### Step 3.2: Add Health Checks

```go
import (
    "github.com/LerianStudio/lib-commons/commons/health"
)

func (rpc *ResilientPostgresConnection) RegisterHealthCheck(healthService *health.Service) error {
    primaryDB := rpc.GetPrimaryDB()
    if primaryDB == nil {
        return errors.New("primary database connection not available")
    }

    healthService.RegisterChecker("postgres", &health.PostgresChecker{
        DB: primaryDB,
    })
    
    return nil
}

// In bootstrap/initialization:
healthService := health.NewService("your-service", "v1.0.0", cfg.Environment, "")
if err := postgresConn.RegisterHealthCheck(healthService); err != nil {
    logger.Warn("Failed to register postgres health check", "error", err)
    // Non-fatal - continue without health check
}
```

## 🧪 Testing & Validation

### Test Suite for Each Phase

#### Phase 1 Tests: Fatal Call Elimination
```go
func TestConnect_DatabaseDown_NoFatal(t *testing.T) {
    config := &Config{
        PrimaryConnectionString: "postgres://invalid:connection@localhost:5432/test",
        PrimaryDBName:          "test",
        ComponentName:          "test",
    }
    
    conn, err := NewEnhancedPostgresConnection(config, logger)
    require.NoError(t, err)
    
    // This should return an error, not crash the test process
    err = conn.ConnectSafely(context.Background())
    
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "postgres connection failed")
    // Test process should still be running - no Fatal call
}
```

#### Phase 2 Tests: Commons-Go Integration
```go
func TestEnhancedConnection_ValidConfiguration(t *testing.T) {
    config := &Config{
        PrimaryConnectionString: "postgres://user:pass@localhost:5432/test",
        ReplicaConnectionString: "postgres://user:pass@localhost:5433/test",
        PrimaryDBName:          "test",
        ReplicaDBName:          "test_replica",
        ComponentName:          "test-component",
        MaxOpenConnections:     50,
        MaxIdleConnections:     25,
    }
    
    conn, err := NewEnhancedPostgresConnection(config, logger)
    
    assert.NoError(t, err)
    assert.NotNil(t, conn)
    assert.Equal(t, 50, conn.MaxOpenConnections)
    assert.Equal(t, 25, conn.MaxIdleConnections)
}

func TestConfig_Validation(t *testing.T) {
    testCases := []struct {
        name        string
        config      *Config
        expectError bool
        errorMsg    string
    }{
        {
            name:        "missing primary connection",
            config:      &Config{},
            expectError: true,
            errorMsg:    "primary connection string is required",
        },
        {
            name: "valid config",
            config: &Config{
                PrimaryConnectionString: "postgres://localhost:5432/test",
                PrimaryDBName:          "test",
                ComponentName:          "test",
            },
            expectError: false,
        },
    }
    
    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            err := tc.config.Validate()
            if tc.expectError {
                assert.Error(t, err)
                assert.Contains(t, err.Error(), tc.errorMsg)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

#### Phase 3 Tests: Enhanced Reliability
```go
func TestCircuitBreaker_DatabaseFailures(t *testing.T) {
    // Mock connection that fails
    config := &Config{
        PrimaryConnectionString: "postgres://fail:fail@localhost:5432/fail",
        PrimaryDBName:          "fail",
        ComponentName:          "test",
    }
    
    conn, err := NewResilientPostgresConnection(config, logger)
    require.NoError(t, err)
    
    // Trigger circuit breaker by repeated failures
    for i := 0; i < 6; i++ {
        _, err := conn.GetDBSafely()
        assert.Error(t, err)
    }
    
    // Circuit breaker should be open now
    assert.Equal(t, circuitbreaker.StateOpen, conn.circuitBreaker.State())
}
```

### Integration Testing

```bash
#!/bin/bash
# test-postgres-migration.sh

echo "Testing Phase 1: Fatal call elimination..."
# Start with invalid DB config - should get error, not crash
export POSTGRES_PRIMARY_URL="invalid://connection"
timeout 10s ./your-app 2>&1 | grep -q "postgres connection failed" || { echo "Phase 1 FAILED"; exit 1; }
echo "Phase 1 PASSED ✅"

echo "Testing Phase 2: Commons-go integration..."
# Test with valid config
export POSTGRES_PRIMARY_URL="postgres://user:pass@localhost:5432/testdb"
timeout 10s ./your-app 2>&1 | grep -q "Successfully connected to postgres" || { echo "Phase 2 FAILED"; exit 1; }
echo "Phase 2 PASSED ✅"

echo "Testing Phase 3: Health checks..."
# Test health endpoint
curl -f http://localhost:8080/health | grep -q "postgres" || { echo "Phase 3 FAILED"; exit 1; }
echo "Phase 3 PASSED ✅"

echo "All phases PASSED! 🎉"
```

## 📋 Deployment Checklist

### Pre-Deployment
- [ ] All tests passing in development environment
- [ ] Configuration environment variables updated
- [ ] Database migrations tested (if applicable)
- [ ] Rollback plan prepared

### Deployment Steps
1. **Deploy Phase 1** (Fatal call fixes)
   - Low risk, immediate crash prevention
   - Can be deployed independently
   - Monitor error logs for proper error handling

2. **Deploy Phase 2** (Commons-go integration)
   - Update configuration management
   - Monitor connection metrics
   - Verify functionality unchanged

3. **Deploy Phase 3** (Enhanced reliability)
   - Add health check monitoring
   - Configure alerting for circuit breaker state changes
   - Monitor performance improvements

### Post-Deployment Validation
- [ ] Application starts successfully
- [ ] Database connections working correctly
- [ ] Health checks responding
- [ ] Error handling working as expected (test with invalid config)
- [ ] Performance metrics within acceptable range
- [ ] No application crashes under error conditions

## 🚨 Rollback Plan

If issues occur during migration:

### Phase 1 Rollback
Simply revert the Fatal call changes - very low risk as this only improves error handling.

### Phase 2 Rollback
1. Revert to original PostgresConnection implementation
2. Remove commons-go dependency usage
3. Update configuration back to original format

### Phase 3 Rollback
1. Remove circuit breaker and health check code
2. Revert to Phase 2 implementation
3. Remove health check endpoints from load balancer configuration

## 📈 Success Metrics

### Immediate Improvements (Phase 1)
- **Zero application crashes** from database connection issues
- **Error visibility** - proper error logging instead of silent crashes
- **Graceful degradation** - application can handle and report database issues

### Medium-term Improvements (Phase 2)
- **Code consolidation** - eliminate duplicate postgres code across plugins
- **Configuration standardization** - consistent postgres configuration patterns
- **Improved maintainability** - leverage commons-go maintenance and improvements

### Long-term Improvements (Phase 3)
- **Enhanced reliability** - circuit breaker prevents cascading failures
- **Better monitoring** - health checks enable proactive monitoring
- **Faster recovery** - automatic circuit breaker recovery from transient issues

---

**🎯 Success Criteria**: Zero application crashes from database connection issues and proper error reporting in logs. Each phase builds incrementally on the previous one, allowing for safe, gradual migration with immediate benefits. 