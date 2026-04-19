//go:build unit

package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// --- T5 helpers. ---

// spyLogger is a concurrency-safe *log.Logger implementation that records
// every Log invocation for assertion in tests. It's deliberately minimal —
// T5 only needs to confirm that the DLQ-failure path emits an ERROR-level
// record with the expected fields, not to match the full zap/stdlib API.
type spyLogger struct {
	mu      sync.Mutex
	entries []spyEntry
}

type spyEntry struct {
	level  log.Level
	msg    string
	fields map[string]any
}

func (s *spyLogger) Log(_ context.Context, level log.Level, msg string, fields ...log.Field) {
	s.mu.Lock()
	defer s.mu.Unlock()

	indexed := make(map[string]any, len(fields))
	for _, f := range fields {
		indexed[f.Key] = f.Value
	}

	s.entries = append(s.entries, spyEntry{level: level, msg: msg, fields: indexed})
}

func (s *spyLogger) With(_ ...log.Field) log.Logger { return s }
func (s *spyLogger) WithGroup(_ string) log.Logger  { return s }
func (s *spyLogger) Enabled(_ log.Level) bool       { return true }
func (s *spyLogger) Sync(_ context.Context) error   { return nil }

// firstErrorEntry returns the first ERROR-level entry whose message contains
// the given substring. Returns nil when no such entry exists.
func (s *spyLogger) firstErrorEntry(contains string) *spyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		e := &s.entries[i]
		if e.level == log.LevelError && strings.Contains(e.msg, contains) {
			return e
		}
	}

	return nil
}

// kfakeDLQConfig is kfakeConfig with an additional pre-seeded DLQ topic.
// Most T5 tests force a produce failure on the source topic and assert the
// DLQ receives the message — pre-seeding the DLQ topic avoids
// UNKNOWN_TOPIC_OR_PARTITION on the happy-path DLQ write. The single test
// that covers "DLQ write itself fails" uses kfakeConfig directly with no
// DLQ seed, plus AllowAutoTopicCreation disabled (which kfake doesn't
// expose — see TestProducer_PublishDLQ_DLQFailure_SurfacesWrappedError for
// the alternative strategy).
func kfakeDLQConfig(t *testing.T) (Config, *kfake.Cluster) {
	t.Helper()

	sourceTopic := "lerian.streaming.transaction.created"
	dlq := sourceTopic + ".dlq"

	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.AllowAutoTopicCreation(),
		kfake.DefaultNumPartitions(3),
		kfake.SeedTopics(3, sourceTopic, dlq),
	)
	if err != nil {
		t.Fatalf("kfake.NewCluster err = %v", err)
	}

	t.Cleanup(cluster.Close)

	return Config{
		Enabled:               true,
		Brokers:               cluster.ListenAddrs(),
		ClientID:              "test-producer",
		BatchLingerMs:         1,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    10_000,
		Compression:           "none",
		RecordRetries:         0, // zero retries so source failure surfaces immediately
		RecordDeliveryTimeout: 5 * time.Second,
		RequiredAcks:          "leader",
		CBFailureRatio:        0.99, // keep the breaker CLOSED for the test duration
		CBMinRequests:         1_000_000,
		CBTimeout:             5 * time.Second,
		CloseTimeout:          5 * time.Second,
		CloudEventsSource:     "//test",
	}, cluster
}

