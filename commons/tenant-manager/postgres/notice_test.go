//go:build unit

package postgres

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreadableCAClient serves a tenant config whose CA file does not exist, so pgx fails parsing
// the DSN before anything is dialed.
func unreadableCAClient(t *testing.T) *client.Client {
	t.Helper()

	caFile := filepath.Join(t.TempDir(), "missing-ca.pem")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id": "tenant-a", "databases": {"onboarding": {"postgresql": {
			"host": "127.0.0.1", "port": 5432, "database": "d", "username": "u", "password": "p",
			"sslmode": "verify-full", "sslrootcert": %q}}}}`, caFile)
	}))
	t.Cleanup(server.Close)

	return mustNewTestClient(t, server.URL)
}

// Only the notice connector reports a DSN parse failure as "failed to parse database dsn", so the
// unreadable CA file tells which path each site took.
func TestManagerHandsOnNoticeToEveryPrimaryItDials(t *testing.T) {
	t.Parallel()

	sites := map[string]func(t *testing.T, opts ...Option) string{
		"first connection": func(t *testing.T, opts ...Option) string {
			manager := NewManager(unreadableCAClient(t), "ledger", append(opts, WithModule("onboarding"))...)

			_, err := manager.GetConnection(context.Background(), "tenant-a")
			require.Error(t, err)

			return err.Error()
		},
		"config-change reconnect": func(t *testing.T, opts ...Option) string {
			logger := testutil.NewCapturingLogger()
			manager := NewManager(unreadableCAClient(t), "ledger",
				append(opts, WithModule("onboarding"), WithLogger(logger))...)

			var cached dbresolver.DB = &pingableDB{}

			manager.connections["tenant-a"] = &PostgresConnection{
				ConnectionStringPrimary: "postgres://u:p@127.0.0.1:5432/old?sslmode=disable",
				ConnectionDB:            &cached,
			}

			manager.revalidatePoolSettings("tenant-a")
			require.True(t, logger.ContainsSubstring("keeping old connection"))

			return fmt.Sprint(logger.GetMessages())
		},
	}

	for name, dial := range sites {
		t.Run(name+" with callback", func(t *testing.T) {
			t.Parallel()

			got := dial(t, WithOnNotice(func(*pgconn.PgConn, *pgconn.Notice) {}))
			assert.Contains(t, got, "failed to parse database dsn")
		})

		t.Run(name+" without callback", func(t *testing.T) {
			t.Parallel()

			got := dial(t)
			assert.NotContains(t, got, "failed to parse database dsn")
		})
	}
}
