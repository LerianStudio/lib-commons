//go:build integration

// Tenant-scoped contract integration tests for the Postgres store.
//
// This file wraps the existing testcontainers helpers from
// postgres_integration_test.go (startPostgresContainer, newTestStore,
// tableSeq) and re-runs systemplanetest.Run — which now includes the
// tenant sub-suite — against a fresh Postgres 17 container.
//
// Postgres supports DELETE change-stream events natively (the trigger is
// extended to AFTER INSERT OR UPDATE OR DELETE in postgres_schema.go), so
// no subtests are skipped at this layer.
package postgres

import (
	"context"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver registration

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

// TestIntegration_PostgresTenantContracts spins up a single Postgres 17
// testcontainer and runs the full systemplanetest.Run suite — global +
// tenant contracts — against it. Each subtest gets its own table (via the
// tableSeq counter in postgres_integration_test.go) so concurrent sub-tests
// do not see each other's writes.
//
// The container lifetime spans the outer test; individual subtests share
// the container but get isolated tables via newTestStore. This matches the
// pre-existing TestIntegration_ContractSuite pattern.
func TestIntegration_PostgresTenantContracts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn := startPostgresContainer(t, ctx)

	systemplanetest.Run(t, func(t *testing.T) store.Store {
		return newTestStore(t, dsn)
	})
}
