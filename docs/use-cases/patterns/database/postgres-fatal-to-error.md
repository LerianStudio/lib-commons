# PostgreSQL Fatal Calls → Proper Error Handling

## 🚨 Critical Issue: Application Crashes from Fatal Calls

**Impact**: HIGH - Application crashes on database connection issues  
**Found in**: `plugins/accounting/pkg/postgres/` and `plugins/smart-templates/pkg/postgres/`  
**Risk**: Production outages, service unavailability  

## 📊 Current Problematic Pattern

Both accounting and smart-templates plugins contain **identical code** with dangerous `Logger.Fatal()` calls:

### ❌ Before: Problematic Code (plugins/accounting & smart-templates)

```go
// plugins/accounting/pkg/postgres/postgres_connection.go
// plugins/smart-templates/pkg/postgres/postgres_connection.go

func (pc *PostgresConnection) Connect() error {
    pc.Logger.Info("Connecting to primary and replica databases...")

    dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
    if err != nil {
        pc.Logger.Fatal("failed to open connect to primary database", zap.Error(err))
        return nil  // ⚠️ Never reached due to Fatal call!
    }

    dbReadOnlyReplica, err := sql.Open("pgx", pc.ConnectionStringReplica)
    if err != nil {
        pc.Logger.Fatal("failed to open connect to replica database", zap.Error(err))
        return nil  // ⚠️ Never reached due to Fatal call!
    }

    migrationsPath, err := filepath.Abs("../../migrations")
    if err != nil {
        pc.Logger.Fatal("failed get filepath", zap.Error(err))
        return err  // ⚠️ Never reached due to Fatal call!
    }

    primaryURL, err := url.Parse(filepath.ToSlash(migrationsPath))
    if err != nil {
        pc.Logger.Fatal("failed parse url", zap.Error(err))
        return err  // ⚠️ Never reached due to Fatal call!
    }

    primaryDriver, err := postgres.WithInstance(dbPrimary, &postgres.Config{...})
    if err != nil {
        pc.Logger.Fatalf("failed to open connect to database %v", zap.Error(err))
        return nil  // ⚠️ Never reached due to Fatal call!
    }

    m, err := migrate.NewWithDatabaseInstance(primaryURL.String(), pc.PrimaryDBName, primaryDriver)
    if err != nil {
        pc.Logger.Fatal("failed to get migrations", zap.Error(err))
        return err  // ⚠️ Never reached due to Fatal call!
    }
    
    // ... more code with similar Fatal patterns
}
```

### 💥 Problems with Current Code

1. **Application Crashes**: `Fatal()` calls `os.Exit(1)` - entire application terminates
2. **No Graceful Degradation**: Cannot recover from temporary database issues  
3. **Poor Observability**: Crashes don't leave traces for debugging
4. **Code Duplication**: Identical code in two plugins
5. **Unreachable Returns**: Code after `Fatal()` never executes
6. **Production Risk**: Network blips or temporary DB issues kill the service

## ✅ After: Improved Code Using Commons-Go

### Step 1: Replace Custom Implementation with Commons-Go

```go
// Refactored using commons-go postgres patterns
package postgres

import (
    "context"
    "github.com/LerianStudio/lib-commons/commons/postgres"
    "github.com/LerianStudio/lib-commons/commons/log"
)

type PostgresConnection struct {
    // Use commons-go PostgresConnection instead of custom implementation
    *postgres.PostgresConnection
}

func NewPostgresConnection(config PostgresConfig, logger log.Logger) *PostgresConnection {
    return &PostgresConnection{
        PostgresConnection: &postgres.PostgresConnection{
            ConnectionStringPrimary: config.PrimaryConnectionString,
            ConnectionStringReplica: config.ReplicaConnectionString,
            PrimaryDBName:          config.PrimaryDBName,
            ReplicaDBName:          config.ReplicaDBName,
            Component:              config.ComponentName,
            MigrationsPath:         config.MigrationsPath,
            Logger:                 logger,
            MaxOpenConnections:     config.MaxOpenConnections,
            MaxIdleConnections:     config.MaxIdleConnections,
        },
    }
}

func (pc *PostgresConnection) ConnectWithRetry(ctx context.Context) error {
    logger := pc.Logger.WithFields("component", "postgres", "operation", "connect")
    
    // Commons-go postgres.Connect() returns errors instead of calling Fatal
    if err := pc.Connect(); err != nil {
        logger.Error("Failed to connect to postgres", "error", err)
        return fmt.Errorf("postgres connection failed: %w", err)
    }
    
    logger.Info("Successfully connected to postgres with proper error handling")
    return nil
}
```

### Step 2: Add Circuit Breaker for Enhanced Reliability

```go
import (
    "github.com/LerianStudio/lib-commons/commons/circuitbreaker"
)

type EnhancedPostgresConnection struct {
    *PostgresConnection
    circuitBreaker *circuitbreaker.CircuitBreaker
}

func NewEnhancedPostgresConnection(config PostgresConfig, logger log.Logger) *EnhancedPostgresConnection {
    cb := circuitbreaker.New("postgres-connection",
        circuitbreaker.WithThreshold(5),
        circuitbreaker.WithTimeout(30*time.Second),
        circuitbreaker.WithOnStateChange(func(change circuitbreaker.StateChange) {
            logger.Info("Postgres circuit breaker state changed", 
                "from", change.From, "to", change.To)
        }),
    )

    return &EnhancedPostgresConnection{
        PostgresConnection: NewPostgresConnection(config, logger),
        circuitBreaker:     cb,
    }
}

func (epc *EnhancedPostgresConnection) ConnectSafely(ctx context.Context) error {
    return epc.circuitBreaker.ExecuteWithContext(ctx, func() error {
        return epc.ConnectWithRetry(ctx)
    })
}
```

