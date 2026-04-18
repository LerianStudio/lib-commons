# Tasks — `commons/streaming/`

## 1. Metadata

| Field | Value |
|---|---|
| Title | Task breakdown for streaming wrapper harness |
| Feature | `commons/streaming/` |
| Gate | 3 (Task decomposition) |
| Date | 2026-04-18 |
| Upstream docs | [`research.md`](./research.md) · [`prd.md`](./prd.md) · [`ux-criteria.md`](./ux-criteria.md) · [`trd.md`](./trd.md) |
| Module | `github.com/LerianStudio/lib-commons/v5/commons/streaming` |
| Estimation unit | AI-agent-hours (not developer-hours, not story points) |
| Total estimate | **60 AI-agent-hours** across 10 tasks (T11 dashboard stretch excluded) |
| Critical path | **46 AI-agent-hours** (T1 → T2 → T3 → T4 → T5 → T6 → T7 → T9 → T10) |
| Confidence roll-up | 3 High · 5 Medium · 2 Low |
| Task count | 10 (plus one stretch T11) |
| Hard ceiling per task | 16 AI-agent-hours (no task exceeds this) |

All tasks target v5 (`github.com/LerianStudio/lib-commons/v5/commons/streaming`). All tasks ship unit tests alongside the feature. Integration and docs are their own tasks per CLAUDE.md invariants.

---

## 2. Task table (summary)

| ID | Title | AI-hours | Confidence | Blocked by | Files delivered (principal) | Primary AC / DX refs |
|---|---|---|---|---|---|---|
| T1 | Emitter interface, sentinels, EmitError, NoopEmitter, MockEmitter, Config loader | 7 | High | — | `streaming.go`, `event.go`, `options.go`, `config.go`, `noop.go`, `mock.go`, `sanitize.go`, `*_test.go` | AC-01, AC-02, AC-12, AC-13, DX-A01-06, DX-B01-03, DX-C01-05, DX-E03, DX-F05 |
| T2 | franz-go Producer happy path (publishDirect, Close, Healthy, classifier) | 8 | Medium | T1 | `producer.go`, `publish.go`, `classify.go`, `health.go`, `cloudevents.go`, `*_test.go` | AC-05, AC-07, DX-A02, DX-B06, DX-D04 |
| T3 | Circuit breaker integration + state listener + outbox-fallback flag plumbing | 5 | Medium | T2 | `producer.go` (CB wiring), `producer_test.go` (+state listener tests) | AC-03 (CB detection), AC-04, DX-B03, DX-D04 |
| T4 | Outbox integration — circuit-open fallback + Dispatcher handler bridge | 6 | Medium | T3 | `outbox_handler.go`, `publish.go` (publishToOutbox), `outbox_handler_test.go` | AC-03, AC-04 (post-retries DLQ split in T5), DX-B05, DX-E05 |
| T5 | Per-topic DLQ publisher + retry-exhaustion path + classifier → DLQ routing | 5 | Medium | T4 | `publish.go` (publishDLQ), `classify.go` (retry verdicts), `publish_test.go` | AC-04, AC-11, DX-B04, DX-D03 (dlq label) |
| T6 | OTEL metrics (6 instruments, lazy-init, nil-guarded) + `streaming.emit` span + log fields | 6 | Medium | T2 | `metrics.go`, `producer.go` (span wrap), `metrics_test.go` | AC-05, AC-06, DX-D01-03 |
| T7 | `commons.App` integration, paired `X/XContext`, nil guards, close-flush deadline, close semantics | 4 | High | T6 | `producer.go` (Run/RunContext/CloseContext), `producer_test.go` | AC-10, AC-14, DX-A03, DX-E01-05 |
| T8 | `WithTLSConfig` + `WithSASL` options (minimal v1 plumbing to kgo) | 2 | High | T2 | `options.go`, `producer.go`, `options_test.go` | AC-09 (partial), TRD §8 |
| T9 | Integration tests — testcontainers Redpanda, round-trip, DLQ, outbox fallback, CloudEvents SDK contract | 12 | Low | T1-T8 (all) | `integration_test.go`, go.mod updates | AC-08, AC-09, DX-C06 |
| T10 | `doc.go`, `.env.reference`, `CLAUDE.md` streaming section, README one-liner | 5 | High | T1-T8 (can parallel T9) | `doc.go`, `.env.reference`, `CLAUDE.md`, `README.md` | AC-12, AC-15, DX-F01-05 |
| T11 (stretch) | Grafana dashboard JSON | 3 | Low | T6 | `dashboards/streaming-emitter.json` | DX-D05 |

Sum: **60 AI-hours** (T1-T10). T11 stretch: +3 hours.

---

## 3. Per-task detail

### T1 — Public surface + test doubles + env loader

**Description.** Delivers every public type a consuming service needs to depend on, plus both no-op and mock implementations. After T1 merges, any consuming service can import `commons/streaming` and start writing unit tests against `NewMockEmitter()` — even though no real Kafka producer exists yet. `NoopEmitter` covers feature-flag-off paths. `LoadConfig()` reads every `STREAMING_*` env var with validation; the 17-row env table is frozen here.

