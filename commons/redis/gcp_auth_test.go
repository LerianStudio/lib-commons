package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// GCPMockLogger is a separate mock to avoid conflicts with existing MockLogger
type GCPMockLogger struct {
	mock.Mock
}

func (m *GCPMockLogger) Info(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Infof(format string, args ...any) {
	m.Called(format, args)
}

func (m *GCPMockLogger) Debug(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Error(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Warn(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Fatal(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Infoln(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Errorln(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Warnln(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Debugln(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Errorf(format string, args ...any) {
	m.Called(format, args)
}

func (m *GCPMockLogger) Warnf(format string, args ...any) {
	m.Called(format, args)
}

func (m *GCPMockLogger) Debugf(format string, args ...any) {
	m.Called(format, args)
}

func (m *GCPMockLogger) Fatalln(args ...any) {
	m.Called(args)
}

func (m *GCPMockLogger) Fatalf(format string, args ...any) {
	m.Called(format, args)
}

func (m *GCPMockLogger) WithFields(fields ...any) log.Logger {
	args := m.Called(fields)
	return args.Get(0).(log.Logger)
}

func (m *GCPMockLogger) WithDefaultMessageTemplate(message string) log.Logger {
	args := m.Called(message)
	return args.Get(0).(log.Logger)
}

func (m *GCPMockLogger) Sync() error {
	args := m.Called()
	return args.Error(0)
}

// GCPMockTokenProvider is a mock implementation of TokenProvider
type GCPMockTokenProvider struct {
	mock.Mock
}

func (m *GCPMockTokenProvider) GetToken(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

func (m *GCPMockTokenProvider) RefreshToken(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

func (m *GCPMockTokenProvider) IsTokenExpired() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *GCPMockTokenProvider) GetTokenTTL() time.Duration {
	args := m.Called()
	return args.Get(0).(time.Duration)
}

func (m *GCPMockTokenProvider) StartAutoRefresh(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *GCPMockTokenProvider) StopAutoRefresh() error {
	args := m.Called()
	return args.Error(0)
}

// TestGCPAuthConfig tests the GCP authentication configuration
func TestGCPAuthConfig(t *testing.T) {
	t.Run("ConfigFromEnv with valid environment variables", func(t *testing.T) {
		// Set up environment
		_ = os.Setenv("GCP_VALKEY_AUTH", "true")
		_ = os.Setenv("GCP_SERVICE_ACCOUNT_PATH", "/path/to/service-account.json")
		_ = os.Setenv("GCP_PROJECT_ID", "test-project")
		_ = os.Setenv("GCP_TOKEN_REFRESH_BUFFER", "5m")
		defer func() {
			_ = os.Unsetenv("GCP_VALKEY_AUTH")
			_ = os.Unsetenv("GCP_SERVICE_ACCOUNT_PATH")
			_ = os.Unsetenv("GCP_PROJECT_ID")
			_ = os.Unsetenv("GCP_TOKEN_REFRESH_BUFFER")
		}()

		config, err := ConfigFromEnv()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.True(t, config.Enabled)
		assert.Equal(t, "/path/to/service-account.json", config.ServiceAccountPath)
		assert.Equal(t, "test-project", config.ProjectID)
		assert.Equal(t, 5*time.Minute, config.TokenRefreshBuffer)
	})

	t.Run("ConfigFromEnv with disabled auth", func(t *testing.T) {
		_ = os.Setenv("GCP_VALKEY_AUTH", "false")
		defer func() { _ = os.Unsetenv("GCP_VALKEY_AUTH") }()

		config, err := ConfigFromEnv()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.False(t, config.Enabled)
	})

	t.Run("ConfigFromEnv with missing environment variables", func(t *testing.T) {
		// Clear relevant env vars
		_ = os.Unsetenv("GCP_VALKEY_AUTH")
		_ = os.Unsetenv("GCP_SERVICE_ACCOUNT_PATH")
		_ = os.Unsetenv("GCP_PROJECT_ID")

		config, err := ConfigFromEnv()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.False(t, config.Enabled) // Should default to false
	})

	t.Run("ValidateConfig with valid configuration", func(t *testing.T) {
		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: "/valid/path.json",
			ProjectID:          "valid-project",
			TokenRefreshBuffer: 5 * time.Minute,
		}

		err := config.ValidateConfig()
		assert.NoError(t, err)
	})

	t.Run("ValidateConfig with disabled configuration", func(t *testing.T) {
		config := &GCPAuthConfig{
			Enabled: false,
		}

		err := config.ValidateConfig()
		assert.NoError(t, err) // Should pass when disabled
	})

	t.Run("ValidateConfig with missing service account path", func(t *testing.T) {
		config := &GCPAuthConfig{
			Enabled:   true,
			ProjectID: "valid-project",
		}

		err := config.ValidateConfig()
		assert.ErrorIs(t, err, ErrServiceAccountMissing)
	})

	t.Run("ValidateConfig with missing project ID", func(t *testing.T) {
		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: "/path/to/service-account.json",
		}

		err := config.ValidateConfig()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "project ID")
	})
}

// TestGCPTokenProvider tests the GCP token provider implementation
func TestGCPTokenProvider(t *testing.T) {
	t.Run("NewGCPTokenProvider with valid configuration", func(t *testing.T) {
		// Create a temporary service account file
		tempDir := t.TempDir()
		serviceAccountPath := filepath.Join(tempDir, "service-account.json")
		
		serviceAccount := map[string]interface{}{
			"type":                        "service_account",
			"project_id":                  "test-project",
			"private_key_id":              "key-id",
			"private_key":                 "-----BEGIN PRIVATE KEY-----\ntest-key\n-----END PRIVATE KEY-----\n",
			"client_email":                "test@test-project.iam.gserviceaccount.com",
			"client_id":                   "123456789",
			"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
			"token_uri":                   "https://oauth2.googleapis.com/token",
			"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		}

		data, _ := json.Marshal(serviceAccount)
		err := os.WriteFile(serviceAccountPath, data, 0644)
		require.NoError(t, err)

		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: serviceAccountPath,
			ProjectID:          "test-project",
			TokenRefreshBuffer: 5 * time.Minute,
			TokenScope:         []string{"https://www.googleapis.com/auth/cloud-platform"},
		}

		mockLogger := &GCPMockLogger{}
		mockLogger.On("Info", mock.Anything, mock.Anything).Maybe()

		provider, err := NewGCPTokenProvider(config, mockLogger)
		assert.NoError(t, err)
		assert.NotNil(t, provider)
		assert.Equal(t, serviceAccountPath, provider.ServiceAccountPath)
		assert.Equal(t, "test-project", provider.ProjectID)
	})

	t.Run("NewGCPTokenProvider with non-existent service account file", func(t *testing.T) {
		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: "/non/existent/path.json",
			ProjectID:          "test-project",
		}

		mockLogger := &GCPMockLogger{}

		provider, err := NewGCPTokenProvider(config, mockLogger)
		assert.Error(t, err)
		assert.Nil(t, provider)
		assert.True(t, errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist))
	})

	t.Run("IsTokenExpired with expired token", func(t *testing.T) {
		provider := &GCPTokenProvider{
			tokenExpiry: time.Now().Add(-1 * time.Hour), // Expired 1 hour ago
		}

		assert.True(t, provider.IsTokenExpired())
	})

	t.Run("IsTokenExpired with valid token", func(t *testing.T) {
		provider := &GCPTokenProvider{
			tokenExpiry: time.Now().Add(1 * time.Hour), // Expires in 1 hour
		}

		assert.False(t, provider.IsTokenExpired())
	})

	t.Run("IsTokenExpired with zero time (no token)", func(t *testing.T) {
		provider := &GCPTokenProvider{
			tokenExpiry: time.Time{}, // Zero time
		}

		assert.True(t, provider.IsTokenExpired())
	})

	t.Run("GetTokenTTL calculation", func(t *testing.T) {
		futureTime := time.Now().Add(30 * time.Minute)
		provider := &GCPTokenProvider{
			tokenExpiry: futureTime,
		}

		ttl := provider.GetTokenTTL()
		assert.True(t, ttl > 25*time.Minute && ttl <= 30*time.Minute)
	})

	t.Run("GetTokenTTL with expired token", func(t *testing.T) {
		provider := &GCPTokenProvider{
			tokenExpiry: time.Now().Add(-1 * time.Hour),
		}

		ttl := provider.GetTokenTTL()
		assert.Equal(t, time.Duration(0), ttl)
	})
}

// TestConcurrentTokenAccess tests thread safety of token operations
func TestConcurrentTokenAccess(t *testing.T) {
	t.Run("concurrent token access", func(t *testing.T) {
		provider := &GCPTokenProvider{
			currentToken: "test-token",
			tokenExpiry:  time.Now().Add(1 * time.Hour),
			tokenMutex:   sync.RWMutex{},
		}

		var wg sync.WaitGroup
		errors := make(chan error, 100)
		ctx := context.Background()

		// Concurrent reads
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				token, err := provider.GetToken(ctx)
				if err != nil {
					errors <- err
					return
				}
				if token == "" {
					errors <- fmt.Errorf("empty token received")
				}
			}()
		}

		// Concurrent TTL checks
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = provider.GetTokenTTL()
				_ = provider.IsTokenExpired()
			}()
		}

		wg.Wait()
		close(errors)

		// Check no errors occurred
		for err := range errors {
			assert.NoError(t, err)
		}
	})
}

// TestTokenLifecycle tests the complete token lifecycle
func TestTokenLifecycle(t *testing.T) {
	t.Run("token refresh scenarios", func(t *testing.T) {
		mockProvider := &GCPMockTokenProvider{}
		ctx := context.Background()

		// Test successful token retrieval
		mockProvider.On("GetToken", ctx).Return("new-token", nil).Once()
		token, err := mockProvider.GetToken(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "new-token", token)

		// Test token refresh
		mockProvider.On("RefreshToken", ctx).Return("refreshed-token", nil).Once()
		refreshedToken, err := mockProvider.RefreshToken(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "refreshed-token", refreshedToken)

		// Test expired token
		mockProvider.On("IsTokenExpired").Return(true).Once()
		assert.True(t, mockProvider.IsTokenExpired())

		// Test TTL
		mockProvider.On("GetTokenTTL").Return(30 * time.Minute).Once()
		ttl := mockProvider.GetTokenTTL()
		assert.Equal(t, 30*time.Minute, ttl)

		mockProvider.AssertExpectations(t)
	})

	t.Run("token error scenarios", func(t *testing.T) {
		mockProvider := &GCPMockTokenProvider{}
		ctx := context.Background()

		// Test token retrieval error
		mockProvider.On("GetToken", ctx).Return("", ErrTokenRefreshFailed).Once()
		token, err := mockProvider.GetToken(ctx)
		assert.Error(t, err)
		assert.Empty(t, token)
		assert.ErrorIs(t, err, ErrTokenRefreshFailed)

		// Test refresh error
		mockProvider.On("RefreshToken", ctx).Return("", ErrInvalidCredentials).Once()
		refreshedToken, err := mockProvider.RefreshToken(ctx)
		assert.Error(t, err)
		assert.Empty(t, refreshedToken)
		assert.ErrorIs(t, err, ErrInvalidCredentials)

		mockProvider.AssertExpectations(t)
	})
}

// TestAuthenticatedRedisConnection tests Redis connection with authentication
func TestAuthenticatedRedisConnection(t *testing.T) {
	t.Run("authenticated connection creation", func(t *testing.T) {
		mockProvider := &GCPMockTokenProvider{}
		mockLogger := &GCPMockLogger{}
		
		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: "/path/to/service-account.json",
			ProjectID:          "test-project",
		}

		connection := &AuthenticatedRedisConnection{
			TokenProvider: mockProvider,
			AuthConfig:    *config,
			Connected:     false,
			Logger:        mockLogger,
		}

		assert.NotNil(t, connection)
		assert.False(t, connection.Connected)
		assert.Equal(t, mockProvider, connection.TokenProvider)
	})

	t.Run("authentication disabled scenario", func(t *testing.T) {
		config := &GCPAuthConfig{
			Enabled: false,
		}

		connection := &AuthenticatedRedisConnection{
			AuthConfig: *config,
		}

		assert.False(t, connection.AuthConfig.Enabled)
	})
}

