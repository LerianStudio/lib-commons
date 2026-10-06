//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const posturePassword = "s3cr3t-posture-pw"

// postureModes is every sslmode spelling the posture table exercises: the six
// libpq modes, an absent setting, and two values pgx refuses as invalid.
var postureModes = []string{"", "disable", "allow", "prefer", "require", "verify-ca", "verify-full", "bogus", "VERIFY-FULL"}

func modeRank(mode string) int {
	switch mode {
	case "disable":
		return 0
	case "allow":
		return 1
	case "prefer":
		return 2
	case "require":
		return 3
	case "verify-ca":
		return 4
	case "verify-full":
		return 5
	default:
		return -1
	}
}

func kvDSN(mode string) string {
	dsn := "host=db.internal port=5432 user=app password='" + posturePassword + "' dbname=ledger"
	if mode != "" {
		dsn += " sslmode=" + mode
	}

	return dsn
}

func urlDSN(mode string) string {
	dsn := "postgres://app:" + url.QueryEscape(posturePassword) + "@db.internal:5432/ledger"
	if mode != "" {
		dsn += "?sslmode=" + mode
	}

	return dsn
}

// TestCheckSSLMode_PostureTable runs every posture and floor against every
// sslmode, in both DSN forms: a mode passes exactly when it is a known libpq
// mode at least as strong as the floor.
func TestCheckSSLMode_PostureTable(t *testing.T) {
	t.Parallel()

	postures := []struct {
		posture TLSPosture
		floor   SSLMode
		want    SSLMode
	}{
		{TLSPostureHardened, "", SSLModeVerifyFull},
		{TLSPostureHardened, SSLModeRequire, SSLModeRequire},
		{TLSPostureHardened, SSLModeVerifyCA, SSLModeVerifyCA},
		{TLSPostureHardened, SSLModeVerifyFull, SSLModeVerifyFull},
		{TLSPostureSaaS, "", SSLModeVerifyFull},
		{TLSPostureSaaS, SSLModeVerifyFull, SSLModeVerifyFull},
	}

	forms := map[string]func(string) string{"keyword/value": kvDSN, "url": urlDSN}

	for _, p := range postures {
		for formName, build := range forms {
			for _, mode := range postureModes {
				name := fmt.Sprintf("%s/floor=%q/%s/mode=%q", p.posture, p.floor, formName, mode)
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					err := CheckSSLMode("primary", build(mode), p.posture, p.floor)

					if modeRank(mode) >= modeRank(string(p.want)) {
						require.NoError(t, err)
						return
					}

					require.Error(t, err)
					assert.ErrorIs(t, err, ErrWeakSSLMode)
					assert.NotContains(t, err.Error(), posturePassword)

					var weak *WeakSSLModeError
					require.ErrorAs(t, err, &weak)
					assert.Equal(t, "primary", weak.Label)
					assert.Equal(t, "sslmode", weak.Setting)
					assert.Equal(t, mode, weak.Got)
					assert.Equal(t, p.want, weak.Min)
					assert.Equal(t, p.posture, weak.Posture)
				})
			}
		}
	}
}

