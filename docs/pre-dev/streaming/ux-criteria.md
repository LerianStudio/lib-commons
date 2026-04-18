# UX (DX) Acceptance Criteria — `commons/streaming/`

**Gate:** 1 (UX validation for a Go library — "users" are backend engineers, not end-users)
**Feature:** Streaming Emitter — single instrumentation point for publishing domain events to Redpanda.
**Date:** 2026-04-18

---

## 1. Purpose and scope

This file defines developer-experience (DX) acceptance criteria for the `commons/streaming/` Go package. Because this is a library, not a UI, every "user flow" is a call-site experience and every "wireframe" is an API signature. Each criterion below is **falsifiable** via a unit test, integration test, or PR code-review checkpoint. Criteria map 1:1 to the PRD §6 acceptance criteria plus additional DX-specific falsifiers covering idiomatic Go ergonomics, test scaffolding, error transparency, observability discipline, and discoverability. Cross-reference: `research.md` §B (personas, 15-criterion draft) and §E (locked decisions E1–E20).

## 2. Personas (condensed)

**P1 — Maya, Service Engineer.** Ships 3–8 PRs/week across midaz, matcher, and other Lerian services. Her measure of a good API is: three lines to emit an event, one line to test it, one godoc page to learn it. She is the canary for any API shape that leaks transport concerns (Kafka records, franz-go types) upward, and she is burned by `commons/rabbitmq`'s 6-arg `Publish` signature and its missing blessed mock. See `research.md` §B.

**P2 — Thiago, Platform / SRE Engineer.** Owns lib-commons and is on-call for the cluster. He does not grade the emit call site; he grades what happens when the broker partitions. He wants bounded-cardinality metrics, a `Healthy` that plugs into `net/http.HealthWithDependencies` with zero adapter code, a shipped Grafana dashboard, a `.env.reference` section listing every `STREAMING_*` var, and a `Close` contract that unambiguously states "called once, by the app bootstrap, idempotent after." See `research.md` §B.

## 3. DX acceptance criteria

### Theme A — Ergonomics at the call site

| ID | Criterion | How to verify |
|---|---|---|
| DX-A01 | A service method emits an event in ≤ 3 application lines: construct `Event`, call `Emit`, check `err`. | Code review: grep for `streaming.Emitter` call sites in the package example; ensure the end-to-end example in `doc.go` fits in a 10-line godoc block. |
| DX-A02 | `Event` struct uses spelled-out, CloudEvents-aligned field names — `Source`, `Subject`, `ResourceType`, `EventType`, `Payload`, `TenantID`, `SchemaVersion`, `SystemEvent`, `DataContentType` — with no Kafka/AMQP vocabulary. | Code review of the struct; no `Topic`, `Exchange`, `RoutingKey`, `Headers map[string]any` exposed on `Event`. |
| DX-A03 | `Emit` is safe to call from ≥ 1000 concurrent goroutines, and the first sentence of its godoc says so. | Unit test: spawn 1000 goroutines emitting into a `MockEmitter`, assert all 1000 events captured and no data race under `-race`. Grep `doc.go` for "safe for concurrent use." |
| DX-A04 | Payloads > 1 MiB rejected synchronously with `ErrPayloadTooLarge` before any I/O. | Unit test: emit an `Event` whose `Payload` is 1 MiB + 1 byte; assert `errors.Is(err, streaming.ErrPayloadTooLarge)` returns true and no broker connection was attempted. |
| DX-A05 | Tenant ID is extracted from context automatically; no tenant parameter appears in `Emit`'s signature. | Code review of `Emit(ctx context.Context, event Event) error`. Unit test: inject tenant via `tenant-manager/core.ContextWithTenantID(ctx, "t-123")`; assert the captured event's `TenantID == "t-123"`. |
| DX-A06 | When `SystemEvent == false` and tenant context is empty, `Emit` returns `ErrMissingTenantID` synchronously before any I/O. | Unit test: emit with a bare `context.Background()`; assert `errors.Is(err, streaming.ErrMissingTenantID)` and zero calls to the fake producer. |

### Theme B — Error transparency

