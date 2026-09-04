//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package mongo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestGetMongoConfigForTenant_BypassesClientCache proves that the config fetch
// on the connection-rebuild path does not replay the stale entry held by the
// client (tier-2) cache. Without WithSkipCache a connection rebuilt after an
// eviction reconnects to the host the tenant was just migrated off.
func TestGetMongoConfigForTenant_BypassesClientCache(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		host := "fresh-host.invalid"
		if requestCount.Add(1) == 1 {
			host = "stale-host.invalid"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "tenant-rebuild",
			"tenantSlug": "rebuild",
			"service": "ledger",
			"status": "active",
			"databases": {
				"onboarding": {
					"mongodb": {"host": "` + host + `", "port": 27017, "database": "testdb", "username": "user", "password": "pass"}
				}
			}
		}`))
	}))
	defer server.Close()

	capLogger := testutil.NewCapturingLogger()
	tmClient, err := client.NewClient(server.URL, capLogger,
		client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = tmClient.Close() })

	cached, err := tmClient.GetTenantConfig(context.Background(), "tenant-rebuild", "ledger")
	require.NoError(t, err)
	require.Equal(t, "stale-host.invalid", cached.Databases["onboarding"].MongoDB.Host)
	require.Equal(t, int32(1), requestCount.Load())

	manager := NewManager(tmClient, "ledger", WithLogger(capLogger), WithModule("onboarding"))

	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "test")
	defer span.End()

	mongoConfig, err := manager.getMongoConfigForTenant(
		context.Background(), "tenant-rebuild", logcompat.New(capLogger), span)
	require.NoError(t, err)
	require.NotNil(t, mongoConfig)

	assert.Equal(t, int32(2), requestCount.Load(),
		"connection rebuild must bypass the client config cache and refetch")
	assert.Equal(t, "fresh-host.invalid", mongoConfig.Host,
		"rebuild must use the fresh config, not the cached stale one")
}
