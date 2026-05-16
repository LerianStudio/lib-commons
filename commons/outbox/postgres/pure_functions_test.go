//go:build unit

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// scanOutboxEvent — via mock scanner
// ---------------------------------------------------------------------------

// mockScanner implements the scanner interface used by scanOutboxEvent.
type mockScanner struct {
	err error
}

func (m *mockScanner) Scan(dest ...any) error {
	return m.err
}

func TestScanOutboxEvent_ScanError(t *testing.T) {
	t.Parallel()

	scanner := &mockScanner{err: ErrConnectionRequired}
	_, err := scanOutboxEvent(scanner)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scanning outbox event")
}

// ---------------------------------------------------------------------------
// storeCachedTenants and cachedTenants round-trip
// ---------------------------------------------------------------------------

func TestStoreCachedTenants_RoundTrip(t *testing.T) {
	t.Parallel()

	resolver := &ColumnResolver{tenantTTL: time.Minute}

	// Before storing — nothing cached
	_, ok := resolver.cachedTenants(time.Now())
	assert.False(t, ok)

	// Store tenants
	tenants := []string{"tenant-a", "tenant-b"}
	resolver.storeCachedTenants(tenants, time.Now())

	// Now should be cached
	cached, ok := resolver.cachedTenants(time.Now())
	assert.True(t, ok)
	assert.Equal(t, tenants, cached)
}

func TestStoreCachedTenants_ZeroTTLNoOp(t *testing.T) {
	t.Parallel()

	resolver := &ColumnResolver{tenantTTL: 0}
	resolver.storeCachedTenants([]string{"tenant-a"}, time.Now())

	_, ok := resolver.cachedTenants(time.Now())
	assert.False(t, ok)
}

func TestStoreCachedTenants_ExpiredCache(t *testing.T) {
	t.Parallel()

	resolver := &ColumnResolver{tenantTTL: time.Millisecond}
	resolver.storeCachedTenants([]string{"tenant-a"}, time.Now().Add(-time.Second))

	_, ok := resolver.cachedTenants(time.Now())
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// collectEventIDs
// ---------------------------------------------------------------------------

func TestCollectEventIDs_Empty(t *testing.T) {
	t.Parallel()

	ids := collectEventIDs(nil)
	assert.Empty(t, ids)
}

func TestCollectEventIDs_NilElement(t *testing.T) {
	t.Parallel()

	events := []*outbox.OutboxEvent{nil, nil}
	ids := collectEventIDs(events)
	assert.Empty(t, ids)
}

func TestCollectEventIDs_NilUUID(t *testing.T) {
	t.Parallel()

	events := []*outbox.OutboxEvent{
		{ID: uuid.Nil},
	}
	ids := collectEventIDs(events)
	assert.Empty(t, ids)
}

func TestCollectEventIDs_Mixed(t *testing.T) {
	t.Parallel()

	id1 := uuid.New()
	id2 := uuid.New()
	events := []*outbox.OutboxEvent{
		nil,
		{ID: uuid.Nil},
		{ID: id1},
		{ID: id2},
	}
	ids := collectEventIDs(events)
	assert.Len(t, ids, 2)
	assert.Equal(t, id1, ids[0])
	assert.Equal(t, id2, ids[1])
}

func TestCollectEventIDs_AllValid(t *testing.T) {
	t.Parallel()

	id1 := uuid.New()
	id2 := uuid.New()
	events := []*outbox.OutboxEvent{
		{ID: id1},
		{ID: id2},
	}
	ids := collectEventIDs(events)
	assert.Len(t, ids, 2)
}

// ---------------------------------------------------------------------------
// applyProcessingState
// ---------------------------------------------------------------------------

func TestApplyProcessingState_Empty(t *testing.T) {
	t.Parallel()

	// Must not panic on empty slice
	applyProcessingState(nil, time.Now())
}

func TestApplyProcessingState_NilElement(t *testing.T) {
	t.Parallel()

	events := []*outbox.OutboxEvent{nil}
	applyProcessingState(events, time.Now())
	// No panic, nil elements skipped
}

func TestApplyProcessingState_SetsStatusAndUpdatedAt(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	event := &outbox.OutboxEvent{
		ID:     uuid.New(),
		Status: outbox.OutboxStatusPending,
	}
	events := []*outbox.OutboxEvent{event}
	applyProcessingState(events, now)

	assert.Equal(t, outbox.OutboxStatusProcessing, event.Status)
	assert.Equal(t, now, event.UpdatedAt)
}

func TestApplyProcessingState_MultipleEvents(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	ev1 := &outbox.OutboxEvent{ID: uuid.New(), Status: outbox.OutboxStatusPending}
	ev2 := &outbox.OutboxEvent{ID: uuid.New(), Status: outbox.OutboxStatusPending}
	events := []*outbox.OutboxEvent{ev1, nil, ev2}

	applyProcessingState(events, now)

	assert.Equal(t, outbox.OutboxStatusProcessing, ev1.Status)
	assert.Equal(t, outbox.OutboxStatusProcessing, ev2.Status)
	assert.Equal(t, now, ev1.UpdatedAt)
	assert.Equal(t, now, ev2.UpdatedAt)
}

// ---------------------------------------------------------------------------
// tenantFilterClause
// ---------------------------------------------------------------------------

func TestTenantFilterClause_NoTenantColumn(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantColumn: ""}
	filter, args, err := repo.tenantFilterClause(1, "tenant-x")
	require.NoError(t, err)
	assert.Empty(t, filter)
	assert.Nil(t, args)
}

