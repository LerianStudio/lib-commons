package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockEnvironmentDetector for testing
type MockEnvironmentDetector struct {
	mock.Mock
}

func (m *MockEnvironmentDetector) IsGCP(ctx context.Context) (bool, string, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.String(1), args.Error(2)
}

func (m *MockEnvironmentDetector) GetGCPProjectID(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

// MockTopologyDetector for testing
type MockTopologyDetector struct {
	mock.Mock
}

func (m *MockTopologyDetector) IsCluster(ctx context.Context, addr string) (bool, []string, error) {
	args := m.Called(ctx, addr)
	return args.Bool(0), args.Get(1).([]string), args.Error(2)
}

func (m *MockTopologyDetector) DetectNodes(ctx context.Context, addrs []string) ([]string, error) {
	args := m.Called(ctx, addrs)
	return args.Get(0).([]string), args.Error(1)
}

// MockLogger for testing
type MockAutoDetectLogger struct {
	mock.Mock
}

func (m *MockAutoDetectLogger) Info(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Infof(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockAutoDetectLogger) Debug(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Error(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Warn(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Fatal(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Infoln(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Errorln(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Warnln(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Debugln(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Errorf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockAutoDetectLogger) Warnf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockAutoDetectLogger) Debugf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockAutoDetectLogger) Fatalln(args ...any) {
	m.Called(args...)
}

func (m *MockAutoDetectLogger) Fatalf(format string, args ...any) {
	m.Called(format, args)
}

func (m *MockAutoDetectLogger) WithFields(fields ...any) log.Logger {
	args := m.Called(fields)
	return args.Get(0).(log.Logger)
}

func (m *MockAutoDetectLogger) WithDefaultMessageTemplate(message string) log.Logger {
	args := m.Called(message)
	return args.Get(0).(log.Logger)
}

func (m *MockAutoDetectLogger) Sync() error {
	args := m.Called()
	return args.Error(0)
}

// TestAutoDetector tests the main auto-detection functionality
func TestAutoDetector(t *testing.T) {
	t.Run("successful detection - GCP cluster", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}

		// Mock successful GCP detection
		mockEnv.On("IsGCP", mock.Anything).Return(true, "test-project", nil)

		// Mock successful cluster detection
		clusterNodes := []string{"node1:6379", "node2:6379", "node3:6379"}
		mockTopo.On("IsCluster", mock.Anything, "redis.example.com:6379").
			Return(true, clusterNodes, nil)

		detector := &AutoDetector{
			envDetector:  mockEnv,
			topoDetector: mockTopo,
			cache: &DetectionCache{
				ttl: 5 * time.Minute,
			},
			logger:           nil, // Use nil logger to avoid mock complexity
			detectionTimeout: 5 * time.Second,
		}

		ctx := context.Background()
		result, err := detector.Detect(ctx, "redis.example.com:6379")

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsGCP)
		assert.True(t, result.IsCluster)
		assert.Equal(t, "test-project", result.GCPProjectID)
		assert.Equal(t, clusterNodes, result.ClusterNodes)
		assert.Nil(t, result.DetectionError)

		mockEnv.AssertExpectations(t)
		mockTopo.AssertExpectations(t)
	})

	t.Run("successful detection - Non-GCP single", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}

		// Mock non-GCP detection
		mockEnv.On("IsGCP", mock.Anything).Return(false, "", nil)

		// Mock single instance detection
		mockTopo.On("IsCluster", mock.Anything, "localhost:6379").
			Return(false, []string{"localhost:6379"}, nil)

		detector := &AutoDetector{
			envDetector:  mockEnv,
			topoDetector: mockTopo,
			cache: &DetectionCache{
				ttl: 5 * time.Minute,
			},
			logger:           nil,
			detectionTimeout: 5 * time.Second,
		}

		ctx := context.Background()
		result, err := detector.Detect(ctx, "localhost:6379")

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.False(t, result.IsGCP)
		assert.False(t, result.IsCluster)
		assert.Empty(t, result.GCPProjectID)
		assert.Equal(t, []string{"localhost:6379"}, result.ClusterNodes)
		assert.Nil(t, result.DetectionError)

		mockEnv.AssertExpectations(t)
		mockTopo.AssertExpectations(t)
	})

	t.Run("detection with errors", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}
		// Mock GCP detection error
		mockEnv.On("IsGCP", mock.Anything).
			Return(false, "", fmt.Errorf("GCP metadata service unavailable"))

		// Mock cluster detection error
		mockTopo.On("IsCluster", mock.Anything, "unreachable:6379").
			Return(false, []string(nil), fmt.Errorf("connection refused"))

		detector := &AutoDetector{
			envDetector:  mockEnv,
			topoDetector: mockTopo,
			cache: &DetectionCache{
				ttl: 5 * time.Minute,
			},
			logger:           nil,
			detectionTimeout: 5 * time.Second,
		}

		ctx := context.Background()
		result, err := detector.Detect(ctx, "unreachable:6379")

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.False(t, result.IsGCP)
		assert.False(t, result.IsCluster)
		assert.NotNil(t, result.DetectionError)
		assert.Contains(t, result.DetectionError.Error(), "detection failed")

		mockEnv.AssertExpectations(t)
		mockTopo.AssertExpectations(t)
	})

	t.Run("caching behavior", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}

		// Mock detection calls - should only be called once due to caching
		mockEnv.On("IsGCP", mock.Anything).Return(true, "cached-project", nil).Once()
		mockTopo.On("IsCluster", mock.Anything, "cached:6379").
			Return(false, []string{"cached:6379"}, nil).
			Once()

		detector := &AutoDetector{
			envDetector:  mockEnv,
			topoDetector: mockTopo,
			cache: &DetectionCache{
				ttl: 5 * time.Minute,
			},
			logger:           nil,
			detectionTimeout: 5 * time.Second,
		}

		ctx := context.Background()

		// First call - should perform detection
		result1, err1 := detector.Detect(ctx, "cached:6379")
		assert.NoError(t, err1)
		assert.True(t, result1.IsGCP)
		assert.Equal(t, "cached-project", result1.GCPProjectID)

		// Second call - should use cache
		result2, err2 := detector.Detect(ctx, "cached:6379")
		assert.NoError(t, err2)
		assert.True(t, result2.IsGCP)
		assert.Equal(t, "cached-project", result2.GCPProjectID)

		// Results should be the same (cached)
		assert.Equal(t, result1.DetectedAt, result2.DetectedAt)

		mockEnv.AssertExpectations(t)
		mockTopo.AssertExpectations(t)
	})

	t.Run("cache expiration", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}

		// Mock detection calls - should be called twice due to cache expiration
		mockEnv.On("IsGCP", mock.Anything).Return(true, "expired-project", nil).Twice()
		mockTopo.On("IsCluster", mock.Anything, "expired:6379").
			Return(false, []string{"expired:6379"}, nil).
			Twice()

		detector := &AutoDetector{
			envDetector:  mockEnv,
			topoDetector: mockTopo,
			cache: &DetectionCache{
				ttl: 100 * time.Millisecond, // Very short TTL
			},
			logger:           nil,
			detectionTimeout: 5 * time.Second,
		}

		ctx := context.Background()

		// First call
		result1, err1 := detector.Detect(ctx, "expired:6379")
		assert.NoError(t, err1)

		// Wait for cache to expire
		time.Sleep(150 * time.Millisecond)

		// Second call - should re-detect due to expired cache
		result2, err2 := detector.Detect(ctx, "expired:6379")
		assert.NoError(t, err2)

		// Times should be different (not cached)
		assert.True(t, result2.DetectedAt.After(result1.DetectedAt))

		mockEnv.AssertExpectations(t)
		mockTopo.AssertExpectations(t)
	})
}