// injectProduceError installs a kfake control function that intercepts
// ProduceRequests targeting sourceTopic and replies with the given Kafka
// error code. Other topics (crucially the DLQ topic) pass through normally.
//
// The installed control function KeepsControl so it fires for every
// ProduceRequest during the test — not just the first. Test teardown (via
// cluster.Close) awakens any sleeping control functions; no explicit
// uninstall is needed.
//
// ProduceRequest v13+ identifies topics by TopicID (uuid) instead of name.
// We resolve TopicID back to the topic name via cluster.TopicIDInfo so the
// match still works with modern brokers.
func injectProduceError(cluster *kfake.Cluster, sourceTopic string, errCode int16) {
	cluster.ControlKey(int16(kmsg.Produce), func(req kmsg.Request) (kmsg.Response, error, bool) {
		cluster.KeepControl()

		produceReq, ok := req.(*kmsg.ProduceRequest)
		if !ok {
			return nil, nil, false
		}

		// Resolve Topic name from TopicID (v13+) or direct name (v0-v12).
		topicNames := resolveRequestTopicNames(cluster, produceReq)

		// Check if ANY topic in this request matches sourceTopic.
		matched := false
		for _, name := range topicNames {
			if name == sourceTopic {
				matched = true
				break
			}
		}

		if !matched {
			return nil, nil, false
		}

		// Build a ProduceResponse that fails every partition of the
		// source topic with errCode. For v13+, the response echoes the
		// TopicID back; for v0-v12 it uses the topic name.
		resp := produceReq.ResponseKind().(*kmsg.ProduceResponse)
		resp.Version = produceReq.Version

		for i, topic := range produceReq.Topics {
			if topicNames[i] != sourceTopic {
				continue
			}

			topicResp := kmsg.NewProduceResponseTopic()
			topicResp.Topic = topic.Topic
			topicResp.TopicID = topic.TopicID

			for _, partition := range topic.Partitions {
				partResp := kmsg.NewProduceResponseTopicPartition()
				partResp.Partition = partition.Partition
				partResp.ErrorCode = errCode
				topicResp.Partitions = append(topicResp.Partitions, partResp)
			}

			resp.Topics = append(resp.Topics, topicResp)
		}

		return resp, nil, true
	})
}

// resolveRequestTopicNames returns a parallel slice of topic names for
// produceReq.Topics. For v13+, TopicID→name via cluster.TopicIDInfo. For
// v0-v12, the topic's Topic string field is used directly. Missing IDs
// resolve to the empty string (match will fail gracefully).
func resolveRequestTopicNames(cluster *kfake.Cluster, produceReq *kmsg.ProduceRequest) []string {
	names := make([]string, len(produceReq.Topics))

	for i, topic := range produceReq.Topics {
		if topic.Topic != "" {
			names[i] = topic.Topic
			continue
		}

		info := cluster.TopicIDInfo(topic.TopicID)
		if info != nil {
			names[i] = info.Topic
		}
	}

	return names
}

// readDLQRecord polls a kgo consumer until one record lands on topic, or the
// deadline expires. Returns nil when no record is fetched (lets tests assert
// on absence). Between empty PollFetches iterations we sleep for 10ms to
// avoid burning CPU under parallel test execution — kfake PollFetches can
// return fast with no records while metadata is still refreshing.
func readDLQRecord(t *testing.T, cluster *kfake.Cluster, topic string, timeout time.Duration) *kgo.Record {
	t.Helper()

	consumer := newConsumer(t, cluster, topic)

	fetchCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Poll in a small loop so kfake's metadata refresh doesn't starve us.
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		fetches := consumer.PollFetches(fetchCtx)

		var got *kgo.Record

		fetches.EachRecord(func(r *kgo.Record) {
			if got == nil {
				got = r
			}
		})

		if got != nil {
			return got
		}

		if fetchCtx.Err() != nil {
			return nil
		}

		// Sleep briefly between empty polls. Prevents spinning the CPU
		// under -race / parallel test execution when kfake returns fast
		// with no records while controller metadata propagates.
		time.Sleep(10 * time.Millisecond)
	}

	return nil
}

// headerValue returns the value of the named header on record, or "" if
// absent. Mirrors the test-side expectation that every x-lerian-dlq-* header
// is present on DLQ records.
func headerValue(record *kgo.Record, key string) string {
	if record == nil {
		return ""
	}

	for _, h := range record.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}

	return ""
}

// --- Pure unit tests (no kfake). ---

// TestDlqTopic_Derivation asserts the {source}.dlq naming convention. The
// helper is trivial today but the contract IS the test — a refactor that
// breaks suffix derivation must surface here before it breaks downstream
// replay tools.
func TestDlqTopic_Derivation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		want   string
	}{
		{"lerian.streaming.transaction.created", "lerian.streaming.transaction.created.dlq"},
		{"base", "base.dlq"},
		{"", ".dlq"},
		{"already.dlq", "already.dlq.dlq"}, // no double-suffix protection; caller's job
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			t.Parallel()
			if got := dlqTopic(tt.source); got != tt.want {
				t.Errorf("dlqTopic(%q) = %q; want %q", tt.source, got, tt.want)
			}
		})
	}
}