func TestTenantFilterClause_EmptyTenantID(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantColumn: "tenant_id"}
	_, _, err := repo.tenantFilterClause(1, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, outbox.ErrTenantIDRequired)
}

func TestTenantFilterClause_WithTenantIDAndColumn(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantColumn: "tenant_id"}
	filter, args, err := repo.tenantFilterClause(3, "my-tenant")
	require.NoError(t, err)
	assert.Contains(t, filter, "$3")
	assert.Contains(t, filter, `"tenant_id"`)
	assert.Len(t, args, 1)
	assert.Equal(t, "my-tenant", args[0])
}

// ---------------------------------------------------------------------------
// queryOutboxEvents — nil tx guard
// ---------------------------------------------------------------------------

func TestQueryOutboxEvents_NilTx(t *testing.T) {
	t.Parallel()

	events, err := queryOutboxEvents(context.Background(), nil, "SELECT 1", nil, 10, "test")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectionRequired)
	assert.Nil(t, events)
}

// ---------------------------------------------------------------------------
// tracerFromContext — no panic on background context
// ---------------------------------------------------------------------------

func TestTracerFromContext_BackgroundContext(t *testing.T) {
	t.Parallel()

	tracer := tracerFromContext(context.Background())
	assert.NotNil(t, tracer)
}

// ---------------------------------------------------------------------------
// WithRequireTenant option
// ---------------------------------------------------------------------------

func TestWithRequireTenant_SetsFlag(t *testing.T) {
	t.Parallel()

	resolver := &SchemaResolver{requireTenant: false}
	WithRequireTenant()(resolver)
	assert.True(t, resolver.requireTenant)
}

// ---------------------------------------------------------------------------
// WithOutboxTableName option
// ---------------------------------------------------------------------------

func TestWithOutboxTableName_SetsName(t *testing.T) {
	t.Parallel()

	resolver := &SchemaResolver{}
	WithOutboxTableName("my_outbox")(resolver)
	assert.Equal(t, "my_outbox", resolver.outboxTableName)
}

// ---------------------------------------------------------------------------
// SchemaResolver.primaryDB — no real DB, returns error
// ---------------------------------------------------------------------------

func TestSchemaResolver_PrimaryDB_NoConnection(t *testing.T) {
	t.Parallel()

	// resolver with a nil-backed client — primaryDB will fail
	resolver, err := NewSchemaResolver(&libPostgres.Client{})
	require.NoError(t, err)

	// primaryDB calls resolvePrimaryDB which will fail with no live connection
	_, err = resolver.primaryDB(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Repository.tenantIDFromContext — various cases
// ---------------------------------------------------------------------------

func TestRepository_TenantIDFromContext_NoTenantRequired(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantColumn: "", requireTenant: false}
	tid, err := repo.tenantIDFromContext(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tid)
}

func TestRepository_TenantIDFromContext_RequiredButMissing(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	_, err := repo.tenantIDFromContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, outbox.ErrTenantIDRequired)
}

func TestRepository_TenantIDFromContext_WithTenantInContext(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	ctx := outbox.ContextWithTenantID(context.Background(), "tenant-abc")
	tid, err := repo.tenantIDFromContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tenant-abc", tid)
}

func TestRepository_TenantIDFromContext_WhitespaceOnlyTenantID(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	ctx := outbox.ContextWithTenantID(context.Background(), "   ")
	_, err := repo.tenantIDFromContext(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, outbox.ErrTenantIDRequired)
}

// ---------------------------------------------------------------------------
// Public method early-exit guards (don't need DB — fail at initialized() or arg validation)
// ---------------------------------------------------------------------------

func TestListPending_NegativeLimit(t *testing.T) {
	t.Parallel()

	repo := &Repository{}
	_, err := repo.ListPending(context.Background(), -1)
	require.Error(t, err)
}

func TestCreateWithTx_NotInitialized(t *testing.T) {
	t.Parallel()

	// repo.create checks initialized() first
	repo := &Repository{}
	_, err := repo.CreateWithTx(context.Background(), nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}