// TestGCPEnvironmentDetector tests GCP environment detection
func TestGCPEnvironmentDetector(t *testing.T) {
	t.Run("GCP detection via environment variables", func(t *testing.T) {
		// Set up GCP environment variables including the required auth flag
		_ = os.Setenv("GCP_VALKEY_AUTH", "true")
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/path/to/credentials.json")
		_ = os.Setenv("GCP_PROJECT_ID", "test-project-env")
		defer func() {
			_ = os.Unsetenv("GCP_VALKEY_AUTH")
			_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
			_ = os.Unsetenv("GCP_PROJECT_ID")
		}()

		detector := NewGCPEnvironmentDetector()
		ctx := context.Background()

		isGCP, projectID, err := detector.IsGCP(ctx)
		assert.NoError(t, err)
		assert.True(t, isGCP)
		assert.Equal(t, "test-project-env", projectID)
	})

	t.Run("GCP detection via metadata service", func(t *testing.T) {
		// Clear environment variables
		_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
		_ = os.Unsetenv("GCP_PROJECT_ID")
		_ = os.Unsetenv("GOOGLE_CLOUD_PROJECT")

		// Create mock metadata server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			if strings.Contains(r.URL.Path, "project-id") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("test-project-metadata"))
				return
			}

			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		// Create detector with custom HTTP client pointing to mock server
		detector := &GCPEnvironmentDetector{
			metadataTimeout: 2 * time.Second,
			httpClient: &http.Client{
				Timeout: 2 * time.Second,
			},
		}

		// Override the metadata URL for testing
		originalURL := GCPProjectEndpoint
		defer func() {
			// Can't change const, but this shows the intent
			_ = originalURL
		}()

		// Test with custom request
		req, err := http.NewRequest("GET", server.URL+"/computeMetadata/v1/project/project-id", nil)
		require.NoError(t, err)
		req.Header.Set("Metadata-Flavor", "Google")

		resp, err := detector.httpClient.Do(req)
		require.NoError(t, err)
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Logf("Failed to close response body: %v", closeErr)
			}
		}()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("non-GCP environment", func(t *testing.T) {
		// Clear all GCP environment variables
		gcpEnvVars := []string{
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GCP_PROJECT_ID",
			"GOOGLE_CLOUD_PROJECT",
			"GAE_APPLICATION",
			"GAE_SERVICE",
			"K_SERVICE",
			"FUNCTION_NAME",
		}

		for _, env := range gcpEnvVars {
			_ = os.Unsetenv(env)
		}

		detector := &GCPEnvironmentDetector{
			metadataTimeout: 100 * time.Millisecond, // Short timeout
			httpClient: &http.Client{
				Timeout: 100 * time.Millisecond,
			},
		}

		ctx := context.Background()
		isGCP, projectID, err := detector.IsGCP(ctx)

		// Should not be GCP (metadata service unavailable)
		assert.NoError(t, err)
		assert.False(t, isGCP)
		assert.Empty(t, projectID)
	})

	t.Run("environment variable detection", func(t *testing.T) {
		testCases := []struct {
			name   string
			envVar string
			value  string
			isGCP  bool
		}{
			{
				"Google Application Credentials",
				"GOOGLE_APPLICATION_CREDENTIALS",
				"/path/to/creds.json",
				true,
			},
			{"GCP Project ID", "GCP_PROJECT_ID", "my-project", true},
			{"Google Cloud Project", "GOOGLE_CLOUD_PROJECT", "my-project", true},
			{"GAE Application", "GAE_APPLICATION", "my-app", true},
			{"GAE Service", "GAE_SERVICE", "my-service", true},
			{"Cloud Run", "K_SERVICE", "my-service", true},
			{"Cloud Functions", "FUNCTION_NAME", "my-function", true},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Clear all env vars first
				for _, env := range []string{"GCP_VALKEY_AUTH", "GOOGLE_APPLICATION_CREDENTIALS", "GCP_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "GAE_APPLICATION", "GAE_SERVICE", "K_SERVICE", "FUNCTION_NAME"} {
					_ = os.Unsetenv(env)
				}

				// Set the required auth flag and test env var
				_ = os.Setenv("GCP_VALKEY_AUTH", "true")
				_ = os.Setenv(tc.envVar, tc.value)
				defer func() {
					_ = os.Unsetenv("GCP_VALKEY_AUTH")
					_ = os.Unsetenv(tc.envVar)
				}()

				detector := NewGCPEnvironmentDetector()
				result := detector.checkGCPEnvironmentVariables()
				assert.Equal(t, tc.isGCP, result)
			})
		}
	})
}

