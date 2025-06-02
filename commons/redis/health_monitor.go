package redis

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// HealthStatus represents the health status of a Redis connection
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusUnknown   HealthStatus = "unknown"
)

// NodeRole represents the role of a Redis node
type NodeRole string

const (
	NodeRoleMaster  NodeRole = "master"
	NodeRoleSlave   NodeRole = "slave"
	NodeRoleUnknown NodeRole = "unknown"
)

// HealthMonitorConfig defines configuration for Redis health monitoring
type HealthMonitorConfig struct {
	CheckInterval          time.Duration         `json:"check_interval"`
	TimeoutDuration        time.Duration         `json:"timeout_duration"`
	MaxFailures            int                   `json:"max_failures"`
	FailureWindow          time.Duration         `json:"failure_window"`
	EnableAutoFailover     bool                  `json:"enable_auto_failover"`
	FailoverCooldown       time.Duration         `json:"failover_cooldown"`
	HealthCheckCommands    []string              `json:"health_check_commands"`
	PerformanceThresholds  PerformanceThresholds `json:"performance_thresholds"`
	EnableDetailedMetrics  bool                  `json:"enable_detailed_metrics"`
	MetricsRetentionPeriod time.Duration         `json:"metrics_retention_period"`
	AlertThresholds        AlertThresholds       `json:"alert_thresholds"`
}

// PerformanceThresholds defines performance-related health thresholds
type PerformanceThresholds struct {
	MaxLatency          time.Duration `json:"max_latency"`
	MaxMemoryUsage      float64       `json:"max_memory_usage"`       // percentage
	MinAvailableMemory  int64         `json:"min_available_memory"`   // bytes
	MaxConnectionsUsage float64       `json:"max_connections_usage"`  // percentage
	MaxCPUUsage         float64       `json:"max_cpu_usage"`          // percentage
	MaxKeyspaceHitRatio float64       `json:"max_keyspace_hit_ratio"` // minimum hit ratio
}

// AlertThresholds defines when to trigger alerts
type AlertThresholds struct {
	ConsecutiveFailures int           `json:"consecutive_failures"`
	LatencyP95          time.Duration `json:"latency_p95"`
	MemoryUsagePercent  float64       `json:"memory_usage_percent"`
	ConnectionsPercent  float64       `json:"connections_percent"`
	KeyspaceHitRatio    float64       `json:"keyspace_hit_ratio"`
	ReplicationLag      time.Duration `json:"replication_lag"`
}

// DefaultHealthMonitorConfig returns a production-ready health monitoring configuration
func DefaultHealthMonitorConfig() HealthMonitorConfig {
	return HealthMonitorConfig{
		CheckInterval:          10 * time.Second,
		TimeoutDuration:        5 * time.Second,
		MaxFailures:            3,
		FailureWindow:          1 * time.Minute,
		EnableAutoFailover:     true,
		FailoverCooldown:       2 * time.Minute,
		HealthCheckCommands:    []string{"PING", "INFO", "MEMORY USAGE"},
		EnableDetailedMetrics:  true,
		MetricsRetentionPeriod: 24 * time.Hour,
		PerformanceThresholds: PerformanceThresholds{
			MaxLatency:          50 * time.Millisecond,
			MaxMemoryUsage:      85.0,              // 85%
			MinAvailableMemory:  100 * 1024 * 1024, // 100MB
			MaxConnectionsUsage: 80.0,              // 80%
			MaxCPUUsage:         90.0,              // 90%
			MaxKeyspaceHitRatio: 0.90,              // 90% minimum hit ratio
		},
		AlertThresholds: AlertThresholds{
			ConsecutiveFailures: 3,
			LatencyP95:          100 * time.Millisecond,
			MemoryUsagePercent:  90.0,
			ConnectionsPercent:  85.0,
			KeyspaceHitRatio:    0.80,
			ReplicationLag:      10 * time.Second,
		},
	}
}

