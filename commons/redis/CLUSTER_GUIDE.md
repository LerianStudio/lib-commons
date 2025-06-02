# Redis Smart Detection and Cluster Configuration Guide

This guide provides comprehensive documentation for Redis smart detection features, cluster topology management, and GCP IAM authentication patterns in lib-commons.

## Table of Contents
- [Overview](#overview)
- [Smart Detection Features](#smart-detection-features)
- [Cluster Topology Detection](#cluster-topology-detection)
- [GCP IAM Authentication](#gcp-iam-authentication)
- [Configuration Patterns](#configuration-patterns)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)

## Overview

The Redis smart detection system automatically identifies and adapts to different Redis deployment patterns:

- **Single Instance**: Traditional Redis server setup
- **Cluster Mode**: Redis Cluster with automatic node discovery
- **GCP Memorystore**: Google Cloud Redis with IAM authentication
- **Hybrid Configurations**: Mixed environments with failover

### Key Benefits

- **Zero Configuration**: Automatic detection eliminates manual setup
- **Backward Compatibility**: Existing code works without changes
- **Performance Optimization**: Connection pooling and cluster-aware routing
- **Enterprise Security**: GCP IAM integration with automatic token refresh

## Smart Detection Features

### Automatic Address Parsing

The smart detection system analyzes connection strings to determine deployment type:

```go
// Single instance - detected automatically
conn := &redis.RedisConnection{
    Addr: "localhost:6379",
}

// Cluster configuration - comma-separated addresses trigger cluster mode
conn := &redis.RedisConnection{
    Addr: "redis-node-1:6379,redis-node-2:6379,redis-node-3:6379",
}

// GCP Memorystore with IAM - detected by GCP_VALKEY_AUTH environment variable
conn := &redis.RedisConnection{
    Addr: "redis-cluster.gcp.internal:6379",
}
```

### Detection Algorithm

1. **Environment Analysis**: Check `GCP_VALKEY_AUTH` for IAM authentication
2. **Address Parsing**: Count comma-separated addresses
3. **Topology Discovery**: Query Redis for cluster information
4. **Optimization Selection**: Choose optimal client configuration

## Cluster Topology Detection

### Cluster Node Discovery

The system automatically discovers and maps cluster topology:

```go
package main

import (
    "context"
    "fmt"
    "log"
    
    "github.com/LerianStudio/lib-commons/commons/redis"
)

func clusterTopologyExample() {
    // Provide any cluster node addresses - system will discover all nodes
    conn := &redis.RedisConnection{
        Addr: "redis-primary:6379,redis-replica-1:6379",
        // Additional cluster nodes will be auto-discovered
    }
    
    ctx := context.Background()
    if err := conn.Connect(ctx); err != nil {
        log.Fatalf("Cluster connection failed: %v", err)
    }
    
    // The connection now has full cluster topology awareness
    fmt.Println("✅ Connected to Redis cluster with auto-discovery")
}
```

### Slot Mapping and Routing

Redis Cluster uses hash slots to distribute data across nodes:

```go
func clusterSlotAwareness() {
    conn := &redis.RedisConnection{
        Addr: "redis-cluster-01:6379,redis-cluster-02:6379,redis-cluster-03:6379",
    }
    
    ctx := context.Background()
    conn.Connect(ctx)
    
    // Keys are automatically routed to correct cluster nodes
    keys := []string{
        "user:1001",    // May go to node 1
        "user:1002",    // May go to node 2  
        "user:1003",    // May go to node 3
        "session:abc",  // Routed based on hash slot
        "cache:def",    // Distributed automatically
    }
    
    // All operations are cluster-aware
    for _, key := range keys {
        value := fmt.Sprintf("data-for-%s", key)
        err := conn.Client.Set(ctx, key, value, time.Hour).Err()
        if err != nil {
            log.Printf("Failed to set %s: %v", key, err)
            continue
        }
        
        // Retrieval is also cluster-aware
        retrieved, err := conn.Client.Get(ctx, key).Result()
        if err != nil {
            log.Printf("Failed to get %s: %v", key, err)
            continue
        }
        
        fmt.Printf("✅ %s: %s\n", key, retrieved)
    }
}
```

### Cluster Health Monitoring

Monitor cluster health and node status:

```go
func clusterHealthMonitoring(conn *redis.RedisConnection) {
    ctx := context.Background()
    
    // The cluster client provides health information
    if clusterClient, ok := conn.Client.(*redis.ClusterClient); ok {
        // Get cluster information
        clusterInfo, err := clusterClient.ClusterInfo(ctx).Result()
        if err != nil {
            log.Printf("Failed to get cluster info: %v", err)
            return
        }
        
        fmt.Printf("Cluster Info: %s\n", clusterInfo)
        
        // Get cluster nodes
        clusterNodes, err := clusterClient.ClusterNodes(ctx).Result()
        if err != nil {
            log.Printf("Failed to get cluster nodes: %v", err)
            return
        }
        
        fmt.Printf("Cluster Nodes: %s\n", clusterNodes)
        
        // Check cluster slots
        clusterSlots, err := clusterClient.ClusterSlots(ctx).Result()
        if err != nil {
            log.Printf("Failed to get cluster slots: %v", err)
            return
        }
        
        fmt.Printf("Found %d slot ranges\n", len(clusterSlots))
    }
}
```

## GCP IAM Authentication

### Enable IAM Authentication

Set the environment variable to enable GCP IAM authentication:

```bash
export GCP_VALKEY_AUTH=true
export REDIS_URL=redis-cluster.gcp.internal:6379

# Service account authentication (choose one):

# Option 1: Service account key file
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json

# Option 2: GCP metadata service (when running on GCP)
# Credentials are automatically detected

# Option 3: gcloud default credentials
# Use: gcloud auth application-default login
```

### IAM Authentication Flow

```go
func gcpIAMAuthenticationExample() {
    // When GCP_VALKEY_AUTH=true, authentication is automatic
    conn := &redis.RedisConnection{
        Addr: "redis-cluster.gcp.internal:6379",
        // Authentication handled automatically via IAM
    }
    
    ctx := context.Background()
    
    // Connection automatically:
    // 1. Detects GCP IAM requirement
    // 2. Obtains access tokens
    // 3. Refreshes tokens before expiry
    // 4. Handles authentication failures gracefully
    
    if err := conn.Connect(ctx); err != nil {
        log.Fatalf("GCP IAM authentication failed: %v", err)
    }
    
    fmt.Println("✅ Connected with GCP IAM authentication")
    
    // All Redis operations work normally
    err := conn.Client.Set(ctx, "gcp:test", "authenticated", time.Minute).Err()
    if err != nil {
        log.Printf("Operation failed: %v", err)
        return
    }
    
    value, err := conn.Client.Get(ctx, "gcp:test").Result()
    if err != nil {
        log.Printf("Retrieval failed: %v", err)
        return
    }
    
    fmt.Printf("✅ Operation successful: %s\n", value)
}
```

### Token Refresh and Error Handling

```go
func iamTokenManagement() {
    conn := &redis.RedisConnection{
        Addr: "redis-cluster.gcp.internal:6379",
    }
    
    ctx := context.Background()
    conn.Connect(ctx)
    
    // The system automatically handles:
    // - Token refresh before expiry
    // - Authentication failures
    // - Network interruptions
    // - IAM permission changes
    
    // Long-running operations work seamlessly
    for i := 0; i < 1000; i++ {
        key := fmt.Sprintf("long-running:%d", i)
        value := fmt.Sprintf("data-%d", i)
        
        err := conn.Client.Set(ctx, key, value, time.Hour).Err()
        if err != nil {
            // Automatic retry with fresh tokens
            log.Printf("Operation %d failed, retrying: %v", i, err)
            time.Sleep(100 * time.Millisecond)
            continue
        }
        
        if i%100 == 0 {
            fmt.Printf("✅ Processed %d operations with automatic token refresh\n", i)
        }
        
        time.Sleep(10 * time.Millisecond)
    }
}
```

## Configuration Patterns

### Environment-Based Configuration

```go
// Production configuration pattern
func productionRedisConfig() *redis.RedisConnection {
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        log.Fatal("REDIS_URL environment variable required")
    }
    
    return &redis.RedisConnection{
        Addr: redisURL,
        // Smart detection handles the rest automatically
    }
}

// Development configuration with fallbacks
func developmentRedisConfig() *redis.RedisConnection {
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        redisURL = "localhost:6379" // Local development fallback
    }
    
    return &redis.RedisConnection{
        Addr: redisURL,
    }
}
```

### Multi-Environment Support

```go
func multiEnvironmentConfig() *redis.RedisConnection {
    environment := os.Getenv("ENVIRONMENT")
    
    var redisAddr string
    switch environment {
    case "production":
        // GCP Memorystore cluster with IAM
        redisAddr = os.Getenv("REDIS_CLUSTER_URL")
        os.Setenv("GCP_VALKEY_AUTH", "true")
        
    case "staging":
        // Redis cluster without IAM
        redisAddr = "redis-staging-1:6379,redis-staging-2:6379"
        
    case "development":
        // Local single instance
        redisAddr = "localhost:6379"
        
    default:
        log.Fatal("ENVIRONMENT must be set to production, staging, or development")
    }
    
    return &redis.RedisConnection{
        Addr: redisAddr,
    }
}
```

### Advanced Configuration

```go
type RedisConfig struct {
    Addresses          []string
    EnableIAMAuth      bool
    MaxRetries         int
    DialTimeout        time.Duration
    ReadTimeout        time.Duration
    WriteTimeout       time.Duration
    PoolSize           int
    MinIdleConnections int
}

func advancedRedisSetup(config RedisConfig) *redis.RedisConnection {
    // Set IAM authentication if required
    if config.EnableIAMAuth {
        os.Setenv("GCP_VALKEY_AUTH", "true")
    }
    
    // Join addresses for smart detection
    addr := strings.Join(config.Addresses, ",")
    
    conn := &redis.RedisConnection{
        Addr: addr,
        // Additional configuration options would be added here
        // in future versions of the library
    }
    
    return conn
}
```

## Troubleshooting

### Common Issues and Solutions

#### 1. Cluster Connection Failures

```go
func troubleshootClusterConnection() {
    conn := &redis.RedisConnection{
        Addr: "redis-node-1:6379,redis-node-2:6379,redis-node-3:6379",
    }
    
    ctx := context.Background()
    err := conn.Connect(ctx)
    
    if err != nil {
        fmt.Printf("Connection failed: %v\n", err)
        
        // Check individual nodes
        addresses := []string{"redis-node-1:6379", "redis-node-2:6379", "redis-node-3:6379"}
        for _, addr := range addresses {
            nodeConn := &redis.RedisConnection{Addr: addr}
            if nodeErr := nodeConn.Connect(ctx); nodeErr != nil {
                fmt.Printf("❌ Node %s unreachable: %v\n", addr, nodeErr)
            } else {
                fmt.Printf("✅ Node %s accessible\n", addr)
            }
        }
    }
}
```

#### 2. GCP IAM Authentication Issues

```go
func troubleshootGCPAuth() {
    // Check environment variables
    if os.Getenv("GCP_VALKEY_AUTH") != "true" {
        fmt.Println("❌ GCP_VALKEY_AUTH not set to 'true'")
        return
    }
    
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        fmt.Println("❌ REDIS_URL environment variable not set")
        return
    }
    
    // Check service account configuration
    if creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); creds != "" {
        if _, err := os.Stat(creds); os.IsNotExist(err) {
            fmt.Printf("❌ Service account file not found: %s\n", creds)
            return
        }
        fmt.Printf("✅ Service account file found: %s\n", creds)
    } else {
        fmt.Println("ℹ️  Using default credentials (metadata service or gcloud)")
    }
    
    // Test connection
    conn := &redis.RedisConnection{Addr: redisURL}
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    if err := conn.Connect(ctx); err != nil {
        fmt.Printf("❌ GCP authentication failed: %v\n", err)
        
        // Common solutions:
        fmt.Println("\n🔧 Troubleshooting steps:")
        fmt.Println("1. Verify service account has Redis IAM permissions")
        fmt.Println("2. Check Memorystore instance allows IAM authentication")
        fmt.Println("3. Verify network connectivity and firewall rules")
        fmt.Println("4. Ensure correct Redis instance endpoint")
    } else {
        fmt.Println("✅ GCP IAM authentication successful")
    }
}
```

#### 3. Performance Optimization

```go
func optimizeClusterPerformance() {
    conn := &redis.RedisConnection{
        Addr: "redis-cluster-endpoint:6379",
    }
    
    ctx := context.Background()
    conn.Connect(ctx)
    
    // Use pipeline for batch operations
    pipe := conn.Client.Pipeline()
    
    // Batch multiple operations
    for i := 0; i < 100; i++ {
        key := fmt.Sprintf("batch:key:%d", i)
        value := fmt.Sprintf("value-%d", i)
        pipe.Set(ctx, key, value, time.Hour)
    }
    
    // Execute all operations at once
    start := time.Now()
    _, err := pipe.Exec(ctx)
    duration := time.Since(start)
    
    if err != nil {
        log.Printf("Batch operation failed: %v", err)
        return
    }
    
    fmt.Printf("✅ Batch operation completed in %v\n", duration)
}
```

## Best Practices

### 1. Connection Management

```go
// ✅ Good: Reuse connections
var globalRedisConn *redis.RedisConnection

func initRedis() {
    globalRedisConn = &redis.RedisConnection{
        Addr: os.Getenv("REDIS_URL"),
    }
    
    ctx := context.Background()
    if err := globalRedisConn.Connect(ctx); err != nil {
        log.Fatalf("Redis connection failed: %v", err)
    }
}

func useRedis(ctx context.Context) {
    // Use the global connection
    err := globalRedisConn.Client.Set(ctx, "key", "value", time.Hour).Err()
    if err != nil {
        log.Printf("Redis operation failed: %v", err)
    }
}

// ❌ Bad: Creating new connections for each operation
func badRedisUsage(ctx context.Context) {
    conn := &redis.RedisConnection{Addr: "localhost:6379"}
    conn.Connect(ctx) // Don't do this repeatedly
    conn.Client.Set(ctx, "key", "value", time.Hour)
}
```

### 2. Error Handling

```go
func robustRedisOperations(conn *redis.RedisConnection) {
    ctx := context.Background()
    
    // Always handle Redis errors appropriately
    err := conn.Client.Set(ctx, "important:key", "critical:value", time.Hour).Err()
    if err != nil {
        // Log error with context
        log.Printf("Failed to set critical key: %v", err)
        
        // Implement fallback strategy
        // Could be: retry, use cache, return error to client, etc.
        return
    }
    
    // Check if key exists before operations that require it
    exists, err := conn.Client.Exists(ctx, "important:key").Result()
    if err != nil {
        log.Printf("Failed to check key existence: %v", err)
        return
    }
    
    if exists == 0 {
        log.Println("Key does not exist, handling appropriately")
        return
    }
    
    // Proceed with operation
}
```

### 3. Monitoring and Observability

```go
func monitoredRedisOperations(conn *redis.RedisConnection) {
    ctx := context.Background()
    
    // Time operations for monitoring
    start := time.Now()
    defer func() {
        duration := time.Since(start)
        // Log or send metrics
        log.Printf("Redis operation completed in %v", duration)
    }()
    
    // Use context with timeout
    timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    
    err := conn.Client.Set(timeoutCtx, "monitored:key", "value", time.Hour).Err()
    if err != nil {
        if err == context.DeadlineExceeded {
            log.Println("Redis operation timed out")
        } else {
            log.Printf("Redis operation failed: %v", err)
        }
        return
    }
    
    log.Println("Redis operation successful")
}
```

### 4. Security Considerations

```go
func secureRedisUsage() {
    // ✅ Always use environment variables for sensitive configuration
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        log.Fatal("REDIS_URL must be set")
    }
    
    // ✅ Enable IAM authentication for GCP environments
    if os.Getenv("ENVIRONMENT") == "production" {
        os.Setenv("GCP_VALKEY_AUTH", "true")
    }
    
    conn := &redis.RedisConnection{Addr: redisURL}
    
    // ✅ Use appropriate timeouts
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    if err := conn.Connect(ctx); err != nil {
        log.Fatalf("Secure Redis connection failed: %v", err)
    }
    
    // ❌ Never log sensitive data
    // log.Printf("Connected to Redis at %s", redisURL) // Don't do this
    
    // ✅ Log connection success without exposing details
    log.Println("✅ Secure Redis connection established")
}
```

This guide covers all aspects of Redis smart detection and cluster management. The system is designed to work automatically while providing flexibility for advanced configurations and troubleshooting.