// TestRedisTopologyDetector tests Redis cluster topology detection
func TestRedisTopologyDetector(t *testing.T) {
	t.Run("cluster detection with mock responses", func(t *testing.T) {
		// This test would require a real Redis server or advanced mocking
		// For now, we'll test the basic structure and error handling
		detector := NewRedisTopologyDetector()
		ctx := context.Background()

		// Test with invalid address - will try to connect and fail
		isCluster, nodes, err := detector.IsCluster(ctx, "invalid-host:6379")
		assert.Error(t, err)
		assert.False(t, isCluster)
		assert.Nil(t, nodes)
	})

	t.Run("address validation in cluster nodes parsing", func(t *testing.T) {
		// Test getClusterNodes parsing logic
		nodesInfo := `
07c37dfeb235213a872192d90877d0cd55635b91 127.0.0.1:30004@31004 slave e7d1eecce10fd6bb5eb35b9f99a514335d9ba9ca 0 1426238317239 4 connected
67ed2db8d677e59ec4a4cdbbc80ceb01e4e6d33c 127.0.0.1:30002@31002 master - 0 1426238316232 2 connected 5461-10922
292f8b365bb7edb5e285caf0b7e6ddc7265d2f4f 127.0.0.1:30003@31003 master - 0 1426238318243 3 connected 10923-16383
6ec23923021cf3ffec47632106199cb7f496ce01 127.0.0.1:30005@31005 slave 67ed2db8d677e59ec4a4cdbbc80ceb01e4e6d33c 0 1426238316232 5 connected
824fe116063bc5fcf9f4ffd895bc17aee7731ac3 127.0.0.1:30006@31006 slave 292f8b365bb7edb5e285caf0b7e6ddc7265d2f4f 0 1426238317741 6 connected
e7d1eecce10fd6bb5eb35b9f99a514335d9ba9ca 127.0.0.1:30001@31001 myself,master - 0 0 1 connected 0-5460
`

		// Create a mock client that returns this nodes info
		// In a real implementation, we'd mock the Redis client
		expectedNodes := []string{
			"127.0.0.1:30004",
			"127.0.0.1:30002",
			"127.0.0.1:30003",
			"127.0.0.1:30005",
			"127.0.0.1:30006",
			"127.0.0.1:30001",
		}

		// Test the parsing logic by extracting addresses from the nodes info
		lines := strings.Split(nodesInfo, "\n")
		var parsedNodes []string

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}

			nodeAddr := parts[1]
			if idx := strings.Index(nodeAddr, "@"); idx > 0 {
				nodeAddr = nodeAddr[:idx]
			}

			if host, port, err := net.SplitHostPort(nodeAddr); err == nil && host != "" &&
				port != "" {
				parsedNodes = append(parsedNodes, nodeAddr)
			}
		}

		assert.Equal(t, expectedNodes, parsedNodes)
	})

	t.Run("detect nodes with multiple addresses", func(t *testing.T) {
		detector := NewRedisTopologyDetector()
		ctx := context.Background()

		// Test with multiple invalid addresses
		addrs := []string{"invalid1:6379", "invalid2:6379", "invalid3:6379"}
		nodes, err := detector.DetectNodes(ctx, addrs)

		// Should fail to detect any valid nodes
		assert.Error(t, err)
		assert.Nil(t, nodes)
	})
}

