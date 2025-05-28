# Fixing Fatal Calls Anti-Pattern in Postgres Package

## Problem

The current postgres package uses `Fatal()` calls which terminate the entire application, making error handling impossible for consuming applications.

## Current Problematic Code

```go
// libs/commons-go/commons/postgres/postgres.go
func (pc *PostgresConnection) Connect() error {
    pc.Logger.Info("Connecting to primary and replica databases...")

    dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
    if err != nil {
        pc.Logger.Fatal("failed to open connect to primary database", zap.Error(err))
        return nil  // Unreachable code!
    }

    // More Fatal calls...
    dbReadOnlyReplica, err := sql.Open("pgx", pc.ConnectionStringReplica)
    if err != nil {
        pc.Logger.Fatal("failed to open connect to replica database", zap.Error(err))
        return nil  // Unreachable code!
    }
}
```

## Fixed Version

```go
// Improved postgres connection with proper error handling
func (pc *PostgresConnection) Connect() error {
    pc.Logger.Info("Connecting to primary and replica databases...")

    // Primary database connection
    dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
    if err != nil {
        pc.Logger.Error("failed to open connection to primary database", zap.Error(err))
        return fmt.Errorf("failed to connect to primary database: %w", err)
    }

    // Test primary connection
    if err := dbPrimary.Ping(); err != nil {
        dbPrimary.Close()
        pc.Logger.Error("failed to ping primary database", zap.Error(err))
        return fmt.Errorf("primary database health check failed: %w", err)
    }

    // Configure primary connection pool
    dbPrimary.SetMaxOpenConns(pc.MaxOpenConnections)
    dbPrimary.SetMaxIdleConns(pc.MaxIdleConnections)
    dbPrimary.SetConnMaxLifetime(time.Minute * 30)

    // Replica database connection
    dbReadOnlyReplica, err := sql.Open("pgx", pc.ConnectionStringReplica)
    if err != nil {
        dbPrimary.Close() // Cleanup primary on replica failure
        pc.Logger.Error("failed to open connection to replica database", zap.Error(err))
        return fmt.Errorf("failed to connect to replica database: %w", err)
    }

    // Test replica connection
    if err := dbReadOnlyReplica.Ping(); err != nil {
        dbPrimary.Close()
        dbReadOnlyReplica.Close()
        pc.Logger.Error("failed to ping replica database", zap.Error(err))
        return fmt.Errorf("replica database health check failed: %w", err)
    }

    // Configure replica connection pool
    dbReadOnlyReplica.SetMaxOpenConns(pc.MaxOpenConnections)
    dbReadOnlyReplica.SetMaxIdleConns(pc.MaxIdleConnections)
    dbReadOnlyReplica.SetConnMaxLifetime(time.Minute * 30)

    // Create dbresolver with both connections
    connectionDB := dbresolver.New(
        dbresolver.WithPrimaryDBs(dbPrimary),
        dbresolver.WithReplicaDBs(dbReadOnlyReplica),
        dbresolver.WithLoadBalancer(dbresolver.RoundRobinLB))

    // Run migrations
    if err := pc.runMigrations(dbPrimary); err != nil {
        dbPrimary.Close()
        dbReadOnlyReplica.Close()
        return fmt.Errorf("failed to run migrations: %w", err)
    }

    // Final health check
    if err := connectionDB.Ping(); err != nil {
        dbPrimary.Close()
        dbReadOnlyReplica.Close()
        pc.Logger.Error("final database health check failed", zap.Error(err))
        return fmt.Errorf("database health check failed: %w", err)
    }

    pc.ConnectionDB = connectionDB
    pc.Connected = true
    pc.Logger.Info("Successfully connected to primary and replica databases")

    return nil
}

// Separated migration logic for better error handling
func (pc *PostgresConnection) runMigrations(db *sql.DB) error {
    if pc.MigrationsPath == "" {
        pc.Logger.Info("No migrations path specified, skipping migrations")
        return nil
    }

    migrationsPath, err := pc.getMigrationsPath()
    if err != nil {
        return fmt.Errorf("failed to resolve migrations path: %w", err)
    }

    primaryURL, err := url.Parse(filepath.ToSlash(migrationsPath))
    if err != nil {
        return fmt.Errorf("failed to parse migrations path: %w", err)
    }

    primaryURL.Scheme = "file"

    primaryDriver, err := postgres.WithInstance(db, &postgres.Config{
        MultiStatementEnabled: true,
        DatabaseName:          pc.PrimaryDBName,
        SchemaName:            "public",
    })
    if err != nil {
        return fmt.Errorf("failed to create migration driver: %w", err)
    }

    m, err := migrate.NewWithDatabaseInstance(primaryURL.String(), pc.PrimaryDBName, primaryDriver)
    if err != nil {
        return fmt.Errorf("failed to create migration instance: %w", err)
    }

    if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
        return fmt.Errorf("failed to run migrations: %w", err)
    }

    if err == migrate.ErrNoChange {
        pc.Logger.Info("No new migrations to apply")
    } else {
        pc.Logger.Info("Migrations applied successfully")
    }

    return nil
}
```

## Usage Example

### Before (Application Crashes)

```go
func main() {
    pc := &PostgresConnection{
        ConnectionStringPrimary: "invalid://connection",
        // ... other config
    }

    // This will terminate the entire application!
    err := pc.Connect()
    // Application never reaches here if connection fails
    
    fmt.Println("This line is never reached")
}
```

### After (Graceful Error Handling)

```go
func main() {
    pc := &PostgresConnection{
        ConnectionStringPrimary: "invalid://connection",
        // ... other config
    }

    // Proper error handling
    if err := pc.Connect(); err != nil {
        log.Printf("Failed to connect to database: %v", err)
        
        // Application can decide how to handle the error:
        // 1. Retry with backoff
        // 2. Fall back to different configuration
        // 3. Exit gracefully with proper cleanup
        
        os.Exit(1) // Controlled exit, not Fatal crash
    }

    fmt.Println("Database connected successfully!")
    
    // Application continues normally...
}
```

## Benefits of the Fix

1. **🔧 Proper Error Handling**: Applications can handle database connection failures appropriately
2. **🧹 Resource Cleanup**: Connections are properly closed on failure
3. **📊 Better Logging**: Errors are logged with context but don't terminate the app
4. **🔄 Retry Capability**: Applications can implement retry logic
5. **🧪 Testability**: Unit tests can verify error conditions without process termination

## Migration Steps

1. **Update postgres package** with proper error returns
2. **Update consuming services** to handle returned errors
3. **Add retry logic** in service initialization
4. **Test error scenarios** to ensure graceful degradation

This fix alone prevents application crashes and enables proper error handling across the entire Midaz platform. 