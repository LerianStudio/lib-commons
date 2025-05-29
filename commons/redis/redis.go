// Package redis provides Redis connection management and operations utilities.
// It includes connection pooling, health checks, and helper functions for interacting
// with Redis databases in a safe and efficient manner.
// 
// The SmartRedisConnection automatically detects cluster mode and GCP IAM authentication
// based on address format and environment variables, while maintaining 100% backward
// compatibility with existing RedisConnection usage patterns.
package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
)

// RedisTTL defines the default time-to-live (TTL) for Redis cache entries in seconds.
const RedisTTL = 300

// RedisConnection is a hub which deal with redis connections.
// The type name intentionally matches the package name for clarity in external usage.
// Now includes intelligent auto-detection for GCP IAM authentication and cluster topology.
type RedisConnection struct {
	Addr         string
	User         string
	Password     string
	DB           int
	Protocol     int
	Client       *redis.Client  // Keep original type for 100% backward compatibility
	Connected    bool
	Logger       log.Logger
	autoDetector *AutoDetector
	
	// Internal fields for smart connection management
	detectedConfig *DetectionResult
	actualClient   interface{} // Can be *redis.Client or *redis.ClusterClient
	tokenProvider  TokenProvider
	gcpAuth        *GCPAuthConfig
}

// Connect keeps a singleton connection with redis using intelligent auto-detection.
// Automatically detects GCP environment and cluster topology for optimal connection.
func (rc *RedisConnection) Connect(ctx context.Context) error {
	if rc.Logger != nil {
		rc.Logger.Info("Connecting to redis with auto-detection...")
	}

	// Initialize auto-detector if not already set
	if rc.autoDetector == nil {
		rc.autoDetector = NewAutoDetector(rc.Logger)
	}

	// Perform auto-detection
	detection, err := rc.autoDetector.Detect(ctx, rc.Addr)
	if err != nil {
		if rc.Logger != nil {
			rc.Logger.Warn("Auto-detection failed, using fallback", "error", err)
		}
		// Continue with fallback to regular connection
		detection = &DetectionResult{
			IsGCP:      false,
			IsCluster:  false,
			DetectedAt: time.Now(),
		}
	}

	rc.detectedConfig = detection

	if rc.Logger != nil {
		rc.Logger.Info("Detection completed", 
			"summary", detection.DetectionSummary(),
			"addr", rc.Addr)
	}

	// Create appropriate client based on detection
	client, actualClient, err := rc.createOptimalClient(ctx, detection)
	if err != nil {
		if rc.Logger != nil {
			rc.Logger.Error("Failed to create optimal client", "error", err)
		}
		return err
	}

	// Test connection using the actual client
	var pingErr error
	switch actualClient := actualClient.(type) {
	case *redis.Client:
		pingErr = actualClient.Ping(ctx).Err()
	case *redis.ClusterClient:
		pingErr = actualClient.Ping(ctx).Err()
	default:
		// Fallback to interface method
		pingErr = client.Ping(ctx).Err()
	}
	
	if pingErr != nil {
		if rc.Logger != nil {
			rc.Logger.Error("Failed to ping Redis", "error", pingErr)
		}
		return fmt.Errorf("failed to ping Redis: %w", pingErr)
	}

	// Store the actual client for backward compatibility
	if redisClient, ok := actualClient.(*redis.Client); ok {
		rc.Client = redisClient
	}
	rc.actualClient = actualClient
	rc.Connected = true

	if rc.Logger != nil {
		rc.Logger.Info("Connected to redis ✅", 
			"type", rc.getConnectionType(),
			"auth", rc.getAuthType())
	}

	return nil
}

// createOptimalClient creates the most appropriate Redis client based on detection
func (rc *RedisConnection) createOptimalClient(ctx context.Context, detection *DetectionResult) (RedisClient, interface{}, error) {
	// Determine connection strategy
	switch {
	case detection.IsGCP && detection.IsCluster:
		return rc.createGCPClusterClient(ctx, detection)
	case detection.IsGCP && !detection.IsCluster:
		return rc.createGCPSingleClient(ctx, detection)
	case !detection.IsGCP && detection.IsCluster:
		return rc.createRegularClusterClient(ctx, detection)
	default:
		return rc.createRegularSingleClient(ctx)
	}
}

