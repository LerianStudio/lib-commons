# TRD — `commons/streaming/`

## 1. Document metadata

| Field | Value |
|---|---|
| Title | Technical Requirements — Streaming wrapper harness for domain event publishing |
| Feature name | `commons/streaming/` |
| Version | v1 |
| Status | draft (Gate 2) |
| Date | 2026-04-18 |
| Track | Small Track (2–4 AI-agent days) |
| Module path | `github.com/LerianStudio/lib-commons/v5/commons/streaming` |
| Upstream docs | [`research.md`](./research.md) (Gate 0, 20 locked decisions) · [`prd.md`](./prd.md) (Gate 1, 15 ACs) · [`ux-criteria.md`](./ux-criteria.md) (Gate 1, 27 DX ACs) |
| Project invariants | [`CLAUDE.md`](../../../CLAUDE.md) (API v5, `log.Logger` interface, no panic, JSON doc rules) |

---

## 2. Executive summary

A new Go package `commons/streaming/` inside `github.com/LerianStudio/lib-commons/v5` that wraps all producer-side Kafka-protocol traffic for Lerian domain events. Events are produced to a Redpanda cluster via `github.com/twmb/franz-go` (v1.20.7), framed in **CloudEvents 1.0 binary mode** — context attributes carried as `ce-*` Kafka headers and the raw domain JSON as the message value. Publish is wrapped in a `commons/circuitbreaker` `CircuitBreaker.Execute(fn)` call: while CLOSED, records ship direct to the broker; while OPEN, the Producer serializes the event to `commons/outbox/OutboxRepository.Create` and returns `nil` — guaranteeing durability without surfacing infra flakiness to callers. Per-source-topic DLQ (`{topic}.dlq`) receives messages that exhaust retries, with an 8-class error taxonomy preserved in headers. Every emit records one `streaming.emit` OTEL span (producer kind) and touches bounded-cardinality metrics (`topic`, `operation`, `outcome`, `error_class` only; `tenant_id` on spans only). Tenant identity is extracted from `ctx` via `tenant-manager/core.GetTenantIDContext`; `SystemEvent=true` opts out of the tenant requirement. The Producer implements `commons.App` so consuming services wire it via `launcher.Add("streaming", producer)`. The package ships a `NoopEmitter`, a concurrency-safe `MockEmitter` with assertion helpers, `kfake`-backed unit tests (no Docker), and `testcontainers-go/modules/redpanda` v0.41.0 integration tests.

---

## 3. Architecture style & patterns

### 3.1 Pattern catalog

| Pattern | Role in this package |
|---|---|
| **Producer-side event publisher** | Single outbound event entry point (`Emitter.Emit`). No consumer-side code in v1. |
| **Circuit Breaker** | Wraps every publish in `CircuitBreaker.Execute(fn)` (see `research.md` §C2). Manager-scoped, service-named `"streaming.producer:<producerID>"`. |
| **Transactional Outbox (at-least-once)** | On circuit OPEN, serializes the event and writes to `commons/outbox/OutboxRepository.Create`. A streaming handler registered with `outbox.Dispatcher` relays outbox rows back through `publishDirect`. |
| **Dead Letter Queue** | Per-source-topic `{topic}.dlq` (not global). Kafka-native DLQ — does NOT use `commons/dlq/` which is Redis-backed. |
| **Fail-Fast + Bulkhead** | Per-Producer circuit breaker isolates one broker/cluster from another. Caller-input validation (tenant, payload size, JSON validity) fails synchronously before any I/O. |
| **Functional options** | All optional configuration on the emitter (logger, metrics, TLS, SASL, partition key overrides, circuit-breaker manager reuse, close timeout) surfaced as `EmitterOption`. |

### 3.2 Concurrency model

- `*streaming.Producer` is **safe for concurrent use**. `Emit` may be called from any number of goroutines; the underlying `*kgo.Client` batches internally.
- One `*kgo.Client` per Producer instance. One Producer per consuming process (wired at bootstrap). Multiple Producers in one process are supported but atypical — each carries a unique `producerID` in its circuit-breaker service name and span attribute.
- Internal state (`closed atomic.Bool`, `cbStateFlag atomic.Int32`) uses atomics; no user-visible mutexes.
- Background goroutines (e.g. the circuit-state-listener-driven flag writer) run under `commons/runtime.SafeGoWithContextAndComponent` for panic-safety.

### 3.3 Degradation ladder

```
┌─ CLOSED ─────── broker healthy ────────── publishDirect → kgo.ProduceSync → Emit returns nil
│
├─ [retry] ───── transient failures ──── franz-go RecordRetries (bounded per class)
│
├─ [retries exhausted] ─────── classifyError → DLQ publish (no outbox fallback for DLQ writes)
│                                                │
│                                                └─ DLQ write fails → ERROR log + metric + wrapped err
│
├─ OPEN ─────── breaker trips ───────────── publishToOutbox → Create row → Emit returns nil
│                                                │
│                                                └─ outbox.Dispatcher calls registered handler
│                                                    → publishDirect (bypasses breaker to avoid loop)
│                                                    → on repeated failure, Dispatcher marks FAILED w/ backoff
│
├─ HALF-OPEN ── probe succeeds ─────────── back to CLOSED (listener fires → flag reset)
│
└─ Closed emitter ── Emit → ErrEmitterClosed (synchronous, no I/O)
```

### 3.4 Ownership model

- **App bootstrap owns Producer lifecycle.** The consuming service's `main.go` constructs one `*streaming.Producer`, calls `launcher.Add("streaming", producer)`, and closes it on shutdown via the `Launcher`. Service methods receive an `Emitter` via constructor injection and MUST NOT call `Close` (DX-E01 in `ux-criteria.md`).
- **Outbox handler registration is explicit.** `(*Producer).RegisterOutboxHandler(registry *outbox.HandlerRegistry) error` is called once at bootstrap, after both the outbox Dispatcher and the Producer exist. The streaming handler calls `(*Producer).publishDirect` (internal), bypassing the circuit-breaker wrapper so that handler retries do not themselves feed the outbox.
- **Circuit breaker Manager is injectable.** If the service already owns a `circuitbreaker.Manager` (e.g. for HTTP clients), it passes it via `WithCircuitBreakerManager(m)`; otherwise the Producer creates its own.