### Step 3: Add Health Checks

```go
import (
    "github.com/LerianStudio/lib-commons/commons/health"
)

func (epc *EnhancedPostgresConnection) RegisterHealthCheck(healthService *health.Service) {
    healthService.RegisterChecker("postgres", &health.PostgresChecker{
        DB: epc.GetPrimaryDB(), // Method from commons-go postgres
    })
}

func (epc *EnhancedPostgresConnection) IsHealthy(ctx context.Context) bool {
    db, err := epc.GetDB()
    if err != nil {
        return false
    }
    
    return db.PingContext(ctx) == nil
}
```

## 🔄 Migration Steps

### Phase 1: Safe Error Handling Migration (Low Risk)

1. **Replace Fatal calls with Error + Return**:
   ```go
   // Before:
   pc.Logger.Fatal("failed to connect", zap.Error(err))
   
   // After:
   pc.Logger.Error("failed to connect", "error", err)
   return fmt.Errorf("postgres connection failed: %w", err)
   ```

2. **Update Callers to Handle Errors**:
   ```go
   // In bootstrap/config.go or similar
   if err := postgresConn.Connect(); err != nil {
       logger.Error("Failed to initialize postgres", "error", err)
       return nil, fmt.Errorf("database initialization failed: %w", err)
   }
   ```

### Phase 2: Adopt Commons-Go Patterns (Medium Risk)

1. **Replace Custom PostgresConnection**:
   - Import `github.com/LerianStudio/lib-commons/commons/postgres`
   - Replace custom struct with commons-go `PostgresConnection`
   - Update configuration mapping

2. **Add Proper Configuration**:
   ```go
   type PostgresConfig struct {
       PrimaryConnectionString string `env:"POSTGRES_PRIMARY_URL"`
       ReplicaConnectionString string `env:"POSTGRES_REPLICA_URL"`
       PrimaryDBName          string `env:"POSTGRES_PRIMARY_DB"`
       ReplicaDBName          string `env:"POSTGRES_REPLICA_DB"`
       ComponentName          string `env:"COMPONENT_NAME"`
       MigrationsPath         string `env:"MIGRATIONS_PATH"`
       MaxOpenConnections     int    `env:"POSTGRES_MAX_OPEN_CONN" envDefault:"100"`
       MaxIdleConnections     int    `env:"POSTGRES_MAX_IDLE_CONN" envDefault:"100"`
   }
   ```

### Phase 3: Enhanced Reliability (High Value)

1. **Add Circuit Breaker**: Prevent cascading failures
2. **Add Health Checks**: Enable monitoring and load balancer integration  
3. **Add Observability**: Integrate with commons-go observability patterns

## 📈 Benefits Gained

| Aspect           | Before                      | After                                |
| ---------------- | --------------------------- | ------------------------------------ |
| **Reliability**  | App crashes on DB issues    | Graceful error handling              |
| **Recovery**     | Manual restart required     | Automatic retry with circuit breaker |
| **Monitoring**   | No visibility into failures | Health checks + observability        |
| **Code Quality** | Duplicated across plugins   | Shared commons-go implementation     |
| **Testing**      | Hard to test (Fatal exits)  | Easily testable error paths          |
| **Production**   | High risk of outages        | Production-ready resilience          |

## 🧪 Testing the Migration

### Before: Untestable Fatal Calls
```go
// Cannot test Fatal calls - they exit the test process
func TestConnect_Failure(t *testing.T) {
    // This test cannot be written safely
}
```

### After: Testable Error Handling
```go
func TestConnectWithRetry_DatabaseDown(t *testing.T) {
    // Mock database connection failure
    mockDB := &postgres.MockConnection{
        ConnectFunc: func() error {
            return errors.New("connection refused")
        },
    }
    
    conn := NewEnhancedPostgresConnection(config, logger)
    conn.PostgresConnection = mockDB
    
    err := conn.ConnectWithRetry(context.Background())
    
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "connection refused")
    // Application didn't crash - test passes!
}

func TestCircuitBreaker_OpenState(t *testing.T) {
    conn := NewEnhancedPostgresConnection(config, logger)
    
    // Simulate failures to open circuit breaker
    for i := 0; i < 6; i++ {
        err := conn.ConnectSafely(context.Background())
        assert.Error(t, err)
    }
    
    // Circuit breaker should be open now
    assert.Equal(t, circuitbreaker.StateOpen, conn.circuitBreaker.State())
}
```

## 🎯 Implementation Priority

1. **Critical**: Fix Fatal calls in accounting and smart-templates (immediate crash prevention)
2. **High**: Migrate to commons-go patterns (eliminate code duplication)  
3. **Medium**: Add circuit breaker (enhanced reliability)
4. **Nice-to-have**: Add advanced observability patterns

## 📋 Component-Specific Examples

- [Accounting Plugin Migration](../../components/smart-templates/postgres-migration.md)
- [Smart-Templates Plugin Migration](../../components/accounting/postgres-migration.md)
- [Core Services Migration](../../components/core/postgres-migration.md)

---

**⚠️ Critical Action Required**: The Fatal calls in production code represent a significant operational risk and should be addressed immediately. Start with Phase 1 migration to eliminate crash risks. 