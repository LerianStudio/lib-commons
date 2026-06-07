//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var errLookupConsulted = errors.New("lookup consulted")

// TestMarkPublished_ConsultsTenantPoolLookup proves the publish/mark side
// routes through the same per-tenant pool lookup as the read side, preserving
// at-least-once delivery: an event read from tenant T's pool is marked
// published against tenant T's pool.
func TestMarkPublished_ConsultsTenantPoolLookup(t *testing.T) {
	t.Parallel()

	var (
		gotTenant string
		called    bool
	)

	repo := &Repository{
		client:             newTestClient(t),
		tenantResolver:     NoopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		requireTenant:      true,
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
		primaryDBLookup: func(ctx context.Context) (*sql.DB, error) {
			called = true
			id, _ := outbox.TenantIDFromContext(ctx)
			gotTenant = id
			// Return an error after recording the consultation so the operation
			// short-circuits before BeginTx. We only assert the lookup was
			// consulted with the tenant context (the routing contract).
			return nil, errLookupConsulted
		},
	}

	ctx := outbox.ContextWithTenantID(context.Background(), "22222222-2222-2222-2222-222222222222")

	// BeginTx on a zero-value *sql.DB will error; we ignore the downstream error
	// and assert only that the lookup was consulted with the tenant context.
	_ = repo.MarkPublished(ctx, uuid.New(), time.Now().UTC())

	require.True(t, called, "MarkPublished must consult the tenant pool lookup")
	require.Equal(t, "22222222-2222-2222-2222-222222222222", gotTenant)
}
