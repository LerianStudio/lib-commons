//go:build unit

package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNewTestMongoClient(t *testing.T) *client.Client {
	t.Helper()

	c, err := client.NewClient("http://localhost:8080", testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	return c
}

// TestApplyConnectionSettings_NoOp covers the no-op method.
func TestApplyConnectionSettings_NoOp(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	assert.NotPanics(t, func() {
		m.ApplyConnectionSettings("tenant-1", &core.TenantConfig{})
	})
}

// TestGetDatabaseForTenant_EmptyTenantID covers the empty tenant guard.
func TestGetDatabaseForTenant_EmptyTenantID(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	_, err := m.GetDatabaseForTenant(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

// TestGetDatabaseForTenant_ManagerClosed covers the closed manager guard.
func TestGetDatabaseForTenant_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")
	_ = m.Close(context.Background())

	_, err := m.GetDatabaseForTenant(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

// TestGetConnection_EmptyTenantID covers the empty tenant guard.
func TestGetConnection_EmptyTenantID(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	_, err := m.GetConnection(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

// TestGetConnection_ManagerClosed covers the closed manager path.
func TestGetConnection_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")
	_ = m.Close(context.Background())

	_, err := m.GetConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

// TestClose_Empty covers Close on empty manager.
func TestMongoClose_Empty(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	err := m.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, m.closed)
}

// TestCloseConnection_NonExistentTenant covers CloseConnection for missing tenant.
func TestMongoCloseConnection_NonExistentTenant(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	err := m.CloseConnection(context.Background(), "non-existent")
	require.NoError(t, err)
}

// TestValidateAndReturnRawURI covers valid and invalid URIs.
func TestValidateAndReturnRawURI_Valid(t *testing.T) {
	t.Parallel()

	uri, err := validateAndReturnRawURI("mongodb://localhost:27017/db", nil)
	require.NoError(t, err)
	assert.Equal(t, "mongodb://localhost:27017/db", uri)
}

func TestValidateAndReturnRawURI_ValidSRV(t *testing.T) {
	t.Parallel()

	uri, err := validateAndReturnRawURI("mongodb+srv://cluster.example.com/db", nil)
	require.NoError(t, err)
	assert.Contains(t, uri, "mongodb+srv://")
}

func TestValidateAndReturnRawURI_InvalidScheme(t *testing.T) {
	t.Parallel()

	_, err := validateAndReturnRawURI("http://localhost:27017", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mongo URI scheme")
}

func TestValidateAndReturnRawURI_InvalidURL(t *testing.T) {
	t.Parallel()

	// A truly malformed URL that Go's url.Parse fails on
	_, err := validateAndReturnRawURI("://bad-url", nil)
	// url.Parse may or may not error, but scheme check should fail
	if err == nil {
		// If no error from url.Parse, the scheme check should catch it
		assert.NotNil(t, err)
	}
}

// TestValidateMongoHostPort covers the validation.
func TestValidateMongoHostPort_Valid(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{Host: "localhost", Port: 27017}
	err := validateMongoHostPort(cfg)
	require.NoError(t, err)
}

func TestValidateMongoHostPort_MissingHost(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{Port: 27017}
	err := validateMongoHostPort(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host is required")
}

func TestValidateMongoHostPort_MissingPort(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{Host: "localhost"}
	err := validateMongoHostPort(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port is required")
}

// TestBuildMongoBaseURL covers the URL builder.
func TestBuildMongoBaseURL_Basic(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Host:     "localhost",
		Port:     27017,
		Database: "testdb",
	}

	u := buildMongoBaseURL(cfg)
	assert.Equal(t, "mongodb", u.Scheme)
	assert.Contains(t, u.Host, "localhost:27017")
	assert.Contains(t, u.Path, "testdb")
}

func TestBuildMongoBaseURL_WithCredentials(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Host:     "mongo.example.com",
		Port:     27017,
		Username: "user",
		Password: "pass",
		Database: "testdb",
	}

	u := buildMongoBaseURL(cfg)
	assert.NotNil(t, u.User)
}

func TestBuildMongoBaseURL_NoDatabase(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Host: "localhost",
		Port: 27017,
	}

	u := buildMongoBaseURL(cfg)
	assert.Equal(t, "/", u.Path)
}

// TestHasSeparateCertAndKey covers the helper.
func TestHasSeparateCertAndKey_True(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		TLS:         true,
		TLSCertFile: "/path/to/cert.pem",
		TLSKeyFile:  "/path/to/key.pem",
	}
	assert.True(t, hasSeparateCertAndKey(cfg))
}

func TestHasSeparateCertAndKey_SameFile(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		TLS:         true,
		TLSCertFile: "/path/to/combined.pem",
		TLSKeyFile:  "/path/to/combined.pem",
	}
	assert.False(t, hasSeparateCertAndKey(cfg))
}

func TestHasSeparateCertAndKey_NoTLS(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		TLS:         false,
		TLSCertFile: "/path/to/cert.pem",
		TLSKeyFile:  "/path/to/key.pem",
	}
	assert.False(t, hasSeparateCertAndKey(cfg))
}

// TestBuildMongoQueryParams covers query param building.
func TestBuildMongoQueryParams_AuthSource(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Database:   "testdb",
		Username:   "user",
		Password:   "pass",
		AuthSource: "myauth",
	}

	params := buildMongoQueryParams(cfg)
	assert.Equal(t, "myauth", params.Get("authSource"))
}

func TestBuildMongoQueryParams_DefaultAuthSource(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Database: "testdb",
		Username: "user",
		Password: "pass",
	}

	params := buildMongoQueryParams(cfg)
	assert.Equal(t, "admin", params.Get("authSource"))
}

