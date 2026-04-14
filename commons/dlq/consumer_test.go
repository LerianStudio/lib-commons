//go:build unit

package dlq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMetrics captures DLQ metric calls for test verification.
type mockMetrics struct {
	mu             sync.Mutex
	retriedCalls   []string
	exhaustedCalls []string
	lostCalls      []string
}

func (m *mockMetrics) RecordRetried(_ context.Context, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.retriedCalls = append(m.retriedCalls, source)
}

func (m *mockMetrics) RecordExhausted(_ context.Context, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.exhaustedCalls = append(m.exhaustedCalls, source)
}

func (m *mockMetrics) RecordLost(_ context.Context, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lostCalls = append(m.lostCalls, source)
}

func (m *mockMetrics) retriedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.retriedCalls)
}

func (m *mockMetrics) exhaustedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.exhaustedCalls)
}

func (m *mockMetrics) lostCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.lostCalls)
}

// injectMessage pushes a FailedMessage directly into the Redis list,
// bypassing Handler.Enqueue so that NextRetryAt is not recalculated.
// This allows tests to control the exact timing semantics.
func injectMessage(t *testing.T, mr *miniredis.Miniredis, key string, msg *FailedMessage) {
	t.Helper()

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	mr.RPush(key, string(data))
}

// newTestConsumer creates a Consumer with a fresh Handler, miniredis, and the
// given retryFunc. Configures "outbound" as the single source.
func newTestConsumer(t *testing.T, retryFn RetryFunc, metrics *mockMetrics) (*Consumer, *Handler, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3, WithMetrics(metrics))

	c, err := NewConsumer(h, retryFn,
		WithSources("outbound"),
		WithBatchSize(10),
		WithPollInterval(100*time.Millisecond),
		WithConsumerMetrics(metrics),
	)
	require.NoError(t, err)

	return c, h, mr
}

func TestNewConsumer_NilHandler(t *testing.T) {
	t.Parallel()

	_, err := NewConsumer(nil, func(_ context.Context, _ *FailedMessage) error { return nil })
	require.ErrorIs(t, err, ErrNilHandler)
}

func TestNewConsumer_NilRetryFunc(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3)

	_, err := NewConsumer(h, nil)
	require.ErrorIs(t, err, ErrNilRetryFunc)
}

func TestNewConsumer_Defaults(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3)

	c, err := NewConsumer(h, func(_ context.Context, _ *FailedMessage) error { return nil })
	require.NoError(t, err)

	assert.Equal(t, 30*time.Second, c.cfg.PollInterval, "default PollInterval should be 30s")
	assert.Equal(t, 10, c.cfg.BatchSize, "default BatchSize should be 10")
	assert.NotNil(t, c.logger, "logger should never be nil")
	assert.NotNil(t, c.tracer, "tracer should never be nil")
}

