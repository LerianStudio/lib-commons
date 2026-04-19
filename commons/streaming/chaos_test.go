//go:build chaos

// Package streaming chaos tests (T9 chaos).
//
// Chaos tests run toxiproxy between the Producer and a real Redpanda broker
// to simulate network degradation. Build tag: chaos. Dual-gate: CHAOS=1
// environment variable + `!testing.Short()`.
//
// How to run:
//
//	CHAOS=1 go test -tags=chaos -timeout=10m ./commons/streaming/...
//
// Requires Docker for both the Redpanda and Toxiproxy containers. Skips
// cleanly when Docker is unavailable (same skipIfNoDocker pattern as the
// integration suite).
//
// Scenario (TestChaos_BrokerLatency_CircuitOpensAndOutboxCatches):
//
//  1. NORMAL: emit N healthy events; CB stays CLOSED.
//  2. INJECT: add 2s latency toxic; emit more events; some timeout → feed CB.
//  3. DEGRADED: CB transitions to OPEN within 5s; outbox absorbs emits.
//  4. RESTORE: remove latency toxic.
//  5. RECOVER: drain outbox via the replay handler; CB eventually CLOSED.

package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredpanda "github.com/testcontainers/testcontainers-go/modules/redpanda"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// chaosRedpandaImage pins the Redpanda tag used by chaos tests. Matches the
// integration suite so the chaos + integration runs share an image cache.
const chaosRedpandaImage = "docker.redpanda.com/redpandadata/redpanda:v24.2.18"

// chaosToxiproxyImage pins the Toxiproxy tag. 2.x line is the only one with
// the HTTP API the client library expects.
const chaosToxiproxyImage = "ghcr.io/shopify/toxiproxy:2.12.0"

// chaosSource is the CloudEvents source used in the chaos scenario. Matches
// the integration-test style so logs/spans collected during a chaos run can
// be visually correlated.
const chaosSource = "//lerian.test/streaming-chaos"

// chaosSkipIfDisabled implements the dual-gate CHAOS=1 + !testing.Short()
// requirement. Returns true when the test should be skipped.
func chaosSkipIfDisabled(t *testing.T) bool {
	t.Helper()

	if testing.Short() {
		t.Skip("chaos test skipped under -short")
		return true
	}

	if os.Getenv("CHAOS") != "1" {
		t.Skip("chaos test skipped; set CHAOS=1 to enable")
		return true
	}

	return false
}

// chaosSkipIfNoDocker mirrors the integration suite's skipIfNoDocker helper
// without importing the integration-tagged file. Any container start error
// containing well-known Docker-unavailable markers → t.Skip.
func chaosSkipIfNoDocker(t *testing.T, err error) bool {
	t.Helper()

	if err == nil {
		return false
	}

	msg := err.Error()
	if strings.Contains(msg, "Cannot connect to the Docker daemon") ||
		strings.Contains(msg, "docker socket") ||
		strings.Contains(msg, "Is the docker daemon running") ||
		strings.Contains(msg, "provider not implemented") {
		t.Skipf("Docker not available in this environment: %v", err)
		return true
	}

	return false
}

// chaosInMemoryOutbox is a minimal thread-safe OutboxRepository for the
// chaos scenario. Stores events in memory and exposes them for replay.
// Satisfies outbox.OutboxRepository interface methods the chaos path
// touches — unused methods stub to zero values. Naming chose
// "chaosInMemoryOutbox" to avoid colliding with outbox_handler_test.go's
// own fakes (which live under a different build tag).
type chaosInMemoryOutbox struct {
	mu    sync.Mutex
	rows  []*outbox.OutboxEvent
	drain []*outbox.OutboxEvent // mirrors rows after replay for verification
}

func (c *chaosInMemoryOutbox) Create(
	_ context.Context,
	event *outbox.OutboxEvent,
) (*outbox.OutboxEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Defensive: a fresh copy so subsequent mutations through the same
	// pointer don't leak into our captured list.
	copy := *event
	c.rows = append(c.rows, &copy)

	return &copy, nil
}

