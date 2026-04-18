# PRD — `commons/streaming/`

## 1. Document metadata

| Field | Value |
|---|---|
| Title | Streaming wrapper harness for domain event publishing |
| Feature name | `commons/streaming/` |
| Version | v1 |
| Status | draft |
| Created | 2026-04-18 |
| Gate 0 approval | Approved (see `research.md` §E, 20 decisions locked) |
| Author | Lerian platform team |
| Stakeholders | SRE on-call rotation; backend leads for midaz, matcher, tracer, fetcher, reporter, flowker; plugin maintainers; analytics / downstream-consumer owners |
| Track | Small Track (tentatively 2–4 AI-agent days; firmed in Gate 3) |
| Reference | `docs/pre-dev/streaming/research.md` (Gate 0, 2026-04-18) |

---

## 2. Problem statement

Lerian products today have no uniform channel for publishing **domain events** (business facts: `transaction.created`, `balance.adjusted`, `ledger.closed`, `tenant.provisioned`) to external consumers. Each service wires its own ad-hoc path, usually RabbitMQ, which was designed for in-network work queues rather than durable, schema-versioned event broadcast. The consequences:

- **Observability gaps.** No standardized span, no standardized metric. Every team reinvents `messaging.*` attributes and metric names. Grafana dashboards don't compose across services.
- **No tenant isolation guarantees.** External consumers cannot trust that a "tenant-A event" will not leak into "tenant-B stream" because enforcement is per-service ad-hoc.
- **No uniform degradation.** A broker outage today produces either silent drops or unbounded retry storms, depending on the service. No consistent circuit-breaker + outbox-fallback story.
- **No uniform DLQ.** When a message cannot be delivered, there is no standard quarantine topic, no standard retry metadata, no standard replay tool.
- **Duplicated wiring.** Every service rebuilds partitioning, compression, acks, producer tuning, client version pins, retry config, and headers. Drift between services is inevitable.
- **Future-hostile.** Without a CloudEvents-compatible envelope, integrating with Dapr, Knative Eventing, AWS EventBridge, or Confluent Schema Registry requires per-service retrofits.

The cost of **not** shipping this: compounding per-product duplication, observability debt, broker-outage blast radius incidents, and an inability to offer "subscribe to Lerian domain events" as a platform capability to partners.

The cost of shipping this: one library package, one version bump per consuming service, one dashboard.

---

## 3. Users & use cases

Research personas are defined in `research.md` §B. Synthesis below.

### P1 — Maya, service engineer

Ships 3–8 PRs/week across midaz / matcher / fetcher. Her goal is **"emit this event in three application lines, unit-test it without Docker, and move on."** Her pain with the previous RabbitMQ-shaped pattern: 944-LOC publisher, 6-argument `Publish(ctx, exchange, routingKey, mandatory, immediate, amqp.Publishing{...})` with AMQP-domain booleans, zero blessed mock. Her success signals: IDE autocomplete on `streaming.` surfaces ≤3 public methods; `errors.Is(err, streaming.ErrMissingTenantID)` self-documents; `streaming.NewMockEmitter()` lets her write table-driven unit tests.

### P2 — Thiago, platform / SRE engineer

Owns lib-commons and is on-call. He does not care how pretty `Emit` is. He cares that a broker partition → outbox-fallback → eventual recovery, with nothing silently dropping; that Grafana shows which service and which tenant are impacted; that `STREAMING_*` env vars are one table in one doc. His pain with prior generations: every service invented its own env namespace (`STREAMING_BROKERS` vs `KAFKA_BROKERS` vs `BROKER_HOST`); no opinionated dashboard JSON; `Close` semantics fuzzy. His success signals: `.env.reference` lists every `STREAMING_*` var; `dashboards/streaming-emitter.json` ships in-repo; `emitter.Healthy(ctx)` plugs into `HealthWithDependencies` without glue.

### P3 — Downstream consumer (analytics engineer / partner-integration engineer)

