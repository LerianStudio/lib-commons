# MongoDB Connection Improvements for Fees Plugin

## 📊 Current Implementation Analysis

**Found in**: `plugins/fees/bootstrap/config.go`  
**Impact**: MEDIUM - Basic connection patterns without optimization  
**Opportunity**: Enhanced performance, health monitoring, and error handling  

## 📋 Current Pattern in Fees Plugin

### ❌ Before: Basic MongoDB Connection

```go
// plugins/fees/bootstrap/config.go
func InitServers() *Service {
    cfg := &Config{}
    if err := commons.SetConfigFromEnvVars(cfg); err != nil {
        panic(err)  // ⚠️ Panic on configuration errors!
    }

    logger := zap.InitializeLogger()

    // Basic mongo connection setup
    mongoSource := fmt.Sprintf("%s://%s:%s@%s:%s",
        cfg.MongoURI, cfg.MongoDBUser, cfg.MongoDBPassword, cfg.MongoDBHost, cfg.MongoDBPort)

    mongoConnection := &mmongoDB.MongoConnection{
        ConnectionStringSource: mongoSource,
        Database:               cfg.MongoDBName,
        Logger:                 logger,
    }

    // No error handling for connection initialization
    packMongoDBRepository := mongoPack.NewPackageMongoDBRepository(mongoConnection)
    
    // Direct usage without health checks or circuit breakers
    service := &services.UseCase{
        PackageRepo: packMongoDBRepository,
        MidazClient: midazService,
    }
}
```

### 🔍 Issues with Current Implementation

1. **Panic on Config Errors**: Application crashes on configuration issues
2. **No Connection Health Checks**: Cannot detect/respond to MongoDB issues  
3. **Missing Connection Pooling Configuration**: Default settings may not be optimal
4. **No Circuit Breaker**: Can't handle MongoDB downtime gracefully
5. **Basic Error Handling**: No retry logic or degradation strategies
6. **No Observability**: Limited visibility into connection health and performance

## ✅ After: Enhanced MongoDB with Commons-Go Patterns

### Step 1: Improved Configuration and Error Handling

```go
// Enhanced configuration structure
type MongoConfig struct {
    URI         string `env:"MONGO_URI" envDefault:"mongodb"`
    Host        string `env:"MONGO_HOST" envDefault:"localhost"`
    Port        string `env:"MONGO_PORT" envDefault:"27017"`
    Database    string `env:"MONGO_NAME" validate:"required"`
    User        string `env:"MONGO_USER"`
    Password    string `env:"MONGO_PASSWORD"`
    MaxPoolSize uint64 `env:"MONGO_MAX_POOL_SIZE" envDefault:"100"`
    
    // Connection timeouts
    ConnectTimeout time.Duration `env:"MONGO_CONNECT_TIMEOUT" envDefault:"10s"`
    ServerTimeout  time.Duration `env:"MONGO_SERVER_TIMEOUT" envDefault:"30s"`
    
    // Retry configuration  
    MaxRetries     int           `env:"MONGO_MAX_RETRIES" envDefault:"3"`
    RetryDelay     time.Duration `env:"MONGO_RETRY_DELAY" envDefault:"1s"`
}

func (c *MongoConfig) Validate() error {
    if c.Database == "" {
        return errors.New("mongo database name is required")
    }
    if c.Host == "" {
        return errors.New("mongo host is required")
    }
    return nil
}

func (c *MongoConfig) ConnectionString() string {
    if c.User != "" && c.Password != "" {
        return fmt.Sprintf("%s://%s:%s@%s:%s",
            c.URI, c.User, c.Password, c.Host, c.Port)
    }
    return fmt.Sprintf("%s://%s:%s", c.URI, c.Host, c.Port)
}
```

### Step 2: Enhanced MongoDB Connection with Health Checks