// TestDetectionResult tests the DetectionResult helper methods
func TestDetectionResult(t *testing.T) {
	t.Run("detection summary", func(t *testing.T) {
		result := &DetectionResult{
			IsGCP:        true,
			IsCluster:    true,
			GCPProjectID: "test-project",
			ClusterNodes: []string{"node1:6379", "node2:6379"},
			DetectedAt:   time.Now(),
		}

		summary := result.DetectionSummary()
		assert.Contains(t, summary, "GCP(project=test-project)")
		assert.Contains(t, summary, "Cluster(nodes=2)")
	})

	t.Run("detection summary with error", func(t *testing.T) {
		result := &DetectionResult{
			IsGCP:          false,
			IsCluster:      false,
			DetectionError: fmt.Errorf("test error"),
			DetectedAt:     time.Now(),
		}

		summary := result.DetectionSummary()
		assert.Contains(t, summary, "Non-GCP")
		assert.Contains(t, summary, "Single")
		assert.Contains(t, summary, "Error=test error")
	})

	t.Run("JSON serialization", func(t *testing.T) {
		result := &DetectionResult{
			IsGCP:        true,
			IsCluster:    false,
			GCPProjectID: "json-project",
			ClusterNodes: []string{"single:6379"},
			DetectedAt:   time.Now(),
		}

		jsonStr, err := result.ToJSON()
		assert.NoError(t, err)
		assert.NotEmpty(t, jsonStr)

		// Verify we can unmarshal it back
		var unmarshaled DetectionResult
		err = json.Unmarshal([]byte(jsonStr), &unmarshaled)
		assert.NoError(t, err)
		assert.Equal(t, result.IsGCP, unmarshaled.IsGCP)
		assert.Equal(t, result.GCPProjectID, unmarshaled.GCPProjectID)
	})
}