Subscribes to Lerian domain events from outside the producing service. Does not care which Lerian service produced the event; cares that the envelope is discoverable, parseable with a standard library (ideally a CloudEvents SDK), and that the schema is versioned. Success signals: can run `go get github.com/cloudevents/sdk-go` (or its Python / JS peer) and decode a Lerian event with zero Lerian-specific code; can read `commons/streaming/schemas/{resource_type}/{event_type}.v1.json` to know the payload shape.

---

## 4. Goals & non-goals

### Goals

1. **Single instrumentation point.** Every Lerian domain event publish goes through `streaming.Emitter.Emit(ctx, Event)`; OTEL spans, metrics, circuit-breaker state, and DLQ routing are uniform across products.
2. **Durable by construction.** Broker outage never silently drops a required event; circuit-open persists to the existing `commons/outbox/` pipeline and returns `nil` to the caller.
3. **One-line DX.** Service engineers emit events in a single line; unit tests run with `go test ./...` — no Docker, no build tags.
4. **Bounded-cardinality telemetry.** Metric labels use a closed set (`topic`, `operation`, `outcome`, `error_class`). Tenant identity lives on spans only, preserving drill-down without breaking Prometheus.
5. **Future-portable envelope.** CloudEvents 1.0 binary mode means Dapr / Knative / EventBridge / partner consumers parse events with off-the-shelf SDKs.

### Non-goals (explicit)

1. **Replacing RabbitMQ for internal queues.** Internal work distribution (RPC-style, worker queues) stays on AMQP. This package is producer-side, Redpanda-targeted, externally-consumable domain events only.
2. **Schema Registry client.** Subject-name resolution, compatibility checks, and wire-format envelopes (Avro, Protobuf) are deferred to v2. v1 ships JSON schemas as files in-repo for reference.
3. **Consumer-side library.** v1 is producer-only. Downstream consumers use franz-go's `kgo.Client` directly or their language's CloudEvents SDK.
4. **Event sourcing / replay infrastructure beyond DLQ.** Point-in-time replay, event-store compaction, projection frameworks — out of scope.
5. **Cross-cluster mirroring.** Redpanda's MirrorMaker-equivalent and multi-region replication are operator concerns, not library concerns.

---

## 5. User stories

| # | Persona | Story |
|---|---|---|
| US-01 | P1 Maya | As a service engineer, I want to emit a domain event by declaring `streaming.Event{Type: "transaction.created", ...}` and calling `emitter.Emit(ctx, event)`, so that I do not hand-wire headers, partitioning, or OTEL spans. |
| US-02 | P1 Maya | As a service engineer, I want to unit-test handlers that emit events using `streaming.NewMockEmitter()`, so that my tests run with plain `go test ./...` — no Docker, no build tags, no external network. |
| US-03 | P1 Maya | As a service engineer, I want `streaming.IsCallerError(err)` and typed `EmitError` / sentinel errors, so that I can distinguish "my input was bad" from "infrastructure is down" programmatically in one line. |
| US-04 | P2 Thiago | As a platform engineer, I want to configure the producer entirely via `STREAMING_*` env vars documented in `.env.reference`, so that every service adopts the same config surface without bespoke loaders. |
| US-05 | P2 Thiago | As a platform engineer, I want `streaming_emitted_total`, `streaming_dlq_total`, `streaming_outbox_depth`, `streaming_circuit_state`, and a shipped Grafana dashboard JSON, so that event-loss signals appear in monitoring without bespoke glue code per service. |
| US-06 | P2 Thiago | As an SRE, I want every emit span to carry `tenant.id`, `messaging.destination.name`, and `event.outcome`, so that I can diagnose a hot-partition or hot-tenant scenario by filtering traces — even though `tenant_id` is deliberately **not** a metric label. |
| US-07 | P3 consumer | As a downstream analytics engineer, I want JSON schemas checked into `commons/streaming/schemas/{resource_type}/{event_type}.v{N}.json`, so that I can discover payload shapes without running a service or reading producer Go code. |
| US-08 | P3 consumer | As a downstream partner-integration engineer, I want events delivered as CloudEvents 1.0 binary mode (`ce-*` Kafka headers, raw JSON body), so that I can consume them with any CloudEvents SDK — no Lerian-specific glue. |