func TestProcessOnce_RetrySuccess(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		return nil // Retry succeeds.
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Inject directly into Redis with NextRetryAt in the past so the consumer
	// considers the message immediately retryable. Enqueue recalculates backoff,
	// so we bypass it for precise timing control.
	injectMessage(t, mr, "dlq:tenant-abc:outbound", &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"1"}`),
		ErrorMessage: "transient error",
		RetryCount:   1,
		MaxRetries:   3,
		CreatedAt:    time.Now().UTC().Add(-5 * time.Minute),
		NextRetryAt:  time.Now().UTC().Add(-1 * time.Minute),
		TenantID:     "tenant-abc",
	})

	c.ProcessOnce(ctx)

	assert.Equal(t, 1, metrics.retriedCount(), "successful retry should record a retried metric")
	assert.Equal(t, 0, metrics.exhaustedCount())

	// Message should be consumed (removed from queue).
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(0), length, "queue should be empty after successful retry")
}

func TestProcessOnce_RetryFailed(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		return errors.New("still broken")
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Inject with past NextRetryAt so it is immediately eligible for retry.
	originalCreatedAt := time.Now().UTC().Add(-5 * time.Minute)

	injectMessage(t, mr, "dlq:tenant-abc:outbound", &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"2"}`),
		ErrorMessage: "original error",
		RetryCount:   0,
		MaxRetries:   3,
		CreatedAt:    originalCreatedAt,
		NextRetryAt:  time.Now().UTC().Add(-1 * time.Minute),
		TenantID:     "tenant-abc",
	})

	c.ProcessOnce(ctx)

	assert.Equal(t, 0, metrics.retriedCount(), "failed retry should not record retried")
	assert.Equal(t, 0, metrics.exhaustedCount(), "not yet exhausted")

	// Message should be re-enqueued with incremented RetryCount.
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(1), length, "message should be re-enqueued")

	msg, err := h.Dequeue(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, 1, msg.RetryCount, "RetryCount should be incremented")
	assert.Equal(t, "still broken", msg.ErrorMessage, "ErrorMessage should reflect last failure")

	// CRITICAL-1 fix: CreatedAt and MaxRetries must survive the re-enqueue.
	assert.Equal(t, originalCreatedAt.Unix(), msg.CreatedAt.Unix(),
		"CreatedAt must be preserved on re-enqueue after retry failure")
	assert.Equal(t, 3, msg.MaxRetries,
		"MaxRetries must be preserved on re-enqueue after retry failure")
	assert.False(t, msg.NextRetryAt.IsZero(),
		"NextRetryAt should be recalculated by consumer on retry failure")
}

func TestProcessOnce_Exhausted(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		return errors.New("should not be called for exhausted messages")
	}

	c, h, _ := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Enqueue a message at max retries — should be discarded without calling retryFn.
	require.NoError(t, h.Enqueue(ctx, &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"3"}`),
		ErrorMessage: "permanent failure",
		RetryCount:   3, // = maxRetries
	}))

	c.ProcessOnce(ctx)

	assert.Equal(t, 0, metrics.retriedCount())
	assert.Equal(t, 1, metrics.exhaustedCount(), "exhausted message should record exhausted metric")

	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(0), length, "exhausted message should be discarded")
}

func TestProcessOnce_NotYetReady(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	called := false

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		called = true
		return nil
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Inject directly into Redis with a known CreatedAt so we can verify
	// it survives the re-enqueue path without being overwritten.
	originalCreatedAt := time.Now().UTC().Add(-10 * time.Minute)
	futureRetryAt := time.Now().UTC().Add(1 * time.Hour)

	injectMessage(t, mr, "dlq:tenant-abc:outbound", &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"4"}`),
		ErrorMessage: "not yet",
		RetryCount:   1,
		MaxRetries:   3,
		CreatedAt:    originalCreatedAt,
		NextRetryAt:  futureRetryAt,
		TenantID:     "tenant-abc",
	})

	c.ProcessOnce(ctx)

	assert.False(t, called, "retryFn should NOT be called for a not-yet-ready message")
	assert.Equal(t, 0, metrics.retriedCount())
	assert.Equal(t, 0, metrics.exhaustedCount())

	// Message should still be in the queue (re-enqueued).
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(1), length, "not-yet-ready message should be re-enqueued")

	// Verify the original CreatedAt and MaxRetries were preserved through
	// the re-enqueue path (CRITICAL-1 fix: Enqueue must not overwrite
	// these fields on re-enqueue).
	requeued, err := h.Dequeue(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, originalCreatedAt.Unix(), requeued.CreatedAt.Unix(),
		"CreatedAt must be preserved on re-enqueue")
	assert.Equal(t, 3, requeued.MaxRetries,
		"MaxRetries must be preserved on re-enqueue")
	assert.Equal(t, 1, requeued.RetryCount,
		"RetryCount must not change for not-yet-ready re-enqueue")
}

