// Package redis provides intelligent auto-detection capabilities for Redis connections.
// This file implements auto-detection for GCP environment and Redis cluster topology.
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
)

// DetectionResult represents the result of environment and topology detection
type DetectionResult struct {
	IsGCP          bool
	IsCluster      bool
	DetectedAt     time.Time
	GCPProjectID   string
	ClusterNodes   []string
	DetectionError error
}

// EnvironmentDetector interface for detecting the runtime environment
type EnvironmentDetector interface {
	IsGCP(ctx context.Context) (bool, string, error)
	GetGCPProjectID(ctx context.Context) (string, error)
}

// TopologyDetector interface for detecting Redis topology
type TopologyDetector interface {
	IsCluster(ctx context.Context, addr string) (bool, []string, error)
	DetectNodes(ctx context.Context, addrs []string) ([]string, error)
}

// DetectionCache stores detection results to avoid repeated checks
type DetectionCache struct {
	mu     sync.RWMutex
	result *DetectionResult
	ttl    time.Duration
}

// AutoDetector provides intelligent auto-detection capabilities
type AutoDetector struct {
	envDetector      EnvironmentDetector
	topoDetector     TopologyDetector
	cache            *DetectionCache
	logger           log.Logger
	detectionTimeout time.Duration
}

// GCPEnvironmentDetector implements GCP environment detection
type GCPEnvironmentDetector struct {
	metadataTimeout time.Duration
	httpClient      *http.Client
}

// RedisTopologyDetector implements Redis cluster topology detection
type RedisTopologyDetector struct {
	connectionTimeout time.Duration
	pingTimeout       time.Duration
}

// Detection configuration constants
const (
	DefaultDetectionCacheTTL = 10 * time.Minute
	DefaultDetectionTimeout  = 5 * time.Second
	DefaultMetadataTimeout   = 2 * time.Second
	DefaultConnectionTimeout = 3 * time.Second
	DefaultPingTimeout       = 1 * time.Second

	// GCP metadata server endpoint
	GCPMetadataServerURL = "http://metadata.google.internal/computeMetadata/v1/"
	GCPProjectEndpoint   = GCPMetadataServerURL + "project/project-id"
	GCPMetadataHeader    = "Metadata-Flavor"
	GCPMetadataValue     = "Google"
)

// NewAutoDetector creates a new auto-detector with default settings
func NewAutoDetector(logger log.Logger) *AutoDetector {
	return &AutoDetector{
		envDetector:  NewGCPEnvironmentDetector(),
		topoDetector: NewRedisTopologyDetector(),
		cache: &DetectionCache{
			ttl: DefaultDetectionCacheTTL,
		},
		logger:           logger,
		detectionTimeout: DefaultDetectionTimeout,
	}
}

// NewGCPEnvironmentDetector creates a new GCP environment detector
func NewGCPEnvironmentDetector() *GCPEnvironmentDetector {
	return &GCPEnvironmentDetector{
		metadataTimeout: DefaultMetadataTimeout,
		httpClient: &http.Client{
			Timeout: DefaultMetadataTimeout,
		},
	}
}

// NewRedisTopologyDetector creates a new Redis topology detector
func NewRedisTopologyDetector() *RedisTopologyDetector {
	return &RedisTopologyDetector{
		connectionTimeout: DefaultConnectionTimeout,
		pingTimeout:       DefaultPingTimeout,
	}
}

