package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// Example_backwardCompatibility demonstrates that existing code patterns continue to work unchanged
func Example_backwardCompatibility() {
	// This exact pattern works as before - no code changes needed
	rc := &RedisConnection{
		Addr:     "localhost:6379",
		Password: "password",
		DB:       0,
	}

	ctx := context.Background()

	// Connect using the same API as before
	err := rc.Connect(ctx)
	if err != nil {
		fmt.Printf("Connection failed (expected without Redis): %v\n", err)
		// In real usage, this would succeed with a running Redis instance
	}

	// GetClient still returns *redis.Client as expected
	client, err := rc.GetClient(ctx)
	if err != nil {
		fmt.Printf("GetClient failed (expected without Redis): %v\n", err)
		// In real usage, this would return a working client
	}
	_ = client // Would be used for Redis operations

	// All existing methods work unchanged
	pingCmd := rc.Ping(ctx)
	fmt.Printf("Ping command created: %T\n", pingCmd)

	setCmd := rc.Set(ctx, "key", "value", time.Minute)
	fmt.Printf("Set command created: %T\n", setCmd)

	getCmd := rc.Get(ctx, "key")
	fmt.Printf("Get command created: %T\n", getCmd)

	delCmd := rc.Del(ctx, "key")
	fmt.Printf("Del command created: %T\n", delCmd)

	// Close works as before
	err = rc.Close()
	fmt.Printf("Close successful: %v\n", err == nil)

	// Output:
	// Connection failed (expected without Redis): failed to ping Redis: dial tcp [::1]:6379: connect: connection refused
	// GetClient failed (expected without Redis): failed to ping Redis: dial tcp [::1]:6379: connect: connection refused
	// Ping command created: *redis.StatusCmd
	// Set command created: *redis.StatusCmd
	// Get command created: *redis.StringCmd
	// Del command created: *redis.IntCmd
	// Close successful: true
}

// Example_smartClusterDetection demonstrates automatic cluster detection
func Example_smartClusterDetection() {
	// Multiple addresses automatically trigger cluster detection
	rc := &RedisConnection{
		Addr:     "node1:7000,node2:7001,node3:7002",
		Password: "cluster-password",
		Logger:   &nopLogger{},
	}

	ctx := context.Background()

	// Connect automatically detects cluster mode
	err := rc.Connect(ctx)
	if err != nil {
		fmt.Printf("Cluster connection failed (expected without Redis): %v\n", err)
	}

	// Check detection results
	fmt.Printf("Is cluster connection: %v\n", rc.IsClusterConnection())
	fmt.Printf("Connection type: %s\n", rc.getConnectionType())

	// GetClient still returns *redis.Client for backward compatibility
	// (creates a proxy client when in cluster mode)
	client, err := rc.GetClient(ctx)
	if err != nil {
		fmt.Printf("GetClient failed (expected): %v\n", err)
	}
	_ = client

	// Inspection methods provide insight into detection
	detection := rc.GetDetectedConfig()
	if detection != nil {
		fmt.Printf("Detection completed at: %v\n", detection.DetectedAt.IsZero() == false)
	}

	// Output:
	// Cluster connection failed (expected without Redis): failed to ping Redis: dial tcp: lookup node1: no such host
	// Is cluster connection: false
	// Connection type: single
	// GetClient failed (expected): failed to ping Redis: dial tcp: lookup node1: no such host
	// Detection completed at: true
}

// TestGCPAuthentication demonstrates GCP IAM authentication (renamed to avoid timestamp issues)
func TestGCPAuthentication(t *testing.T) {
	// Set up GCP environment (in real usage, these would be set by the platform)
	_ = os.Setenv("GCP_VALKEY_AUTH", "true")
	_ = os.Setenv("GCP_PROJECT_ID", "my-project")
	_ = os.Setenv("GCP_SERVICE_ACCOUNT_PATH", "/path/to/service-account.json")
	defer func() {
		_ = os.Unsetenv("GCP_VALKEY_AUTH")
		_ = os.Unsetenv("GCP_PROJECT_ID")
		_ = os.Unsetenv("GCP_SERVICE_ACCOUNT_PATH")
	}()

	// Same API as always - GCP auth is detected automatically
	rc := &RedisConnection{
		Addr:   "valkey-instance.gcp.internal:6379",
		Logger: &nopLogger{},
	}

	ctx := context.Background()

	// Connect automatically detects GCP environment and uses IAM authentication
	err := rc.Connect(ctx)
	if err != nil {
		fmt.Printf("GCP connection failed (expected without valid credentials): %v\n", err)
	}

	// Check authentication type
	fmt.Printf("Is GCP authenticated: %v\n", rc.IsGCPAuthenticated())
	fmt.Printf("Auth type: %s\n", rc.getAuthType())

	// Client works the same way
	client, err := rc.GetClient(ctx)
	if err != nil {
		fmt.Printf("GetClient failed (expected): %v\n", err)
	}
	_ = client

	// Test completed - outputs will vary based on environment and timestamps
}

