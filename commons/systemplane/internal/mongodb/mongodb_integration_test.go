//go:build integration

package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// subscriberDrainTimeout bounds the wait for a Subscribe goroutine to return
// after its context has been cancelled. If a Subscribe implementation
// regresses and stops honoring cancellation, an unbounded <-errCh read would
// hang the package-level `go test` run until the global 10-minute test
// timeout fires. This value is intentionally generous relative to the
// streamOpenSettle/deliveryDeadline constants so CI hardware variance
// doesn't flake it. Keep it in one place so all bounded-drain sites agree.
const subscriberDrainTimeout = 5 * time.Second

// assertSubscribeTerminated waits up to subscriberDrainTimeout for a
// Subscribe goroutine to push its terminal error onto errCh and then
// validates the error is the expected cancel-path result.
//
// Both mongodb backends satisfy Subscribe's "blocks until ctx is cancelled"
// contract but disagree on the terminal error they report:
//   - change-stream path returns ctx.Err() (context.Canceled)
//   - polling path returns nil
//
// Either is a clean shutdown; anything else (including a timeout on errCh)
// indicates a regression where the Subscribe loop is no longer exiting on
// cancel. The label argument is baked into the failure message so the
// first failing channel (cs vs. poll) is obvious in CI output.
func assertSubscribeTerminated(t *testing.T, errCh <-chan error, label string) {
	t.Helper()

	select {
	case err := <-errCh:
		require.True(t, err == nil || errors.Is(err, context.Canceled),
			"%s Subscribe should return nil or context.Canceled after cancel(); got %v", label, err)
	case <-time.After(subscriberDrainTimeout):
		t.Fatalf("%s Subscribe did not return within %s after cancel() — possible regression in cancellation handling", label, subscriberDrainTimeout)
	}
}

// Test timing constants. Keeping them as package-level named values makes
// the intent obvious (settle = "wait for change stream to open"; delivery
// = "wait for an event to arrive") and makes knob tuning a one-line
// change when CI hardware changes (L-S3-test-9).
const (
	// streamOpenSettle is the pause after Subscribe() returns before
	// issuing writes that the stream is expected to observe. The
	// change-stream cursor's first $changeStream aggregation round-trip
	// dominates this window; 2s is generous for CI containers.
	streamOpenSettle = 2 * time.Second

	// shortPollInterval is the poll cadence used by tests that want to
	// exercise the polling path without waiting the default 5s tick.
	shortPollInterval = 100 * time.Millisecond

	// deliveryDeadline bounds how long a test will wait for an expected
	// event before failing. Slightly longer than streamOpenSettle +
	// network round-trip to absorb CI flakiness without masking real
	// regressions.
	deliveryDeadline = 10 * time.Second

	// extendedDeliveryDeadline bounds scenarios where an intermediate
	// reconnect is part of the test flow (H5). Covers one backoff cycle
	// plus the delivery window.
	extendedDeliveryDeadline = 15 * time.Second
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
		Client:              client,
		Database:            dbName,
		PollInterval:        pollInterval,
		TenantSchemaEnabled: true,
	}

	s, err := New(cfg)
	require.NoError(t, err, "failed to create test store")

	t.Cleanup(func() {
		_ = s.Close()
	})

	return s
}

func TestIntegration_TenantStoreContracts_ChangeStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)
	storetest.RunTenantContracts(t, func(t *testing.T) store.Store {
		t.Helper()

		return newTestStore(t, client, 0)
	}, storetest.WithEventSettle(streamOpenSettle))
}

func TestIntegration_TenantStoreContracts_Polling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)
	storetest.RunTenantContracts(t, func(t *testing.T) store.Store {
		t.Helper()

		return newTestStore(t, client, shortPollInterval)
	},
		storetest.WithEventSettle(streamOpenSettle),
		storetest.SkipSubtest("TenantSubscribeReceivesDeleteEvent"),
	)
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
	time.Sleep(streamOpenSettle)

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

	time.Sleep(streamOpenSettle)

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

