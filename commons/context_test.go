//go:build unit

package commons

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

func TestWithTimeoutSafe_NilParent(t *testing.T) {
	ctx, cancel, err := WithTimeoutSafe(nil, 5*time.Second)

	if ctx != nil {
		t.Error("expected nil context")
	}

	if err == nil {
		t.Fatal("expected error for nil parent")
	}

	if !errors.Is(err, ErrNilParentContext) {
		t.Errorf("expected ErrNilParentContext, got %v", err)
	}

	if cancel != nil {
		t.Error("expected nil cancel function")
	}
}

func TestWithTimeoutSafe_Success(t *testing.T) {
	parent := context.Background()
	timeout := 5 * time.Second

	ctx, cancel, err := WithTimeoutSafe(parent, timeout)
	defer cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ctx == nil {
		t.Fatal("expected non-nil context")
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have a deadline")
	}

	timeUntil := time.Until(deadline)
	if timeUntil < 4800*time.Millisecond || timeUntil > 5200*time.Millisecond {
		t.Errorf("deadline not within expected range: got %.2fs remaining, expected ~5s", timeUntil.Seconds())
	}
}

func TestWithTimeoutSafe_ParentDeadlineShorter(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer parentCancel()

	ctx, cancel, err := WithTimeoutSafe(parent, 10*time.Second)
	defer cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have a deadline")
	}

	timeUntil := time.Until(deadline)
	if timeUntil > 2*time.Second || timeUntil < 1*time.Second {
		t.Errorf("expected deadline to be ~2s from now, got %v", timeUntil)
	}
}

func TestWithTimeoutSafe_CancelWorks(t *testing.T) {
	parent := context.Background()
	ctx, cancel, err := WithTimeoutSafe(parent, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cancel()

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("context was not cancelled")
	}

	if ctx.Err() != context.Canceled {
		t.Errorf("expected context.Canceled error, got %v", ctx.Err())
	}
}

func TestWithTimeoutSafe_ZeroTimeout(t *testing.T) {
	parent := context.Background()
	ctx, cancel, err := WithTimeoutSafe(parent, 0)
	defer cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ctx == nil {
		t.Fatal("expected non-nil context")
	}

	// Zero timeout should create an already-expired context
	select {
	case <-ctx.Done():
		// Expected - context should be done immediately or very soon
	case <-time.After(100 * time.Millisecond):
		t.Error("expected context to be done with zero timeout")
	}
}

func TestWithTimeoutSafe_NegativeTimeout(t *testing.T) {
	parent := context.Background()
	ctx, cancel, err := WithTimeoutSafe(parent, -1*time.Second)
	defer cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ctx == nil {
		t.Fatal("expected non-nil context")
	}

	// Negative timeout should create an already-expired context
	select {
	case <-ctx.Done():
		// Expected - context should be done immediately
	case <-time.After(100 * time.Millisecond):
		t.Error("expected context to be done with negative timeout")
	}
}

// ---- Logger context helpers ----

func TestNewLoggerFromContext(t *testing.T) {
	t.Parallel()

	t.Run("without_logger", func(t *testing.T) {
		t.Parallel()

		logger := NewLoggerFromContext(context.Background())
		require.NotNil(t, logger)
		assert.IsType(t, &log.NopLogger{}, logger)
	})

	t.Run("with_logger", func(t *testing.T) {
		t.Parallel()

		nop := &log.NopLogger{}
		ctx := ContextWithLogger(context.Background(), nop)
		logger := NewLoggerFromContext(ctx)
		assert.Equal(t, nop, logger)
	})

	t.Run("nil_ctx_returns_nop_logger", func(t *testing.T) {
		t.Parallel()

		//nolint:staticcheck // SA1012: intentionally passing nil context to test nil-safety
		logger := NewLoggerFromContext(nil)
		require.NotNil(t, logger, "nil ctx must not panic, must return NopLogger")
		assert.IsType(t, &log.NopLogger{}, logger)
	})
}

func TestContextWithLogger(t *testing.T) {
	t.Parallel()

	nop := &log.NopLogger{}
	ctx := ContextWithLogger(context.Background(), nop)
	v := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	assert.Equal(t, nop, v.Logger)
}

// ---- Tracer context helpers ----

func TestContextWithTracer(t *testing.T) {
	t.Parallel()

	tracer := otel.Tracer("test")
	ctx := ContextWithTracer(context.Background(), tracer)
	v := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	assert.Equal(t, tracer, v.Tracer)
}

// ---- MetricFactory context helpers ----

func TestContextWithMetricFactory(t *testing.T) {
	t.Parallel()

	ctx := ContextWithMetricFactory(context.Background(), nil)
	v := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	assert.Nil(t, v.MetricFactory)
}

// ---- HeaderID context helpers ----

