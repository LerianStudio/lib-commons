//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// insertAgedEvent writes one row with an explicit status and created_at through
// the given INSERT target (a quoted table path), bypassing the repository.
func insertAgedEvent(t *testing.T, db *sql.DB, target, status string, createdAt time.Time) uuid.UUID {
	t.Helper()

	id := uuid.New()
	_, err := db.ExecContext(context.Background(), fmt.Sprintf(`
INSERT INTO %s (id, event_type, aggregate_id, payload, status, attempts, created_at, updated_at)
VALUES ($1, 'payment.purge', $2, '{}', $3::outbox_event_status, 0, $4, $4)`, target), // #nosec G201 -- test-owned identifiers
		id, uuid.New(), status, createdAt)
	require.NoError(t, err)

	return id
}

func TestIntegration_Retention_SchemaModeSweepsOnlyTheTenantSchema(t *testing.T) {
	dsn, cleanup := integrationPostgresDSN(t)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	fx, schemaA, schemaB := newSchemaModeIntegrationRepoFixture(t, dsn)
	table := func(schema string) string { return quoteIdentifier(schema) + "." + quoteIdentifier(fx.tableName) }
	aged := time.Now().UTC().Add(-2 * time.Hour)

	publishedA := insertAgedEvent(t, fx.primaryDB, table(schemaA), outbox.OutboxStatusPublished, aged)
	pendingA := insertAgedEvent(t, fx.primaryDB, table(schemaA), outbox.OutboxStatusPending, aged)
	publishedB := insertAgedEvent(t, fx.primaryDB, table(schemaB), outbox.OutboxStatusPublished, aged)

	deleted, err := fx.repo.DeletePublishedBefore(fx.tenantCtx, time.Now().UTC().Add(-time.Hour), nil, 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	require.Zero(t, countOutboxRowsByIDInSchema(t, fx, schemaA, publishedA))
	require.Equal(t, 1, countOutboxRowsByIDInSchema(t, fx, schemaA, pendingA))
	require.Equal(t, 1, countOutboxRowsByIDInSchema(t, fx, schemaB, publishedB), "tenant B's schema is untouched")
}

func TestIntegration_Retention_PoolModeSkipsTenantWithoutTable(t *testing.T) {
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a", "tenant_d"},
		map[string]bool{"root_db": true, "tenant_a": true, "tenant_d": false},
	)

	resolver := newMapPoolResolver(
		map[string]*sql.DB{poolTenantA: h.pools["tenant_a"], poolTenantD: h.pools["tenant_d"]},
		[]string{poolTenantA, poolTenantD},
	)
	repo := newPoolRepo(t, h, resolver)

	aged := time.Now().UTC().Add(-2 * time.Hour)
	cutoff := time.Now().UTC().Add(-time.Hour)
	publishedA := insertAgedEvent(t, h.pools["tenant_a"], "outbox_events", outbox.OutboxStatusPublished, aged)

	deleted, err := repo.DeletePublishedBefore(outbox.ContextWithTenantID(context.Background(), poolTenantD), cutoff, nil, 100)
	require.NoError(t, err, "a tenant without the outbox table is skipped, not failed")
	require.Zero(t, deleted)

	deleted, err = repo.DeletePublishedBefore(outbox.ContextWithTenantID(context.Background(), poolTenantA), cutoff, nil, 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	_, found := statusInDB(t, h.pools["tenant_a"], publishedA)
	require.False(t, found)
}

// TestIntegration_Retention_ColumnModeSweepsIdleTenant runs the real dispatcher
// over column-per-tenant discovery with nothing pending, so dispatch discovery
// lists no tenant at all.
func TestIntegration_Retention_ColumnModeSweepsIdleTenant(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	now := time.Now().UTC()
	agedID, recentID := uuid.New(), uuid.New()
	insertTenantEvent(t, fx, "tenant-idle", agedID, now.Add(-2*time.Hour))
	insertTenantEvent(t, fx, "tenant-idle", recentID, now.Add(-30*time.Minute))

	dispatcher, err := outbox.NewDispatcher(fx.repo, outbox.NewHandlerRegistry(), nil, noop.NewTracerProvider().Tracer("test"),
		outbox.WithRetentionPublished(time.Hour))
	require.NoError(t, err)

	runDispatcherUntil(t, dispatcher, func() bool { return !tenantRowExists(t, fx, "tenant-idle", agedID) })

	require.True(t, tenantRowExists(t, fx, "tenant-idle", recentID), "a PUBLISHED row younger than the window stays")
}

// insertTenantEvent writes one PUBLISHED column-mode row, bypassing the repository.
func insertTenantEvent(t *testing.T, fx *integrationRepoFixture, tenantID string, id uuid.UUID, createdAt time.Time) {
	t.Helper()

	_, err := fx.primaryDB.ExecContext(context.Background(), fmt.Sprintf(`
INSERT INTO %s (id, event_type, aggregate_id, payload, status, attempts, created_at, updated_at, tenant_id)
VALUES ($1, 'payment.purge', $2, '{}', 'PUBLISHED', 1, $3, $3, $4)`, quoteIdentifier(fx.tableName)), // #nosec G201 -- test-owned identifiers
		id, uuid.New(), createdAt, tenantID)
	require.NoError(t, err)
}

func tenantRowExists(t *testing.T, fx *integrationRepoFixture, tenantID string, id uuid.UUID) bool {
	t.Helper()

	var count int
	require.NoError(t, fx.primaryDB.QueryRowContext(context.Background(), fmt.Sprintf( // #nosec G201 -- test-owned identifiers
		"SELECT COUNT(*) FROM %s WHERE tenant_id = $1 AND id = $2", quoteIdentifier(fx.tableName)), tenantID, id).Scan(&count))

	return count == 1
}