// TestIntegration_PollingAndChangeStreamParity asserts that for the shared
// write paths (insert/update), both subscription modes observe the same
// (namespace, key) pairs. Delete semantics intentionally diverge between the
// two paths — a nested subtest pins that known gap (M-S3-9).
//
// Renamed from PollingMatchesChangeStreamSemantics: "Matches" overstates the
// contract because delete visibility is change-stream exclusive. "Parity"
// accurately describes the insert/update equivalence that IS preserved.
func TestIntegration_PollingAndChangeStreamParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	t.Run("InsertUpdateParity", func(t *testing.T) {
		// Both stores share the same database so they see the same collection.
		dbName := fmt.Sprintf("test_dual_%d", time.Now().UnixNano())

		// Change-stream store.
		csCfg := Config{Client: client, Database: dbName, TenantSchemaEnabled: true}

		csStore, err := New(csCfg)
		require.NoError(t, err)

		t.Cleanup(func() { _ = csStore.Close() })

		// Polling store.
		pollCfg := Config{Client: client, Database: dbName, PollInterval: shortPollInterval, TenantSchemaEnabled: true}

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

		time.Sleep(streamOpenSettle)

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
		}, extendedDeliveryDeadline, 200*time.Millisecond, "change-stream should see 2 events")

		require.Eventually(t, func() bool {
			pollMu.Lock()
			defer pollMu.Unlock()

			return len(pollEvents) >= 2
		}, extendedDeliveryDeadline, 200*time.Millisecond, "poll should see 2 events")

		cancel()

		// cancel() is asynchronous — the subscriber goroutines may still
		// be appending to csEvents/pollEvents when we read them. Wait for
		// both Subscribe loops to exit so no further appends can happen,
		// then snapshot under each mutex to guarantee a race-free parity
		// check (race detector surfaces this otherwise).
		//
		// Use bounded drains with validated terminal errors instead of
		// naked <-errCh reads. An unbounded read would silently turn a
		// cancellation-handling regression into a 10-minute package-level
		// test timeout, which is a much less diagnosable failure mode
		// than "Subscribe did not return within 5s".
		assertSubscribeTerminated(t, csErrCh, "change-stream")
		assertSubscribeTerminated(t, pollErrCh, "poll")

		csMu.Lock()
		csSnapshot := append([]store.Event(nil), csEvents...)
		csMu.Unlock()

		pollMu.Lock()
		pollSnapshot := append([]store.Event(nil), pollEvents...)
		pollMu.Unlock()

		csKeys := eventKeys(csSnapshot)
		pollKeys := eventKeys(pollSnapshot)

		assert.ElementsMatch(t, csKeys, pollKeys,
			"change-stream and poll mode should observe the same namespace+key pairs")
	})

	t.Run("DeleteSemanticsDiverge", func(t *testing.T) {
		// Pins the known gap: the polling path has no native delete signal,
		// so a DeleteTenantValue that happens between two ticks is
		// invisible to polling subscribers but delivered by change streams.
		// Documented in subscribePoll's godoc.
		dbName := fmt.Sprintf("test_delete_%d", time.Now().UnixNano())

		csStore, err := New(Config{Client: client, Database: dbName, TenantSchemaEnabled: true})
		require.NoError(t, err)

		t.Cleanup(func() { _ = csStore.Close() })

		pollStore, err := New(Config{Client: client, Database: dbName, PollInterval: shortPollInterval, TenantSchemaEnabled: true})
		require.NoError(t, err)

		t.Cleanup(func() { _ = pollStore.Close() })

		var (
			csMu, pollMu     sync.Mutex
			csDeletes        int
			pollDeletesAfter int
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Track delete events by watching for tenantID on an event against a
		// (ns, key) we know was deleted. Polling only ever emits updates, so
		// the post-delete count should stay at zero.
		const ns = "del"

		const key = "target"

		const tenant = "tenant-del-1"

		// Capture Subscribe's terminal error instead of discarding it. A
		// Subscribe call that fails before the delete wave (bad config,
		// handler panic recovery, resume-token load error, etc.) would
		// previously leave pollDeletesAfter at zero and let this subtest
		// pass silently even though the polling delivery path never
		// executed. Surfacing the error after cancel() turns that class
		// of regression into a loud test failure.
		csSubErrCh := make(chan error, 1)
		pollSubErrCh := make(chan error, 1)

		go func() {
			csSubErrCh <- csStore.Subscribe(ctx, func(evt store.Event) {
				if evt.Namespace == ns && evt.Key == key && evt.TenantID == tenant {
					csMu.Lock()
					csDeletes++
					csMu.Unlock()
				}
			})
		}()

		go func() {
			pollSubErrCh <- pollStore.Subscribe(ctx, func(evt store.Event) {
				if evt.Namespace == ns && evt.Key == key {
					pollMu.Lock()
					pollDeletesAfter++
					pollMu.Unlock()
				}
			})
		}()

		time.Sleep(streamOpenSettle)

		// Seed an override so there is something to delete.
		require.NoError(t, csStore.SetTenantValue(context.Background(), tenant, store.Entry{
			Namespace: ns, Key: key, Value: []byte(`"seed"`), UpdatedBy: "t",
		}))

		// Wait for the seed event to propagate to both subscribers so our
		// "after" counts capture the delete wave only.
		time.Sleep(shortPollInterval * 3)

		pollMu.Lock()
		pollSeen := pollDeletesAfter
		pollMu.Unlock()

		// Reset: count only deletes strictly after this point.
		pollMu.Lock()
		pollDeletesAfter = 0
		pollMu.Unlock()

		csMu.Lock()
		csDeletes = 0
		csMu.Unlock()

		require.NoError(t, csStore.DeleteTenantValue(context.Background(), tenant, ns, key, "t"))

		require.Eventually(t, func() bool {
			csMu.Lock()
			defer csMu.Unlock()

			return csDeletes >= 1
		}, deliveryDeadline, 200*time.Millisecond, "change-stream should surface the delete")

		// Polling should NOT observe the delete — at most another update if
		// a write races in. Give the ticker a few rotations to confirm.
		time.Sleep(shortPollInterval * 5)

		// Snapshot the counter under the mutex but release before the
		// bounded Subscribe drain below — the drain waits up to 5s and
		// holding pollMu that long would block the subscriber goroutine
		// from its own final handler call (if one is in flight) and risk
		// a self-deadlock.
		pollMu.Lock()
		pollGot := pollDeletesAfter
		pollMu.Unlock()

		assert.Equal(t, 0, pollGot,
			"polling must not observe inter-tick deletes; got %d (pre-delete baseline %d)",
			pollGot, pollSeen)

		// Cancel the subscribers and assert both Subscribe calls return
		// cleanly within the bounded drain window. A Subscribe that fails
		// before the delete wave would leave pollDeletesAfter at zero
		// (passing the assertion above) even though the delivery path
		// was never exercised — draining csSubErrCh/pollSubErrCh here
		// converts that silent pass into a surfaced error.
		cancel()
		assertSubscribeTerminated(t, csSubErrCh, "change-stream")
		assertSubscribeTerminated(t, pollSubErrCh, "poll")
	})
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
// C1: phase-1 rolling-deploy upsert against pre-existing ObjectId _id rows
// ---------------------------------------------------------------------------

// TestIntegration_Mongo_Phase1_SetOverwritesLegacyObjectIDRow pins C1: a
// phase-1 Store (TenantSchemaEnabled=false) invoked against a collection
// that still carries the legacy ObjectId-keyed rows from a pre-tenant
// v5.0.x deploy MUST upsert in place rather than collide on the legacy
// unique index on (namespace, key). The symptom of a regression is an
// E11000 duplicate-key error thrown by the driver.
func TestIntegration_Mongo_Phase1_SetOverwritesLegacyObjectIDRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c1_%d", time.Now().UnixNano())
	collName := defaultCollection

	// Seed a legacy row directly via the driver so its _id is an ObjectId,
	// which is the shape every pre-tenant v5.0.x upsert produced.
	coll := client.Database(dbName).Collection(collName)
	legacyID := bson.NewObjectID()

	_, err := coll.InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: legacyID},
		{Key: "namespace", Value: "legacy"},
		{Key: "key", Value: "fee.rate"},
		// tenant_id absent — phase-1 leaves legacy rows untouched.
		{Key: "value", Value: `"0.01"`},
		{Key: "updated_at", Value: time.Now().UTC()},
		{Key: "updated_by", Value: "seed"},
	})
	require.NoError(t, err, "seeding legacy row must succeed")

	// Build a phase-1 store. ensureLegacySchema creates the unique index on
	// (namespace, key) — this is the index the pre-tenant binaries write
	// against and the one a regression in upsert() would violate.
	s, err := New(Config{
		Client:              client,
		Database:            dbName,
		Collection:          collName,
		TenantSchemaEnabled: false,
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = s.Close() })

	// A phase-1 Set with the same (namespace, key) MUST update the legacy
	// row in place and MUST NOT throw E11000.
	setErr := s.Set(context.Background(), store.Entry{
		Namespace: "legacy",
		Key:       "fee.rate",
		Value:     []byte(`"0.02"`),
		UpdatedBy: "phase1-writer",
	})
	require.NoError(t, setErr, "phase-1 Set against legacy ObjectId row must not collide on (namespace,key) unique index")

	// The Get surface is phase-1 aware only when tenant_id == "_global", so
	// we re-read via the driver to verify the value was actually updated in
	// place (not inserted as a second row).
	var got bson.M

	require.NoError(t,
		coll.FindOne(context.Background(), bson.D{
			{Key: "namespace", Value: "legacy"},
			{Key: "key", Value: "fee.rate"},
		}).Decode(&got),
	)

	assert.Equal(t, `"0.02"`, got["value"], "value must be overwritten")
	assert.Equal(t, "phase1-writer", got["updated_by"])

	// Exactly one row should exist for this (namespace, key).
	count, err := coll.CountDocuments(context.Background(), bson.D{
		{Key: "namespace", Value: "legacy"},
		{Key: "key", Value: "fee.rate"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "phase-1 Set must not insert a duplicate row")
}

// ---------------------------------------------------------------------------
// C4: phase-2 migration scenarios
// ---------------------------------------------------------------------------

// TestIntegration_Mongo_Migration_FreshCollection pins the most common
// case: a brand-new deployment where the collection does not yet exist.
// ensureSchema must be a no-op on the unused collection and leave the
// compound unique index in place so subsequent writes land with the
// phase-2 shape.
func TestIntegration_Mongo_Migration_FreshCollection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c4_fresh_%d", time.Now().UnixNano())

	s, err := New(Config{
		Client:              client,
		Database:            dbName,
		TenantSchemaEnabled: true,
	})
	require.NoError(t, err, "fresh collection must migrate cleanly")

	t.Cleanup(func() { _ = s.Close() })

	// Confirm the compound unique index is present.
	assertCompoundIndexExists(t, client, dbName, defaultCollection)
}

