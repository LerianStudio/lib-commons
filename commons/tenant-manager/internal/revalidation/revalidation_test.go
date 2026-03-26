//go:build unit

package revalidation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nopLogger() *logcompat.Logger {
	return logcompat.New(nil)
}

// --- ShouldSchedule ---

func TestShouldSchedule_ReturnsFalseWhenNoClient(t *testing.T) {
	t.Parallel()

	lastCheck := make(map[string]time.Time)
	now := time.Now()

	result := ShouldSchedule(lastCheck, "tenant-1", now, 5*time.Minute, false)

	assert.False(t, result)
}

func TestShouldSchedule_ReturnsFalseWhenIntervalZero(t *testing.T) {
	t.Parallel()

	lastCheck := make(map[string]time.Time)
	now := time.Now()

	result := ShouldSchedule(lastCheck, "tenant-1", now, 0, true)

	assert.False(t, result)
}

func TestShouldSchedule_ReturnsFalseWhenIntervalNegative(t *testing.T) {
	t.Parallel()

	lastCheck := make(map[string]time.Time)
	now := time.Now()

	result := ShouldSchedule(lastCheck, "tenant-1", now, -1*time.Second, true)

	assert.False(t, result)
}

func TestShouldSchedule_ReturnsTrueWhenElapsed(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lastCheck := map[string]time.Time{
		"tenant-1": now.Add(-10 * time.Minute),
	}

	result := ShouldSchedule(lastCheck, "tenant-1", now, 5*time.Minute, true)

	assert.True(t, result)
}

func TestShouldSchedule_ReturnsFalseWithinInterval(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lastCheck := map[string]time.Time{
		"tenant-1": now.Add(-1 * time.Minute),
	}

	result := ShouldSchedule(lastCheck, "tenant-1", now, 5*time.Minute, true)

	assert.False(t, result)
}

func TestShouldSchedule_ReturnsTrueForNewTenant(t *testing.T) {
	t.Parallel()

	lastCheck := make(map[string]time.Time)
	now := time.Now()

	// A key absent from the map returns the zero time.Time, which is far
	// enough in the past that any positive interval will be exceeded.
	result := ShouldSchedule(lastCheck, "brand-new-tenant", now, 5*time.Minute, true)

	assert.True(t, result)
}

func TestShouldSchedule_UpdatesLastCheck(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lastCheck := map[string]time.Time{
		"tenant-1": now.Add(-10 * time.Minute),
	}

	result := ShouldSchedule(lastCheck, "tenant-1", now, 5*time.Minute, true)

	require.True(t, result)
	assert.Equal(t, now, lastCheck["tenant-1"], "lastCheck must be updated to 'now' on a successful schedule")
}

// --- HandleFetchError ---

func TestHandleFetchError_ReturnsTrueForSuspendedTenant(t *testing.T) {
	t.Parallel()

	logger := nopLogger()
	suspendedErr := &core.TenantSuspendedError{
		TenantID: "t1",
		Status:   "suspended",
	}

	var closeCalled bool
	var closedTenantID string

	closeFn := func(_ context.Context, tenantID string) error {
		closeCalled = true
		closedTenantID = tenantID

		return nil
	}

	result := HandleFetchError(logger, "t1", suspendedErr, closeFn, 5*time.Second)

	assert.True(t, result)
	assert.True(t, closeCalled, "closeFn must be invoked for suspended tenants")
	assert.Equal(t, "t1", closedTenantID)
}

func TestHandleFetchError_ReturnsFalseForGenericError(t *testing.T) {
	t.Parallel()

	logger := nopLogger()
	genericErr := errors.New("connection timeout")

	var closeCalled bool

	closeFn := func(_ context.Context, _ string) error {
		closeCalled = true

		return nil
	}

	result := HandleFetchError(logger, "t1", genericErr, closeFn, 5*time.Second)

	assert.False(t, result)
	assert.False(t, closeCalled, "closeFn must NOT be invoked for generic errors")
}

func TestHandleFetchError_LogsCloseError(t *testing.T) {
	t.Parallel()

	logger := nopLogger()
	suspendedErr := &core.TenantSuspendedError{
		TenantID: "t1",
		Status:   "suspended",
	}

	closeFn := func(_ context.Context, _ string) error {
		return errors.New("eviction failed")
	}

	// Must not panic even when closeFn returns an error.
	result := HandleFetchError(logger, "t1", suspendedErr, closeFn, 5*time.Second)

	assert.True(t, result, "still returns true because the tenant IS suspended")
}

// --- RecoverPanic ---

func TestRecoverPanic_RecoversPanic(t *testing.T) {
	t.Parallel()

	logger := nopLogger()

	// RecoverPanic must be called inside a deferred function that runs after
	// a panic, so we wrap the whole thing in a function that panics.
	assert.NotPanics(t, func() {
		defer RecoverPanic(logger, "t1")
		panic("test panic value")
	})
}
