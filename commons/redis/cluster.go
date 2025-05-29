// Package redis provides Redis connection management and operations utilities.
// This file contains cluster-specific implementations for Redis cluster mode support.
package redis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
)

// ConnectionType represents the type of Redis connection
type ConnectionType int

const (
	// SingleInstance represents a single Redis instance connection
	SingleInstance ConnectionType = iota
	// Cluster represents a Redis cluster connection
	Cluster
	// Sentinel represents a Redis Sentinel connection
	Sentinel
)

// String returns the string representation of ConnectionType
func (ct ConnectionType) String() string {
	switch ct {
	case SingleInstance:
		return "SingleInstance"
	case Cluster:
		return "Cluster"
	case Sentinel:
		return "Sentinel"
	default:
		return "Unknown"
	}
}

// RedisClient interface abstracts both single and cluster clients
type RedisClient interface {
	Ping(ctx context.Context) *redis.StatusCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Close() error
}

// ClusterConfig represents Redis cluster configuration
type ClusterConfig struct {
	Addrs          []string      `json:"addrs"`
	Password       string        `json:"password,omitempty"`
	Username       string        `json:"username,omitempty"`
	MaxRetries     int           `json:"max_retries,omitempty"`
	PoolSize       int           `json:"pool_size,omitempty"`
	MinIdleConns   int           `json:"min_idle_conns,omitempty"`
	MaxConnAge     time.Duration `json:"max_conn_age,omitempty"`
	PoolTimeout    time.Duration `json:"pool_timeout,omitempty"`
	IdleTimeout    time.Duration `json:"idle_timeout,omitempty"`
	ReadTimeout    time.Duration `json:"read_timeout,omitempty"`
	WriteTimeout   time.Duration `json:"write_timeout,omitempty"`
	RouteByLatency bool          `json:"route_by_latency,omitempty"`
	RouteRandomly  bool          `json:"route_randomly,omitempty"`
}

// Validate validates the cluster configuration
func (cc *ClusterConfig) Validate() error {
	if len(cc.Addrs) == 0 {
		return fmt.Errorf("cluster addresses cannot be empty")
	}

	for _, addr := range cc.Addrs {
		if !isValidAddress(addr) {
			return fmt.Errorf("invalid address format: %s", addr)
		}
	}

	return nil
}

// ToRedisOptions converts ClusterConfig to redis.ClusterOptions
func (cc *ClusterConfig) ToRedisOptions() *redis.ClusterOptions {
	return &redis.ClusterOptions{
		Addrs:           cc.Addrs,
		Password:        cc.Password,
		Username:        cc.Username,
		MaxRetries:      cc.MaxRetries,
		PoolSize:        cc.PoolSize,
		MinIdleConns:    cc.MinIdleConns,
		ConnMaxLifetime: cc.MaxConnAge,
		PoolTimeout:     cc.PoolTimeout,
		ConnMaxIdleTime: cc.IdleTimeout,
		ReadTimeout:     cc.ReadTimeout,
		WriteTimeout:    cc.WriteTimeout,
		RouteByLatency:  cc.RouteByLatency,
		RouteRandomly:   cc.RouteRandomly,
	}
}

// RedisClusterConnection manages Redis cluster connections
type RedisClusterConnection struct {
	ClusterConfig ClusterConfig
	ClusterClient *redis.ClusterClient
	Connected     bool
	Logger        log.Logger
}

// ConnectCluster establishes cluster connection
func (rcc *RedisClusterConnection) ConnectCluster(ctx context.Context) error {
	if rcc.Logger != nil {
		rcc.Logger.Info("Connecting to Redis cluster...")
	}

	// Validate configuration
	if err := rcc.ClusterConfig.Validate(); err != nil {
		if rcc.Logger != nil {
			rcc.Logger.Error("Invalid cluster configuration:", err)
		}
		return fmt.Errorf("invalid cluster configuration: %w", err)
	}

	// Create cluster client
	opts := rcc.ClusterConfig.ToRedisOptions()
	client := redis.NewClusterClient(opts)

	// Test connection
	_, err := client.Ping(ctx).Result()
	if err != nil {
		if rcc.Logger != nil {
			rcc.Logger.Error("Failed to ping Redis cluster:", err)
		}
		if closeErr := client.Close(); closeErr != nil && rcc.Logger != nil {
			rcc.Logger.Warn("Failed to close Redis client", "error", closeErr)
		}
		return fmt.Errorf("failed to connect to Redis cluster: %w", err)
	}

	rcc.ClusterClient = client
	rcc.Connected = true

	if rcc.Logger != nil {
		rcc.Logger.Info("Connected to Redis cluster ✅")
	}

	return nil
}

// GetClusterClient returns cluster client with health check
func (rcc *RedisClusterConnection) GetClusterClient(ctx context.Context) (*redis.ClusterClient, error) {
	if rcc.ClusterClient == nil || !rcc.Connected {
		if err := rcc.ConnectCluster(ctx); err != nil {
			return nil, err
		}
	}

	return rcc.ClusterClient, nil
}