// TestIntegration_Mongo_Migration_BackfillsMissingTenantID seeds a legacy
// row that lacks the tenant_id field, then runs the migration and asserts
// the field is filled in with "_global". This is the step-3 half of
// ensureSchema.
func TestIntegration_Mongo_Migration_BackfillsMissingTenantID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c4_bf_%d", time.Now().UnixNano())
	coll := client.Database(dbName).Collection(defaultCollection)

	// Seed a legacy row missing tenant_id entirely (represents a document
	// inserted by a v5.0.x binary).
	_, err := coll.InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: bson.NewObjectID()},
		{Key: "namespace", Value: "ns"},
		{Key: "key", Value: "k"},
		{Key: "value", Value: `"v"`},
		{Key: "updated_at", Value: time.Now().UTC()},
		{Key: "updated_by", Value: "seed"},
	})
	require.NoError(t, err)

	s, err := New(Config{
		Client:              client,
		Database:            dbName,
		TenantSchemaEnabled: true,
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = s.Close() })

	// The row must now be present under the compound-id shape with
	// tenant_id="_global".
	entry, found, err := s.Get(context.Background(), "ns", "k")
	require.NoError(t, err)
	require.True(t, found, "backfilled row must be visible via phase-2 Get")
	assert.Equal(t, store.SentinelGlobal, entry.TenantID)
	assert.Equal(t, `"v"`, string(entry.Value))
}

