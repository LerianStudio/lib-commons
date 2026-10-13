//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tenantcache

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadTenant_OneTenantTwoSpellingsIsOneEntry covers a tenant loaded under
// the dashed UUID and then under the Tenant Manager's dashless ID: one Tenant
// Manager call, one cache entry, one "loaded" notification, all canonical.
func TestLoadTenant_OneTenantTwoSpellingsIsOneEntry(t *testing.T) {
	t.Parallel()

	const (
		dashless = "11111111111111111111111111111111"
		dashed   = "11111111-1111-1111-1111-111111111111"
	)

	var requests atomic.Int64
	server := setupLoaderServer(t, dashless, newTestTenantConfig(dashless), &requests)

	cache := NewTenantCache()
	loader := NewTenantLoader(newTestClient(t, server.URL), cache, testServiceName, DefaultTenantCacheTTL, obs.Nop())

	var loaded []string
	loader.SetOnTenantLoaded(func(_ context.Context, tenantID string) { loaded = append(loaded, tenantID) })

	for _, id := range []string{dashed, dashless} {
		cfg, err := loader.LoadTenant(context.Background(), id)
		require.NoError(t, err, "tenant %s", id)
		assert.Equal(t, dashless, cfg.ID)
	}

	assert.Equal(t, int64(1), requests.Load(), "the second spelling is served from the cache")
	assert.Equal(t, []string{dashless}, cache.TenantIDs())
	assert.Equal(t, []string{dashless}, loaded)

	_, err := loader.LoadTenant(context.Background(), "bad id")
	require.ErrorIs(t, err, core.ErrInvalidTenantIDFormat)
}