// TestProcessOnce_HeadOfLineRotation verifies that when a queue contains a
// future-dated message at the head followed by a ready message, ProcessOnce
// rotates the future message to the tail and immediately processes the ready
// message in the same poll cycle — without waiting for the next tick.
func TestProcessOnce_HeadOfLineRotation(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	var retriedMessages []*FailedMessage

	retryFn := func(_ context.Context, msg *FailedMessage) error {
		retriedMessages = append(retriedMessages, msg)
		return nil
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-rot")

	// Message 1: future-dated (NOT yet ready for retry).
	futureCreatedAt := time.Now().UTC().Add(-10 * time.Minute)
	futureRetryAt := time.Now().UTC().Add(1 * time.Hour)

	injectMessage(t, mr, "dlq:tenant-rot:outbound", &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"future"}`),
		ErrorMessage: "not yet",
		RetryCount:   1,
		MaxRetries:   3,
		CreatedAt:    futureCreatedAt,
		NextRetryAt:  futureRetryAt,
		TenantID:     "tenant-rot",
	})

	// Message 2: ready (past NextRetryAt).
	injectMessage(t, mr, "dlq:tenant-rot:outbound", &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"ready"}`),
		ErrorMessage: "transient error",
		RetryCount:   0,
		MaxRetries:   3,
		CreatedAt:    time.Now().UTC().Add(-5 * time.Minute),
		NextRetryAt:  time.Now().UTC().Add(-1 * time.Minute),
		TenantID:     "tenant-rot",
	})

	c.ProcessOnce(ctx)

	// The ready message must have been retried.
	assert.Equal(t, 1, metrics.retriedCount(),
		"the ready message should be retried in the same poll cycle")
	assert.Len(t, retriedMessages, 1, "retryFn should be invoked exactly once")
	assert.JSONEq(t, `{"id":"ready"}`, string(retriedMessages[0].OriginalData),
		"the retried message should be the ready one, not the future-dated one")

	// Queue should contain exactly 1 message: the re-enqueued future message.
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(1), length,
		"only the future-dated message should remain in the queue")

	// Verify the re-enqueued future message preserved its original fields.
	remaining, err := h.Dequeue(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, futureCreatedAt.Unix(), remaining.CreatedAt.Unix(),
		"CreatedAt must be preserved through rotation re-enqueue")
	assert.Equal(t, 3, remaining.MaxRetries,
		"MaxRetries must be preserved through rotation re-enqueue")
	assert.Equal(t, 1, remaining.RetryCount,
		"RetryCount must not change for a rotated not-yet-ready message")
}

func TestProcessOnce_EmptyQueue(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	called := false

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		called = true
		return nil
	}

	c, _, _ := newTestConsumer(t, retryFn, metrics)

	// Process with no messages at all.
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")
	c.ProcessOnce(ctx)

	assert.False(t, called, "retryFn should NOT be called on empty queue")
	assert.Equal(t, 0, metrics.retriedCount())
	assert.Equal(t, 0, metrics.exhaustedCount())
}

func TestStop_IdempotentClose(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error { return nil }

	c, _, _ := newTestConsumer(t, retryFn, metrics)

	ctx, cancel := context.WithCancel(context.Background())

	// Start consumer in a goroutine so Run blocks.
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()

	// Give the loop a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Calling Stop multiple times must not panic.
	c.Stop()
	c.Stop()
	c.Stop()

	cancel()
	<-done // Wait for Run to return.
}

// newTestConsumerWithSources creates a Consumer that listens to multiple sources.
// This is a variant of newTestConsumer for multi-source tests.
func newTestConsumerWithSources(t *testing.T, retryFn RetryFunc, metrics *mockMetrics, sources ...string) (*Consumer, *Handler, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3, WithMetrics(metrics))

	c, err := NewConsumer(h, retryFn,
		WithSources(sources...),
		WithBatchSize(10),
		WithPollInterval(100*time.Millisecond),
		WithConsumerMetrics(metrics),
	)
	require.NoError(t, err)

	return c, h, mr
}

