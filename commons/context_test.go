package commons

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithTimeout_NoParentDeadline(t *testing.T) {
	parent := context.Background()
	timeout := 5 * time.Second

	ctx, cancel := WithTimeout(parent, timeout)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have a deadline")
	}

	expectedDeadline := time.Now().Add(timeout)
	// Allow 200ms variance for test execution time
	timeUntil := time.Until(deadline)
	if timeUntil < 4800*time.Millisecond || timeUntil > 5200*time.Millisecond {
		t.Errorf("deadline not within expected range: got %v (%.2fs remaining), expected ~%v (5s)",
			deadline, timeUntil.Seconds(), expectedDeadline)
	}
}

func TestWithTimeout_ParentDeadlineShorter(t *testing.T) {
	// Parent has 2s deadline
	parent, parentCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer parentCancel()

	// We request 10s, but parent's 2s should win
	ctx, cancel := WithTimeout(parent, 10*time.Second)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have a deadline")
	}

	// Should use parent's deadline (2s)
	timeUntil := time.Until(deadline)
	if timeUntil > 2*time.Second || timeUntil < 1*time.Second {
		t.Errorf("expected deadline to be ~2s from now, got %v", timeUntil)
	}
}

func TestWithTimeout_ParentDeadlineLonger(t *testing.T) {
	// Parent has 10s deadline
	parent, parentCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer parentCancel()

	// We request 2s, our timeout should win
	ctx, cancel := WithTimeout(parent, 2*time.Second)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have a deadline")
	}

	// Should use our timeout (2s)
	timeUntil := time.Until(deadline)
	// Allow 200ms variance
	if timeUntil < 1800*time.Millisecond || timeUntil > 2200*time.Millisecond {
		t.Errorf("expected deadline to be ~2s from now, got %v (%.2fs)", timeUntil, timeUntil.Seconds())
	}
}

func TestWithTimeout_CancelWorks(t *testing.T) {
	parent := context.Background()
	ctx, cancel := WithTimeout(parent, 5*time.Second)

	// Cancel immediately
	cancel()

	// Context should be cancelled
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

func TestWithTimeout_PanicOnNilParent(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil parent")
		}
	}()

	WithTimeout(nil, 5*time.Second)
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
