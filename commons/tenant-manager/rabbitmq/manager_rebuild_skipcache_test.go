//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateConnection_BypassesClientCache proves that a post-eviction rebuild
// re-reads the tenant config from the Tenant Manager instead of replaying the
// stale entry still held by the client (tier-2) cache. Without WithSkipCache the
// rebuild dials the vhost the tenant no longer uses, which is exactly the
// "operator invalidated the cache and the service never recovered" symptom.
func TestCreateConnection_BypassesClientCache(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		vhost := "fresh-vhost"
		if requestCount.Add(1) == 1 {
			vhost = "stale-vhost"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "tenant-rebuild",
			"tenantSlug": "rebuild",
			"messaging": {
				"rabbitmq": {"host": "rebuild-host.invalid", "port": 5672, "vhost": "` + vhost + `", "username": "guest", "password": "guest"}
			}
		}`))
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient, err := client.NewClient(server.URL, capLogger,
		client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = tmClient.Close() })

	cfg, err := tmClient.GetTenantConfig(context.Background(), "tenant-rebuild", "ledger")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, int32(1), requestCount.Load())

	manager := NewManager(tmClient, "ledger", WithLogger(capLogger))

	_, connErr := manager.GetConnection(context.Background(), "tenant-rebuild")
	require.Error(t, connErr, "dial to unresolvable host must fail")

	assert.Equal(t, int32(2), requestCount.Load(),
		"connection rebuild must bypass the client config cache and refetch")
	assert.True(t, capLogger.ContainsSubstring("vhost=fresh-vhost"),
		"rebuild must use the fresh config; got: %v", capLogger.GetMessages())
	assert.False(t, capLogger.ContainsSubstring("vhost=stale-vhost"),
		"rebuild must not replay the cached stale config; got: %v", capLogger.GetMessages())
}
