//go:build unit

package mongo

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

// ---------------------------------------------------------------------------
// tenantMatchFilter
// ---------------------------------------------------------------------------

func TestTenantMatchFilter_EmptyTenantField(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantField: ""}
	filter := repo.tenantMatchFilter("tenant-x")
	assert.Equal(t, bson.M{}, filter)
}

func TestTenantMatchFilter_WithTenantField(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantField: "tenant_id"}
	filter := repo.tenantMatchFilter("tenant-123")
	assert.Equal(t, bson.M{"tenant_id": "tenant-123"}, filter)
}

func TestTenantMatchFilter_EmptyTenantID(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantField: "tenant_id"}
	filter := repo.tenantMatchFilter("")
	assert.Equal(t, bson.M{"tenant_id": ""}, filter)
}

// ---------------------------------------------------------------------------
// idTenantFilter
// ---------------------------------------------------------------------------

func TestIDTenantFilter_NoTenantField(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantField: ""}
	id := uuid.New()
	filter := repo.idTenantFilter(id, "tenant-x")
	assert.Equal(t, bson.M{"id": id.String()}, filter)
}

func TestIDTenantFilter_WithTenantField(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantField: "tenant_id"}
	id := uuid.New()
	filter := repo.idTenantFilter(id, "tenant-abc")

	assert.Equal(t, id.String(), filter["id"])
	assert.Equal(t, "tenant-abc", filter["tenant_id"])
}

func TestIDTenantFilter_NilUUID(t *testing.T) {
	t.Parallel()

	repo := &Repository{tenantField: "tenant_id"}
	filter := repo.idTenantFilter(uuid.Nil, "tenant-x")

	// uuid.Nil.String() is the zero UUID string
	assert.Equal(t, uuid.Nil.String(), filter["id"])
	assert.Equal(t, "tenant-x", filter["tenant_id"])
}

// ---------------------------------------------------------------------------
// tracking — background context returns non-nil tracer
// ---------------------------------------------------------------------------

func TestTracking_BackgroundContext(t *testing.T) {
	t.Parallel()

	repo := &Repository{}
	tracer := repo.tracking(context.Background())
	assert.NotNil(t, tracer)
}

func TestTracking_NilRepoTracer(t *testing.T) {
	t.Parallel()

	repo := &Repository{tracer: nil}
	tracer := repo.tracking(context.Background())
	// Should fall back to noop tracer
	assert.NotNil(t, tracer)
}

// ---------------------------------------------------------------------------
// ensureIndexes — returns nil when no indexes built
// ---------------------------------------------------------------------------

func TestEnsureIndexes_NoIndexesBuilt(t *testing.T) {
	t.Parallel()

	// buildIndexes always returns non-empty for both "" and "tenant_id".
	// ensureIndexes short-circuits only when buildIndexes returns empty.
	// For coverage: call ensureIndexes on an uninitialized repo — it will
	// fail at EnsureIndexes since client is nil, but we verify the guard path.
	repo := &Repository{
		tenantField:    "",
		collectionName: defaultCollectionName,
	}
	// With a nil client, ensureIndexes will hit the initialized() guard
	// and return ErrRepositoryNotInitialized only when client is set.
	// Without client (nil), repo.initialized() is false and collection()
	// returns ErrRepositoryNotInitialized — but ensureIndexes goes to
	// client.EnsureIndexes directly. Let's just verify it returns an error
	// instead of panicking on nil client.
	err := repo.ensureIndexes(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Repository.tenantIDFromContext — various cases
// ---------------------------------------------------------------------------

func TestMongoRepository_TenantIDFromContext_NotRequired(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: false}
	tid, err := repo.tenantIDFromContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, defaultScopeTenantID, tid)
}

func TestMongoRepository_TenantIDFromContext_Required(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	_, err := repo.tenantIDFromContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, outbox.ErrTenantIDRequired)
}

func TestMongoRepository_TenantIDFromContext_WithTenant(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true, tenantField: defaultTenantField}
	ctx := outbox.ContextWithTenantID(context.Background(), "tenant-xyz")
	tid, err := repo.tenantIDFromContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tenant-xyz", tid)
}

func TestMongoRepository_TenantIDFromContext_InvalidTenantID(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: false}
	ctx := outbox.ContextWithTenantID(context.Background(), "_invalid")
	_, err := repo.tenantIDFromContext(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, outbox.ErrInvalidTenantID)
}

// ---------------------------------------------------------------------------
// RequiresTenant
// ---------------------------------------------------------------------------

func TestMongoRepository_RequiresTenant_NilReceiver(t *testing.T) {
	t.Parallel()

	var repo *Repository
	assert.True(t, repo.RequiresTenant())
}

func TestMongoRepository_RequiresTenant_False(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: false, tenantField: ""}
	assert.False(t, repo.RequiresTenant())
}

func TestMongoRepository_RequiresTenant_TrueViaFlag(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	assert.True(t, repo.RequiresTenant())
}

// ---------------------------------------------------------------------------
// initialized
// ---------------------------------------------------------------------------

func TestMongoRepository_Initialized_Nil(t *testing.T) {
	t.Parallel()

	var repo *Repository
	assert.False(t, repo.initialized())
}

func TestMongoRepository_Initialized_NilClient(t *testing.T) {
	t.Parallel()

	repo := &Repository{client: nil}
	assert.False(t, repo.initialized())
}
