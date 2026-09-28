//go:build unit

package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestResolverRoutesOnlyPlainSelectsToReplica drives each statement through
// the resolver's QueryContext and QueryRowContext over two sqlmock pools; a
// statement landing on the pool that did not expect it fails the test.
func TestResolverRoutesOnlyPlainSelectsToReplica(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		toReplica bool
	}{
		{name: "select", query: "SELECT id FROM t WHERE id = $1", toReplica: true},
		{name: "lowercase select after whitespace", query: " \n\tselect id FROM t", toReplica: true},
		{name: "select after comments", query: "-- list\n/* hint */ SELECT id FROM t", toReplica: true},
		{name: "update without returning", query: "UPDATE t SET a = 1 WHERE id = $1"},
		{name: "insert without returning", query: "INSERT INTO t (a) VALUES (1)"},
		{name: "select for update", query: "SELECT id FROM t WHERE id = $1 FOR UPDATE"},
		{name: "select for no key update", query: "SELECT id FROM t FOR NO KEY UPDATE SKIP LOCKED"},
		{name: "select for share", query: "SELECT id FROM t for share"},
		{name: "select for key share", query: "SELECT id FROM t FOR KEY SHARE"},
		{name: "read cte", query: "WITH x AS (SELECT 1) SELECT * FROM x"},
		{name: "write behind a commented select", query: "-- SELECT\nDELETE FROM t WHERE id = 1"},
		{name: "select nextval", query: "SELECT nextval('position_request_control_seq')"},
		{name: "select setval", query: "SELECT setval('s', 42)"},
		{name: "select set_config", query: "SELECT set_config('app.tenant', $1, false)"},
		{name: "select try advisory xact lock", query: "select PG_TRY_ADVISORY_XACT_LOCK ($1)"},
		{name: "select advisory unlock shared", query: "SELECT pg_advisory_unlock_shared(7)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary, primaryMock := mockPool(t)
			replica, replicaMock := mockPool(t)

			client, err := NewFromPools(primary, replica, Config{})
			require.NoError(t, err)

			resolver, err := client.Resolver(context.Background())
			require.NoError(t, err)

			target := primaryMock
			if tt.toReplica {
				target = replicaMock
			}

			for range 2 {
				target.ExpectQuery(regexp.QuoteMeta(tt.query)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
			}

			rows, err := resolver.QueryContext(context.Background(), tt.query)
			require.NoError(t, err)
			require.NoError(t, rows.Close())

			var n int
			require.NoError(t, resolver.QueryRowContext(context.Background(), tt.query).Scan(&n))
		})
	}
}