// NodeHealth represents the health status of a Redis node
type NodeHealth struct {
	NodeID              string               `json:"node_id"`
	Address             string               `json:"address"`
	Role                NodeRole             `json:"role"`
	Status              HealthStatus         `json:"status"`
	LastChecked         time.Time            `json:"last_checked"`
	LastHealthy         time.Time            `json:"last_healthy"`
	ConsecutiveFailures int                  `json:"consecutive_failures"`
	FailureHistory      []HealthCheckFailure `json:"failure_history"`
	Performance         PerformanceMetrics   `json:"performance"`
	Alerts              []HealthAlert        `json:"alerts"`

	// Redis-specific metrics
	UptimeSeconds     int64 `json:"uptime_seconds"`
	ConnectedClients  int64 `json:"connected_clients"`
	UsedMemory        int64 `json:"used_memory"`
	MaxMemory         int64 `json:"max_memory"`
	KeyspaceHits      int64 `json:"keyspace_hits"`
	KeyspaceMisses    int64 `json:"keyspace_misses"`
	TotalCommands     int64 `json:"total_commands"`
	ReplicationOffset int64 `json:"replication_offset"`
}

// PerformanceMetrics contains detailed performance metrics
type PerformanceMetrics struct {
	Latency          time.Duration    `json:"latency"`
	Throughput       float64          `json:"throughput"`        // ops per second
	MemoryUsage      float64          `json:"memory_usage"`      // percentage
	ConnectionsUsage float64          `json:"connections_usage"` // percentage
	CPUUsage         float64          `json:"cpu_usage"`         // percentage
	KeyspaceHitRatio float64          `json:"keyspace_hit_ratio"`
	NetworkIO        NetworkIOMetrics `json:"network_io"`
}

// NetworkIOMetrics contains network I/O statistics
type NetworkIOMetrics struct {
	InputKbps  float64 `json:"input_kbps"`
	OutputKbps float64 `json:"output_kbps"`
	InputOps   float64 `json:"input_ops"`
	OutputOps  float64 `json:"output_ops"`
}

// HealthCheckFailure represents a health check failure
type HealthCheckFailure struct {
	Timestamp time.Time     `json:"timestamp"`
	Error     string        `json:"error"`
	Command   string        `json:"command"`
	Duration  time.Duration `json:"duration"`
}

// HealthAlert represents a health-related alert
type HealthAlert struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Severity   string                 `json:"severity"`
	Message    string                 `json:"message"`
	Timestamp  time.Time              `json:"timestamp"`
	Resolved   bool                   `json:"resolved"`
	ResolvedAt *time.Time             `json:"resolved_at,omitempty"`
	Metadata   map[string]interface{} `json:"metadata"`
}

// ClusterHealth represents the overall health of a Redis cluster
type ClusterHealth struct {
	ClusterID      string                 `json:"cluster_id"`
	OverallStatus  HealthStatus           `json:"overall_status"`
	TotalNodes     int                    `json:"total_nodes"`
	HealthyNodes   int                    `json:"healthy_nodes"`
	DegradedNodes  int                    `json:"degraded_nodes"`
	UnhealthyNodes int                    `json:"unhealthy_nodes"`
	LastChecked    time.Time              `json:"last_checked"`
	Nodes          map[string]*NodeHealth `json:"nodes"`
	MasterNodes    []string               `json:"master_nodes"`
	SlaveNodes     []string               `json:"slave_nodes"`
	FailoverEvents []FailoverEvent        `json:"failover_events"`
	Alerts         []HealthAlert          `json:"alerts"`
}

// FailoverEvent represents a failover event
type FailoverEvent struct {
	Timestamp    time.Time     `json:"timestamp"`
	Type         string        `json:"type"` // automatic, manual, partial
	FromNode     string        `json:"from_node"`
	ToNode       string        `json:"to_node"`
	Reason       string        `json:"reason"`
	Duration     time.Duration `json:"duration"`
	Success      bool          `json:"success"`
	ErrorMessage string        `json:"error_message,omitempty"`
}

// HealthMonitor monitors Redis connection health and provides automatic failover
type HealthMonitor struct {
	config HealthMonitorConfig
	logger log.Logger

	// Connection management
	primaryClient  redis.UniversalClient
	replicaClients []redis.UniversalClient
	activeClient   redis.UniversalClient

	// Health tracking
	clusterHealth *ClusterHealth
	healthMutex   sync.RWMutex

	// Monitoring state
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Failover management
	lastFailover  time.Time
	failoverMutex sync.Mutex

	// Metrics history
	metricsHistory []PerformanceMetrics
	metricsMutex   sync.RWMutex
}

