//go:build unit

package streaming

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// --- GROUP G: streaming benchmarks. ---
//
// All benchmarks use the Go 1.24+ `for b.Loop()` idiom so the loop body is
// automatically measured from the first warmup iteration onward and the
// compiler cannot hoist work out of the loop.
//
// How to run:
//
//	go test -tags=unit -bench=. -benchmem ./commons/streaming/... -run=^$
//	go test -tags=unit -bench=BenchmarkEmit -benchmem -benchtime=5s ./commons/streaming/ -run=^$
//
// The `-run=^$` filter suppresses regular tests so benchmark output is
// uncluttered. `-benchmem` adds the B/op + allocs/op columns that gate
// allocation regressions.

// kfakeBenchConfig is the *testing.B twin of kfakeConfig. Same kfake cluster
// shape and Config defaults, but accepts testing.TB so the helper serves
// benchmarks. The returned cluster is registered for teardown via b.Cleanup.
func kfakeBenchConfig(tb testing.TB) (Config, *kfake.Cluster) {
	tb.Helper()

	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.AllowAutoTopicCreation(),
		kfake.DefaultNumPartitions(3),
		kfake.SeedTopics(3, "lerian.streaming.transaction.created"),
	)
	if err != nil {
		tb.Fatalf("kfake.NewCluster err = %v", err)
	}

	tb.Cleanup(cluster.Close)

	return Config{
		Enabled:               true,
		Brokers:               cluster.ListenAddrs(),
		ClientID:              "bench-producer",
		BatchLingerMs:         1,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    10_000,
		Compression:           "none",
		RecordRetries:         2,
		RecordDeliveryTimeout: 5 * time.Second,
		RequiredAcks:          "leader",
		CBFailureRatio:        0.5,
		CBMinRequests:         10,
		CBTimeout:             5 * time.Second,
		CloseTimeout:          5 * time.Second,
		CloudEventsSource:     "//bench",
	}, cluster
}

// kfakeBenchDLQConfig adds the DLQ topic alongside the source so DLQ-routed
// benchmarks don't stall on UNKNOWN_TOPIC_OR_PARTITION during the DLQ write.
func kfakeBenchDLQConfig(tb testing.TB) (Config, *kfake.Cluster) {
	tb.Helper()

	source := "lerian.streaming.transaction.created"

	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.AllowAutoTopicCreation(),
		kfake.DefaultNumPartitions(3),
		kfake.SeedTopics(3, source, source+".dlq"),
	)
	if err != nil {
		tb.Fatalf("kfake.NewCluster err = %v", err)
	}

	tb.Cleanup(cluster.Close)

	return Config{
		Enabled:               true,
		Brokers:               cluster.ListenAddrs(),
		ClientID:              "bench-dlq",
		BatchLingerMs:         1,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    10_000,
		Compression:           "none",
		RecordRetries:         0,
		RecordDeliveryTimeout: 5 * time.Second,
		RequiredAcks:          "leader",
		CBFailureRatio:        0.99, // keep breaker CLOSED
		CBMinRequests:         1_000_000,
		CBTimeout:             5 * time.Second,
		CloseTimeout:          5 * time.Second,
		CloudEventsSource:     "//bench",
	}, cluster
}

// BenchmarkEmit_HappyPath measures steady-state Emit cost on a healthy
// broker. Baseline for detecting per-Emit allocation regressions.
func BenchmarkEmit_HappyPath(b *testing.B) {
	b.ReportAllocs()

	cfg, _ := kfakeBenchConfig(b)

	p, err := NewProducer(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		b.Fatalf("NewProducer err = %v", err)
	}
	b.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()
	event := sampleEvent()

	for b.Loop() {
		if err := p.Emit(ctx, event); err != nil {
			b.Fatalf("Emit err = %v", err)
		}
	}
}

