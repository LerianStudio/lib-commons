package commons

import (
	"context"
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
	// Allow 100ms variance for test execution time
	if time.Until(deadline) < 4*time.Second || time.Until(deadline) > 6*time.Second {
		t.Errorf("deadline not within expected range: got %v, expected ~%v", deadline, expectedDeadline)
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
	if timeUntil > 3*time.Second || timeUntil < 1*time.Second {
		t.Errorf("expected deadline to be ~2s from now, got %v", timeUntil)
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