// NewHealthMonitor creates a new Redis health monitor
func NewHealthMonitor(
	primary redis.UniversalClient,
	replicas []redis.UniversalClient,
	config HealthMonitorConfig,
	logger log.Logger,
) *HealthMonitor {
	ctx, cancel := context.WithCancel(context.Background())

	monitor := &HealthMonitor{
		config:         config,
		logger:         logger,
		primaryClient:  primary,
		replicaClients: replicas,
		activeClient:   primary,
		ctx:            ctx,
		cancel:         cancel,
		clusterHealth: &ClusterHealth{
			ClusterID:      generateClusterID(),
			OverallStatus:  HealthStatusUnknown,
			Nodes:          make(map[string]*NodeHealth),
			FailoverEvents: make([]FailoverEvent, 0),
			Alerts:         make([]HealthAlert, 0),
		},
		metricsHistory: make([]PerformanceMetrics, 0),
	}

	monitor.initializeNodes()
	monitor.startMonitoring()

	return monitor
}

// GetActiveClient returns the currently active Redis client
func (hm *HealthMonitor) GetActiveClient() redis.UniversalClient {
	hm.healthMutex.RLock()
	defer hm.healthMutex.RUnlock()
	return hm.activeClient
}

// GetClusterHealth returns the current cluster health status
func (hm *HealthMonitor) GetClusterHealth() ClusterHealth {
	hm.healthMutex.RLock()
	defer hm.healthMutex.RUnlock()
	return *hm.clusterHealth
}

// GetNodeHealth returns the health status of a specific node
func (hm *HealthMonitor) GetNodeHealth(nodeID string) (*NodeHealth, error) {
	hm.healthMutex.RLock()
	defer hm.healthMutex.RUnlock()

	node, exists := hm.clusterHealth.Nodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	return node, nil
}

// ForceFailover manually triggers a failover to the next available replica
func (hm *HealthMonitor) ForceFailover(reason string) error {
	hm.failoverMutex.Lock()
	defer hm.failoverMutex.Unlock()

	// Check failover cooldown
	if time.Since(hm.lastFailover) < hm.config.FailoverCooldown {
		return fmt.Errorf("failover cooldown period not elapsed")
	}

	return hm.performFailover(reason, "manual")
}

// initializeNodes sets up initial node health tracking
func (hm *HealthMonitor) initializeNodes() {
	hm.healthMutex.Lock()
	defer hm.healthMutex.Unlock()

	// Initialize primary node
	primaryNodeID := generateNodeID(hm.primaryClient)
	hm.clusterHealth.Nodes[primaryNodeID] = &NodeHealth{
		NodeID:         primaryNodeID,
		Address:        getClientAddress(hm.primaryClient),
		Role:           NodeRoleMaster,
		Status:         HealthStatusUnknown,
		LastChecked:    time.Now(),
		FailureHistory: make([]HealthCheckFailure, 0),
		Alerts:         make([]HealthAlert, 0),
	}
	hm.clusterHealth.MasterNodes = append(hm.clusterHealth.MasterNodes, primaryNodeID)

	// Initialize replica nodes
	for _, replica := range hm.replicaClients {
		replicaNodeID := generateNodeID(replica)
		hm.clusterHealth.Nodes[replicaNodeID] = &NodeHealth{
			NodeID:         replicaNodeID,
			Address:        getClientAddress(replica),
			Role:           NodeRoleSlave,
			Status:         HealthStatusUnknown,
			LastChecked:    time.Now(),
			FailureHistory: make([]HealthCheckFailure, 0),
			Alerts:         make([]HealthAlert, 0),
		}
		hm.clusterHealth.SlaveNodes = append(hm.clusterHealth.SlaveNodes, replicaNodeID)
	}

	hm.clusterHealth.TotalNodes = len(hm.clusterHealth.Nodes)
}

