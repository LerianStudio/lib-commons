//go:build integration

// Tenant-scoped contract integration tests for the MongoDB store.
//
// Two modes are exercised:
//
//   - Change-stream mode (replica set): MongoDB's native change streams
//     surface insert/update/replace/delete events, so the full
//     systemplanetest.Run suite passes unmodified.
//
//   - Polling mode (standalone): the subscribePoll loop polls for
//     documents updated after a watermark. It has no native delete signal
//     — deletions that happen between two ticks are invisible to the
//     polling path (see internal/mongodb/mongodb_changestream.go
//     subscribePoll godoc). We opt the contract suite out of the
//     "TenantSubscribeReceivesDeleteEvent" subtest via SkipSubtest to
//     reflect this known limitation without lying to the contract.
//
// Both suites share the helpers (setupMongoDB, newTestStore) defined in
// mongodb_integration_test.go.
package mongodb

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

// TestIntegration_MongoDBTenantContracts_ChangeStream runs the full
// systemplanetest.Run suite — including the tenant sub-suite — against a
// MongoDB replica set via change-stream subscription. The replica set
// bootstrap and connection validation live in setupMongoDB; if the host
// cannot reach the rs0 advertised address the helper calls t.Skip (Docker
// Desktop on macOS occasionally hits this).
func TestIntegration_MongoDBTenantContracts_ChangeStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	systemplanetest.Run(t, func(t *testing.T) store.Store {
		return newTestStore(t, client, 0) // PollInterval=0 -> change streams
	})
}

// TestIntegration_MongoDBTenantContracts_Polling runs the same suite
// against the polling subscription path. The "TenantSubscribeReceivesDeleteEvent"
// contract is skipped because inter-tick deletes are not visible in
// polling mode — the Client layer is expected to document this limitation
// for operators who choose standalone MongoDB.
//
// Polling interval is 100ms (same as the existing contract_suite_polling
// test) so the eventually() helper's 10s budget comfortably covers the
// delivery window.
func TestIntegration_MongoDBTenantContracts_Polling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	systemplanetest.Run(t, func(t *testing.T) store.Store {
		return newTestStore(t, client, 100*time.Millisecond)
	},
		// Polling mode cannot observe inter-tick deletes; see
		// subscribePoll godoc. Skip the delete-event contract rather than
		// silently weaken the assertion.
		systemplanetest.SkipSubtest("TenantSubscribeReceivesDeleteEvent"),
	)
}
