//go:build integration

package mongo

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
)

const (
	testDatabase   = "integration_test_db"
	testCollection = "integration_test_col"
)

// setupMongoContainer starts a disposable MongoDB 7 container and returns
// the connection string plus a cleanup function. The container is terminated
// when cleanup runs (typically via t.Cleanup).
func setupMongoContainer(t *testing.T) (string, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcmongo.Run(ctx,
		"mongo:7",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Waiting for connections").
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	endpoint, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	return endpoint, func() {
		require.NoError(t, container.Terminate(ctx))
	}
}

// newIntegrationClient creates a Client backed by the testcontainer at uri.
func newIntegrationClient(t *testing.T, uri string) *Client {
	t.Helper()

	ctx := context.Background()

	client, err := NewClient(ctx, Config{
		URI:      uri,
		Database: testDatabase,
		Logger:   log.NewNop(),
	})
	require.NoError(t, err)

	return client
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestIntegration_Mongo_ConnectAndPing(t *testing.T) {
	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client := newIntegrationClient(t, uri)
	defer func() { require.NoError(t, client.Close(ctx)) }()

	// Ping must succeed on a healthy container.
	err := client.Ping(ctx)
	require.NoError(t, err)
}

func TestIntegration_Mongo_DatabaseAccess(t *testing.T) {
	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client := newIntegrationClient(t, uri)
	defer func() { require.NoError(t, client.Close(ctx)) }()

	// Obtain a database handle and verify the name.
	db, err := client.Database(ctx)
	require.NoError(t, err)
	assert.Equal(t, testDatabase, db.Name())

	// Insert a document into a fresh collection.
	type testDoc struct {
		Name  string `bson:"name"`
		Value int    `bson:"value"`
	}

	col := db.Collection(testCollection)
	insertDoc := testDoc{Name: "integration", Value: 42}

	_, err = col.InsertOne(ctx, insertDoc)
	require.NoError(t, err)

	// Read it back and verify contents.
	var result testDoc

	err = col.FindOne(ctx, bson.M{"name": "integration"}).Decode(&result)
	require.NoError(t, err)
	assert.Equal(t, "integration", result.Name)
	assert.Equal(t, 42, result.Value)
}

func TestIntegration_Mongo_EnsureIndexes(t *testing.T) {
	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client := newIntegrationClient(t, uri)
	defer func() { require.NoError(t, client.Close(ctx)) }()

	// Force-create the collection so index listing returns results.
	db, err := client.Database(ctx)
	require.NoError(t, err)

	err = db.CreateCollection(ctx, testCollection)
	require.NoError(t, err)

	// Ensure an index on the "email" field.
	err = client.EnsureIndexes(ctx, testCollection,
		mongodriver.IndexModel{
			Keys: bson.D{{Key: "email", Value: 1}},
		},
	)
	require.NoError(t, err)

	// List indexes and verify ours is present.
	driverClient, err := client.Client(ctx)
	require.NoError(t, err)

	cursor, err := driverClient.Database(testDatabase).
		Collection(testCollection).
		Indexes().
		List(ctx)
	require.NoError(t, err)

	var indexes []bson.M

	err = cursor.All(ctx, &indexes)
	require.NoError(t, err)

	// MongoDB always creates a default _id index, so we expect at least 2.
	require.GreaterOrEqual(t, len(indexes), 2, "expected at least the _id index + email index")

	// Find the email index by inspecting the "key" document.
	found := false

	for _, idx := range indexes {
		keyDoc, ok := idx["key"].(bson.M)
		if !ok {
			continue
		}

		if _, hasEmail := keyDoc["email"]; hasEmail {
			found = true

			break
		}
	}

	assert.True(t, found, "expected to find an index on 'email'; indexes: %+v", indexes)
}

func TestIntegration_Mongo_ResolveClient(t *testing.T) {
	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client := newIntegrationClient(t, uri)
	defer func() {
		// ResolveClient may have reconnected, so Close should still work.
		_ = client.Close(ctx)
	}()

	// Confirm the client is alive before closing.
	err := client.Ping(ctx)
	require.NoError(t, err)

	// Close the internal connection — subsequent Client() calls should fail.
	err = client.Close(ctx)
	require.NoError(t, err)

	_, err = client.Client(ctx)
	require.ErrorIs(t, err, ErrClientClosed, "Client() on a closed connection should return ErrClientClosed")

	// ResolveClient should transparently reconnect via lazy-connect.
	driverClient, err := client.ResolveClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, driverClient)

	// Verify the reconnected client is functional.
	err = client.Ping(ctx)
	require.NoError(t, err)
}

func TestIntegration_Mongo_ConcurrentPing(t *testing.T) {
	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client := newIntegrationClient(t, uri)
	defer func() { require.NoError(t, client.Close(ctx)) }()

	const goroutines = 10

	var wg sync.WaitGroup

	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			errs[idx] = client.Ping(ctx)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "goroutine %d returned an error", i)
	}
}
