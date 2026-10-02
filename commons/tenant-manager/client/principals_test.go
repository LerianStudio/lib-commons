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

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/cache"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
)

func TestClient_GetTenantPrincipals_DecodesLiveParties(t *testing.T) {
	var gotPath, gotAPIKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"nature":"originador","partyRef":"ORQUESS"},{"nature":"","partyRef":"LEGACY"}]}`))
	}))
	defer server.Close()

	principals, err := mustNewClient(t, server.URL).GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")

	require.NoError(t, err)
	assert.Equal(t, "/v1/tenants/tenant-123/associations/streaming-hub/principals", gotPath)
	assert.Equal(t, "test-api-key", gotAPIKey)
	assert.Equal(t, []TenantPrincipal{
		{Nature: "originador", PartyRef: "ORQUESS"},
		{Nature: "", PartyRef: "LEGACY"},
	}, principals)
}

func TestClient_GetTenantPrincipals_RevocationShowsOnNextCall(t *testing.T) {
	bodies := []string{
		`{"items":[{"nature":"fundo","partyRef":"VERT"},{"nature":"originador","partyRef":"ORQUESS"}]}`,
		`{"items":[{"nature":"fundo","partyRef":"VERT"}]}`,
	}
	calls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(bodies[calls]))
		calls++
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCache(cache.NewInMemoryCache()))
	t.Cleanup(func() { _ = client.Close() })

	_, err := client.GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")
	require.NoError(t, err)

	principals, err := client.GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")
	require.NoError(t, err)
	assert.Equal(t, []TenantPrincipal{{Nature: "fundo", PartyRef: "VERT"}}, principals,
		"never served from the client's cache: a revoked party drops out on the next call")
}

func TestClient_GetTenantPrincipals_MapsStatusesLikeGetTenantConfig(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unknown or unassociated tenant", http.StatusNotFound, `{"code":"TN-0103"}`, core.ErrTenantNotFound},
		{"another service's key", http.StatusForbidden, `{"code":"TN-0116","error":"API key is not authorized for the requested service"}`, core.ErrTenantServiceAccessDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			principals, err := mustNewClient(t, server.URL).GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")

			require.ErrorIs(t, err, tt.want)
			assert.Nil(t, principals)
		})
	}
}

func TestClient_GetTenantPrincipals_ServerErrorTripsBreaker(t *testing.T) {
	calls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, time.Minute))

	_, err := client.GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")
	require.Error(t, err)

	_, err = client.GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")
	require.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
	assert.Equal(t, 1, calls, "an open breaker fails fast without calling the Tenant Manager")
}

func TestClient_GetTenantPrincipals_ZeroValueClientErrsInsteadOfPanicking(t *testing.T) {
	_, err := (&Client{}).GetTenantPrincipals(context.Background(), "tenant-123", "streaming-hub")

	require.Error(t, err)
}