---

## 4. Component design

### C1 — `streaming.Event` (wire format)

CloudEvents-aligned struct. Field choices follow `research.md` §D4 and `ux-criteria.md` DX-A02.

```go
type Event struct {
    // Required CloudEvents context attributes (will ship as ce-* headers)
    TenantID      string          // ce-tenantid (extension); empty => derive from ctx
    ResourceType  string          // composed into ce-type: "studio.lerian.<resource>.<event>"
    EventType     string          // composed into ce-type
    EventID       string          // ce-id; auto-populated as uuid.NewV7() when empty
    SchemaVersion string          // ce-schemaversion (extension); default "1.0.0"
    Timestamp     time.Time       // ce-time; default time.Now().UTC()
    Source        string          // ce-source (required CloudEvents) e.g. "//lerian.midaz/transaction-service"

    // Optional CloudEvents fields
    Subject         string        // ce-subject; typically aggregate ID
    DataContentType string        // ce-datacontenttype; default "application/json"
    DataSchema      string        // ce-dataschema; optional schema URI

    // Lerian-specific extensions
    SystemEvent bool              // ce-systemevent; when true, skip TenantID requirement
    Payload     json.RawMessage   // Kafka message body (raw domain data)
}

func (e Event) Topic() string         // lerian.streaming.<resource>.<event>[.v<major>]
func (e Event) PartitionKey() string  // tenant_id (or event_type when SystemEvent)
```

**Topic derivation rules:**
- Base form: `lerian.streaming.<ResourceType>.<EventType>`.
- Semver major ≥ 2 (parsed from `SchemaVersion` via `golang.org/x/mod/semver`): append `.v<major>` suffix.
  - `SchemaVersion="1.0.0"` → `lerian.streaming.transaction.created`
  - `SchemaVersion="2.3.1"` → `lerian.streaming.transaction.created.v2`
- Invalid semver: fall through to base form; classifier will surface a `validation_error` at publish time only if schema version is required.

**Partition key rules:**
- Default: `e.TenantID`. Preserves per-tenant FIFO ordering when `StickyKeyPartitioner(KafkaHasher(nil))` is used.
- When `SystemEvent == true`: fall back to `"system:" + e.EventType` so tenant-less events have a deterministic key.
- Operator override: `WithPartitionKey(fn func(Event) string)` replaces the default. Documented escape hatch for hot-tenant salting (see `prd.md` R2).

### C2 — `streaming.Emitter` interface

```go
type Emitter interface {
    Emit(ctx context.Context, event Event) error
    Close() error
    Healthy(ctx context.Context) error
}
```

The 3-method surface is intentional (DX-A01). Safe for concurrent use; each implementation states this in its first godoc sentence (DX-A03).

**Error surface per method:**
| Method | Error classes |
|---|---|
| `Emit` | `ErrMissingTenantID`, `ErrMissingSource`, `ErrEmitterClosed`, `ErrEventDisabled`, `ErrPayloadTooLarge`, `ErrNotJSON`, `*EmitError{Class: <8 classes>}`. Returns `nil` when the circuit is OPEN and outbox write succeeds. |
| `Close` | Idempotent; always returns `nil` after the first successful close. Wraps `kgo.Client.Close` and `kgo.Client.Flush(ctx)` under the close-timeout deadline. |
| `Healthy` | `*HealthError` wrapping a typed `HealthState` (`Healthy`, `Degraded`, `Down`). Never `nil` unless the probe succeeded. |

### C3 — `streaming.Producer` (concrete `Emitter` for franz-go + Redpanda)

Implements `Emitter` **and** `commons.App`. A compile-time assertion `var _ Emitter = (*Producer)(nil)` and `var _ commons.App = (*Producer)(nil)` documents this (DX-E05).

**Fields:**
```go
type Producer struct {
    client      *kgo.Client
    cfg         Config
    cbManager   circuitbreaker.Manager
    cb          circuitbreaker.CircuitBreaker
    outbox      outbox.OutboxRepository
    dispatcher  *outbox.Dispatcher       // optional (used when caller wants auto-register)
    metrics     *streamingMetrics
    tracer      trace.Tracer
    logger      log.Logger               // NEVER *zap.Logger
    closed      atomic.Bool
    cbStateFlag atomic.Int32             // 0=closed, 1=half-open, 2=open
    producerID  string                   // machine-id or uuid.New().String()
    partFn      func(Event) string       // partition key function (default tenant_id)
    toggles     map[string]bool          // per-event kill switches from STREAMING_EVENT_TOGGLES
}
```

**Constructor:**
```go
func New(ctx context.Context, cfg Config, opts ...EmitterOption) (*Producer, error)
```

**Lifecycle methods:**
```go
func (p *Producer) Run(launcher *commons.Launcher) error
func (p *Producer) RunContext(ctx context.Context, launcher *commons.Launcher) error
func (p *Producer) Close() error
func (p *Producer) CloseContext(ctx context.Context) error
```

**Not responsibilities:**
- Schema validation (other than JSON-parsability).
- Producer-side authorization (broker SASL covers transport-level auth).
- Consumer-side logic.
- DLQ replay tooling.

**Collaborators:** `kgo.Client` (franz-go), `circuitbreaker.Manager` + `CircuitBreaker`, `outbox.OutboxRepository`, `outbox.Dispatcher` (optional), `log.Logger`, `metrics.MetricsFactory`, `trace.Tracer`, `tenant-manager/core.GetTenantIDContext`.

### C4 — `streaming.Config` & env loader