```go
import (
    "github.com/LerianStudio/lib-commons/commons/mongo"
    "github.com/LerianStudio/lib-commons/commons/health"
    "github.com/LerianStudio/lib-commons/commons/circuitbreaker"
    "github.com/LerianStudio/lib-commons/commons/retry"
)

type EnhancedMongoConnection struct {
    *mongo.MongoConnection
    config         *MongoConfig
    healthChecker  *health.MongoChecker
    circuitBreaker *circuitbreaker.CircuitBreaker
    logger         log.Logger
}

func NewEnhancedMongoConnection(config *MongoConfig, logger log.Logger) (*EnhancedMongoConnection, error) {
    if err := config.Validate(); err != nil {
        return nil, fmt.Errorf("invalid mongo config: %w", err)
    }

    // Create commons-go mongo connection with enhanced settings
    mongoConn := &mongo.MongoConnection{
        ConnectionStringSource: config.ConnectionString(),
        Database:               config.Database,
        Logger:                 logger,
        MaxPoolSize:            config.MaxPoolSize,
    }

    // Add circuit breaker for MongoDB operations
    cb := circuitbreaker.New("mongodb-connection",
        circuitbreaker.WithThreshold(5),
        circuitbreaker.WithTimeout(30*time.Second),
        circuitbreaker.WithOnStateChange(func(change circuitbreaker.StateChange) {
            logger.Info("MongoDB circuit breaker state changed",
                "from", change.From, "to", change.To)
        }),
    )

    return &EnhancedMongoConnection{
        MongoConnection: mongoConn,
        config:         config,
        circuitBreaker: cb,
        logger:         logger,
    }, nil
}

func (emc *EnhancedMongoConnection) ConnectWithRetry(ctx context.Context) error {
    logger := emc.logger.WithFields("component", "mongodb", "operation", "connect")
    
    retryConfig := &retry.JitterConfig{
        Type:       retry.ExponentialJitter,
        BaseDelay:  emc.config.RetryDelay,
        MaxDelay:   emc.config.RetryDelay * 10,
        Multiplier: 2.0,
    }

    return retry.ExecuteWithJitter(func() error {
        return emc.circuitBreaker.ExecuteWithContext(ctx, func() error {
            if err := emc.Connect(ctx); err != nil {
                logger.Error("MongoDB connection attempt failed", "error", err)
                return fmt.Errorf("mongo connection failed: %w", err)
            }
            logger.Info("Successfully connected to MongoDB")
            return nil
        })
    }, retryConfig, emc.config.MaxRetries)
}

func (emc *EnhancedMongoConnection) RegisterHealthCheck(healthService *health.Service) error {
    client, err := emc.GetDB(context.Background())
    if err != nil {
        return fmt.Errorf("failed to get mongo client for health check: %w", err)
    }

    emc.healthChecker = health.NewMongoChecker(client)
    healthService.RegisterChecker("mongodb", emc.healthChecker)
    return nil
}
```

### Step 3: Enhanced Repository with Observability

```go
import (
    "github.com/LerianStudio/lib-commons/commons/observability"
)

type EnhancedPackageRepository struct {
    *mongoPack.PackageMongoDBRepository
    metrics *observability.MetricsCollector
    tracer  trace.Tracer
}

func NewEnhancedPackageRepository(
    mongoConn *EnhancedMongoConnection,
    provider observability.Provider,
) (*EnhancedPackageRepository, error) {
    
    baseRepo := mongoPack.NewPackageMongoDBRepository(mongoConn.MongoConnection)
    
    metricsCollector, err := observability.NewMetricsCollector(provider)
    if err != nil {
        return nil, fmt.Errorf("failed to create metrics collector: %w", err)
    }

    return &EnhancedPackageRepository{
        PackageMongoDBRepository: baseRepo,
        metrics:                  metricsCollector,
        tracer:                   provider.Tracer(),
    }, nil
}

func (epr *EnhancedPackageRepository) Create(
    ctx context.Context, 
    collection string, 
    pack *mongoPack.Package, 
    organizationID uuid.UUID,
) (*mongoPack.Package, error) {
    
    // Add distributed tracing
    ctx, span := epr.tracer.Start(ctx, "mongodb.package.create")
    defer span.End()
    
    // Add metrics collection
    timer := epr.metrics.NewTimer(ctx, "package_create", "mongodb")
    defer func() {
        timer.Stop(200) // Success status code
    }()

    // Add span attributes
    span.SetAttributes(
        attribute.String("collection", collection),
        attribute.String("organization_id", organizationID.String()),
    )

    result, err := epr.PackageMongoDBRepository.Create(ctx, collection, pack, organizationID)
    if err != nil {
        span.RecordError(err)
        epr.metrics.RecordError(ctx, "package_create", "mongodb", "create_failed")
        return nil, fmt.Errorf("failed to create package: %w", err)
    }

    return result, nil
}
```

### Step 4: Improved Bootstrap with Error Handling

