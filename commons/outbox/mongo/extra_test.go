//go:build unit

package mongo

import (
	"testing"

	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

// TestWithLogger covers the WithLogger option.
func TestWithLogger_NilLogger(t *testing.T) {
	t.Parallel()

	// WithLogger with nil should be a no-op
	repo := &Repository{}
	WithLogger(nil)(repo)
	assert.Nil(t, repo.logger)
}

// TestWithAllowEmptyTenant covers the WithAllowEmptyTenant option.
func TestWithAllowEmptyTenant(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	WithAllowEmptyTenant()(repo)
	assert.False(t, repo.requireTenant)
}

// TestWithRequireTenant covers the WithRequireTenant option.
func TestWithRequireTenant(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: false}
	WithRequireTenant()(repo)
	assert.True(t, repo.requireTenant)
}

// TestWithTenantDatabaseResolver_Nil covers typed-nil guard.
func TestWithTenantDatabaseResolver_Nil(t *testing.T) {
	t.Parallel()

	repo := &Repository{}
	// nil resolver should be a no-op
	WithTenantDatabaseResolver(nil)(repo)
	assert.Nil(t, repo.tenantDatabaseResolver)
}

// TestMergeFilters covers the mergeFilters helper.
func TestMergeFilters_EmptyExtras(t *testing.T) {
	t.Parallel()

	base := bson.M{"status": "pending"}
	result := mergeFilters(base)
	assert.Equal(t, "pending", result["status"])
}

func TestMergeFilters_WithExtras(t *testing.T) {
	t.Parallel()

	base := bson.M{"status": "pending"}
	extra := bson.M{"tenant_id": "t1"}
	result := mergeFilters(base, extra)
	assert.Equal(t, "pending", result["status"])
	assert.Equal(t, "t1", result["tenant_id"])
}

func TestMergeFilters_OverwritesBaseKey(t *testing.T) {
	t.Parallel()

	base := bson.M{"status": "pending"}
	extra := bson.M{"status": "failed"}
	result := mergeFilters(base, extra)
	assert.Equal(t, "failed", result["status"])
}

// TestBuildIndexes covers the buildIndexes helper.
func TestBuildIndexes_WithTenantField(t *testing.T) {
	t.Parallel()

	indexes := buildIndexes("tenant_id")
	// With tenant field, should have more indexes than without
	assert.Greater(t, len(indexes), 4)
}

func TestBuildIndexes_WithoutTenantField(t *testing.T) {
	t.Parallel()

	indexes := buildIndexes("")
	assert.Greater(t, len(indexes), 0)
	// Without tenant field, should have fewer indexes
	withTenant := buildIndexes("tenant_id")
	assert.Less(t, len(indexes), len(withTenant))
}

// TestNewRepository_NilClient covers the nil client guard.
func TestNewRepository_NilClient(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectionRequired)
	assert.Nil(t, repo)
}

// TestNewRepository_InvalidCollectionName covers the collection name validation.
func TestNewRepository_InvalidCollectionNameOpt(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(&libMongo.Client{}, WithCollectionName("bad-name"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidIdentifier)
	assert.Nil(t, repo)
}

// TestNewRepository_InvalidTenantField covers the tenant field validation.
func TestNewRepository_InvalidTenantFieldOpt(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(&libMongo.Client{}, WithTenantField("bad-field-name"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidIdentifier)
	assert.Nil(t, repo)
}

// TestNormalizeTenantIDs covers the normalizeTenantIDs function.
func TestNormalizeTenantIDs_Empty(t *testing.T) {
	t.Parallel()

	result, err := normalizeTenantIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestNormalizeTenantIDs_Valid(t *testing.T) {
	t.Parallel()

	input := []string{"tenant-b", "tenant-a", "tenant-a"} // duplicate + unsorted
	result, err := normalizeTenantIDs(input)
	require.NoError(t, err)
	assert.Equal(t, []string{"tenant-a", "tenant-b"}, result) // sorted, deduplicated
}

func TestNormalizeTenantIDs_EmptyStrings(t *testing.T) {
	t.Parallel()

	input := []string{"", "  ", "tenant-1"}
	result, err := normalizeTenantIDs(input)
	require.NoError(t, err)
	assert.Equal(t, []string{"tenant-1"}, result)
}

func TestNormalizeTenantIDs_InvalidID(t *testing.T) {
	t.Parallel()

	input := []string{"tenant-1", "_invalid"}
	_, err := normalizeTenantIDs(input)
	require.Error(t, err)
}