Env-driven configuration with prefix `STREAMING_`. Loader uses `commons.GetenvOrDefault`, `commons.GetenvBoolOrDefault`, `commons.GetenvIntOrDefault` (returns `int64` — per `research.md` §C4). Broker CSV split inline with `strings.Split`.

```go
type Config struct {
    Enabled              bool
    Brokers              []string
    ClientID             string
    BatchLingerMs        int
    BatchMaxBytes        int
    MaxBufferedRecords   int
    Compression          string  // snappy|lz4|zstd|gzip|none
    RecordRetries        int
    RecordDeliveryTimeout time.Duration
    RequiredAcks         string  // all|leader|none
    CBFailureRatio       float64
    CBMinRequests        int
    CBTimeout            time.Duration
    CloseTimeout         time.Duration
    CloudEventsSource    string  // required when Enabled=true
    EventToggles         map[string]bool
}

func LoadConfig() (Config, error)
```

| Env var | Type | Default | Purpose |
|---|---|---|---|
| `STREAMING_ENABLED` | bool | `false` | Master kill switch. When false, `New` returns a `NoopEmitter`. |
| `STREAMING_BROKERS` | csv | `localhost:9092` | Redpanda bootstrap list. Required when `ENABLED=true`. |
| `STREAMING_CLIENT_ID` | string | derived from hostname | Kafka `client.id`. |
| `STREAMING_BATCH_LINGER_MS` | int | `5` | `ProducerLinger`; explicit to counter franz-go v1.20 default flip. |
| `STREAMING_BATCH_MAX_BYTES` | int | `1048576` | `ProducerBatchMaxBytes` (1 MiB). |
| `STREAMING_MAX_BUFFERED_RECORDS` | int | `10000` | Backpressure ceiling. |
| `STREAMING_COMPRESSION` | string | `lz4` | One of `snappy`, `lz4`, `zstd`, `gzip`, `none`. |
| `STREAMING_RECORD_RETRIES` | int | `10` | franz-go `RecordRetries`. |
| `STREAMING_RECORD_DELIVERY_TIMEOUT_S` | int | `30` | Per-record delivery cap. |
| `STREAMING_REQUIRED_ACKS` | string | `all` | `AllISRAcks` / `LeaderAck` / `NoAck`. |
| `STREAMING_CB_FAILURE_RATIO` | float | `0.5` | Circuit-breaker trip threshold. |
| `STREAMING_CB_MIN_REQUESTS` | int | `10` | Minimum observations before evaluating ratio. |
| `STREAMING_CB_TIMEOUT_S` | int | `30` | Circuit-breaker open→half-open probe delay. |
| `STREAMING_CLOSE_TIMEOUT_S` | int | `30` | Max time for `Close` to drain + flush. |
| `STREAMING_CLOUDEVENTS_SOURCE` | string | *(required)* | `ce-source` default, e.g. `//lerian.midaz/transaction-service`. |
| `STREAMING_EVENT_TOGGLES` | csv or JSON | `""` | Per-event kill switches (format: `"resource.event=false,other.event=true"`). |

**Validation at `LoadConfig`:**
- `Enabled=true` + empty broker list → `ErrMissingBrokers`.
- `Enabled=true` + empty `CloudEventsSource` → `ErrMissingSource`.
- Invalid `Compression` value → `ErrInvalidCompression`.
- Invalid `RequiredAcks` → `ErrInvalidAcks`.

Every `STREAMING_*` key above is added to `.env.reference` in the same PR (DX-F05, hard project rule).

### C5 — `streaming.NoopEmitter`

```go
func NewNoopEmitter() Emitter
```

Returned automatically when `Config.Enabled == false` OR `len(Config.Brokers) == 0`. `Emit` returns `nil`. `Healthy` returns `nil`. `Close` is a no-op returning `nil`. Safe for tests and feature-flag-off paths. Mirrors `log.NewNop()`, `metrics.NewNopFactory()` (DX-C05).

### C6 — `streaming.MockEmitter` + assertion helpers

```go
type MockEmitter struct { /* unexported */ }

func NewMockEmitter() *MockEmitter
func (m *MockEmitter) Emit(ctx context.Context, event Event) error
func (m *MockEmitter) Events() []Event
func (m *MockEmitter) SetError(err error)
func (m *MockEmitter) Reset()
func (m *MockEmitter) Close() error
func (m *MockEmitter) Healthy(ctx context.Context) error

// Helpers use testing.TB so they work in benchmarks and fuzz tests.
func AssertEventEmitted(t testing.TB, m *MockEmitter, resourceType, eventType string)
func AssertEventCount(t testing.TB, m *MockEmitter, resourceType, eventType string, n int)
func AssertTenantID(t testing.TB, m *MockEmitter, tenantID string)
func AssertNoEvents(t testing.TB, m *MockEmitter)
func WaitForEvent(t testing.TB, ctx context.Context, m *MockEmitter, matcher func(Event) bool, timeout time.Duration) Event
```

All state guarded by `sync.Mutex`. Captured events are deep-copied on `Emit` to prevent caller-side mutation after the fact. Every helper calls `t.Helper()` (DX-C03). `WaitForEvent` uses `time.NewTimer` and polls every 1ms; under `testing/synctest` it is fully deterministic.

### C7 — Outbox integration (registered handler, NOT a sidecar relay)

Per `research.md` §C3 / §E3: reuse the existing `outbox.Dispatcher`. Register a streaming handler rather than inventing a parallel relay.

```go
// RegisterOutboxHandler registers a handler with the outbox Dispatcher that
// relays outbox rows whose EventType begins with "lerian.streaming." back
// through the Producer's direct publish path. Calling this is optional —
// callers may also wire a handler themselves.
func (p *Producer) RegisterOutboxHandler(registry *outbox.HandlerRegistry) error
```

**How circuit-open → outbox flow works:**