// TestProcessOnce_MultipleSources verifies that a Consumer configured with
// multiple sources drains messages from all of them in a single ProcessOnce call.
func TestProcessOnce_MultipleSources(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		return nil // all retries succeed
	}

	c, h, mr := newTestConsumerWithSources(t, retryFn, metrics, "source-a", "source-b")

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-multi")

	// Inject one message per source with past NextRetryAt so both are immediately eligible.
	for _, src := range []string{"source-a", "source-b"} {
		injectMessage(t, mr, fmt.Sprintf("dlq:tenant-multi:%s", src), &FailedMessage{
			Source:      src,
			TenantID:    "tenant-multi",
			RetryCount:  0,
			MaxRetries:  3,
			CreatedAt:   time.Now().UTC().Add(-5 * time.Minute),
			NextRetryAt: time.Now().UTC().Add(-1 * time.Minute),
		})
	}

	c.ProcessOnce(ctx)

	assert.Equal(t, 2, metrics.retriedCount(), "one successful retry per source = 2 total")

	// Both queues should be empty.
	for _, src := range []string{"source-a", "source-b"} {
		length, err := h.QueueLength(ctx, src)
		require.NoError(t, err)
		assert.Equal(t, int64(0), length, "queue for %q should be empty after ProcessOnce", src)
	}
}

// TestProcessOnce_BatchSizeEnforcement verifies that the Consumer respects its
// BatchSize limit and does not drain more than BatchSize messages per poll cycle.
func TestProcessOnce_BatchSizeEnforcement(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		return nil
	}

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3, WithMetrics(metrics))

	// BatchSize deliberately set to 2 so only 2 of the 5 injected messages
	// should be processed in a single poll cycle.
	c, err := NewConsumer(h, retryFn,
		WithSources("outbound"),
		WithBatchSize(2),
		WithConsumerMetrics(metrics),
	)
	require.NoError(t, err)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-batch")

	// Inject 5 immediately-retryable messages.
	for i := range 5 {
		injectMessage(t, mr, "dlq:tenant-batch:outbound", &FailedMessage{
			Source:       "outbound",
			TenantID:     "tenant-batch",
			RetryCount:   0,
			MaxRetries:   3,
			CreatedAt:    time.Now().UTC().Add(-5 * time.Minute),
			NextRetryAt:  time.Now().UTC().Add(-1 * time.Minute),
			OriginalData: []byte(fmt.Sprintf(`{"i":%d}`, i)),
		})
	}

	c.ProcessOnce(ctx)

	// Only 2 should have been processed.
	assert.Equal(t, 2, metrics.retriedCount(), "BatchSize=2 means at most 2 messages processed per cycle")

	// 3 should remain in the queue.
	remaining, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(3), remaining, "3 of 5 messages should remain after batch-limited ProcessOnce")
}

// TestConsumerOptions verifies the boundary behaviour of each ConsumerOption:
// nil inputs are no-ops, zero/negative numeric values keep the default, and
// valid string/duration values are applied correctly.
func TestConsumerOptions(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	baseHandler := New(conn, "dlq:", 3)
	noop := func(_ context.Context, _ *FailedMessage) error { return nil }

	t.Run("WithConsumerLogger nil is no-op", func(t *testing.T) {
		t.Parallel()

		c, err := NewConsumer(baseHandler, noop, WithConsumerLogger(nil))
		require.NoError(t, err)
		assert.NotNil(t, c.logger, "logger should remain non-nil after WithConsumerLogger(nil)")
	})

	t.Run("WithConsumerTracer nil is no-op", func(t *testing.T) {
		t.Parallel()

		c, err := NewConsumer(baseHandler, noop, WithConsumerTracer(nil))
		require.NoError(t, err)
		assert.NotNil(t, c.tracer, "tracer should remain non-nil after WithConsumerTracer(nil)")
	})

	t.Run("WithConsumerModule sets module", func(t *testing.T) {
		t.Parallel()

		c, err := NewConsumer(baseHandler, noop, WithConsumerModule("test-module"))
		require.NoError(t, err)
		assert.Equal(t, "test-module", c.module)
	})

	t.Run("WithPollInterval zero keeps default", func(t *testing.T) {
		t.Parallel()

		c, err := NewConsumer(baseHandler, noop, WithPollInterval(0))
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, c.cfg.PollInterval,
			"zero PollInterval should keep the 30s default")
	})

	t.Run("WithBatchSize negative keeps default", func(t *testing.T) {
		t.Parallel()

		c, err := NewConsumer(baseHandler, noop, WithBatchSize(-1))
		require.NoError(t, err)
		assert.Equal(t, 10, c.cfg.BatchSize,
			"negative BatchSize should keep the 10 default")
	})
}

