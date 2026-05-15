package circuitbreaker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

const noTenantHashLabel = ""

func validateBreakerIdentity(tenantID, serviceName string, tenantAware bool) (breakerMapKey, error) {
	if err := validateServiceName(serviceName); err != nil {
		return breakerMapKey{}, err
	}

	if tenantAware {
		if err := validateTenantID(tenantID); err != nil {
			return breakerMapKey{}, err
		}
	}

	return breakerMapKey{tenantID: tenantID, serviceName: serviceName}, nil
}

func validateServiceName(serviceName string) error {
	if serviceName == "" {
		return fmt.Errorf("%w: service name must not be empty", ErrInvalidServiceName)
	}

	for _, r := range serviceName {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: service name must not contain control characters", ErrInvalidServiceName)
		}
	}

	return nil
}

func validateTenantID(tenantID string) error {
	if !tmcore.IsValidTenantID(tenantID) {
		return fmt.Errorf("%w: tenant id must be non-empty and match tenant-manager constraints", ErrInvalidTenantID)
	}

	return nil
}

func tenantHashLabel(tenantID string) string {
	if tenantID == "" {
		return noTenantHashLabel
	}

	h := sha256.Sum256([]byte(tenantID))

	return hex.EncodeToString(h[:8])
}

func tenantHashMetricLabel(tenantID string) string {
	return tenantHashLabel(tenantID)
}
