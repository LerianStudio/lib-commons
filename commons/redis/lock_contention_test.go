//go:build unit

package redis

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	libobs "github.com/LerianStudio/lib-observability/v4"
	"github.com/go-redsync/redsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans installs a recording TracerProvider as the OTel global, which is
// where obsbridge resolves the tracer from when a context carries none.
//
// WARNING: mutates global state — tests using it must NOT call t.Parallel().
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)

	t.Cleanup(func() {
		otel.SetTracerProvider(original)
		_ = tp.Shutdown(context.Background())
	})

	return recorder
}

// lockSpanStatus returns the status of the single span named spanName. The
// recorder also collects the go-redis command spans, so filtering by name is
// what isolates the lock's own span.
func lockSpanStatus(t *testing.T, recorder *tracetest.SpanRecorder, spanName string) sdktrace.ReadOnlySpan {
	t.Helper()

	var found sdktrace.ReadOnlySpan

	for _, span := range recorder.Ended() {
		if span.Name() == spanName {
			require.Nil(t, found, "more than one %s span was recorded", spanName)

			found = span
		}
	}

	require.NotNil(t, found, "no %s span was recorded", spanName)

	return found
}

// TestWithLockOptions_ContentionIsNotAFailure pins the classification a periodic
// sweep depends on. A replica that skips a cycle because a sibling holds the key
// is the DESIGNED outcome, so it must not produce an ERROR log line or an error
// span — but the error is still returned, because that is how the caller knows
// the function did not run.
func TestWithLockOptions_ContentionIsNotAFailure(t *testing.T) {
	recorder := recordSpans(t)
	_, lock := setupTestLock(t)

	logger := &recordingLogger{}
	ctx := libobs.ContextWithLogger(context.Background(), logger)

	handle, acquired, err := lock.TryLock(ctx, "test:contention:classified")
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = handle.Unlock(ctx) }()

	err = lock.WithLockOptions(ctx, "test:contention:classified", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		t.Fatal("the function must not run when the lock is held elsewhere")

		return nil
	})

	require.Error(t, err, "the caller learns from the error that fn did not run")
	assert.ErrorIs(t, err, ErrLockContended,
		"a consumer must be able to recognise contention without importing redsync")

	assert.False(t, logger.hasLevel(obs.LevelError),
		"contention was logged at ERROR: a sweep that skips a cycle by design fills the log with false failures; got %+v", logger.loggedEntries())

	span := lockSpanStatus(t, recorder, "redis.lock.with_lock")
	assert.NotEqual(t, codes.Error, span.Status().Code,
		"contention recorded an error on the span: the trace reports a designed outcome as a fault")
	assert.Empty(t, span.Events(), "contention recorded an exception event on the span")
}

// TestWithLockOptions_CallerCancellationIsNotAFailure covers a caller whose own
// context is already done. MEASURED against redsync v4.17: that surfaces as
// *redsync.RedisError wrapping context.Canceled, which is neither ErrFailed nor
// *ErrTaken — so only the caller's own ctx.Err() tells the two apart.
func TestWithLockOptions_CallerCancellationIsNotAFailure(t *testing.T) {
	recorder := recordSpans(t)
	_, lock := setupTestLock(t)

	logger := &recordingLogger{}
	ctx, cancel := context.WithCancel(libobs.ContextWithLogger(context.Background(), logger))
	cancel()

	err := lock.WithLockOptions(ctx, "test:cancelled:classified", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		t.Fatal("the function must not run when the caller's context is done")

		return nil
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	assert.False(t, logger.hasLevel(obs.LevelError),
		"a caller that cancelled its own context was reported as a lock failure; got %+v", logger.loggedEntries())

	span := lockSpanStatus(t, recorder, "redis.lock.with_lock")
	assert.NotEqual(t, codes.Error, span.Status().Code,
		"the caller's own cancellation was recorded as an error on the span")
}

// TestWithLockOptions_CancellationDuringRetryIsNotAFailure covers the other
// cancellation shape. MEASURED against redsync v4.17: a context that ends while
// the mutex is waiting out a retry delay is reported as a bare redsync.ErrFailed
// — indistinguishable from exhausted retries — so the classification cannot come
// from the error chain.
func TestWithLockOptions_CancellationDuringRetryIsNotAFailure(t *testing.T) {
	recorder := recordSpans(t)
	_, lock := setupTestLock(t)

	logger := &recordingLogger{}
	base := libobs.ContextWithLogger(context.Background(), logger)

	handle, acquired, err := lock.TryLock(base, "test:cancelled:midretry")
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = handle.Unlock(base) }()

	ctx, cancel := context.WithTimeout(base, 50*time.Millisecond)
	defer cancel()

	err = lock.WithLockOptions(ctx, "test:cancelled:midretry", LockOptions{
		Expiry:      5 * time.Second,
		Tries:       100,
		RetryDelay:  20 * time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		t.Fatal("the function must not run when the caller's context is done")

		return nil
	})

	require.Error(t, err)

	assert.False(t, logger.hasLevel(obs.LevelError),
		"a caller whose deadline expired while waiting for the lock was reported as a lock failure; got %+v", logger.loggedEntries())

	span := lockSpanStatus(t, recorder, "redis.lock.with_lock")
	assert.NotEqual(t, codes.Error, span.Status().Code,
		"an expired caller deadline was recorded as an error on the span")
}

// TestWithLockOptions_InfrastructureFaultStillErrors is the narrowing half: the
// reclassification must not swallow a Redis that stopped answering. The stalled
// server from context_deadline_test.go is used rather than a closed miniredis,
// because a freed port can be re-bound by another process mid-test.
func TestWithLockOptions_InfrastructureFaultStillErrors(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	recorder := recordSpans(t)

	client, err := New(context.Background(), Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: startStalledRedis(t)}},
		Options: ConnectionOptions{
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
			DialTimeout:  2 * time.Second,
			MaxRetries:   -1,
		},
		Logger: obs.Nop(),
	})
	require.NoError(t, err, "the stalled server answers PING, so connecting must succeed")

	t.Cleanup(func() { _ = client.Close() })

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	logger := &recordingLogger{}
	ctx := libobs.ContextWithLogger(context.Background(), logger)

	err = lock.WithLockOptions(ctx, "test:stalled", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		t.Fatal("the function must not run when Redis never answers")

		return nil
	})

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrLockContended,
		"a Redis that stopped answering was reported as ordinary contention")

	assert.True(t, logger.hasLevel(obs.LevelError),
		"a Redis that stopped answering was downgraded to debug; got %+v", logger.loggedEntries())

	span := lockSpanStatus(t, recorder, "redis.lock.with_lock")
	assert.Equal(t, codes.Error, span.Status().Code,
		"a Redis that stopped answering left the span clean")
}

// TestIsLockContention pins the shared classifier both entry points read, so the
// two cannot drift apart again. The values are the ones MEASURED from redsync
// v4.17: contention on a single node is *ErrTaken, and exhausted retries — or a
// context that ends during a retry delay — is a bare ErrFailed.
func TestIsLockContention(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil is not contention", err: nil, want: false},
		{name: "taken on one node", err: &redsync.ErrTaken{Nodes: []int{0}}, want: true},
		{name: "exhausted retries", err: redsync.ErrFailed, want: true},
		{name: "wrapped exhausted retries", err: fmt.Errorf("acquire: %w", redsync.ErrFailed), want: true},
		{name: "network fault", err: errors.New("dial tcp: connection refused"), want: false},
		{name: "caller cancellation", err: context.Canceled, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isLockContention(tt.err))
		})
	}
}