// TestIntegration_Mongo_Migration_ObjectIDRewrite seeds multiple legacy
// rows (more than one migration batch) to exercise the batched
// InsertMany/DeleteMany path (H4), then asserts every row survives with a
// compound _id and no duplicates remain.
func TestIntegration_Mongo_Migration_ObjectIDRewrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c4_rewrite_%d", time.Now().UnixNano())
	coll := client.Database(dbName).Collection(defaultCollection)

	// Seed enough rows to span two batches (migrationBatchSize = 500).
	// Kept conservative at 1.5x to avoid long CI runs; the batching code
	// path fires on any input > migrationBatchSize.
	const seedCount = 750

	docs := make([]any, 0, seedCount)

	for i := range seedCount {
		docs = append(docs, bson.D{
			{Key: "_id", Value: bson.NewObjectID()},
			{Key: "namespace", Value: "ns"},
			{Key: "key", Value: fmt.Sprintf("k-%d", i)},
			{Key: "tenant_id", Value: store.SentinelGlobal},
			{Key: "value", Value: fmt.Sprintf(`"v-%d"`, i)},
			{Key: "updated_at", Value: time.Now().UTC()},
			{Key: "updated_by", Value: "seed"},
		})
	}

	_, err := coll.InsertMany(context.Background(), docs)
	require.NoError(t, err)

	// Run the migration.
	s, err := New(Config{
		Client:              client,
		Database:            dbName,
		TenantSchemaEnabled: true,
		SchemaInitTimeout:   2 * time.Minute, // generous for the 750-row walk
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = s.Close() })

	// No ObjectId-keyed rows should remain.
	count, err := coll.CountDocuments(context.Background(), bson.D{
		{Key: "_id", Value: bson.D{{Key: "$type", Value: "objectId"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "rewrite must eliminate every ObjectId _id")

	// Every seed row must be present under its compound-id shape. Exclude
	// the migration-lease sentinel and any other internal rows (none, but
	// an explicit filter guards against future drift).
	total, err := coll.CountDocuments(context.Background(), bson.D{
		{Key: "namespace", Value: "ns"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(seedCount), total, "row count must be preserved across migration")
}

// TestIntegration_Mongo_Migration_IdempotentRerun runs ensureSchema twice
// against the same collection. The second pass must be a no-op (no new
// writes, no E11000).
func TestIntegration_Mongo_Migration_IdempotentRerun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c4_rerun_%d", time.Now().UnixNano())
	coll := client.Database(dbName).Collection(defaultCollection)

	// Seed a legacy row so the first migration has non-trivial work.
	_, err := coll.InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: bson.NewObjectID()},
		{Key: "namespace", Value: "ns"},
		{Key: "key", Value: "k"},
		{Key: "value", Value: `"v"`},
		{Key: "updated_at", Value: time.Now().UTC()},
	})
	require.NoError(t, err)

	// First pass.
	s1, err := New(Config{Client: client, Database: dbName, TenantSchemaEnabled: true})
	require.NoError(t, err)

	require.NoError(t, s1.Close())

	// Second pass against the same collection must not error and must not
	// duplicate the row.
	s2, err := New(Config{Client: client, Database: dbName, TenantSchemaEnabled: true})
	require.NoError(t, err, "re-running ensureSchema must be idempotent")

	t.Cleanup(func() { _ = s2.Close() })

	total, err := coll.CountDocuments(context.Background(), bson.D{
		{Key: "namespace", Value: "ns"},
		{Key: "key", Value: "k"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total, "idempotent rerun must not duplicate rows")
}

// TestIntegration_Mongo_Migration_AmbiguousAborts seeds a (namespace, key)
// pair with both a tenant_id-missing and a tenant_id="_global" row. The
// pre-flight verifyNoAmbiguousTenantDocs must fail the migration with a
// clear error instead of silently collapsing the rows at the backfill
// step.
func TestIntegration_Mongo_Migration_AmbiguousAborts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c4_ambig_%d", time.Now().UnixNano())
	coll := client.Database(dbName).Collection(defaultCollection)

	_, err := coll.InsertMany(context.Background(), []any{
		// Missing tenant_id entirely.
		bson.D{
			{Key: "_id", Value: bson.NewObjectID()},
			{Key: "namespace", Value: "ambig"},
			{Key: "key", Value: "k"},
			{Key: "value", Value: `"a"`},
		},
		// Already has tenant_id="_global".
		bson.D{
			{Key: "_id", Value: bson.NewObjectID()},
			{Key: "namespace", Value: "ambig"},
			{Key: "key", Value: "k"},
			{Key: "tenant_id", Value: store.SentinelGlobal},
			{Key: "value", Value: `"b"`},
		},
	})
	require.NoError(t, err)

	_, err = New(Config{Client: client, Database: dbName, TenantSchemaEnabled: true})
	require.Error(t, err, "phase-2 migration must abort on ambiguous pre-migration state")
	assert.Contains(t, err.Error(), "ambiguous pre-migration state",
		"error message should be actionable for operators")
}

// TestIntegration_Mongo_Migration_CrashRecovery simulates H7: a partial
// migration (some rows already converted, some still on ObjectId _id) is
// interrupted. Re-running ensureSchema must pick up where it left off
// without duplicates and without errors.
//
// The partial state is fabricated by manually inserting both shapes and
// then running the migration once. Crashing mid-rewrite is impossible to
// trigger deterministically from inside the same process, but the
// rewrite path is designed so re-running it is a no-op against already-
// migrated rows.
func TestIntegration_Mongo_Migration_CrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)

	dbName := fmt.Sprintf("test_c4_crash_%d", time.Now().UnixNano())
	coll := client.Database(dbName).Collection(defaultCollection)

	// Already-migrated row (compound _id).
	_, err := coll.InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: compoundID{Namespace: "ns", Key: "migrated", TenantID: store.SentinelGlobal}},
		{Key: "namespace", Value: "ns"},
		{Key: "key", Value: "migrated"},
		{Key: "tenant_id", Value: store.SentinelGlobal},
		{Key: "value", Value: `"m"`},
		{Key: "updated_at", Value: time.Now().UTC()},
	})
	require.NoError(t, err)

	// Not-yet-migrated row (ObjectId _id).
	_, err = coll.InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: bson.NewObjectID()},
		{Key: "namespace", Value: "ns"},
		{Key: "key", Value: "pending"},
		{Key: "tenant_id", Value: store.SentinelGlobal},
		{Key: "value", Value: `"p"`},
		{Key: "updated_at", Value: time.Now().UTC()},
	})
	require.NoError(t, err)

	s, err := New(Config{Client: client, Database: dbName, TenantSchemaEnabled: true})
	require.NoError(t, err, "crash-recovery run must complete without error")

	t.Cleanup(func() { _ = s.Close() })

	// Both rows must be readable via the phase-2 Get surface.
	migrated, foundM, err := s.Get(context.Background(), "ns", "migrated")
	require.NoError(t, err)
	require.True(t, foundM)
	assert.Equal(t, `"m"`, string(migrated.Value))

	pending, foundP, err := s.Get(context.Background(), "ns", "pending")
	require.NoError(t, err)
	require.True(t, foundP)
	assert.Equal(t, `"p"`, string(pending.Value))

	// No ObjectId-_id rows should remain.
	count, err := coll.CountDocuments(context.Background(), bson.D{
		{Key: "_id", Value: bson.D{{Key: "$type", Value: "objectId"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "rewrite must eliminate every ObjectId _id on retry")
}

// ---------------------------------------------------------------------------
// H5: change-stream reconnect after stream closure
// ---------------------------------------------------------------------------

// TestIntegration_Mongo_ChangeStream_ReconnectsAfterStreamClose exercises
// the reconnect path in subscribeChangeStream: an in-flight stream is
// forcibly torn down by issuing killCursors, the subscriber loop falls
// into backoff, reconnects, and delivers a subsequent write. Guards
// against a regression where a closed stream returns a terminal error the
// loop treats as non-recoverable.
func TestIntegration_Mongo_ChangeStream_ReconnectsAfterStreamClose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, _ := setupMongoDB(t)
	s := newTestStore(t, client, 0) // change-stream mode

	var mu sync.Mutex

	var events []store.Event

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Capture the terminal error from Subscribe so a config-phase failure
	// (bad resume token, killed cursor on the very first cursor open, etc.)
	// surfaces as an actionable diagnostic rather than the much less useful
	// "first event must be delivered" timeout downstream.
	subErrCh := make(chan error, 1)

	go func() {
		subErrCh <- s.Subscribe(ctx, func(evt store.Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		})
	}()

	time.Sleep(streamOpenSettle)

	// First write — baseline that the stream is alive.
	require.NoError(t, s.SetTenantValue(context.Background(), "t1", store.Entry{
		Namespace: "h5", Key: "k", Value: []byte(`"1"`), UpdatedBy: "w1",
	}))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(events) >= 1
	}, deliveryDeadline, 100*time.Millisecond, "first event must be delivered before reconnect test")

	// Forcibly close every open cursor on this database. mongo-driver/v2's
	// change stream cursor is an aggregate cursor and is listed by
	// listCursors; killCursors on its ID severs the stream the same way a
	// primary step-down would. The driver should surface a retryable
	// error, the reconnect loop should open a fresh cursor, and the next
	// write should be delivered.
	adminDB := client.Database("admin")

	// Enumerate the cursors. MongoDB 7 supports `$listLocalSessions` but
	// the simpler approach is to call `killAllSessions` on the admin db,
	// which aborts every open cursor on the connection. If this command
	// is rejected or no-ops the second write would arrive on the original
	// stream and the reconnect path would never be exercised, so fail the
	// test loudly rather than let it pass silently.
	cmdErr := adminDB.RunCommand(context.Background(), bson.D{
		{Key: "killAllSessionsByPattern", Value: bson.A{}},
	}).Err()
	require.NoError(t, cmdErr,
		"failed to tear down change-stream sessions; reconnect path was not exercised")

	// Give the reconnect loop time to re-establish the stream.
	time.Sleep(streamOpenSettle)

	// Second write — must be delivered through the reconnected stream.
	require.NoError(t, s.SetTenantValue(context.Background(), "t1", store.Entry{
		Namespace: "h5", Key: "k", Value: []byte(`"2"`), UpdatedBy: "w2",
	}))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(events) >= 2
	}, extendedDeliveryDeadline, 200*time.Millisecond,
		"second event must be delivered after change-stream reconnect")

	// Bounded drain: cancel now (instead of waiting for defer) and confirm
	// the Subscribe loop terminated cleanly within subscriberDrainTimeout.
	// An unbounded read here would let a cancellation-handling regression
	// masquerade as a 10-minute package-level timeout, which is far less
	// diagnosable than "Subscribe did not return within 5s".
	cancel()
	assertSubscribeTerminated(t, subErrCh, "change-stream")
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