func (c *chaosInMemoryOutbox) CreateWithTx(
	ctx context.Context,
	_ outbox.Tx,
	event *outbox.OutboxEvent,
) (*outbox.OutboxEvent, error) {
	return c.Create(ctx, event)
}

func (c *chaosInMemoryOutbox) ListPending(
	_ context.Context,
	limit int,
) ([]*outbox.OutboxEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	take := len(c.rows)
	if limit > 0 && limit < take {
		take = limit
	}

	out := make([]*outbox.OutboxEvent, take)
	copy(out, c.rows[:take])

	return out, nil
}

func (*chaosInMemoryOutbox) ListPendingByType(
	_ context.Context,
	_ string,
	_ int,
) ([]*outbox.OutboxEvent, error) {
	return nil, nil
}

func (*chaosInMemoryOutbox) ListTenants(_ context.Context) ([]string, error) {
	return nil, nil
}

func (c *chaosInMemoryOutbox) GetByID(
	_ context.Context,
	id uuid.UUID,
) (*outbox.OutboxEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, r := range c.rows {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (c *chaosInMemoryOutbox) MarkPublished(
	_ context.Context,
	id uuid.UUID,
	_ time.Time,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, r := range c.rows {
		if r.ID == id {
			c.drain = append(c.drain, r)
			c.rows = append(c.rows[:i], c.rows[i+1:]...)
			return nil
		}
	}

	return nil
}

func (*chaosInMemoryOutbox) MarkFailed(
	_ context.Context,
	_ uuid.UUID,
	_ string,
	_ int,
) error {
	return nil
}

func (*chaosInMemoryOutbox) ListFailedForRetry(
	_ context.Context,
	_ int,
	_ time.Time,
	_ int,
) ([]*outbox.OutboxEvent, error) {
	return nil, nil
}

func (*chaosInMemoryOutbox) ResetForRetry(
	_ context.Context,
	_ int,
	_ time.Time,
	_ int,
) ([]*outbox.OutboxEvent, error) {
	return nil, nil
}

func (*chaosInMemoryOutbox) ResetStuckProcessing(
	_ context.Context,
	_ int,
	_ time.Time,
	_ int,
) ([]*outbox.OutboxEvent, error) {
	return nil, nil
}

func (*chaosInMemoryOutbox) MarkInvalid(
	_ context.Context,
	_ uuid.UUID,
	_ string,
) error {
	return nil
}

// pendingCount returns the number of outbox rows not yet drained.
func (c *chaosInMemoryOutbox) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.rows)
}

// drainedCount returns the number of rows that were published through
// MarkPublished — i.e., successfully replayed.
func (c *chaosInMemoryOutbox) drainedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.drain)
}

// snapshotPending returns a copy of the current pending rows so the caller
// can iterate without holding the lock.
func (c *chaosInMemoryOutbox) snapshotPending() []*outbox.OutboxEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]*outbox.OutboxEvent, len(c.rows))
	copy(out, c.rows)

	return out
}