1. Caller invokes `(*Producer).Emit(ctx, event)`.
2. `Emit` performs synchronous caller-side validation (tenant, source, size, JSON).
3. `Emit` dispatches to `cb.Execute(func() (any, error) { ... })`. Inside the closure:
   - If `p.cbStateFlag.Load() == flagOpen`: serialize the event to JSON, call `p.outbox.Create(ctx, outboxEvent)` (or `CreateWithTx` if an ambient `*sql.Tx` is on the context), record `streaming_outbox_routed_total{topic, reason="circuit_open"}`, return `(nil, nil)` from the closure — `Execute` sees success and the circuit breaker remains untouched. Caller sees `nil`.
   - Else: call `p.publishDirect(ctx, event)`. On success, return `(nil, nil)`. On failure, return `(nil, err)` — `Execute` counts the failure against the circuit.
4. On retry exhaustion (franz-go returns `kgo.ErrRecordTimeout` or `kgo.ErrRecordRetries`): classify, publish to DLQ via `publishDLQ`, return the wrapped `*EmitError` to the caller.

**Handler bridge (registered via `RegisterOutboxHandler`):**

The handler receives an `*outbox.OutboxEvent`. It unmarshals `Payload` into a `streaming.Event`, then calls `p.publishDirect(ctx, event)` — NOT `p.Emit`. This bypasses both the circuit-breaker wrapper and the outbox fallback, guaranteeing no loop. On success, the Dispatcher marks the row `PUBLISHED`. On failure, it returns the error; the Dispatcher marks the row `FAILED` with exponential backoff retry (pre-existing behavior; no change to outbox required).

### C8 — DLQ publisher

Per-source-topic DLQ: source topic `lerian.streaming.transaction.created` → DLQ topic `lerian.streaming.transaction.created.dlq`.

**Headers on DLQ message (in addition to all original `ce-*` headers, preserved verbatim):**
| Header | Value |
|---|---|
| `x-lerian-dlq-source-topic` | original topic name |
| `x-lerian-dlq-error-class` | one of 8 classes in C9 |
| `x-lerian-dlq-error-message` | sanitized cause (`sanitizeBrokerURL` applied) |
| `x-lerian-dlq-retry-count` | int as string (franz-go `RecordRetries` counter at exhaust) |
| `x-lerian-dlq-first-failure-at` | RFC3339 UTC timestamp |
| `x-lerian-dlq-producer-id` | `p.producerID` |

**Body:** the original domain payload, **unchanged**. This makes replay trivial: an ops tool copies the message, strips `x-lerian-dlq-*` headers, and republishes to the source topic.

**No outbox fallback for DLQ writes.** If the DLQ publish itself fails: log ERROR, increment `streaming_dlq_publish_failed_total{topic}`, return the wrapped original error. This prevents a failure in DLQ infrastructure from amplifying into an outbox backlog.

### C9 — Error taxonomy (8 classes, per `research.md` §D6)

```go
type ErrorClass string

const (
    ClassSerialization     ErrorClass = "serialization_error"
    ClassValidation        ErrorClass = "validation_error"
    ClassAuth              ErrorClass = "auth_error"
    ClassTopicNotFound     ErrorClass = "topic_not_found"
    ClassBrokerUnavailable ErrorClass = "broker_unavailable"
    ClassNetworkTimeout    ErrorClass = "network_timeout"
    ClassContextCanceled   ErrorClass = "context_canceled"
    ClassBrokerOverloaded  ErrorClass = "broker_overloaded"
)

var (
    ErrMissingTenantID   = errors.New("streaming: tenant_id required for non-system events")
    ErrMissingSource     = errors.New("streaming: Event.Source required (CloudEvents ce-source)")
    ErrEmitterClosed     = errors.New("streaming: emitter is closed")
    ErrEventDisabled     = errors.New("streaming: event disabled by configuration toggle")
    ErrPayloadTooLarge   = errors.New("streaming: payload exceeds max size (1 MiB)")
    ErrNotJSON           = errors.New("streaming: payload must be valid JSON")
    ErrMissingBrokers    = errors.New("streaming: STREAMING_BROKERS required when ENABLED=true")
    ErrInvalidCompression = errors.New("streaming: invalid compression codec")
    ErrInvalidAcks       = errors.New("streaming: invalid required-acks value")
)

type EmitError struct {
    ResourceType string
    EventType    string
    TenantID     string
    Topic        string
    Class        ErrorClass
    Cause        error
}

func (e *EmitError) Error() string  // redacts credentials via sanitizeBrokerURL
func (e *EmitError) Unwrap() error

func IsCallerError(err error) bool  // true for the caller-fault classes + sentinels
```

**`IsCallerError` truth table (DX-B03):**
- `true`: `ClassSerialization`, `ClassValidation`, `ClassAuth` (config fault, not runtime), `ErrMissingTenantID`, `ErrMissingSource`, `ErrPayloadTooLarge`, `ErrNotJSON`, `ErrEventDisabled`.
- `false`: `ClassTopicNotFound`, `ClassBrokerUnavailable`, `ClassNetworkTimeout`, `ClassContextCanceled`, `ClassBrokerOverloaded`, `ErrEmitterClosed` (lifecycle).

**Classifier: `classifyError(err error) ErrorClass`**

Maps franz-go errors to classes. Mapping table:

| Upstream error | Class |
|---|---|
| `kerr.TopicAuthorizationFailed`, `kerr.ClusterAuthorizationFailed`, `kerr.SaslAuthenticationFailed` | `auth_error` |
| `kerr.UnknownTopicOrPartition` | `topic_not_found` |
| `kerr.MessageTooLarge`, `kerr.InvalidRecord`, `kerr.CorruptMessage` | `serialization_error` |
| `context.Canceled`, `context.DeadlineExceeded` | `context_canceled` |
| `net.Error` with `.Timeout()==true`, `kgo.ErrRecordTimeout` | `network_timeout` |
| `kerr.ThrottlingQuotaExceeded`, `kerr.PolicyViolation` | `broker_overloaded` |
| `kgo.ErrRecordRetries` (post-retries) | previously-classified cause, or `broker_unavailable` |
| default | `broker_unavailable` |