// TestProcessOnce_Exhausted_CalledFlag is a targeted regression test that
// verifies retryFn is NOT invoked for an exhausted message (RetryCount >= MaxRetries).
// It uses an explicit called flag to make this assertion unambiguous.
func TestProcessOnce_Exhausted_CalledFlag(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	called := false

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		called = true
		return nil
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Inject a message already at MaxRetries with a past NextRetryAt so the
	// consumer considers it immediately eligible for the exhaustion check.
	injectMessage(t, mr, "dlq:tenant-abc:outbound", &FailedMessage{
		Source:      "outbound",
		TenantID:    "tenant-abc",
		RetryCount:  3,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC().Add(-1 * time.Hour),
		NextRetryAt: time.Now().UTC().Add(-1 * time.Minute),
	})

	c.ProcessOnce(ctx)

	assert.False(t, called, "retryFn must NOT be called for an exhausted message")
	assert.Equal(t, 1, metrics.exhaustedCount(), "exhausted metric should be recorded once")
	assert.Equal(t, 0, metrics.retriedCount(), "retried metric must not be recorded")

	// Message should be discarded — queue must be empty.
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(0), length, "exhausted message must be discarded from the queue")
}

// TestProcessOnce_CancelledContext verifies that ProcessOnce returns quickly
// and performs no work when its context is already cancelled before the call.
// This covers the ctx.Err() guard inside drainSource which short-circuits the
// dequeue loop on cancellation.
func TestProcessOnce_CancelledContext(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	called := false

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		called = true
		return nil
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	// Inject a retryable message into Redis so there IS work to do in principle.
	injectMessage(t, mr, "dlq:tenant-cancel:outbound", &FailedMessage{
		Source:      "outbound",
		TenantID:    "tenant-cancel",
		RetryCount:  0,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC().Add(-5 * time.Minute),
		NextRetryAt: time.Now().UTC().Add(-1 * time.Minute),
	})

	// Pre-cancel the context BEFORE calling ProcessOnce.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	c.ProcessOnce(ctx)
	elapsed := time.Since(start)

	// ProcessOnce must return quickly — no blocking or long work.
	assert.Less(t, elapsed, 2*time.Second,
		"ProcessOnce with a pre-cancelled context should return quickly, took %s", elapsed)

	// No metrics should be recorded and retryFn must not have been invoked.
	// NOTE: the ctx.Err() guard fires inside drainSource's select, AFTER
	// ScanQueues has already been called (it runs against Redis directly).
	// If ScanQueues completes before the context cancellation is checked,
	// the message may be dequeued but then the keyCtx.Err() guard in
	// processSource fires before drainSource is entered. Either way,
	// retryFn must never be called.
	assert.False(t, called, "retryFn must NOT be called when context is pre-cancelled")

	// Queue integrity: the message may or may not have been dequeued depending
	// on where exactly the cancellation was observed; we only assert retryFn
	// was not called and no metrics were emitted.
	assert.Equal(t, 0, metrics.retriedCount(), "no retried metrics on cancelled context")
	assert.Equal(t, 0, metrics.exhaustedCount(), "no exhausted metrics on cancelled context")

	// Suppress "h declared but not used" if compiler complains.
	_ = h
}

