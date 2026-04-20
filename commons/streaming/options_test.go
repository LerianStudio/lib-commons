//go:build unit

package streaming

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// TestOptions_ApplyAllSetsFields threads every EmitterOption through an
// empty emitterOptions and verifies each field is populated. This is a
// plumbing test only — no Producer construction is exercised; that lands
// in T2. Ensures each option function is at minimum non-broken so the T2
// harness does not hit compile errors first.
func TestOptions_ApplyAllSetsFields(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	partFn := func(_ Event) string { return "custom" }

	opts := &emitterOptions{}
	for _, apply := range []EmitterOption{
		WithLogger(logger),
		WithMetricsFactory(nil), // nil is a valid, supported value
		WithTracer(nil),         // nil is a valid, supported value
		WithCircuitBreakerManager(nil),
		WithPartitionKey(partFn),
		WithCloseTimeout(7 * time.Second),
		WithAllowSystemEvents(),
	} {
		apply(opts)
	}

	if opts.logger == nil {
		t.Error("WithLogger did not set opts.logger")
	}
	if opts.partitionKeyFn == nil {
		t.Error("WithPartitionKey did not set opts.partitionKeyFn")
	}
	if got := opts.partitionKeyFn(Event{}); got != "custom" {
		t.Errorf("opts.partitionKeyFn returned %q; want custom", got)
	}
	if opts.closeTimeout != 7*time.Second {
		t.Errorf("opts.closeTimeout = %v; want 7s", opts.closeTimeout)
	}
	if !opts.allowSystemEvents {
		t.Error("WithAllowSystemEvents did not set opts.allowSystemEvents")
	}
}
