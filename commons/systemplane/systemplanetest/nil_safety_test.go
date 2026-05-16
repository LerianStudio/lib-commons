//go:build unit

package systemplanetest_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

// TestRun_NilReceiverSafety exercises the NilReceiverSafety contract subtest
// without requiring a real backend. All other subtests are skipped.
func TestRun_NilReceiverSafety(t *testing.T) {
	t.Parallel()

	// Factory that returns nil - only used for NilReceiverSafety which explicitly creates a nil client
	factory := func(t *testing.T) *systemplane.Client {
		t.Helper()
		return nil
	}

	// Skip all subtests that require a real backend
	systemplanetest.Run(t, factory,
		systemplanetest.SkipSubtest("RegisterBeforeStart"),
		systemplanetest.SkipSubtest("StartAndCloseLifecycle"),
		systemplanetest.SkipSubtest("TypedReads"),
		systemplanetest.SkipSubtest("SetPersistence"),
		systemplanetest.SkipSubtest("OnChangeReceivesSet"),
		systemplanetest.SkipSubtest("DebounceDeliversLatestValue"),
		systemplanetest.SkipSubtest("PanicSafeSubscriberCallbacks"),
		systemplanetest.SkipSubtest("MultipleSubscribers"),
		systemplanetest.SkipSubtest("TenantScopedOverrideLifecycle"),
		systemplanetest.SkipSubtest("TenantOnChangeReceivesSet"),
		systemplanetest.SkipSubtest("TenantOnChangeReceivesDelete"),
		systemplanetest.SkipSubtest("TenantDeleteNoOpEmitsNoEvent"),
		systemplanetest.SkipSubtest("TenantContextValidation"),
		systemplanetest.SkipSubtest("ValidationFailures"),
		systemplanetest.SkipSubtest("RedactionMetadata"),
		// Only NilReceiverSafety runs - it uses a nil *systemplane.Client directly
	)
}
