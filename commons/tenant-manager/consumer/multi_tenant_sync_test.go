package consumer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiTenantConsumer_SyncTenants_EagerModeStartsNewTenant(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	currentTenants := []*client.TenantSummary{
		{ID: "tenant-a", Name: "A", Status: "active"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tenants := currentTenants
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tenants)
	}))
	t.Cleanup(server.Close)

	config := newTestConfig(server.URL)
	consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())
	defer func() { require.NoError(t, consumer.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer.discoverTenants(ctx)

	// Add tenant-b to API response
	mu.Lock()
	currentTenants = append(currentTenants, &client.TenantSummary{
		ID: "tenant-b", Name: "B", Status: "active",
	})
	mu.Unlock()

	err := consumer.syncTenants(ctx)
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		consumer.mu.RLock()
		defer consumer.mu.RUnlock()

		_, active := consumer.tenants["tenant-b"]
		return consumer.knownTenants["tenant-b"] && active
	}, time.Second, 10*time.Millisecond)
}
