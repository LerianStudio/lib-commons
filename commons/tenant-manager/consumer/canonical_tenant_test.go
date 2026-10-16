//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tenantOneDashless = "11111111111111111111111111111111"
	tenantOneDashed   = "11111111-1111-1111-1111-111111111111"
	tenantTwoDashless = "22222222222222222222222222222222"
	tenantTwoDashed   = "22222222-2222-2222-2222-222222222222"
)

// TestConsumer_OneTenantTwoSpellingsIsOneConsumer covers a Tenant Manager
// listing that spells one tenant twice, and a caller that names it a third
// way: one consumer per tenant, started, found and stopped under any spelling.
func TestConsumer_OneTenantTwoSpellingsIsOneConsumer(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, []*client.TenantSummary{
		{ID: tenantOneDashed, Status: "active"},
		{ID: tenantOneDashless, Status: "active"},
		{ID: tenantTwoDashed, Status: "active"},
		{ID: "bad id", Status: "active"},
	})
	c, err := NewMultiTenantConsumerWithError(newTestConfig(server.URL), testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
	require.NoError(t, err)

	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.Run(context.Background()))

	started, err := c.StartActiveTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, started, "two tenants, whatever the spellings")
	assert.ElementsMatch(t, []string{tenantOneDashless, tenantTwoDashless}, c.Stats().TenantIDs)

	c.EnsureConsumerStarted(context.Background(), tenantTwoDashless)
	c.EnsureConsumerStarted(context.Background(), "bad id")
	assert.ElementsMatch(t, []string{tenantOneDashless, tenantTwoDashless}, c.Stats().TenantIDs)

	c.StopConsumer(tenantOneDashed)
	assert.Equal(t, []string{tenantTwoDashless}, c.Stats().TenantIDs, "stopped under the other spelling")
}