**Per-class retry + DLQ policy** (retries internal to franz-go; DLQ routing applied post-exhaustion):

| Class | Retries (franz-go) | DLQ on exhaust? | Log level |
|---|---|---|---|
| `serialization_error` | 0 | yes (immediate) | ERROR |
| `validation_error` | 0 | no — returned to caller | WARN |
| `auth_error` | 0 | yes + alert | ERROR |
| `topic_not_found` | 0 | yes + alert | ERROR |
| `broker_unavailable` | 5 (exp 100ms→10s) | yes | WARN |
| `network_timeout` | 3 (exp 100ms→5s) | yes | WARN |
| `context_canceled` | 0 | no — caller canceled | DEBUG |
| `broker_overloaded` | 3 (exp 1s→30s) | yes | WARN |

---

## 5. Integration surfaces

Each row describes a concrete lib-commons package touched by `commons/streaming/` and the nature of the integration.

| Package | Integration | Nature |
|---|---|---|
| `commons/log` | `log.Logger` interface in constructor; `log.NewNop()` fallback when nil; structured `Log(ctx, level, msg, fields...)` only | Consumer (read-only) |
| `commons/circuitbreaker` | `Manager.GetOrCreate("streaming.producer:<id>", cfg)`, `CircuitBreaker.Execute(fn)`, `Manager.RegisterStateChangeListener` | Consumer |
| `commons/outbox` | `OutboxRepository.Create` / `CreateWithTx` for fallback writes; `RegisterOutboxHandler(*HandlerRegistry)` for relay | Consumer (both sides) |
| `commons/tenant-manager/core` | `GetTenantIDContext(ctx) string` — empty string is the miss | Consumer |
| `commons/opentelemetry/metrics` | `MetricsFactory.Counter` / `Histogram` / `Gauge`; lazy `sync.Map` cache; `NewNopFactory()` for tests | Consumer |
| `commons/opentelemetry` | Reuse caller's `Tracer`; package attribute set defined locally | Consumer |
| `commons` (`app.go`) | `*Producer` implements `App`; registered via `Launcher.Add("streaming", producer)` | Producer (integrates) |
| `commons/net/http` | `HealthWithDependencies(...)` wraps `emitter.Healthy` with zero adapter | Consumer (indirect via caller) |
| `commons/runtime` | `SafeGoWithContextAndComponent` wrapper for background goroutines; `RecoverAndLog` inside circuit-state listener | Consumer |
| `commons/assert` | Nil-receiver guards mirroring `nilConnectionAssert` from `rabbitmq/rabbitmq.go:147-152` | Consumer |
| `commons/backoff` | Referenced by per-class retry policy table; franz-go's internal retries carry the work | Consumer (indirect) |
| `commons/dlq` | **NOT USED.** Streaming's DLQ is a Kafka topic, not Redis. | None |
| `commons/rabbitmq` | **NOT TOUCHED.** Orthogonal (internal work queues). | None |

---

## 6. Data architecture

Three concrete data shapes travel through this package.

### 6.1 CloudEvents wire format (binary mode)

Kafka message **headers** (string → `[]byte`), one per CloudEvents context attribute:

| Header | Source | Required |
|---|---|---|
| `ce-specversion` | literal `"1.0"` | yes |
| `ce-id` | `Event.EventID` (auto-populated `uuid.NewV7()`) | yes |
| `ce-source` | `Event.Source` (validated non-empty) | yes |
| `ce-type` | `"studio.lerian." + ResourceType + "." + EventType` | yes |
| `ce-time` | `Event.Timestamp.Format(time.RFC3339Nano)` | yes |
| `ce-subject` | `Event.Subject` | optional |
| `ce-datacontenttype` | `Event.DataContentType` (default `"application/json"`) | optional |
| `ce-dataschema` | `Event.DataSchema` | optional |
| `ce-tenantid` | `Event.TenantID` | extension (mandatory unless SystemEvent) |
| `ce-schemaversion` | `Event.SchemaVersion` (default `"1.0.0"`) | extension |
| `ce-resourcetype` | `Event.ResourceType` | extension |
| `ce-eventtype` | `Event.EventType` | extension |
| `ce-systemevent` | `"true"` if `SystemEvent`; header omitted otherwise | extension |

Kafka message **key** (`[]byte`): `Event.PartitionKey()` — `tenant_id` by default; `"system:"+EventType` when `SystemEvent`; or operator-supplied via `WithPartitionKey`.

Kafka message **value** (`[]byte`): `Event.Payload` passed through unchanged. No wrapping envelope. Consumers that want the envelope already have it in headers.

Kafka message **timestamp**: franz-go-populated `CreateTime`.

### 6.2 Outbox row

Reuses `commons/outbox.OutboxEvent` as-is. No schema changes.

| Column | Value written |
|---|---|
| `ID` | `uuid.New()` |
| `EventType` | full topic name, e.g. `lerian.streaming.transaction.created` — so `Dispatcher` routes by topic |
| `AggregateID` | deterministic UUID derived from partition key hash (`uuid.NewSHA1(namespace, []byte(partKey))`); random UUID for system events |
| `Payload` | JSON marshaling of the `streaming.Event` struct — all CloudEvents fields + raw `Payload` |
| `Status` | `PENDING` on creation |
| `Attempts` | 0 on creation |

JSON payload shape (canonical, for consumer implementations):

```json
{
    "tenant_id": "t-abc",
    "resource_type": "transaction",
    "event_type": "created",
    "event_id": "0190abcd-ef01-7890-abcd-ef0123456789",
    "schema_version": "1.0.0",
    "timestamp": "2026-04-18T10:00:00Z",
    "source": "//lerian.midaz/transaction-service",
    "subject": "tx-123",
    "data_content_type": "application/json",
    "data_schema": "",
    "system_event": false,
    "payload": { ... }
}
```