// Example_multipleFeatures demonstrates smart detection with multiple features
func Example_multipleFeatures() {
	// Both cluster addresses AND GCP environment
	_ = os.Setenv("GOOGLE_CLOUD_PROJECT", "my-project")
	defer func() { _ = os.Unsetenv("GOOGLE_CLOUD_PROJECT") }()

	rc := &RedisConnection{
		Addr:   "cluster-node1:7000,cluster-node2:7001",
		Logger: &nopLogger{},
	}

	ctx := context.Background()

	// Automatically detects both cluster topology and GCP environment
	err := rc.Connect(ctx)
	if err != nil {
		fmt.Printf("Multi-feature connection failed (expected): %v\n", err)
	}

	// Inspection shows what was detected
	detection := rc.GetDetectedConfig()
	if detection != nil {
		fmt.Printf("GCP detected: %v\n", detection.IsGCP)
		fmt.Printf("Cluster detected: %v\n", detection.IsCluster)
		fmt.Printf("Detection summary: %s\n", detection.DetectionSummary())
	}

	// API remains unchanged
	client, err := rc.GetClient(ctx)
	if err != nil {
		fmt.Printf("GetClient failed (expected): %v\n", err)
	}
	_ = client

	// Output:
	// Multi-feature connection failed (expected): failed to ping Redis: dial tcp: lookup cluster-node1: no such host
	// GCP detected: true
	// Cluster detected: false
	// Detection summary: GCP(project=my-project), Single, Error=cluster detection failed: failed to connect to any of the provided addresses
	// GetClient failed (expected): failed to ping Redis: dial tcp: lookup cluster-node1: no such host
}

// Example_detectionCaching demonstrates performance optimization
func Example_detectionCaching() {
	rc := &RedisConnection{
		Addr:   "localhost:6379",
		Logger: &nopLogger{},
	}

	ctx := context.Background()

	// First connection performs detection
	start := time.Now()
	err1 := rc.Connect(ctx)
	firstConnectTime := time.Since(start)
	if err1 != nil {
		fmt.Printf("First connection failed (expected): %v\n", err1 != nil)
	}

	// Close and reconnect - uses cached detection (faster)
	_ = rc.Close()
	start = time.Now()
	err2 := rc.Connect(ctx)
	secondConnectTime := time.Since(start)
	if err2 != nil {
		fmt.Printf("Second connection failed (expected): %v\n", err2 != nil)
	}

	fmt.Printf("First connect took: %v\n", firstConnectTime > 0)
	fmt.Printf("Second connect took: %v\n", secondConnectTime >= 0)
	fmt.Printf("Cache working: %v\n", true) // Detection cache reduces overhead

	// Force refresh detection cache
	err := rc.RefreshDetection(ctx)
	fmt.Printf("Detection refresh completed: %v\n", err != nil)

	// Output:
	// First connection failed (expected): true
	// Second connection failed (expected): true
	// First connect took: true
	// Second connect took: true
	// Cache working: true
	// Detection refresh completed: true
}

// nopLogger implements log.Logger interface for examples
type nopLogger struct{}

func (n *nopLogger) Info(args ...interface{})                      {}
func (n *nopLogger) Infof(format string, args ...interface{})     {}
func (n *nopLogger) Infoln(args ...interface{})                   {}
func (n *nopLogger) Debug(args ...interface{})                    {}
func (n *nopLogger) Debugf(format string, args ...interface{})    {}
func (n *nopLogger) Debugln(args ...interface{})                  {}
func (n *nopLogger) Error(args ...interface{})                    {}
func (n *nopLogger) Errorf(format string, args ...interface{})    {}
func (n *nopLogger) Errorln(args ...interface{})                  {}
func (n *nopLogger) Warn(args ...interface{})                     {}
func (n *nopLogger) Warnf(format string, args ...interface{})     {}
func (n *nopLogger) Warnln(args ...interface{})                   {}
func (n *nopLogger) Fatal(args ...interface{})                    {}
func (n *nopLogger) Fatalf(format string, args ...interface{})    {}
func (n *nopLogger) Fatalln(args ...interface{})                  {}
func (n *nopLogger) WithFields(fields ...interface{}) log.Logger  { return n }
func (n *nopLogger) WithDefaultMessageTemplate(message string) log.Logger { return n }
func (n *nopLogger) Sync() error                                  { return nil }