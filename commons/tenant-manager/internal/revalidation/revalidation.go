package revalidation

import (
	"context"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

func ShouldSchedule(lastCheck map[string]time.Time, tenantID string, now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}

	if time.Since(lastCheck[tenantID]) <= interval {
		return false
	}

	lastCheck[tenantID] = now

	return true
}

func RecoverPanic(logger *logcompat.Logger, tenantID string) {
	if recovered := recover(); recovered != nil {
		logger.Warnf("recovered from panic during settings revalidation for tenant %s: %v", tenantID, recovered)
	}
}

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

		_ = closeFn(ctx, tenantID)

		return true
	}

	logger.Warnf("failed to revalidate connection settings for tenant %s: %v", tenantID, err)

	return false
}