// TestCheckSSLMode_ReadsTheDSNLikePgx pins the parsing rules the posture
// relies on: the sslmode it judges is the one pgx would use, never a lookalike.
func TestCheckSSLMode_ReadsTheDSNLikePgx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dsn     string
		wantGot string
		pass    bool
	}{
		{name: "kv quoted value", dsn: "host=h sslmode='verify-full' password='p q'", pass: true},
		{name: "kv spaces around equals", dsn: "host=h sslmode = verify-full", pass: true},
		{name: "kv duplicate, last wins (weak last)", dsn: "host=h sslmode=verify-full sslmode=require", wantGot: "require"},
		{name: "kv duplicate, last wins (strong last)", dsn: "host=h sslmode=require sslmode=verify-full", pass: true},
		{name: "kv mode hidden inside password", dsn: "host=h password='x sslmode=verify-full'", wantGot: ""},
		{name: "kv unterminated quote is malformed", dsn: "host=h sslmode=verify-full password='open", wantGot: ""},
		{name: "kv trailing backslash is malformed", dsn: `host=h sslmode=verify-full password=x\`, wantGot: ""},
		{name: "kv key without value separator", dsn: "host=h sslmode=verify-full garbage", wantGot: ""},
		{name: "url duplicate, first wins as in pgx", dsn: "postgres://u:p@h/db?sslmode=verify-full&sslmode=disable", pass: true},
		{name: "url duplicate, first wins (weak first)", dsn: "postgres://u:p@h/db?sslmode=disable&sslmode=verify-full", wantGot: "disable"},
		{name: "postgresql scheme", dsn: "postgresql://u:p@h/db?sslmode=verify-full", pass: true},
		{name: "url malformed escape", dsn: "postgres://u:pa%zz@h/db?sslmode=verify-full", wantGot: ""},
		{name: "url quoted value is not a mode", dsn: "postgres://u:p@h/db?sslmode='verify-full'", wantGot: "'verify-full'"},
		{name: "uppercase scheme is not a url to pgx", dsn: "POSTGRES://u:p@h/db?sslmode=verify-full", wantGot: ""},
		{name: "leading space is not a url to pgx", dsn: " postgres://u:p@h/db?sslmode=verify-full", wantGot: ""},
		{name: "empty dsn", dsn: "", wantGot: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckSSLMode("primary", tt.dsn, TLSPostureHardened, "")
			if tt.pass {
				require.NoError(t, err)
				return
			}

			var weak *WeakSSLModeError
			require.ErrorAs(t, err, &weak)
			assert.Equal(t, tt.wantGot, weak.Got)
		})
	}
}

func TestCheckSSLMode_DefaultPostureIsTodaysRule(t *testing.T) {
	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	require.NoError(t, CheckSSLMode("primary", kvDSN("require"), TLSPostureDefault, ""))

	err := CheckSSLMode("primary", kvDSN("disable"), TLSPostureDefault, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), commons.EnvAllowInsecureTLS)

	t.Setenv(commons.EnvAllowInsecureTLS, "true")
	require.NoError(t, CheckSSLMode("primary", kvDSN("disable"), TLSPostureDefault, ""))
}

func TestCheckSSLModeValue(t *testing.T) {
	t.Parallel()

	require.NoError(t, CheckSSLModeValue("primary", "verify-full", TLSPostureSaaS, ""))

	err := CheckSSLModeValue("replica", "", TLSPostureHardened, SSLModeVerifyCA)

	var weak *WeakSSLModeError
	require.ErrorAs(t, err, &weak)
	assert.Equal(t, "replica", weak.Label)
	assert.Empty(t, weak.Got)
	assert.Equal(t, SSLModeVerifyCA, weak.Min)
	assert.Contains(t, err.Error(), "sslmode")
	assert.Contains(t, err.Error(), "not set")
}

func TestValidateTLSPosture(t *testing.T) {
	t.Parallel()

	valid := []struct {
		posture TLSPosture
		floor   SSLMode
	}{
		{TLSPostureDefault, ""},
		{TLSPostureHardened, ""},
		{TLSPostureHardened, SSLModeRequire},
		{TLSPostureHardened, SSLModeVerifyCA},
		{TLSPostureHardened, SSLModeVerifyFull},
		{TLSPostureSaaS, ""},
		{TLSPostureSaaS, SSLModeVerifyFull},
	}
	for _, v := range valid {
		assert.NoError(t, ValidateTLSPosture(v.posture, v.floor), "%s/%q", v.posture, v.floor)
	}

	invalid := []struct {
		posture TLSPosture
		floor   SSLMode
	}{
		{TLSPostureDefault, SSLModeVerifyFull},
		{TLSPostureSaaS, SSLModeRequire},
		{TLSPostureSaaS, SSLModeVerifyCA},
		{TLSPostureHardened, "prefer"},
		{TLSPostureHardened, "disable"},
		{TLSPostureHardened, "VERIFY-FULL"},
		{TLSPosture(9), ""},
	}
	for _, v := range invalid {
		err := ValidateTLSPosture(v.posture, v.floor)
		assert.ErrorIs(t, err, ErrInvalidConfig, "%s/%q", v.posture, v.floor)
	}

	// CheckSSLMode refuses an invalid posture as a config error, never as a
	// verdict on the DSN.
	err := CheckSSLMode("primary", kvDSN("verify-full"), TLSPostureSaaS, SSLModeRequire)
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.NotErrorIs(t, err, ErrWeakSSLMode)
}

func TestTLSPostureString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "default", TLSPostureDefault.String())
	assert.Equal(t, "hardened", TLSPostureHardened.String())
	assert.Equal(t, "saas", TLSPostureSaaS.String())
	assert.Equal(t, "TLSPosture(9)", TLSPosture(9).String())
}

func TestWeakSSLModeError_NilSafe(t *testing.T) {
	t.Parallel()

	var e *WeakSSLModeError
	assert.NotPanics(t, func() { _ = e.Error() })
	assert.ErrorIs(t, e.Unwrap(), ErrWeakSSLMode)
}

// TestNew_HardenedPostureIgnoresInsecureBypass: TestMain sets
// ALLOW_INSECURE_TLS=true for the whole binary; a posture's floor still holds.
func TestNew_HardenedPostureIgnoresInsecureBypass(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	_, err := New(Config{PrimaryDSN: kvDSN("require"), TLSPosture: TLSPostureHardened})
	require.ErrorIs(t, err, ErrWeakSSLMode)
	assert.NotContains(t, err.Error(), posturePassword)

	_, err = New(Config{PrimaryDSN: kvDSN("disable"), TLSPosture: TLSPostureHardened, MinSSLMode: SSLModeRequire})
	require.ErrorIs(t, err, ErrWeakSSLMode)

	client, err := New(Config{PrimaryDSN: kvDSN("require"), TLSPosture: TLSPostureHardened, MinSSLMode: SSLModeRequire})
	require.NoError(t, err)
	require.NotNil(t, client)

	client, err = New(Config{PrimaryDSN: urlDSN("verify-full"), TLSPosture: TLSPostureSaaS})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNew_PostureChecksTheReplica(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		PrimaryDSN: urlDSN("verify-full"),
		ReplicaDSN: "postgres://app:" + posturePassword + "@replica.internal:5432/ledger?sslmode=require",
		TLSPosture: TLSPostureSaaS,
	})

	var weak *WeakSSLModeError
	require.ErrorAs(t, err, &weak)
	assert.Equal(t, "replica", weak.Label)
	assert.Equal(t, "require", weak.Got)
	assert.NotContains(t, err.Error(), posturePassword)
}

func TestNew_InvalidPostureIsConfigError(t *testing.T) {
	t.Parallel()

	_, err := New(Config{PrimaryDSN: urlDSN("verify-full"), TLSPosture: TLSPostureSaaS, MinSSLMode: SSLModeRequire})
	require.ErrorIs(t, err, ErrInvalidConfig)

	_, err = New(Config{PrimaryDSN: urlDSN("verify-full"), MinSSLMode: SSLModeVerifyFull})
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewFromPools_RefusesPosture(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("pgx", urlDSN("verify-full"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = NewFromPools(db, nil, Config{TLSPosture: TLSPostureHardened})
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "posture")

	_, err = NewFromPools(db, nil, Config{MinSSLMode: SSLModeVerifyFull})
	require.ErrorIs(t, err, ErrInvalidConfig)

	client, err := NewFromPools(db, nil, Config{})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewMigrator_InvalidPostureIsConfigError(t *testing.T) {
	t.Parallel()

	_, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     urlDSN("verify-full"),
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
		TLSPosture:     TLSPostureSaaS,
		MinSSLMode:     SSLModeVerifyCA,
	})
	require.ErrorIs(t, err, ErrInvalidConfig)
}

// TestMigratorUp_PostureRefusesBeforeOpen: the posture is judged before the
// migration database is opened, and ALLOW_INSECURE_TLS does not lift it.
func TestMigratorUp_PostureRefusesBeforeOpen(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	openCalled := false
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) {
			openCalled = true
			return nil, errors.New("must not open")
		},
		func(*sql.DB, *sql.DB, obs.Logger) (dbresolver.DB, error) { return nil, nil },
		func(context.Context, *sql.DB, migrationRun) error { return nil },
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     urlDSN("require"),
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
		TLSPosture:     TLSPostureHardened,
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.ErrorIs(t, err, ErrWeakSSLMode)
	assert.NotContains(t, err.Error(), posturePassword)
	assert.False(t, openCalled, "the posture must refuse before the database is opened")
}

func TestMigratorUp_PostureAdmitsStrongMode(t *testing.T) {
	openCalled := false
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) {
			openCalled = true
			return testDB(t), nil
		},
		func(*sql.DB, *sql.DB, obs.Logger) (dbresolver.DB, error) { return nil, nil },
		func(context.Context, *sql.DB, migrationRun) error { return nil },
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     urlDSN("verify-full"),
		DatabaseName:   "ledger",
		MigrationsPath: writeMigrationDir(t, "000001_init.up.sql"),
		TLSPosture:     TLSPostureSaaS,
	})
	require.NoError(t, err)

	require.NoError(t, m.Up(context.Background()))
	assert.True(t, openCalled)
}
