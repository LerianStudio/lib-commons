package mongo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
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

// distinctTenants lists the tenant ids of the documents matching filter,
// excluding the default scope, normalized like every tenant listing.
func distinctTenants(ctx context.Context, collection *mongodriver.Collection, tenantField string, filter bson.M) ([]string, error) {
	result := collection.Distinct(ctx, tenantField, mergeFilters(filter, bson.M{tenantField: bson.M{"$ne": defaultScopeTenantID}}))
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}

	var values []any
	if err := result.Decode(&values); err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}

	tenants := make([]string, 0, len(values))
	for _, value := range values {
		if tenantID, ok := value.(string); ok {
			tenants = append(tenants, tenantID)
		}
	}

	return normalizeTenantIDs(tenants)
}