// HealthCheck performs cluster-wide health check
func (rcc *RedisClusterConnection) HealthCheck(ctx context.Context) (err error) {
	if rcc.ClusterClient == nil {
		return fmt.Errorf("cluster not connected")
	}

	if !rcc.Connected {
		return fmt.Errorf("cluster not connected")
	}

	// Use defer to recover from potential panics in uninitialized clients
	defer func() {
		if r := recover(); r != nil {
			rcc.Connected = false
			err = fmt.Errorf("cluster health check failed: panic occurred: %v", r)
		}
	}()

	_, pingErr := rcc.ClusterClient.Ping(ctx).Result()
	if pingErr != nil {
		rcc.Connected = false
		return fmt.Errorf("cluster health check failed: %w", pingErr)
	}

	return nil
}

// GetClusterInfo returns cluster topology information
func (rcc *RedisClusterConnection) GetClusterInfo(ctx context.Context) (result map[string]any, err error) {
	if rcc.ClusterClient == nil {
		return nil, fmt.Errorf("cluster not connected")
	}

	if !rcc.Connected {
		return nil, fmt.Errorf("cluster not connected")
	}

	// Use defer to recover from potential panics in uninitialized clients
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("failed to get cluster info: panic occurred: %v", r)
		}
	}()

	// Get cluster nodes information
	clusterNodes, nodesErr := rcc.ClusterClient.ClusterNodes(ctx).Result()
	if nodesErr != nil {
		return nil, fmt.Errorf("failed to get cluster nodes: %w", nodesErr)
	}

	// Get cluster info
	clusterInfo, infoErr := rcc.ClusterClient.ClusterInfo(ctx).Result()
	if infoErr != nil {
		return nil, fmt.Errorf("failed to get cluster info: %w", infoErr)
	}

	return map[string]any{
		"nodes": clusterNodes,
		"info":  clusterInfo,
	}, nil
}

// Ping implements RedisClient interface
func (rcc *RedisClusterConnection) Ping(ctx context.Context) *redis.StatusCmd {
	if rcc.ClusterClient == nil {
		return redis.NewStatusCmd(ctx)
	}
	return rcc.ClusterClient.Ping(ctx)
}

// Set implements RedisClient interface
func (rcc *RedisClusterConnection) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	if rcc.ClusterClient == nil {
		return redis.NewStatusCmd(ctx)
	}
	return rcc.ClusterClient.Set(ctx, key, value, expiration)
}

// Get implements RedisClient interface
func (rcc *RedisClusterConnection) Get(ctx context.Context, key string) *redis.StringCmd {
	if rcc.ClusterClient == nil {
		return redis.NewStringCmd(ctx)
	}
	return rcc.ClusterClient.Get(ctx, key)
}

// Del implements RedisClient interface
func (rcc *RedisClusterConnection) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	if rcc.ClusterClient == nil {
		return redis.NewIntCmd(ctx)
	}
	return rcc.ClusterClient.Del(ctx, keys...)
}

// Close implements RedisClient interface
func (rcc *RedisClusterConnection) Close() error {
	if rcc.ClusterClient != nil {
		err := rcc.ClusterClient.Close()
		rcc.Connected = false
		rcc.ClusterClient = nil
		return err
	}
	return nil
}

// RedisConnectionFactory creates appropriate Redis connections
type RedisConnectionFactory struct {
	Logger log.Logger
}

// CreateConnection factory method for creating Redis connections
func (f *RedisConnectionFactory) CreateConnection(connType ConnectionType, config any) (RedisClient, error) {
	switch connType {
	case SingleInstance:
		if config == nil {
			return nil, fmt.Errorf("config cannot be nil")
		}
		
		redisConn, ok := config.(*RedisConnection)
		if !ok {
			return nil, fmt.Errorf("invalid config type for single instance connection: expected *RedisConnection")
		}
		
		// Set logger if not already set
		if redisConn.Logger == nil {
			redisConn.Logger = f.Logger
		}
		
		return redisConn, nil

	case Cluster:
		if config == nil {
			return nil, fmt.Errorf("config cannot be nil")
		}
		
		clusterConfig, ok := config.(*ClusterConfig)
		if !ok {
			return nil, fmt.Errorf("invalid config type for cluster connection: expected *ClusterConfig")
		}

		clusterConn := &RedisClusterConnection{
			ClusterConfig: *clusterConfig,
			Logger:        f.Logger,
		}

		return clusterConn, nil

	case Sentinel:
		return nil, fmt.Errorf("sentinel connection type is not yet implemented")

	default:
		return nil, fmt.Errorf("unsupported connection type: %v", connType)
	}
}

// isValidAddress validates if an address has the correct format (host:port)
func isValidAddress(addr string) bool {
	if addr == "" {
		return false
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}

	// Basic validation for host and port
	if host == "" || port == "" {
		return false
	}

	// Check if it looks like an invalid format
	if strings.Contains(addr, "//") || strings.HasPrefix(addr, ":") {
		return false
	}

	return true
}