**Scope (files created).**
- `commons/streaming/streaming.go` — `Emitter` interface (3 methods), `ErrorClass` enum + 8 class constants, 9 sentinel errors (`ErrMissingTenantID`, `ErrMissingSource`, `ErrEmitterClosed`, `ErrEventDisabled`, `ErrPayloadTooLarge`, `ErrNotJSON`, `ErrMissingBrokers`, `ErrInvalidCompression`, `ErrInvalidAcks`), `EmitError` struct with `Error()`+`Unwrap()`, `IsCallerError(err) bool`.
- `commons/streaming/event.go` — `Event` struct + `Topic()` (semver major suffix rule) + `PartitionKey()` (system-event branch) + default helpers (`EventID` uuid.NewV7, `Timestamp`, `SchemaVersion`, `DataContentType`).
- `commons/streaming/options.go` — `EmitterOption` functional-options scaffolding: `WithLogger`, `WithMetricsFactory`, `WithTracer`, `WithCircuitBreakerManager`, `WithPartitionKey`, `WithCloseTimeout` (TLS/SASL live in T8).
- `commons/streaming/config.go` — `Config` struct + `LoadConfig()` env loader using `commons.GetenvOrDefault` / `GetenvBoolOrDefault` / `GetenvIntOrDefault` (research §A4). Broker CSV split inline. All 17 env vars validated.
- `commons/streaming/noop.go` — `NewNoopEmitter() Emitter`.
- `commons/streaming/mock.go` — `MockEmitter` + `NewMockEmitter()` + `Events()` + `SetError` + `Reset` + `Close`/`Healthy` + `AssertEventEmitted` + `AssertEventCount` + `AssertTenantID` + `AssertNoEvents` + `WaitForEvent`. All helpers use `testing.TB` and call `t.Helper()`.
- `commons/streaming/sanitize.go` — `sanitizeBrokerURL(s string) string` (regex mirrored from `rabbitmq/rabbitmq.go:129, 470`).
- Test files: `event_test.go`, `mock_test.go`, `config_test.go`, `sanitize_test.go`, `streaming_test.go`.

**Dependencies.** None (foundation task). No new `go.mod` entries.

**AC / DX references.** AC-01 (Event/Emit shape), AC-02 (ErrMissingTenantID sentinel + `SystemEvent` gate, enforced in T2 producer), AC-12 (env vars), AC-13 (NoopEmitter), DX-A01-06, DX-B01-03, DX-C01-05, DX-E03 (sentinel exists), DX-F05 (every `STREAMING_*` literal enumerated).

**Testing strategy.** Table-driven:
- `Event.Topic()` — rows cover semver 1.x → no suffix, 2.0.0 → `.v2`, invalid semver → base.
- `Event.PartitionKey()` — TenantID, SystemEvent branch, override.
- `IsCallerError` — truth table from TRD §C9 (all 8 classes + 9 sentinels).
- `sanitizeBrokerURL` — `sasl://user:secret@host:9092` → `****` substitution; golden output.
- `MockEmitter` — 1000-goroutine concurrency test under `-race` (DX-A03); deep-copy verification; assertion helpers pass/fail paths.
- `WaitForEvent` — uses `testing/synctest` for deterministic virtual time (DX-C04).
- `LoadConfig` — table over env combos; `ENABLED=true` + empty `BROKERS` → `ErrMissingBrokers`; invalid compression → `ErrInvalidCompression`.

NOT tested here: real Kafka behavior (T2), circuit integration (T3), outbox integration (T4), metrics (T6).

**Estimate.** 7 AI-hours. **Confidence: High.** Every subfile is a well-established pattern (sentinels, mock, env loader); only `WaitForEvent` with `testing/synctest` is novel and small.

**Exit criteria.**
- `go test ./commons/streaming/...` passes with zero build tags.
- `errors.Is(err, ErrMissingTenantID)` and `errors.As(err, &EmitError{})` both exercised in tests.
- `.env.reference` parity linter has a source of truth to check against (even if `.env.reference` itself lands in T10).
- `MockEmitter` captures 1000 concurrent emits with `-race` green.
- `NoopEmitter` — every method test passes.

---

### T2 — Producer happy path (franz-go, classifier, health, CloudEvents headers)

**Description.** Delivers a working `*Producer` that can publish real events to a Kafka broker — as long as the circuit is closed and nothing fails. No circuit breaker wiring yet (T3), no outbox fallback (T4), no DLQ (T5), no metrics (T6), no App integration (T7). The classifier and CloudEvents header builder are shipped here because they're needed on the happy path to produce correct wire-format messages.

**Scope (files created).**
- `commons/streaming/producer.go` — `*Producer` struct, `New(ctx, cfg, opts...)`, `(*Producer).Emit` (direct publish only; returns `ErrEmitterClosed` after close), `Close()`, `Healthy(ctx)`, compile-time `var _ Emitter = (*Producer)(nil)`. Returns `NoopEmitter` when `cfg.Enabled == false` or `len(cfg.Brokers) == 0` (DX-C05 fail-safe).
- `commons/streaming/publish.go` — `publishDirect(ctx, event)` using `kgo.ProduceSync`; pre-flight validation (tenant, source, size, JSON).
- `commons/streaming/classify.go` — `classifyError(err) ErrorClass` with full franz-go `kerr` table (TRD §C9).
- `commons/streaming/health.go` — `HealthError`, `HealthState` enum (Healthy/Degraded/Down), ping-backed `Healthy` impl.
- `commons/streaming/cloudevents.go` — `buildCloudEventsHeaders(Event) []kgo.RecordHeader` + round-trip parser for tests. Uses `semconv/v1.27.0`.
- Test files: `producer_test.go` (kfake-backed), `classify_test.go`, `cloudevents_test.go`.

**Dependencies.** Blocked by T1 (needs Emitter, Event, sentinels). Adds `github.com/twmb/franz-go v1.20.7`, `github.com/twmb/franz-go/pkg/kversion`, `github.com/twmb/franz-go/pkg/kerr`, `github.com/twmb/franz-go/pkg/kfake` (test-only) to `go.mod`.

**AC / DX references.** AC-05 (OTEL span — attribute shape defined here, full wrap in T6), AC-07 (CloudEvents binary mode, every `ce-*` header), DX-A02 (spelled-out Event fields; no Kafka vocab leaks), DX-B06 (credential sanitization in error strings), DX-D04 (Healthy state enum shape). Pre-flight validation covers AC-02's synchronous behavior for ErrMissingTenantID/ErrPayloadTooLarge/ErrNotJSON.

