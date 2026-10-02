//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	vhostHolder    = "tenant-a"
	vhostRequester = "tenant-b"
	// unreachableHost:unreachablePort refuses at once, so a call that gets past
	// the vhost check fails with a dial error instead of reaching a broker.
	unreachableHost = "127.0.0.1"
	unreachablePort = 1
	vhostSecret     = "s3cr3t-pass"
)

// rabbitConfigServer serves each tenant's legacy single RabbitMQ config from
// the Tenant Manager connections endpoint, keyed by tenant ID.
func rabbitConfigServer(t *testing.T, configs map[string]core.RabbitMQConfig) *client.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for tenantID, cfg := range configs {
			if !strings.Contains(r.URL.Path, "/tenants/"+tenantID+"/") {
				continue
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{
				"id": %q,
				"tenantSlug": %q,
				"messaging": {
					"rabbitmq": {"host": %q, "port": %d, "vhost": %q, "username": "guest", "password": %q}
				}
			}`, tenantID, tenantID, cfg.Host, cfg.Port, cfg.VHost, vhostSecret)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	return c
}

// seedHolder records holder as the live owner of cfg's vhost. The zero-value
// connection reports itself open; tests that seed it must never Close the
// manager, because closing a zero-value amqp.Connection does not return.
func seedHolder(m *Manager, holder string, cfg core.RabbitMQConfig) {
	m.connections[holder] = &amqp.Connection{}
	m.vhosts[holder] = claimFor(&cfg)
	m.lastAccessed[holder] = time.Now()
}

func TestExclusiveVHosts_RefusesSecondTenantBeforeDial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		holder  core.RabbitMQConfig
		request core.RabbitMQConfig
	}{
		{
			name:    "same host, port and vhost",
			holder:  core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"},
			request: core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"},
		},
		{
			name:    "host differs only in case",
			holder:  core.RabbitMQConfig{Host: "LOCALHOST", Port: unreachablePort, VHost: "shared"},
			request: core.RabbitMQConfig{Host: "localhost", Port: unreachablePort, VHost: "shared"},
		},
		{
			name:    "empty vhost is the default vhost",
			holder:  core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "/"},
			request: core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostRequester: tt.request})
			m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())
			seedHolder(m, vhostHolder, tt.holder)

			conn, err := m.GetConnection(context.Background(), vhostRequester)
			require.ErrorIs(t, err, core.ErrVHostConflict)
			assert.Nil(t, conn)

			msg := err.Error()
			assert.Contains(t, msg, vhostRequester)
			assert.Contains(t, msg, vhostHolder)
			assert.Contains(t, msg, fmt.Sprintf("%s:%d", strings.ToLower(tt.request.Host), tt.request.Port))
			assert.NotContains(t, msg, vhostSecret)
			assert.NotContains(t, msg, "failed to connect", "the refusal must come before the dial")

			_, cached := m.connections[vhostRequester]
			assert.False(t, cached)
			_, claimed := m.vhosts[vhostRequester]
			assert.False(t, claimed)
		})
	}
}

func TestExclusiveVHosts_DistinctVHostReachesDial(t *testing.T) {
	t.Parallel()

	request := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	tests := []struct {
		name       string
		holder     core.RabbitMQConfig
		holderConn *amqp.Connection
	}{
		{
			name:       "same vhost name on another host",
			holder:     core.RabbitMQConfig{Host: "rabbit-a.invalid", Port: unreachablePort, VHost: "shared"},
			holderConn: &amqp.Connection{},
		},
		{
			name:       "same vhost name on another port",
			holder:     core.RabbitMQConfig{Host: unreachableHost, Port: 2, VHost: "shared"},
			holderConn: &amqp.Connection{},
		},
		{
			name:       "another vhost on the same broker",
			holder:     core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "other"},
			holderConn: &amqp.Connection{},
		},
		{
			name:       "vhost names are case-sensitive",
			holder:     core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "Shared"},
			holderConn: &amqp.Connection{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostRequester: request})
			m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())
			seedHolder(m, vhostHolder, tt.holder)
			m.connections[vhostHolder] = tt.holderConn

			_, err := m.GetConnection(context.Background(), vhostRequester)
			require.Error(t, err)
			assert.NotErrorIs(t, err, core.ErrVHostConflict)
			assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
		})
	}
}

// TestExclusiveVHosts_ClaimOutlivesADroppedConnection covers a broker restart
// or network drop: the holder's connection is closed but not released, and a
// misconfigured tenant retrying on the same vhost must not take the vhost
// before the holder reconnects.
func TestExclusiveVHosts_ClaimOutlivesADroppedConnection(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: shared})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())
	seedHolder(m, vhostHolder, shared)

	// No live connection behind the claim (a nil entry reads as dropped).
	m.connections[vhostHolder] = nil

	_, err := m.GetConnection(context.Background(), vhostRequester)
	require.ErrorIs(t, err, core.ErrVHostConflict, "a dropped connection does not release the claim")
	assert.Contains(t, err.Error(), vhostHolder)

	// The holder's own reconnect is not refused by its claim.
	delete(m.connections, vhostHolder)

	_, err = m.GetConnection(context.Background(), vhostHolder)
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrVHostConflict)
	assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
}

func TestExclusiveVHosts_OffByDefault(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostRequester: shared})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))
	seedHolder(m, vhostHolder, shared)

	_, err := m.GetConnection(context.Background(), vhostRequester)
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrVHostConflict)
	assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
}

func TestExclusiveVHosts_ReconnectKeepsOldConnectionOnCollision(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}
	old := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "old"}
	oldURI := "amqp://guest:guest@127.0.0.1:1/old"

	tests := []struct {
		name          string
		opts          []Option
		wantRefusal   bool
		wantDialTried bool
	}{
		{name: "exclusive refuses without dialing", opts: []Option{WithExclusiveVHosts()}, wantRefusal: true},
		{name: "default dials the new config", wantDialTried: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostRequester: shared})
			logger := testutil.NewLevelCapturingLogger()
			m := NewManager(c, "ledger", append([]Option{WithLogger(logger)}, tt.opts...)...)
			seedHolder(m, vhostHolder, shared)

			m.connections[vhostRequester] = nil
			m.cachedURIs[vhostRequester] = oldURI
			m.vhosts[vhostRequester] = claimFor(&old)
			m.lastAccessed[vhostRequester] = time.Now()

			m.revalidatePoolSettings(vhostRequester)

			assert.Equal(t, oldURI, m.cachedURIs[vhostRequester], "the old connection's URI must be kept")
			assert.Equal(t, claimFor(&old), m.vhosts[vhostRequester], "the old vhost claim must be kept")
			assert.Equal(t, tt.wantRefusal,
				logger.ContainsAtLevel(obs.LevelError, vhostRequester, vhostHolder, "shared"),
				"refusal ERROR naming both tenants: %v", logger.Entries())
			assert.Equal(t, tt.wantDialTried,
				logger.ContainsAtLevel(obs.LevelWarn, "keeping old connection"),
				"dial attempt: %v", logger.Entries())

			for _, entry := range logger.Entries() {
				assert.NotContains(t, entry.Message, vhostSecret)
			}
		})
	}
}

func TestExclusiveVHosts_ClaimReleasedWithConnection(t *testing.T) {
	t.Parallel()

	claim := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	seed := func(m *Manager, tenantID string, accessed time.Time) {
		m.connections[tenantID] = nil
		m.cachedURIs[tenantID] = "amqp://guest:guest@127.0.0.1:1/shared"
		m.vhosts[tenantID] = claimFor(&claim)
		m.lastAccessed[tenantID] = accessed
	}

	t.Run("CloseConnection", func(t *testing.T) {
		t.Parallel()

		m := NewManager(mustNewTestClient(t), "ledger", WithExclusiveVHosts())
		seed(m, vhostHolder, time.Now())

		require.NoError(t, m.CloseConnection(context.Background(), vhostHolder))
		assert.NotContains(t, m.vhosts, vhostHolder)
	})

	t.Run("evictLRU", func(t *testing.T) {
		t.Parallel()

		m := NewManager(mustNewTestClient(t), "ledger",
			WithExclusiveVHosts(), WithMaxTenantPools(1), WithIdleTimeout(time.Minute))
		seed(m, vhostHolder, time.Now().Add(-time.Hour))

		m.evictLRU(testutil.NewMockLogger())
		assert.NotContains(t, m.vhosts, vhostHolder)
	})

	t.Run("Close", func(t *testing.T) {
		t.Parallel()

		m := NewManager(mustNewTestClient(t), "ledger", WithExclusiveVHosts())
		seed(m, vhostHolder, time.Now())

		require.NoError(t, m.Close(context.Background()))
		assert.Empty(t, m.vhosts)
	})
}

func TestClaimFor_NeverCarriesCredentials(t *testing.T) {
	t.Parallel()

	got := claimFor(&core.RabbitMQConfig{
		Host: "Rabbit.Example", Port: 5671, VHost: "Tenant-A", Username: "user", Password: vhostSecret,
	})

	assert.Equal(t, vhostClaim{broker: "rabbit.example:5671", vhost: "Tenant-A"}, got)
	assert.NotContains(t, fmt.Sprintf("%+v", got), vhostSecret)
}

// TestExclusiveVHosts_SwapRechecksUnderLock covers a second tenant claiming the
// vhost while the reconnect was dialing: the swap keeps the old connection.
func TestExclusiveVHosts_SwapRechecksUnderLock(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}
	old := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "old"}
	oldURI := "amqp://guest:guest@127.0.0.1:1/old"

	logger := testutil.NewLevelCapturingLogger()
	m := NewManager(mustNewTestClient(t), "ledger", WithLogger(logger), WithExclusiveVHosts())
	seedHolder(m, vhostHolder, shared)

	m.connections[vhostRequester] = nil
	m.cachedURIs[vhostRequester] = oldURI
	m.vhosts[vhostRequester] = claimFor(&old)

	m.swapRabbitMQConnection(vhostRequester, nil, "amqp://guest:guest@127.0.0.1:1/shared", claimFor(&shared))

	assert.Equal(t, oldURI, m.cachedURIs[vhostRequester])
	assert.Equal(t, claimFor(&old), m.vhosts[vhostRequester])
	assert.True(t, logger.ContainsAtLevel(obs.LevelError, vhostRequester, vhostHolder, "keeping old connection"),
		"%v", logger.Entries())
}
