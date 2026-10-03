//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// tenantDirectory is a fake Tenant Manager: it lists tenants on
// /v1/tenants/active and serves each one's legacy single RabbitMQ config from
// its connections endpoint. Every field is guarded by mu, so a test can change
// the directory while a manager reads it.
type tenantDirectory struct {
	mu         sync.Mutex
	configs    map[string]core.RabbitMQConfig // tenant -> config; absent answers 404
	statuses   map[string]string              // tenant -> status listed as active; absent is not listed
	failConfig map[string]bool                // tenant -> config endpoint answers 500
	activeDown bool                           // the active-tenants endpoint answers 503
	block      chan struct{}                  // when set, the active-tenants endpoint waits for it to close

	activeHits atomic.Int32
}

// newTenantDirectory serves configs and lists every one of their tenants as
// active.
func newTenantDirectory(t *testing.T, configs map[string]core.RabbitMQConfig) (*tenantDirectory, *client.Client) {
	t.Helper()

	d := &tenantDirectory{
		configs:    make(map[string]core.RabbitMQConfig, len(configs)),
		statuses:   make(map[string]string, len(configs)),
		failConfig: make(map[string]bool),
	}

	for tenantID, cfg := range configs {
		d.configs[tenantID] = cfg
		d.statuses[tenantID] = "active"
	}

	server := httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	return d, c
}

