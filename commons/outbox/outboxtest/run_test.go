//go:build unit

package outboxtest

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// TestRun_AllSkipped exercises the Run framework with all subtests skipped,
// covering the option processing and subtest dispatch code without needing a real DB.
func TestRun_AllSkipped(t *testing.T) {
	t.Parallel()

	// Factory that never actually creates a repo (not called since all subtests are skipped)
	factory := func(_ *testing.T) outbox.OutboxRepository {
		return nil
	}

	// Skip all subtests
	Run(t, factory,
		SkipSubtest("CreateThenGetRoundtrip"),
		SkipSubtest("CreateForcesPendingLifecycleInvariants"),
		SkipSubtest("CreateWithTx"),
		SkipSubtest("EmptyRepositoryReturnsNoRows"),
		SkipSubtest("ListPendingClaimsProcessing"),
		SkipSubtest("ConcurrentListPendingClaimsUnique"),
		SkipSubtest("ListPendingByTypeFilters"),
		SkipSubtest("MarkPublishedAfterClaim"),
		SkipSubtest("StateTransitionsRequireProcessing"),
		SkipSubtest("MarkFailedRedactsSensitiveData"),
		SkipSubtest("MarkFailedAtMaxAttemptsInvalidates"),
		SkipSubtest("ListFailedForRetryReadOnly"),
		SkipSubtest("RetryScansSkipRowsAtMaxAttempts"),
		SkipSubtest("ResetForRetryMovesFailedToProcessing"),
		SkipSubtest("ResetStuckProcessingReprocessesAndInvalidates"),
		SkipSubtest("TenantIsolationAndDiscovery"),
		SkipSubtest("WrongTenantMutationsRejected"),
		SkipSubtest("DispatcherLifecyclePersistsPublishedState"),
	)
}