// Detect performs comprehensive auto-detection
func (ad *AutoDetector) Detect(ctx context.Context, addr string) (*DetectionResult, error) {
	// Check cache first
	if cached := ad.getCachedResult(); cached != nil {
		if ad.logger != nil {
			ad.logger.Debug("Using cached detection result",
				"is_gcp", cached.IsGCP,
				"is_cluster", cached.IsCluster,
				"age", time.Since(cached.DetectedAt))
		}
		return cached, nil
	}

	// Create timeout context for detection
	detectionCtx, cancel := context.WithTimeout(ctx, ad.detectionTimeout)
	defer cancel()

	result := &DetectionResult{
		DetectedAt: time.Now(),
	}

	// Run detections in parallel for performance
	var wg sync.WaitGroup
	var gcpErr, clusterErr error

	// GCP environment detection
	wg.Add(1)
	go func() {
		defer wg.Done()
		isGCP, projectID, err := ad.envDetector.IsGCP(detectionCtx)
		if err != nil {
			gcpErr = err
			if ad.logger != nil {
				ad.logger.Debug("GCP detection failed", "error", err)
			}
		} else {
			result.IsGCP = isGCP
			result.GCPProjectID = projectID
			if ad.logger != nil {
				ad.logger.Debug("GCP detection completed", "is_gcp", isGCP, "project_id", projectID)
			}
		}
	}()

	// Redis cluster detection
	wg.Add(1)
	go func() {
		defer wg.Done()
		isCluster, nodes, err := ad.topoDetector.IsCluster(detectionCtx, addr)
		if err != nil {
			clusterErr = err
			if ad.logger != nil {
				ad.logger.Debug("Cluster detection failed", "error", err)
			}
		} else {
			result.IsCluster = isCluster
			result.ClusterNodes = nodes
			if ad.logger != nil {
				ad.logger.Debug("Cluster detection completed", "is_cluster", isCluster, "nodes", len(nodes))
			}
		}
	}()

	wg.Wait()

	// Combine errors if both failed
	if gcpErr != nil && clusterErr != nil {
		result.DetectionError = fmt.Errorf("detection failed: gcp=%v, cluster=%v", gcpErr, clusterErr)
	} else if gcpErr != nil {
		result.DetectionError = fmt.Errorf("gcp detection failed: %w", gcpErr)
	} else if clusterErr != nil {
		result.DetectionError = fmt.Errorf("cluster detection failed: %w", clusterErr)
	}

	// Cache the result
	ad.cacheResult(result)

	if ad.logger != nil {
		ad.logger.Info("Auto-detection completed",
			"is_gcp", result.IsGCP,
			"is_cluster", result.IsCluster,
			"detection_time", time.Since(result.DetectedAt),
			"has_error", result.DetectionError != nil)
	}

	return result, nil
}

// IsGCP detects if the application is running on Google Cloud Platform
func (gcd *GCPEnvironmentDetector) IsGCP(ctx context.Context) (bool, string, error) {
	// Check environment variables first (faster)
	if gcd.checkGCPEnvironmentVariables() {
		projectID, err := gcd.GetGCPProjectID(ctx)
		return true, projectID, err
	}

	// Check metadata service
	return gcd.checkGCPMetadataService(ctx)
}

// GetGCPProjectID retrieves the GCP project ID
func (gcd *GCPEnvironmentDetector) GetGCPProjectID(ctx context.Context) (string, error) {
	// Try environment variable first
	if projectID := os.Getenv("GCP_PROJECT_ID"); projectID != "" {
		return projectID, nil
	}

	if projectID := os.Getenv("GOOGLE_CLOUD_PROJECT"); projectID != "" {
		return projectID, nil
	}

	// Try metadata service
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GCPProjectEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create metadata request: %w", err)
	}

	req.Header.Set(GCPMetadataHeader, GCPMetadataValue)

	resp, err := gcd.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query GCP metadata: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// Log the error but don't override the main error
			// In practice, you might want to use a logger here
			_ = closeErr
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GCP metadata query failed with status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read metadata response: %w", err)
	}

	return strings.TrimSpace(string(body)), nil
}

// checkGCPEnvironmentVariables checks for GCP-specific environment variables
func (gcd *GCPEnvironmentDetector) checkGCPEnvironmentVariables() bool {
	gcpIndicators := []string{
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GCP_PROJECT_ID",
		"GOOGLE_CLOUD_PROJECT",
		"GAE_APPLICATION",
		"GAE_SERVICE",
		"K_SERVICE", // Cloud Run
		"FUNCTION_NAME", // Cloud Functions
	}

	for _, env := range gcpIndicators {
		if os.Getenv(env) != "" {
			return true
		}
	}

	return false
}

// checkGCPMetadataService queries the GCP metadata service
func (gcd *GCPEnvironmentDetector) checkGCPMetadataService(ctx context.Context) (bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GCPProjectEndpoint, nil)
	if err != nil {
		return false, "", err
	}

	req.Header.Set(GCPMetadataHeader, GCPMetadataValue)

	resp, err := gcd.httpClient.Do(req)
	if err != nil {
		// Not on GCP if metadata service is unreachable
		return false, "", nil
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// Log the error but don't override the main error
			// In practice, you might want to use a logger here
			_ = closeErr
		}
	}()

	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return true, "", err
		}
		return true, strings.TrimSpace(string(body)), nil
	}

	return false, "", nil
}

