# Research — `commons/streaming/` feature

**Gate:** 0 (Research)
**Feature:** Streaming wrapper harness — single instrumentation point for domain event publishing to Redpanda across all Lerian products.
**Date:** 2026-04-18
**Mode:** greenfield (new package) with strong modification context (integrates with 6+ existing lib-commons packages).

---

## Executive summary

Four parallel research agents (repo patterns, external best practices, framework docs, developer-experience) produced findings that fall into three buckets:

1. **Spec-vs-reality API mismatches** — the provided spec references public APIs that either do not exist in lib-commons today or have different shapes. Every one of these is a blocker and must be corrected in the TRD (Gate 2).
2. **External best-practice refinements** — choices in the spec that are defensible but not optimal (partitioner default, DLQ topology, error taxonomy, semconv naming, compression codec).
3. **DX criteria for Gate 1 (`ux-criteria.md`)** — 15 testable developer-experience acceptance criteria derived from the two-persona research.

No research agent raised existential concerns with the feature's premise (streaming wrapper is the right pattern, franz-go is the right library, circuit-breaker + outbox is the right degradation strategy). **The feature is greenlit for Gate 1; decisions below must be locked in before TRD.**

---

## Section A — CRITICAL spec-vs-reality mismatches

Each row is a merge-blocker if left unfixed. Full evidence in Section C (Codebase Findings).

| # | Spec assumption | Actual lib-commons API | Fix location |
|---|---|---|---|
| A1 | `commons.GetTenantID(ctx) (string, bool)` | `tenant-manager/core.GetTenantIDContext(ctx) string` (empty string on miss, no `ok` flag) — `commons/tenant-manager/core/context.go:36-48` | TRD + code: import `tenant-manager/core`; treat `""` as miss |
| A2 | `circuitbreaker.CircuitBreaker` with `.IsOpen()` / `.RecordSuccess()` / `.RecordFailure()` | `Manager.GetOrCreate(name, Config) (CircuitBreaker, error)`; `CircuitBreaker.Execute(fn func() (any, error)) (any, error)`; no manual Record*. State changes tracked via `Manager.RegisterStateChangeListener(listener)` — `commons/circuitbreaker/types.go:28-57` | TRD: use `Execute()`-wrapped publish; state listener drives outbox-fallback flag |
| A3 | `outbox.Store` / `outbox.Entry` / `FetchPending` / `MarkDelivered` | `outbox.OutboxRepository` / `outbox.OutboxEvent` / `ListPending` / `MarkPublished` / `MarkFailed`; payload must be ≤1 MiB and valid JSON — `commons/outbox/event.go:24-35, 75-81`, `commons/outbox/repository.go:20-33` | TRD + code: adapt relay to use existing interface; register as outbox handler rather than inventing a sidecar relay |
| A4 | `environment.GetBool` / `GetString` / `GetInt` / `GetStringSlice` | `commons.GetenvBoolOrDefault(key, bool) bool`, `commons.GetenvIntOrDefault(key, int64) int64` (NB: returns `int64`), `commons.GetenvOrDefault(key, string) string`. **No `GetStringSlice`** — `commons/os.go:19-68` | TRD + code: use actual helpers; split broker CSV inline |
| A5 | `*zap.Logger` in public constructors | **CLAUDE.md invariant violation.** lib-commons uses `commons/log.Logger` interface in every production constructor (circuitbreaker, dlq, outbox, rabbitmq, webhook). Zap is imported only inside `commons/zap/` adapter package | TRD + code: constructors take `log.Logger`; `log.NewNop()` fallback on nil |
| A6 | Add `InitStreaming` method to a central `App` struct | No such struct. `commons/app.go` defines an `App` **interface** (one method: `Run(*Launcher) error`) and a `Launcher` registry. Components like `*outbox.Dispatcher` implement `App` and register themselves via `RunApp("name", component)` | TRD: `*streaming.Producer` implements `commons.App`; consumer services wire via `launcher.Add("streaming", producer)` |
| A7 | `testcontainers.modules/redpanda.RunContainer(ctx, opts...)` | Deprecated. Current API: `redpanda.Run(ctx, img string, opts ...ContainerCustomizer) (*Container, error)` with image as positional arg. Broker access: `container.KafkaSeedBroker(ctx) (string, error)` | Code: integration test uses `Run(ctx, img, opts...)` shape |
| A8 | `kgo.EnableIdempotent()` option | Does not exist. Idempotency is **on by default** in franz-go. Only `DisableIdempotentWrite()` exists (do NOT call it) | TRD + code: omit idempotency option; requires `AllISRAcks()` which spec already has |
| A9 | `kversion.V3_0()` | Actual name: `kversion.V3_0_0()`; relevant constants are `V3_6_0`, `V3_7_0`, `V3_8_0`, `V3_9_0`, `V4_0_0`, `V4_1_0`, `Stable()`, `Tip()` | Code: use `kversion.V3_7_0()` as a Redpanda-safe floor |
| A10 | semconv `messaging.operation` with value `"publish"` | Deprecated attribute + deprecated value. Current: `messaging.operation.type` with value `"send"`. `messaging.kafka.message.key` is still valid | Code: pin `go.opentelemetry.io/otel/semconv/v1.27.0` (project-compatible) and use modern attr names |