**Testing strategy.**
- kfake-backed unit tests for `publishDirect`: round-trip `ce-*` headers, partition key set, value byte-equal to `event.Payload`.
- Table-driven `classifyError`: one row per kerr (`TopicAuthorizationFailed`, `UnknownTopicOrPartition`, `MessageTooLarge`, `SaslAuthenticationFailed`, `InvalidRecord`, `CorruptMessage`, `ThrottlingQuotaExceeded`, `PolicyViolation`), plus `context.Canceled`, `kgo.ErrRecordTimeout`, `kgo.ErrRecordRetries`, `net.Error` timeout, default.
- Pre-flight: 1 MiB + 1 byte → `ErrPayloadTooLarge`, no broker call; invalid JSON → `ErrNotJSON`; empty tenant + `SystemEvent=false` → `ErrMissingTenantID`.
- Concurrency: 1000 goroutines `Emit` on a kfake producer with `-race` (DX-A03).
- Healthy: force `Ping` failure via fake → `*HealthError{State: Degraded}`; happy path → nil.
- Credential sanitization: fake a connect error with `sasl://user:hunter2@broker`, assert error string contains neither `hunter2` nor `user:hunter2@`.

NOT tested here: circuit-breaker branching, outbox fallback, DLQ headers, App integration.

**Estimate.** 8 AI-hours. **Confidence: Medium.** franz-go API well-documented, but `kfake` quirks and getting partition-key + header round-trip perfect under test is usually the source of an extra hour of fiddling.

**Exit criteria.**
- `Emit` → kfake → produced record has every required `ce-*` header with correct values.
- All 11+ classifier rows green.
- `-race` green with 1000 concurrent `Emit` calls.
- No credential leakage in any error string (automated test).
- `go mod tidy` clean after adding franz-go.

---

### T3 — Circuit breaker integration + state listener

**Description.** Wires the Producer's publish path through `circuitbreaker.Manager.Execute`. Registers a state-change listener that flips `cbStateFlag` (atomic.Int32) on OPEN/CLOSED/HALF-OPEN. Exposes `WithCircuitBreakerManager(m)` so callers can reuse a process-level manager. The outbox-fallback branch reads the flag (not `IsOpen()` — no such method exists per research §A2).

**Scope (files modified).**
- `commons/streaming/producer.go` — CB construction in `New()`: `cbManager.GetOrCreate("streaming.producer:"+producerID, HTTPServiceConfig())`; `RegisterStateChangeListener(...)` runs under `runtime.RecoverAndLog`, writes `cbStateFlag` atomically, emits span event `circuit.state_changed` if active span.
- `commons/streaming/publish.go` — Wrap `publishDirect` in `cb.Execute(func() (any, error) { ... })`. For now, when flag == open, the closure returns `(nil, ErrCircuitOpen)` — full outbox fallback lands in T4.
- `commons/streaming/options.go` — `WithCircuitBreakerManager(m circuitbreaker.Manager) EmitterOption`.
- Test files: extend `producer_test.go`.

**Dependencies.** Blocked by T2.

**AC / DX references.** AC-03 (circuit-open detection; outbox write lands in T4), AC-04 (errors surface as `*EmitError` with class), DX-D04 (state transitions visible).

**Testing strategy.**
- Fake `circuitbreaker.Manager` that lets us force state transitions via a test handle; assert `cbStateFlag` is updated and a log line emitted at correct level.
- Table-driven: breaker closed → publishDirect called; breaker open → publishDirect NOT called, `ErrCircuitOpen` surfaces (temporary surface until T4).
- Race test: 100 goroutines call `Emit` while listener fires state changes 50 times — no data race, no panic.
- Assert listener duration < 1ms (per TRD risk R5): measure wall-clock inside listener.

NOT tested here: outbox write (T4), DLQ routing (T5).

**Estimate.** 5 AI-hours. **Confidence: Medium.** The circuit-breaker Manager API is stable (research §C2). One risk: `Execute`'s closure signature `func() (any, error)` is awkward; getting the typed-nil dance right requires a careful first pass.

**Exit criteria.**
- CB open → `Emit` returns a surfaced error (not panic, not nil); no broker call.
- State listener runs < 1ms and is panic-safe.
- `WithCircuitBreakerManager` option plumbs a caller-supplied manager end-to-end.
- All T2 tests still green.

---

### T4 — Outbox integration (circuit-open fallback + Dispatcher handler bridge)

