package mongo

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

func normalizeTenantIDs(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	tenants := make([]string, 0, len(raw))

	for _, candidate := range raw {
		tenantID := strings.TrimSpace(candidate)
		if tenantID == "" {
			continue
		}

		if !tmcore.IsValidTenantID(tenantID) {
			return nil, fmt.Errorf("%w: %q", outbox.ErrInvalidTenantID, tenantID)
		}

		if _, exists := seen[tenantID]; exists {
			continue
		}

		seen[tenantID] = struct{}{}
		tenants = append(tenants, tenantID)
	}

	sort.Strings(tenants)

	return tenants, nil
}
