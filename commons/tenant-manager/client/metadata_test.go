//go:build unit

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