// IsCluster detects if the Redis endpoint is a cluster
func (rtd *RedisTopologyDetector) IsCluster(ctx context.Context, addr string) (bool, []string, error) {
	// Parse comma-separated addresses
	addresses := parseCommaSeparatedAddresses(addr)
	
	// If multiple addresses are provided, likely cluster mode
	if len(addresses) > 1 {
		// Try to detect cluster using all addresses
		return rtd.detectClusterFromMultipleAddresses(ctx, addresses)
	}
	
	// Single address - try to connect and check cluster info
	singleAddr := addresses[0]
	client := redis.NewClient(&redis.Options{
		Addr:        singleAddr,
		DialTimeout: rtd.connectionTimeout,
		ReadTimeout: rtd.pingTimeout,
	})
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			// Log the error but don't override the main error
			// In practice, you might want to use a logger here
			_ = closeErr
		}
	}()

	// Test basic connectivity first
	pingCtx, cancel := context.WithTimeout(ctx, rtd.pingTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		return false, nil, fmt.Errorf("failed to ping Redis: %w", err)
	}

	// Try cluster-specific commands
	clusterCtx, cancel := context.WithTimeout(ctx, rtd.pingTimeout)
	defer cancel()

	// Check if CLUSTER INFO command is supported
	clusterInfo, err := client.ClusterInfo(clusterCtx).Result()
	if err != nil {
		// If CLUSTER command fails, it's likely a single instance
		if strings.Contains(err.Error(), "CLUSTER") || 
		   strings.Contains(err.Error(), "cluster") ||
		   strings.Contains(err.Error(), "This instance has cluster support disabled") {
			return false, []string{singleAddr}, nil
		}
		return false, nil, fmt.Errorf("failed to check cluster info: %w", err)
	}

	// Parse cluster info to check if it's actually clustered
	if strings.Contains(clusterInfo, "cluster_enabled:1") ||
	   strings.Contains(clusterInfo, "cluster_state:ok") {
		
		// Get cluster nodes
		nodes, err := rtd.getClusterNodes(clusterCtx, client)
		if err != nil {
			return true, []string{singleAddr}, nil // It's a cluster, but can't get all nodes
		}
		
		return true, nodes, nil
	}

	return false, []string{singleAddr}, nil
}

// DetectNodes discovers all nodes in a Redis cluster
func (rtd *RedisTopologyDetector) DetectNodes(ctx context.Context, addrs []string) ([]string, error) {
	var allNodes []string
	seen := make(map[string]bool)

	for _, addr := range addrs {
		isCluster, nodes, err := rtd.IsCluster(ctx, addr)
		if err != nil {
			continue // Try next address
		}

		if isCluster {
			for _, node := range nodes {
				if !seen[node] {
					allNodes = append(allNodes, node)
					seen[node] = true
				}
			}
		} else {
			if !seen[addr] {
				allNodes = append(allNodes, addr)
				seen[addr] = true
			}
		}
	}

	if len(allNodes) == 0 {
		return nil, fmt.Errorf("no valid Redis nodes detected")
	}

	return allNodes, nil
}

// getClusterNodes retrieves all cluster node addresses
func (rtd *RedisTopologyDetector) getClusterNodes(ctx context.Context, client *redis.Client) ([]string, error) {
	nodesInfo, err := client.ClusterNodes(ctx).Result()
	if err != nil {
		return nil, err
	}

	var nodes []string
	lines := strings.Split(nodesInfo, "\n")
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// Parse node address (format: ip:port@cluster_port or ip:port)
		nodeAddr := parts[1]
		if idx := strings.Index(nodeAddr, "@"); idx > 0 {
			nodeAddr = nodeAddr[:idx]
		}

		// Validate address format
		if host, port, err := net.SplitHostPort(nodeAddr); err == nil && host != "" && port != "" {
			nodes = append(nodes, nodeAddr)
		}
	}

	return nodes, nil
}

// getCachedResult returns cached detection result if valid
func (ad *AutoDetector) getCachedResult() *DetectionResult {
	ad.cache.mu.RLock()
	defer ad.cache.mu.RUnlock()

	if ad.cache.result == nil {
		return nil
	}

	// Check if cache is still valid
	if time.Since(ad.cache.result.DetectedAt) > ad.cache.ttl {
		return nil
	}

	return ad.cache.result
}

