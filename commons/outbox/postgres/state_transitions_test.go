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

// uninitializedRepo returns a Repository that is NOT initialized (no primaryDBLookup).
// Used to test the ErrRepositoryNotInitialized guard on every method.
func uninitializedRepo() *Repository {
	return &Repository{
		tableName:          "outbox_events",
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		transactionTimeout: defaultTransactionTimeout,
	}
}

// TestMarkPublished_NotInitialized covers the initialized guard.
func TestMarkPublished_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	err := repo.MarkPublished(context.Background(), uuid.New(), time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMarkFailed_NotInitialized covers the initialized guard.
func TestMarkFailed_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	err := repo.MarkFailed(context.Background(), uuid.Nil, "error", 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMarkFailed_NilID_Uninitialized covers the initialized guard taking priority.
func TestMarkFailed_NilID_Uninitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	err := repo.MarkFailed(context.Background(), uuid.New(), "error", 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMarkFailed_MaxAttemptsZero is tested via the uninitialized path already.
// The maxAttempts check runs after the initialized check, so we can only test
// it with a real connection. Keeping as a placeholder for documentation.
func TestMarkFailed_InitializedGuard(t *testing.T) {
	t.Parallel()

	// Verify ErrRepositoryNotInitialized takes priority over maxAttempts check
	repo := uninitializedRepo()
	err := repo.MarkFailed(context.Background(), uuid.New(), "error", 0) // maxAttempts=0 invalid
	require.Error(t, err)
	// Initialized check runs first - returns ErrRepositoryNotInitialized, not ErrMaxAttemptsMustBePositive
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestListFailedForRetry_NotInitialized covers the guard.
func TestListFailedForRetry_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.ListFailedForRetry(context.Background(), 10, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestResetForRetry_NotInitialized covers the guard.
func TestResetForRetry_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.ResetForRetry(context.Background(), 10, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestResetStuckProcessing_NotInitialized covers the guard.
func TestResetStuckProcessing_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.ResetStuckProcessing(context.Background(), 10, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestMarkInvalid_NotInitialized covers the guard.
func TestMarkInvalid_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	err := repo.MarkInvalid(context.Background(), uuid.New(), "error")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestGetByID_NotInitialized covers the guard.
func TestGetByID_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.GetByID(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestCreate_NotInitialized covers the guard.
func TestCreate_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
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

// TestListPending_NotInitialized covers the guard.
func TestListPending_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.ListPending(context.Background(), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestListPendingByType_NotInitialized covers the guard.
func TestListPendingByType_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.ListPendingByType(context.Background(), "event.type", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// TestListTenants_NotInitialized covers the guard.
func TestListTenants_NotInitialized(t *testing.T) {
	t.Parallel()

	repo := uninitializedRepo()
	_, err := repo.ListTenants(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRepositoryNotInitialized)
}

// initializedRepo returns a Repository with initialized() returning true
// but no real DB connection - for testing validation before DB access.
func initializedRepo() *Repository {
	return &Repository{
		client:             &libPostgres.Client{},
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		tableName:          "outbox_events",
		transactionTimeout: defaultTransactionTimeout,
	}
}

// TestListPending_LimitZero covers the limit validation.
func TestListPending_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListPending(context.Background(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestListPending_LimitNegative covers the negative limit.
func TestListPending_LimitNegative(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListPending(context.Background(), -1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestListPendingByType_LimitZero covers the limit validation.
func TestListPendingByType_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListPendingByType(context.Background(), "test.event", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestListFailedForRetry_LimitZero covers the limit validation.
func TestListFailedForRetry_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListFailedForRetry(context.Background(), 0, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestListFailedForRetry_MaxAttemptsZero covers the maxAttempts validation.
func TestListFailedForRetry_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListFailedForRetry(context.Background(), 10, time.Now(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestResetForRetry_LimitZero covers the limit validation.
func TestResetForRetry_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ResetForRetry(context.Background(), 0, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestResetForRetry_MaxAttemptsZero covers the maxAttempts validation.
func TestResetForRetry_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ResetForRetry(context.Background(), 10, time.Now(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestResetStuckProcessing_LimitZero covers the limit validation.
func TestResetStuckProcessing_LimitZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ResetStuckProcessing(context.Background(), 0, time.Now(), 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLimitMustBePositive)
}

// TestResetStuckProcessing_MaxAttemptsZero covers the maxAttempts validation.
func TestResetStuckProcessing_MaxAttemptsZero(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ResetStuckProcessing(context.Background(), 10, time.Now(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestMarkFailed_MaxAttemptsValidation covers the maxAttempts validation.
func TestMarkFailed_MaxAttemptsValidation(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	err := repo.MarkFailed(context.Background(), uuid.New(), "error", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

// TestMarkFailed_NilUUID covers the nil UUID validation.
func TestMarkFailed_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	err := repo.MarkFailed(context.Background(), uuid.Nil, "error", 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestGetByID_NilUUID covers the nil UUID validation.
func TestGetByID_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.GetByID(context.Background(), uuid.Nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestMarkPublished_NilUUID covers the nil UUID validation.
func TestMarkPublished_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	err := repo.MarkPublished(context.Background(), uuid.Nil, time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestMarkInvalid_NilUUID covers the nil UUID validation.
func TestMarkInvalid_NilUUID(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	err := repo.MarkInvalid(context.Background(), uuid.Nil, "error")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIDRequired)
}

// TestListPendingByType_EmptyEventType covers the empty event type validation.
func TestListPendingByType_EmptyEventType(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListPendingByType(context.Background(), "", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEventTypeRequired)
}

func TestListPendingByType_WhitespaceEventType(t *testing.T) {
	t.Parallel()

	repo := initializedRepo()
	_, err := repo.ListPendingByType(context.Background(), "   ", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEventTypeRequired)
}
