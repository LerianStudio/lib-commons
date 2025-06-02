package redis

import (
	"context"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/mock"
)

// MockLogger is a mock implementation of log.Logger
type MockLogger struct {
	mock.Mock
}

func (m *MockLogger) Info(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Infof(format string, args ...any) {
	m.Called(append([]any{format}, args...)...)
}

func (m *MockLogger) Infoln(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Error(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Errorf(format string, args ...any) {
	m.Called(append([]any{format}, args...)...)
}

func (m *MockLogger) Errorln(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Warn(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Warnf(format string, args ...any) {
	m.Called(append([]any{format}, args...)...)
}

func (m *MockLogger) Warnln(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Debug(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Debugf(format string, args ...any) {
	m.Called(append([]any{format}, args...)...)
}

func (m *MockLogger) Debugln(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Fatal(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) Fatalf(format string, args ...any) {
	m.Called(append([]any{format}, args...)...)
}

func (m *MockLogger) Fatalln(args ...any) {
	m.Called(args...)
}

func (m *MockLogger) WithFields(fields ...any) log.Logger {
	args := m.Called(fields...)
	return args.Get(0).(log.Logger)
}

func (m *MockLogger) WithDefaultMessageTemplate(message string) log.Logger {
	args := m.Called(message)
	return args.Get(0).(log.Logger)
}

func (m *MockLogger) Sync() error {
	args := m.Called()
	return args.Error(0)
}

// MockRedisClusterClient is a mock implementation for testing cluster client
type MockRedisClusterClient struct {
	mock.Mock
}

func (m *MockRedisClusterClient) Ping(ctx context.Context) *redis.StatusCmd {
	args := m.Called(ctx)
	return args.Get(0).(*redis.StatusCmd)
}

func (m *MockRedisClusterClient) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	args := m.Called(ctx, key, value, expiration)
	return args.Get(0).(*redis.StatusCmd)
}

func (m *MockRedisClusterClient) Get(ctx context.Context, key string) *redis.StringCmd {
	args := m.Called(ctx, key)
	return args.Get(0).(*redis.StringCmd)
}

func (m *MockRedisClusterClient) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	args := m.Called(ctx, keys)
	return args.Get(0).(*redis.IntCmd)
}

func (m *MockRedisClusterClient) Close() error {
	args := m.Called()
	return args.Error(0)
}