// TestChaos_BrokerLatency_CircuitOpensAndOutboxCatches is the T9 chaos
// scenario. Runs a 5-phase drill as described at the top of this file.
func TestChaos_BrokerLatency_CircuitOpensAndOutboxCatches(t *testing.T) {
	if chaosSkipIfDisabled(t) {
		return
	}

	// --- Phase 0: infra setup. ---
	ctx := context.Background()

	// Shared docker network so toxiproxy can dial redpanda by container name.
	nw, err := tcnetwork.New(ctx)
	if chaosSkipIfNoDocker(t, err) {
		return
	}
	require.NoError(t, err, "network create")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = nw.Remove(cleanupCtx)
	})

	// Redpanda alias so toxiproxy has a stable upstream target.
	redpandaAlias := "redpanda-chaos"

	rp, err := tcredpanda.Run(ctx,
		chaosRedpandaImage,
		tcredpanda.WithAutoCreateTopics(),
		tcnetwork.WithNetwork([]string{redpandaAlias}, nw),
	)
	if chaosSkipIfNoDocker(t, err) {
		return
	}
	require.NoError(t, err, "redpanda run")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = rp.Terminate(cleanupCtx)
	})

	// redpanda internal-network port from kadm perspective. Default Redpanda
	// Kafka port is 9092; the internal listener on the container network
	// uses that port name.
	const kafkaInternalPort = "9092/tcp"

	// Toxiproxy container on the same network. Expose the API port (8474)
	// to the host so our client library can drive it. Toxiproxy will also
	// listen on port 8666 for the Kafka proxy — we'll expose that to the
	// host too so our Producer can dial it.
	toxiproxyReq := testcontainers.ContainerRequest{
		Image:        chaosToxiproxyImage,
		ExposedPorts: []string{"8474/tcp", "8666/tcp"},
		WaitingFor:   wait.ForListeningPort("8474/tcp").WithStartupTimeout(60 * time.Second),
		Networks:     []string{nw.Name},
		Cmd:          []string{"-host=0.0.0.0"},
	}
	tpContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: toxiproxyReq,
		Started:          true,
	})
	if chaosSkipIfNoDocker(t, err) {
		return
	}
	require.NoError(t, err, "toxiproxy run")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = tpContainer.Terminate(cleanupCtx)
	})

	// Resolve port mappings on the host so the test can reach both the
	// toxiproxy admin API and the proxied Kafka endpoint.
	tpAPIHost, err := tpContainer.Host(ctx)
	require.NoError(t, err, "toxiproxy host")

	tpAPIPort, err := tpContainer.MappedPort(ctx, "8474/tcp")
	require.NoError(t, err, "toxiproxy api port")

	tpKafkaPort, err := tpContainer.MappedPort(ctx, "8666/tcp")
	require.NoError(t, err, "toxiproxy kafka port")

	// Configure toxiproxy: create a proxy that listens on 8666 inside the
	// container and forwards to redpanda:9092 (the internal alias).
	tpClient := toxiproxy.NewClient(net.JoinHostPort(tpAPIHost, tpAPIPort.Port()))

	proxy, err := tpClient.CreateProxy(
		"redpanda-kafka",
		"0.0.0.0:8666",
		redpandaAlias+":9092",
	)
	require.NoError(t, err, "create toxiproxy proxy")

	t.Cleanup(func() {
		_ = proxy.Delete()
	})

	// Verify the broker can answer through the toxiproxy path before we
	// start the chaos drill.
	proxiedBroker := net.JoinHostPort(tpAPIHost, tpKafkaPort.Port())
	chaosWaitForBroker(t, proxiedBroker)

	// Pre-create topic to avoid UNKNOWN_TOPIC_OR_PARTITION on the first
	// produce. Use the DIRECT broker address (not the proxy) so topic setup
	// isn't affected by the injected chaos later.
	directBroker, err := rp.KafkaSeedBroker(ctx)
	require.NoError(t, err, "redpanda direct seed broker")

	chaosTopic := topicPrefix + "chaos.event"
	chaosEnsureTopic(t, directBroker, chaosTopic)
	chaosEnsureTopic(t, directBroker, dlqTopic(chaosTopic))

	// --- Phase 1: NORMAL — 100 healthy emits. ---

	store := &chaosInMemoryOutbox{}

	cfg := Config{
		Enabled:               true,
		Brokers:               []string{proxiedBroker},
		ClientID:              "chaos-producer",
		BatchLingerMs:         0,
		BatchMaxBytes:         defaultBatchMaxBytes,
		MaxBufferedRecords:    defaultMaxBufferedRecords,
		Compression:           defaultCompression,
		RecordRetries:         1,
		RecordDeliveryTimeout: 3 * time.Second,
		RequiredAcks:          defaultRequiredAcks,
		CBFailureRatio:        0.5,
		CBMinRequests:         5,
		CBTimeout:             10 * time.Second,
		CloseTimeout:          3 * time.Second,
		CloudEventsSource:     chaosSource,
	}

	p, err := NewProducer(ctx, cfg,
		WithLogger(log.NewNop()),
		WithOutboxRepository(store),
	)
	require.NoError(t, err, "NewProducer")
	t.Cleanup(func() { _ = p.Close() })

	emitted := int64(0)
	const normalEmits = 100

	healthyEvent := func(i int) Event {
		return Event{
			TenantID:     "tenant-chaos",
			ResourceType: "chaos",
			EventType:    "event",
			Source:       chaosSource,
			Payload:      json.RawMessage(fmt.Sprintf(`{"seq":%d}`, i)),
		}
	}

	for i := 0; i < normalEmits; i++ {
		emitCtx, emitCancel := context.WithTimeout(context.Background(), 10*time.Second)
		require.NoError(t, p.Emit(emitCtx, healthyEvent(i)), "NORMAL emit %d", i)
		emitCancel()
		atomic.AddInt64(&emitted, 1)
	}

	require.Equal(t, int32(flagCBClosed), p.cbStateFlag.Load(),
		"after 100 healthy emits the breaker must be CLOSED")

	// --- Phase 2: INJECT — 2s latency toxic on downstream. ---

	_, err = proxy.AddToxic(
		"slow_responses",
		"latency",
		"downstream",
		1.0,
		toxiproxy.Attributes{
			"latency": 2000, // 2s per-packet latency
		},
	)
	require.NoError(t, err, "add latency toxic")

	// Emit more events. Each will block on RecordDeliveryTimeout (3s) and
	// surface a timeout error. We don't assert per-emit result — the CB
	// transition is the load-bearing signal, checked in phase 3.
	const injectEmits = 50

	for i := 0; i < injectEmits; i++ {
		emitCtx, emitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = p.Emit(emitCtx, healthyEvent(1000+i))
		emitCancel()

		// Early exit once CB flips to OPEN so we don't spend 5s×50 = 250s.
		if p.cbStateFlag.Load() == flagCBOpen {
			break
		}
	}

	// --- Phase 3: DEGRADED — CB transitions to OPEN within 5s. ---

	observedOpen := false
	pollCBDeadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(pollCBDeadline) {
		if p.cbStateFlag.Load() == flagCBOpen {
			observedOpen = true
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	require.True(t, observedOpen, "CB never transitioned to OPEN within 5s under latency injection")

	// With outbox wired, a subsequent Emit succeeds (returns nil) because
	// the outbox absorbs the hit.
	outboxCtx, outboxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	require.NoError(t, p.Emit(outboxCtx, healthyEvent(5000)), "Emit during CB OPEN must succeed via outbox")
	outboxCancel()

	require.Positive(t, store.pendingCount(), "outbox must contain at least one pending row")

	// --- Phase 4: RESTORE — remove latency toxic. ---

	require.NoError(t, proxy.RemoveToxic("slow_responses"), "remove latency toxic")

	// --- Phase 5: RECOVER — drain outbox, CB recovers, zero event loss. ---

	// Register the replay handler. RegisterOutboxHandler wires publishDirect
	// (NOT Emit) so replays bypass the breaker and cannot re-enqueue.
	registry := outbox.NewHandlerRegistry()
	require.NoError(t, p.RegisterOutboxHandler(registry, chaosTopic), "RegisterOutboxHandler")

	// Drive the drain manually: snapshot pending rows, call the registered
	// handler on each via registry.Handle, mark published. This mirrors
	// what the outbox Dispatcher's timer loop does — we do it inline so
	// the test doesn't need a Dispatcher instance.

	// Allow the CB HALF-OPEN window to open before replaying: CBTimeout is
	// 10s above. We wait at least that long so the breaker's internal
	// probe cycle allows passes through.
	time.Sleep(11 * time.Second)

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer drainCancel()

	snapshot := store.snapshotPending()
	for _, row := range snapshot {
		require.NoError(t, registry.Handle(drainCtx, row), "drain row %s", row.ID)
		require.NoError(t, store.MarkPublished(drainCtx, row.ID, time.Now().UTC()),
			"MarkPublished row %s", row.ID)
	}

	require.Zero(t, store.pendingCount(), "all outbox rows must drain")
	require.Positive(t, store.drainedCount(), "at least one row must have been drained")

	// CB transitions back to CLOSED after successful replays. HALF-OPEN may
	// be transient — we poll for CLOSED up to 10s.
	pollClosedDeadline := time.Now().Add(10 * time.Second)
	observedClosed := false

	for time.Now().Before(pollClosedDeadline) {
		if p.cbStateFlag.Load() == flagCBClosed {
			observedClosed = true
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	require.True(t, observedClosed, "CB never returned to CLOSED after restore + drain")

	// Zero event loss verification: consume from the topic and count
	// successful records.
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(directBroker),
		kgo.ConsumeTopics(chaosTopic),
		kgo.ConsumerGroup("chaos-consumer-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:12]),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	require.NoError(t, err, "consumer kgo.NewClient")
	t.Cleanup(func() { consumer.Close() })

	// We expect at least `normalEmits + 1 (outbox-absorbed) + drain` records.
	// Exact number is hard to pin because some INJECT-phase emits may have
	// landed before the CB tripped. The lower bound is normalEmits + drained.
	wantAtLeast := int64(normalEmits) + int64(store.drainedCount())

	received := int64(0)
	consumerCtx, consumerCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer consumerCancel()

	for received < wantAtLeast {
		fetches := consumer.PollFetches(consumerCtx)
		if consumerCtx.Err() != nil {
			break
		}

		fetches.EachRecord(func(_ *kgo.Record) {
			received++
		})
	}

	require.GreaterOrEqualf(t, received, wantAtLeast,
		"zero-event-loss: received %d records; want at least %d", received, wantAtLeast)
}

// chaosWaitForBroker polls ListTopics until the broker responds successfully
// through the given address (host:port). Used after toxiproxy setup to
// confirm the proxy forwards correctly before we start the drill.
func chaosWaitForBroker(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		cl, err := kgo.NewClient(kgo.SeedBrokers(addr))
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		admin := kadm.NewClient(cl)
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)

		_, err = admin.ListTopics(pingCtx)
		cancel()
		cl.Close()

		if err == nil {
			return
		}

		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}

	require.NoErrorf(t, lastErr, "broker %s never became reachable through toxiproxy", addr)
}

// chaosEnsureTopic creates the named topic if it doesn't already exist.
// Uses the DIRECT broker address so topic setup isn't affected by any
// toxiproxy state.
func chaosEnsureTopic(t *testing.T, broker, topic string) {
	t.Helper()

	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	require.NoError(t, err, "admin kgo.NewClient")
	defer cl.Close()

	admin := kadm.NewClient(cl)

	adminCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = admin.CreateTopic(adminCtx, 1, 1, nil, topic)
	if err == nil {
		return
	}

	if strings.Contains(err.Error(), "already exists") ||
		strings.Contains(err.Error(), "TOPIC_ALREADY_EXISTS") {
		return
	}

	require.NoErrorf(t, err, "CreateTopic %s", topic)
}

// Sanity compile-time check: *chaosInMemoryOutbox satisfies the repository
// contract Producer consumes. If outbox.OutboxRepository ever grows a new
// method, this line fails the build and forces us to add a stub.
var _ outbox.OutboxRepository = (*chaosInMemoryOutbox)(nil)
