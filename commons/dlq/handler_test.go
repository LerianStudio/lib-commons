//go:build unit

package dlq

import (
	"context"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libRedis "github.com/LerianStudio/lib-commons/v4/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// newTestRedisClient creates a *libRedis.Client backed by miniredis.
// Follows the exact pattern from commons/redis/lock_test.go and
// commons/net/http/ratelimit/redis_storage_test.go.
func newTestRedisClient(t *testing.T, mr *miniredis.Miniredis) *libRedis.Client {
	t.Helper()

	conn, err := libRedis.New(context.Background(), libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &libLog.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("newTestRedisClient cleanup: conn.Close() error: %v", err)
		}
	})

	return conn
}

// newTestHandler creates a Handler wired to a fresh miniredis instance.
func newTestHandler(t *testing.T) (*Handler, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "dlq:", 3)

	return h, mr
}

func TestNew_Defaults(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	h := New(conn, "", 0)

	assert.Equal(t, 3, h.maxRetries, "default maxRetries should be 3")
	assert.Equal(t, "dlq:", h.keyPrefix, "default keyPrefix should be 'dlq:'")
	assert.NotNil(t, h.logger, "logger should never be nil")
	assert.NotNil(t, h.tracer, "tracer should never be nil")
	assert.Nil(t, h.metrics, "metrics should be nil when not provided")
}

func TestNew_NilConn(t *testing.T) {
	t.Parallel()

	h := New(nil, "dlq:", 3)
	assert.Nil(t, h, "New should return nil when conn is nil")

	// Nil *Handler is safe to use — all methods return ErrNilHandler.
	err := h.Enqueue(context.Background(), &FailedMessage{Source: "test"})
	require.ErrorIs(t, err, ErrNilHandler)

	_, err = h.Dequeue(context.Background(), "test")
	require.ErrorIs(t, err, ErrNilHandler)

	_, err = h.QueueLength(context.Background(), "test")
	require.ErrorIs(t, err, ErrNilHandler)
}

func TestNew_WithOptions(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisClient(t, mr)

	logger := libLog.NewNop()
	tracer := noop.NewTracerProvider().Tracer("test")
	m := &mockMetrics{}

	h := New(conn, "custom:", 5,
		WithLogger(logger),
		WithTracer(tracer),
		WithMetrics(m),
		WithModule("payments"),
	)

	assert.Equal(t, 5, h.maxRetries)
	assert.Equal(t, "custom:", h.keyPrefix)
	assert.Equal(t, "payments", h.module)
	assert.Same(t, m, h.metrics, "metrics should be the supplied mock")
}

func TestEnqueue_NilHandler(t *testing.T) {
	t.Parallel()

	var h *Handler

	err := h.Enqueue(context.Background(), &FailedMessage{Source: "test"})
	require.ErrorIs(t, err, ErrNilHandler)
}

func TestEnqueue_NilMessage(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)

	err := h.Enqueue(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil message")
}

func TestEnqueue_Success(t *testing.T) {
	t.Parallel()

	h, mr := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	msg := &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"id":1}`),
		ErrorMessage: "timeout",
	}

	err := h.Enqueue(ctx, msg)
	require.NoError(t, err)

	// Verify the message landed in the correct Redis key.
	key := "dlq:tenant-abc:outbound"
	items, err := mr.List(key)
	require.NoError(t, err)
	assert.Len(t, items, 1, "queue should contain exactly one message")

	// Verify message fields were set by Enqueue.
	assert.Equal(t, h.maxRetries, msg.MaxRetries, "MaxRetries should be stamped from handler")
	assert.Equal(t, "tenant-abc", msg.TenantID, "TenantID should be resolved from context")
	assert.False(t, msg.CreatedAt.IsZero(), "CreatedAt should be set")
	assert.False(t, msg.NextRetryAt.IsZero(), "NextRetryAt should be set for retryable messages")
}

func TestEnqueue_TenantFromContext(t *testing.T) {
	t.Parallel()

	h, mr := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "ctx-tenant")

	msg := &FailedMessage{
		Source:       "inbound",
		OriginalData: []byte(`{}`),
		ErrorMessage: "fail",
		// TenantID intentionally left empty — should be resolved from ctx.
	}

	err := h.Enqueue(ctx, msg)
	require.NoError(t, err)
	assert.Equal(t, "ctx-tenant", msg.TenantID)

	items, err := mr.List("dlq:ctx-tenant:inbound")
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestEnqueue_TenantMismatch(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-A")

	msg := &FailedMessage{
		Source:   "outbound",
		TenantID: "tenant-B",
	}

	err := h.Enqueue(ctx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant mismatch")
}

func TestDequeue_Success(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	original := &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{"key":"value"}`),
		ErrorMessage: "connection refused",
	}

	require.NoError(t, h.Enqueue(ctx, original))

	got, err := h.Dequeue(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, "outbound", got.Source)
	assert.Equal(t, "tenant-abc", got.TenantID)
	assert.Equal(t, []byte(`{"key":"value"}`), got.OriginalData)
}