func (d *tenantDirectory) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/tenants/active" {
		d.serveActive(w)

		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for tenantID, cfg := range d.configs {
		if !strings.Contains(r.URL.Path, "/tenants/"+tenantID+"/") {
			continue
		}

		if d.failConfig[tenantID] {
			w.WriteHeader(http.StatusInternalServerError)

			return
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
}

func (d *tenantDirectory) serveActive(w http.ResponseWriter) {
	d.activeHits.Add(1)

	d.mu.Lock()
	block := d.block
	d.mu.Unlock()

	if block != nil {
		<-block
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.activeDown {
		w.WriteHeader(http.StatusServiceUnavailable)

		return
	}

	type summary struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	listed := make([]summary, 0, len(d.statuses))
	for tenantID, status := range d.statuses {
		listed = append(listed, summary{ID: tenantID, Name: tenantID, Status: status})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(listed)
}

// set configures tenantID's RabbitMQ config.
func (d *tenantDirectory) set(tenantID string, cfg core.RabbitMQConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.configs[tenantID] = cfg
}

// list lists tenantID on the active endpoint with status; "" unlists it.
func (d *tenantDirectory) list(tenantID, status string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if status == "" {
		delete(d.statuses, tenantID)

		return
	}

	d.statuses[tenantID] = status
}

func (d *tenantDirectory) setActiveDown(down bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.activeDown = down
}

func (d *tenantDirectory) setFailConfig(tenantID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.failConfig[tenantID] = true
}

func (d *tenantDirectory) blockActive() (release func()) {
	ch := make(chan struct{})

	d.mu.Lock()
	d.block = ch
	d.mu.Unlock()

	return sync.OnceFunc(func() { close(ch) })
}

// rabbitConfigServer serves configs and lists every one of their tenants as
// active.
func rabbitConfigServer(t *testing.T, configs map[string]core.RabbitMQConfig) *client.Client {
	t.Helper()

	_, c := newTenantDirectory(t, configs)

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

// TestExclusiveVHosts_LiveClaimRefusesBeforeDial covers the second layer: the
// holder's config is not in the census (it no longer lists that vhost, or the
// census is older than its connection), yet it still holds the vhost.
func TestExclusiveVHosts_LiveClaimRefusesBeforeDial(t *testing.T) {
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
// before the holder reconnects. Neither tenant is listed as active, so the
// configuration census sees no sharing and the live claim alone decides.
func TestExclusiveVHosts_ClaimOutlivesADroppedConnection(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: shared})
	dir.list(vhostHolder, "")
	dir.list(vhostRequester, "")

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

const (
	vhostTenantC = "tenant-c"
	// longInterval keeps a built census fresh for the whole test.
	longInterval = time.Hour
	// alwaysStale makes every census read rebuild it.
	alwaysStale = time.Nanosecond
)

// assertNoSecret fails when the error or any captured log line carries the
// tenants' RabbitMQ password.
func assertNoSecret(t *testing.T, err error, logger *testutil.LevelCapturingLogger) {
	t.Helper()

	if err != nil {
		assert.NotContains(t, err.Error(), vhostSecret)
	}

	if logger == nil {
		return
	}

	for _, entry := range logger.Entries() {
		assert.NotContains(t, entry.Message, vhostSecret)
	}
}

// TestExclusiveVHosts_ConfiguredSharedVHostRefusesEveryTenant pins BRSFN-67:
// two active tenants configured for one vhost are both refused, before any
// dial, whichever connects first.
func TestExclusiveVHosts_ConfiguredSharedVHostRefusesEveryTenant(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	for _, order := range [][]string{{vhostHolder, vhostRequester}, {vhostRequester, vhostHolder}} {
		t.Run(strings.Join(order, " then "), func(t *testing.T) {
			t.Parallel()

			c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: shared})
			logger := testutil.NewLevelCapturingLogger()
			m := NewManager(c, "ledger", WithLogger(logger), WithExclusiveVHosts())

			for _, tenantID := range order {
				conn, err := m.GetConnection(context.Background(), tenantID)
				require.ErrorIs(t, err, core.ErrVHostConflict, "tenant %s", tenantID)
				assert.Nil(t, conn)

				msg := err.Error()
				assert.Contains(t, msg, "127.0.0.1:1")
				assert.Contains(t, msg, `"shared"`)
				assert.Contains(t, msg, vhostHolder+", "+vhostRequester, "both tenants, sorted")
				assert.NotContains(t, msg, "failed to connect", "the refusal must come before the dial")
				assertNoSecret(t, err, logger)
			}

			assert.Zero(t, m.Stats().TotalConnections)
			assert.Empty(t, m.vhosts)
		})
	}
}

func TestExclusiveVHosts_DistinctConfiguredVHostsReachDial(t *testing.T) {
	t.Parallel()

	c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{
		vhostHolder:    {Host: unreachableHost, Port: unreachablePort, VHost: "tenant-a"},
		vhostRequester: {Host: unreachableHost, Port: unreachablePort, VHost: "tenant-b"},
		vhostTenantC:   {Host: unreachableHost, Port: 2, VHost: "tenant-a"},
	})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

	for _, tenantID := range []string{vhostHolder, vhostRequester, vhostTenantC} {
		_, err := m.GetConnection(context.Background(), tenantID)
		require.Error(t, err)
		assert.NotErrorIs(t, err, core.ErrVHostConflict, "tenant %s", tenantID)
		assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
	}
}

// TestExclusiveVHosts_TenantsOutsideTheCensusDoNotBlock covers tenants whose
// config names the vhost but which cannot connect: listed with a non-active
// status, not listed, or listed without a config for this service.
func TestExclusiveVHosts_TenantsOutsideTheCensusDoNotBlock(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	tests := []struct {
		name  string
		setup func(d *tenantDirectory)
	}{
		{name: "listed as inactive", setup: func(d *tenantDirectory) { d.list(vhostRequester, "inactive") }},
		{name: "not listed", setup: func(d *tenantDirectory) { d.list(vhostRequester, "") }},
		{name: "listed without a config", setup: func(d *tenantDirectory) {
			d.mu.Lock()
			delete(d.configs, vhostRequester)
			d.mu.Unlock()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: shared})
			tt.setup(dir)

			m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

			_, err := m.GetConnection(context.Background(), vhostHolder)
			require.Error(t, err)
			assert.NotErrorIs(t, err, core.ErrVHostConflict)
			assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
		})
	}
}

