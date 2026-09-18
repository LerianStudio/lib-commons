//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	observability "github.com/LerianStudio/lib-observability/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClient_RoutineFetchesLogAtDebug pins the production symptom this level
// change exists for: every service polls the tenant-manager on a schedule, and
// each poll used to emit a pair of Info lines saying only that a routine read
// started and succeeded. In the consignado gateway that was 38% of all log
// output. Routine reads belong at debug; only negatives and state transitions
// stay at info or above, so the error path is pinned here as well.
func TestClient_RoutineFetchesLogAtDebug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		okBody     string
		fetchMsg   string
		successMsg string
		call       func(ctx context.Context, c *Client) error
	}{
		{
			name:       "tenant config",
			okBody:     `{"id":"tenant-123","tenantSlug":"acme","service":"ledger","status":"active"}`,
			fetchMsg:   "fetching tenant config",
			successMsg: "successfully fetched tenant config",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetTenantConfig(ctx, "tenant-123", "ledger")

				return err
			},
		},
		{
			name:       "active tenants",
			okBody:     `[{"id":"tenant-123","name":"Acme","status":"active"}]`,
			fetchMsg:   "fetching active tenants",
			successMsg: "successfully fetched active tenants",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetActiveTenantsByService(ctx, "ledger")

				return err
			},
		},
		{
			name:       "tenant metadata",
			okBody:     `{"id":"tenant-123","metadata":{"midaz_org_id":"org-1"}}`,
			fetchMsg:   "fetching tenant metadata",
			successMsg: "successfully fetched tenant metadata",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetTenantMetadata(ctx, "tenant-123")

				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" success pair at debug", func(t *testing.T) {
			t.Parallel()

			logger, ctx, client := newLogCapturingClient(t, http.StatusOK, tt.okBody)

			require.NoError(t, tt.call(ctx, client))

			assert.True(t, logger.ContainsAtLevel(obs.LevelDebug, tt.fetchMsg),
				"%q must be emitted at debug, entries: %v", tt.fetchMsg, logger.Entries())
			assert.True(t, logger.ContainsAtLevel(obs.LevelDebug, tt.successMsg),
				"%q must be emitted at debug, entries: %v", tt.successMsg, logger.Entries())
			assert.False(t, logger.ContainsAtLevel(obs.LevelInfo, tt.fetchMsg),
				"%q must no longer be emitted at info", tt.fetchMsg)
			assert.False(t, logger.ContainsAtLevel(obs.LevelInfo, tt.successMsg),
				"%q must no longer be emitted at info", tt.successMsg)
		})

		t.Run(tt.name+" failure still at error", func(t *testing.T) {
			t.Parallel()

			logger, ctx, client := newLogCapturingClient(t, http.StatusInternalServerError, `{"error":"boom"}`)

			require.Error(t, tt.call(ctx, client))

			assert.True(t, logger.ContainsAtLevel(obs.LevelError, "tenant manager returned error"),
				"a 500 from the tenant manager must stay at error, entries: %v", logger.Entries())
		})
	}
}

// newLogCapturingClient wires a client against a server answering every request
// with the given status and body. The same capturing logger backs the client and
// the context, so a record is captured whichever of the two the call site reads.
func newLogCapturingClient(t *testing.T, status int, body string) (*testutil.LevelCapturingLogger, context.Context, *Client) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	logger := testutil.NewLevelCapturingLogger()

	client, err := NewClient(server.URL, logger, WithAllowInsecureHTTP(), WithServiceAPIKey("test-api-key"))
	require.NoError(t, err)

	return logger, observability.ContextWithLogger(context.Background(), logger), client
}