// startMonitoring begins the health monitoring process
func (hm *HealthMonitor) startMonitoring() {
	hm.wg.Add(2)

	// Health check goroutine
	go func() {
		defer hm.wg.Done()
		ticker := time.NewTicker(hm.config.CheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-hm.ctx.Done():
				return
			case <-ticker.C:
				hm.performHealthCheck()
			}
		}
	}()

	// Metrics cleanup goroutine
	go func() {
		defer hm.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-hm.ctx.Done():
				return
			case <-ticker.C:
				hm.cleanupOldMetrics()
			}
		}
	}()

	hm.logger.Info("Redis health monitoring started",
		zap.Duration("check_interval", hm.config.CheckInterval),
		zap.Bool("auto_failover", hm.config.EnableAutoFailover),
		zap.Int("total_nodes", hm.clusterHealth.TotalNodes),
	)
}

// performHealthCheck executes health checks on all nodes
func (hm *HealthMonitor) performHealthCheck() {
	hm.healthMutex.Lock()
	defer hm.healthMutex.Unlock()

	now := time.Now()
	healthyCount := 0
	degradedCount := 0
	unhealthyCount := 0

	// Check each node
	for nodeID, node := range hm.clusterHealth.Nodes {
		client := hm.getClientForNode(nodeID)
		if client == nil {
			continue
		}

		nodeHealth := hm.checkNodeHealth(client, node)
		hm.clusterHealth.Nodes[nodeID] = nodeHealth

		// Update counters
		switch nodeHealth.Status {
		case HealthStatusHealthy:
			healthyCount++
		case HealthStatusDegraded:
			degradedCount++
		case HealthStatusUnhealthy:
			unhealthyCount++
		}

		// Check for failover conditions
		if hm.config.EnableAutoFailover && nodeID == generateNodeID(hm.activeClient) {
			if nodeHealth.Status == HealthStatusUnhealthy {
				if nodeHealth.ConsecutiveFailures >= hm.config.MaxFailures {
					go hm.handleAutoFailover(nodeID, "consecutive_failures")
				}
			}
		}
	}

	// Update cluster health
	hm.clusterHealth.HealthyNodes = healthyCount
	hm.clusterHealth.DegradedNodes = degradedCount
	hm.clusterHealth.UnhealthyNodes = unhealthyCount
	hm.clusterHealth.LastChecked = now

	// Determine overall cluster status
	if unhealthyCount == 0 && degradedCount == 0 {
		hm.clusterHealth.OverallStatus = HealthStatusHealthy
	} else if unhealthyCount > 0 && healthyCount > 0 {
		hm.clusterHealth.OverallStatus = HealthStatusDegraded
	} else if healthyCount == 0 {
		hm.clusterHealth.OverallStatus = HealthStatusUnhealthy
	} else {
		hm.clusterHealth.OverallStatus = HealthStatusDegraded
	}
}

// checkNodeHealth performs health check on a specific node
func (hm *HealthMonitor) checkNodeHealth(
	client redis.UniversalClient,
	node *NodeHealth,
) *NodeHealth {
	ctx, cancel := context.WithTimeout(hm.ctx, hm.config.TimeoutDuration)
	defer cancel()

	start := time.Now()
	isHealthy := true
	var checkErrors []HealthCheckFailure

	// Perform health check commands
	for _, command := range hm.config.HealthCheckCommands {
		commandStart := time.Now()
		err := hm.executeHealthCheckCommand(ctx, client, command)
		commandDuration := time.Since(commandStart)

		if err != nil {
			isHealthy = false
			checkErrors = append(checkErrors, HealthCheckFailure{
				Timestamp: commandStart,
				Error:     err.Error(),
				Command:   command,
				Duration:  commandDuration,
			})
		}
	}

	latency := time.Since(start)

	// Get detailed performance metrics if enabled
	var performance PerformanceMetrics
	if hm.config.EnableDetailedMetrics {
		performance = hm.collectPerformanceMetrics(ctx, client, latency)
	}

	// Update node health
	node.LastChecked = time.Now()
	node.Performance = performance

	// Determine health status
	if isHealthy && hm.isPerformanceHealthy(performance) {
		node.Status = HealthStatusHealthy
		node.LastHealthy = time.Now()
		node.ConsecutiveFailures = 0
	} else if isHealthy {
		node.Status = HealthStatusDegraded
		node.ConsecutiveFailures = 0
	} else {
		node.Status = HealthStatusUnhealthy
		node.ConsecutiveFailures++
	}

	// Add failure history
	if len(checkErrors) > 0 {
		node.FailureHistory = append(node.FailureHistory, checkErrors...)
		// Keep only recent failures
		if len(node.FailureHistory) > 100 {
			node.FailureHistory = node.FailureHistory[len(node.FailureHistory)-100:]
		}
	}

	// Generate alerts if needed
	node.Alerts = hm.generateAlertsForNode(node)

	return node
}