**Additional non-blocking but important corrections:**

| Finding | Impact |
|---|---|
| franz-go default `ProducerLinger` flipped **0ms → 10ms in v1.20.0** | Spec says 5ms default. If we pin franz-go v1.20+, set `ProducerLinger(5*time.Millisecond)` explicitly. |
| franz-go default partitioner is `StickyPartitioner()` — round-robins regardless of key, breaks per-tenant FIFO | Must explicitly set `RecordPartitioner(kgo.StickyKeyPartitioner(kgo.KafkaHasher(nil)))` — Java-compat murmur2, preserves per-key ordering |
| `commons/dlq` is Redis-backed; spec wants a Redpanda-topic DLQ | These are DIFFERENT DLQ patterns. Reusing `FailedMessage` struct shape for protocol consistency is fine; the transport is a Redpanda topic (this package's own implementation) |

---

## Section B — Personas & DX criteria

### Persona P1 — Service Engineer (Maya)

Builds midaz/matcher/etc. Ships 3-8 PRs/week. Wants to emit an event in ≤3 application lines, unit-test without Docker, read one godoc and ship.

**Top pain points with the prior pattern (`commons/rabbitmq/publisher.go`):**
- 944 LOC, 15+ sentinel errors, 4 health states — great for operators, terrifying for first-time authors.
- 6-arg `Publish(ctx, exchange, routingKey, mandatory, immediate, amqp.Publishing{...})` with AMQP-domain-specific booleans.
- No blessed mock — every service rolls its own interface or falls to integration tests.

**Success signals:**
- Typing `streaming.` in an IDE produces a 3-method interface, not a 30-symbol surface.
- `errors.Is(err, streaming.ErrMissingTenantID)` tells the story; the error message names the missing field.
- Unit tests import `streaming/mock` and pass `go test ./...` (no tags, no containers).

### Persona P2 — Platform / SRE Engineer (Thiago)

Owns lib-commons, on-call. Doesn't care how pretty `Emit` is. Cares that: partition → degrade to outbox, events never silently drop, Grafana + span attributes show which service + tenant are impacted.

**Top pain points with prior generations:**
- Every service built its own `STREAMING_*` env namespace (or `KAFKA_BROKERS` vs `BROKER_HOST`).
- No opinionated "default Grafana panel."
- `Close` semantics underdocumented in early libs — who calls it? Once? Twice safe?

**Success signals:**
- Env-var table in `.env.reference`.
- `dashboards/streaming-emitter.json` ships in-repo.
- `Healthy(ctx) error` plugs into `net/http.HealthWithDependencies` without glue.

### 15 DX acceptance criteria (drafted for Gate 1 `ux-criteria.md`)

Each is falsifiable via code review or a test. Summary (full detail stays in `ux-criteria.md`):

| # | Criterion (condensed) |
|---|---|
| DX-01 | One-line emit from a service method, using `Event` literal, no other boilerplate |
| DX-02 | Unit tests using `NewMockEmitter()` pass with `go test ./...` — no build tags, no Docker |
| DX-03 | `ErrMissingTenantID` returned synchronously before any I/O when `TenantID==""` and `SystemEvent==false` |
| DX-04 | `ErrCircuitOpen` path persists to outbox AND returns `nil` to caller |
| DX-05 | `Emit` safe to call from ≥1000 concurrent goroutines; godoc states this in first sentence |
| DX-06 | `streaming.IsCallerError(err) bool` — caller vs infra fault is one function call |
| DX-07 | `ConfigFromEnv()` reads every `STREAMING_*` var; missing `BROKERS` returns config error, not panic |
| DX-08 | `Healthy(ctx) error` returns typed state (healthy/degraded/down); plugs into `HealthWithDependencies` |
| DX-09 | Every `Emit` records an OTEL span `streaming.emit` with standardized attributes + `outcome` |
| DX-10 | Counters: `streaming_emitted_total`, `streaming_dlq_total`, `streaming_outbox_depth` (gauge), `streaming_circuit_state` (gauge) |
| DX-11 | `NewNoopEmitter()` returns fully functional no-op Emitter (swallows emits, `Healthy==nil`) |
| DX-12 | `Close` is idempotent; post-close `Emit` returns `ErrEmitterClosed` without I/O |
| DX-13 | Payload > 1 MiB rejected synchronously as `ErrPayloadTooLarge` (aligned with `outbox.DefaultMaxPayloadBytes`) |
| DX-14 | `streaming.WaitForEvent(t, ctx, matcher, timeout) Event` test helper for async flows |
| DX-15 | Package godoc: 10-line end-to-end example, env-var table, error table, Grafana link |

### Open DX decisions requiring user input before Gate 1

1. **Error metadata shape.** Sentinels are enough for *routing* (`errors.Is`) but not for *context* (which tenant? which event?). Recommend typed `EmitError struct { ResourceType, EventType, TenantID string; Cause error }` with `errors.Is`/`errors.As` support. [Decision needed]
2. **`SystemEvent` flag.** Spec mentions it; Gate 1 must lock how it's surfaced (bool on Event struct — recommended).
3. **Envelope auto-population.** Should `Emit` auto-populate `EventID` (UUID v7 — time-ordered, better for partitioned storage), `Timestamp`, `SchemaVersion` default? Recommend: yes, auto-populate when zero-valued.
4. **Close semantics.** Idempotent + flushes outbox + bounded deadline (`WithCloseTimeout(d)`)? Recommend: yes.
5. **DLQ topology.** Spec: single global `lerian.streaming.dlq`. Research strongly recommends **per-topic `{topic}.dlq`** (isolation, retention tuning, ACLs, replay ergonomics). [Decision needed — see Section D]

---

## Section C — Codebase findings (lib-commons)

Full file:line references from the repo-research-analyst.

### C1 — Tenant context

- Location: `commons/tenant-manager/core/context.go:36-48`
- API: `ContextWithTenantID(ctx, id) context.Context`; `GetTenantIDContext(ctx) string` (empty string on miss, nil-safe via `nonNilContext`).
- Compatibility shim at `commons/outbox/tenant.go:39-52` (`ContextWithTenantID` with trim), `:80-103` (`TenantIDFromContext` returns ok flag). Outbox's own key is deprecated for v3.0 removal.
- **Streaming must import `github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core`** and treat `""` as the miss case.

### C2 — Circuit breaker

- `commons/circuitbreaker/types.go:28-57` — Manager and CircuitBreaker interfaces.
- `commons/circuitbreaker/manager.go:62-83` — `NewManager(logger log.Logger, opts ...ManagerOption) (Manager, error)` (errors if logger is nil).
- Presets: `DefaultConfig`, `AggressiveConfig`, `ConservativeConfig`, `HTTPServiceConfig` (10s timeout, 5 consecutive failures, 0.5 failure ratio over min 10 requests), `DatabaseConfig` — none Kafka-specific. **`HTTPServiceConfig` is the closest preset** for a Kafka producer.
- Execution: `CircuitBreaker.Execute(fn func() (any, error)) (any, error)` — no context arg. **Context must be captured in the closure.**
- State changes: `Manager.RegisterStateChangeListener(listener)` with 10s listener timeout (`manager.go:21, 377-381`).
- `HealthChecker` primitive exists (`types.go:167-182`) if streaming wants active liveness probes.

### C3 — Outbox

- Exists at `commons/outbox/`. Shape:
  ```go
  type OutboxEvent struct {
      ID          uuid.UUID
      EventType   string
      AggregateID uuid.UUID
      Payload     []byte         // must be valid JSON, ≤1 MiB
      Status      string         // PENDING | PROCESSING | PUBLISHED | FAILED | INVALID
      Attempts    int
      PublishedAt *time.Time
      LastError   string
      CreatedAt, UpdatedAt time.Time
  }
  ```
- `OutboxRepository` interface (`commons/outbox/repository.go:20-33`) — `Create` / `CreateWithTx` / `ListPending` / `ListPendingByType` / `ListTenants` / `GetByID` / `MarkPublished` / `MarkFailed` / `ListFailedForRetry` / `ResetForRetry` / `ResetStuckProcessing` / `MarkInvalid`.
- Dispatcher pattern: `*outbox.Dispatcher` implements `libCommons.App` (`commons/outbox/dispatcher.go:57`). Handler registry dispatches by `EventType`.
- **Streaming integration strategy:** register a streaming-topic handler with the existing Dispatcher — do NOT reinvent a second relay. One dispatcher, many handlers.

### C4 — Environment

- `commons/os.go:19-68` — `GetenvOrDefault`, `GetenvBoolOrDefault`, `GetenvIntOrDefault` (returns `int64`).
- `commons.SetConfigFromEnvVars(s any) error` at `:127-171` — reflection-driven struct loader using `env:` tags (bool/int/string only; no slice support).
- **No `GetStringSlice`.** Streaming parses broker CSV inline: `strings.Split(commons.GetenvOrDefault("STREAMING_BROKERS", "localhost:9092"), ",")`.

### C5 — OTEL metrics

- `commons/opentelemetry/metrics/metrics.go:19-101` — `NewMetricsFactory(meter, logger) (*MetricsFactory, error)`.
- Builders: `.Counter(Metric)`, `.Gauge(Metric)`, `.Histogram(Metric)` each returning `(*XBuilder, error)`. Lazy cached via `sync.Map`. Terminal `.Add()` / `.Set()` / `.Record()` return errors.
- `NewNopFactory()` (`:105-110`) for tests and disabled metrics.
- **Pattern to mirror:** `commons/circuitbreaker/manager.go:85-103` — lazy init, log-and-continue on nil factory, nil-guard every record helper (`:417-449`). Producer-side instrumentation at `commons/rabbitmq/rabbitmq.go:33-37` uses package-level `metrics.Metric` vars lazily constructed during connect.

### C6 — Logger abstraction

- `commons/log.Logger` interface: `Log(ctx, level, msg, fields...)`, `With(fields...)`, `WithGroup(name)`, `Enabled(level)`, `Sync(ctx)`.
- `commons/log.NewNop()` for test/disabled use.
- Grep for `"go.uber.org/zap"` imports: **only** `commons/zap/zap.go`, `commons/zap/injector.go`, `commons/zap/zap_test.go`. Zero production code outside the adapter imports zap directly. CLAUDE.md explicitly codifies: *"Use the structured log interface (`Log(ctx, level, msg, fields...)`) — do not add printf-style methods."*
- **Streaming constructors take `log.Logger`, not `*zap.Logger`.** Full stop.

### C7 — RabbitMQ patterns to mirror

Worth copying almost verbatim:
- **Connection struct with RWMutex + snapshot methods** (`rabbitmq/rabbitmq.go:40-98, 185-202`) — read state without blocking on mutations.
- **Paired `X()` + `XContext(ctx)` methods** (`:155-157, 511-569`) — ergonomic adoption path for callers who can't always thread a context.
- **Reconnect rate-limiting** via `commons/backoff` with cap at 30s (`:95-98, 102-103, 456-467`).
- **Nil-receiver assertions** via `commons/assert` + `ErrNilConnection` (`:147-152`).
- **Sanitized credentials** in error strings via regex that strips passwords from broker URLs (`:129, 470`) — streaming MUST do this too; bootstrap URLs leak SASL creds into logs.
- **Publisher health state enum** (`publisher.go:61-81`): `Connected`/`Reconnecting`/`Degraded`/`Disconnected` — recommend same four states for streaming producer.

### C8 — DLQ

- `commons/dlq/handler.go:36-51` — `FailedMessage` struct with `Source`, `OriginalData []byte`, `ErrorMessage`, `RetryCount`, `MaxRetries`, `CreatedAt`, `NextRetryAt`, `TenantID`.
- Redis-backed (keys: `<prefix><tenantID>:<source>`, RPush/LPop). Exponential backoff AWS Full Jitter, base 30s, floor 5s.
- **Streaming's DLQ is a Redpanda topic, not Redis.** Keep the `FailedMessage` JSON shape for protocol consistency (ops tools written for Redis DLQ keep working), but transport is a Kafka topic.

### C9 — App / Launcher

- `commons/app.go:34-36` — `App` is an interface (`Run(*Launcher) error`).
- `Launcher` (`app.go:75-81`) holds `map[string]App`, runs each via `runtime.SafeGoWithContextAndComponent`.
- Example: `var _ libCommons.App = (*Dispatcher)(nil)` at `commons/outbox/dispatcher.go:57`.
- **No `InitStreaming` on any central App.** Wiring is per-service: `launcher.Add("streaming-producer", producer)` in each consuming service's `main.go`.

### C10 — testcontainers convention

- 8 packages use testcontainers. Pattern exemplar: `commons/rabbitmq/rabbitmq_integration_test.go`.
- Build tag `//go:build integration` (bare, no legacy `// +build` form).
- Test name `TestIntegration_<Name>`.
- Container helper returns `(endpoint, cleanup)`; cleanup via `defer` or `t.Cleanup` (both coexist).
- `wait.ForLog(...).WithStartupTimeout(60s)`.
- Logger: `log.NewNop()` in tests.

### C11 — Module versioning

- `go.mod:1` — `module github.com/LerianStudio/lib-commons/v5`; `go.mod:3` — `go 1.25.9`.
- All internal imports use `/v5/commons/...` prefix. Streaming must as well.

### C12 — Existing streaming/Kafka code

- **None.** Branch is `feat/adding-streaming` but no `commons/streaming/` directory, no franz-go imports, no Kafka/Redpanda references in code. Greenfield. "Streaming-adjacent" mentions of MongoDB change streams in `commons/systemplane/` are unrelated.

---

## Section D — External best practices & framework findings

### D1 — Client library

**Decision: `github.com/twmb/franz-go` at v1.20.7 (or latest 1.20.x).** Alternatives:
- `IBM/sarama`: ~2.4x slower produce, 1.5x slower consume. No EOS transactions.
- `segmentio/kafka-go`: simpler API, 2-6x slower consume.
- `confluent-kafka-go`: **disqualified** — CGO dependency; `CGO_ENABLED=0` impossible; cross-compile hostile (issue #119 documents this).

Redpanda itself uses franz-go in Redpanda Connect. No abandonment signals (active commits, small open PR backlog). **High confidence.**

### D2 — Producer config defaults (post-research)

| Option | Recommended | Rationale (why it differs from spec) |
|---|---|---|
| `RequiredAcks` | `AllISRAcks()` | Spec-correct. Required for ledger/audit-grade events. Confirm broker has `min.insync.replicas=2`, `replication.factor=3`. |
| Idempotency | **Leave default ON; do NOT call `DisableIdempotentWrite()`** | Spec is wrong about `EnableIdempotent()` — that function doesn't exist. |
| `RecordRetries` | **10** | Spec doesn't set this explicitly. Transient leader elections should auto-recover. |
| `RecordDeliveryTimeout` | **30s** | Spec doesn't set this. Hard cap per record; prevents unbounded stalling. |
| `ProducerBatchMaxBytes` | **1 MiB** (1_048_576) | Spec says 16 KiB — too small for bulk ingestion paths. Align with broker's `max.message.bytes=1 MiB`. |
| `ProducerLinger` | **5 ms** | Spec-correct for low-latency. Must be **explicit** since v1.20+ default flipped to 10ms. |
| Compression | **LZ4 primary, Snappy fallback** | Spec defaults Snappy. Both work; LZ4 is slightly better throughput at similar CPU per Redpanda docs. Caller-overridable. |
| `MaxBufferedRecords` | **10_000** | Spec doesn't set. Backpressure boundary before Produce blocks. |
| `RecordPartitioner` | `StickyKeyPartitioner(KafkaHasher(nil))` | Spec-correct, but flag: franz-go's **default** `StickyPartitioner()` (no key arg) breaks per-key FIFO. Must set explicitly. |
| `kversion.MaxVersions` | `V3_7_0()` | Spec says `V3_0()` — wrong name (no underscores). `V3_7_0()` is a safe floor for modern Redpanda (v23.3+). |

### D3 — Partition key (per-tenant FIFO)

- **Recommended key:** composite `tenant_id|aggregate_id` for high-skew tenants, or plain `tenant_id` if tenants are balanced.
- **Hot-tenant mitigation:** document salting (`tenantID|0..K-1`) as an escape hatch — loses strict FIFO per tenant but spreads load.
- **Monitoring:** per-partition throughput dashboards; alert when coefficient of variation > 0.5.
- **Hash choice:** `KafkaHasher(nil)` = murmur2 = matches Java default → cross-language consumer interop preserved.

### D4 — Schema evolution

Spec's semver-with-major-suffix approach is reasonable for MVP without a Schema Registry. **Recommended augmentations:**
1. Check JSON Schema files into `commons/streaming/schemas/{resource_type}/{event_type}.v1.json` for producer-side reference.
2. Contract tests in CI: consumer repos run golden-payload tests against producer schemas.
3. Consumer-first rollout doctrine: deploy v2 consumer BEFORE producer emits v2.
4. Future: Redpanda's built-in Schema Registry is API-compatible with Confluent's — add as `WithSchemaValidator()` option later.

**CloudEvents consideration:** Using CloudEvents field naming + binary mode (context attributes as Kafka headers) preserves future Dapr/Knative/EventBridge interop at near-zero cost. Flag for **product decision** — if midaz already ships events with a custom envelope, the conversion cost may exceed the benefit.

### D5 — DLQ topology — **RECOMMEND CHANGING SPEC**

Spec: single global `lerian.streaming.dlq`.
Recommendation: **per-source-topic `{topic}.dlq`** (e.g., `lerian.streaming.transaction.created.dlq`).

Reasons:
- **Isolation:** noisy DLQ producer doesn't impact other topics' triage.
- **Retention tuning:** different event types need different DLQ retention (small tx events: 30d; bulk payloads: 24h).
- **Replay ergonomics:** mirror-named DLQs trivially replay back to source; global DLQ needs a header-based router.
- **ACLs:** domain A's team doesn't need read perms on domain B's failures.
- **Schema variety:** global DLQ mixes schemas → every DLQ consumer must handle all payload types.

Exception: if DLQ rate is ≤ 1 msg/sec cluster-wide and a centralized ops team inspects every DLQ manually, a single `*.dlq` is acceptable.

### D6 — Error classification taxonomy — **RECOMMEND EXPANDING SPEC**

Spec has 3 classes (`timeout`, `canceled`, `publish_error`). Research recommends 8:

| Class | Retry? | Route |
|---|---|---|
| `serialization_error` | **No** | DLQ immediately (payload bug, won't heal) |
| `validation_error` | **No** | DLQ immediately (tenant missing, schema unknown) |
| `auth_error` | **No** | DLQ + alert (config issue) |
| `topic_not_found` | **No** | DLQ + alert (config issue or auto-create disabled) |
| `broker_unavailable` | **Yes** (exp backoff 100ms→10s, 5 retries) | Then DLQ |
| `network_timeout` | **Yes** (exp backoff 100ms→5s, 3 retries) | Then DLQ |
| `context_canceled` | **No** | Drop or DLQ with `cancellation_reason` header |
| `broker_overloaded` | **Yes** (longer backoff 1s→30s, 3 retries) | Then DLQ |

franz-go error mapping:
- `kerr.TopicAuthorizationFailed`, `kerr.ClusterAuthorizationFailed` → `auth_error`
- `kerr.UnknownTopicOrPartition` → `topic_not_found`
- `kerr.MessageTooLarge`, `kerr.InvalidRecord` → `serialization_error`
- `context.Canceled`, `context.DeadlineExceeded` → `context_canceled`
- `net.Error` + `.Timeout()` → `network_timeout`
- `kgo.ErrRecordTimeout`, `kgo.ErrRecordRetries` → post-retry exhaustion → DLQ
- Default: `broker_unavailable`

Metric label `error_class` uses these 8 values — bounded cardinality, safe for Prometheus.

### D7 — OTEL instrumentation corrections

| Spec | Actual | Action |
|---|---|---|
| `messaging.operation` = `"publish"` | **Deprecated attr + deprecated value.** Current: `messaging.operation.type` = `"send"` | Use modern naming (`semconv/v1.27.0+`) |
| `messaging.kafka.message.key` | Still valid | Keep |
| Metric attributes include `tenant_id` | **Cardinality bomb** — tenant count is unbounded | **Remove `tenant_id` from metric labels.** Keep it on spans (spans are sampled, per-span cardinality is fine). Use OTEL exemplars to attach tenant_id to histogram buckets if drill-down is needed. |

Span kind `trace.SpanKindProducer` is correct. `tracer.Start` signature is correct.

### D8 — Testing strategy

Dual-track:
- **Unit tests:** `github.com/twmb/franz-go/pkg/kfake` — in-process Kafka fake, ~ms startup, no Docker. Covers 80%+ of cases. Transactions not supported (WIP).
- **Integration tests:** `testcontainers-go/modules/redpanda` — real Redpanda binary, real replication. Build tag `integration`. Pin testcontainers version to repo's existing `v0.41.0` (don't accidentally bump the whole family to v0.42).

Image tag: spec pins `redpandadata/redpanda:v23.3.5` (stale but functional). Recommend `docker.redpanda.com/redpandadata/redpanda:v24.2.x` or latest approved internal tag.

### D9 — Version drift summary

| Dependency | Spec pin | Latest (Apr 2026) | Upgrade verdict |
|---|---|---|---|
| `github.com/twmb/franz-go` | v1.17.0 | v1.20.7 | **Safe** — one behavior change (linger default 0→10ms); pass 5ms explicitly |
| `testcontainers-go` | v0.33.0 | v0.41.0 in repo / v0.42.0 latest | **Use v0.41.0 to match repo** |
| `testcontainers-go/modules/redpanda` | v0.33.0 | v0.41.0 | Add at v0.41.0 |
| `github.com/google/uuid` | v1.6.0 | v1.6.0 | No change (still current) — consider `uuid.NewV7()` for time-ordered IDs |
| `golang.org/x/mod` | v0.21.0 | v0.23.0+ | **Safe** — no API changes affecting `IsValid`/`Major` |
| `otel` core | (project v1.42.0) | v1.43.0 | Patch-level, safe |
| `otel/semconv` | not explicit | v1.27.0 stable / v1.40.0 current | Pin v1.27.0 for conservative, v1.37.0+ for modern messaging constants |

### D10 — Go 1.25 features we can use

- `sync.WaitGroup.Go(func())` — cleaner concurrent flush-on-shutdown.
- `testing/synctest` — deterministic concurrency tests with virtualized time; **very useful** for testing retry/flush timing without real clocks.
- Auto `GOMAXPROCS` from cgroup limits — transparent in containers.
- Avoid `encoding/json/v2` (experimental `GOEXPERIMENT=jsonv2`) in library code.

---

## Section E — Decisions that must be locked before Gate 2 (TRD)

Listed for explicit user approval at Gate 0 exit. Each is annotated with research-agent recommendation.

| # | Decision | Recommendation | Spec difference? |
|---|---|---|---|
| E1 | Logger type in public APIs | `commons/log.Logger`, not `*zap.Logger` | **Yes — spec violates CLAUDE.md** |
| E2 | Circuit breaker integration shape | Wrap publish in `CircuitBreaker.Execute(fn)`; register `StateChangeListener` to flip outbox-fallback flag | **Yes — spec's `IsOpen()`/`RecordSuccess()`/`RecordFailure()` API doesn't exist** |
| E3 | Outbox integration | Register a streaming handler with the existing `outbox.Dispatcher`; do NOT build `OutboxRelay` sidecar | **Yes — spec's Store/FetchPending/MarkDelivered API doesn't exist** |
| E4 | App bootstrap | `*streaming.Producer` implements `commons.App`; wired via `launcher.Add("streaming", producer)` in each service's `main.go` | **Yes — no central `InitStreaming` exists in `commons/app.go`** |
| E5 | Config loading | Use `commons.GetenvOrDefault`/`GetenvBoolOrDefault`/`GetenvIntOrDefault`; split broker CSV inline; optionally use `commons.SetConfigFromEnvVars` reflection loader | **Yes — no `environment.GetStringSlice` exists** |
| E6 | Tenant extraction | Import `tenant-manager/core`; treat empty string as miss | **Yes — no `commons.GetTenantID` exists** |
| E7 | DLQ topology | Per-topic `{topic}.dlq` instead of global `lerian.streaming.dlq` | **Recommend changing spec** |
| E8 | Error taxonomy | 8 classes (serialization/validation/auth/topic_not_found/broker_unavailable/network_timeout/context_canceled/broker_overloaded) | **Recommend expanding from spec's 3** |
| E9 | Partitioner | Explicit `StickyKeyPartitioner(KafkaHasher(nil))`; composite `tenant_id|aggregate_id` key when callers supply it | **Clarify — spec is implicitly correct but the default franz-go partitioner silently breaks FIFO** |
| E10 | Metric cardinality | `tenant_id` on spans yes; on metrics labels **no** (use exemplars if needed) | **Yes — spec has `tenant_id` as a metric attr** |
| E11 | semconv messaging | `messaging.operation.type` = `"send"` (modern); import `semconv/v1.27.0` | **Yes — spec uses deprecated attr + value** |
| E12 | Idempotency | Leave default ON; do NOT call `DisableIdempotentWrite`; requires `AllISRAcks` (already spec'd) | **Correction — spec's `EnableIdempotent()` doesn't exist** |
| E13 | Version pins | franz-go v1.20.7, testcontainers v0.41.0 (match repo), semconv v1.27.0, x/mod v0.23.0, kversion `V3_7_0()` | **Yes — spec versions are stale** |
| E14 | Testing strategy | `kfake` for unit (no Docker); `testcontainers-go/modules/redpanda` for integration (build tag) | **Augment spec** |
| E15 | `NewNoopEmitter()` as a first-class constructor | Yes — mirrors `log.NewNop()`, `metrics.NewNopFactory()` | **Recommend elevating** |
| E16 | Typed `EmitError` wrapping sentinels with `TenantID/ResourceType/EventType/Cause` | Yes — supports both `errors.Is` (routing) and `errors.As` (context) | **New decision** |
| E17 | `uuid.NewV7()` for EventID auto-populate (time-ordered IDs) | Optional — recommended for future-proofing consumer indexes | **New decision** |
| E18 | CloudEvents envelope compatibility | Deferred — requires product-level decision; proposed as a follow-up PR | **Flag for later** |
| E19 | Health state enum (Connected/Reconnecting/Degraded/Disconnected) mirroring rabbitmq publisher | Yes — consistent operator mental model | **Augment spec** |
| E20 | Grafana dashboard JSON shipped in `commons/streaming/dashboards/` | Recommended — stretch for Gate 2 | **New deliverable** |

---

## Section F — Pattern-to-copy reference (lib-commons internal)

Code patterns the streaming package should copy verbatim (file:line references):

1. **OTEL metric registration with nil-guards** — `commons/circuitbreaker/manager.go:85-103` (init) + `:417-449` (record).
2. **Producer lifecycle & health** — `commons/rabbitmq/publisher.go:61-196` — `HealthState` enum, `ChannelProvider`, `HealthCallback`, auto-recovery with capped attempts.
3. **Connection resilience** — `commons/rabbitmq/rabbitmq.go` — paired `X()`/`XContext()`, RWMutex snapshots, `nilConnectionAssert`, sanitized error strings.
4. **App integration** — `commons/outbox/dispatcher.go:57, 134-139` — compile-time `App` assertion + `Run(launcher)` → `RunContext(ctx, launcher)` delegation.
5. **Integration tests** — `commons/rabbitmq/rabbitmq_integration_test.go:1-80` — build tag, helper shape, `TestIntegration_` prefix, 60s startup wait.
6. **Nil-safe constructors** — `commons/dlq/handler.go:54-62` (returns nil on nil conn), `commons/webhook/deliverer.go` (nil receiver guards).

---

## Appendix — Source citations (external)

- franz-go: [pkg.go.dev/github.com/twmb/franz-go/pkg/kgo](https://pkg.go.dev/github.com/twmb/franz-go/pkg/kgo), [CHANGELOG](https://github.com/twmb/franz-go/blob/master/CHANGELOG.md), [producing-and-consuming docs](https://github.com/twmb/franz-go/blob/master/docs/producing-and-consuming.md)
- testcontainers-go redpanda: [pkg.go.dev/.../modules/redpanda](https://pkg.go.dev/github.com/testcontainers/testcontainers-go/modules/redpanda)
- OTel messaging semconv: [opentelemetry.io/docs/specs/semconv/messaging/kafka](https://opentelemetry.io/docs/specs/semconv/messaging/kafka/)
- Redpanda producer tuning: [redpanda.com/blog/producer-config-deep-dive](https://www.redpanda.com/blog/producer-config-deep-dive)
- Idempotent producers in Redpanda: [docs.redpanda.com/.../idempotent-producers](https://docs.redpanda.com/current/develop/produce-data/idempotent-producers/)
- Outbox pattern: [Conduktor transactional outbox](https://www.conduktor.io/blog/transactional-outbox-pattern-database-kafka), [dev.to — PG outbox revamped](https://dev.to/msdousti/postgresql-outbox-pattern-revamped-part-1-3lai)
- Partitioning + cross-language compat: [Confluent standardized hashing](https://www.confluent.io/blog/standardized-hashing-across-java-and-non-java-producers/)
- DLQ conventions: [Confluent DLQ guide](https://www.confluent.io/learn/kafka-dead-letter-queue/), [Kai Waehner DLQ](https://www.kai-waehner.de/blog/2022/05/30/error-handling-via-dead-letter-queue-in-apache-kafka/)
- Metric cardinality discipline: [CNCF Prometheus labels](https://www.cncf.io/blog/2025/07/22/prometheus-labels-understanding-and-best-practices/)
- CloudEvents: [cloudevents.io](https://cloudevents.io/), [Quarkus Kafka CloudEvents example](https://quarkus.io/blog/kafka-cloud-events/)