// TestAutoRefreshMechanism tests the automatic token refresh mechanism
func TestAutoRefreshMechanism(t *testing.T) {
	t.Run("auto refresh start and stop", func(t *testing.T) {
		mockProvider := &GCPMockTokenProvider{}
		ctx := context.Background()

		// Test starting auto refresh
		mockProvider.On("StartAutoRefresh", ctx).Return(nil).Once()
		err := mockProvider.StartAutoRefresh(ctx)
		assert.NoError(t, err)

		// Test stopping auto refresh
		mockProvider.On("StopAutoRefresh").Return(nil).Once()
		err = mockProvider.StopAutoRefresh()
		assert.NoError(t, err)

		mockProvider.AssertExpectations(t)
	})

	t.Run("auto refresh error scenarios", func(t *testing.T) {
		mockProvider := &GCPMockTokenProvider{}
		ctx := context.Background()

		// Test auto refresh start error
		expectedErr := errors.New("failed to start auto refresh")
		mockProvider.On("StartAutoRefresh", ctx).Return(expectedErr).Once()
		err := mockProvider.StartAutoRefresh(ctx)
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)

		// Test auto refresh stop error
		stopErr := errors.New("failed to stop auto refresh")
		mockProvider.On("StopAutoRefresh").Return(stopErr).Once()
		err = mockProvider.StopAutoRefresh()
		assert.Error(t, err)
		assert.Equal(t, stopErr, err)

		mockProvider.AssertExpectations(t)
	})
}