// executeHealthCheckCommand executes a specific health check command
func (hm *HealthMonitor) executeHealthCheckCommand(
	ctx context.Context,
	client redis.UniversalClient,
	command string,
) error {
	switch command {
	case "PING":
		return client.Ping(ctx).Err()
	case "INFO":
		return client.Info(ctx).Err()
	case "MEMORY USAGE":
		// Check memory usage of a test key
		return client.MemoryUsage(ctx, "healthcheck:test").Err()
	default:
		// Execute custom command
		return client.Do(ctx, command).Err()
	}
}

// collectPerformanceMetrics gathers detailed performance metrics
func (hm *HealthMonitor) collectPerformanceMetrics(
	ctx context.Context,
	client redis.UniversalClient,
	latency time.Duration,
) PerformanceMetrics {
	metrics := PerformanceMetrics{
		Latency: latency,
	}

	// Get Redis INFO
	info, err := client.Info(ctx, "all").Result()
	if err != nil {
		return metrics
	}

	// Parse INFO output for metrics
	infoMap := parseRedisInfo(info)

	// Calculate memory usage percentage
	if usedMemory, ok := infoMap["used_memory"]; ok {
		if maxMemory, ok := infoMap["maxmemory"]; ok && maxMemory > 0 {
			metrics.MemoryUsage = float64(usedMemory) / float64(maxMemory) * 100
		}
	}

	// Calculate connections usage
	if connectedClients, ok := infoMap["connected_clients"]; ok {
		if maxClients, ok := infoMap["maxclients"]; ok && maxClients > 0 {
			metrics.ConnectionsUsage = float64(connectedClients) / float64(maxClients) * 100
		}
	}

	// Calculate keyspace hit ratio
	if hits, hitsOk := infoMap["keyspace_hits"]; hitsOk {
		if misses, missesOk := infoMap["keyspace_misses"]; missesOk {
			total := hits + misses
			if total > 0 {
				metrics.KeyspaceHitRatio = float64(hits) / float64(total)
			}
		}
	}

	// Calculate throughput (approximate)
	if totalCommands, ok := infoMap["total_commands_processed"]; ok {
		if uptime, ok := infoMap["uptime_in_seconds"]; ok && uptime > 0 {
			metrics.Throughput = float64(totalCommands) / float64(uptime)
		}
	}

	return metrics
}

// isPerformanceHealthy checks if performance metrics are within healthy thresholds
func (hm *HealthMonitor) isPerformanceHealthy(metrics PerformanceMetrics) bool {
	thresholds := hm.config.PerformanceThresholds

	if metrics.Latency > thresholds.MaxLatency {
		return false
	}

	if metrics.MemoryUsage > thresholds.MaxMemoryUsage {
		return false
	}

	if metrics.ConnectionsUsage > thresholds.MaxConnectionsUsage {
		return false
	}

	if metrics.CPUUsage > thresholds.MaxCPUUsage {
		return false
	}

	if metrics.KeyspaceHitRatio < thresholds.MaxKeyspaceHitRatio {
		return false
	}

	return true
}

