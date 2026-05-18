//go:build unit

package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type storageLogEntry struct {
	level  libLog.Level
	msg    string
	fields []libLog.Field
}

type captureStorageLogger struct {
	mu      sync.Mutex
	entries []storageLogEntry
}

func (l *captureStorageLogger) Log(_ context.Context, level libLog.Level, msg string, fields ...libLog.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entryFields := append([]libLog.Field(nil), fields...)
	l.entries = append(l.entries, storageLogEntry{level: level, msg: msg, fields: entryFields})
}

func (l *captureStorageLogger) With(...libLog.Field) libLog.Logger { return l }
func (l *captureStorageLogger) WithGroup(string) libLog.Logger     { return l }
func (l *captureStorageLogger) Enabled(libLog.Level) bool          { return true }
func (l *captureStorageLogger) Sync(context.Context) error         { return nil }

func (l *captureStorageLogger) snapshot() []storageLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]storageLogEntry(nil), l.entries...)
}

type typedNilStorageLogger struct{}

func (*typedNilStorageLogger) Log(context.Context, libLog.Level, string, ...libLog.Field) {}
func (*typedNilStorageLogger) With(...libLog.Field) libLog.Logger                         { return nil }
func (*typedNilStorageLogger) WithGroup(string) libLog.Logger                             { return nil }
func (*typedNilStorageLogger) Enabled(libLog.Level) bool                                  { return false }
func (*typedNilStorageLogger) Sync(context.Context) error                                 { return nil }

func TestRedisStorage_ResetRemovesTenantScopedRateLimitKeys(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)
	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Set(t.Context(), "tenant:tenant-A:ratelimit:default:user-1", "1", time.Minute).Err())
	require.NoError(t, client.Set(t.Context(), "tenant:tenant-A:my-svc:ratelimit:default:user-1", "1", time.Minute).Err())
	require.NoError(t, client.Set(t.Context(), "tenant:tenant-A:non-ratelimit:key", "keep", time.Minute).Err())

	require.NoError(t, storage.Reset())

	keys := mr.Keys()
	assert.NotContains(t, keys, "tenant:tenant-A:ratelimit:default:user-1")
	assert.Contains(t, keys, "tenant:tenant-A:my-svc:ratelimit:default:user-1")
	assert.Contains(t, keys, "tenant:tenant-A:non-ratelimit:key")
	kept, err := client.Get(t.Context(), "tenant:tenant-A:non-ratelimit:key").Result()
	require.NoError(t, err)
	assert.Equal(t, "keep", kept)
}

func TestRedisStorage_LogError_HashesKeyAndOmitsRawKey(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)
	logger := &captureStorageLogger{}
	storage := NewRedisStorage(conn, WithRedisStorageLogger(logger))
	require.NotNil(t, storage)

	const rawKey = "tenant-A:user-123:export"
	mr.Close()

	_, err := storage.Get(rawKey)
	require.Error(t, err)

	entries := logger.snapshot()
	require.NotEmpty(t, entries)

	foundHash := false
	for _, entry := range entries {
		for _, field := range entry.fields {
			assert.NotEqual(t, "key", field.Key, "raw key field must not be logged")
			assert.NotEqual(t, rawKey, field.Value, "raw key value must not be logged")
			if field.Key == "key_hash" {
				foundHash = true
				assert.Equal(t, hashKey(rawKey), field.Value)
			}
		}
	}

	assert.True(t, foundHash, "redis operation errors must include key_hash")
}

func TestRedisStorageLogger_TypedNilIgnoredAndGuarded(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)
	var logger *typedNilStorageLogger

	storage := NewRedisStorage(conn, WithRedisStorageLogger(logger))
	require.NotNil(t, storage)
	assert.Nil(t, storage.logger)

	storage.logger = logger
	assert.NotPanics(t, func() {
		storage.logError(context.Background(), "redis get failed", assert.AnError, "key", "raw-identity")
	})
}