**Description.** Completes the circuit-open degradation path. When the CB is OPEN, `Emit` serializes the event to an `outbox.OutboxEvent`, writes it via `OutboxRepository.Create` / `CreateWithTx`, and returns `nil` to the caller. Separately, registers a streaming handler with the existing `outbox.Dispatcher` that relays rows back through `publishDirect` (bypassing the CB wrapper so handler retries don't themselves feed the outbox — loop prevention).

**Scope (files created/modified).**
- `commons/streaming/publish.go` — `publishToOutbox(ctx, event)`: JSON-marshal, pick `CreateWithTx` when ambient `*sql.Tx` is on context, else `Create`. On repo write failure, surface original error (no silent loss).
- `commons/streaming/outbox_handler.go` — `(*Producer).RegisterOutboxHandler(registry *outbox.HandlerRegistry) error`. Handler pattern: unmarshal payload → `Event` → `p.publishDirect(ctx, event)`. Event-type prefix filter `lerian.streaming.`.
- `commons/streaming/producer.go` — Inside `cb.Execute` closure: when `cbStateFlag == flagOpen` → call `publishToOutbox`, return `(nil, nil)` so `Execute` sees success (CB untouched). Record `streaming_outbox_routed_total{reason="circuit_open"}` (metric actually defined in T6; stub here, label the TODO in a comment).
- Test files: `outbox_handler_test.go`, extend `producer_test.go`.

**Dependencies.** Blocked by T3. Uses existing `commons/outbox` types (no changes needed there — research §C3 confirmed).

**AC / DX references.** AC-03 (main acceptance — circuit open → outbox → caller sees nil), DX-B05 (circuit-open returns nil).

**Testing strategy.**
- Fake `OutboxRepository` captures `Create` calls; drive CB to OPEN via T3's fake; assert `Emit` returns nil and fake repository shows one `OutboxEvent` with `EventType = "lerian.streaming.transaction.created"`, `Payload = <JSON bytes of streaming.Event>`, `Status = PENDING`.
- Fake `HandlerRegistry`: `RegisterOutboxHandler` inserts handler; invoke handler with synthetic `*OutboxEvent` → asserts `publishDirect` was called (NOT `Emit` — loop would happen).
- Outbox write failure path: repo returns error → `Emit` returns wrapped error (no silent drop), metric tag `outcome=caller_error`.
- Ambient tx path: context with `*sql.Tx` in well-known key → `CreateWithTx` used (via fake that distinguishes).

NOT tested here: what the Dispatcher does after `PUBLISHED` / `FAILED` — that's existing outbox behavior.

**Estimate.** 6 AI-hours. **Confidence: Medium.** Outbox interface is stable; main uncertainty is the handler-registry API ergonomics and making sure the loop-prevention bypass is airtight.

**Exit criteria.**
- CB-open → outbox row written → `Emit` returns nil.
- Registered handler calls `publishDirect`, never `Emit`.
- Outbox repo failure does NOT swallow (returns error or routes DLQ per policy — here: returns caller-visible error).
- Handler-registry wiring documented in `doc.go` (deferred to T10).

---

### T5 — Per-topic DLQ + retry exhaustion

**Description.** When franz-go returns `kgo.ErrRecordRetries` or `kgo.ErrRecordTimeout`, the Producer classifies the original cause and publishes the message to a per-topic DLQ (`{source_topic}.dlq`) with `x-lerian-dlq-*` headers. Original `ce-*` headers preserved verbatim; body unchanged. DLQ publish does NOT fall back to outbox (prevents cascade). If DLQ write itself fails: ERROR log + `streaming_dlq_publish_failed_total` counter (metric stub; defined in T6) + wrapped original error returned to caller.

**Scope (files modified).**
- `commons/streaming/publish.go` — `publishDLQ(ctx, event, cause, retryCount)` function; callers wire it into the post-retries branch of `publishDirect`.
- `commons/streaming/classify.go` — ensure classes map correctly to "DLQ yes / DLQ no" per TRD §C9 retry table (validation_error → return to caller; serialization_error → DLQ immediately; etc.).
- `commons/streaming/publish_test.go` (new) or extend `producer_test.go`.

**Dependencies.** Blocked by T4 (so that circuit-open path doesn't get mixed up with retry-exhaust path during tests).

**AC / DX references.** AC-04 (8 classes distinguishable, retry behavior per class), AC-11 (per-topic DLQ + all 6 headers), DX-B04 (per-class retry + DLQ policy), DX-D03 (`streaming_dlq_total{topic,error_class}` — metric shape; actual instrument in T6).

**Testing strategy.**
- kfake-backed: force `kerr.MessageTooLarge` on source topic → `publishDLQ` called → DLQ topic receives message with all 6 `x-lerian-dlq-*` headers and original `ce-*` headers intact.
- Table: one test per DLQ-routable class (serialization_error, auth_error, topic_not_found, broker_unavailable, network_timeout, broker_overloaded) asserting routing and headers.
- `validation_error`, `context_canceled` → NOT routed to DLQ (returned to caller).
- DLQ publish failure: kfake returns error on DLQ topic → original error wrapped, `streaming_dlq_publish_failed_total` increment (stub), ERROR log emitted.
- Body unchanged: assert DLQ message value byte-equal to original payload.

NOT tested here: end-to-end against real Redpanda (T9).

**Estimate.** 5 AI-hours. **Confidence: Medium.** DLQ header construction is mechanical; the harder part is forcing the right kfake errors in a deterministic way.

**Exit criteria.**
- All 6 `x-lerian-dlq-*` headers present and typed correctly on DLQ messages.
- `{topic}.dlq` naming derived correctly.
- DLQ failure path logs + counts + returns wrapped error; does NOT loop back into outbox.
- Original payload and `ce-*` headers preserved byte-for-byte.

---

### T6 — OTEL metrics + spans + log field conventions

**Description.** Full observability suite: 6 metric instruments (lazy-init, nil-guarded, mirroring `circuitbreaker/manager.go:85-103`), `streaming.emit` span with every attribute from TRD §7.2, log field conventions from TRD §7.3. `tenant_id` on spans only (never metrics — DX-D02). `outcome` label has 5 values: `produced`, `outboxed`, `circuit_open`, `caller_error`, `dlq` (Gate 2 addition).

**Scope (files created/modified).**
- `commons/streaming/metrics.go` — `streamingMetrics` struct; `newStreamingMetrics(factory *metrics.MetricsFactory, logger log.Logger) *streamingMetrics`. 6 instruments:
  1. `streaming_emitted_total` (Counter, labels: topic/operation/outcome)
  2. `streaming_emit_duration_ms` (Histogram, labels: topic/outcome)
  3. `streaming_dlq_total` (Counter, labels: topic/error_class)
  4. `streaming_dlq_publish_failed_total` (Counter, labels: topic)
  5. `streaming_outbox_routed_total` (Counter, labels: topic/reason)
  6. `streaming_circuit_state` (UpDownCounter, no labels)
  (Also: `streaming_outbox_depth` gauge — callback-driven; defer if outbox doesn't expose a read side, note as v1.1.)
- `commons/streaming/producer.go` — wrap `Emit` in `tracer.Start(ctx, "streaming.emit", trace.WithSpanKind(trace.SpanKindProducer))`; defer `span.End()`. Attributes per TRD §7.2. `messaging.kafka.message.key` only emitted when debug level enabled.
- `commons/streaming/publish.go` — each terminal branch records metrics + span `event.outcome`.
- Log fields per TRD §7.3 — standardize through `p.logger.Log(ctx, level, msg, log.String(...), ...)`.
- Test files: `metrics_test.go`, extend `producer_test.go` with span-exporter spy.

**Dependencies.** Blocked by T2 (spans wrap publish). Can run in parallel with T3-T5 once T2 merges; here sequenced after T5 so the `dlq` outcome label is testable against a real DLQ emit path.

**AC / DX references.** AC-05 (every span attribute), AC-06 (label cardinality, no tenant_id on metrics), DX-D01 (exactly one span), DX-D02 (no tenant_id in metric labels — automated test), DX-D03 (all 6 counters + gauge + state).

**Testing strategy.**
- In-memory OTEL span exporter (SDK provides one): emit 1 event → exactly 1 span named `streaming.emit` with all required attributes.
- Metric recorder spy via `metric.NewManualReader` or `NewNopFactory` swap: emit → counter++ with correct labels.
- **Automated cardinality test:** iterate all metric attribute sets across 10k synthetic emits with distinct tenant_ids; assert `tenant_id` appears 0 times in any metric attribute set.
- Nil-factory safety: `WithMetricsFactory(nil)` → no panic, first Emit logs a warning once, subsequent Emits silent.
- Log field test: capture log lines via fake logger; assert every required standard field present on at least one representative emission.

NOT tested here: Grafana panel queries (T11).

**Estimate.** 6 AI-hours. **Confidence: Medium.** Pattern is well-established (circuitbreaker/manager.go), but the span attribute discipline and the label-cardinality automated test require care.

**Exit criteria.**
- 6 instruments registered lazily, nil-safe.
- Span exporter shows exactly 1 `streaming.emit` span per emit with complete attribute set.
- Automated test confirms `tenant_id` never on any metric attribute set.
- `outcome` label covers all 5 values across test matrix.

---

### T7 — `commons.App` integration + paired methods + close semantics

**Description.** Makes `*Producer` implement `commons.App` so consuming services wire via `launcher.Add("streaming", producer)`. Adds paired `Close()/CloseContext(ctx)`, `Healthy(ctx)` nil-guards, `WithCloseTimeout(d)` option (default 30s). `Close` is idempotent, flushes the franz-go producer, drains in-flight outbox writes, respects deadline. Post-close `Emit` returns `ErrEmitterClosed` synchronously before any I/O.

**Scope (files modified).**
- `commons/streaming/producer.go`:
  - `(*Producer).Run(launcher *commons.Launcher) error`
  - `(*Producer).RunContext(ctx context.Context, launcher *commons.Launcher) error`
  - `(*Producer).Close() error` (calls `CloseContext(context.Background())`)
  - `(*Producer).CloseContext(ctx context.Context) error` — `closed.CompareAndSwap(false,true)`; `kgo.Client.Flush(ctxWithDeadline)`; `kgo.Client.Close()`.
  - `var _ commons.App = (*Producer)(nil)` assertion.
  - Nil-receiver guards on every method (mirror `rabbitmq/rabbitmq.go:147-152`).
- `commons/streaming/options.go` — `WithCloseTimeout(d time.Duration) EmitterOption`.
- Test files: extend `producer_test.go`.

**Dependencies.** Blocked by T6 (so close can record a final metric flush if needed). Could in theory parallel T6 but sequencing keeps `Healthy` + state interactions testable end-to-end.

**AC / DX references.** AC-10 (Healthy plugs into HealthWithDependencies), AC-14 (Close idempotent, post-close Emit returns ErrEmitterClosed), DX-A03 (concurrency doc sentence), DX-E01-05 (lifecycle semantics).

**Testing strategy.**
- Launcher smoke test: `launcher := &commons.Launcher{...}; launcher.Add("streaming", producer); go launcher.Run()`; trigger shutdown; assert no goroutine leak (`goleak.VerifyNone`).
- Idempotent close: call `Close()` 3 times → all return nil.
- Post-close Emit: close, then `Emit` → `errors.Is(err, ErrEmitterClosed)` and zero broker calls (kfake counter at zero).
- Close flush: enqueue 10 buffered records with `kfake`; `Close` with 5s deadline → all 10 flushed; or deadline hit → remaining count logged.
- Nil receiver: `var p *Producer; p.Emit(ctx, e)` → assertion-level error (mirrors `ErrNilConnection`).
- `Healthy` plugs into `net/http.HealthWithDependencies` — compile-time function-signature test.

**Estimate.** 4 AI-hours. **Confidence: High.** Pattern directly mirrors `commons/outbox/dispatcher.go:134-139` and `commons/rabbitmq/rabbitmq.go:155-157, 511-569`. Low novelty.

**Exit criteria.**
- `var _ commons.App = (*Producer)(nil)` compiles.
- 3x `Close()` → 3x nil.
- Post-close `Emit` zero I/O.
- Launcher integration passes `goleak.VerifyNone`.
- Nil-receiver guard on every exported method.

---

### T8 — `WithTLSConfig` + `WithSASL` options (v1 minimal)

**Description.** Plumbs `WithTLSConfig(*tls.Config)` and `WithSASL(sasl.Mechanism)` options through to `kgo.DialTLSConfig(cfg)` and `kgo.SASL(m)`. Gate 2 locked this as a v1 ship (minimal implementation acceptable; avoids a later breaking change). No full SASL SCRAM/OAUTHBEARER integration tests in v1.

**Scope (files modified).**
- `commons/streaming/options.go` — two new `EmitterOption` functions.
- `commons/streaming/producer.go` — thread options into the `[]kgo.Opt` slice passed to `kgo.NewClient`.
- `commons/streaming/options_test.go` (new) — plumbing tests.

**Dependencies.** Blocked by T2. Can run in parallel with T3-T7 once T2 merges.

**AC / DX references.** AC-09 (integration path), TRD §8 security architecture.

**Testing strategy.**
- Unit: pass a `*tls.Config` with a distinctive ServerName → capture `[]kgo.Opt` via indirection (or construct a Producer and inspect a test-injected record of options) → assert the TLS opt is present.
- Unit: pass a stub `sasl.Mechanism` with a known Name → assert the SASL opt passed through.
- NOT tested in v1: real TLS handshake, real SCRAM/OAUTHBEARER auth. Flag in godoc as "minimal v1 plumbing; integration tests arrive with first SASL-using consumer."

**Estimate.** 2 AI-hours. **Confidence: High.** Pure plumbing. Only risk is figuring out how to inspect the assembled `[]kgo.Opt` slice from a test without reflection — a small interface seam suffices.

**Exit criteria.**
- Both options defined + exported + godoc'd.
- Plumbing tests green.
- No behavioral regression on TLS-less / SASL-less path.

---

### T9 — Integration tests (testcontainers Redpanda + CloudEvents contract)

**Description.** The biggest single task. Spins up real Redpanda via `testcontainers-go/modules/redpanda` v0.41.0, produces events end-to-end, consumes them, validates CloudEvents headers with the reference `cloudevents/sdk-go/v2` SDK (contract test per PRD R3). Covers: round-trip produce+consume with all `ce-*` headers preserved, partition-key FIFO across 5 tenants × 1000 events, DLQ routing under `kerr.MessageTooLarge`, outbox fallback under broker partition (stop container mid-run; verify outbox rows grow; restart; verify drain).

**Scope (files created).**
- `commons/streaming/integration_test.go` — `//go:build integration`, `TestIntegration_*` naming, helper `startRedpanda(t)` returns `(brokerAddr, cleanup)`.
- `go.mod` additions: `github.com/testcontainers/testcontainers-go/modules/redpanda v0.41.0` (test-only), `github.com/cloudevents/sdk-go/v2 v2.15.2` (test-only).

**Dependencies.** Blocked by T1-T8. Can run in parallel with T10 (docs) once T8 finishes.

**AC / DX references.** AC-08 (unit tests no-tags pass — verified by every prior task), AC-09 (integration tests with build tag + real Redpanda), DX-C06 (`testcontainers-go/modules/redpanda v0.41.0`).

**Testing strategy.**
- `TestIntegration_RoundTripHeaders` — emit 1 event → consume from source topic → assert every CloudEvents header round-trips; decode via `cloudevents.Event` SDK; assert `event.ID()`, `event.Type()`, etc. match.
- `TestIntegration_PartitionFIFO` — 5 tenants × 200 events each; consume per partition; within each partition, assert event-sequence-number monotonically increases. This validates `StickyKeyPartitioner(KafkaHasher(nil))` behaves per research §D3.
- `TestIntegration_DLQRouting` — synthesize a payload that exceeds `broker.max.message.bytes` via broker config override; emit → consume from `{topic}.dlq`; assert headers.
- `TestIntegration_OutboxFallbackUnderPartition` — start Redpanda; stop it (`container.Stop`); emit events; assert `OutboxRepository.ListPending` has the events; restart container; run Dispatcher tick; assert drain. Toxiproxy-based partition is optional; stop/start is deterministic enough for v1. Flag Toxiproxy-based variant as a stretch.
- `TestIntegration_CloudEventsSDKContract` — produce 1 event; consume; parse with `cloudevents/sdk-go/v2`; assert zero parse errors; assert `Validate()` on the resulting Event returns nil.
- Use `log.NewNop()` in tests per research §C10. Use `redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v24.2.x", ...)` — NOT deprecated `RunContainer` (research §A7).

**Estimate.** 12 AI-hours. **Confidence: Low.** Integration tests against real infrastructure always surprise. Risk breakdown in §7.

**Exit criteria.**
- `make test-integration` green on branch.
- 5 integration test functions, all passing.
- `go mod tidy` clean after test-only deps.
- CI time budget under 3 minutes total for streaming package.

---

### T10 — Docs, `.env.reference`, `CLAUDE.md`, README

**Description.** Everything non-code that is required for v1 ship. Hard project rule (CLAUDE.md): `.env.reference` MUST be updated in the same PR that introduces env vars. Package godoc MUST include 10-line end-to-end example (compiles under `go test -run Example`), env-var table, error table (8 classes + 9 sentinels), pointer to Grafana dashboard (or TBD if T11 slips).

**Scope (files modified/created).**
- `commons/streaming/doc.go` — 10-line example (bootstrap → `launcher.Add` → inject into service → call `Emit` → swap `MockEmitter` in test); scope statement distinguishing streaming (past-tense external) from RabbitMQ (internal commands) in line 1; env-var table (17 rows); error class + sentinel tables; concurrency-safety note ("safe for concurrent use from multiple goroutines" — DX-A03); lifecycle warning ("service methods must not call Close" — DX-E01).
- `.env.reference` — 17 `STREAMING_*` rows with defaults and one-line purposes.
- `CLAUDE.md` — new "Streaming (`commons/streaming`)" section under "API invariants to respect" summarizing: `Emitter` interface (3 methods), `Event` shape, sentinel errors, scope boundary vs `commons/rabbitmq`.
- `README.md` — one-liner in the package listing: "`commons/streaming`: CloudEvents-framed domain event publishing to Redpanda with circuit-breaker + outbox + per-topic DLQ."

**Dependencies.** Blocked by T1-T8 (docs describe final public surface). Can run in parallel with T9.

**AC / DX references.** AC-12 (.env.reference parity — CI linter runs on this), AC-15 (every exported symbol has godoc), DX-F01-05 (all 5 discoverability criteria).

**Testing strategy.**
- `go test -run Example ./commons/streaming/...` compiles and runs the example without error.
- CI linter (new, lightweight): scan `commons/streaming/*.go` for every `"STREAMING_*"` string literal; grep `.env.reference` for each; fail on mismatch. Add this linter to `make check-envs` (already a target per CLAUDE.md).
- Manual read-the-godoc-cold check: given only `go doc github.com/LerianStudio/lib-commons/v5/commons/streaming`, a first-time reader can write a working Emit in 5 minutes.

**Estimate.** 5 AI-hours. **Confidence: High.** All three documents have clear templates from existing packages.

**Exit criteria.**
- Example test passes.
- `.env.reference` parity linter green.
- CLAUDE.md Streaming section adjacent in style to the existing RabbitMQ / outbox / idempotency sections.
- `go doc` output is self-explanatory.

---

### T11 (stretch) — Grafana dashboard JSON

**Description.** Authors `dashboards/streaming-emitter.json` covering emitted rate, DLQ rate, outbox depth, circuit state, p95 emit latency, per-partition throughput (for hot-tenant detection per PRD R2). Flag: may slip to v1.1 per TRD §11.

**Scope.** `commons/streaming/dashboards/streaming-emitter.json`.

**Dependencies.** Blocked by T6 (final metric names).

**AC / DX references.** DX-D05.

**Testing strategy.** Manual: import into Grafana 10+, verify panels render without errors. No automated test.

**Estimate.** 3 AI-hours. **Confidence: Low.** Dashboard authoring is hit-or-miss; time depends on Grafana version quirks.

**Exit criteria.** JSON validates; imports cleanly; six panels render.

---

## 4. Dependency graph

```
T1 (foundation)
 │
 ├──► T2 (franz-go producer) ──┬──► T3 (CB wiring) ──► T4 (outbox) ──► T5 (DLQ) ──► T6 (metrics+spans) ──► T7 (App+close) ──┐
 │                              │                                                                                           │
 │                              └──► T8 (TLS/SASL — parallel w/ T3-T7)                                                      │
 │                                                                                                                           ▼
 └───────────────────────────────────────────────────────────────────────────────────────────────────────────────► T9 (integration tests)
                                                                                                                             │
                                                                                                                             │  (T10 parallel w/ T9 once T8 done)
                                                                                                                             ▼
                                                                                                                          T10 (docs + .env.reference + CLAUDE.md)

T11 (stretch) depends on T6; can run any time after.
```

**Critical path (AI-hours):** T1 (7) → T2 (8) → T3 (5) → T4 (6) → T5 (5) → T6 (6) → T7 (4) → T9 (12) → (T10 parallel) → **critical path = 53 hours** if T10 is fully sequenced after T9. Treating T10 as parallel-with-T9 (per §5 below) lowers the critical path to **46 hours** (T9 is still the tail since 12 > 5).

T8 at 2 hours is off the critical path entirely — it parallels T3/T4/T5 once T2 merges.

---

## 5. Parallelization opportunities

| Window | Parallel tracks |
|---|---|
| After T2 merges | T3 (sequential toward T9) **and** T8 (TLS/SASL) in parallel — T8 takes 2h and unblocks nothing downstream, so it can slot in anywhere. |
| After T6 merges | T11 (dashboard) can start even though T7/T9/T10 are still running. |
| After T8 merges | T9 (integration) and T10 (docs) in parallel. T10 is docs + `.env.reference` + CLAUDE.md — no code touched; T9 touches no docs. Merge conflict risk near-zero. |

**Sequential-required chain:** T1 → T2 → T3 → T4 → T5 → T6 → T7. Each step depends on internals of the prior step and would cause merge headaches if parallelized.

**Practical AI-agent scheduling.** If one agent carries the sequential chain (T1-T7 = 41h) and a second agent picks up T8 (2h) and later T10 (5h), the wall-clock per agent is T1-T7 + T9 = 53h (agent A) and T8 + T10 = 7h (agent B). Add T11 = 3h to agent B (total 10h) with idle slack. Per §6 this is the shape Gate 4 converts to calendar days.

---

## 6. Total estimates

| Metric | Value |
|---|---|
| Sum AI-agent-hours (T1-T10) | **60 hours** |
| Sum including T11 stretch | 63 hours |
| Critical-path AI-hours (T10 parallel w/ T9) | **46 hours** |
| Sequential baseline (if no parallelism) | 60 hours |
| Task count | 10 (+1 stretch) |
| Confidence: High | 3 (T1, T7, T8, T10 — 4) |
| Confidence: Medium | 5 (T2, T3, T4, T5, T6) |
| Confidence: Low | 2 (T9, T11) |
| Weighted confidence | ~0.70 (High=1.0, Medium=0.75, Low=0.5; weighted by hours) |

Corrected confidence count: **High = 4 (T1, T7, T8, T10); Medium = 5 (T2, T3, T4, T5, T6); Low = 2 (T9, T11).**

Gate 4 formula input: `ai_estimate = 60`, critical path `= 46`. Multipliers and team-size divisions are Gate 4's call.

---

## 7. Risks & unknowns (task-specific)

### T9 — Integration tests (Low confidence)

| Risk | Potential inflation |
|---|---|
| Redpanda container cold-start on first CI run ≠ local — image pull adds minutes in constrained CI | +2h debugging CI-specific timing (pin `wait.ForLog(...).WithStartupTimeout(90*time.Second)`). |
| `testcontainers-go/modules/redpanda` v0.41.0 API quirks vs v0.33 docs we researched against | +2h API-alignment fiddling. |
| Toxiproxy-based broker-partition test requires network wiring we don't have out of box | Flagged as stretch; falls back to container stop/start. 0h inflation if we accept the downgrade; +3h if stretch pursued. |
| CloudEvents SDK v2.15.2 strict mode rejects one of our header choices we haven't anticipated | +2h round-trip-naming fixes. |
| Flaky CI: partition-FIFO test racy under constrained CI CPU | +2h test hardening (increase per-event spacing, retry loop with bounded attempts). |
| **Worst-case total for T9** | **+9h** → 21h instead of 12h. Would breach the 16h cap, so we would split T9 into T9a (round-trip + FIFO + CloudEvents contract) and T9b (DLQ + outbox fallback). Flag for Gate 4 if budget doubles. |

### T11 — Dashboard (Low confidence, stretch)

| Risk | Potential inflation |
|---|---|
| Grafana 10 vs 11 variable syntax divergence | +1h. |
| Panel queries on histograms require precomputed `_bucket` series that don't exist until metrics flow | +1h — requires a sample Redpanda environment to author against, which we may not have. |
| **Worst-case** | +2h → 5h total. Still small; we would just slip it to v1.1 rather than split. |

### Tasks flagged as potentially doubling

**T9 is the only task where worst-case inflation approaches 1.75x.** If every risk in T9 fires simultaneously (container start issues + API quirks + CloudEvents mismatch + flaky FIFO), the estimate could grow from 12h to 21h — not quite doubling, but close. We would split T9 into T9a + T9b rather than breach 16h per task. No other task has risks that could double its estimate.

No task exceeds the 16h cap at baseline. T9 at 12h has the most headroom-consuming risks; all others have < 2h risk inflation.

---

## 8. Testing coverage map

| Criterion | Primary test location |
|---|---|
| AC-01 (one-line emit) | T10 (godoc example compiles) |
| AC-02 (ErrMissingTenantID synchronous) | T1 (sentinel), T2 (pre-flight enforcement) |
| AC-03 (circuit-open → outbox → nil) | T3 (CB detection), T4 (outbox write), T9 (integration) |
| AC-04 (8 classes, `errors.Is`/`errors.As`, retry behavior) | T1 (IsCallerError truth table), T2 (classifier), T5 (retry+DLQ) |
| AC-05 (single span, modern semconv) | T6 (span exporter spy), T9 (integration) |
| AC-06 (bounded metric labels, no tenant_id) | T6 (automated cardinality test) |
| AC-07 (CloudEvents binary mode, all headers) | T2 (cloudevents_test.go), T9 (SDK contract) |
| AC-08 (unit tests pass without tags / Docker) | T1, T2, T3, T4, T5, T6, T7, T8 (every unit test task) |
| AC-09 (integration tests with build tag against real Redpanda) | T9 |
| AC-10 (Healthy plugs into HealthWithDependencies) | T2 (Healthy impl), T7 (signature compatibility test) |
| AC-11 (per-topic DLQ, 6 headers) | T5 (unit), T9 (integration) |
| AC-12 (.env.reference parity) | T10 (linter), T1 (env loader source of truth) |
| AC-13 (NoopEmitter first-class) | T1 |
| AC-14 (Close idempotent, post-close Emit) | T7 |
| AC-15 (godoc coverage) | T10 (docstring review) |
| DX-A01-06 | T1, T2 |
| DX-B01-06 | T1 (sentinels), T2 (credential sanitization) |
| DX-C01-06 | T1 (mock/noop/helpers), T9 (integration w/ containers) |
| DX-D01-04 | T6 (spans/metrics/log fields), T2 (Healthy enum) |
| DX-D05 (dashboard JSON) | T11 (stretch) |
| DX-E01-05 | T7 |
| DX-F01-04 | T10 |
| DX-F05 (.env.reference parity) | T10 (linter) |

**Flag:** No AC or DX criterion is unmapped. DX-D05 sits on the stretch task T11; if T11 slips to v1.1 the criterion slips with it.

---

## 9. Gate 4 inputs

| Input | Value |
|---|---|
| Total AI-agent-hours | 60 (T1-T10); 63 with T11 stretch |
| Critical path AI-hours | 46 (T1→T2→T3→T4→T5→T6→T7→T9, T10 parallel w/ T9) |
| Task count | 10 |
| Dependency graph | §4 above; linear T1→T7 then T9 tail, T8 + T10 parallel slots |
| Parallelizable hours | T8 (2h) + T10 (5h) + T11 (3h stretch) = 7-10h off the critical path |
| Confidence roll-up | High: 4 tasks (18h), Medium: 5 tasks (30h), Low: 2 tasks (15h including stretch) |
| Weighted confidence | ~0.70 |
| Recommended team size for Gate 4 formula | 1 agent for the sequential chain (T1-T7), 1 agent for parallel slots (T8/T10/T11) → effective team_size = 1.2-1.5 depending on how Gate 4 scores agent overlap |
| Worst-case inflation risk | T9 up to +9h (would split into T9a/T9b if breached) |

Gate 4 will plug these into `(ai_estimate × multiplier / 0.90) / 8 / team_size = calendar_days`. With `ai_estimate=60`, `multiplier=1.15` (modest, since 8 of 10 tasks are High/Medium confidence), `team_size=1.3`: `(60 × 1.15 / 0.90) / 8 / 1.3 ≈ 7.4 calendar days`. Within the PRD §11 "2-4 AI-agent days" working estimate if `team_size=2.0` (two agents in full parallel), landing at ~4.8 days. Gate 4's call.
