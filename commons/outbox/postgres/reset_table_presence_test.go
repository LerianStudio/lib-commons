//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/stretchr/testify/require"
)

// The dispatcher invokes ResetStuckProcessing and ResetForRetry FIRST each
// cycle (before ListPending). In pool-per-tenant mode a tenant whose outbox
// table is absent must skip these quietly — no 42P01 round-trip — relying on
// the probe-level WARN as the single observability point.
func TestResetMethods_SkipWhenTableMissing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(repo *Repository) ([]*outbox.OutboxEvent, error)
	}{
		{
			name: "ResetStuckProcessing",
			call: func(repo *Repository) ([]*outbox.OutboxEvent, error) {
				return repo.ResetStuckProcessing(validTenantCtx(), 10, time.Now().UTC(), 3)
			},
		},
		{
			name: "ResetForRetry",
			call: func(repo *Repository) ([]*outbox.OutboxEvent, error) {
				return repo.ResetForRetry(validTenantCtx(), 10, time.Now().UTC(), 3)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			probeCalls := 0

			repo := &Repository{
				client:             newTestClient(t),
				tenantResolver:     NoopTenantResolver{},
				tenantDiscoverer:   poolDiscovererShim{},
				poolResolver:       &recordingTenantPoolResolver{},
				requireTenant:      true,
				tableName:          "outbox_events",
				transactionTimeout: time.Second,
				tablePresence: newTablePresenceGuard(func(context.Context, string) (bool, error) {
					probeCalls++
					return false, nil // table absent
				}, time.Hour),
				// Must short-circuit before any tenant transaction is opened.
				primaryDBLookup: func(context.Context) (*sql.DB, error) {
					t.Fatalf("%s: primaryDBLookup must not be reached when table is missing", tc.name)
					return nil, nil
				},
			}

			events, err := tc.call(repo)
			require.NoError(t, err)
			require.Nil(t, events)
			require.Equal(t, 1, probeCalls)
		})
	}
}

// When the table is present the gate must be transparent: the reset query is
// attempted against the tenant pool exactly as before.
func TestResetMethods_ProceedWhenTablePresent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(repo *Repository) ([]*outbox.OutboxEvent, error)
	}{
		{
			name: "ResetStuckProcessing",
			call: func(repo *Repository) ([]*outbox.OutboxEvent, error) {
				return repo.ResetStuckProcessing(validTenantCtx(), 10, time.Now().UTC(), 3)
			},
		},
		{
			name: "ResetForRetry",
			call: func(repo *Repository) ([]*outbox.OutboxEvent, error) {
				return repo.ResetForRetry(validTenantCtx(), 10, time.Now().UTC(), 3)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			require.NoError(t, err)

			defer func() { _ = db.Close() }()

			// Table present -> reset proceeds: a tenant transaction is opened and
			// the select-for-update query is issued. We assert SQL is reached;
			// the exact query/result is exercised elsewhere, so a Begin + query
			// expectation suffices to prove the gate did not skip.
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT").WillReturnRows(
				sqlmock.NewRows([]string{
					"id", "event_type", "aggregate_id", "payload", "status",
					"attempts", "published_at", "last_error", "created_at", "updated_at",
				}),
			)
			mock.ExpectCommit()

			repo := &Repository{
				client:             newTestClient(t),
				tenantResolver:     NoopTenantResolver{},
				tenantDiscoverer:   poolDiscovererShim{},
				poolResolver:       &recordingTenantPoolResolver{},
				requireTenant:      true,
				tableName:          "outbox_events",
				transactionTimeout: time.Second,
				tablePresence: newTablePresenceGuard(func(context.Context, string) (bool, error) {
					return true, nil // table present
				}, time.Hour),
				primaryDBLookup: func(context.Context) (*sql.DB, error) {
					return db, nil
				},
			}

			events, err := tc.call(repo)
			require.NoError(t, err)
			require.Empty(t, events)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
