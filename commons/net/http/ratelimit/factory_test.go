package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons"
	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// mockRedisConnectionGetter is a mock implementation of RedisConnectionGetter for testing
type mockRedisConnectionGetter struct {
	client redis.UniversalClient
	err    error
}

func (m *mockRedisConnectionGetter) GetClient(ctx context.Context) (redis.UniversalClient, error) {
	return m.client, m.err
}

// mockLimiterFactory creates a mock limiter factory for testing
func mockLimiterFactory(client redis.UniversalClient, config Config) Limiter {
	return &mockLimiter{
		config: config,
	}
}

// mockLimiter is a simple mock implementation of Limiter for testing
type mockLimiter struct {
	config Config
}

func (m *mockLimiter) Allow(ctx context.Context, key string) (*Result, error) {
	return &Result{
		Allowed:   true,
		Remaining: m.config.Max - 1,
		Limit:     m.config.Max,
		ResetAt:   time.Now().Add(m.config.Window),
	}, nil
}

func (m *mockLimiter) Reset(ctx context.Context, key string) error {
	return nil
}

func (m *mockLimiter) GetConfig() Config {
	return m.config
}

func TestNewGlobalHandler(t *testing.T) {
	// Start a mini Redis server for testing
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	tests := []struct {
		name        string
		config      *GlobalHandlerConfig
		expectPanic bool
		validate    func(t *testing.T, handler *GlobalHandler)
	}{
		{
			name: "successful initialization with rate limiting enabled",
			config: &GlobalHandlerConfig{
				Enabled:         true,
				GlobalMax:       100,
				GlobalWindowSec: 60,
				KeyPrefix:       "test:ratelimit",
				GlobalError: &commons.RateLimitError{
					Code:    "RATE_LIMIT_EXCEEDED",
					Title:   "Rate Limit Exceeded",
					Message: "Too many requests",
				},
				RedisConnection: &mockRedisConnectionGetter{
					client: client,
					err:    nil,
				},
				LimiterFactory: mockLimiterFactory,
			},
			expectPanic: false,
			validate: func(t *testing.T, handler *GlobalHandler) {
				assert.NotNil(t, handler)
				assert.NotNil(t, handler.manager)
				assert.True(t, handler.config.Enabled)
			},
		},
		{
			name: "rate limiting disabled",
			config: &GlobalHandlerConfig{
				Enabled:   false,
				GlobalMax: 100,
			},
			expectPanic: false,
			validate: func(t *testing.T, handler *GlobalHandler) {
				assert.NotNil(t, handler)
				assert.Nil(t, handler.manager)
				assert.False(t, handler.config.Enabled)
			},
		},
		{
			name: "missing limiter factory - should panic",
			config: &GlobalHandlerConfig{
				Enabled:         true,
				GlobalMax:       100,
				GlobalWindowSec: 60,
				RedisConnection: &mockRedisConnectionGetter{
					client: client,
					err:    nil,
				},
				LimiterFactory: nil,
			},
			expectPanic: true,
		},
		{
			name: "missing logger - should panic",
			config: &GlobalHandlerConfig{
				Enabled:         true,
				GlobalMax:       100,
				GlobalWindowSec: 60,
				RedisConnection: &mockRedisConnectionGetter{
					client: client,
					err:    nil,
				},
				LimiterFactory: mockLimiterFactory,
			},
			expectPanic: true,
		},
		{
			name: "redis connection fails - should panic",
			config: &GlobalHandlerConfig{
				Enabled:         true,
				GlobalMax:       100,
				GlobalWindowSec: 60,
				RedisConnection: &mockRedisConnectionGetter{
					client: nil,
					err:    assert.AnError,
				},
				LimiterFactory: mockLimiterFactory,
			},
			expectPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectPanic {
				// For the missing logger test, pass nil logger
				if tt.name == "missing logger - should panic" {
					assert.Panics(t, func() {
						NewGlobalHandler(tt.config, nil)
					})
				} else {
					assert.Panics(t, func() {
						NewGlobalHandler(tt.config, logger)
					})
				}
			} else {
				handler := NewGlobalHandler(tt.config, logger)
				if tt.validate != nil {
					tt.validate(t, handler)
				}
			}
		})
	}
}

func TestGlobalHandler_GlobalMiddleware(t *testing.T) {
	// Start a mini Redis server for testing
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	t.Run("rate limiting enabled", func(t *testing.T) {
		handler := NewGlobalHandler(&GlobalHandlerConfig{
			Enabled:         true,
			GlobalMax:       100,
			GlobalWindowSec: 60,
			KeyPrefix:       "test:ratelimit",
			GlobalError: &commons.RateLimitError{
				Code:    "RATE_LIMIT_EXCEEDED",
				Title:   "Rate Limit Exceeded",
				Message: "Too many requests",
			},
			RedisConnection: &mockRedisConnectionGetter{
				client: client,
				err:    nil,
			},
			LimiterFactory: mockLimiterFactory,
		}, logger)

		middleware := handler.GlobalMiddleware()
		assert.NotNil(t, middleware)
	})

	t.Run("rate limiting disabled", func(t *testing.T) {
		handler := NewGlobalHandler(&GlobalHandlerConfig{
			Enabled: false,
		}, logger)

		middleware := handler.GlobalMiddleware()
		assert.NotNil(t, middleware)
	})
}

func TestGlobalHandler_GetManager(t *testing.T) {
	// Start a mini Redis server for testing
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	t.Run("rate limiting enabled - manager exists", func(t *testing.T) {
		handler := NewGlobalHandler(&GlobalHandlerConfig{
			Enabled:         true,
			GlobalMax:       100,
			GlobalWindowSec: 60,
			KeyPrefix:       "test:ratelimit",
			RedisConnection: &mockRedisConnectionGetter{
				client: client,
				err:    nil,
			},
			LimiterFactory: mockLimiterFactory,
		}, logger)

		manager := handler.GetManager()
		assert.NotNil(t, manager)
	})

	t.Run("rate limiting disabled - manager is nil", func(t *testing.T) {
		handler := NewGlobalHandler(&GlobalHandlerConfig{
			Enabled: false,
		}, logger)

		manager := handler.GetManager()
		assert.Nil(t, manager)
	})
}

func TestGlobalHandler_createLogger(t *testing.T) {
	logger := &log.GoLogger{Level: log.InfoLevel}

	handler := &GlobalHandler{
		logger: logger,
	}

	loggerFunc := handler.createLogger()
	assert.NotNil(t, loggerFunc)

	// Test that logger function doesn't panic
	loggerFunc("info", "test message")
	loggerFunc("error", "test error: %v", assert.AnError)
	loggerFunc("warn", "test warning")
}

func TestGlobalHandler_createLoggerNil(t *testing.T) {
	handler := &GlobalHandler{
		logger: nil,
	}

	loggerFunc := handler.createLogger()
	assert.NotNil(t, loggerFunc)

	// Test that no-op logger doesn't panic
	loggerFunc("info", "test message")
	loggerFunc("error", "test error")
}
