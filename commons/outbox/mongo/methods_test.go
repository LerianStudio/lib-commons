//go:build unit

package mongo

import (
	"context"
	"testing"
	"time"

	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uninitializedMongoRepo returns a Repository that is NOT initialized.
func uninitializedMongoRepo() *Repository {
	return &Repository{
		collectionName: defaultCollectionName,
		tenantField:    defaultTenantField,
		requireTenant:  true,
	}
}

// TestMongoGetByID_NotInitialized covers the initialized guard.
func TestMongoGetByID_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.GetByID(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoMarkPublished_NotInitialized covers the guard.
func TestMongoMarkPublished_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	err := repo.MarkPublished(context.Background(), uuid.New(), time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoMarkFailed_NotInitialized covers the guard.
func TestMongoMarkFailed_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	err := repo.MarkFailed(context.Background(), uuid.New(), "error", 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoListFailedForRetry_NotInitialized covers the guard.
func TestMongoListFailedForRetry_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.ListFailedForRetry(context.Background(), 10, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoResetForRetry_NotInitialized covers the guard.
func TestMongoResetForRetry_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.ResetForRetry(context.Background(), 10, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoResetStuckProcessing_NotInitialized covers the guard.
func TestMongoResetStuckProcessing_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.ResetStuckProcessing(context.Background(), 10, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoMarkInvalid_NotInitialized covers the guard.
func TestMongoMarkInvalid_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	err := repo.MarkInvalid(context.Background(), uuid.New(), "error")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoListPending_NotInitialized covers the guard.
func TestMongoListPending_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.ListPending(context.Background(), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoListPendingByType_NotInitialized covers the guard.
func TestMongoListPendingByType_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.ListPendingByType(context.Background(), "event.type", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoListTenants_NotInitialized covers the guard.
func TestMongoListTenants_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	_, err := repo.ListTenants(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoCreate_NotInitialized covers the guard.
func TestMongoCreate_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedMongoRepo()
	event := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "test.event",
		AggregateID: uuid.New(),
		Payload:     []byte(`{}`),
	}
	_, err := repo.Create(context.Background(), event)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMongoRequiresTenant covers the RequiresTenant method.
func TestMongoRequiresTenant_True(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	assert.True(t, repo.RequiresTenant())
}

func TestMongoRequiresTenant_False(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: false}
	assert.False(t, repo.RequiresTenant())
}

// initializedMongoRepo returns a Repository with client set (initialized() == true).
func initializedMongoRepo() *Repository {
	return &Repository{
		client:         &libMongo.Client{},
		collectionName: defaultCollectionName,
		tenantField:    defaultTenantField,
		requireTenant:  true,
	}
}

// TestMongoMarkFailed_MaxAttemptsZero covers the maxAttempts validation.
func TestMongoMarkFailed_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	err := repo.MarkFailed(context.Background(), uuid.New(), "error", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestMongoMarkFailed_NilUUID covers the nil UUID guard.
func TestMongoMarkFailed_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	err := repo.MarkFailed(context.Background(), uuid.Nil, "error", 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestMongoGetByID_NilUUID covers the nil UUID guard.
func TestMongoGetByID_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.GetByID(context.Background(), uuid.Nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestMongoMarkPublished_NilUUID covers the nil UUID guard.
func TestMongoMarkPublished_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	err := repo.MarkPublished(context.Background(), uuid.Nil, time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestMongoMarkInvalid_NilUUID covers the nil UUID guard.
func TestMongoMarkInvalid_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	err := repo.MarkInvalid(context.Background(), uuid.Nil, "error")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestMongoListPending_LimitZero covers the limit validation.
func TestMongoListPending_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ListPending(context.Background(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestMongoListPendingByType_LimitZero covers the limit validation.
func TestMongoListPendingByType_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ListPendingByType(context.Background(), "test.event", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestMongoListFailedForRetry_LimitZero covers the limit validation.
func TestMongoListFailedForRetry_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ListFailedForRetry(context.Background(), 0, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestMongoListFailedForRetry_MaxAttemptsZero covers the maxAttempts validation.
func TestMongoListFailedForRetry_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ListFailedForRetry(context.Background(), 10, time.Now(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestMongoResetForRetry_LimitZero covers the limit validation.
func TestMongoResetForRetry_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ResetForRetry(context.Background(), 0, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestMongoResetForRetry_MaxAttemptsZero covers the maxAttempts validation.
func TestMongoResetForRetry_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ResetForRetry(context.Background(), 10, time.Now(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestMongoResetStuckProcessing_LimitZero covers the limit validation.
func TestMongoResetStuckProcessing_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ResetStuckProcessing(context.Background(), 0, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestMongoResetStuckProcessing_MaxAttemptsZero covers the maxAttempts validation.
func TestMongoResetStuckProcessing_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ResetStuckProcessing(context.Background(), 10, time.Now(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestMongoListPendingByType_EmptyEventType covers the empty event type check.
func TestMongoListPendingByType_EmptyEventType(t *testing.T) {
	t.Parallel()

	repo := initializedMongoRepo()
	_, err := repo.ListPendingByType(context.Background(), "", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEventTypeRequired)
}

// TestMongoListTenants_EmptyTenantField covers the empty tenant field early return.
func TestMongoListTenants_EmptyTenantField(t *testing.T) {
	t.Parallel()

	// Repo with empty tenant field - ListTenants returns empty slice
	repo := &Repository{
		client:         &libMongo.Client{},
		collectionName: defaultCollectionName,
		tenantField:    "", // no tenant field configured
	}

	tenants, err := repo.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tenants)
}
