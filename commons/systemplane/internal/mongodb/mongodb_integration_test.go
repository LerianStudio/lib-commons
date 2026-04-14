//go:build integration

package mongodb

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// waitForReplicaSet polls the MongoDB instance using a direct connection
// until it responds or the timeout elapses. This bypasses the replica set
// topology negotiation that can fail when Docker internal IPs are
// unreachable from the test host.
func waitForReplicaSet(uri string, timeout time.Duration) error {
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

// setupMongoDB starts a MongoDB container with a replica set and returns a
// connected client plus the connection URI. The container and client are
// cleaned up when the test finishes.
func setupMongoDB(t *testing.T) (*mongo.Client, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcmongo.Run(ctx,
		"mongo:7",
		tcmongo.WithReplicaSet("rs0"),
	)
	require.NoError(t, err, "failed to start MongoDB container")

	t.Cleanup(func() {
		if termErr := container.Terminate(context.Background()); termErr != nil {
			t.Errorf("failed to terminate MongoDB container: %v", termErr)
		}
	})

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err, "failed to get MongoDB connection string")

	// Wait for the replica set to become ready by polling with direct
	// connections. Docker Desktop on macOS may expose the container on
	// localhost:<mapped-port> but the replica set advertises its internal IP,
	// making non-direct connections unreachable.
	if err := waitForReplicaSet(uri, 90*time.Second); err != nil {
		t.Skipf("skipping mongo integration: %v", err)
	}

	clientOpts := options.Client().
		ApplyURI(uri).
		SetDirect(true).
		SetServerSelectionTimeout(30 * time.Second)

	client, err := mongo.Connect(clientOpts)
	require.NoError(t, err, "failed to create mongo client")

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pingCancel()

	if pingErr := client.Ping(pingCtx, nil); pingErr != nil {
		_ = client.Disconnect(context.Background())
		t.Skipf("skipping mongo integration: replica set ping failed: %v", pingErr)
	}

	t.Cleanup(func() {
		_ = client.Disconnect(context.Background())
	})

	return client, uri
}

// newTestStore creates a Store with a unique database for test isolation.
func newTestStore(t *testing.T, client *mongo.Client, pollInterval time.Duration) *Store {
	t.Helper()

	dbName := fmt.Sprintf("test_%d", time.Now().UnixNano())

	cfg := Config{
		Client:       client,
		Database:     dbName,
		PollInterval: pollInterval,
	}

	s, err := New(cfg)
	require.NoError(t, err, "failed to create test store")

	t.Cleanup(func() {
		_ = s.Close()
	})

	return s
}

// ---------------------------------------------------------------------------
// Contract test suites (Phase 4's systemplanetest.Run)
// ---------------------------------------------------------------------------

func TestIntegration_ContractSuite_ChangeStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	systemplanetest.Run(t, func(t *testing.T) store.Store {
		return newTestStore(t, client, 0) // PollInterval=0 -> change streams
	})
}

func TestIntegration_ContractSuite_Polling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	systemplanetest.Run(t, func(t *testing.T) store.Store {
		return newTestStore(t, client, 100*time.Millisecond) // polling mode, fast poll for tests
	})
}

// ---------------------------------------------------------------------------
// MongoDB-specific tests
// ---------------------------------------------------------------------------

func TestIntegration_ChangeStreamEmitsOnInsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)
	s := newTestStore(t, client, 0) // change-stream mode

	signalCh := make(chan store.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	go func() {
		errCh <- s.Subscribe(ctx, func(evt store.Event) {
			select {
			case signalCh <- evt:
			default:
			}
		})
	}()

	// Give the change stream time to open.
	time.Sleep(2 * time.Second)

	// Insert a new entry.
	err := s.Set(context.Background(), store.Entry{
		Namespace: "app",
		Key:       "max_retries",
		Value:     []byte(`{"v": 5}`),
		UpdatedBy: "test",
	})
	require.NoError(t, err)

	// Wait for the signal.
	select {
	case evt := <-signalCh:
		assert.Equal(t, "app", evt.Namespace)
		assert.Equal(t, "max_retries", evt.Key)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for change stream event on insert")
	}

	cancel()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
}

func TestIntegration_ChangeStreamEmitsOnUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)
	s := newTestStore(t, client, 0)

	var mu sync.Mutex

	var received []store.Event

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	go func() {
		errCh <- s.Subscribe(ctx, func(evt store.Event) {
			mu.Lock()
			received = append(received, evt)
			mu.Unlock()
		})
	}()

	time.Sleep(2 * time.Second)

	// First write (insert).
	err := s.Set(context.Background(), store.Entry{
		Namespace: "app",
		Key:       "timeout_ms",
		Value:     []byte(`{"v": 1000}`),
		UpdatedBy: "test",
	})
	require.NoError(t, err)

	// Second write (update same key).
	err = s.Set(context.Background(), store.Entry{
		Namespace: "app",
		Key:       "timeout_ms",
		Value:     []byte(`{"v": 2000}`),
		UpdatedBy: "test",
	})
	require.NoError(t, err)

	// Wait for both events.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(received) >= 2
	}, 15*time.Second, 200*time.Millisecond, "expected at least 2 change stream events")

	cancel()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}

	mu.Lock()
	defer mu.Unlock()

	for _, evt := range received {
		assert.Equal(t, "app", evt.Namespace)
		assert.Equal(t, "timeout_ms", evt.Key)
	}
}

func TestIntegration_PollingMatchesChangeStreamSemantics(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	// Both stores share the same database so they see the same collection.
	dbName := fmt.Sprintf("test_dual_%d", time.Now().UnixNano())

	// Change-stream store.
	csCfg := Config{Client: client, Database: dbName}

	csStore, err := New(csCfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = csStore.Close() })

	// Polling store.
	pollCfg := Config{Client: client, Database: dbName, PollInterval: 100 * time.Millisecond}

	pollStore, err := New(pollCfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = pollStore.Close() })

	var csMu, pollMu sync.Mutex

	var csEvents, pollEvents []store.Event

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	csErrCh := make(chan error, 1)
	pollErrCh := make(chan error, 1)

	go func() {
		csErrCh <- csStore.Subscribe(ctx, func(evt store.Event) {
			csMu.Lock()
			csEvents = append(csEvents, evt)
			csMu.Unlock()
		})
	}()

	go func() {
		pollErrCh <- pollStore.Subscribe(ctx, func(evt store.Event) {
			pollMu.Lock()
			pollEvents = append(pollEvents, evt)
			pollMu.Unlock()
		})
	}()

	time.Sleep(2 * time.Second)

	// Write entries via the change-stream store (both share the collection).
	entries := []store.Entry{
		{Namespace: "svc", Key: "pool_size", Value: []byte(`{"v": 10}`), UpdatedBy: "test"},
		{Namespace: "svc", Key: "retries", Value: []byte(`{"v": 3}`), UpdatedBy: "test"},
	}

	for _, e := range entries {
		require.NoError(t, csStore.Set(context.Background(), e))
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for both subscribers to receive the events.
	require.Eventually(t, func() bool {
		csMu.Lock()
		defer csMu.Unlock()

		return len(csEvents) >= 2
	}, 15*time.Second, 200*time.Millisecond, "change-stream should see 2 events")

	require.Eventually(t, func() bool {
		pollMu.Lock()
		defer pollMu.Unlock()

		return len(pollEvents) >= 2
	}, 15*time.Second, 200*time.Millisecond, "poll should see 2 events")

	cancel()

	csKeys := eventKeys(csEvents)
	pollKeys := eventKeys(pollEvents)

	assert.ElementsMatch(t, csKeys, pollKeys,
		"change-stream and poll mode should observe the same namespace+key pairs")
}

func TestIntegration_UniqueIndexPreventsRaceDuplicates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)
	s := newTestStore(t, client, 0)

	const concurrency = 10

	var wg sync.WaitGroup

	errCh := make(chan error, concurrency)

	for i := range concurrency {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			e := store.Entry{
				Namespace: "race",
				Key:       "shared_key",
				Value:     []byte(fmt.Sprintf(`{"v": %d}`, n)),
				UpdatedBy: fmt.Sprintf("writer-%d", n),
			}

			errCh <- s.Set(context.Background(), e)
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		assert.NoError(t, err, "concurrent Set should succeed")
	}

	// Verify exactly one document exists for this (namespace, key).
	entry, found, err := s.Get(context.Background(), "race", "shared_key")
	require.NoError(t, err)
	require.True(t, found, "entry should exist")
	assert.Equal(t, "race", entry.Namespace)
	assert.Equal(t, "shared_key", entry.Key)

	entries, err := s.List(context.Background())
	require.NoError(t, err)

	count := 0

	for _, e := range entries {
		if e.Namespace == "race" && e.Key == "shared_key" {
			count++
		}
	}

	assert.Equal(t, 1, count, "unique index should prevent duplicate documents")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func eventKeys(events []store.Event) []string {
	keys := make([]string, len(events))
	for i, e := range events {
		keys[i] = e.Namespace + "/" + e.Key
	}

	return keys
}