// TestAuthError tests the authentication error wrapper
func TestAuthError(t *testing.T) {
	t.Run("AuthError creation and methods", func(t *testing.T) {
		originalErr := errors.New("original error")
		authErr := &AuthError{
			Operation: "token_refresh",
			Cause:     originalErr,
			Timestamp: time.Now(),
		}

		assert.Contains(t, authErr.Error(), "token_refresh")
		assert.Contains(t, authErr.Error(), "original error")
		assert.Equal(t, originalErr, authErr.Unwrap())
	})

	t.Run("AuthError with nil cause", func(t *testing.T) {
		authErr := &AuthError{
			Operation: "test_operation",
			Cause:     nil,
			Timestamp: time.Now(),
		}

		assert.Contains(t, authErr.Error(), "test_operation")
		assert.Nil(t, authErr.Unwrap())
	})
}

// TestRedisConnectionBuilder tests the Redis connection builder pattern
func TestRedisConnectionBuilder(t *testing.T) {
	t.Run("builder pattern with GCP auth", func(t *testing.T) {
		mockLogger := &GCPMockLogger{}
		
		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: "/path/to/service-account.json",
			ProjectID:          "test-project",
		}

		singleConfig := &RedisConnection{
			Addr:     "localhost:6379",
			Password: "",
			DB:       0,
		}

		builder := &RedisConnectionBuilder{
			ConnectionType: SingleInstance,
			SingleConfig:   singleConfig,
			GCPAuth:        config,
			Logger:         mockLogger,
		}

		assert.Equal(t, SingleInstance, builder.ConnectionType)
		assert.NotNil(t, builder.GCPAuth)
		assert.True(t, builder.GCPAuth.Enabled)
	})

	t.Run("builder with cluster configuration", func(t *testing.T) {
		mockLogger := &GCPMockLogger{}

		clusterConfig := &GCPClusterConfig{
			Addrs:    []string{"localhost:6379", "localhost:6380"},
			Password: "",
		}

		builder := &RedisConnectionBuilder{
			ConnectionType: Cluster,
			ClusterConfig:  clusterConfig,
			Logger:         mockLogger,
		}

		assert.Equal(t, Cluster, builder.ConnectionType)
		assert.NotNil(t, builder.ClusterConfig)
		assert.Len(t, builder.ClusterConfig.Addrs, 2)
	})
}

