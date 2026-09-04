//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsOwnedLocally_CoversCachedTenantsMissedByOwnershipChecker captures the
// production symptom for tenants lazy-loaded through the HTTP middleware: they
// land in the shared config cache but never in the consumer's knownTenants map,
// so an ownership checker that only consults knownTenants reports "not owned"
// and every tenant-level event for that tenant is silently dropped.
func TestIsOwnedLocally_CoversCachedTenantsMissedByOwnershipChecker(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-http-loaded"

	cache := tenantcache.NewTenantCache()
	cache.Set(tenantID, &core.TenantConfig{ID: tenantID}, 1*time.Hour)

	var removed []string

	dispatcher := NewEventDispatcher(cache, nil, "ledger",
		WithTenantOwnershipChecker(func(string) bool { return false }),
		WithOnTenantRemoved(func(_ context.Context, id string) {
			removed = append(removed, id)
		}),
	)

	err := dispatcher.HandleEvent(context.Background(), TenantLifecycleEvent{
		EventID:   "evt-1",
		EventType: EventTenantSuspended,
		TenantID:  tenantID,
	})
	require.NoError(t, err)

	_, found := cache.Get(tenantID)
	assert.False(t, found, "tenant.suspended must evict a tenant present in the shared cache")
	assert.Equal(t, []string{tenantID}, removed,
		"the consumer must be notified so it can stop any goroutine for this tenant")
}

// TestIsOwnedLocally_StillReportsUnknownTenantsAsNotOwned pins the gate that
// keeps unrelated tenants out: neither the checker nor the cache knows this one.
func TestIsOwnedLocally_StillReportsUnknownTenantsAsNotOwned(t *testing.T) {
	t.Parallel()

	dispatcher := NewEventDispatcher(tenantcache.NewTenantCache(), nil, "ledger",
		WithTenantOwnershipChecker(func(string) bool { return false }),
	)

	assert.False(t, dispatcher.isOwnedLocally("tenant-never-seen"))
}

// TestIsOwnedLocally_ChecksOwnershipCheckerBeforeCache keeps the expired-entry
// guarantee that WithTenantOwnershipChecker was introduced for: a tenant the
// consumer owns stays owned even once its cache entry has gone.
func TestIsOwnedLocally_ChecksOwnershipCheckerBeforeCache(t *testing.T) {
	t.Parallel()

	dispatcher := NewEventDispatcher(tenantcache.NewTenantCache(), nil, "ledger",
		WithTenantOwnershipChecker(func(string) bool { return true }),
	)

	assert.True(t, dispatcher.isOwnedLocally("tenant-expired-entry"))
}
