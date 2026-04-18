//go:build unit

package streaming

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// TestProducer_EmitClosed: after Close, Emit returns ErrEmitterClosed
// synchronously; no broker I/O.
func TestProducer_EmitClosed(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	err = emitter.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrEmitterClosed) {
		t.Fatalf("Emit err = %v; want ErrEmitterClosed", err)
	}
}

// TestProducer_Close_Idempotent: repeated Close calls all return nil.
func TestProducer_Close_Idempotent(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := emitter.Close(); err != nil {
			t.Errorf("Close iteration %d err = %v; want nil", i, err)
		}
	}
}

// TestProducer_Healthy_OK: Healthy returns nil against a live kfake broker.
func TestProducer_Healthy_OK(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := emitter.Healthy(ctx); err != nil {
		t.Fatalf("Healthy err = %v; want nil", err)
	}
}

// TestProducer_Healthy_Degraded_AfterClusterShutdown: close the kfake
// cluster, then call Healthy — Ping fails, Healthy returns a *HealthError
// with State()=Degraded.
func TestProducer_Healthy_Degraded_AfterClusterShutdown(t *testing.T) {
	cfg, cluster := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	// Kill the broker so Ping fails.
	cluster.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = emitter.Healthy(ctx)
	if err == nil {
		t.Fatal("Healthy returned nil after cluster shutdown; want *HealthError")
	}

	var he *HealthError
	if !errors.As(err, &he) {
		t.Fatalf("Healthy err = %T; want *HealthError", err)
	}

	if he.State() != Degraded {
		t.Errorf("Healthy state = %q; want %q", he.State(), Degraded)
	}
}

// TestProducer_Healthy_Down_AfterClose: after Close, Healthy returns
// State()=Down.
func TestProducer_Healthy_Down_AfterClose(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	err = emitter.Healthy(context.Background())
	if err == nil {
		t.Fatal("Healthy after Close returned nil; want *HealthError")
	}

	var he *HealthError
	if !errors.As(err, &he) {
		t.Fatalf("Healthy err = %T; want *HealthError", err)
	}

	if he.State() != Down {
		t.Errorf("Healthy state = %q; want %q", he.State(), Down)
	}
}

// TestProducer_CloseContext_RespectsDeadline: CloseContext under a canceled
// context still completes (Flush returns immediately, Close always runs).
func TestProducer_CloseContext_RespectsDeadline(t *testing.T) {
	cfg, _ := kfakeConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// CloseContext with a canceled ctx: Flush should return immediately
	// with the ctx err; Close still runs. Result is either nil (nothing to
	// flush) or an error wrapping ctx.Err() — both are acceptable by
	// contract; the key property is that this doesn't hang.
	done := make(chan error, 1)
	go func() {
		done <- p.CloseContext(canceledCtx)
	}()

	select {
	case <-done:
		// Great — returned quickly.
	case <-time.After(3 * time.Second):
		t.Fatal("CloseContext hung past 3s under canceled ctx")
	}
}