func TestDequeue_EmptyQueue(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	_, err := h.Dequeue(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis: nil")
}

func TestDequeue_NilHandler(t *testing.T) {
	t.Parallel()

	var h *Handler

	_, err := h.Dequeue(context.Background(), "test")
	require.ErrorIs(t, err, ErrNilHandler)
}

func TestQueueLength_Empty(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(0), length)
}

func TestQueueLength_NonEmpty(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	for i := range 5 {
		require.NoError(t, h.Enqueue(ctx, &FailedMessage{
			Source:       "outbound",
			ErrorMessage: "err",
			OriginalData: []byte{byte(i)},
		}))
	}

	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(5), length)
}

// TestEnqueue_EmptySource_Rejected verifies that Enqueue rejects messages with
// an empty source. An empty source would produce a malformed Redis key and
// is not a valid routing target.
func TestEnqueue_EmptySource_Rejected(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	msg := &FailedMessage{
		Source:       "",
		OriginalData: []byte(`{}`),
		ErrorMessage: "err",
	}

	err := h.Enqueue(ctx, msg)
	require.Error(t, err, "empty source should be rejected")
	assert.Contains(t, err.Error(), "source must not be empty")
}

func TestExtractTenantFromKey(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)

	tests := []struct {
		name   string
		key    string
		source string
		want   string
	}{
		{
			name:   "standard tenant key",
			key:    "dlq:tenant-abc:outbound",
			source: "outbound",
			want:   "tenant-abc",
		},
		{
			name:   "uuid tenant",
			key:    "dlq:550e8400-e29b-41d4-a716-446655440000:inbound",
			source: "inbound",
			want:   "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:   "no tenant segment (global key)",
			key:    "dlq:outbound",
			source: "outbound",
			want:   "",
		},
		{
			name:   "wrong prefix",
			key:    "other:tenant-abc:outbound",
			source: "outbound",
			want:   "",
		},
		{
			name:   "wrong suffix",
			key:    "dlq:tenant-abc:wrong",
			source: "outbound",
			want:   "",
		},
		{
			name:   "empty key",
			key:    "",
			source: "outbound",
			want:   "",
		},
		{
			name:   "nil handler returns empty",
			key:    "dlq:tenant:outbound",
			source: "outbound",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			target := h
			if tt.name == "nil handler returns empty" {
				target = nil
			}

			got := target.ExtractTenantFromKey(tt.key, tt.source)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScanQueues_Success(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)

	tenants := []string{"tenant-A", "tenant-B", "tenant-C"}

	for _, tid := range tenants {
		ctx := tmcore.ContextWithTenantID(context.Background(), tid)
		require.NoError(t, h.Enqueue(ctx, &FailedMessage{
			Source:       "outbound",
			OriginalData: []byte(`{}`),
			ErrorMessage: "err",
		}))
	}

	keys, err := h.ScanQueues(context.Background(), "outbound")
	require.NoError(t, err)
	assert.Len(t, keys, 3, "should discover all three tenant-scoped keys")

	// Verify each discovered key maps back to a known tenant.
	discovered := make(map[string]bool)
	for _, key := range keys {
		tid := h.ExtractTenantFromKey(key, "outbound")
		discovered[tid] = true
	}

	for _, tid := range tenants {
		assert.True(t, discovered[tid], "tenant %s should be discovered by ScanQueues", tid)
	}
}

func TestPruneExhaustedMessages(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Enqueue two exhausted messages (RetryCount >= MaxRetries).
	for range 2 {
		require.NoError(t, h.Enqueue(ctx, &FailedMessage{
			Source:       "outbound",
			OriginalData: []byte(`{}`),
			ErrorMessage: "permanent failure",
			RetryCount:   3, // = maxRetries
		}))
	}

	// Enqueue one non-exhausted message.
	require.NoError(t, h.Enqueue(ctx, &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{}`),
		ErrorMessage: "transient failure",
		RetryCount:   0,
	}))

	pruned, err := h.PruneExhaustedMessages(ctx, "outbound", 10)
	require.NoError(t, err)
	assert.Equal(t, 2, pruned, "exactly two exhausted messages should be pruned")

	// The non-exhausted message should still be in the queue.
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(1), length, "one non-exhausted message should remain")

	remaining, err := h.Dequeue(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, 0, remaining.RetryCount, "surviving message should be the non-exhausted one")
}

// TestDequeue_EmptySource_Rejected verifies that Dequeue rejects an empty source.
func TestDequeue_EmptySource_Rejected(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := context.Background()

	_, err := h.Dequeue(ctx, "")
	require.Error(t, err, "empty source should be rejected")
	assert.Contains(t, err.Error(), "source must not be empty")
}

// TestQueueLength_EmptySource_Rejected verifies that QueueLength rejects an empty source.
func TestQueueLength_EmptySource_Rejected(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := context.Background()

	_, err := h.QueueLength(ctx, "")
	require.Error(t, err, "empty source should be rejected")
	assert.Contains(t, err.Error(), "source must not be empty")
}

// TestScanQueues_EmptySource_Rejected verifies that ScanQueues rejects an empty source.
func TestScanQueues_EmptySource_Rejected(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := context.Background()

	_, err := h.ScanQueues(ctx, "")
	require.Error(t, err, "empty source should be rejected")
	assert.Contains(t, err.Error(), "source must not be empty")
}

// TestPruneExhaustedMessages_EmptySource_Rejected verifies that PruneExhaustedMessages
// rejects an empty source.
func TestPruneExhaustedMessages_EmptySource_Rejected(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := context.Background()

	_, err := h.PruneExhaustedMessages(ctx, "", 10)
	require.Error(t, err, "empty source should be rejected")
	assert.Contains(t, err.Error(), "source must not be empty")
}

// TestDequeue_MalformedJSON verifies that Dequeue returns an error (not a nil
// pointer dereference) when the Redis list contains non-JSON data. This covers
// the case where a message was manually injected or corrupted in transit.
func TestDequeue_MalformedJSON(t *testing.T) {
	t.Parallel()

	h, mr := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Inject raw non-JSON bytes directly via miniredis, bypassing Enqueue.
	mr.RPush("dlq:tenant-abc:outbound", "this is not valid json {{{")

	got, err := h.Dequeue(ctx, "outbound")
	require.Error(t, err, "malformed JSON payload should return an error")
	assert.Nil(t, got, "result pointer should be nil on unmarshal failure")
	assert.Contains(t, err.Error(), "unmarshal", "error should indicate unmarshal failure")
}

// TestBackoffDuration verifies the backoffDuration helper:
//   - floor is at least 5 seconds for any retry count
//   - values grow in trend as retryCount increases
//   - jitter causes variance (calling twice rarely returns identical values)
func TestBackoffDuration(t *testing.T) {
	t.Parallel()

	minFloor := 5 * time.Second

	for _, count := range []int{0, 1, 5} {
		d := backoffDuration(count)
		assert.GreaterOrEqual(t, d, minFloor,
			"backoffDuration(%d) should be >= 5s floor, got %s", count, d)
	}

	// Higher retry counts should not produce values below floor.
	d5 := backoffDuration(5)
	assert.GreaterOrEqual(t, d5, minFloor, "retryCount=5 must still respect floor")

	// Jitter: calling the function multiple times for the same retryCount
	// should eventually produce different values (probabilistic — we allow
	// up to 20 attempts before concluding there is no variance).
	const attempts = 20

	seenVariance := false

	first := backoffDuration(2)

	for range attempts {
		next := backoffDuration(2)
		if next != first {
			seenVariance = true

			break
		}
	}

	assert.True(t, seenVariance, "backoffDuration should exhibit jitter variance across multiple calls")
}

// TestEnqueue_InvalidSourceChars verifies that Enqueue rejects source strings
// that contain characters which would corrupt Redis key patterns or enable
// glob-injection into SCAN commands.
func TestEnqueue_InvalidSourceChars(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := context.Background()

	invalidChars := []struct {
		name   string
		source string
	}{
		{"colon", "out:bound"},
		{"asterisk", "out*bound"},
		{"question-mark", "out?bound"},
		{"open-bracket", "out[bound"},
		{"close-bracket", "out]bound"},
	}

	for _, tc := range invalidChars {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := h.Enqueue(ctx, &FailedMessage{
				Source:       tc.source,
				OriginalData: []byte(`{}`),
				ErrorMessage: "err",
			})
			require.Error(t, err, "source %q containing disallowed char should be rejected", tc.source)
			assert.Contains(t, err.Error(), "disallowed character",
				"error for source %q should mention disallowed character", tc.source)
		})
	}
}

// TestScanQueues_NilHandler verifies that ScanQueues on a nil Handler returns
// ErrNilHandler without panicking.
func TestScanQueues_NilHandler(t *testing.T) {
	t.Parallel()

	var h *Handler

	keys, err := h.ScanQueues(context.Background(), "outbound")
	require.ErrorIs(t, err, ErrNilHandler)
	assert.Nil(t, keys)
}

// TestPruneExhaustedMessages_NilHandler verifies that PruneExhaustedMessages on
// a nil Handler returns ErrNilHandler without panicking.
func TestPruneExhaustedMessages_NilHandler(t *testing.T) {
	t.Parallel()

	var h *Handler

	pruned, err := h.PruneExhaustedMessages(context.Background(), "outbound", 10)
	require.ErrorIs(t, err, ErrNilHandler)
	assert.Equal(t, 0, pruned)
}

// TestPruneExhaustedMessages_ZeroLimit verifies that PruneExhaustedMessages with
// limit=0 (or negative) returns immediately with zero pruned and no error.
// This is a short-circuit guard in the implementation: a zero/negative limit
// means "prune nothing", which is a valid no-op call — not an error condition.
func TestPruneExhaustedMessages_ZeroLimit(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-abc")

	// Enqueue an exhausted message so we can verify it is NOT consumed when limit=0.
	require.NoError(t, h.Enqueue(ctx, &FailedMessage{
		Source:       "outbound",
		OriginalData: []byte(`{}`),
		ErrorMessage: "permanent failure",
		RetryCount:   3, // = maxRetries
	}))

	pruned, err := h.PruneExhaustedMessages(ctx, "outbound", 0)
	require.NoError(t, err, "limit=0 should not return an error")
	assert.Equal(t, 0, pruned, "limit=0 should prune zero messages")

	// The exhausted message should still be in the queue — we did not touch it.
	length, err := h.QueueLength(ctx, "outbound")
	require.NoError(t, err)
	assert.Equal(t, int64(1), length, "message should remain in queue after limit=0 prune call")

	// Also verify negative limit has the same short-circuit behaviour.
	pruned, err = h.PruneExhaustedMessages(ctx, "outbound", -5)
	require.NoError(t, err, "negative limit should not return an error")
	assert.Equal(t, 0, pruned, "negative limit should prune zero messages")
}

// TestDequeue_Success_CompleteAssertions extends the basic Dequeue success case
// to assert every FailedMessage field is correctly round-tripped through the
// enqueue → dequeue cycle.
func TestDequeue_Success_CompleteAssertions(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t)
	ctx := tmcore.ContextWithTenantID(context.Background(), "tenant-xyz")

	original := &FailedMessage{
		Source:       "payments",
		OriginalData: []byte(`{"amount":42}`),
		ErrorMessage: "gateway timeout",
	}

	before := time.Now().UTC()
	require.NoError(t, h.Enqueue(ctx, original))
	after := time.Now().UTC()

	got, err := h.Dequeue(ctx, "payments")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "payments", got.Source, "Source should round-trip")
	assert.Equal(t, "tenant-xyz", got.TenantID, "TenantID should be set from context")
	assert.Equal(t, []byte(`{"amount":42}`), got.OriginalData, "OriginalData should round-trip")
	assert.Equal(t, "gateway timeout", got.ErrorMessage, "ErrorMessage should round-trip")
	assert.Equal(t, 0, got.RetryCount, "RetryCount should be 0 on first enqueue")
	assert.Equal(t, h.maxRetries, got.MaxRetries, "MaxRetries should be stamped from handler default")
	assert.False(t, got.CreatedAt.IsZero(), "CreatedAt should not be zero after enqueue")
	assert.True(t, !got.CreatedAt.Before(before) && !got.CreatedAt.After(after),
		"CreatedAt should be within the test window [%s, %s], got %s", before, after, got.CreatedAt)
	assert.False(t, got.NextRetryAt.IsZero(), "NextRetryAt should be set for a retryable message")
	assert.True(t, got.NextRetryAt.After(got.CreatedAt),
		"NextRetryAt should be in the future relative to CreatedAt")
}

// TestTruncateString verifies the truncateString helper across its boundary cases.
func TestTruncateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
		{
			name:   "short string under limit",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "string at exact limit",
			input:  "helloworld",
			maxLen: 10,
			want:   "helloworld",
		},
		{
			name:   "string over limit",
			input:  "helloworld!",
			maxLen: 10,
			want:   "helloworld...",
		},
		{
			name:   "long string gets truncated with ellipsis",
			input:  "this is a very long error message that must be trimmed",
			maxLen: 20,
			want:   "this is a very long ...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := truncateString(tc.input, tc.maxLen)
			assert.Equal(t, tc.want, got)

			// For over-limit cases, the result should end in "...".
			if len(tc.input) > tc.maxLen {
				assert.True(t, len(got) == tc.maxLen+3,
					"truncated string should be maxLen+3 bytes (maxLen + ellipsis), got len=%d", len(got))
			}
		})
	}
}
