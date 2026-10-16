//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One tenant in its two UUID spellings: the Tenant Manager's dashless ID and
// the dashed UUID a payload or a caller may carry.
const (
	tenantOneDashless = "11111111111111111111111111111111"
	tenantOneDashed   = "11111111-1111-1111-1111-111111111111"
	tenantTwoDashless = "22222222222222222222222222222222"
	tenantTwoDashed   = "22222222-2222-2222-2222-222222222222"
)

// TestExclusiveVHosts_OneTenantTwoSpellingsShareOneOwner covers a tenant that
// holds its vhost under one spelling and asks again under another: it is the
// same owner and gets the same connection, never a refusal naming itself.
func TestExclusiveVHosts_OneTenantTwoSpellingsShareOneOwner(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	tests := []struct {
		name      string
		requested string
	}{
		{name: "dashed", requested: tenantOneDashed},
		{name: "dashless", requested: tenantOneDashless},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{tenantOneDashless: shared})
			m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

			held := connectViaCanonical(t, m, tenantOneDashed, shared)

			conn, err := m.GetConnection(context.Background(), tt.requested)
			require.NoError(t, err)
			assert.Same(t, held, conn, "one tenant, one connection")
			assert.Equal(t, 1, m.Stats().TotalConnections)
			assert.Equal(t, []string{tenantOneDashless}, m.Stats().TenantIDs)
			assert.Len(t, m.vhosts, 1, "one owner of the vhost")

			// A zero-value connection cannot be closed; a nil one closes as a no-op.
			m.connections[tenantOneDashless] = nil

			require.NoError(t, m.CloseConnection(context.Background(), tt.requested))
			assert.Empty(t, m.vhosts, "closing under either spelling releases the claim")
			assert.Zero(t, m.Stats().TotalConnections)
		})
	}
}

// TestExclusiveVHosts_DistinctTenantsStillRefused covers two different
// tenants configured onto one vhost, each reached under a spelling the other
// does not use: still refused, and the refusal names both canonically.
func TestExclusiveVHosts_DistinctTenantsStillRefused(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	t.Run("held by the other tenant", func(t *testing.T) {
		t.Parallel()

		c := rabbitConfigServer(t, map[string]core.RabbitMQConfig{tenantOneDashless: shared, tenantTwoDashless: shared})
		m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())
		connectViaCanonical(t, m, tenantOneDashed, shared)

		conn, err := m.GetConnection(context.Background(), tenantTwoDashed)
		require.ErrorIs(t, err, core.ErrVHostConflict)
		assert.Nil(t, conn)
		assert.Contains(t, err.Error(), "requested by tenant "+tenantTwoDashless+" is held by tenant "+tenantOneDashless)
		assert.NotContains(t, err.Error(), "failed to connect", "the refusal must come before the dial")
	})

	t.Run("configured for both, no holder", func(t *testing.T) {
		t.Parallel()

		dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{tenantOneDashless: shared, tenantTwoDashless: shared})
		dir.list(tenantTwoDashless, "")
		dir.list(tenantTwoDashed, "active")

		m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

		for _, requested := range []string{tenantOneDashed, tenantTwoDashless} {
			conn, err := m.GetConnection(context.Background(), requested)
			require.ErrorIs(t, err, core.ErrVHostConflict, "tenant %s", requested)
			assert.Nil(t, conn)
			assert.Contains(t, err.Error(), "configured for tenants "+tenantOneDashless+", "+tenantTwoDashless)
		}

		assert.Empty(t, m.vhosts)
	})
}

// TestExclusiveVHosts_MixedSpellingCensus covers a Tenant Manager listing that
// spells one tenant twice: the census holds that tenant once, so a request
// under a third spelling is not refused as a second tenant of the vhost.
func TestExclusiveVHosts_MixedSpellingCensus(t *testing.T) {
	t.Parallel()

	shared := core.RabbitMQConfig{Host: unreachableHost, Port: unreachablePort, VHost: "shared"}

	dir, c := newTenantDirectory(t, map[string]core.RabbitMQConfig{
		tenantOneDashless: shared,
		tenantTwoDashless: {Host: unreachableHost, Port: unreachablePort, VHost: "other"},
	})
	dir.list(tenantOneDashed, "active")
	dir.list(tenantTwoDashless, "")
	dir.list(tenantTwoDashed, "active")

	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

	census, err := m.vhostCensus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]vhostClaim{
		tenantOneDashless: claimFor(&shared),
		tenantTwoDashless: {broker: "127.0.0.1:1", vhost: "other"},
	}, census, "every census entry under the canonical ID, once per tenant")

	_, err = m.GetConnection(context.Background(), "11111111-1111-1111-1111-111111111111")
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrVHostConflict)
	assert.Contains(t, err.Error(), "failed to connect to RabbitMQ", "admitted, then the dial fails")
}

// TestGetConnection_RejectsAnInvalidTenantID covers an ID that has no
// canonical form: refused before any Tenant Manager call or dial.
func TestGetConnection_RejectsAnInvalidTenantID(t *testing.T) {
	t.Parallel()

	m := NewManager(rabbitConfigServer(t, nil), "ledger", WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())

	for _, id := range []string{"bad id", "{11111111-1111-1111-1111-111111111111}", "../etc"} {
		conn, err := m.GetConnection(context.Background(), id)
		require.ErrorIs(t, err, core.ErrInvalidTenantIDFormat, "tenant %q", id)
		assert.Nil(t, conn)
		require.ErrorIs(t, m.CloseConnection(context.Background(), id), core.ErrInvalidTenantIDFormat)
	}
}

// connectViaCanonical records tenantID, canonicalised as the manager does, as
// the live holder of cfg's vhost and returns its connection.
func connectViaCanonical(t *testing.T, m *Manager, tenantID string, cfg core.RabbitMQConfig) *amqp.Connection {
	t.Helper()

	canonical, err := core.CanonicalTenantID(tenantID)
	require.NoError(t, err)

	seedHolder(m, canonical, cfg)

	return m.connections[canonical]
}