// TestErrorScenarios tests various error conditions
func TestErrorScenarios(t *testing.T) {
	t.Run("service account file read errors", func(t *testing.T) {
		// Test with directory instead of file
		tempDir := t.TempDir()
		
		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: tempDir, // Directory, not a file
			ProjectID:          "test-project",
		}

		mockLogger := &GCPMockLogger{}
		
		provider, err := NewGCPTokenProvider(config, mockLogger)
		assert.Error(t, err)
		assert.Nil(t, provider)
	})

	t.Run("invalid JSON in service account file", func(t *testing.T) {
		tempDir := t.TempDir()
		serviceAccountPath := filepath.Join(tempDir, "invalid.json")
		
		// Write invalid JSON
		err := os.WriteFile(serviceAccountPath, []byte("invalid json content"), 0644)
		require.NoError(t, err)

		config := &GCPAuthConfig{
			Enabled:            true,
			ServiceAccountPath: serviceAccountPath,
			ProjectID:          "test-project",
		}

		mockLogger := &GCPMockLogger{}

		provider, err := NewGCPTokenProvider(config, mockLogger)
		assert.Error(t, err)
		assert.Nil(t, provider)
	})
}

// TestEnvironmentVariableParsing tests environment variable parsing edge cases
func TestEnvironmentVariableParsing(t *testing.T) {
	t.Run("invalid token refresh buffer", func(t *testing.T) {
		_ = os.Setenv("GCP_VALKEY_AUTH", "true")
		_ = os.Setenv("GCP_TOKEN_REFRESH_BUFFER", "invalid-duration")
		defer func() {
			_ = os.Unsetenv("GCP_VALKEY_AUTH")
			_ = os.Unsetenv("GCP_TOKEN_REFRESH_BUFFER")
		}()

		config, err := ConfigFromEnv()
		// Should handle invalid duration gracefully
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.True(t, config.Enabled)
		// Should fall back to default value when parsing fails
		assert.Equal(t, DefaultTokenRefreshBuffer, config.TokenRefreshBuffer)
	})

	t.Run("empty environment values", func(t *testing.T) {
		_ = os.Setenv("GCP_VALKEY_AUTH", "")
		_ = os.Setenv("GCP_SERVICE_ACCOUNT_PATH", "")
		_ = os.Setenv("GCP_PROJECT_ID", "")
		defer func() {
			_ = os.Unsetenv("GCP_VALKEY_AUTH")
			_ = os.Unsetenv("GCP_SERVICE_ACCOUNT_PATH")
			_ = os.Unsetenv("GCP_PROJECT_ID")
		}()

		config, err := ConfigFromEnv()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.False(t, config.Enabled) // Empty string should be false
		assert.Empty(t, config.ServiceAccountPath)
		assert.Empty(t, config.ProjectID)
	})
}