// generateAlertsForNode generates alerts based on node health and performance
func (hm *HealthMonitor) generateAlertsForNode(node *NodeHealth) []HealthAlert {
	var alerts []HealthAlert
	now := time.Now()

	// Check for consecutive failures
	if node.ConsecutiveFailures >= hm.config.AlertThresholds.ConsecutiveFailures {
		alerts = append(alerts, HealthAlert{
			ID:        fmt.Sprintf("consecutive_failures_%s_%d", node.NodeID, now.Unix()),
			Type:      "consecutive_failures",
			Severity:  "high",
			Message:   fmt.Sprintf("Node has %d consecutive failures", node.ConsecutiveFailures),
			Timestamp: now,
			Metadata: map[string]interface{}{
				"node_id":   node.NodeID,
				"failures":  node.ConsecutiveFailures,
				"threshold": hm.config.AlertThresholds.ConsecutiveFailures,
			},
		})
	}

	// Check performance alerts
	if node.Performance.Latency > hm.config.AlertThresholds.LatencyP95 {
		alerts = append(alerts, HealthAlert{
			ID:       fmt.Sprintf("high_latency_%s_%d", node.NodeID, now.Unix()),
			Type:     "high_latency",
			Severity: "medium",
			Message: fmt.Sprintf(
				"Node latency is %v, exceeds threshold %v",
				node.Performance.Latency,
				hm.config.AlertThresholds.LatencyP95,
			),
			Timestamp: now,
			Metadata: map[string]interface{}{
				"node_id":   node.NodeID,
				"latency":   node.Performance.Latency.String(),
				"threshold": hm.config.AlertThresholds.LatencyP95.String(),
			},
		})
	}

	if node.Performance.MemoryUsage > hm.config.AlertThresholds.MemoryUsagePercent {
		alerts = append(alerts, HealthAlert{
			ID:       fmt.Sprintf("high_memory_%s_%d", node.NodeID, now.Unix()),
			Type:     "high_memory_usage",
			Severity: "high",
			Message: fmt.Sprintf(
				"Node memory usage is %.2f%%, exceeds threshold %.2f%%",
				node.Performance.MemoryUsage,
				hm.config.AlertThresholds.MemoryUsagePercent,
			),
			Timestamp: now,
			Metadata: map[string]interface{}{
				"node_id":      node.NodeID,
				"memory_usage": node.Performance.MemoryUsage,
				"threshold":    hm.config.AlertThresholds.MemoryUsagePercent,
			},
		})
	}

	return alerts
}

// handleAutoFailover handles automatic failover scenarios
func (hm *HealthMonitor) handleAutoFailover(failedNodeID, reason string) {
	hm.failoverMutex.Lock()
	defer hm.failoverMutex.Unlock()

	// Check failover cooldown
	if time.Since(hm.lastFailover) < hm.config.FailoverCooldown {
		hm.logger.Warn("Skipping failover due to cooldown period",
			zap.String("failed_node", failedNodeID),
			zap.String("reason", reason),
			zap.Duration("time_since_last", time.Since(hm.lastFailover)),
		)
		return
	}

	hm.logger.Warn("Initiating automatic failover",
		zap.String("failed_node", failedNodeID),
		zap.String("reason", reason),
	)

	if err := hm.performFailover(reason, "automatic"); err != nil {
		hm.logger.Error("Automatic failover failed",
			zap.String("failed_node", failedNodeID),
			zap.Error(err),
		)
	}
}

// performFailover executes the failover process
func (hm *HealthMonitor) performFailover(reason, failoverType string) error {
	start := time.Now()
	currentNodeID := generateNodeID(hm.activeClient)

	// Find the best replica to failover to
	bestReplica := hm.findBestReplica()
	if bestReplica == nil {
		return fmt.Errorf("no healthy replica found for failover")
	}

	newNodeID := generateNodeID(bestReplica)

	// Perform the failover
	hm.healthMutex.Lock()
	hm.activeClient = bestReplica
	hm.lastFailover = time.Now()
	hm.healthMutex.Unlock()

	duration := time.Since(start)

	// Record failover event
	failoverEvent := FailoverEvent{
		Timestamp: start,
		Type:      failoverType,
		FromNode:  currentNodeID,
		ToNode:    newNodeID,
		Reason:    reason,
		Duration:  duration,
		Success:   true,
	}

	hm.healthMutex.Lock()
	hm.clusterHealth.FailoverEvents = append(hm.clusterHealth.FailoverEvents, failoverEvent)
	hm.healthMutex.Unlock()

	hm.logger.Info("Failover completed successfully",
		zap.String("from_node", currentNodeID),
		zap.String("to_node", newNodeID),
		zap.String("reason", reason),
		zap.String("type", failoverType),
		zap.Duration("duration", duration),
	)

	return nil
}