// TestNewConsumer_NilOptions verifies that passing nil ConsumerOption values to
// NewConsumer is safe — nil options are silently skipped and the Consumer is
// constructed with its defaults intact.
func TestNewConsumer_NilOptions(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3)

	// Pass a nil option alongside a valid one to verify nil-safety.
	c, err := NewConsumer(h,
		func(_ context.Context, _ *FailedMessage) error { return nil },
		nil,              // nil option must be skipped
		WithBatchSize(5), // valid option applied after nil
		nil,              // trailing nil also fine
	)
	require.NoError(t, err, "nil ConsumerOption must not cause an error")
	require.NotNil(t, c, "Consumer must be non-nil even when nil options are passed")

	assert.Equal(t, 5, c.cfg.BatchSize, "valid option after nil must still be applied")
	assert.NotNil(t, c.logger, "logger must remain non-nil")
	assert.NotNil(t, c.tracer, "tracer must remain non-nil")
}

// TestProcessOnce_RetryFuncPanics verifies that if the caller-provided retryFn
// panics, the consumer recovers gracefully: the test does not crash, and the
// message is re-enqueued with an incremented retry count so it is not lost.
func TestProcessOnce_RetryFuncPanics(t *testing.T) {
	t.Parallel()

	metrics := &mockMetrics{}

	retryFn := func(_ context.Context, _ *FailedMessage) error {
		panic("boom from retryFn")
	}

	c, h, mr := newTestConsumer(t, retryFn, metrics)

	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-panic")

	// Inject a retryable message with a past NextRetryAt so the consumer
	// considers it immediately eligible for retry.
	injectMessage(t, mr, "dlq:tenant-panic:outbound", &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":"panic-msg"}`),
		ErrorMessage: "original error",
		RetryCount:   0,
		MaxRetries:   3,
		CreatedAt:    time.Now().UTC().Add(-5 * time.Minute),
		NextRetryAt:  time.Now().UTC().Add(-1 * time.Minute),
		TenantID:     "tenant-panic",
	})

	// ProcessOnce must not panic — the safeRetryFunc wrapper recovers.
	require.NotPanics(t, func() {
		c.ProcessOnce(ctx)
	}, "ProcessOnce must not propagate a panic from retryFn")

	// The message must be re-enqueued (not lost) with an incremented RetryCount.
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(1), length,
		"message must be re-enqueued after retryFn panic, not lost")

	// Dequeue and verify the retry count was incremented.
	msg, err := h.Dequeue(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, 1, msg.RetryCount,
		"RetryCount must be incremented after panic-recovered retry failure")
	assert.Contains(t, msg.ErrorMessage, "panicked",
		"ErrorMessage must indicate the panic was recovered")
}

// TestIsRedisNilError covers the four cases for isRedisNilError:
// direct redis.Nil sentinel, wrapped sentinel, arbitrary error, and nil.
func TestIsRedisNilError(t *testing.T) {
	t.Parallel()

	t.Run("direct redis.Nil returns true", func(t *testing.T) {
		t.Parallel()

		assert.True(t, isRedisNilError(redis.Nil))
	})

	t.Run("wrapped redis.Nil returns true", func(t *testing.T) {
		t.Parallel()

		wrapped := fmt.Errorf("dlq: dequeue: %w", redis.Nil)
		assert.True(t, isRedisNilError(wrapped))
	})

	t.Run("non-redis error returns false", func(t *testing.T) {
		t.Parallel()

		assert.False(t, isRedisNilError(errors.New("some other error")))
	})

	t.Run("nil error returns false", func(t *testing.T) {
		t.Parallel()

		assert.False(t, isRedisNilError(nil))
	})
}