// BenchmarkEmit_CircuitOpenOutbox measures the circuit-open + outbox fallback
// branch. Exercises publishToOutbox (JSON marshal + Repo.Create) instead of
// publishDirect. Catches any regression in the outbox-routing hot path.
func BenchmarkEmit_CircuitOpenOutbox(b *testing.B) {
	b.ReportAllocs()

	cfg, _ := kfakeBenchConfig(b)
	repo := &fakeOutboxRepo{}

	p, err := NewProducer(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithOutboxRepository(repo),
	)
	if err != nil {
		b.Fatalf("NewProducer err = %v", err)
	}
	b.Cleanup(func() { _ = p.Close() })

	// Force the mirrored flag OPEN so every Emit takes the outbox branch.
	p.cbStateFlag.Store(flagCBOpen)

	ctx := context.Background()
	event := sampleEvent()

	for b.Loop() {
		if err := p.Emit(ctx, event); err != nil {
			b.Fatalf("Emit err = %v", err)
		}
	}
}

// BenchmarkEmit_DLQRoute measures the hot path when publishDirect fails and
// the error routes to DLQ. kfake is instructed to reject every source-topic
// produce with MessageTooLarge so each Emit runs through classifyError +
// publishDLQ.
func BenchmarkEmit_DLQRoute(b *testing.B) {
	b.ReportAllocs()

	cfg, cluster := kfakeBenchDLQConfig(b)

	injectProduceError(cluster, "lerian.streaming.transaction.created",
		kerr.MessageTooLarge.Code)

	p, err := NewProducer(context.Background(), cfg, WithLogger(log.NewNop()))
	if err != nil {
		b.Fatalf("NewProducer err = %v", err)
	}
	b.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()
	event := sampleEvent()

	for b.Loop() {
		// Every Emit returns a non-nil *EmitError (class=serialization).
		// We discard it — we're measuring the cost, not the shape.
		_ = p.Emit(ctx, event)
	}
}

// BenchmarkClassifyError cycles through a representative set of errors to
// measure the classifier cost. The fastest class (ClassContextCanceled) and
// the slowest (fall-through to ClassBrokerUnavailable) both appear in the
// mix so the bench reports an average over realistic inputs.
func BenchmarkClassifyError(b *testing.B) {
	b.ReportAllocs()

	// A mix of error shapes exercising every resolution branch:
	//   - context.Canceled (fast short-circuit)
	//   - kerr.UnknownTopicOrPartition (sentinel-table match)
	//   - a net.Error timeout (errors.As branch; fakeNetTimeoutErr lives in classify_test.go)
	//   - an unrelated error (default fall-through)
	mix := []error{
		context.Canceled,
		kerr.UnknownTopicOrPartition,
		fakeNetTimeoutErr{},
		errors.New("unrelated opaque error"),
	}

	for b.Loop() {
		for _, e := range mix {
			_ = classifyError(e)
		}
	}
}

// BenchmarkSanitizeBrokerURL exercises both the credential-bearing and
// no-credential paths. The fast path (no "://", no "password=") dominates
// real traffic; the credential path matters for log-fanout scenarios.
func BenchmarkSanitizeBrokerURL(b *testing.B) {
	b.Run("with_credentials", func(b *testing.B) {
		b.ReportAllocs()
		s := "dial sasl://admin:hunter2@broker.example.com:9092 failed: connection refused"
		for b.Loop() {
			_ = sanitizeBrokerURL(s)
		}
	})

	b.Run("no_credentials", func(b *testing.B) {
		b.ReportAllocs()
		s := "ordinary error with no credentials and no URL shape"
		for b.Loop() {
			_ = sanitizeBrokerURL(s)
		}
	})
}

// BenchmarkBuildCloudEventsHeaders measures header assembly for a realistic
// Event. All optional ce-* headers are populated so the benchmark exercises
// the full 11-header path.
func BenchmarkBuildCloudEventsHeaders(b *testing.B) {
	b.ReportAllocs()

	event := sampleEvent()
	(&event).ApplyDefaults()
	event.DataSchema = "https://schemas.lerian.test/tx.json"
	event.Subject = "aggregate-1"

	for b.Loop() {
		_ = buildCloudEventsHeaders(event)
	}
}

