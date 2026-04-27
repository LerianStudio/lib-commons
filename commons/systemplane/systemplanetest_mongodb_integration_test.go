//go:build integration

package systemplane_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

const publicContractMongoSettle = 2 * time.Second

var publicContractMongoSeq atomic.Int64

func TestIntegration_SystemplanetestRun_MongoDBClient_ChangeStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := startPublicContractMongo(t)
	systemplanetest.Run(t, func(t *testing.T) *systemplane.Client {
		t.Helper()

		seq := publicContractMongoSeq.Add(1)
		c, err := systemplane.NewMongoDB(client, fmt.Sprintf("sp_public_contract_cs_%d", seq),
			systemplane.WithTenantSchemaEnabled(),
		)
		require.NoError(t, err)

		return c
	}, systemplanetest.WithEventSettle(publicContractMongoSettle))
}

func TestIntegration_SystemplanetestRun_MongoDBClient_Polling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := startPublicContractMongo(t)
	systemplanetest.Run(t, func(t *testing.T) *systemplane.Client {
		t.Helper()

		seq := publicContractMongoSeq.Add(1)
		c, err := systemplane.NewMongoDB(client, fmt.Sprintf("sp_public_contract_poll_%d", seq),
			systemplane.WithPollInterval(100*time.Millisecond),
			systemplane.WithTenantSchemaEnabled(),
		)
		require.NoError(t, err)

		return c
	},
		systemplanetest.WithEventSettle(publicContractMongoSettle),
		systemplanetest.SkipSubtest("TenantOnChangeReceivesDelete"),
	)
}

func startPublicContractMongo(t *testing.T) *mongo.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcmongo.Run(ctx,
		"mongo:7",
		tcmongo.WithReplicaSet("rs0"),
	)
	require.NoError(t, err, "failed to start MongoDB container")

	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err, "failed to get MongoDB connection string")

	if err := waitForPublicContractMongo(uri, 90*time.Second); err != nil {
		t.Skipf("skipping mongo integration: %v", err)
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetDirect(true).SetServerSelectionTimeout(30 * time.Second))
	require.NoError(t, err, "failed to create mongo client")

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pingCancel()

	if pingErr := client.Ping(pingCtx, nil); pingErr != nil {
		_ = client.Disconnect(context.Background())
		t.Skipf("skipping mongo integration: replica set ping failed: %v", pingErr)
	}

	t.Cleanup(func() {
		require.NoError(t, client.Disconnect(context.Background()))
	})

	return client
}

func waitForPublicContractMongo(uri string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client, err := mongo.Connect(options.Client().ApplyURI(uri).SetDirect(true))
		if err == nil {
			err = client.Ping(ctx, nil)
			_ = client.Disconnect(context.Background())
		}

		cancel()

		if err == nil {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("mongo replica set did not become ready within %s", timeout)
}