// TestExclusiveVHosts_ConnectingTenantFreshConfigOverlaysCensus covers a
// census older than the connecting tenant's config: its fresh config is what
// counts, so a move onto a shared vhost is refused at once.
func TestExclusiveVHosts_ConnectingTenantFreshConfigOverlaysCensus(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}
	own := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "own"}

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: own})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()),
		WithExclusiveVHosts(), WithConnectionsCheckInterval(longInterval))

	_, err := m.GetConnection(context.Background(), vhostRequester)
	require.NotErrorIs(t, err, core.ErrVHostConflict)

	dir.set(vhostRequester, shared)

	_, err = m.GetConnection(context.Background(), vhostRequester)
	require.ErrorIs(t, err, core.ErrVHostConflict)
	assert.Contains(t, err.Error(), vhostHolder)
	assert.Equal(t, int32(1), dir.activeHits.Load(), "the cached census was reused")
}

// TestExclusiveVHosts_RevalidationClosesTenantsOnANewlySharedVHost covers a
// config change that makes a vhost shared while both tenants are connected:
// at their next revalidation both lose their connection and claim, whichever
// revalidates first.
func TestExclusiveVHosts_RevalidationClosesTenantsOnANewlySharedVHost(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}
	own := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "own"}

	for _, order := range [][]string{{vhostHolder, vhostRequester}, {vhostRequester, vhostHolder}} {
		t.Run(strings.Join(order, " then "), func(t *testing.T) {
			t.Parallel()

			dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: own})
			logger := testutil.NewLevelCapturingLogger()
			m := NewManager(c, "ledger", WithLogger(logger),
				WithExclusiveVHosts(), WithConnectionsCheckInterval(alwaysStale))

			seedConnected(m, vhostHolder, shared)
			seedConnected(m, vhostRequester, own)

			dir.set(vhostRequester, shared)

			for _, tenantID := range order {
				m.revalidatePoolSettings(tenantID)
			}

			for _, tenantID := range []string{vhostHolder, vhostRequester} {
				assert.NotContains(t, m.connections, tenantID, "tenant %s keeps a connection", tenantID)
				assert.NotContains(t, m.vhosts, tenantID, "tenant %s keeps its claim", tenantID)
				assert.True(t, logger.ContainsAtLevel(obs.LevelError, tenantID, vhostHolder, vhostRequester, "shared"),
					"ERROR naming the tenants for %s: %v", tenantID, logger.Entries())
			}

			assertNoSecret(t, nil, logger)
		})
	}
}

// TestExclusiveVHosts_RevalidationKeepsAnUnsharedTenant is the control: a
// census that confirms the vhost is the tenant's own leaves it connected.
func TestExclusiveVHosts_RevalidationKeepsAnUnsharedTenant(t *testing.T) {
	t.Parallel()

	own := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "own"}

	c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{vhostHolder: own})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()),
		WithExclusiveVHosts(), WithConnectionsCheckInterval(alwaysStale))
	seedConnected(m, vhostHolder, own)

	m.revalidatePoolSettings(vhostHolder)

	assert.Contains(t, m.connections, vhostHolder)
	assert.Equal(t, claimFor(&own), m.vhosts[vhostHolder])
}

func TestExclusiveVHosts_FixingTheConfigAdmitsBothTenants(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: shared})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()),
		WithExclusiveVHosts(), WithConnectionsCheckInterval(alwaysStale))

	_, err := m.GetConnection(context.Background(), vhostHolder)
	require.ErrorIs(t, err, core.ErrVHostConflict)

	dir.set(vhostRequester, core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "own"})

	for _, tenantID := range []string{vhostHolder, vhostRequester} {
		_, err = m.GetConnection(context.Background(), tenantID)
		require.Error(t, err)
		assert.NotErrorIs(t, err, core.ErrVHostConflict, "tenant %s", tenantID)
		assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
	}
}