// BenchmarkApplyDefaults measures the zero-value fill cost. Each iteration
// needs a fresh zero Event, so the loop allocates one — the benchmark
// captures ApplyDefaults + the Event copy together. Still useful for
// tracking regressions in the uuid.NewV7 path.
func BenchmarkApplyDefaults(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		e := Event{
			ResourceType: "transaction",
			EventType:    "created",
			Source:       "//bench",
		}
		e.ApplyDefaults()
	}
}

// BenchmarkParseCloudEventsHeaders measures header-map parsing for a
// realistic header set. Uses the header slice produced by
// buildCloudEventsHeaders so it mirrors the exact on-wire shape.
func BenchmarkParseCloudEventsHeaders(b *testing.B) {
	b.ReportAllocs()

	event := sampleEvent()
	(&event).ApplyDefaults()
	event.DataSchema = "https://schemas.lerian.test/tx.json"

	headers := buildCloudEventsHeaders(event)

	for b.Loop() {
		if _, err := ParseCloudEventsHeaders(headers); err != nil {
			b.Fatalf("ParseCloudEventsHeaders err = %v", err)
		}
	}
}

// BenchmarkPreFlight measures the preflight validation path alone —
// isolated from franz-go / kfake so allocation regressions in validation
// surface cleanly. BenchmarkEmit_HappyPath's per-op allocs are dominated
// by kfake's in-process broker simulation; this bench measures the
// streaming-code-only cost of the same hot path prefix.
func BenchmarkPreFlight(b *testing.B) {
	b.ReportAllocs()

	// Minimal Producer with no broker — we never exercise publish, so no
	// kgo.Client is needed for this bench. Constructing the struct directly
	// avoids the whole NewProducer / kgo.NewClient cost and leaves the
	// measurement isolated to preflight-touched fields.
	p := &Producer{
		allowSystemEvents: false,
		toggles:           nil,
	}

	event := sampleEvent()
	(&event).ApplyDefaults()

	for b.Loop() {
		if err := p.preFlight(event); err != nil {
			b.Fatalf("preFlight err = %v", err)
		}
	}
}

// BenchmarkEvent_Topic measures the Topic() hot-path string concat + semver
// parse. Isolated from franz-go so the L3 fast-path (SchemaVersion="1.0.0"
// short-circuit) can be verified as a pure win. Compare against runs before
// the fast-path lands to see the delta.
func BenchmarkEvent_Topic(b *testing.B) {
	b.Run("v1_default", func(b *testing.B) {
		b.ReportAllocs()
		event := Event{
			ResourceType:  "transaction",
			EventType:     "created",
			SchemaVersion: "1.0.0",
		}
		for b.Loop() {
			_ = event.Topic()
		}
	})

	b.Run("v2_semver", func(b *testing.B) {
		b.ReportAllocs()
		event := Event{
			ResourceType:  "transaction",
			EventType:     "created",
			SchemaVersion: "2.3.1",
		}
		for b.Loop() {
			_ = event.Topic()
		}
	})

	b.Run("empty_version", func(b *testing.B) {
		b.ReportAllocs()
		event := Event{
			ResourceType: "transaction",
			EventType:    "created",
		}
		for b.Loop() {
			_ = event.Topic()
		}
	})
}

// fakeNetTimeoutErr is declared in classify_test.go (same package). The
// benchmark above reuses it via fakeNetTimeoutErr{}.
//
// Compile-time assertion that the shared double still implements net.Error.
// If classify_test.go ever removes the type, this line fails the build and
// forces the benchmark file to either grow its own equivalent or point to a
// new canonical location.
var _ net.Error = fakeNetTimeoutErr{}
