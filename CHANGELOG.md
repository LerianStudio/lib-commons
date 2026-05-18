# Lib-commons Changelog

## [5.2.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.2.0)

- **Features:**
  - Removed observability boundaries and deprecated commons observability shims, migrating to `lib-observability`.
  - Added native MongoDB transaction support in the outbox module.
  - Introduced a bridge between `AttrBag` and `lib-observability`.

- **Fixes:**
  - Addressed Docker CVEs in testcontainers for improved security.
  - Enforced fail-closed termination for license compliance.
  - Made schema-per-tenant migration parser-safe in the outbox/postgres module.
  - Updated unit tests to validate shim contracts and improve coverage.

- **Improvements:**
  - Bumped Go toolchain to `1.26.3`.
  - Deprecated commons telemetry and logging middleware, aligning with the new observability strategy.
  - Improved code readability with additional blank lines.

Contributors: @bedatty, @fredcamaral, @gandalf-at-lerian, @jeffersonrodrigues92, @qnen.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.1.1...v5.2.0)