| ID | Criterion | How to verify |
|---|---|---|
| DX-B01 | `errors.Is(err, streaming.ErrMissingTenantID)` (and every other sentinel) returns true for the canonical routing case. | Unit test per sentinel: trigger the fault, assert `errors.Is` matches. |
| DX-B02 | `errors.As(err, &streaming.EmitError{})` exposes `ResourceType`, `EventType`, `TenantID`, `Class`, and `Cause` for observability and logging. | Unit test: trigger a publish failure in the fake producer; `errors.As` into `*EmitError` and assert every field is populated (tenant redacted if policy says so). |
| DX-B03 | `streaming.IsCallerError(err) bool` returns true for serialization / validation / auth / topic-not-found faults and false for infra faults — one function call, no introspection. | Unit test: feed each of the 8 error classes through `IsCallerError` and assert a fixed truth table. |
| DX-B04 | Each of the 8 error classes (`serialization_error`, `validation_error`, `auth_error`, `topic_not_found`, `broker_unavailable`, `network_timeout`, `context_canceled`, `broker_overloaded`) has a corresponding sentinel AND a documented retry policy in godoc. | Code review: one godoc table listing class → sentinel → retry verdict. Mapping table matches `research.md` §D6. |
| DX-B05 | When the circuit is open, `Emit` persists to outbox AND returns `nil` — the caller's success path is preserved; delivery is guaranteed asynchronously. | Unit test: force the internal circuit breaker open via its state listener; assert `Emit` returns `nil` and the payload appears in the injected `outbox.OutboxRepository` fake with the correct `EventType`. |
| DX-B06 | Error messages NEVER contain broker passwords, SASL credentials, or full connection URLs with auth. | Unit test: configure an `Emitter` with `sasl://user:secret@broker:9092`, force a connection error, assert the error string contains `****` or `[REDACTED]` and does not contain `"secret"`. Mirror the regex from `commons/rabbitmq/rabbitmq.go`. |

### Theme C — Testability

| ID | Criterion | How to verify |
|---|---|---|
| DX-C01 | `streaming.NewMockEmitter()` returns a fully functional test double that captures emitted events; `(*MockEmitter).Events() []Event` returns the captured slice in emit order. | Unit test: emit 3 events, assert `mock.Events()` has length 3 in FIFO order and every field is preserved. |
| DX-C02 | Unit tests using `MockEmitter` pass with `go test ./...` — no build tags, no Docker, no external network, no `kfake`. | CI: run `go test ./commons/streaming/...` with no tags; assert exit 0 and zero container spin-ups in the test trace. |
| DX-C03 | Assertion helpers exist: `AssertEventEmitted(t, mock, resourceType, eventType)`, `AssertEventCount(t, mock, resourceType, eventType, n)`, `AssertTenantID(t, mock, tenantID)`, `AssertNoEvents(t, mock)`. | Code review: helpers defined with `testing.TB` (not `*testing.T`) and `t.Helper()` called. Unit tests for each helper covering pass and fail paths. |
| DX-C04 | `streaming.WaitForEvent(t testing.TB, ctx context.Context, matcher func(Event) bool, timeout time.Duration) Event` blocks until a matching event is captured or calls `t.Fatalf` on timeout. | Unit test using `testing/synctest` to drive deterministic virtual time; assert pass on match and failure on timeout. |
| DX-C05 | `streaming.NewNoopEmitter()` is a fully functional no-op: `Emit` returns `nil`, `Healthy` returns `nil`, `Close` is idempotent, `Events()` returns an empty slice. | Unit test: each method contract verified on a fresh `NoopEmitter`. |
| DX-C06 | Integration tests (build tag `integration`, name `TestIntegration_<Name>`) spin up a Redpanda testcontainer (v0.41.0) and assert round-trip produce + consume. | CI: `make test-integration` runs and passes; trace confirms `testcontainers-go/modules/redpanda.Run` is invoked. |

### Theme D — Observability

| ID | Criterion | How to verify |
|---|---|---|
| DX-D01 | Every `Emit` produces exactly one OTEL span named `streaming.emit` with attributes `messaging.system=kafka`, `messaging.destination.name=<topic>`, `messaging.operation.type=send`, `tenant.id=<id>`, `event.resource_type`, `event.event_type`, and `event.outcome ∈ {produced, outboxed, circuit_open, caller_error}`. | Unit test with an in-memory OTEL span exporter; emit one event, assert span count == 1 and every attribute is present with the correct value. Semconv pin: `v1.27.0`. |
| DX-D02 | OTEL metric labels are bounded-cardinality only — `topic`, `operation`, `outcome`, `error_class`. `tenant_id` is NEVER a metric label. | Unit test with a metric recorder spy: assert `tenant_id` does not appear on any metric attribute set, only on span attributes. |
| DX-D03 | Counters `streaming_emitted_total{topic,operation,outcome}`, `streaming_dlq_total{topic,error_class}`, gauge `streaming_outbox_depth{topic}`, and gauge `streaming_circuit_state` (values 0=closed, 1=half-open, 2=open) are registered and recorded on their canonical events. | Unit test per metric: trigger the event, read the spy, assert the counter/gauge changed by the expected delta with the expected labels. |
| DX-D04 | `Healthy(ctx context.Context) error` plugs into `commons/net/http.HealthWithDependencies` with zero adapter code; on failure, the returned error carries a typed `.State()` method returning `HealthState ∈ {Healthy, Degraded, Down}`. | Code review: `(*Producer).Healthy` satisfies the `HealthDependency` functional signature. Unit test: force each state, assert `errors.As(err, &s) && s.State() == expected`. States mirror `commons/rabbitmq/publisher.go`. |
| DX-D05 | A Grafana dashboard JSON is published at `commons/streaming/dashboards/streaming-emitter.json` covering emitted rate, DLQ rate, outbox depth, circuit state, and p95 emit latency. | File exists in repo; dashboard imports cleanly into a Grafana 10+ instance (manual one-time check at Gate 2, or stretch to Gate 3). |
| DX-D06 | Sensitive fields (broker passwords, SASL credentials) are sanitized in logs and error messages via the same regex pattern used in `commons/rabbitmq/rabbitmq.go`. | Unit test: configure with a credentialed URL, provoke an error, assert the credential substring is absent from both the error string and any log line emitted. |