### 6.3 DLQ message

Same CloudEvents binary envelope as the primary message, plus the `x-lerian-dlq-*` headers from C8. Body identical to the original payload — error metadata lives in headers only, never in the body. Replay tooling strips `x-lerian-dlq-*` headers and republishes to the source topic.

---

## 7. Observability architecture

### 7.1 Metrics

Registered lazily on first record via the `commons/opentelemetry/metrics.MetricsFactory` pattern from `circuitbreaker/manager.go:85-103`. Nil-safe — a nil factory logs once and skips recording.

| Name | Type | Labels | Purpose |
|---|---|---|---|
| `streaming_emitted_total` | Int64Counter | `topic`, `operation`, `outcome` | Top-line success by outcome (`produced`, `outboxed`, `circuit_open`, `caller_error`, `dlq`) |
| `streaming_emit_duration_ms` | Int64Histogram | `topic`, `outcome` | Latency distribution per topic |
| `streaming_dlq_total` | Int64Counter | `topic`, `error_class` | Terminal losses (quarantined to DLQ) |
| `streaming_dlq_publish_failed_total` | Int64Counter | `topic` | DLQ writes that themselves failed — alerting signal |
| `streaming_outbox_routed_total` | Int64Counter | `topic`, `reason` | Fallback writes (`reason=circuit_open`, `reason=broker_error`) |
| `streaming_outbox_depth` | Int64Gauge | `topic` | Pending outbox rows of `event_type LIKE 'lerian.streaming.%'` (observed via outbox callback or scrape) |
| `streaming_circuit_state` | Int64UpDownCounter (gauge-like) | (none) | 0=closed, 1=half-open, 2=open |

**Label discipline (DX-D02):** `tenant_id` is **never** a metric label. Drill-down from metrics to tenant uses OTEL exemplars (span-linked histogram buckets).

### 7.2 OTEL spans

- **Name:** `streaming.emit`
- **Kind:** `trace.SpanKindProducer`
- **semconv package:** `go.opentelemetry.io/otel/semconv/v1.27.0`

**Attributes:**

| Key | Value |
|---|---|
| `messaging.system` | literal `"kafka"` |
| `messaging.destination.name` | topic name |
| `messaging.operation.type` | literal `"send"` (modern semconv; NOT deprecated `messaging.operation`) |
| `messaging.kafka.message.key` | partition key (only if `DEBUG` log level or explicit test mode; omitted by default to avoid cardinality leakage) |
| `messaging.client.id` | Kafka `client.id` |
| `event.resource_type` | `Event.ResourceType` |
| `event.event_type` | `Event.EventType` |
| `event.outcome` | `produced` / `outboxed` / `circuit_open` / `caller_error` / `dlq` |
| `tenant.id` | `Event.TenantID` — span only, NOT metric |
| `streaming.producer_id` | `p.producerID` |
| `error.type` | set on error paths (one of the 8 classes) |

**Span events:**
- `circuit.state_changed` — fired inside the state-change listener when the breaker transitions (attributes: `from`, `to`).
- `outbox.routed` — fired when falling back to outbox (attributes: `reason`).

### 7.3 Logs

Structured via `log.Logger.Log(ctx, level, msg, fields...)`. Standard fields across all log lines:

| Field | Type |
|---|---|
| `producer_id` | string |
| `topic` | string |
| `resource_type` | string |
| `event_type` | string |
| `tenant_id` | string (redacted per policy when applicable) |
| `outcome` | string |
| `error_class` | string (on error) |
| `event_id` | string |

**Credential sanitization before any log output:** broker URL strings pass through `sanitizeBrokerURL(s string) string` which strips `password=...`, `pass=...`, and `user:pass@` userinfo segments via regex. Pattern mirrors `commons/rabbitmq/rabbitmq.go:129, 470`.

**Log levels:**
- `ERROR`: `auth_error`, `topic_not_found`, `dlq_publish_failed`, outbox write failures.
- `WARN`: `broker_unavailable`, `network_timeout`, circuit OPEN transition, `broker_overloaded`, `validation_error` in caller flow.
- `INFO`: circuit state change to CLOSED, Producer start/stop.
- `DEBUG`: `context_canceled`, per-emit trace (opt-in via logger level).

### 7.4 Health check

```go
type HealthState int

const (
    Healthy HealthState = iota
    Degraded
    Down
)

type HealthError struct { /* unexported fields */ }

func (e *HealthError) Error() string
func (e *HealthError) State() HealthState
```

`Healthy(ctx)` returns `nil` when all of the following are true:
- Producer not `closed`.
- `kgo.Client.Ping(ctx)` succeeds within 500ms.
- `cbStateFlag != flagOpen` for more than 5 minutes (short spikes tolerated).

Otherwise returns `*HealthError` with typed `.State()`:
- `Degraded`: broker unreachable but outbox repository still writable (guaranteed persistence).
- `Down`: both broker and outbox unreachable.

Plugs into `commons/net/http.HealthWithDependencies` without any adapter (DX-D04 — matches the `HealthDependency` functional signature).

---

## 8. Security architecture

- **Multi-tenant isolation.** Zero-trust at entry. `Emit` returns `ErrMissingTenantID` synchronously before any I/O when `ctx` resolves to empty string and `SystemEvent == false`. `SystemEvent=true` is explicit opt-out. Tenant identity is included on spans but **never** on metric labels (DX-A05, DX-A06, DX-D02).
- **Credential sanitization.** Broker URLs with SASL credentials are stripped by regex before any log output — pattern mirrored from `commons/rabbitmq/rabbitmq.go:129, 470`. The `EmitError.Error()` method passes through `sanitizeBrokerURL`. Unit test DX-B06 enforces this.
- **TLS + SASL.** franz-go options exposed via `WithTLSConfig(*tls.Config)` and `WithSASL(sasl.Mechanism)`. Not required for v1 functional correctness but the options MUST exist at v1 to avoid a later breaking change.
- **Payload validation.** `json.Valid(event.Payload)` check before publish; rejects with `ErrNotJSON`. Prevents malformed messages from reaching consumers and from poisoning DLQ replay.
- **Size limits.** `len(event.Payload) > 1_048_576` rejects with `ErrPayloadTooLarge` pre-flight. Aligned with `outbox.DefaultMaxPayloadBytes` so the outbox-fallback path never accepts what Redpanda cannot deliver.
- **No cross-tenant read exposure.** This is a producer-only package; no consumer path, no multi-tenant cache, no cross-tenant query surface.

