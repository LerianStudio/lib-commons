//go:build unit

package systemplane

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestClient_Close_NoGoroutineLeak pins the invariant called out in
// runSubscribe's doc comment: a goleak.VerifyNone immediately after Close
// returns must observe no transient leak. This regression test ensures the
// M5 fix in 4198ecd (cancel-bridge tracked in c.wg) does not silently
// regress — both the Subscribe goroutine and the cancel-bridge must drain
// before Close returns.
//
// t.Cleanup(c.Close) is NOT used here; the test calls Close explicitly
// before VerifyNone so the assertion runs at the point that matters.
func TestClient_Close_NoGoroutineLeak(t *testing.T) {
	fs := newFakeStore()

	cfg := defaultClientConfig()
	cfg.debounce = 0

	c := newClient(fs, cfg)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the Subscribe handler to be registered before closing — this
	// guarantees both the Subscribe goroutine and the cancel-bridge are
	// actually running at the moment Close fires.
	fs.waitForSubscriber(t, 2*time.Second)

	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	goleak.VerifyNone(t)
}
