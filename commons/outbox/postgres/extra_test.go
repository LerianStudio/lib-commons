//go:build unit

package postgres

import (
	"testing"
	"time"

	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewRepository_NilConnection covers the nil connection guard.
func TestNewRepository_NilConnection(t *testing.T) {
	t.Parallel()

	_, err := NewRepository(nil, noopTenantResolver{}, noopTenantDiscoverer{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectionRequired)
}

// TestNewRepository_NilTenantResolver covers the nil resolver guard.
func TestNewRepository_NilTenantResolver(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	_, err := NewRepository(client, nil, noopTenantDiscoverer{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTenantResolverRequired)
}

// TestNewRepository_NilTenantDiscoverer covers the nil discoverer guard.
func TestNewRepository_NilTenantDiscoverer(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	_, err := NewRepository(client, noopTenantResolver{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTenantDiscovererRequired)
}

// TestNewRepository_InvalidTableName covers the table name validation path.
func TestNewRepository_InvalidTableName(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	_, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithTableName("123invalid"),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table name")
}

// TestNewRepository_InvalidTenantColumn covers the tenant column validation.
func TestNewRepository_InvalidTenantColumn(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	_, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithTenantColumn("bad-column-name"),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant column")
}

// TestNewRepository_WithRequiresTenant covers the tenant requirement interface.
func TestNewRepository_WithRequiresTenant(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, requireTenantResolver{}, noopTenantDiscoverer{})
	require.NoError(t, err)
	assert.True(t, repo.requireTenant)
}

// TestWithLogger_NilLogger covers the nil logger guard in the option.
func TestWithLogger_NilLogger(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithLogger(nil),
	)
	require.NoError(t, err)
	assert.NotNil(t, repo.logger)
}

// TestWithLogger_ValidLogger covers the logger option.
func TestWithLogger_ValidLogger(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	logger := &panicLogger{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithLogger(logger),
	)
	require.NoError(t, err)
	assert.Equal(t, logger, repo.logger)
}

// TestWithTransactionTimeout covers the option.
func TestWithTransactionTimeout_PositiveValue(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithTransactionTimeout(60*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, 60*time.Second, repo.transactionTimeout)
}

func TestWithTransactionTimeout_ZeroIgnored(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithTransactionTimeout(0),
	)
	require.NoError(t, err)
	// Should use default since 0 is ignored
	assert.Equal(t, defaultTransactionTimeout, repo.transactionTimeout)
}

// TestWithTableName covers the table name option.
func TestWithTableName_ValidName(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithTableName("my_outbox"),
	)
	require.NoError(t, err)
	assert.Equal(t, "my_outbox", repo.tableName)
}

// TestNewRepository_Success covers a valid repository creation.
func TestNewRepository_Success(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{})
	require.NoError(t, err)
	require.NotNil(t, repo)
	assert.Equal(t, "outbox_events", repo.tableName)
	assert.NotNil(t, repo.logger)
}

// TestWithTenantColumn covers the tenant column option.
func TestWithTenantColumn_ValidColumn(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithTenantColumn("tenant_id"),
	)
	require.NoError(t, err)
	assert.Equal(t, "tenant_id", repo.tenantColumn)
}

// TestNewRepository_WithNilTypedLogger covers the typed-nil logger detection.
func TestNewRepository_WithNilTypedLogger(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	var nilLogger *panicLogger
	repo, err := NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{},
		WithLogger(nilLogger),
	)
	require.NoError(t, err)
	// Typed-nil should be replaced with Nop logger
	assert.NotNil(t, repo.logger)
	_, isNop := repo.logger.(*libLog.NopLogger)
	assert.True(t, isNop)
}