// createGCPClusterClient creates a GCP IAM authenticated cluster client
func (rc *RedisConnection) createGCPClusterClient(ctx context.Context, detection *DetectionResult) (RedisClient, interface{}, error) {
	config, err := ConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load GCP config: %w", err)
	}

	if !config.Enabled {
		if rc.Logger != nil {
			rc.Logger.Warn("GCP detected but authentication disabled, using regular cluster connection")
		}
		return rc.createRegularClusterClient(ctx, detection)
	}

	// Create GCP authenticated cluster connection
	tokenProvider, err := NewGCPTokenProvider(config, rc.Logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create GCP token provider: %w", err)
	}

	// Get access token
	token, err := tokenProvider.GetToken(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get GCP token: %w", err)
	}

	// Create cluster client with GCP auth
	addrs := detection.ClusterNodes
	if len(addrs) == 0 {
		// Parse comma-separated addresses
		addrs = rc.parseAddresses()
	}

	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:    addrs,
		Username: "default", // GCP uses token as password
		Password: token,
	})

	// Wrap in authenticated connection for token management
	authConn := &AuthenticatedRedisConnection{
		BaseConnection: &RedisClusterConnection{
			ClusterClient: clusterClient,
			Connected:     true,
			Logger:        rc.Logger,
		},
		TokenProvider: tokenProvider,
		AuthConfig:    *config,
		Connected:     true,
		Logger:        rc.Logger,
	}

	return authConn, clusterClient, nil
}

// createGCPSingleClient creates a GCP IAM authenticated single instance client
func (rc *RedisConnection) createGCPSingleClient(ctx context.Context, detection *DetectionResult) (RedisClient, interface{}, error) {
	config, err := ConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load GCP config: %w", err)
	}

	if !config.Enabled {
		if rc.Logger != nil {
			rc.Logger.Warn("GCP detected but authentication disabled, using regular connection")
		}
		return rc.createRegularSingleClient(ctx)
	}

	// Create GCP authenticated single connection
	tokenProvider, err := NewGCPTokenProvider(config, rc.Logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create GCP token provider: %w", err)
	}

	// Get access token
	token, err := tokenProvider.GetToken(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get GCP token: %w", err)
	}

	// Create single client with GCP auth
	// Parse comma-separated addresses and use the first one for single client
	addresses := rc.parseAddresses()
	singleAddr := addresses[0]
	
	singleClient := redis.NewClient(&redis.Options{
		Addr:     singleAddr,
		Username: "default", // GCP uses token as password
		Password: token,
		DB:       rc.DB,
		Protocol: rc.Protocol,
	})

	// Store token provider and config for token management
	// Note: rc.Client will be set later in Connect() after successful ping test
	rc.tokenProvider = tokenProvider
	rc.gcpAuth = config

	// Create an authenticated wrapper that doesn't cause circular references
	authConn := &AuthenticatedRedisConnection{
		BaseConnection: rc,
		TokenProvider:  tokenProvider,
		AuthConfig:     *config,
		Connected:      true,
		Logger:         rc.Logger,
	}

	// Start token auto-refresh
	if err := tokenProvider.StartAutoRefresh(ctx); err != nil && rc.Logger != nil {
		rc.Logger.Warn("Failed to start token auto-refresh", "error", err)
	}

	return authConn, singleClient, nil
}

// createRegularClusterClient creates a regular cluster client without GCP auth
func (rc *RedisConnection) createRegularClusterClient(ctx context.Context, detection *DetectionResult) (RedisClient, interface{}, error) {
	addrs := detection.ClusterNodes
	if len(addrs) == 0 {
		// Parse comma-separated addresses
		addrs = rc.parseAddresses()
	}

	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:    addrs,
		Username: rc.User,
		Password: rc.Password,
	})

	clusterConn := &RedisClusterConnection{
		ClusterConfig: ClusterConfig{
			Addrs:    addrs,
			Username: rc.User,
			Password: rc.Password,
		},
		ClusterClient: clusterClient,
		Connected:     true,
		Logger:        rc.Logger,
	}

	return clusterConn, clusterClient, nil
}

// createRegularSingleClient creates a regular single instance client
func (rc *RedisConnection) createRegularSingleClient(ctx context.Context) (RedisClient, interface{}, error) {
	// Parse comma-separated addresses and use the first one for single client
	addresses := rc.parseAddresses()
	singleAddr := addresses[0]
	
	singleClient := redis.NewClient(&redis.Options{
		Addr:     singleAddr,
		Username: rc.User,
		Password: rc.Password,
		DB:       rc.DB,
		Protocol: rc.Protocol,
	})

	// Note: rc.Client will be set later in Connect() after successful ping test
	return rc, singleClient, nil
}

// getConnectionType returns human-readable connection type
func (rc *RedisConnection) getConnectionType() string {
	if rc.detectedConfig == nil {
		return "unknown"
	}
	if rc.detectedConfig.IsCluster {
		return "cluster"
	}
	return "single"
}

// getAuthType returns human-readable auth type
func (rc *RedisConnection) getAuthType() string {
	if rc.detectedConfig == nil {
		return "unknown"
	}
	if rc.detectedConfig.IsGCP {
		return "gcp-iam"
	}
	return "standard"
}

