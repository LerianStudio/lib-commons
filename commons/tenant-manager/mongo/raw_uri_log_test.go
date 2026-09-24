//go:build unit

package mongo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

const rawURILogLine = "using raw mongodb URI from tenant configuration"

func countRawURILines(logger *testutil.LevelCapturingLogger, level int) int {
	n := 0

	for _, e := range logger.Entries() {
		if e.Level == level && strings.Contains(e.Message, rawURILogLine) {
			n++
		}
	}

	return n
}

// Config change detection runs on every tenant revalidation while the cached
// connection stays in place. With an unchanged raw-URI config it must neither
// reconnect nor log: a raw URI is the normal tenant config shape.
func TestDetectAndReconnectMongo_UnchangedRawURI_IsSilent(t *testing.T) {
	t.Parallel()

	const (
		tenantID = "tenant-raw"
		rawURI   = "mongodb://user:pass@mongo.internal:27017/testdb?authSource=admin"
	)

	capLogger := testutil.NewLevelCapturingLogger()
	m := NewManager(&client.Client{}, "ledger", WithLogger(capLogger))

	m.mu.Lock()
	m.connections[tenantID] = &MongoConnection{ConnectionStringSource: rawURI, Database: "testdb", MaxPoolSize: DefaultMaxConnections}
	m.databaseNames[tenantID] = "testdb"
	m.mu.Unlock()

	cfg := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"ledger": {MongoDB: &core.MongoDBConfig{URI: rawURI, Database: "testdb"}},
		},
	}

	for range 3 {
		assert.False(t, m.detectAndReconnectMongo(context.Background(), tenantID, cfg),
			"an unchanged raw-URI config must not trigger a reconnection")
	}

	assert.Zero(t, countRawURILines(capLogger, obs.LevelWarn), "change detection must not warn about a raw URI")
	assert.Zero(t, countRawURILines(capLogger, obs.LevelDebug), "change detection must not log a raw URI at any level")
	assert.Empty(t, capLogger.Entries(), "change detection with an unchanged config must not log anything")
}

// Building a connection from a raw URI logs exactly one Debug line. The build
// is stopped at the TLS key-pair load (nonexistent files), which runs after the
// log call and before any network dial, so no live MongoDB is needed.
func TestBuildAndCacheNewConnection_RawURI_LogsOnceAtDebug(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "tenant-raw",
			"tenantSlug": "raw",
			"service": "ledger",
			"status": "active",
			"databases": {
				"ledger": {
					"mongodb": {
						"uri": "mongodb://mongo.invalid:27017/testdb",
						"database": "testdb",
						"tls": true,
						"tlsCertFile": "/nonexistent/client.crt",
						"tlsKeyFile": "/nonexistent/client.key"
					}
				}
			}
		}`))
	}))
	defer server.Close()

	tmClient, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = tmClient.Close() })

	capLogger := testutil.NewLevelCapturingLogger()
	m := NewManager(tmClient, "ledger", WithLogger(capLogger))

	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "test")
	defer span.End()

	_, err = m.buildAndCacheNewConnection(context.Background(), "tenant-raw", logcompat.New(capLogger), span)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to build TLS config")

	assert.Equal(t, 1, countRawURILines(capLogger, obs.LevelDebug), "one Debug line per connection build")
	assert.Zero(t, countRawURILines(capLogger, obs.LevelWarn), "a raw URI is not a warning condition")
}
