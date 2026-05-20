# Lib-commons Changelog

## [5.3.0]

- **Changes:**
  - **mongo**: Migrated from `go.mongodb.org/mongo-driver` v1 to `go.mongodb.org/mongo-driver/v2` (v2.6.0) in-place across `commons/mongo/`, `commons/outbox/mongo/`, `commons/tenant-manager/core/`, and `commons/tenant-manager/mongo/`. Public function signatures are unchanged, but the concrete `*mongo.Database`/`*mongo.Client` types now resolve to v2. Consumers updating to v5.3.0 must adapt their own MongoDB query code for the following v2 API changes:
    - `options.UpdateOne()`, `options.Find()`, and `options.Index()` now return builder types (`*XxxOptionsBuilder`). Fluent `.SetX()` chains are unchanged.
    - `primitive.ObjectID` → `bson.ObjectID`, `primitive.DateTime` → `bson.DateTime`, `primitive.NewObjectID()` → `bson.NewObjectID()` (the `primitive` package was merged into `bson` in v2).
    - `mongo.Connect` no longer accepts a `context.Context` parameter **and no longer pings the server to validate reachability**. If you relied on `Connect` to fail on unreachable deployments, call `client.Ping(ctx, nil)` explicitly after connecting.
    - `Collection.Distinct` returns `*DistinctResult` instead of `([]any, error)` — use `result.Err()` followed by `result.Decode(&values)`.
    - `WithTransaction` callbacks receive `context.Context` instead of `mongo.SessionContext`. Use `mongo.SessionFromContext(ctx)` if you need the session.

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