func TestContextWithHeaderID(t *testing.T) {
	t.Parallel()

	ctx := ContextWithHeaderID(context.Background(), "hdr-123")
	v := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	assert.Equal(t, "hdr-123", v.HeaderID)
}

// ---- Tracking bundle ----

func TestNewTrackingFromContext(t *testing.T) {
	t.Parallel()

	t.Run("empty_context_returns_defaults", func(t *testing.T) {
		t.Parallel()

		logger, tracer, headerID, mf := NewTrackingFromContext(context.Background())
		assert.NotNil(t, logger)
		assert.NotNil(t, tracer)
		assert.NotEmpty(t, headerID)
		assert.NotNil(t, mf)
	})

	t.Run("full_context", func(t *testing.T) {
		t.Parallel()

		nop := &log.NopLogger{}
		tracer := otel.Tracer("test-tracer")
		ctx := ContextWithLogger(context.Background(), nop)
		ctx = ContextWithTracer(ctx, tracer)
		ctx = ContextWithHeaderID(ctx, "id-456")

		logger, tr, hid, mf := NewTrackingFromContext(ctx)
		assert.Equal(t, nop, logger)
		assert.Equal(t, tracer, tr)
		assert.Equal(t, "id-456", hid)
		assert.NotNil(t, mf)
	})

	t.Run("nil_values_get_defaults", func(t *testing.T) {
		t.Parallel()

		ctx := context.WithValue(context.Background(), CustomContextKey, &CustomContextKeyValue{})

		logger, tracer, headerID, mf := NewTrackingFromContext(ctx)
		assert.IsType(t, &log.NopLogger{}, logger)
		assert.NotNil(t, tracer)
		assert.NotEmpty(t, headerID)
		assert.NotNil(t, mf)
	})

	t.Run("nil_ctx_returns_defaults", func(t *testing.T) {
		t.Parallel()

		//nolint:staticcheck // SA1012: intentionally testing nil ctx
		logger, tracer, headerID, mf := NewTrackingFromContext(nil)
		assert.NotNil(t, logger)
		assert.NotNil(t, tracer)
		assert.NotEmpty(t, headerID)
		assert.NotNil(t, mf)
	})
}

// ---- Attribute Bag ----

func TestContextWithSpanAttributes(t *testing.T) {
	t.Parallel()

	t.Run("empty_kvs_returns_same_ctx", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		ctx2 := ContextWithSpanAttributes(ctx)
		assert.Equal(t, ctx, ctx2)
	})

	t.Run("nil_ctx_with_no_attrs_returns_non_nil", func(t *testing.T) {
		t.Parallel()

		//nolint:staticcheck // SA1012: intentionally testing nil ctx
		result := ContextWithSpanAttributes(nil)
		assert.NotNil(t, result, "nil ctx + no attrs must return context.Background(), not nil")
	})

	t.Run("nil_ctx_with_attrs_returns_non_nil", func(t *testing.T) {
		t.Parallel()

		//nolint:staticcheck // SA1012: intentionally testing nil ctx
		result := ContextWithSpanAttributes(nil, attribute.String("k", "v"))
		assert.NotNil(t, result, "nil ctx + attrs must return valid context, not nil")

		attrs := AttributesFromContext(result)
		assert.Len(t, attrs, 1)
	})

	t.Run("appends_attributes", func(t *testing.T) {
		t.Parallel()

		ctx := ContextWithSpanAttributes(context.Background(),
			attribute.String("tenant.id", "t1"),
		)
		ctx = ContextWithSpanAttributes(ctx,
			attribute.String("region", "us"),
		)

		attrs := AttributesFromContext(ctx)
		assert.Len(t, attrs, 2)
	})
}

func TestAttributesFromContext(t *testing.T) {
	t.Parallel()

	t.Run("no_attributes", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, AttributesFromContext(context.Background()))
	})

	t.Run("returns_copy", func(t *testing.T) {
		t.Parallel()

		ctx := ContextWithSpanAttributes(context.Background(),
			attribute.String("k", "v"),
		)

		a1 := AttributesFromContext(ctx)
		a2 := AttributesFromContext(ctx)
		assert.Equal(t, a1, a2)

		// Mutating the copy should not affect next retrieval.
		a1[0] = attribute.String("k", "changed")
		a3 := AttributesFromContext(ctx)
		assert.Equal(t, "v", a3[0].Value.AsString())
	})
}

func TestReplaceAttributes(t *testing.T) {
	t.Parallel()

	ctx := ContextWithSpanAttributes(context.Background(),
		attribute.String("old", "val"),
	)

	ctx = ReplaceAttributes(ctx, attribute.String("new", "val2"))

	attrs := AttributesFromContext(ctx)
	require.Len(t, attrs, 1)
	assert.Equal(t, "new", string(attrs[0].Key))
}