---

## 6. Acceptance criteria

Each AC is testable via code review or automated test.

| ID | Criterion |
|---|---|
| AC-01 | A consuming service publishes a domain event by declaring `streaming.Event{...}` and calling `emitter.Emit(ctx, event)` — exactly **one line** of application code at the call site. |
| AC-02 | `Emit` returns `streaming.ErrMissingTenantID` **synchronously, before any network I/O**, when `ctx` has no tenant (per `tenant-manager/core.GetTenantIDContext`, empty string = miss) **and** `Event.SystemEvent == false`. |
| AC-03 | When the underlying `circuitbreaker.CircuitBreaker` is OPEN, `Emit` persists the event to the `commons/outbox/` pipeline and returns `nil` to the caller. Delivery is guaranteed by the existing outbox `*Dispatcher` with a streaming-topic handler registered; no sidecar relay is introduced. |
| AC-04 | All 8 error classes (`serialization_error`, `validation_error`, `auth_error`, `topic_not_found`, `broker_unavailable`, `network_timeout`, `context_canceled`, `broker_overloaded`) are distinguishable at runtime via `errors.Is` on exported sentinels **and** via `errors.As(&streaming.EmitError{}).Class`. Retry behavior per class follows `research.md` §D6. |
| AC-05 | Every emit records one OTEL span `streaming.emit` (kind `SpanKindProducer`) with attributes: `messaging.system=kafka`, `messaging.destination.name=<topic>`, `messaging.operation.type=send`, `messaging.kafka.message.key=<partition-key>`, `event.outcome` (one of `produced`, `outboxed`, `circuit_open`, `caller_error`), `tenant.id=<tid>`. Uses `semconv/v1.27.0`. |
| AC-06 | Metric labels are a bounded closed set: `topic`, `operation`, `outcome`, `error_class`. **`tenant_id` is NEVER a metric label.** Tenant drill-down from metrics uses OTEL exemplars (span-linked histogram buckets). |
| AC-07 | Events are published in **CloudEvents 1.0 binary mode**: context attributes as Kafka headers (`ce-id`, `ce-source`, `ce-type`, `ce-time`, `ce-specversion`, `ce-subject`, `ce-datacontenttype`, `ce-dataschema`) plus Lerian extensions (`ce-tenantid`, `ce-schemaversion`, `ce-resourcetype`, `ce-eventtype`, `ce-systemevent`). Kafka value is the raw domain payload with `content-type: application/json` — no wrapping envelope. |
| AC-08 | Unit tests using `streaming.NewMockEmitter()` pass with `go test ./...` — zero build tags, zero Docker, zero external network. `kfake` is used where franz-go-level fidelity is required; still in-process, no Docker. |
| AC-09 | Integration tests gated by `//go:build integration` run with `go test -tags=integration ./commons/streaming/...` against a real Redpanda container via `testcontainers-go/modules/redpanda` v0.41.0. Tests assert round-trip produce + consume, header round-trip, partition-key FIFO preservation, and DLQ routing. |
| AC-10 | `emitter.Healthy(ctx) error` plugs into `commons/net/http.HealthWithDependencies` with no adapter code. Returns typed state (`Connected` / `Reconnecting` / `Degraded` / `Disconnected`) mirroring the RabbitMQ publisher state enum (`research.md` §C7). |
| AC-11 | DLQ messages land on **per-source-topic** `{source_topic}.dlq` (not a single global topic). Headers: `x-lerian-dlq-error-class`, `x-lerian-dlq-retry-count`, `x-lerian-dlq-first-failure-at`, `x-lerian-dlq-source-topic`. Original event headers preserved verbatim. |
| AC-12 | Every `STREAMING_*` env var is documented in `.env.reference` in the same PR that introduces it (hard project rule per `CLAUDE.md`). Minimum set: `STREAMING_ENABLED`, `STREAMING_BROKERS`, `STREAMING_COMPRESSION`, `STREAMING_LINGER_MS`, `STREAMING_BATCH_MAX_BYTES`, `STREAMING_MAX_BUFFERED_RECORDS`, `STREAMING_RECORD_RETRIES`, `STREAMING_DELIVERY_TIMEOUT`, `STREAMING_REQUIRED_ACKS`, `STREAMING_CLIENT_ID`, `STREAMING_CLOSE_TIMEOUT`. |
| AC-13 | `streaming.NewNoopEmitter()` is a first-class constructor returning a fully functional no-op `Emitter` (swallows emits, `Healthy` returns nil, `Close` is a no-op). Mirrors `log.NewNop()` and `metrics.NewNopFactory()`. |
| AC-14 | `Close` is idempotent (safe to call ≥2 times) and flushes the producer within a bounded deadline configurable via `WithCloseTimeout(d)`. Post-`Close` calls to `Emit` return `ErrEmitterClosed` synchronously, before any I/O. |
| AC-15 | Every exported type, function, method, sentinel, and constant has godoc. Package-level godoc includes a ≤10-line end-to-end example, an env-var table, the 8-class error table, and a pointer to the shipped Grafana dashboard. |