---

## 9. Circuit breaker wiring

Concrete details for the `commons/circuitbreaker` integration, per `research.md` §C2 / §E2.

- **Manager construction.** Reuse caller's `circuitbreaker.Manager` if provided via `WithCircuitBreakerManager(m)`. Otherwise the Producer constructs its own via `circuitbreaker.NewManager(p.logger)`.
- **Service name.** `"streaming.producer:" + p.producerID`. Disambiguates multiple Producers in one process (rare but supported).
- **Config.** `circuitbreaker.HTTPServiceConfig()` as default — the closest-fit preset (10s timeout, 5 consecutive failures, 0.5 failure ratio over min 10 requests). Overrides from env (`STREAMING_CB_FAILURE_RATIO`, `STREAMING_CB_MIN_REQUESTS`, `STREAMING_CB_TIMEOUT_S`).
- **State tracking.** The Producer registers a listener via `p.cbManager.RegisterStateChangeListener(func(svc, from, to State) { ... })` at `New`. The listener:
  - Updates `p.cbStateFlag` atomically (0=closed, 1=half-open, 2=open).
  - Emits a `circuit.state_changed` span event on the **currently active** span if present.
  - Logs at INFO on CLOSED, WARN on OPEN.
  - Runs under `commons/runtime.RecoverAndLog` for panic safety. The listener does NO I/O — only atomic writes and logging — so it stays well under the 10s listener deadline.
- **Publish path reads the flag, not `IsOpen()`.** Per `research.md` §A2, no such method exists on `CircuitBreaker`. The flag is the single source of truth for the outbox-fallback branch.

---

## 10. Testing architecture

### 10.1 Unit tests (no build tags)

- `streaming.MockEmitter` + assertion helpers for downstream service tests.
- `*kgo.Client` behavior stubbed via `github.com/twmb/franz-go/pkg/kfake` where franz-go-level behavior (retries, partitioning, header round-trip) needs to be tested without a real broker. `kfake` is in-process, starts in milliseconds, requires no Docker.
- Table-driven tests for: `classifyError` (one row per franz-go error), `Event.Topic()` (semver suffix rules), `Event.PartitionKey()` (system-event branch), CloudEvents header marshaling, outbox row shape, DLQ header shape.
- Concurrency tests (`-race`): spawn 1000 goroutines each calling `Emit` once on a single `*Producer` backed by `kfake`; assert 1000 records delivered, no data race.
- Deterministic retry/flush timing tests using `testing/synctest` (Go 1.25 feature; see `research.md` §D10).

### 10.2 Integration tests (build tag `integration`)

- `github.com/testcontainers/testcontainers-go/modules/redpanda` **v0.41.0** (match repo pin — `research.md` §D9).
- Use `redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v24.2.x", opts...)` — NOT the deprecated `RunContainer`.
- Startup wait: `wait.ForLog(...).WithStartupTimeout(60 * time.Second)`.
- Broker address: `container.KafkaSeedBroker(ctx)`.
- Assertions per test:
  - Round-trip produce + consume preserving every `ce-*` header.
  - Partition-key FIFO: enqueue 1000 events with 5 distinct tenant keys; consume per partition; assert within-partition order preserved.
  - DLQ routing: forcibly inject `kerr.MessageTooLarge` via a large payload; assert message lands on `{topic}.dlq` with all documented `x-lerian-dlq-*` headers.
  - Outbox fallback: use Toxiproxy (if already present in `go.mod`; otherwise flag as stretch) to break the broker connection; assert `outbox.OutboxRepository.ListPending` returns the emitted events.
- Naming: `TestIntegration_<Name>`, file suffix `_integration_test.go`, bare `//go:build integration` tag (no legacy `// +build` form — per `research.md` §C10).

### 10.3 CloudEvents contract test (CI)

Uses `github.com/cloudevents/sdk-go/v2` v2.15.2 (test-only dep) to parse messages produced by the package and validate every required context attribute round-trips correctly. Fails CI if parse fails. Addresses `prd.md` R3.

---

## 11. Package layout

Final file list, responsibility per file:

```
commons/streaming/
├── doc.go                   # Package godoc: 10-line example, env-var table, error table, scope statement
├── streaming.go             # Emitter interface, sentinel errors, EmitError, ErrorClass, IsCallerError
├── event.go                 # Event struct + Topic() + PartitionKey() + defaults helpers
├── options.go               # EmitterOption (functional options)
├── config.go                # Config struct + LoadConfig() + validation
├── producer.go              # *Producer (franz-go impl of Emitter; implements commons.App)
├── publish.go               # publishDirect (kgo.ProduceSync), publishToOutbox, publishDLQ
├── classify.go              # classifyError(err) ErrorClass with full franz-go kerr mapping
├── noop.go                  # NoopEmitter
├── mock.go                  # MockEmitter + assertion helpers + WaitForEvent
├── health.go                # HealthError, HealthState enum
├── outbox_handler.go        # RegisterOutboxHandler(*HandlerRegistry) — bridges Dispatcher → publishDirect
├── metrics.go               # streamingMetrics struct + newStreamingMetrics(factory)
├── cloudevents.go           # buildCloudEventsHeaders(Event) + parseCloudEventsHeaders
├── sanitize.go              # sanitizeBrokerURL(s) — credential-stripping regex
├── producer_test.go         # Unit tests for Producer (kfake-backed)
├── event_test.go            # Event.Topic / PartitionKey / CloudEvents marshaling
├── classify_test.go         # Error classifier table tests
├── mock_test.go             # Assertion helper coverage
├── outbox_handler_test.go   # Handler registration + invocation
├── integration_test.go      # //go:build integration — Redpanda round-trip, DLQ, FIFO, outbox fallback
└── dashboards/
    └── streaming-emitter.json  # Grafana dashboard (stretch — may slip to v1.1)
```