func TestBuildMongoQueryParams_TLS(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		TLS: true,
	}

	params := buildMongoQueryParams(cfg)
	assert.Equal(t, "true", params.Get("tls"))
}

// TestNewManager covers the manager constructor.
func TestMongoNewManager(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	require.NotNil(t, m)
	assert.Equal(t, "ledger", m.service)
}

// TestMongoStats covers the Stats method.
func TestMongoStats_Empty(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	stats := m.Stats()
	assert.Equal(t, 0, stats.TotalConnections)
	assert.False(t, stats.Closed)
}

// TestBuildMongoURI_WithRawURI covers the raw URI path.
func TestBuildMongoURI_WithRawURI(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		URI: "mongodb://localhost:27017/db",
	}

	uri, err := buildMongoURI(cfg, nil)
	require.NoError(t, err)
	assert.Equal(t, "mongodb://localhost:27017/db", uri)
}

func TestBuildMongoURI_WithHostPort(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Host:     "localhost",
		Port:     27017,
		Database: "testdb",
	}

	uri, err := buildMongoURI(cfg, nil)
	require.NoError(t, err)
	assert.Contains(t, uri, "mongodb://")
	assert.Contains(t, uri, "localhost:27017")
}

func TestBuildMongoURI_MissingHost(t *testing.T) {
	t.Parallel()

	cfg := &core.MongoDBConfig{
		Port: 27017,
	}

	_, err := buildMongoURI(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host is required")
}

// TestIsMultiTenant covers the method.
func TestMongoIsMultiTenant_WithClient(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")
	assert.True(t, m.IsMultiTenant())
}

func TestMongoIsMultiTenant_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")
	assert.False(t, m.IsMultiTenant())
}

// TestCanStoreMongoConnection_TenantExists covers the exists path (returns true).
func TestCanStoreMongoConnection_TenantExists(t *testing.T) {
	t.Parallel()

	c := mustNewTestMongoClient(t)
	m := NewManager(c, "ledger")

	// Pre-populate connections map directly
	m.mu.Lock()
	m.connections["tenant-exists"] = &MongoConnection{}
	m.lastAccessed["tenant-exists"] = time.Now()
	m.mu.Unlock()

	result := m.canStoreMongoConnection(context.Background(), "tenant-exists", &MongoConnection{})
	assert.True(t, result)
}
