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

func validCreateTenantRequest() CreateTenantRequest {
	return CreateTenantRequest{
		Name:        "Acme Payments",
		Slug:        "acme-payments",
		Environment: "production",
		Isolation:   "shared",
		Metadata:    map[string]string{"origin": "self-serve"},
		Owner: TenantOwner{
			Email:         "founder@acme.com",
			PasswordHash:  "$2a$10$abcdefghijklmnopqrstuv",
			EmailVerified: true,
		},
	}
}

func TestClient_CreateTenant_Success(t *testing.T) {
	var gotBody CreateTenantRequest

	var gotAuth, gotAPIKey, gotMethod, gotPath, gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		gotContentType = r.Header.Get("Content-Type")

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateTenantResponse{
			ID:     "tenant-123",
			Slug:   "acme-payments",
			Status: "active",
		})
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithBearerTokenProvider(func(context.Context) (string, error) {
		return "service-account-jwt", nil
	}))

	resp, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "tenant-123", resp.ID)
	assert.Equal(t, "acme-payments", resp.Slug)
	assert.Equal(t, "active", resp.Status)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v1/tenants", gotPath)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "Bearer service-account-jwt", gotAuth, "RBAC-gated write must carry the service-account bearer")
	assert.Equal(t, "test-api-key", gotAPIKey, "X-API-Key still sent for parity with the read methods")

	assert.Equal(t, "Acme Payments", gotBody.Name)
	assert.Equal(t, "acme-payments", gotBody.Slug)
	assert.Equal(t, "production", gotBody.Environment)
	assert.Equal(t, "shared", gotBody.Isolation)
	assert.Equal(t, "self-serve", gotBody.Metadata["origin"])
	assert.Equal(t, "founder@acme.com", gotBody.Owner.Email)
	assert.True(t, gotBody.Owner.EmailVerified, "owner email is born verified")
	assert.NotEmpty(t, gotBody.Owner.PasswordHash)
}

func TestClient_CreateTenant_ExistingTenant_Idempotent(t *testing.T) {
	// The Tenant Manager is idempotent by tenant identity: a repeat create returns
	// the existing tenant with 200 OK. The client must treat that as success.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(CreateTenantResponse{ID: "tenant-123", Slug: "acme-payments", Status: "active"})
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	resp, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "tenant-123", resp.ID)
}

func TestClient_CreateTenant_NoBearerProvider_NoAuthHeader(t *testing.T) {
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateTenantResponse{ID: "t1"})
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.NoError(t, err)
	assert.Empty(t, gotAuth, "no bearer provider configured -> no Authorization header")
}

func TestClient_CreateTenant_BearerProviderError(t *testing.T) {
	sentinel := errors.New("token acquisition failed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request must not be sent when the bearer token cannot be acquired")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithBearerTokenProvider(func(context.Context) (string, error) {
		return "", sentinel
	}))

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestClient_CreateTenant_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)

	// A 5xx counts as a service failure and trips the breaker on the next call.
	_, err = client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
}

func TestClient_CreateTenant_MalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}

func TestClient_CreateTenant_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // close immediately so the dial fails

	client := mustNewClient(t, url)

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "execute request")
}

func TestClient_CreateTenant_Conflict_TypedSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"slug already taken"}`))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantConflict, "a 409 must map to the typed conflict sentinel")

	// A 409 is a valid 4xx round-trip: it must NOT trip the breaker.
	assert.NotEqual(t, cbOpen, client.cbState)
}

func TestClient_CreateTenant_BadRequest_IncludesTruncatedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte("invalid company name"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid company name", "a generic 4xx error must carry the (truncated) response body")
}

func TestClient_CreateTenant_UnexpectedStatus_DoesNotTouchBreaker(t *testing.T) {
	// An out-of-contract status (202, 3xx, ...) is neither a business rejection
	// nor a service failure: it must not reset the consecutive-failure counter
	// (mirrors handleGetTenantConfigStatus's default branch).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(2, 30*time.Second))
	client.cbFailures = 1 // simulate a prior 5xx

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrTenantConflict)
	assert.Contains(t, err.Error(), "unexpected status 202")
	assert.Equal(t, 1, client.cbFailures, "unexpected status must neither reset nor advance the breaker counter")
}

func TestClient_CreateTenant_BadRequest_NotBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid slug"))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(1, 30*time.Second))

	_, err := client.CreateTenant(context.Background(), validCreateTenantRequest())
	require.Error(t, err)

	// A 4xx is a valid round-trip: it must NOT trip the breaker.
	assert.NotEqual(t, cbOpen, client.cbState)
}