Note: `doc.go` is the only file with prose; every other `.go` file has only godoc on exported symbols. Schema directory (`schemas/`) is mentioned in PRD US-07 but is a consumer-facing doc drop — populated as downstream services define events, not owned by this TRD.

---

## 12. Dependencies (go.mod additions)

```
github.com/twmb/franz-go v1.20.7
github.com/twmb/franz-go/pkg/kversion v1.3.0
github.com/twmb/franz-go/pkg/kerr            // transitive via franz-go
github.com/twmb/franz-go/pkg/kfake          // test-only
github.com/cloudevents/sdk-go/v2 v2.15.2    // test-only (contract test)
github.com/testcontainers/testcontainers-go/modules/redpanda v0.41.0  // test-only, repo-pinned
golang.org/x/mod v0.23.0                     // semver major parsing
go.opentelemetry.io/otel/semconv/v1.27.0    // likely already indirect
```

**Already present in go.mod (no version change):** `github.com/google/uuid v1.6.0` (used via `uuid.NewV7()`).

**Explicitly excluded:**
- `github.com/confluentinc/confluent-kafka-go` — CGO dependency, cross-compile hostile (`research.md` §D1).
- `github.com/IBM/sarama` — ~2.4x slower produce (`research.md` §D1).

---

## 13. Migration / adoption notes

- **No existing code to migrate.** Greenfield package on branch `feat/adding-streaming`.
- **Downstream adoption pattern** (to be copy-pasted into `doc.go` and then into each consuming service's README):
  1. Add env vars to the service's `.env` from `.env.reference`.
  2. In `main.go`: `producer, err := streaming.New(ctx, cfg); launcher.Add("streaming", producer)`.
  3. After constructing the outbox `Dispatcher`: `producer.RegisterOutboxHandler(dispatcher.Registry())`.
  4. Inject `streaming.Emitter` into service constructors (not `*Producer` — interface keeps tests honest).
  5. In unit tests: `mock := streaming.NewMockEmitter()` then use assertion helpers.
- **RabbitMQ stays as-is.** Documented boundary in `doc.go`: streaming = past-tense domain facts for external consumers; RabbitMQ = commands / work for internal queues. No deprecation, no rename.

---

## 14. Risks & mitigations

| # | Risk | Mitigation |
|---|---|---|
| R1 | **franz-go `ProducerLinger` default flipped v1.17 → v1.20 (0ms → 10ms).** Silent latency change if the upstream default flips again. | Pin `ProducerLinger(5 * time.Millisecond)` explicitly in `producer.go`. Every producer option in `research.md` §D2 is pinned explicitly (not defaulted). Integration test asserts p50 emit latency < 20ms over 10k messages. |
| R2 | **Hot-tenant partition skew.** One partition CPU-saturated, others idle; FIFO-per-tenant prevents trivial rebalancing. | Expose `WithPartitionKey(fn func(Event) string)` as an escape hatch. Document composite `tenant_id|aggregate_id` and salting (`tenant_id|0..K-1`) patterns in the option's godoc. Grafana dashboard includes a per-partition throughput panel with an alert rule on coefficient-of-variation > 0.5 (`prd.md` R2). |
| R3 | **CloudEvents interop untested externally.** Subtle header-naming mismatch breaks every downstream consumer at once. | CI contract test using `cloudevents/sdk-go/v2` parses produced messages and asserts every required context attribute round-trips. Added in same PR as producer code. Fails build on parse error. |
| R4 | **Outbox growth during extended broker outage.** Postgres table bloat, dispatcher throughput ceiling. | Gauge `streaming_outbox_depth` labeled by topic; Grafana alert at > 10k rows per topic or > 1k rows per tenant. Operator runbook in dashboard panel annotations covers: verify Redpanda reachability, inspect circuit state, drain via dispatcher tuning knobs. |
| R5 | **Circuit-state-listener 10s deadline.** If listener blocks on I/O, the circuit breaker Manager aborts it; state flag diverges from actual state. | Listener does NO I/O — only atomic int32 write and single log line. Run under `commons/runtime.RecoverAndLog`. Unit test asserts listener duration < 1ms under normal conditions. |

---

## 15. Open items for Gate 3 (task breakdown)

Items that are explicitly Gate 3's call, not the TRD's:

1. **Exact task decomposition and AI-agent-hours per task.** This TRD lists the file structure; Gate 3 will bin-pack work items (interface + sentinels → Event + topic derivation → Config + env loader → Producer skeleton + kgo init → circuit-breaker wiring → publish paths → DLQ → outbox handler → metrics → mock + helpers → integration tests → dashboard JSON).
2. **Dependency ordering between tasks.** Can integration tests be authored in parallel with the Producer (they require only the `Emitter` interface)? Should the DLQ path land before or after the outbox handler? Gate 3 resolves these.
3. **Go module version pinning exercise.** Run `go get github.com/twmb/franz-go@v1.20.7` etc. and `go mod tidy`; verify no transitive version skew against the existing module graph.
4. **Grafana dashboard JSON authoring.** `dashboards/streaming-emitter.json` is marked as stretch for v1.0 (`ux-criteria.md` DX-D05). Gate 3 decides whether to split this into its own subtask or slip to v1.1.
5. **Schema directory ownership.** `commons/streaming/schemas/{resource_type}/{event_type}.v1.json` is mentioned in `prd.md` US-07 as a consumer-discoverability artifact. Gate 3 decides whether this package owns the directory skeleton or leaves it entirely to downstream services.
