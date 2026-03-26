package revalidation

import (
	"context"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// ShouldSchedule returns true when interval has elapsed since the last check
// for tenantID and atomically updates lastCheck.  The caller MUST hold a write
// lock that protects lastCheck.  When hasClient is false the function returns
// false unconditionally so that revalidation is never scheduled for managers
// that have no tenant-manager API client.
func ShouldSchedule(lastCheck map[string]time.Time, tenantID string, now time.Time, interval time.Duration, hasClient bool) bool {
	if !hasClient || interval <= 0 {
		return false
	}

	if now.Sub(lastCheck[tenantID]) <= interval {
		return false
	}

	lastCheck[tenantID] = now

	return true
}

// RecoverPanic is intended to be deferred inside goroutines that perform
// background revalidation.  It logs the recovered value at warn level.
func RecoverPanic(logger *logcompat.Logger, tenantID string) {
	if recovered := recover(); recovered != nil {
		logger.Warnf("recovered from panic during settings revalidation for tenant %s: %v", tenantID, recovered)
	}
}

// HandleFetchError inspects err to decide whether the tenant's cached
// connection should be evicted.  It returns true when the tenant is suspended
// and the connection was (or was attempted to be) closed.
func HandleFetchError(
	logger *logcompat.Logger,
	tenantID string,
	err error,
	closeFn func(context.Context, string) error,
	timeout time.Duration,
) bool {
	if core.IsTenantSuspendedError(err) {
		logger.Warnf("tenant %s service suspended, evicting cached connection", tenantID)

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if closeErr := closeFn(ctx, tenantID); closeErr != nil {
			logger.Warnf("failed to evict cached connection for suspended tenant %s: %v", tenantID, closeErr)
		}

		return true
	}

	logger.Warnf("failed to revalidate connection settings for tenant %s: %v", tenantID, err)

	return false
}