// GetClient returns a pointer to the redis connection, initializing it if necessary.
// This method maintains backward compatibility while leveraging auto-detection.
func (rc *RedisConnection) GetClient(ctx context.Context) (*redis.Client, error) {
	if rc.Client == nil {
		err := rc.Connect(ctx)
		if err != nil {
			if rc.Logger != nil {
				rc.Logger.Error("Failed to connect", "error", err)
			}
			return nil, err
		}
	}

	// Handle different client types for backward compatibility
	switch client := rc.actualClient.(type) {
	case *redis.Client:
		return client, nil
	case *redis.ClusterClient:
		// For cluster clients, we need to provide a compatible interface
		// Create a wrapper that implements the expected interface
		if rc.Logger != nil {
			rc.Logger.Warn("Returning cluster client as single client interface - some operations may not work as expected")
		}
		// Return the first single client from cluster for compatibility
		return rc.getSingleClientFromCluster(client), nil
	default:
		if rc.Logger != nil {
			rc.Logger.Warn("Unknown client type, attempting to return stored client")
		}
		// Return the stored client if available
		if rc.Client != nil {
			return rc.Client, nil
		}
		return nil, fmt.Errorf("unable to provide *redis.Client from current connection type")
	}
}

// getSingleClientFromCluster creates a single client connection for backward compatibility
func (rc *RedisConnection) getSingleClientFromCluster(clusterClient *redis.ClusterClient) *redis.Client {
	// Create a single connection to the first available node
	return redis.NewClient(&redis.Options{
		Addr:     rc.Addr,
		Username: rc.User,
		Password: rc.Password,
		DB:       rc.DB,
		Protocol: rc.Protocol,
	})
}

// GetDetectedConfig returns the auto-detection results for inspection
func (rc *RedisConnection) GetDetectedConfig() *DetectionResult {
	return rc.detectedConfig
}

// GetActualClient returns the actual underlying client (cluster or single)
func (rc *RedisConnection) GetActualClient() interface{} {
	return rc.actualClient
}

// IsClusterConnection returns true if the connection is using cluster mode
func (rc *RedisConnection) IsClusterConnection() bool {
	return rc.detectedConfig != nil && rc.detectedConfig.IsCluster
}

// IsGCPAuthenticated returns true if the connection is using GCP IAM authentication
func (rc *RedisConnection) IsGCPAuthenticated() bool {
	return rc.detectedConfig != nil && rc.detectedConfig.IsGCP
}

// Ping implements RedisClient interface
func (rc *RedisConnection) Ping(ctx context.Context) *redis.StatusCmd {
	if rc.Client == nil {
		return redis.NewStatusCmd(ctx)
	}
	return rc.Client.Ping(ctx)
}

// Set implements RedisClient interface
func (rc *RedisConnection) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd {
	if rc.Client == nil {
		return redis.NewStatusCmd(ctx)
	}
	return rc.Client.Set(ctx, key, value, expiration)
}

// Get implements RedisClient interface
func (rc *RedisConnection) Get(ctx context.Context, key string) *redis.StringCmd {
	if rc.Client == nil {
		return redis.NewStringCmd(ctx)
	}
	return rc.Client.Get(ctx, key)
}

// Del implements RedisClient interface
func (rc *RedisConnection) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	if rc.Client == nil {
		return redis.NewIntCmd(ctx)
	}
	return rc.Client.Del(ctx, keys...)
}

// Close implements RedisClient interface
func (rc *RedisConnection) Close() error {
	if rc.Client != nil {
		err := rc.Client.Close()
		rc.Connected = false
		rc.Client = nil
		rc.actualClient = nil
		rc.detectedConfig = nil
		return err
	}
	return nil
}

// RefreshDetection clears the cache and re-runs auto-detection
func (rc *RedisConnection) RefreshDetection(ctx context.Context) error {
	if rc.autoDetector != nil {
		rc.autoDetector.ClearCache()
	}
	
	// Reconnect with fresh detection
	if err := rc.Close(); err != nil {
		return fmt.Errorf("failed to close existing connection: %w", err)
	}
	
	return rc.Connect(ctx)
}

// parseAddresses extracts individual addresses from the Addr field
// Supports formats: "host:port" or "host1:port1,host2:port2,host3:port3"
func (rc *RedisConnection) parseAddresses() []string {
	if rc.Addr == "" {
		return []string{"localhost:6379"} // Default
	}

	// Split by comma and trim whitespace
	rawAddrs := strings.Split(rc.Addr, ",")
	addresses := make([]string, 0, len(rawAddrs))

	for _, addr := range rawAddrs {
		trimmed := strings.TrimSpace(addr)
		if trimmed != "" && isValidRedisAddress(trimmed) {
			addresses = append(addresses, trimmed)
		}
	}

	if len(addresses) == 0 {
		return []string{rc.Addr} // Return original if parsing failed
	}

	return addresses
}
