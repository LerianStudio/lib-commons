//go:build unit

package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
)

func TestClient_ReactivateTenant_Success(t *testing.T) {
	var gotAuth, gotAPIKey, gotMethod, gotPath, gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath() // wire form, so %20 escaping is observable
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		gotContentType = r.Header.Get("Content-Type")

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithBearerTokenProvider(func(context.Context) (string, error) {
		return "service-account-jwt", nil
	}))

	err := client.ReactivateTenant(context.Background(), "tenant abc")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/v1/tenants/tenant%20abc/reactivate", gotPath)
	assert.Empty(t, gotContentType, "a body-less PUT must not set Content-Type")
	assert.Equal(t, "Bearer service-account-jwt", gotAuth, "RBAC-gated write must carry the service-account bearer")
	assert.Equal(t, "test-api-key", gotAPIKey, "X-API-Key still sent for parity with the read methods")
}

func TestClient_ReactivateTenant_Conflict_TolerateNotSuspended(t *testing.T) {
	// Idempotency tolerance: reactivating a not-suspended tenant returns 409,
	// which the client treats as success (nil error).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"not suspended"}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	err := client.ReactivateTenant(context.Background(), "tenant-123")
	require.NoError(t, err, "409 not-suspended must be tolerated as success")

	// A 409 is a valid round-trip: it must NOT trip the breaker.
	assert.NotEqual(t, cbOpen, client.cbState)
}

func TestClient_ReactivateTenant_ServerError_TripsBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	err := client.ReactivateTenant(context.Background(), "tenant-123")
	require.Error(t, err)

	// A 5xx counts as a service failure and trips the breaker on the next call.
	err = client.ReactivateTenant(context.Background(), "tenant-123")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
}

func TestClient_ReactivateTenant_BadRequest_IncludesTruncatedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("tenant not found"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	err := client.ReactivateTenant(context.Background(), "tenant-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant not found", "a generic 4xx must carry the (truncated) response body")

	// A 4xx is a valid round-trip: it must NOT trip the breaker.
	assert.NotEqual(t, cbOpen, client.cbState)
}

func TestClient_ReactivateTenant_UnexpectedStatus_DoesNotTouchBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("found"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(2, 30*time.Second))
	client.cbFailures = 1 // simulate a prior 5xx

	err := client.ReactivateTenant(context.Background(), "tenant-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 302")
	assert.Equal(t, 1, client.cbFailures, "unexpected status must neither reset nor advance the breaker counter")
}

func TestClient_ReactivateTenant_NoBearerProvider_NoAuthHeader(t *testing.T) {
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	err := client.ReactivateTenant(context.Background(), "tenant-123")
	require.NoError(t, err)
	assert.Empty(t, gotAuth, "no bearer provider configured -> no Authorization header")
}

func TestClient_ReactivateTenant_BearerProviderError(t *testing.T) {
	sentinel := errors.New("token acquisition failed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request must not be sent when the bearer token cannot be acquired")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithBearerTokenProvider(func(context.Context) (string, error) {
		return "", sentinel
	}))

	err := client.ReactivateTenant(context.Background(), "tenant-123")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}