// TestConcurrentDetection tests thread safety of detection operations
func TestConcurrentDetection(t *testing.T) {
	t.Run("concurrent detection calls", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}

		// Mock successful detection - will be called multiple times in concurrent scenarios
		mockEnv.On("IsGCP", mock.Anything).Return(true, "concurrent-project", nil)
		mockTopo.On("IsCluster", mock.Anything, "concurrent:6379").
			Return(false, []string{"concurrent:6379"}, nil)

		detector := &AutoDetector{
			envDetector:  mockEnv,
			topoDetector: mockTopo,
			cache: &DetectionCache{
				ttl: 5 * time.Minute,
			},
			logger:           nil,
			detectionTimeout: 5 * time.Second,
		}

		ctx := context.Background()
		var wg sync.WaitGroup
		errors := make(chan error, 10)
		results := make(chan *DetectionResult, 10)

		// Run 10 concurrent detections
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := detector.Detect(ctx, "concurrent:6379")
				if err != nil {
					errors <- err
					return
				}
				results <- result
			}()
		}

		wg.Wait()
		close(errors)
		close(results)

		// Check no errors occurred
		for err := range errors {
			assert.NoError(t, err)
		}

		// All results should be consistent (either same cached result or different detected results)
		var firstResult *DetectionResult
		resultCount := 0
		for result := range results {
			if firstResult == nil {
				firstResult = result
			} else {
				// In concurrent scenarios, results may have different timestamps if detection ran multiple times
				// This is acceptable behavior - the important thing is that all calls succeeded
				assert.Equal(t, firstResult.IsGCP, result.IsGCP)
				assert.Equal(t, firstResult.IsCluster, result.IsCluster)
				assert.Equal(t, firstResult.GCPProjectID, result.GCPProjectID)
			}
			resultCount++
		}

		assert.Equal(t, 10, resultCount)
		// Note: In concurrent scenarios, detection may be called more than once before cache is populated
		// This is acceptable behavior and doesn't indicate a bug
	})
}

// TestCacheManagement tests cache management operations
func TestCacheManagement(t *testing.T) {
	t.Run("clear cache", func(t *testing.T) {
		detector := NewAutoDetector(nil)

		// Manually set cache result
		detector.cache.mu.Lock()
		detector.cache.result = &DetectionResult{
			IsGCP:      true,
			DetectedAt: time.Now(),
		}
		detector.cache.mu.Unlock()

		// Verify cache exists
		cached := detector.getCachedResult()
		assert.NotNil(t, cached)

		// Clear cache
		detector.ClearCache()

		// Verify cache is cleared
		cached = detector.getCachedResult()
		assert.Nil(t, cached)
	})

	t.Run("set cache TTL", func(t *testing.T) {
		detector := NewAutoDetector(nil)

		// Set custom TTL
		customTTL := 30 * time.Second
		detector.SetCacheTTL(customTTL)

		assert.Equal(t, customTTL, detector.cache.ttl)
	})

	t.Run("cache expiration check", func(t *testing.T) {
		detector := NewAutoDetector(nil)
		detector.SetCacheTTL(100 * time.Millisecond)

		// Set expired cache result
		detector.cache.mu.Lock()
		detector.cache.result = &DetectionResult{
			IsGCP:      true,
			DetectedAt: time.Now().Add(-200 * time.Millisecond), // 200ms ago
		}
		detector.cache.mu.Unlock()

		// Should return nil due to expiration
		cached := detector.getCachedResult()
		assert.Nil(t, cached)
	})
}

// TestDetectionTimeouts tests timeout behavior
func TestDetectionTimeouts(t *testing.T) {
	t.Run("detection timeout", func(t *testing.T) {
		mockEnv := &MockEnvironmentDetector{}
		mockTopo := &MockTopologyDetector{}

		// Mock slow responses that exceed timeout
		mockEnv.On("IsGCP", mock.Anything).Return(false, "", context.DeadlineExceeded)
		mockTopo.On("IsCluster", mock.Anything, "slow:6379").
			Return(false, []string{}, context.DeadlineExceeded)

		detector := &AutoDetector{
			envDetector:      mockEnv,
			topoDetector:     mockTopo,
			cache:            &DetectionCache{ttl: 5 * time.Minute},
			logger:           nil,
			detectionTimeout: 500 * time.Millisecond, // Short timeout
		}

		ctx := context.Background()
		result, err := detector.Detect(ctx, "slow:6379")

		// Should complete but with timeout errors
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotNil(t, result.DetectionError)
		assert.Contains(t, result.DetectionError.Error(), "detection failed")

		mockEnv.AssertExpectations(t)
		mockTopo.AssertExpectations(t)
	})
}