// findBestReplica finds the healthiest replica for failover
func (hm *HealthMonitor) findBestReplica() redis.UniversalClient {
	hm.healthMutex.RLock()
	defer hm.healthMutex.RUnlock()

	var bestClient redis.UniversalClient
	var bestScore float64 = -1

	for _, nodeID := range hm.clusterHealth.SlaveNodes {
		node := hm.clusterHealth.Nodes[nodeID]
		if node.Status == HealthStatusHealthy {
			client := hm.getClientForNode(nodeID)
			if client != nil {
				// Calculate health score
				score := hm.calculateHealthScore(node)
				if score > bestScore {
					bestScore = score
					bestClient = client
				}
			}
		}
	}

	return bestClient
}

// calculateHealthScore calculates a health score for a node
func (hm *HealthMonitor) calculateHealthScore(node *NodeHealth) float64 {
	score := 100.0 // Start with perfect score

	// Deduct points for consecutive failures
	score -= float64(node.ConsecutiveFailures) * 10

	// Deduct points for high latency
	if node.Performance.Latency > hm.config.PerformanceThresholds.MaxLatency {
		latencyRatio := float64(
			node.Performance.Latency,
		) / float64(
			hm.config.PerformanceThresholds.MaxLatency,
		)
		score -= (latencyRatio - 1) * 20
	}

	// Deduct points for high memory usage
	if node.Performance.MemoryUsage > hm.config.PerformanceThresholds.MaxMemoryUsage {
		memoryPenalty := (node.Performance.MemoryUsage - hm.config.PerformanceThresholds.MaxMemoryUsage) / 10
		score -= memoryPenalty
	}

	// Deduct points for low hit ratio
	if node.Performance.KeyspaceHitRatio < hm.config.PerformanceThresholds.MaxKeyspaceHitRatio {
		hitRatioPenalty := (hm.config.PerformanceThresholds.MaxKeyspaceHitRatio - node.Performance.KeyspaceHitRatio) * 50
		score -= hitRatioPenalty
	}

	if score < 0 {
		score = 0
	}

	return score
}

// getClientForNode returns the Redis client for a specific node ID
func (hm *HealthMonitor) getClientForNode(nodeID string) redis.UniversalClient {
	if nodeID == generateNodeID(hm.primaryClient) {
		return hm.primaryClient
	}

	for _, replica := range hm.replicaClients {
		if nodeID == generateNodeID(replica) {
			return replica
		}
	}

	return nil
}

// cleanupOldMetrics removes old metrics data
func (hm *HealthMonitor) cleanupOldMetrics() {
	hm.metricsMutex.Lock()
	defer hm.metricsMutex.Unlock()

	cutoff := time.Now().Add(-hm.config.MetricsRetentionPeriod)

	// Clean up failure history for all nodes
	hm.healthMutex.Lock()
	defer hm.healthMutex.Unlock()

	for _, node := range hm.clusterHealth.Nodes {
		var recentFailures []HealthCheckFailure
		for _, failure := range node.FailureHistory {
			if failure.Timestamp.After(cutoff) {
				recentFailures = append(recentFailures, failure)
			}
		}
		node.FailureHistory = recentFailures
	}

	// Clean up old failover events
	var recentEvents []FailoverEvent
	for _, event := range hm.clusterHealth.FailoverEvents {
		if event.Timestamp.After(cutoff) {
			recentEvents = append(recentEvents, event)
		}
	}
	hm.clusterHealth.FailoverEvents = recentEvents
}

// Stop gracefully stops the health monitor
func (hm *HealthMonitor) Stop() {
	hm.logger.Info("Stopping Redis health monitor")
	hm.cancel()
	hm.wg.Wait()
	hm.logger.Info("Redis health monitor stopped")
}

// Helper functions

func generateClusterID() string {
	return fmt.Sprintf("cluster_%d", time.Now().Unix())
}

func generateNodeID(client redis.UniversalClient) string {
	return fmt.Sprintf("node_%s_%d", getClientAddress(client), time.Now().Unix())
}

func getClientAddress(client redis.UniversalClient) string {
	// This is a simplified implementation - in real usage, you'd extract the actual address
	return fmt.Sprintf("redis_%p", client)
}

func parseRedisInfo(info string) map[string]int64 {
	result := make(map[string]int64)
	// This is a simplified parser - in real implementation, you'd parse the actual INFO output
	// Format: key:value\r\n
	return result
}