// TestIsDLQRoutable_TruthTable: every ErrorClass value has a documented
// routing decision. Checked here so a future class addition (9th class) is
// forced to be explicit in this table.
func TestIsDLQRoutable_TruthTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		cls  ErrorClass
		want bool
	}{
		{ClassSerialization, true},
		{ClassValidation, false}, // caller fault — returned to caller
		{ClassAuth, true},
		{ClassTopicNotFound, true},
		{ClassBrokerUnavailable, true},
		{ClassNetworkTimeout, true},
		{ClassContextCanceled, false}, // caller canceled — not routed
		{ClassBrokerOverloaded, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.cls), func(t *testing.T) {
			t.Parallel()
			if got := isDLQRoutable(tt.cls); got != tt.want {
				t.Errorf("isDLQRoutable(%s) = %v; want %v", tt.cls, got, tt.want)
			}
		})
	}
}

// TestExtractRetryCount_Stub proves the stub returns 0 for every input. When
// franz-go exposes a real accessor in a future version this test is the
// canary — it will force a deliberate update rather than silent drift.
func TestExtractRetryCount_Stub(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain ErrRecordRetries", kgo.ErrRecordRetries},
		{"wrapped ErrRecordRetries", fmt.Errorf("wrap: %w", kgo.ErrRecordRetries)},
		{"unrelated error", errors.New("something else")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractRetryCount(tt.err); got != 0 {
				t.Errorf("extractRetryCount = %d; want 0 (stub)", got)
			}
		})
	}
}

// TestPublishDLQ_NilReceiver proves nil-receiver safety. Parallels the guard
// on every other *Producer method.
func TestPublishDLQ_NilReceiver(t *testing.T) {
	t.Parallel()

	var p *Producer

	ev := sampleEvent()

	err := p.publishDLQ(context.Background(), ev, errors.New("cause"), ev.Topic(), 0, time.Now())
	if !errors.Is(err, ErrNilProducer) {
		t.Errorf("nil.publishDLQ err = %v; want ErrNilProducer", err)
	}
}

// TestPublishDLQ_ClosedProducer surfaces ErrEmitterClosed when the producer
// has been closed mid-flight. Matches the semantics of publishDirect's
// defensive closed-flag check.
func TestPublishDLQ_ClosedProducer(t *testing.T) {
	cfg, _ := kfakeDLQConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	p := asProducer(t, emitter)

	if err := emitter.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}

	ev := sampleEvent()

	err = p.publishDLQ(context.Background(), ev, errors.New("cause"), ev.Topic(), 0, time.Now())
	if !errors.Is(err, ErrEmitterClosed) {
		t.Errorf("closed publishDLQ err = %v; want ErrEmitterClosed", err)
	}
}

// TestBuildEmitErrorWithDLQ_RoutedSuccess asserts the Cause on an *EmitError
// is the original error alone when DLQ succeeded. Callers can errors.Is
// against kerr sentinels without extra unwrapping.
func TestBuildEmitErrorWithDLQ_RoutedSuccess(t *testing.T) {
	t.Parallel()

	orig := kerr.MessageTooLarge
	res := dlqRouteResult{routed: true}
	ev := sampleEvent()

	emit := buildEmitErrorWithDLQ(ev, orig, ev.Topic(), ClassSerialization, res)
	if emit == nil {
		t.Fatalf("buildEmitErrorWithDLQ returned nil")
	}

	if !errors.Is(emit, kerr.MessageTooLarge) {
		t.Errorf("errors.Is(emit, MessageTooLarge) = false; want true")
	}
	if emit.Class != ClassSerialization {
		t.Errorf("emit.Class = %q; want %q", emit.Class, ClassSerialization)
	}
}