---

## 7. Success metrics

### Launch (v1.0)

- `lib-commons` releases a tagged version with `commons/streaming/` v1.
- **At least one product** (midaz recommended — it has the highest event volume and the clearest domain-event semantics) adopts the package for **at least one event type** in production.
- Integration tests pass in CI against Redpanda container (`make test-integration` green).
- `dashboards/streaming-emitter.json` ships in the repo; Grafana import renders without errors; panel queries resolve against real metric names.

### 3 months post-launch

- **Two additional products** adopt the package (candidates: matcher, tracer, fetcher).
- **Zero production incidents** are attributable to `commons/streaming/` code itself. Incidents caused by caller misuse (e.g., emitting without a tenant context) do not count against this metric but must inform documentation and DX iteration (see `ring-pm-team:dev-report` follow-ups).
- **DLQ rate < 0.1%** of total emits in steady state across adopted products. A DLQ rate above this threshold is a signal for a caller-side or schema-side bug, not a library bug.

### 6 months post-launch

- **CloudEvents compatibility verified externally.** At least one downstream consumer — internal analytics pipeline or external partner — parses Lerian events using a non-Lerian CloudEvents SDK (Go, Python, or JS) with zero Lerian-specific glue code. Proven by a consumer-side integration test in a downstream repo or a partner-reported working integration.

---

## 8. Out of scope

Made explicit to prevent scope-creep in code review:

- **Schema Registry client.** No Avro, no Protobuf wire format, no Confluent-compat or Redpanda Schema Registry client. JSON schemas are reference files on disk.
- **Consumer-side library.** No `streaming.Consumer`, no `streaming.Subscription`, no `streaming.Handler`. Consumers use `kgo.Client` directly or a CloudEvents SDK.
- **RabbitMQ replacement.** AMQP stays for internal work queues. This package does not deprecate `commons/rabbitmq/`.
- **Cross-cluster mirroring.** Redpanda MirrorMaker-equivalent features are an operator concern. Not a library concern.
- **Event sourcing beyond DLQ replay.** No event-store semantics, no projection rebuild, no point-in-time replay.
- **Service-to-service auth inside the event envelope.** Events travel in-network; broker-level SASL covers transport. Payload-level auth is not in scope.
- **Frontend / UI.** N/A — library package.

---

## 9. Dependencies on other work

This feature is **additive**. No breaking API change is assumed in any dependency.

| Dependency | Usage | Assumed stability |
|---|---|---|
| `commons/outbox/` | `*Dispatcher` hosts a streaming-topic handler; `OutboxRepository.Create` persists on circuit-open. Payload ≤ 1 MiB, JSON-valid (already enforced). | Stable. No changes required. (See `research.md` §C3.) |
| `commons/circuitbreaker/` | `Manager.GetOrCreate(name, Config)` + `CircuitBreaker.Execute(fn)` wrap publish. `Manager.RegisterStateChangeListener` flips the outbox-fallback flag. | Stable. No changes required. (See `research.md` §C2.) |
| `commons/log.Logger` | All public constructors take `log.Logger`. Never `*zap.Logger` (CLAUDE.md invariant). | Stable. |
| `commons/opentelemetry/metrics.MetricsFactory` | Lazy-cached counters / gauges / histograms. `NewNopFactory()` for disabled envs. | Stable. |
| `commons/tenant-manager/core` | `GetTenantIDContext(ctx) string` (empty = miss) for tenant extraction. | Stable. |
| `commons.App` / `*Launcher` | `*streaming.Producer` implements `App`; wiring via `launcher.Add("streaming", producer)` in each consuming service. | Stable. (See `research.md` §C9.) |
| `commons/net/http.HealthWithDependencies` | Consumes `Healthy(ctx) error`. | Stable. |

