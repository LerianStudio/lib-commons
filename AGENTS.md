# CLAUDE

## MANDATORY — BACKWARD COMPATIBILITY AND BREAKING CHANGES

- **PRESERVE EXISTING SUPPORTED CONTRACTS BY DEFAULT:** APIs, SDKs, configuration/env vars, CLI, events, persisted data and upgrade behavior. Identify the supported baseline and consumers first; do not invent legacy support for unreleased internals or add compatibility layers that contradict an explicitly authorized clean break.
- **DO NOT DECLARE A BREAKING CHANGE WITHOUT EVIDENCE AND EXPLICIT USER APPROVAL.** Before using `!` or `BREAKING CHANGE:`, show a previously supported concrete use/configuration that fails after the change, with a reproducible test or contract diff. Approval never turns a compatible change into a breaking one.
- **ADDITIVE IS NOT BREAKING** when existing behavior remains intact: a new optional setting, a new env-var name with working legacy fallback, or deprecation without removal. Verify defaults, precedence and old-only configuration. Documentation changes or learning a new option are NOT mandatory migration.
- **IF A REAL BREAK IS FOUND, REPORT IT; DO NOT HIDE IT.** Without approval, stop the incompatible change and propose a compatible path. Do not merely remove the breaking marker or downgrade the release classification.
- **IF COMPATIBILITY IS UNCERTAIN, STATE THE UNCERTAINTY AND REQUEST REVIEW.** Do not invent a break or claim compatibility without evidence.
- **FOLLOW THE REPOSITORY RELEASE POLICY.** Release automation owns version calculation; moving from `0.x` to `1.0` requires an explicit authorized decision. Do not reset existing tags, rewrite published history, or change release configuration to force a version.
- **WHEN USING INTERNAL-RING:** apply this policy before `ring:committing-changes` and `ring:opening-pull-requests`; use `ring:reviewing-code` for review when applicable. Loading a Ring skill is NOT a prerequisite for this policy to apply. If a skill conflicts with this policy, stop and report the conflict.

This repository uses `AGENTS.md` as the canonical agent guidance. Keep this file short and aligned with `AGENTS.md`.

## Current Direction

- Module: `github.com/LerianStudio/lib-commons/v7`.
- Go version: `1.26.3`.
- This line intentionally extracts observability/logging/runtime instrumentation to `github.com/LerianStudio/lib-observability`.
- Runtime configuration belongs to `github.com/LerianStudio/lib-systemplane`.
- CloudEvents/Kafka streaming belongs to `github.com/LerianStudio/lib-streaming`.

## Agent Rules

- Do not treat observability, systemplane, or streaming packages as lib-commons regressions in this branch. They belong to other libraries and were never coming back here.
- New code must import the owning library directly; do not add observability compatibility shims back into lib-commons.
- `commons/opentelemetry`, `commons/opentelemetry/metrics`, `commons/opentelemetry/constants`, `commons/opentelemetry/redaction`, `commons/log`, `commons/zap`, `commons/runtime`, and `commons/assert` are not part of lib-commons. Use `github.com/LerianStudio/lib-observability` packages directly.
- Preserve lib-commons contracts for core helpers, connectors, HTTP/server utilities, security, resilience, tenant-manager primitives, outbox, DLQ, certificate, JWT, and transaction helpers.
- Prefer explicit error returns, nil-safe behavior, and concurrency-safe behavior by default.