// TestBuildEmitErrorWithDLQ_DLQFailed asserts both legs (original cause and
// DLQ failure) remain errors.Is-matchable when DLQ itself failed.
func TestBuildEmitErrorWithDLQ_DLQFailed(t *testing.T) {
	t.Parallel()

	orig := kerr.MessageTooLarge
	dlqFail := errors.New("dlq produce sync failed")
	res := dlqRouteResult{routed: false, dlqErr: dlqFail}
	ev := sampleEvent()

	emit := buildEmitErrorWithDLQ(ev, orig, ev.Topic(), ClassSerialization, res)

	if !errors.Is(emit, kerr.MessageTooLarge) {
		t.Errorf("errors.Is(emit, MessageTooLarge) = false; want true (original cause preserved)")
	}
	if !errors.Is(emit, dlqFail) {
		t.Errorf("errors.Is(emit, dlqFail) = false; want true (DLQ leg preserved)")
	}
	if !strings.Contains(emit.Error(), "DLQ also failed") {
		t.Errorf("emit.Error() = %q; want to contain 'DLQ also failed'", emit.Error())
	}
}

// --- kfake-backed integration tests. ---

// TestProducer_PublishDLQ_WritesAllHeaders is the primary T5 acceptance
// scenario. Force a broker-side error (MessageTooLarge) via kfake.Control,
// verify the DLQ message receives:
//
//  1. All 7 x-lerian-dlq-* headers with correct values.
//  2. All CloudEvents ce-* headers preserved verbatim.
//  3. The original payload bytes, unchanged.
//  4. Partition key preserved (tenant ID for non-system events).
func TestProducer_PublishDLQ_WritesAllHeaders(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.MessageTooLarge.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	event := sampleEvent()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	emitErr := emitter.Emit(ctx, event)
	if emitErr == nil {
		t.Fatalf("Emit err = nil; want non-nil (source topic injected MessageTooLarge)")
	}

	var ee *EmitError
	if !errors.As(emitErr, &ee) {
		t.Fatalf("Emit err = %v; want *EmitError", emitErr)
	}

	if ee.Class != ClassSerialization {
		t.Errorf("emit.Class = %q; want %q", ee.Class, ClassSerialization)
	}

	// Now consume the DLQ topic.
	dlq := dlqTopic(sourceTopic)

	record := readDLQRecord(t, cluster, dlq, 10*time.Second)
	if record == nil {
		t.Fatalf("no record on DLQ topic %q", dlq)
	}

	// Body byte-equal.
	if string(record.Value) != string(event.Payload) {
		t.Errorf("record.Value = %q; want %q", record.Value, event.Payload)
	}

	// Partition key preserved (tenant ID).
	if string(record.Key) != event.TenantID {
		t.Errorf("record.Key = %q; want %q", record.Key, event.TenantID)
	}

	// All 7 x-lerian-dlq-* headers present.
	if got := headerValue(record, dlqHeaderSourceTopic); got != sourceTopic {
		t.Errorf("%s = %q; want %q", dlqHeaderSourceTopic, got, sourceTopic)
	}
	if got := headerValue(record, dlqHeaderErrorClass); got != string(ClassSerialization) {
		t.Errorf("%s = %q; want %q", dlqHeaderErrorClass, got, ClassSerialization)
	}
	if got := headerValue(record, dlqHeaderErrorMessage); got == "" {
		t.Errorf("%s is empty; want sanitized cause message", dlqHeaderErrorMessage)
	}
	if got := headerValue(record, dlqHeaderRetryCount); got != "0" {
		t.Errorf("%s = %q; want %q (stub)", dlqHeaderRetryCount, got, "0")
	}
	if got := headerValue(record, dlqHeaderFirstFailureAt); got == "" {
		t.Errorf("%s is empty; want RFC3339Nano timestamp", dlqHeaderFirstFailureAt)
	} else if _, parseErr := time.Parse(time.RFC3339Nano, got); parseErr != nil {
		t.Errorf("%s = %q; parse err = %v", dlqHeaderFirstFailureAt, got, parseErr)
	}
	if got := headerValue(record, dlqHeaderProducerID); got == "" {
		t.Errorf("%s is empty; want non-empty producer ID", dlqHeaderProducerID)
	}
	if got := headerValue(record, dlqHeaderTenantID); got != event.TenantID {
		t.Errorf("%s = %q; want %q (tenant convenience header)", dlqHeaderTenantID, got, event.TenantID)
	}
}

