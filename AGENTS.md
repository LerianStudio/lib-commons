# CLAUDE

This repository uses `AGENTS.md` as the canonical agent guidance. Keep this file short and aligned with `AGENTS.md`.

## Current Direction

- Module: `github.com/LerianStudio/lib-commons/v5`.
- Go version: `1.26.3`.
- This v5 minor line intentionally extracts observability/logging/runtime instrumentation to `github.com/LerianStudio/lib-observability`.
- Runtime configuration belongs to `github.com/LerianStudio/lib-systemplane`.
- CloudEvents/Kafka streaming belongs to `github.com/LerianStudio/lib-streaming`.

## Agent Rules

- Do not treat removed v5 observability, systemplane, or streaming packages as lib-commons regressions in this branch.
- New code must import the owning library directly; do not add observability compatibility shims back into lib-commons.
- `commons/opentelemetry`, `commons/opentelemetry/metrics`, `commons/opentelemetry/constants`, `commons/opentelemetry/redaction`, `commons/log`, `commons/zap`, `commons/runtime`, and `commons/assert` have been removed from lib-commons/v5. Use `github.com/LerianStudio/lib-observability` packages directly.
- Preserve v5 minor-line lib-commons contracts for core helpers, connectors, HTTP/server utilities, security, resilience, tenant-manager primitives, outbox, DLQ, certificate, JWT, and transaction helpers.
- Prefer explicit error returns, nil-safe behavior, and concurrency-safe behavior by default.