If Gate 2 TRD discovery reveals any required change in these packages, it is flagged here as a merge-block. As of Gate 0 research, **no changes are required.**

---

## 10. Risks & mitigations

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | Redpanda outage > 10 min fills the outbox → Postgres write pressure, bloating pending-event tables. | Medium | High | SRE alerts on `streaming_outbox_depth` > 10k and per-tenant depth > 1k. `commons/outbox/` retention + dispatcher throughput tuning are pre-existing knobs. Document runbook in `dashboards/streaming-emitter.json` annotations. |
| R2 | Hot tenant skews partitions — one partition CPU-saturated, others idle. FIFO-per-tenant prevents simple rebalancing. | Medium | Medium | Default partition key is composite `tenant_id\|aggregate_id`; document salting escape hatch (`tenantID\|0..K-1`) that trades strict-per-tenant FIFO for load spreading. Per-partition throughput panel in the shipped Grafana dashboard with alert rule on coefficient-of-variation > 0.5. |
| R3 | CloudEvents binary-mode interop is untested in practice with Lerian's stack; a subtle header-naming mismatch could break every downstream consumer at once. | Low | High | Add a contract test in CI: use the reference `cloudevents/sdk-go` library to decode a produced event and assert every required context attribute round-trips. Ship before v1.0. |
| R4 | Version drift — franz-go's `ProducerLinger` default flipped 0 → 10 ms in v1.20. An upstream default flip could silently change latency characteristics for consumers of future versions. | Medium | Medium | Pin **all** producer options explicitly (`ProducerLinger(5*time.Millisecond)`, `RecordPartitioner(StickyKeyPartitioner(KafkaHasher(nil)))`, `ProducerBatchMaxBytes(1MiB)`, `MaxBufferedRecords(10000)`, `RecordRetries(10)`, `RecordDeliveryTimeout(30s)`). Pin `franz-go v1.20.7` in `go.mod`. Document this discipline in the package godoc. |
| R5 | A caller serializes a payload > 1 MiB; broker rejects; error surfaces late with low debuggability. | Medium | Low | Pre-flight size check returns `streaming.ErrPayloadTooLarge` synchronously before `Produce` is called. Aligned with `outbox.DefaultMaxPayloadBytes` so outbox-fallback path never accepts what Redpanda cannot eventually deliver. |

---

## 11. Timeline ballpark

This is **Small Track** under the pre-dev workflow classification (feature < 2 days). Realistically, for the documented surface — ~9 source files, circuit-breaker + outbox + DLQ wiring, CloudEvents header serialization, mock emitter, kfake-backed unit tests, testcontainers-backed integration tests, and a Grafana dashboard — **2–4 AI-agent days** is the working estimate. The tighter number comes out of Gate 3 task breakdown with AI-hours per task. This PRD does not commit to a delivery date; Gate 9 (delivery planning) produces the calendar.

---

## Appendix: cross-references

- Gate 0 research output (all file:line references, 8 franz-go error mappings, 20 locked decisions): `docs/pre-dev/streaming/research.md`.
- lib-commons invariants: `CLAUDE.md` (root of repo).
- `.env.reference` policy: `CLAUDE.md` — "When adding, removing, or changing any environment variable consumed by lib-commons, update `.env.reference` in the same change."
- Pattern-to-copy references (RabbitMQ publisher state enum, circuitbreaker nil-guard pattern, outbox Dispatcher App assertion): `research.md` §F.