```go
func InitServers() (*Service, error) {
    cfg := &Config{}
    
    // Replace panic with proper error handling
    if err := commons.SetConfigFromEnvVars(cfg); err != nil {
        return nil, fmt.Errorf("failed to load configuration: %w", err)
    }

    logger := zap.InitializeLogger()

    // Create enhanced MongoDB configuration
    mongoConfig := &MongoConfig{
        URI:      cfg.MongoURI,
        Host:     cfg.MongoDBHost,
        Port:     cfg.MongoDBPort,
        Database: cfg.MongoDBName,
        User:     cfg.MongoDBUser,
        Password: cfg.MongoDBPassword,
    }

    // Initialize enhanced MongoDB connection
    mongoConn, err := NewEnhancedMongoConnection(mongoConfig, logger)
    if err != nil {
        return nil, fmt.Errorf("failed to create mongo connection: %w", err)
    }

    // Connect with retry logic
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    if err := mongoConn.ConnectWithRetry(ctx); err != nil {
        return nil, fmt.Errorf("failed to connect to mongodb: %w", err)
    }

    // Initialize observability provider
    obsProvider, err := observability.New(ctx,
        observability.WithServiceName("fees-service"),
        observability.WithEnvironment(cfg.EnvName),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to initialize observability: %w", err)
    }

    // Create enhanced repository with observability
    packRepo, err := NewEnhancedPackageRepository(mongoConn, obsProvider)
    if err != nil {
        return nil, fmt.Errorf("failed to create package repository: %w", err)
    }

    // Initialize health service and register checks
    healthService := health.NewService("fees-service", "v1.0.0", cfg.EnvName, "")
    if err := mongoConn.RegisterHealthCheck(healthService); err != nil {
        return nil, fmt.Errorf("failed to register health checks: %w", err)
    }

    service := &services.UseCase{
        PackageRepo: packRepo,
        MidazClient: midazService,
    }

    return &Service{
        serverAPI: NewServer(cfg, httpApp, logger, telemetry),
        logger:    logger,
    }, nil
}
```

## 🔄 Migration Steps

### Phase 1: Configuration and Error Handling (Low Risk)

1. **Replace Panic with Error Returns**:
   ```go
   // Before:
   if err := commons.SetConfigFromEnvVars(cfg); err != nil {
       panic(err)
   }

   // After:
   if err := commons.SetConfigFromEnvVars(cfg); err != nil {
       return nil, fmt.Errorf("failed to load configuration: %w", err)
   }
   ```

2. **Add Configuration Validation**:
   ```go
   type MongoConfig struct {
       // ... fields with validation tags
   }
   
   func (c *MongoConfig) Validate() error {
       // Validation logic
   }
   ```

### Phase 2: Enhanced Connection Patterns (Medium Risk)

1. **Add Connection Pooling Configuration**
2. **Implement Retry Logic with Jitter**
3. **Add Health Check Registration**

### Phase 3: Advanced Patterns (High Value)

1. **Add Circuit Breaker Pattern**
2. **Integrate Observability and Metrics**
3. **Add Distributed Tracing**

## 📈 Benefits Gained

| Aspect            | Before                      | After                           |
| ----------------- | --------------------------- | ------------------------------- |
| **Reliability**   | Panic on config errors      | Graceful error handling         |
| **Observability** | Basic logging               | Metrics, tracing, health checks |
| **Performance**   | Default connection settings | Optimized connection pooling    |
| **Resilience**    | Direct connection failures  | Circuit breaker + retry logic   |
| **Monitoring**    | No health endpoints         | Health check integration        |
| **Debugging**     | Limited error context       | Rich error context and tracing  |

## 🧪 Testing Improvements

### Before: Limited Testing
```go
// Hard to test without actual MongoDB instance
func TestPackageRepository(t *testing.T) {
    // Requires real MongoDB connection
}
```

### After: Comprehensive Testing
```go
func TestEnhancedRepository_Create_Success(t *testing.T) {
    // Mock MongoDB connection
    mockConn := &mongo.MockConnection{}
    obsProvider := observability.NewNoopProvider()
    
    repo, err := NewEnhancedPackageRepository(mockConn, obsProvider)
    require.NoError(t, err)
    
    // Test with proper error handling
    pack, err := repo.Create(context.Background(), "packages", testPackage, testOrgID)
    
    assert.NoError(t, err)
    assert.NotNil(t, pack)
}

func TestCircuitBreaker_MongoDB_Failures(t *testing.T) {
    config := &MongoConfig{...}
    logger := log.NewMockLogger()
    
    conn, err := NewEnhancedMongoConnection(config, logger)
    require.NoError(t, err)
    
    // Simulate MongoDB failures
    for i := 0; i < 6; i++ {
        err := conn.ConnectWithRetry(context.Background())
        assert.Error(t, err)
    }
    
    // Circuit breaker should be open
    assert.Equal(t, circuitbreaker.StateOpen, conn.circuitBreaker.State())
}
```

## 🎯 Implementation Priority

1. **Critical**: Replace panic calls with proper error handling
2. **High**: Add connection pooling and health checks
3. **Medium**: Implement circuit breaker and retry logic  
4. **Nice-to-have**: Full observability integration

## 📋 Related Examples

- [PostgreSQL Fatal Calls Fix](./postgres-fatal-to-error.md)
- [Fees Plugin Complete Migration](../../components/fees/mongo-migration.md)
- [Health Check Patterns](../observability/health-checks.md)

---

**💡 Quick Win**: Start with replacing the panic call and adding configuration validation - these changes provide immediate stability improvements with minimal risk. 