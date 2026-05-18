<table border="0" cellspacing="0" cellpadding="0">
  <tr>
    <td><img src="https://github.com/LerianStudio.png" width="72" alt="Lerian" /></td>
    <td><h1>Lib Commons</h1></td>
  </tr>
</table>

---

## Description

<!-- Summarize what this PR changes and why. Mention the package(s) affected
     (assert, certificate, circuitbreaker, dlq, log, mongo, net, opentelemetry,
     postgres, rabbitmq, redis, runtime, server, streaming, systemplane,
     transaction, webhook, zap, ...). -->

## Type of Change

- [ ] `feat`: New feature or capability
- [ ] `fix`: Bug fix
- [ ] `perf`: Performance improvement
- [ ] `refactor`: Internal restructuring with no behavior change
- [ ] `docs`: Documentation only (README, docs/, inline comments)
- [ ] `style`: Formatting, whitespace, naming (no logic change)
- [ ] `test`: Adding or updating tests
- [ ] `ci`: CI pipeline or workflow changes
- [ ] `build`: Build system, Go module dependencies
- [ ] `chore`: Maintenance, config, tooling
- [ ] `revert`: Reverts a previous commit
- [ ] `BREAKING CHANGE`: Consumers must update their integration

## Breaking Changes

<!-- If applicable, describe exactly what breaks (public API signatures,
     exported types, default behaviors, configuration keys) and how downstream
     services should migrate. Update MIGRATION_MAP.md when renaming or removing
     symbols. Remove this section if not applicable. -->

None.

## Testing

- [ ] `make test-unit` passes
- [ ] `make test-integration` passes if integration paths are exercised
- [ ] `make lint` passes
- [ ] `make sec` passes (gosec)
- [ ] Coverage threshold respected (see Go Combined Analysis)

**Test evidence / Actions run:** <!-- Optional: link to a CI run or screenshot -->

## Architectural Checklist

- [ ] No `panic()` in production paths — uses `assert` helpers or wrapped errors
- [ ] Errors wrapped with `%w`, never swallowed
- [ ] Nil-safe and concurrency-safe by default
- [ ] Timestamps use `time.Now().UTC()`
- [ ] Public v5 API contracts preserved (or `BREAKING CHANGE` flagged above)
- [ ] Exported docs match behavior (godoc + README/CLAUDE.md if user-facing)
- [ ] No high-cardinality telemetry labels introduced
- [ ] `.env.reference` updated when env vars change
- [ ] Structured logging only (`Log(ctx, level, msg, fields...)`)

## Related Issues

Closes #