func TestExclusiveVHosts_CensusUnavailable(t *testing.T) {
	t.Parallel()

	own := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "own"}

	t.Run("no census yet refuses without dialing", func(t *testing.T) {
		t.Parallel()

		dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: own})
		dir.setActiveDown(true)

		m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

		_, err := m.GetConnection(context.Background(), vhostHolder)
		require.ErrorIs(t, err, core.ErrVHostCensusUnavailable)
		assert.NotErrorIs(t, err, core.ErrVHostConflict)
		assert.NotContains(t, err.Error(), "failed to connect")
	})

	t.Run("a tenant config the census cannot read fails it", func(t *testing.T) {
		t.Parallel()

		dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: own, vhostTenantC: own})
		dir.setFailConfig(vhostTenantC)

		m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

		_, err := m.GetConnection(context.Background(), vhostHolder)
		require.ErrorIs(t, err, core.ErrVHostCensusUnavailable)
		assert.NotContains(t, err.Error(), "failed to connect")
	})

	t.Run("a prior census is used with a WARN", func(t *testing.T) {
		t.Parallel()

		shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

		dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{
			vhostHolder: own, vhostRequester: shared, vhostTenantC: shared,
		})
		logger := testutil.NewLevelCapturingLogger()
		m := NewManager(c, "ledger", WithLogger(logger),
			WithExclusiveVHosts(), WithConnectionsCheckInterval(alwaysStale))

		_, err := m.GetConnection(context.Background(), vhostHolder)
		require.Contains(t, err.Error(), "failed to connect to RabbitMQ")

		dir.setActiveDown(true)

		_, err = m.GetConnection(context.Background(), vhostHolder)
		require.Error(t, err)
		assert.NotErrorIs(t, err, core.ErrVHostCensusUnavailable)
		assert.Contains(t, err.Error(), "failed to connect to RabbitMQ", "the last census admits the tenant")

		_, err = m.GetConnection(context.Background(), vhostRequester)
		require.ErrorIs(t, err, core.ErrVHostConflict, "the last census still refuses a shared vhost")

		assert.True(t, logger.ContainsAtLevel(obs.LevelWarn, "census"), "%v", logger.Entries())
	})
}

func TestExclusiveVHosts_CensusCachedWithinInterval(t *testing.T) {
	t.Parallel()

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{
		vhostHolder:    {Host: unreachableHost, Port: unreachablePort, VHost: "tenant-a"},
		vhostRequester: {Host: unreachableHost, Port: unreachablePort, VHost: "tenant-b"},
	})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()),
		WithExclusiveVHosts(), WithConnectionsCheckInterval(longInterval))

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			tenantID := vhostHolder
			if i%2 == 1 {
				tenantID = vhostRequester
			}

			_, err := m.GetConnection(context.Background(), tenantID)
			assert.NotErrorIs(t, err, core.ErrVHostConflict)
		}()
	}

	wg.Wait()

	assert.Equal(t, int32(1), dir.activeHits.Load())
}

// TestExclusiveVHosts_CensusWaitHonoursCallerContext covers a stalled Tenant
// Manager: a caller whose context ends stops waiting for the census.
func TestExclusiveVHosts_CensusWaitHonoursCallerContext(t *testing.T) {
	t.Parallel()

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{
		vhostHolder: {Host: unreachableHost, Port: unreachablePort, VHost: "own"},
	})
	release := dir.blockActive()
	t.Cleanup(release)

	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.GetConnection(ctx, vhostHolder)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestExclusiveVHosts_OffByDefaultMakesNoCensusCalls(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{vhostHolder: shared, vhostRequester: shared})
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithConnectionsCheckInterval(alwaysStale))

	for _, tenantID := range []string{vhostHolder, vhostRequester} {
		_, err := m.GetConnection(context.Background(), tenantID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to connect to RabbitMQ")
	}

	seedConnected(m, vhostHolder, shared)
	m.revalidatePoolSettings(vhostHolder)

	assert.Zero(t, dir.activeHits.Load())
}

// seedConnected records tenantID as connected through cfg, behind a nil
// connection (closing it is a no-op), with the URI the manager would build.
func seedConnected(m *Manager, tenantID string, cfg core.RabbitMQConfig) {
	cfg.Username = "guest"
	cfg.Password = vhostSecret

	m.connections[tenantID] = nil
	m.cachedURIs[tenantID] = connectionKey(buildRabbitMQURI(&cfg, m.resolveTLS(&cfg)), cfg.TLSCAFile)
	m.vhosts[tenantID] = claimFor(&cfg)
	m.lastAccessed[tenantID] = time.Now()
}
