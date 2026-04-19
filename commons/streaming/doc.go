// Package streaming provides a wrapper harness for publishing Lerian domain
// events to a Redpanda/Kafka cluster using CloudEvents 1.0 binary-mode,
// backed by franz-go, with circuit-breaker + outbox fallback and per-topic
// DLQ.
//
// # Scope
//
// streaming is the producer-only entry point for past-tense domain facts
// intended for external consumers (e.g. "transaction.created"). It is NOT
// for internal command dispatch or work queues — for those, use
// commons/rabbitmq. It is NOT a consumer library — downstream services
// consume with cloudevents/sdk-go/v2 + franz-go directly.
//
// streaming and rabbitmq are orthogonal. Neither deprecates the other.
//
// # Quick start
//
// Bootstrap in main.go:
//
//	cfg, err := streaming.LoadConfig()
//	if err != nil { return err }
//	producer, err := streaming.NewProducer(ctx, cfg,
//	    streaming.WithLogger(logger),
//	    streaming.WithMetricsFactory(metricsFactory),
//	    streaming.WithTracer(tracer),
//	    streaming.WithCircuitBreakerManager(cbManager),
//	    streaming.WithOutboxRepository(outboxRepo),
//	)
//	if err != nil { return err }
//	launcher.Add("streaming", producer)
//	// Inject producer (as streaming.Emitter) into service constructors.
//
// Service method uses the injected Emitter:
//
//	err := emitter.Emit(ctx, streaming.Event{
//	    TenantID:     "t-abc",
//	    ResourceType: "transaction",
//	    EventType:    "created",
//	    Source:       "//lerian.midaz/transaction-service",
//	    Subject:      "tx-123",
//	    Payload:      payloadBytes,
//	})
//
// Unit-test with the mock emitter:
//
//	mock := streaming.NewMockEmitter()
//	svc := NewMyService(mock)
//	svc.DoSomething(ctx)
//	streaming.AssertEventEmitted(t, mock, "transaction", "created")
//
// # Environment variables
//
// All env vars use the STREAMING_ prefix. LoadConfig reads every var
// below, applies defaults, and validates the result. When Enabled is
// false, validation is skipped and New returns a NoopEmitter.
//
//	Variable                             | Type     | Default         | Purpose
//	-------------------------------------|----------|-----------------|---------------------------------------------------------------
//	STREAMING_ENABLED                    | bool     | false           | Master kill switch; when false New returns a NoopEmitter
//	STREAMING_BROKERS                    | csv      | localhost:9092  | Redpanda/Kafka bootstrap list; required when Enabled=true
//	STREAMING_CLIENT_ID                  | string   | ""              | Kafka client.id for broker-side diagnostics
//	STREAMING_BATCH_LINGER_MS            | int      | 5               | franz-go ProducerLinger in ms (pinned across franz-go versions)
//	STREAMING_BATCH_MAX_BYTES            | int      | 1048576         | ProducerBatchMaxBytes (1 MiB)
//	STREAMING_MAX_BUFFERED_RECORDS       | int      | 10000           | Backpressure ceiling for in-flight records
//	STREAMING_COMPRESSION                | string   | lz4             | One of snappy, lz4, zstd, gzip, none
//	STREAMING_RECORD_RETRIES             | int      | 10              | Per-record retry budget inside franz-go
//	STREAMING_RECORD_DELIVERY_TIMEOUT_S  | int(s)   | 30              | Per-record delivery cap in seconds
//	STREAMING_REQUIRED_ACKS              | string   | all             | One of all, leader, none
//	STREAMING_CB_FAILURE_RATIO           | float    | 0.5             | Circuit-breaker trip ratio in [0.0, 1.0]
//	STREAMING_CB_MIN_REQUESTS            | int      | 10              | Minimum observations before the CB evaluates the ratio
//	STREAMING_CB_TIMEOUT_S               | int(s)   | 30              | Open to half-open probe delay in seconds
//	STREAMING_CLOSE_TIMEOUT_S            | int(s)   | 30              | Max drain+flush window on Close in seconds
//	STREAMING_CLOUDEVENTS_SOURCE         | string   | ""              | Default ce-source (required when Enabled=true)
//	STREAMING_EVENT_TOGGLES              | csv      | ""              | "resource.event=bool,..." per-event kill switches
//
// # Error classes and sentinels
//
// Sentinel errors are defined in streaming.go (search for `var (` on that
// file for the authoritative list with full godoc on each). The categories:
//
//   - Caller-side validation (synchronous, no I/O): ErrMissingTenantID,
//     ErrSystemEventsNotAllowed, ErrMissingSource, ErrMissingResourceType,
//     ErrMissingEventType, ErrInvalid{TenantID,ResourceType,EventType,Source,
//     Subject,EventID,SchemaVersion,DataContentType,DataSchema},
//     ErrPayloadTooLarge, ErrNotJSON, ErrEventDisabled, ErrEmitterClosed.
//   - Config validation (LoadConfig): ErrMissingBrokers, ErrMissingSource,
//     ErrInvalidCompression, ErrInvalidAcks.
//   - Lifecycle / wiring (NOT caller errors — see IsCallerError):
//     ErrNilProducer, ErrCircuitOpen, ErrOutboxNotConfigured,
//     ErrNilOutboxRegistry.
//
// Use IsCallerError(err) to distinguish caller-correctable faults from
// infrastructure faults without matching each sentinel individually.
//
// Runtime publish failures surface as *EmitError with one of eight
// ErrorClass values. Per the TRD §C9 retry-and-DLQ table, DLQ routing
// applies to every class except caller-cancel (ClassContextCanceled) and
// caller-validation (ClassValidation):
//
//	Class                   | DLQ routed | Caller-correctable (IsCallerError)
//	------------------------|------------|-----------------------------------
//	ClassSerialization      | yes        | yes
//	ClassValidation         | no         | yes
//	ClassAuth               | yes        | yes (deployment config fault)
//	ClassTopicNotFound      | yes        | no
//	ClassBrokerUnavailable  | yes        | no
//	ClassNetworkTimeout     | yes        | no
//	ClassContextCanceled    | no         | no
//	ClassBrokerOverloaded   | yes        | no
//
// Use IsCallerError(err) to distinguish caller faults from infrastructure
// faults. Caller faults are worth a 4xx-style response; infrastructure
// faults usually warrant a retry or circuit-breaker consultation.
//
// # Lifecycle
//
// *Producer implements commons.App. The consuming service's main.go wires
// it via launcher.Add / launcher.RunApp; the Launcher owns the lifecycle.
// Service methods receive an Emitter via constructor injection and MUST
// NOT call Close — the Launcher does on shutdown.
//
// Close is idempotent: the first call flushes the underlying franz-go
// client under a deadline derived from STREAMING_CLOSE_TIMEOUT_S and
// closes the client; subsequent calls return nil without re-flushing.
// CloseContext honors the caller's ctx on top of the close-timeout
// deadline so Flush cannot hang indefinitely.
//
// After Close, subsequent Emit calls return ErrEmitterClosed synchronously
// before any I/O.
//
// # Consumer responsibilities
//
// Topics are SHARED across tenants. The topic name derives from
// <resource>.<event> only — NOT tenant. Partition keys give per-tenant FIFO
// ordering within a topic but do NOT isolate tenants at the topic level.
//
// Every consumer MUST filter events by ce-tenantid (or Event.TenantID after
// parsing) before dispatching to tenant-scoped business logic. A consumer
// that processes an event without a tenant check has a cross-tenant data
// leak.
//
// This is the single biggest operational invariant of the streaming bus:
// producer-side tenant discipline alone is not sufficient.
//
// # Concurrency safety
//
// *Producer is safe for concurrent use from any number of goroutines.
// Emit batches internally via the underlying *kgo.Client; callers do not
// need to serialize or pool. Internal state uses atomics; there is no
// user-visible mutex.
//
// MockEmitter and NoopEmitter are likewise concurrency-safe.
//
// # Outbox fallback
//
// When the circuit breaker is OPEN and WithOutboxRepository has been
// wired, Emit writes the serialized event to the outbox and returns nil.
// The outbox Dispatcher drains rows back through the handler registered
// via (*Producer).RegisterOutboxHandler — which calls publishDirect, not
// Emit, so replays bypass the breaker and cannot re-enqueue themselves on
// a sustained outage.
//
// Without an outbox wired, circuit-open Emits return ErrCircuitOpen.
//
// # Outbox wire format
//
// The outbox row Payload is a JSON-marshaled streaming.Event using Go's
// default field capitalization (PascalCase: TenantID, ResourceType, ...),
// NOT the snake_case shape referenced in TRD §6.2. The round-trip is
// self-consistent — publishToOutbox and handleOutboxRow both use the same
// json package — but any external consumer (reconciliation tooling, audit
// readers) must be written to the PascalCase shape documented here, not
// to the TRD §6.2 snake_case draft.
//
// # Minimum broker version
//
// Tested against Redpanda v24.2.x (the v24.2.18 image is pinned by the
// integration suite in commons/streaming/integration_test.go). franz-go
// auto-negotiates ApiVersions with the broker, so older Kafka clusters
// may work but are unsupported — consumer services running against Kafka
// <3.0 should validate manually before production rollout.
//
// # Relation to commons/dlq
//
// commons/dlq is a Redis-backed retriable work-item queue with consumer-
// driven dequeue semantics. This package's per-topic Kafka DLQ
// (<source>.dlq) is an immutable, consumer-pull, append-only quarantine
// log for failed event publications. They are orthogonal and not
// substitutes:
//
//   - commons/dlq: work items that need retry with exponential backoff.
//   - streaming Kafka DLQ: events that failed to publish and need forensic
//     analysis or manual replay.
//
// Choose commons/dlq for operational work queues; streaming's DLQ is
// automatic and scoped to publish failures.
//
// Note: x-lerian-dlq-retry-count is currently 0. Do not build tooling
// that relies on non-zero values.
//
// # Tuning for throughput
//
// Default configuration targets low-latency per-event emission. For
// high-throughput workloads (>10k RPS per service), consider:
//
//   - STREAMING_BATCH_LINGER_MS=20..50: allows more records to accumulate
//     per batch, improving compression ratio and broker efficiency. Trades
//     per-event latency for throughput.
//   - STREAMING_MAX_BUFFERED_RECORDS=100000+: raises the in-flight ceiling
//     before Emit back-pressures. Monitor memory proportionally.
//   - STREAMING_COMPRESSION=zstd: better compression ratio than lz4 at
//     higher CPU cost. Prefer lz4 for latency-sensitive paths; zstd for
//     bulk/async paths.
//   - STREAMING_BATCH_MAX_BYTES: keep at 1 MiB unless broker
//     max.message.bytes is raised. Must match broker config.
//
// Benchmark with your actual payload distribution before tuning; defaults
// are safe for <1k RPS.
//
// # Dashboard
//
// Metrics conform to: streaming_emitted_total, streaming_emit_duration_ms,
// streaming_dlq_total, streaming_dlq_publish_failed_total,
// streaming_outbox_routed_total, streaming_circuit_state.
//
// Per-tenant attribution of DLQ or routing spikes is available through
// the span attribute tenant.id, NOT metric labels — tenant is deliberately
// kept off the metric label set to bound cardinality.
package streaming