### Theme E — Lifecycle and resource ownership

| ID | Criterion | How to verify |
|---|---|---|
| DX-E01 | Package godoc explicitly states that app bootstrap owns `Close` and that service methods MUST NOT call `Close`. | Grep `doc.go` for the exact warning phrase "service methods must not call Close". |
| DX-E02 | `Close` is idempotent; the second and subsequent calls return `nil`. | Unit test: call `Close` three times on a live producer and on a closed producer; assert every return is `nil`. |
| DX-E03 | Post-`Close`, any `Emit` returns `ErrEmitterClosed` synchronously without I/O. | Unit test: close, then emit; assert `errors.Is(err, streaming.ErrEmitterClosed)` and zero broker calls via the fake. |
| DX-E04 | `Close` flushes buffered records and drains the outbox handler up to a bounded deadline (default 30s, tunable via `WithCloseTimeout(d)`). | Unit test: enqueue 10 buffered events, call `Close` with `WithCloseTimeout(5*time.Second)`; assert all 10 were flushed (or the deadline was respected and remaining events surface in logs with count). |
| DX-E05 | `*streaming.Producer` implements `commons.App`; a compile-time assertion exists; wiring is `launcher.Add("streaming", producer)` in the consuming service. | Grep for `var _ commons.App = (*Producer)(nil)` in the package. Example in `doc.go` shows the `launcher.Add` call. |

### Theme F — Discoverability