// assertCompoundIndexExists asserts the (namespace, key, tenant_id) unique
// index is present on the collection. Used by migration tests to pin the
// post-migration schema shape.
//
// Also pins the "unique" flag so a regression that creates a non-unique
// compound index (which would still allow duplicate tenant rows and break
// last-write-wins) fails this assertion instead of passing silently.
func assertCompoundIndexExists(t *testing.T, client *mongo.Client, dbName, collName string) {
	t.Helper()

	coll := client.Database(dbName).Collection(collName)

	cursor, err := coll.Indexes().List(context.Background())
	require.NoError(t, err)

	var indexes []bson.M
	require.NoError(t, cursor.All(context.Background(), &indexes))

	var found bool

	for _, idx := range indexes {
		// mongo-driver/v2 decodes nested documents inside a top-level
		// bson.M as bson.D (to preserve field order). listIndexes returns
		// the "key" subdocument as bson.D — an older implementation of
		// this helper asserted to bson.M and silently skipped every
		// index, which is why the assertion failed even when the compound
		// index was present. Iterate bson.D by element instead.
		keyDoc, ok := idx["key"].(bson.D)
		if !ok {
			continue
		}

		// Pin the compound index ORDER, not just field membership: the
		// migration contract and keyset-ordering paths rely on
		// (namespace, key, tenant_id) specifically. A reordered variant
		// like (tenant_id, namespace, key) must fail this assertion.
		if len(keyDoc) != 3 ||
			keyDoc[0].Key != "namespace" ||
			keyDoc[1].Key != "key" ||
			keyDoc[2].Key != "tenant_id" {
			continue
		}

		// MongoDB omits the "unique" flag from listIndexes output when
		// false, so a missing key here is treated as non-unique. Require
		// the flag to be both present and true.
		unique, _ := idx["unique"].(bool)
		if !unique {
			continue
		}

		found = true

		break
	}

	assert.True(t, found, "unique compound (namespace, key, tenant_id) index must exist after phase-2 migration")
}
