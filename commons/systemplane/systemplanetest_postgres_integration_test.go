//go:build integration

package systemplane_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver registration
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

var publicContractTableSeq atomic.Int64

func TestIntegration_SystemplanetestRun_PostgresClient(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn := startPublicContractPostgres(ctx, t)

	systemplanetest.Run(t, func(t *testing.T) *systemplane.Client {
		t.Helper()

		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)

		t.Cleanup(func() {
			require.NoError(t, db.Close())
		})

		seq := publicContractTableSeq.Add(1)
		client, err := systemplane.NewPostgres(db, dsn,
			systemplane.WithTable(fmt.Sprintf("sp_public_contract_%d", seq)),
			systemplane.WithListenChannel(fmt.Sprintf("sp_public_contract_ch_%d", seq)),
			systemplane.WithTenantSchemaEnabled(),
		)
		require.NoError(t, err)

		return client
	})
}

func startPublicContractPostgres(ctx context.Context, t *testing.T) string {
	t.Helper()

	container, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("systemplanetest"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
				wait.ForListeningPort("5432/tcp"),
			).WithStartupTimeout(90*time.Second),
		),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	return dsn
}
