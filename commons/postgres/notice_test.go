//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ignoreNotice(*pgconn.PgConn, *pgconn.Notice) {}

// Only the primary carries the handler, and its pool still gets the configured tuning and telemetry.
func TestOnNoticeOpensOnlyThePrimaryFromAConnector(t *testing.T) {
	tests := []struct {
		name       string
		onNotice   pgconn.NoticeHandler
		replicaDSN string
		wantOpened []string
	}{
		{name: "nil handler keeps sql.Open for both pools", replicaDSN: replicaTestDSN,
			wantOpened: []string{primaryTestDSN, replicaTestDSN}},
		{name: "callback leaves the replica on sql.Open", onNotice: ignoreNotice, replicaDSN: replicaTestDSN,
			wantOpened: []string{replicaTestDSN}},
		{name: "callback on a single pool", onNotice: ignoreNotice},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := withGlobalMeterProvider(t)
			probe := installPoolOpenProbe(t)

			client, err := NewWithNotice(Config{
				PrimaryDSN: primaryTestDSN, ReplicaDSN: tt.replicaDSN, MaxOpenConnections: 7,
			}, tt.onNotice)
			require.NoError(t, err)
			require.NoError(t, client.Connect(context.Background()))
			t.Cleanup(func() { _ = client.Close() })

			assert.Equal(t, tt.wantOpened, probe.opened)

			primary, err := client.Primary()
			require.NoError(t, err)
			assert.Same(t, probe.resolverPrimary, primary, "the resolver must be built over the kept primary")
			assert.Equal(t, 7, primary.Stats().MaxOpenConnections, "pool tuning must land on the kept primary")
			assert.Contains(t, poolRoles(t, reader), "primary", "the kept primary must report pool telemetry")
		})
	}
}

// The callback path re-reads the DSN for every physical connection, as sql.Open does, and a failed
// parse must not expose pgx's ParseConfigError, which keeps the raw DSN.
func TestOnNoticeParseErrorsAreSanitized(t *testing.T) {
	withPatchedDependencies(t, sql.Open,
		func(*sql.DB, *sql.DB, obs.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		nil,
	)

	// A socket directory with no server: a dial that slips through fails locally.
	dsn := "host=" + t.TempDir() + " user=u password=secret-password dbname=d sslmode=require"

	client, err := NewWithNotice(Config{PrimaryDSN: dsn}, ignoreNotice)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Connect(context.Background()))

	primary, err := client.Primary()
	require.NoError(t, err)

	t.Setenv("PGPORT", "not-a-port")

	err = primary.PingContext(context.Background())

	var sanitized *SanitizedError
	require.ErrorAs(t, err, &sanitized)
	assert.Contains(t, err.Error(), "invalid port")
	assert.NotContains(t, err.Error(), "secret-password")

	var parseErr *pgconn.ParseConfigError
	assert.NotErrorAs(t, err, &parseErr, "the raw DSN must not stay reachable through the chain")
}

// NewWithNotice is New plus the handler: New's validation, defaults and TLS refusal still apply.
func TestNewWithNoticeKeepsNewContract(t *testing.T) {
	_, err := NewWithNotice(Config{}, ignoreNotice)
	require.ErrorIs(t, err, ErrInvalidConfig)

	client, err := NewWithNotice(validConfig(), ignoreNotice)
	require.NoError(t, err)
	assert.Equal(t, defaultMaxOpenConns, client.cfg.MaxOpenConnections)

	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	client, err = NewWithNotice(validConfig(), ignoreNotice)
	require.ErrorContains(t, err, "TLS required")
	assert.Nil(t, client)
}