| ID | Criterion | How to verify |
|---|---|---|
| DX-F01 | `commons/streaming/doc.go` opens with a one-sentence scope statement distinguishing streaming (external domain events, past-tense facts) from RabbitMQ (internal queues, commands). | Read `doc.go` line 1–3; statement is present and unambiguous. |
| DX-F02 | Godoc includes a 10-line end-to-end example: bootstrap, inject into service, call `Emit`, swap in `MockEmitter` in the test. | Example compiles under `go test -run Example` (Go's example test harness). |
| DX-F03 | Godoc enumerates every `STREAMING_*` environment variable with its default and one-line purpose, in a table. | Grep `doc.go` for the env-var table; cross-check against the package's actual env-var consumers. |
| DX-F04 | `CLAUDE.md` gets a "Streaming (`commons/streaming`)" section under "API invariants to respect" summarizing `Emitter`, `Event`, the three methods (`Emit`, `Healthy`, `Close`), the sentinel errors, and the scope boundary vs. RabbitMQ. | `CLAUDE.md` diff contains the new section; wording is factual and mirrors the other package entries. |
| DX-F05 | `.env.reference` lists every `STREAMING_*` variable with defaults — mandatory per the project rule in `CLAUDE.md`. | CI linter (see §5) verifies that every `STREAMING_*` key read by the package source is present in `.env.reference`. |

Total criteria: **27** (spans all six themes; superset of the 15-item draft in `research.md` §B).

## 4. Anti-criteria (what good DX is NOT)

The following are contract violations; a PR that introduces any of them MUST be rejected.

- The public API MUST NOT expose franz-go types (`kgo.Record`, `kgo.Client`, `kgo.Opt`, etc.) on any exported signature, struct field, or godoc example.
- The public API MUST NOT expose RabbitMQ concepts (exchanges, routing keys, AMQP headers) — `commons/streaming` is a separate abstraction, not a rename.
- `tenant_id` MUST NOT appear as a metric label (unbounded cardinality); it may appear on spans only.
- Constructors MUST NOT accept `*zap.Logger` or return `*zap.Logger` — use `commons/log.Logger` per CLAUDE.md.
- Call-site signatures MUST NOT require more than three positional arguments; anything additional goes on the `Event` struct or on functional options.
- Error strings MUST NOT leak broker passwords, SASL credentials, or full credentialed URLs.
- `Emit` MUST NOT silently swallow `ErrMissingTenantID` — the caller must see it unless `SystemEvent == true`.
- `Emit` MUST NOT block indefinitely; it respects the caller's context deadline and returns `context.DeadlineExceeded` mapped to `network_timeout` class.

## 5. DX validation plan

| Theme | Primary validators | Secondary validators |
|---|---|---|
| A — Ergonomics | Code review checklist (grep for signature shape); compile-time example in `doc.go`. | Pair review with P1 (Maya) at PR time. |
| B — Error transparency | Unit tests per sentinel and per class; `errors.Is`/`errors.As` tests. | Golden-file test for `EmitError.Error()` formatting (no credential leak). |
| C — Testability | Unit tests of the mock, noop, and assertion helpers; `go test ./...` green with zero tags. | `testing/synctest`-based deterministic test of `WaitForEvent`. |
| D — Observability | In-memory OTEL span exporter + metric recorder spy in unit tests; stdout-exporter smoke test that a human visually confirms attribute names once at Gate 3. | Grafana dashboard import check. |
| E — Lifecycle | Unit tests for idempotent `Close`, post-`Close` `Emit`, bounded-deadline drain. | Integration test asserting `launcher.Add("streaming", producer)` shuts down cleanly under `ServerManager`. |
| F — Discoverability | Doc review checklist at PR. CI linter (new): scan `commons/streaming/*.go` for every `STREAMING_*` literal, verify each is listed in `.env.reference`; fail CI on mismatch. | Manual "read the godoc cold" smoke test by a first-time reader. |

## 6. Out of scope for DX (deferred to v2)

- **Schema Registry integration** (Redpanda SR is API-compatible with Confluent; propose as `WithSchemaValidator()` option in v2).
- **Auto-generated schema docs** from Go struct reflection / JSON Schema.
- **Consumer-side DX** — this package is producer-only; a sister `commons/streaming/consumer` package is out of scope until a consumer service requests it.
- **CLI tool for DLQ replay** — ops tooling, not library DX. Tracked separately.
- **CloudEvents structured mode** — we ship binary mode only (CloudEvents 1.0); structured mode is a follow-up PR if a consumer demands it.

## 7. Decision log

The following Gate 0 decisions (see `research.md` §E) directly shaped these DX criteria. Recorded here so Gate 2 reviewers can trace any criterion back to its originating decision.

| Decision | Influence on DX criteria |
|---|---|
| **CloudEvents 1.0 binary mode** (metadata in Kafka headers `ce-*`, body is raw domain payload) | Introduces `Source`, `Subject`, `DataContentType` fields on `Event` → DX-A02. Payload stays domain-native, so DX-A04 size check applies to the raw payload, not a wrapped envelope. |
| **Per-source-topic DLQ** (`{topic}.dlq`, not a single global DLQ) | DX-D03: metric `streaming_dlq_total` is labeled by `topic`, giving 1:1 source-to-DLQ visibility. Replay ergonomics are mirror-named, so no routing header lookup is needed at the ops-tool layer. |
| **8-class error taxonomy** (serialization / validation / auth / topic_not_found / broker_unavailable / network_timeout / context_canceled / broker_overloaded) | DX-B04 granularity, DX-D03 `error_class` label cardinality (bounded at 8), DX-B03 `IsCallerError` truth table. |
| **`commons/log.Logger`, not `*zap.Logger`** | Anti-criterion: zap types are banned from public constructors. CLAUDE.md invariant enforced by DX-F04. |
| **Circuit-open returns `nil`** (outbox takes over) | DX-B05 success-path guarantee; the caller's error-handling code does not need a circuit-breaker branch. |
| **`*streaming.Producer` implements `commons.App`** (no central `InitStreaming`) | DX-E05: wiring is `launcher.Add("streaming", producer)`; lifecycle is owned by the consuming service's bootstrap, not lib-commons. |
| **`tenant_id` on spans only, never on metrics** | DX-D01 puts `tenant.id` on spans; DX-D02 forbids it on metrics — exemplars remain as the drill-down path. |
| **Tenant from `tenant-manager/core`, empty string is the miss** | DX-A05 / DX-A06: no tenant parameter in `Emit`; `ErrMissingTenantID` fires when the context returns `""` and `SystemEvent == false`. |

---

**Criteria count:** 27 across 6 themes. Anti-criteria: 8. Deferred: 5. Decision-log entries: 8.
