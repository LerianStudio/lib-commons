//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoaderLazyLoadMarksTenantKnown proves the SetOnTenantLoaded callback is
// wired: a tenant loaded through the consumer's shared TenantLoader (the loader
// a service also hands to the HTTP middleware) must count as owned, otherwise
// the dispatcher's ownership gate drops every tenant-level event for it.
func TestLoaderLazyLoadMarksTenantKnown(t *testing.T) {
	const tenantID = "tenant-loader-callback"

	server := setupLazyLoadServer(t, tenantID, newTestTenantConfig(tenantID), nil)

	consumer, err := NewMultiTenantConsumerWithError(MultiTenantConfig{
		MultiTenantURL:    server.URL,
		Service:           testServiceName,
		ServiceAPIKey:     "test-key",
		AllowInsecureHTTP: true,
	}, testutil.NewMockLogger())
	require.NoError(t, err)

	t.Cleanup(func() { _ = consumer.Close() })

	loader := consumer.Loader()
	require.NotNil(t, loader, "the consumer must expose its shared loader so the middleware can reuse it")

	_, err = loader.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	assert.Contains(t, consumer.Stats().KnownTenantIDs, tenantID,
		"a tenant lazy-loaded through the shared loader must be known to the consumer")

	dispatcher := consumer.dispatcher
	require.NotNil(t, dispatcher)

	require.NoError(t, dispatcher.HandleEvent(context.Background(), event.TenantLifecycleEvent{
		EventID:   "evt-1",
		EventType: event.EventTenantSuspended,
		TenantID:  tenantID,
	}))

	_, found := consumer.cache.Get(tenantID)
	assert.False(t, found,
		"tenant.suspended must evict a tenant that was lazy-loaded through the shared loader")
}
