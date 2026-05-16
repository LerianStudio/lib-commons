//go:build unit

package storetest

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// TestRunTenantContracts_AllSkipped exercises the RunTenantContracts framework
// with all subtests skipped, covering the option processing and subtest dispatch code
// without needing a real database.
func TestRunTenantContracts_AllSkipped(t *testing.T) {
	t.Parallel()

	// Factory that never actually creates a store (not called since all subtests are skipped)
	factory := func(_ *testing.T) store.Store {
		return nil
	}

	// Skip all subtests
	RunTenantContracts(t, factory,
		SkipSubtest("TenantListOnEmpty"),
		SkipSubtest("SetTenantThenGetRoundtrip"),
		SkipSubtest("SetTenantTwiceLastWriteWins"),
		SkipSubtest("DeleteTenantValueReturnsMissing"),
		SkipSubtest("DeleteTenantValueIsIdempotent"),
		SkipSubtest("ListTenantsForKeySorted"),
		SkipSubtest("GlobalAndTenantRowsCoexist"),
		SkipSubtest("ListTenantOverrides_FiltersGlobalsServerSide"),
		SkipSubtest("TenantSubscribeReceivesDeleteEvent"),
		SkipSubtest("DeleteTenantValueNoOpEmitsNoEvent"),
		WithEventSettle(0),
	)
}
