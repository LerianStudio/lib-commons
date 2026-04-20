//go:build unit

package streaming

import (
	"context"
	"testing"
)

// TestNoopEmitter_AllNil covers DX-C05: every method on NewNoopEmitter must
// return nil. Idempotent Close, silent Emit, always-Healthy.
func TestNoopEmitter_AllNil(t *testing.T) {
	t.Parallel()

	e := NewNoopEmitter()
	if e == nil {
		t.Fatal("NewNoopEmitter returned nil")
	}

	ctx := context.Background()

	if err := e.Emit(ctx, Event{ResourceType: "transaction", EventType: "created", TenantID: "t-1"}); err != nil {
		t.Errorf("Emit() = %v; want nil", err)
	}

	if err := e.Healthy(ctx); err != nil {
		t.Errorf("Healthy() = %v; want nil", err)
	}

	if err := e.Close(); err != nil {
		t.Errorf("Close() = %v; want nil", err)
	}
	// Idempotent — second close still nil.
	if err := e.Close(); err != nil {
		t.Errorf("second Close() = %v; want nil", err)
	}
}

// TestNoopEmitter_SatisfiesInterface is a compile-time proof that NoopEmitter
// is assignable to Emitter.
func TestNoopEmitter_SatisfiesInterface(t *testing.T) {
	t.Parallel()

	var _ Emitter = NewNoopEmitter()
}
