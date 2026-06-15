//go:build unit

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

func TestClient_GetTenantMetadata_Success(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "11111111-1111-1111-1111-111111111111",
			"tenantSlug": "acme",
			"metadata": {
				"midaz_org_id": "22222222-2222-2222-2222-222222222222",
				"payments_ledger_id": "33333333-3333-3333-3333-333333333333"
			}
		}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	md, err := client.GetTenantMetadata(context.Background(), "11111111-1111-1111-1111-111111111111")

	require.NoError(t, err)
	assert.Equal(t, "/v1/tenants/11111111-1111-1111-1111-111111111111", gotPath)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", md["midaz_org_id"])
	assert.Equal(t, "33333333-3333-3333-3333-333333333333", md["payments_ledger_id"])
}

func TestClient_GetTenantMetadata_NoMetadataField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"11111111-1111-1111-1111-111111111111","tenantSlug":"acme"}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	md, err := client.GetTenantMetadata(context.Background(), "11111111-1111-1111-1111-111111111111")

	require.NoError(t, err)
	assert.Empty(t, md)
}

func TestClient_GetTenantMetadata_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"tenant not found"}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	md, err := client.GetTenantMetadata(context.Background(), "missing")

	require.Error(t, err)
	assert.Nil(t, md)
}

func TestClient_GetTenantMetadata_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	md, err := client.GetTenantMetadata(context.Background(), "11111111-1111-1111-1111-111111111111")

	require.Error(t, err)
	assert.Nil(t, md)
}

func TestClient_GetTenantMetadata_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	md, err := client.GetTenantMetadata(context.Background(), "11111111-1111-1111-1111-111111111111")

	require.Error(t, err)
	assert.Nil(t, md)
}

func TestClient_GetTenantMetadata_APIKeyHeader(t *testing.T) {
	var gotAPIKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"metadata":{"midaz_org_id":"org"}}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithServiceAPIKey("secret-key"))

	_, err := client.GetTenantMetadata(context.Background(), "11111111-1111-1111-1111-111111111111")

	require.NoError(t, err)
	assert.Equal(t, "secret-key", gotAPIKey)
}

func TestClient_CircuitBreaker_GetTenantMetadata(t *testing.T) {
	t.Run("server errors (5xx) open the breaker", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		threshold := 2
		client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
		ctx := context.Background()

		for i := 0; i < threshold; i++ {
			_, err := client.GetTenantMetadata(ctx, "tenant-x")
			require.Error(t, err)
		}

		assert.Equal(t, cbOpen, client.cbState)

		// Subsequent call fails fast without hitting the server.
		_, err := client.GetTenantMetadata(ctx, "tenant-x")
		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
	})

	t.Run("client errors (404) do not trip the breaker", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		threshold := 2
		client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
		ctx := context.Background()

		// Exceed the threshold with 404s — the breaker must stay closed.
		for i := 0; i < threshold+2; i++ {
			_, err := client.GetTenantMetadata(ctx, "tenant-x")
			require.Error(t, err)
			assert.NotErrorIs(t, err, core.ErrCircuitBreakerOpen, "a 404 must not open the breaker")
		}

		assert.NotEqual(t, cbOpen, client.cbState, "4xx must not open the breaker")
		assert.Zero(t, client.cbFailures, "4xx must not record breaker failures")
	})
}
