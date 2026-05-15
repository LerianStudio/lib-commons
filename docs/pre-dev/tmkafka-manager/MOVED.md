# Moved to lib-streaming

> **Pre-dev docs for `tmkafka-manager` migrated to `lib-streaming` on 2026-05-14.**

Per architectural decision **D14** (locked in the brief on 2026-05-14), the primary code home for the Kafka multi-tenant client primitive is `lib-streaming`, not `lib-commons`. The pre-dev documentation followed the code.

## New location

```
/Users/jeffersonrodrigues/workspace/lib-streaming/docs/pre-dev/tmkafka-manager/
├── research.md
├── research-codebase.md
├── research-external.md
├── research-framework.md
├── prd.md
├── trd.md
├── tasks.md
├── delivery-planning.md
├── delivery-roadmap.json
└── workflow-state.json
```

## What lib-commons gets (small surface)

Phase 1 lands a small additive surface in `lib-commons`:

- `commons/tenant-manager/core/types.go` — add `KafkaConfig` struct + `MessagingConfig.Kafka` field + `GetKafkaConfig()` + `HasKafka()` helpers
- `commons/tenant-manager/event/dispatcher.go` — add `WithKafka(ConnectionCloser) DispatcherOption` (interface-based; no import of lib-streaming)
- `commons/tenant-manager/event/dispatcher_helpers.go` — extend `removeTenant` cascade

That's all. Roughly 3-5 hours of the total ~22h Phase 1 effort. Branch: `feat/kafka-config-schema`.

## Published architecture brief (single source of truth)

- 📄 Brief (EN): https://alfarrabio.lerian.net/jeff/lib-streaming-mt-brief.html
- 📋 PRD (EN): https://alfarrabio.lerian.net/jeff/lib-streaming-mt-prd.html
- ⚙️ TRD (EN): https://alfarrabio.lerian.net/jeff/lib-streaming-mt-trd.html
- 📄 Brief (PT-BR): https://alfarrabio.lerian.net/jeff/lib-streaming-mt-brief-pt-br.html
- 📋 PRD (PT-BR): https://alfarrabio.lerian.net/jeff/lib-streaming-mt-prd-pt-br.html
- ⚙️ TRD (PT-BR): https://alfarrabio.lerian.net/jeff/lib-streaming-mt-trd-pt-br.html

## Implementation start

`ring:dev-cycle` invocation on **2026-06-29** against `lib-streaming/docs/pre-dev/tmkafka-manager/tasks.md`.
