//go:build unit

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
)

func validAssociateServiceRequest() AssociateServiceRequest {
	return AssociateServiceRequest{
		IsolationMode: "dedicated",
		MaxOpenConns:  25,
		MaxIdleConns:  5,
	}
}

func TestClient_AssociateService_Success(t *testing.T) {
	var gotBody AssociateServiceRequest

	var gotAuth, gotAPIKey, gotMethod, gotPath, gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath() // wire form, so %20 escaping is observable
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		gotContentType = r.Header.Get("Content-Type")

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(AssociateServiceResponse{
			ServiceName: "matcher",
			Status:      "active",
		})
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithBearerTokenProvider(func(context.Context) (string, error) {
		return "service-account-jwt", nil
	}))

	resp, err := client.AssociateService(context.Background(), "tenant abc", "matcher", validAssociateServiceRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "matcher", resp.ServiceName)
	assert.Equal(t, "active", resp.Status)

	assert.Equal(t, http.MethodPost, gotMethod)
	// tenantID "tenant abc" must be path-escaped to defend against traversal/injection.
	assert.Equal(t, "/v1/tenants/tenant%20abc/associations/matcher", gotPath)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "Bearer service-account-jwt", gotAuth, "RBAC-gated write must carry the service-account bearer")
	assert.Equal(t, "test-api-key", gotAPIKey, "X-API-Key still sent for parity with the read methods")

	assert.Equal(t, "dedicated", gotBody.IsolationMode)
	assert.Equal(t, 25, gotBody.MaxOpenConns)
	assert.Equal(t, 5, gotBody.MaxIdleConns)
}

func TestClient_AssociateService_Success_200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(AssociateServiceResponse{ServiceName: "matcher", Status: "active"})
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	resp, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "matcher", resp.ServiceName)
}

func TestClient_AssociateService_Conflict_TypedSentinel(t *testing.T) {
	// A 409 means the association already exists: the endpoint is idempotent by
	// identity, so the client surfaces the typed sentinel and the caller converges.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"association already exists"}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrAssociationConflict, "a 409 must map to the typed association-conflict sentinel")

	// A 409 is a valid 4xx round-trip: it must NOT trip the breaker.
	assert.NotEqual(t, cbOpen, client.cbState)
}

func TestClient_AssociateService_ServerError_TripsBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)

	// A 5xx counts as a service failure and trips the breaker on the next call.
	_, err = client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
}

func TestClient_AssociateService_BadRequest_IncludesTruncatedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte("invalid isolation mode"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrAssociationConflict)
	assert.Contains(t, err.Error(), "invalid isolation mode", "a generic 4xx must carry the (truncated) response body")

	// A 4xx is a valid round-trip: it must NOT trip the breaker.
	assert.NotEqual(t, cbOpen, client.cbState)
}

func TestClient_AssociateService_UnexpectedStatus_DoesNotTouchBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(2, 30*time.Second))
	client.cbFailures = 1 // simulate a prior 5xx

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrAssociationConflict)
	assert.Contains(t, err.Error(), "unexpected status 202")
	assert.Equal(t, 1, client.cbFailures, "unexpected status must neither reset nor advance the breaker counter")
}

func TestClient_AssociateService_NoBearerProvider_NoAuthHeader(t *testing.T) {
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(AssociateServiceResponse{ServiceName: "matcher"})
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.NoError(t, err)
	assert.Empty(t, gotAuth, "no bearer provider configured -> no Authorization header")
}

func TestClient_AssociateService_BearerProviderError(t *testing.T) {
	sentinel := errors.New("token acquisition failed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request must not be sent when the bearer token cannot be acquired")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithBearerTokenProvider(func(context.Context) (string, error) {
		return "", sentinel
	}))

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestClient_AssociateService_MalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	_, err := client.AssociateService(context.Background(), "tenant-123", "matcher", validAssociateServiceRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}