// TestProducer_PublishDLQ_CePrefixHeadersPreserved verifies all required
// CloudEvents ce-* headers round-trip through the DLQ unchanged. Optional
// headers (ce-subject, ce-datacontenttype) are included in the check when
// present on the original event so we catch drift in buildCloudEventsHeaders.
func TestProducer_PublishDLQ_CePrefixHeadersPreserved(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.MessageTooLarge.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	event := sampleEvent()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, event); err == nil {
		t.Fatalf("Emit err = nil; want non-nil")
	}

	record := readDLQRecord(t, cluster, dlqTopic(sourceTopic), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record")
	}

	parsed, err := ParseCloudEventsHeaders(record.Headers)
	if err != nil {
		t.Fatalf("ParseCloudEventsHeaders err = %v", err)
	}

	if parsed.ResourceType != event.ResourceType {
		t.Errorf("parsed.ResourceType = %q; want %q", parsed.ResourceType, event.ResourceType)
	}
	if parsed.EventType != event.EventType {
		t.Errorf("parsed.EventType = %q; want %q", parsed.EventType, event.EventType)
	}
	if parsed.TenantID != event.TenantID {
		t.Errorf("parsed.TenantID = %q; want %q", parsed.TenantID, event.TenantID)
	}
	if parsed.Source != event.Source {
		t.Errorf("parsed.Source = %q; want %q", parsed.Source, event.Source)
	}
	if parsed.Subject != event.Subject {
		t.Errorf("parsed.Subject = %q; want %q", parsed.Subject, event.Subject)
	}
}

