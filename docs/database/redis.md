# Redis Integration

The Redis package provides connection management, caching utilities, and distributed primitives for working with Redis.

## Table of Contents

- [Overview](#overview)
- [Connection Management](#connection-management)
- [Basic Operations](#basic-operations)
- [Data Structures](#data-structures)
- [Pub/Sub](#pubsub)
- [Distributed Locks](#distributed-locks)
- [Caching Patterns](#caching-patterns)
- [Best Practices](#best-practices)

## Overview

The Redis integration provides:

- Connection pooling and management
- Support for all Redis data types
- Pub/Sub messaging
- Distributed locks
- Pipeline and transaction support
- Cluster support
- Sentinel support

## Connection Management

### Basic Connection

```go
import (
    "github.com/yourusername/commons-go/commons/redis"
)

// Connect using environment variables
client, err := redis.Connect()
if err != nil {
    log.Fatal(err)
}
defer client.Close()

// Test connection
ctx := context.Background()
pong, err := client.Ping(ctx).Result()
if err != nil {
    log.Fatal(err)
}
log.Printf("Connected to Redis: %s", pong)
```

### Connection with Configuration

```go
config := redis.Config{
    Addr:         "localhost:6379",
    Password:     "password",
    DB:           0,
    MaxRetries:   3,
    PoolSize:     10,
    MinIdleConns: 5,
    MaxConnAge:   5 * time.Minute,
}

client, err := redis.ConnectWithConfig(config)
if err != nil {
    log.Fatal(err)
}
```

### Cluster Connection

```go
config := redis.ClusterConfig{
    Addrs: []string{
        "localhost:7000",
        "localhost:7001",
        "localhost:7002",
    },
    Password:     "password",
    MaxRetries:   3,
    PoolSize:     10,
}

clusterClient, err := redis.ConnectCluster(config)
if err != nil {
    log.Fatal(err)
}
```

### Sentinel Connection

```go
config := redis.SentinelConfig{
    MasterName: "mymaster",
    SentinelAddrs: []string{
        "localhost:26379",
        "localhost:26380",
        "localhost:26381",
    },
    Password: "password",
}

sentinelClient, err := redis.ConnectSentinel(config)
if err != nil {
    log.Fatal(err)
}
```

## Basic Operations

### String Operations

```go
// Set a value
err := client.Set(ctx, "user:123:name", "John Doe", 24*time.Hour).Err()
if err != nil {
    return err
}

// Get a value
val, err := client.Get(ctx, "user:123:name").Result()
if err == redis.Nil {
    log.Println("Key does not exist")
} else if err != nil {
    return err
} else {
    log.Printf("User name: %s", val)
}

// Set with expiration
err = client.SetEX(ctx, "session:abc123", sessionData, 30*time.Minute).Err()

// Set only if not exists
success, err := client.SetNX(ctx, "lock:resource", "locked", 5*time.Second).Result()
if success {
    log.Println("Lock acquired")
}

// Increment/Decrement
newVal, err := client.Incr(ctx, "counter:visits").Result()
newVal, err = client.IncrBy(ctx, "counter:views", 5).Result()
newVal, err = client.Decr(ctx, "counter:stock").Result()
```

### Key Operations

```go
// Check if key exists
exists, err := client.Exists(ctx, "user:123").Result()

// Delete keys
deleted, err := client.Del(ctx, "user:123", "session:abc").Result()

// Set expiration
err = client.Expire(ctx, "cache:data", 5*time.Minute).Err()

// Get TTL
ttl, err := client.TTL(ctx, "cache:data").Result()
if ttl == -2 {
    log.Println("Key does not exist")
} else if ttl == -1 {
    log.Println("Key has no expiration")
} else {
    log.Printf("Key expires in: %v", ttl)
}

// Rename key
err = client.Rename(ctx, "old:key", "new:key").Err()

// Pattern matching
keys, err := client.Keys(ctx, "user:*").Result()
```

## Data Structures

### Lists

```go
// Push to list
err := client.LPush(ctx, "queue:tasks", "task1", "task2").Err()
err = client.RPush(ctx, "queue:tasks", "task3").Err()

// Pop from list
val, err := client.LPop(ctx, "queue:tasks").Result()
val, err = client.RPop(ctx, "queue:tasks").Result()

// Blocking pop
val, err = client.BLPop(ctx, 5*time.Second, "queue:tasks").Result()

// Get list length
length, err := client.LLen(ctx, "queue:tasks").Result()

// Get range
values, err := client.LRange(ctx, "queue:tasks", 0, -1).Result()

// Trim list
err = client.LTrim(ctx, "queue:tasks", 0, 99).Err()
```

### Sets

```go
// Add to set
added, err := client.SAdd(ctx, "tags:post:123", "golang", "redis", "tutorial").Result()

// Check membership
isMember, err := client.SIsMember(ctx, "tags:post:123", "golang").Result()

// Get all members
members, err := client.SMembers(ctx, "tags:post:123").Result()

// Set operations
// Union
union, err := client.SUnion(ctx, "tags:post:123", "tags:post:456").Result()

// Intersection
intersection, err := client.SInter(ctx, "tags:post:123", "tags:post:456").Result()

// Difference
diff, err := client.SDiff(ctx, "tags:post:123", "tags:post:456").Result()

// Remove from set
removed, err := client.SRem(ctx, "tags:post:123", "tutorial").Result()

// Random member
randomMember, err := client.SRandMember(ctx, "tags:post:123").Result()
```

### Sorted Sets

```go
// Add to sorted set
err := client.ZAdd(ctx, "leaderboard", &redis.Z{
    Score:  100,
    Member: "player1",
}, &redis.Z{
    Score:  95,
    Member: "player2",
}, &redis.Z{
    Score:  98,
    Member: "player3",
}).Err()

// Get rank
rank, err := client.ZRank(ctx, "leaderboard", "player1").Result()
reverseRank, err := client.ZRevRank(ctx, "leaderboard", "player1").Result()

// Get score
score, err := client.ZScore(ctx, "leaderboard", "player1").Result()

// Get range
// Top 10 players
topPlayers, err := client.ZRevRangeWithScores(ctx, "leaderboard", 0, 9).Result()
for _, z := range topPlayers {
    log.Printf("%s: %.0f points", z.Member, z.Score)
}

// Get by score range
players, err := client.ZRangeByScore(ctx, "leaderboard", &redis.ZRangeBy{
    Min: "90",
    Max: "100",
}).Result()

// Increment score
newScore, err := client.ZIncrBy(ctx, "leaderboard", 5, "player1").Result()

// Remove members
removed, err := client.ZRem(ctx, "leaderboard", "player2").Result()
```

### Hashes

```go
// Set hash fields
err := client.HSet(ctx, "user:123", map[string]interface{}{
    "name":  "John Doe",
    "email": "john@example.com",
    "age":   30,
}).Err()

// Get single field
name, err := client.HGet(ctx, "user:123", "name").Result()

// Get multiple fields
values, err := client.HMGet(ctx, "user:123", "name", "email").Result()

// Get all fields
allFields, err := client.HGetAll(ctx, "user:123").Result()
for field, value := range allFields {
    log.Printf("%s: %s", field, value)
}

// Check field existence
exists, err := client.HExists(ctx, "user:123", "phone").Result()

// Increment field
newAge, err := client.HIncrBy(ctx, "user:123", "age", 1).Result()

// Delete fields
deleted, err := client.HDel(ctx, "user:123", "temp_field").Result()

// Get field count
count, err := client.HLen(ctx, "user:123").Result()
```

## Pub/Sub

### Basic Pub/Sub

```go
// Subscribe to channels
pubsub := client.Subscribe(ctx, "notifications", "updates")
defer pubsub.Close()

// Get channel
ch := pubsub.Channel()

// Listen for messages
go func() {
    for msg := range ch {
        log.Printf("Channel: %s, Message: %s", msg.Channel, msg.Payload)
        
        // Process message based on channel
        switch msg.Channel {
        case "notifications":
            handleNotification(msg.Payload)
        case "updates":
            handleUpdate(msg.Payload)
        }
    }
}()

// Publish messages
subscribers, err := client.Publish(ctx, "notifications", "New message").Result()
log.Printf("Message sent to %d subscribers", subscribers)
```

### Pattern Subscribe

```go
// Subscribe to pattern
pubsub := client.PSubscribe(ctx, "user:*", "order:*")
defer pubsub.Close()

ch := pubsub.Channel()

go func() {
    for msg := range ch {
        log.Printf("Pattern: %s, Channel: %s, Message: %s", 
            msg.Pattern, msg.Channel, msg.Payload)
    }
}()
```

### Pub/Sub with Context

```go
func SubscribeWithContext(ctx context.Context, client *redis.Client, channel string) {
    pubsub := client.Subscribe(ctx, channel)
    defer pubsub.Close()
    
    ch := pubsub.Channel()
    
    for {
        select {
        case msg := <-ch:
            if msg == nil {
                return
            }
            log.Printf("Received: %s", msg.Payload)
            
        case <-ctx.Done():
            log.Println("Subscription cancelled")
            return
        }
    }
}
```

## Distributed Locks

### Simple Lock

```go
// Acquire lock
lockKey := "lock:resource:123"
lockValue := uuid.New().String()
locked, err := client.SetNX(ctx, lockKey, lockValue, 30*time.Second).Result()
if !locked {
    return fmt.Errorf("resource is locked")
}

// Ensure we release the lock
defer func() {
    // Only delete if we own the lock
    script := `
        if redis.call("get", KEYS[1]) == ARGV[1] then
            return redis.call("del", KEYS[1])
        else
            return 0
        end
    `
    client.Eval(ctx, script, []string{lockKey}, lockValue)
}()

// Do work with locked resource
```

### Lock with Retry

```go
type DistributedLock struct {
    client   *redis.Client
    key      string
    value    string
    duration time.Duration
}

func NewDistributedLock(client *redis.Client, key string, duration time.Duration) *DistributedLock {
    return &DistributedLock{
        client:   client,
        key:      fmt.Sprintf("lock:%s", key),
        value:    uuid.New().String(),
        duration: duration,
    }
}

func (l *DistributedLock) Acquire(ctx context.Context) error {
    deadline := time.Now().Add(5 * time.Second) // Max wait time
    
    for time.Now().Before(deadline) {
        locked, err := l.client.SetNX(ctx, l.key, l.value, l.duration).Result()
        if err != nil {
            return err
        }
        
        if locked {
            return nil
        }
        
        // Wait before retry
        select {
        case <-time.After(50 * time.Millisecond):
        case <-ctx.Done():
            return ctx.Err()
        }
    }
    
    return fmt.Errorf("failed to acquire lock")
}

func (l *DistributedLock) Release(ctx context.Context) error {
    script := `
        if redis.call("get", KEYS[1]) == ARGV[1] then
            return redis.call("del", KEYS[1])
        else
            return 0
        end
    `
    
    result, err := l.client.Eval(ctx, script, []string{l.key}, l.value).Result()
    if err != nil {
        return err
    }
    
    if result.(int64) == 0 {
        return fmt.Errorf("lock not owned")
    }
    
    return nil
}

func (l *DistributedLock) Extend(ctx context.Context, duration time.Duration) error {
    script := `
        if redis.call("get", KEYS[1]) == ARGV[1] then
            return redis.call("expire", KEYS[1], ARGV[2])
        else
            return 0
        end
    `
    
    result, err := l.client.Eval(ctx, script, []string{l.key}, l.value, int(duration.Seconds())).Result()
    if err != nil {
        return err
    }
    
    if result.(int64) == 0 {
        return fmt.Errorf("lock not owned")
    }
    
    return nil
}
```

## Caching Patterns

### Cache-Aside Pattern

```go
func GetUser(ctx context.Context, userID string) (*User, error) {
    cacheKey := fmt.Sprintf("user:%s", userID)
    
    // Try cache first
    cached, err := client.Get(ctx, cacheKey).Result()
    if err == nil {
        var user User
        if err := json.Unmarshal([]byte(cached), &user); err == nil {
            return &user, nil
        }
    }
    
    // Load from database
    user, err := db.GetUser(userID)
    if err != nil {
        return nil, err
    }
    
    // Cache for next time
    data, _ := json.Marshal(user)
    client.Set(ctx, cacheKey, data, 5*time.Minute)
    
    return user, nil
}
```

### Write-Through Cache

```go
func UpdateUser(ctx context.Context, user *User) error {
    // Update database
    if err := db.UpdateUser(user); err != nil {
        return err
    }
    
    // Update cache
    cacheKey := fmt.Sprintf("user:%s", user.ID)
    data, _ := json.Marshal(user)
    client.Set(ctx, cacheKey, data, 5*time.Minute)
    
    // Invalidate related caches
    client.Del(ctx, 
        fmt.Sprintf("user:email:%s", user.Email),
        fmt.Sprintf("user:list:page:*"),
    )
    
    return nil
}
```

### Cache Warming

```go
func WarmCache(ctx context.Context) error {
    // Get frequently accessed data
    users, err := db.GetTopUsers(100)
    if err != nil {
        return err
    }
    
    // Use pipeline for efficiency
    pipe := client.Pipeline()
    
    for _, user := range users {
        data, _ := json.Marshal(user)
        pipe.Set(ctx, fmt.Sprintf("user:%s", user.ID), data, 1*time.Hour)
    }
    
    _, err = pipe.Exec(ctx)
    return err
}
```

## Best Practices

### 1. Use Pipelines for Bulk Operations

```go
// Inefficient: Multiple round trips
for i := 0; i < 1000; i++ {
    client.Set(ctx, fmt.Sprintf("key:%d", i), i, 0)
}

// Efficient: Single round trip
pipe := client.Pipeline()
for i := 0; i < 1000; i++ {
    pipe.Set(ctx, fmt.Sprintf("key:%d", i), i, 0)
}
cmds, err := pipe.Exec(ctx)
if err != nil {
    return err
}
```

### 2. Use Transactions

```go
// Watch keys for changes
err = client.Watch(ctx, func(tx *redis.Tx) error {
    // Get current balance
    balance, err := tx.Get(ctx, "balance:123").Int()
    if err != nil {
        return err
    }
    
    if balance < amount {
        return fmt.Errorf("insufficient balance")
    }
    
    // Execute transaction
    _, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
        pipe.DecrBy(ctx, "balance:123", amount)
        pipe.IncrBy(ctx, "balance:456", amount)
        pipe.LPush(ctx, "transactions", fmt.Sprintf("%d:%d:%d", 123, 456, amount))
        return nil
    })
    
    return err
}, "balance:123", "balance:456")
```

### 3. Connection Pool Management

```go
// Monitor pool stats
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        stats := client.PoolStats()
        log.Printf("Redis Pool - Hits: %d, Misses: %d, Timeouts: %d, Total: %d, Idle: %d",
            stats.Hits, stats.Misses, stats.Timeouts, 
            stats.TotalConns, stats.IdleConns)
    }
}()
```

### 4. Error Handling

```go
func HandleRedisError(err error) error {
    if err == nil {
        return nil
    }
    
    // Check for nil response
    if err == redis.Nil {
        return fmt.Errorf("key not found")
    }
    
    // Check for timeout
    if err.Error() == "redis: connection pool timeout" {
        return fmt.Errorf("redis connection timeout")
    }
    
    // Check for connection errors
    if strings.Contains(err.Error(), "connection refused") {
        return fmt.Errorf("redis connection failed")
    }
    
    return err
}
```

### 5. Key Naming Conventions

```go
// Use consistent key naming
type KeyBuilder struct {
    prefix string
}

func NewKeyBuilder(prefix string) *KeyBuilder {
    return &KeyBuilder{prefix: prefix}
}

func (k *KeyBuilder) User(userID string) string {
    return fmt.Sprintf("%s:user:%s", k.prefix, userID)
}

func (k *KeyBuilder) Session(sessionID string) string {
    return fmt.Sprintf("%s:session:%s", k.prefix, sessionID)
}

func (k *KeyBuilder) Cache(resource, id string) string {
    return fmt.Sprintf("%s:cache:%s:%s", k.prefix, resource, id)
}

// Usage
keys := NewKeyBuilder("myapp")
userKey := keys.User("123")
sessionKey := keys.Session("abc")
```

### 6. Monitoring and Metrics

```go
type MetricsClient struct {
    *redis.Client
    hits     atomic.Int64
    misses   atomic.Int64
    errors   atomic.Int64
}

func (m *MetricsClient) Get(ctx context.Context, key string) (string, error) {
    val, err := m.Client.Get(ctx, key).Result()
    
    if err == redis.Nil {
        m.misses.Add(1)
        return "", err
    } else if err != nil {
        m.errors.Add(1)
        return "", err
    } else {
        m.hits.Add(1)
        return val, nil
    }
}

func (m *MetricsClient) GetMetrics() (hits, misses, errors int64) {
    return m.hits.Load(), m.misses.Load(), m.errors.Load()
}
```

### 7. Testing with Redis

```go
// Use miniredis for unit tests
func TestRedisOperations(t *testing.T) {
    // Start mini redis server
    mr, err := miniredis.Run()
    if err != nil {
        t.Fatal(err)
    }
    defer mr.Close()
    
    // Create client
    client := redis.NewClient(&redis.Options{
        Addr: mr.Addr(),
    })
    
    // Test operations
    ctx := context.Background()
    
    // Test SET/GET
    err = client.Set(ctx, "key", "value", 0).Err()
    assert.NoError(t, err)
    
    val, err := client.Get(ctx, "key").Result()
    assert.NoError(t, err)
    assert.Equal(t, "value", val)
    
    // Test expiration
    mr.FastForward(10 * time.Second)
    
    // Verify key expired
    _, err = client.Get(ctx, "key").Result()
    assert.Equal(t, redis.Nil, err)
}
```

## Examples

### Complete Cache Service Example

```go
package cache

import (
    "context"
    "encoding/json"
    "fmt"
    "time"
    
    "github.com/go-redis/redis/v8"
    "github.com/google/uuid"
)

type CacheService struct {
    client     *redis.Client
    defaultTTL time.Duration
    prefix     string
}

func NewCacheService(client *redis.Client, prefix string) *CacheService {
    return &CacheService{
        client:     client,
        defaultTTL: 5 * time.Minute,
        prefix:     prefix,
    }
}

func (s *CacheService) key(parts ...string) string {
    key := s.prefix
    for _, part := range parts {
        key += ":" + part
    }
    return key
}

// Generic cache methods
func (s *CacheService) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
    if ttl == 0 {
        ttl = s.defaultTTL
    }
    
    data, err := json.Marshal(value)
    if err != nil {
        return err
    }
    
    return s.client.Set(ctx, s.key(key), data, ttl).Err()
}

func (s *CacheService) Get(ctx context.Context, key string, dest interface{}) error {
    data, err := s.client.Get(ctx, s.key(key)).Bytes()
    if err != nil {
        return err
    }
    
    return json.Unmarshal(data, dest)
}

func (s *CacheService) Delete(ctx context.Context, keys ...string) error {
    fullKeys := make([]string, len(keys))
    for i, key := range keys {
        fullKeys[i] = s.key(key)
    }
    
    return s.client.Del(ctx, fullKeys...).Err()
}

func (s *CacheService) Exists(ctx context.Context, key string) (bool, error) {
    n, err := s.client.Exists(ctx, s.key(key)).Result()
    return n > 0, err
}

// Pattern-based deletion
func (s *CacheService) DeletePattern(ctx context.Context, pattern string) error {
    var cursor uint64
    var keys []string
    
    for {
        var k []string
        var err error
        k, cursor, err = s.client.Scan(ctx, cursor, s.key(pattern), 100).Result()
        if err != nil {
            return err
        }
        
        keys = append(keys, k...)
        
        if cursor == 0 {
            break
        }
    }
    
    if len(keys) > 0 {
        return s.client.Del(ctx, keys...).Err()
    }
    
    return nil
}

// Rate limiting
func (s *CacheService) RateLimit(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
    rateKey := s.key("rate", key)
    
    pipe := s.client.Pipeline()
    incr := pipe.Incr(ctx, rateKey)
    pipe.Expire(ctx, rateKey, window)
    
    _, err := pipe.Exec(ctx)
    if err != nil {
        return false, err
    }
    
    return incr.Val() <= int64(limit), nil
}

// Distributed counter
func (s *CacheService) IncrementCounter(ctx context.Context, name string) (int64, error) {
    return s.client.Incr(ctx, s.key("counter", name)).Result()
}

func (s *CacheService) GetCounter(ctx context.Context, name string) (int64, error) {
    val, err := s.client.Get(ctx, s.key("counter", name)).Int64()
    if err == redis.Nil {
        return 0, nil
    }
    return val, err
}

// Session management
type Session struct {
    ID        string                 `json:"id"`
    UserID    string                 `json:"user_id"`
    Data      map[string]interface{} `json:"data"`
    CreatedAt time.Time              `json:"created_at"`
    ExpiresAt time.Time              `json:"expires_at"`
}

func (s *CacheService) CreateSession(ctx context.Context, userID string, ttl time.Duration) (*Session, error) {
    session := &Session{
        ID:        uuid.New().String(),
        UserID:    userID,
        Data:      make(map[string]interface{}),
        CreatedAt: time.Now(),
        ExpiresAt: time.Now().Add(ttl),
    }
    
    err := s.Set(ctx, fmt.Sprintf("session:%s", session.ID), session, ttl)
    if err != nil {
        return nil, err
    }
    
    // Add to user's session set
    s.client.SAdd(ctx, s.key("user:sessions", userID), session.ID)
    s.client.Expire(ctx, s.key("user:sessions", userID), ttl)
    
    return session, nil
}

func (s *CacheService) GetSession(ctx context.Context, sessionID string) (*Session, error) {
    var session Session
    err := s.Get(ctx, fmt.Sprintf("session:%s", sessionID), &session)
    if err != nil {
        return nil, err
    }
    
    if time.Now().After(session.ExpiresAt) {
        s.Delete(ctx, fmt.Sprintf("session:%s", sessionID))
        return nil, fmt.Errorf("session expired")
    }
    
    return &session, nil
}

func (s *CacheService) UpdateSession(ctx context.Context, session *Session) error {
    ttl := time.Until(session.ExpiresAt)
    if ttl <= 0 {
        return fmt.Errorf("session expired")
    }
    
    return s.Set(ctx, fmt.Sprintf("session:%s", session.ID), session, ttl)
}

func (s *CacheService) DeleteSession(ctx context.Context, sessionID string) error {
    session, err := s.GetSession(ctx, sessionID)
    if err != nil {
        return err
    }
    
    // Remove from user's session set
    s.client.SRem(ctx, s.key("user:sessions", session.UserID), sessionID)
    
    return s.Delete(ctx, fmt.Sprintf("session:%s", sessionID))
}

func (s *CacheService) GetUserSessions(ctx context.Context, userID string) ([]string, error) {
    return s.client.SMembers(ctx, s.key("user:sessions", userID)).Result()
}

// Leaderboard operations
func (s *CacheService) UpdateScore(ctx context.Context, leaderboard, member string, score float64) error {
    return s.client.ZAdd(ctx, s.key("leaderboard", leaderboard), &redis.Z{
        Score:  score,
        Member: member,
    }).Err()
}

func (s *CacheService) GetTopScores(ctx context.Context, leaderboard string, limit int) ([]redis.Z, error) {
    return s.client.ZRevRangeWithScores(ctx, s.key("leaderboard", leaderboard), 0, int64(limit-1)).Result()
}

func (s *CacheService) GetRank(ctx context.Context, leaderboard, member string) (int64, error) {
    rank, err := s.client.ZRevRank(ctx, s.key("leaderboard", leaderboard), member).Result()
    if err != nil {
        return 0, err
    }
    return rank + 1, nil // Convert 0-based to 1-based
}

// Queue operations
func (s *CacheService) EnqueueTask(ctx context.Context, queue string, task interface{}) error {
    data, err := json.Marshal(task)
    if err != nil {
        return err
    }
    
    return s.client.LPush(ctx, s.key("queue", queue), data).Err()
}

func (s *CacheService) DequeueTask(ctx context.Context, queue string, timeout time.Duration, dest interface{}) error {
    result, err := s.client.BRPop(ctx, timeout, s.key("queue", queue)).Result()
    if err != nil {
        return err
    }
    
    if len(result) < 2 {
        return fmt.Errorf("invalid result")
    }
    
    return json.Unmarshal([]byte(result[1]), dest)
}

func (s *CacheService) GetQueueLength(ctx context.Context, queue string) (int64, error) {
    return s.client.LLen(ctx, s.key("queue", queue)).Result()
}

// Health check
func (s *CacheService) HealthCheck(ctx context.Context) error {
    return s.client.Ping(ctx).Err()
}

// Stats
func (s *CacheService) GetStats(ctx context.Context) (map[string]interface{}, error) {
    info, err := s.client.Info(ctx).Result()
    if err != nil {
        return nil, err
    }
    
    stats := map[string]interface{}{
        "info":       info,
        "pool_stats": s.client.PoolStats(),
    }
    
    return stats, nil
}
```