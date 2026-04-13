// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package tmratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustNewTestClient creates a RateLimitClient pointing at a test server.
// It sets AllowInsecureHTTP since httptest servers use http://.
func mustNewTestClient(t *testing.T, baseURL string) *RateLimitClient {
	t.Helper()

	c, err := NewRateLimitClient(ClientConfig{
		BaseURL:            baseURL,
		APIKey:             "test-api-key",
		Service:            "ledger",
		AllowInsecureHTTP:  true,
	})
	require.NoError(t, err)

	return c
}

func TestNewRateLimitClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ClientConfig
		wantErr string
	}{
		{
			name: "valid HTTPS config",
			cfg: ClientConfig{
				BaseURL: "https://tenant-manager:8080",
				APIKey:  "my-key",
				Service: "ledger",
			},
		},
		{
			name: "valid HTTP with AllowInsecureHTTP",
			cfg: ClientConfig{
				BaseURL:           "http://localhost:8080",
				APIKey:            "my-key",
				Service:           "ledger",
				AllowInsecureHTTP: true,
			},
		},
		{
			name: "missing base URL",
			cfg: ClientConfig{
				BaseURL: "",
				APIKey:  "my-key",
			},
			wantErr: "invalid tenant manager baseURL",
		},
		{
			name: "malformed base URL",
			cfg: ClientConfig{
				BaseURL: "://bad",
				APIKey:  "my-key",
			},
			wantErr: "invalid tenant manager baseURL",
		},
		{
			name: "HTTP without AllowInsecureHTTP",
			cfg: ClientConfig{
				BaseURL: "http://localhost:8080",
				APIKey:  "my-key",
			},
			wantErr: core.ErrInsecureHTTP.Error(),
		},
		{
			name: "missing API key",
			cfg: ClientConfig{
				BaseURL:           "http://localhost:8080",
				APIKey:            "",
				AllowInsecureHTTP: true,
			},
			wantErr: core.ErrServiceAPIKeyRequired.Error(),
		},
		{
			name: "whitespace-only API key",
			cfg: ClientConfig{
				BaseURL:           "http://localhost:8080",
				APIKey:            "   ",
				AllowInsecureHTTP: true,
			},
			wantErr: core.ErrServiceAPIKeyRequired.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewRateLimitClient(tc.cfg)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestRateLimitClient_FetchSettings(t *testing.T) {
	t.Parallel()

	t.Run("success 200 with valid JSON", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/v1/tenants/tenant-123/associations/ledger/settings/rate-limit", r.URL.Path)
			assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"))
			assert.Equal(t, "application/json", r.Header.Get("Accept"))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{"rateLimit":{"default":{"max":100,"window":60},"aggressive":{"max":20,"window":10}}}`))
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		settings, err := client.FetchSettings(context.Background(), "tenant-123")

		require.NoError(t, err)
		require.NotNil(t, settings)
		assert.Equal(t, 100, settings["default"].Max)
		assert.Equal(t, 60, settings["default"].Window)
		assert.Equal(t, 20, settings["aggressive"].Max)
		assert.Equal(t, 10, settings["aggressive"].Window)
	})

	t.Run("404 returns nil nil", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		settings, err := client.FetchSettings(context.Background(), "tenant-123")

		assert.NoError(t, err)
		assert.Nil(t, settings)
	})

	t.Run("500 returns error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		settings, err := client.FetchSettings(context.Background(), "tenant-123")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 500")
		assert.Nil(t, settings)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{invalid json`))
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		settings, err := client.FetchSettings(context.Background(), "tenant-123")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse rate limit response")
		assert.Nil(t, settings)
	})

	t.Run("empty tenantID returns error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		settings, err := client.FetchSettings(context.Background(), "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "tenant ID must not be empty")
		assert.Nil(t, settings)
	})

	t.Run("whitespace tenantID returns error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		settings, err := client.FetchSettings(context.Background(), "   ")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "tenant ID must not be empty")
		assert.Nil(t, settings)
	})

	t.Run("X-API-Key header is sent", func(t *testing.T) {
		t.Parallel()

		var receivedAPIKey string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAPIKey = r.Header.Get("X-API-Key")
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		_, _ = client.FetchSettings(context.Background(), "tenant-1")

		assert.Equal(t, "test-api-key", receivedAPIKey)
	})

	t.Run("tenantID is path-escaped in URL", func(t *testing.T) {
		t.Parallel()

		var receivedPath string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.RawPath
			if receivedPath == "" {
				receivedPath = r.URL.Path
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		_, _ = client.FetchSettings(context.Background(), "tenant/with/slashes")

		assert.Contains(t, receivedPath, "tenant%2Fwith%2Fslashes")
	})

	t.Run("canceled context returns error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		settings, err := client.FetchSettings(ctx, "tenant-1")

		require.Error(t, err)
		assert.Nil(t, settings)
	})
}