// TestProducer_PublishDLQ_BodyByteEqual focuses narrowly on the load-bearing
// invariant: record.Value MUST be byte-equal to the original payload. The
// replay-tool contract from TRD §C8 depends on this.
func TestProducer_PublishDLQ_BodyByteEqual(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.MessageTooLarge.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	event := sampleEvent()
	event.Payload = json.RawMessage(`{"foo":"bar","nested":{"n":42},"arr":[1,2,3]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, event); err == nil {
		t.Fatalf("Emit err = nil; want non-nil")
	}

	record := readDLQRecord(t, cluster, dlqTopic(sourceTopic), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record")
	}

	// Byte-equal comparison via stdlib bytes.Equal — the actual comparison
	// is on the raw []byte; string conversion is only for the error message.
	if !bytes.Equal(record.Value, event.Payload) {
		t.Errorf("record.Value = %q; want byte-equal to %q", record.Value, event.Payload)
	}
}

// TestProducer_PublishDLQ_AuthError_RoutesToDLQ exercises a second DLQ-
// routable class. TopicAuthorizationFailed is a deployment-config fault,
// routes immediately (no retries), and MUST land on DLQ.
func TestProducer_PublishDLQ_AuthError_RoutesToDLQ(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.TopicAuthorizationFailed.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	emitErr := emitter.Emit(ctx, sampleEvent())
	if emitErr == nil {
		t.Fatalf("Emit err = nil; want non-nil")
	}

	var ee *EmitError
	if !errors.As(emitErr, &ee) || ee.Class != ClassAuth {
		t.Errorf("Emit err class = %v; want %v", ee, ClassAuth)
	}

	record := readDLQRecord(t, cluster, dlqTopic(sourceTopic), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record for auth_error class")
	}

	if got := headerValue(record, dlqHeaderErrorClass); got != string(ClassAuth) {
		t.Errorf("%s = %q; want %q", dlqHeaderErrorClass, got, ClassAuth)
	}
}

// TestProducer_PublishDLQ_TopicNotFound_RoutesToDLQ covers the
// UnknownTopicOrPartition class. Same shape as the auth test.
func TestProducer_PublishDLQ_TopicNotFound_RoutesToDLQ(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.UnknownTopicOrPartition.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err == nil {
		t.Fatalf("Emit err = nil; want non-nil")
	}

	record := readDLQRecord(t, cluster, dlqTopic(sourceTopic), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record for topic_not_found class")
	}

	if got := headerValue(record, dlqHeaderErrorClass); got != string(ClassTopicNotFound) {
		t.Errorf("%s = %q; want %q", dlqHeaderErrorClass, got, ClassTopicNotFound)
	}
}

// TestProducer_PublishDLQ_BrokerOverloaded_RoutesToDLQ covers PolicyViolation
// which classifies as broker_overloaded. Verifies the path for the third
// DLQ-routable non-retry class.
func TestProducer_PublishDLQ_BrokerOverloaded_RoutesToDLQ(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.PolicyViolation.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err == nil {
		t.Fatalf("Emit err = nil; want non-nil")
	}

	record := readDLQRecord(t, cluster, dlqTopic(sourceTopic), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record for broker_overloaded class")
	}

	if got := headerValue(record, dlqHeaderErrorClass); got != string(ClassBrokerOverloaded) {
		t.Errorf("%s = %q; want %q", dlqHeaderErrorClass, got, ClassBrokerOverloaded)
	}
}

// TestProducer_PublishDLQ_NotRoutable_ContextCanceled proves that a
// caller-canceled ctx produces ClassContextCanceled and DOES NOT route to
// DLQ. Emit still returns an error to the caller.
func TestProducer_PublishDLQ_NotRoutable_ContextCanceled(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	// Cancel immediately so ProduceSync returns context.Canceled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	emitErr := emitter.Emit(ctx, sampleEvent())
	if emitErr == nil {
		t.Fatalf("Emit err = nil; want non-nil (canceled ctx)")
	}

	// The error chain must preserve context.Canceled OR *EmitError with
	// ClassContextCanceled. Either is acceptable per TRD §C9 row for
	// context_canceled.
	var ee *EmitError
	if errors.As(emitErr, &ee) && ee.Class != ClassContextCanceled {
		t.Errorf("emit.Class = %q; want %q", ee.Class, ClassContextCanceled)
	}

	// DLQ must NOT have any record.
	record := readDLQRecord(t, cluster, dlqTopic("lerian.streaming.transaction.created"), 500*time.Millisecond)
	if record != nil {
		t.Errorf("DLQ received a record for canceled ctx; want none")
	}
}

// TestProducer_PublishDLQ_ErrorMessageSanitized: embed a credentialed URL in
// the cause and assert neither the password NOR the user:pass@ construct
// appears in x-lerian-dlq-error-message.
func TestProducer_PublishDLQ_ErrorMessageSanitized(t *testing.T) {
	t.Parallel()

	cfg, cluster := kfakeDLQConfig(t)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	event := sampleEvent()
	event.ApplyDefaults()

	// Craft a cause with an embedded password. classify maps this to
	// broker_unavailable (default bucket), which IS DLQ-routable.
	cause := errors.New("dial failed: sasl://alice:hunter2@broker-1.example.com:9092")

	ctx, cancelCtx := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCtx()

	if err := p.publishDLQ(ctx, event, cause, event.Topic(), 0, time.Now()); err != nil {
		t.Fatalf("publishDLQ err = %v", err)
	}

	record := readDLQRecord(t, cluster, dlqTopic(event.Topic()), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record")
	}

	msg := headerValue(record, dlqHeaderErrorMessage)
	if strings.Contains(msg, "hunter2") {
		t.Errorf("error message leaked password: %q", msg)
	}
	if strings.Contains(msg, "alice:hunter2") {
		t.Errorf("error message leaked user:pass combo: %q", msg)
	}
}

// TestProducer_PublishDLQ_DLQFailure_SurfacesWrappedError covers the sad path:
// when the DLQ topic write itself fails (e.g., DLQ topic doesn't exist with
// auto-create off), the caller receives a wrapped error AND an ERROR log is
// emitted. The strategy: inject errors on BOTH the source topic AND the DLQ
// topic so publishDirect hits the "DLQ also failed" branch.
func TestProducer_PublishDLQ_DLQFailure_SurfacesWrappedError(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	dlq := dlqTopic(sourceTopic)

	// Inject errors on BOTH topics. kfake.ControlKey handlers fire for
	// every request after KeepControl, so one registration covers both.
	cluster.ControlKey(int16(kmsg.Produce), func(req kmsg.Request) (kmsg.Response, error, bool) {
		cluster.KeepControl()

		produceReq, ok := req.(*kmsg.ProduceRequest)
		if !ok {
			return nil, nil, false
		}

		names := resolveRequestTopicNames(cluster, produceReq)

		resp := produceReq.ResponseKind().(*kmsg.ProduceResponse)
		resp.Version = produceReq.Version

		handled := false

		for i, topic := range produceReq.Topics {
			var errCode int16

			switch names[i] {
			case sourceTopic:
				errCode = kerr.MessageTooLarge.Code
			case dlq:
				errCode = kerr.UnknownTopicOrPartition.Code
			default:
				continue
			}

			handled = true

			topicResp := kmsg.NewProduceResponseTopic()
			topicResp.Topic = topic.Topic
			topicResp.TopicID = topic.TopicID

			for _, partition := range topic.Partitions {
				partResp := kmsg.NewProduceResponseTopicPartition()
				partResp.Partition = partition.Partition
				partResp.ErrorCode = errCode
				topicResp.Partitions = append(topicResp.Partitions, partResp)
			}

			resp.Topics = append(resp.Topics, topicResp)
		}

		if !handled {
			return nil, nil, false
		}

		return resp, nil, true
	})

	spy := &spyLogger{}

	emitter, err := New(context.Background(), cfg, WithLogger(spy))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	emitErr := emitter.Emit(ctx, sampleEvent())
	if emitErr == nil {
		t.Fatalf("Emit err = nil; want non-nil (both topics failed)")
	}

	// The error MUST carry both legs: the original MessageTooLarge class
	// and the DLQ publish failure description.
	var ee *EmitError
	if !errors.As(emitErr, &ee) {
		t.Fatalf("Emit err = %v; want *EmitError", emitErr)
	}

	if ee.Class != ClassSerialization {
		t.Errorf("ee.Class = %q; want %q (original classification preserved)",
			ee.Class, ClassSerialization)
	}

	if !strings.Contains(ee.Error(), "DLQ also failed") {
		t.Errorf("ee.Error() = %q; want to contain 'DLQ also failed'", ee.Error())
	}

	// An ERROR-level log entry must have been emitted by publishDLQ.
	entry := spy.firstErrorEntry("DLQ publish failed")
	if entry == nil {
		t.Fatalf("no ERROR log entry for 'DLQ publish failed'; entries=%d", len(spy.entries))
	}

	// T6 standardization (TRD §7.3): source topic is carried in the "topic"
	// field; the dedicated "dlq_topic" field carries the derived DLQ name.
	if got, ok := entry.fields["topic"].(string); !ok || got != sourceTopic {
		t.Errorf("log entry topic = %v; want %q", entry.fields["topic"], sourceTopic)
	}
	if got, ok := entry.fields["dlq_topic"].(string); !ok || got != dlq {
		t.Errorf("log entry dlq_topic = %v; want %q", entry.fields["dlq_topic"], dlq)
	}
	if got, ok := entry.fields["error_class"].(string); !ok || got != string(ClassSerialization) {
		t.Errorf("log entry error_class = %v; want %q", entry.fields["error_class"], ClassSerialization)
	}
}

// TestProducer_PublishDLQ_ProducerIDHeader asserts the producer ID header
// matches the producer instance's own ID — which is a UUID set at construction
// and used elsewhere (circuit-breaker service name).
func TestProducer_PublishDLQ_ProducerIDHeader(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.MessageTooLarge.Code)

	emitter, err := New(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("New err = %v", err)
	}

	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err == nil {
		t.Fatalf("Emit err = nil; want non-nil")
	}

	record := readDLQRecord(t, cluster, dlqTopic(sourceTopic), 10*time.Second)
	if record == nil {
		t.Fatalf("no DLQ record")
	}

	if got := headerValue(record, dlqHeaderProducerID); got != p.producerID {
		t.Errorf("%s = %q; want %q (producer ID)", dlqHeaderProducerID, got, p.producerID)
	}
}