// cacheResult stores detection result in cache
func (ad *AutoDetector) cacheResult(result *DetectionResult) {
	ad.cache.mu.Lock()
	defer ad.cache.mu.Unlock()
	
	ad.cache.result = result
}

// ClearCache clears the detection cache
func (ad *AutoDetector) ClearCache() {
	ad.cache.mu.Lock()
	defer ad.cache.mu.Unlock()
	
	ad.cache.result = nil
}

// SetCacheTTL sets the cache time-to-live
func (ad *AutoDetector) SetCacheTTL(ttl time.Duration) {
	ad.cache.mu.Lock()
	defer ad.cache.mu.Unlock()
	
	ad.cache.ttl = ttl
}

// DetectionSummary provides a string summary of detection results
func (dr *DetectionResult) DetectionSummary() string {
	var parts []string
	
	if dr.IsGCP {
		parts = append(parts, fmt.Sprintf("GCP(project=%s)", dr.GCPProjectID))
	} else {
		parts = append(parts, "Non-GCP")
	}
	
	if dr.IsCluster {
		parts = append(parts, fmt.Sprintf("Cluster(nodes=%d)", len(dr.ClusterNodes)))
	} else {
		parts = append(parts, "Single")
	}
	
	if dr.DetectionError != nil {
		parts = append(parts, fmt.Sprintf("Error=%v", dr.DetectionError))
	}
	
	return strings.Join(parts, ", ")
}

// ToJSON converts detection result to JSON for logging/debugging
func (dr *DetectionResult) ToJSON() (string, error) {
	data, err := json.Marshal(dr)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// parseCommaSeparatedAddresses parses comma-separated addresses into a slice
func parseCommaSeparatedAddresses(addr string) []string {
	if addr == "" {
		return []string{"localhost:6379"} // Default
	}

	// Split by comma and trim whitespace
	rawAddrs := strings.Split(addr, ",")
	addresses := make([]string, 0, len(rawAddrs))

	for _, rawAddr := range rawAddrs {
		trimmed := strings.TrimSpace(rawAddr)
		if trimmed != "" && isValidRedisAddress(trimmed) {
			addresses = append(addresses, trimmed)
		}
	}

	if len(addresses) == 0 {
		return []string{addr} // Return original if parsing failed
	}

	return addresses
}

// isValidRedisAddress validates if an address has the correct format (host:port)
func isValidRedisAddress(addr string) bool {
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

// detectClusterFromMultipleAddresses tries to detect cluster mode using multiple addresses
func (rtd *RedisTopologyDetector) detectClusterFromMultipleAddresses(ctx context.Context, addresses []string) (bool, []string, error) {
	// Try each address to see if any respond
	for _, addr := range addresses {
		client := redis.NewClient(&redis.Options{
			Addr:        addr,
			DialTimeout: rtd.connectionTimeout,
			ReadTimeout: rtd.pingTimeout,
		})

		// Test basic connectivity
		pingCtx, cancel := context.WithTimeout(ctx, rtd.pingTimeout)
		
		err := client.Ping(pingCtx).Err()
		cancel()
		
		if err != nil {
			if closeErr := client.Close(); closeErr != nil {
				// Log the error but continue processing
				_ = closeErr
			}
			continue // Try next address
		}

		// Check if this node is part of a cluster
		clusterCtx, cancel := context.WithTimeout(ctx, rtd.pingTimeout)
		
		clusterInfo, err := client.ClusterInfo(clusterCtx).Result()
		cancel()
		
		if err == nil && (strings.Contains(clusterInfo, "cluster_enabled:1") || strings.Contains(clusterInfo, "cluster_state:ok")) {
			// This is a cluster node, get all cluster nodes
			nodes, err := rtd.getClusterNodes(clusterCtx, client)
			if closeErr := client.Close(); closeErr != nil {
				// Log the error but continue processing
				_ = closeErr
			}
			if err != nil {
				return true, addresses, err // It's a cluster, use provided addresses
			}
			return true, nodes, nil
		}
		
		if closeErr := client.Close(); closeErr != nil {
			// Log the error but continue processing
			_ = closeErr
		}
	}

	// If we reach here, none of the addresses responded or they're not cluster nodes
	// Return false since we couldn't confirm cluster mode, but return the addresses for fallback
	return false, addresses, fmt.Errorf("failed to connect to any of the provided addresses")
}