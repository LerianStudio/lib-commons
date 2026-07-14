# Lib-commons Changelog

## [5.10.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.10.0)

Features:
- Add content-addressed idempotent writes to the outbox functionality. (@fredcamaral)

Fixes:
- Harden security headers in Scalar documentation to improve protection. (@fredcamaral)

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.9.0...v5.10.0)

---

## [5.9.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.9.0)

Features:
- Implement per-module messaging and streaming configuration within `TenantConfig`. (@jeffersonrodrigues92)
- Restore legacy `Messaging/GetRabbitMQConfig` alongside the new per-module configuration. (@jeffersonrodrigues92)
- Add functionality to list S3 object keys by prefix with pagination support. (@jeffersonrodrigues92)
- Introduce conditional S3 blob creation using `IfNoneMatch` for immutable blobs. (@jeffersonrodrigues92)
- Enable tenant-scoped blob storage operations in S3, including upload, download, delete, and existence checks. (@jeffersonrodrigues92)

Fixes:
- Correct parsing of `[]string` environment fields to prevent panics in `SetConfigFromEnvVars`. (@fredcamaral)
- Address panic issue in `SetConfig` slice handling. (@fredcamaral)
- Parse migration version at `uint` width to prevent errors in Postgres. (@andreimatiazi)
- Disambiguate version-ahead migration error messages in Postgres. (@andreimatiazi)
- Resolve error on truncated S3 list pages without a continuation token. (@jeffersonrodrigues92)
- Normalize S3 bucket storage and provide explicit error handling for nil downloads. (@jeffersonrodrigues92)

Improvements:
- Extract `resolveRabbitMQConfig` helper function for better code organization in tenant management. (@jeffersonrodrigues92)
- Bump `gofiber` dependency to patch `CVE-2026-45045/44332` and ignore unreachable `x/crypto` openpgp advisory. (@fredcamaral)

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.8.0...v5.9.0)

---

## [5.8.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.8.0)

- Features:
  - Add `cache.invalidate` hot-reload event in tenant-manager.

- Fixes:
  - Guard `InvalidateClientCache` against nil receiver in tenant-manager.

Contributors: @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.7.0...v5.8.0)

---

## [5.8.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.8.0)

- Features:
  - Add `tenant.cache.invalidate` event type and `CacheInvalidatePayload` for operator-triggered per-service cache hot-reload.
  - Add dispatcher `handleCacheInvalidate`: evicts the tenant from tier-1 (local) and tier-2 (client) caches and eagerly reloads when the tenant is owned locally.
  - Add `TenantLoader.InvalidateClientCache` to evict the tenant-manager client (tier-2) config cache.

- Fixes:
  - Close the tier-2 staleness gap: `removeTenant` now also invalidates the client (tier-2) config cache, so `tenant.credentials.rotated` and `tenant.service.disassociated` no longer leave a stale tier-2 entry.

Contributors: @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.7.0...v5.8.0)

---

## [5.7.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.7.0)

- Features:
  - Add `GetTenantMetadata` client method.

- Fixes:
  - Reset circuit breaker on 4xx round-trips.
  - Remove data race in HTTP client lazy init.

Contributors: @alexgarzao, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.6.1...v5.7.0)

---

## [5.6.1](https://github.com/LerianStudio/lib-commons/releases/tag/v5.6.1)

- Fixes:
  - Genericize shared error-code example in network module.

Contributors: @fredcamaral, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.6.0...v5.6.1)

---

## [5.6.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.6.0)

- **Features**
  - Added Huma OpenAPI wrapper and RFC 9457 problem model.
  - Propagated `tenant.id` via OTel baggage.

- **Fixes**
  - Hardened Huma OpenAPI wrapper edges, including prefix normalization, nil `ErrorDetail`, and status clamping.
  - Improved OpenAPI wrapper by escaping docs HTML and guarding against nils.
  - Merged `tenant.id` into existing OTel baggage.
  - Logged publish failures and added optional `OnInvalid`/`OnFailed` lifecycle callbacks in the outbox.
  - Ensured `OnInvalid` callback is invoked only after successful `MarkInvalid`.

Contributors: @augusto-draxx, @fredcamaral, @gandalf-at-lerian, @lerian-studio, @qnen.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.5.3...v5.6.0)

---

## [5.5.3](https://github.com/LerianStudio/lib-commons/releases/tag/v5.5.3)

- Fixes:
  - Fix(outbox): Invoke `OnInvalid` callback only after successful `MarkInvalid`.
  - Fix(outbox): Log publish failures and add optional `OnInvalid`/`OnFailed` lifecycle callbacks.
  - Fix(deps): Retract `v5.5.2` published outside release flow.

Contributors: @bedatty, @gandalf-at-lerian.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.5.2...v5.5.3)

---

## [5.5.1](https://github.com/LerianStudio/lib-commons/releases/tag/v5.5.1)

- Fixes:
  - Remove wildcard retrocompat from tenant manager channel subscription.

Contributors: @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.5.0...v5.5.1)

---

## [5.5.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.5.0)

- Features:
  - Dispatch from per-tenant pools in the outbox/postgres component.
  - Add `TenantPoolResolver` for pool-per-tenant dispatch in the outbox.
  - Add dual-driver SQLSTATE error classification in the postgres component.

- Fixes:
  - Return `ErrClientRequired` and ensure nil-safe presence guard in the outbox/postgres component.

- Improvements:
  - Enforce unit coverage threshold and fix integration TLS environment in CI.
  - Align `stubResolverDB` method set using `gofmt` in the tenant-manager component.

Contributors: @fredcamaral, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.4.1...v5.5.0)

---

## [5.5.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.5.0)

- **Features:**
  - Added dual-driver SQLSTATE error classification to `commons/postgres`. New exported helpers `SQLState`, `Constraint`, and `DriverMessage` extract the SQLSTATE code, violated constraint name, and raw driver message from both pgx (`*pgconn.PgError`) and lib/pq (`*pq.Error`) errors, unwrapping through wrapped chains via `errors.As`. New predicates `IsUniqueViolation` (23505), `IsForeignKeyViolation` (23503), `IsCheckViolation` (23514), and `IsUndefinedTable` (42P01) classify common constraint failures. All helpers are nil-safe: nil and non-driver errors classify false and report absent. Classification does not traverse `*SanitizedError` by design, preserving its credential-scrubbing contract.
  - Promoted `github.com/lib/pq` from an indirect to a direct dependency to support lib/pq error classification (no new modules added; already present transitively at v1.12.3).

- **Improvements:**
  - `commons/tenant-manager/core`: `IsTenantNotProvisionedError` now uses the typed `postgres.IsUndefinedTable` check (which reads the driver SQLSTATE field through both pgx and lib/pq) as its primary detection path, ahead of the existing string fallbacks. Behavior is unchanged for existing callers; the string fallbacks are retained for errors whose driver type was flattened to text upstream.

- **Contributors:** @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.4.1...v5.5.0)

---

## [5.4.1](https://github.com/LerianStudio/lib-commons/releases/tag/v5.4.1)

- Fixes:
  - Listener now subscribes to environment-scoped channels only.

Contributors: @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.4.0...v5.4.1)

---

## [5.4.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.4.0)

- **Features:**
  - Added `CurrentEnv` and `TenantEventsChannel` helpers to enhance environment and event handling capabilities.

- **Contributors:** @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.3.3...v5.4.0)

---

## [Unreleased]

- Fixes:
  - Multi-tenant event listener (`commons/tenant-manager/event.TenantEventListener`) now subscribes only to the env-scoped channel `tenant-events:{env}:` resolved via `commons.CurrentEnv()`. The previous wildcard `PSubscribe("tenant-events:*")` leaked events across environments (staging consumers receiving production events and vice versa) and defeated the env-scoped channel work shipped in v5.4.0.
    - Behavior change: `Start()` now requires `ENVIRONMENT_NAME` (or `ENV_NAME`) to be set in the multi-tenant consumer process. Apps that wire `NewTenantEventListener` but do not set either env var will fail to start with an error referencing both variable names. Single-tenant apps that never wire the listener are unaffected.
    - `event.SubscriptionPattern` is retained for backwards compatibility with custom subscriber implementations but is marked deprecated; the bundled listener no longer references it.
    - Affected consumers (16) that wire `NewTenantEventListener`: br-ccs, br-sfn, br-sisbajud, br-spb-bc-correios, br-spi, br-sta, fetcher, flowker, go-boilerplate-ddd, lerian-notification, midaz, plugin-br-bank-transfer, plugin-fees, reporter, tracer, underwriter. Each needs `ENVIRONMENT_NAME` (or `ENV_NAME`) set to `staging` or `production` on the multi-tenant pod before bumping to v5.4.1.

- Features:
  - Added the pool-per-tenant outbox tenancy strategy to `commons/outbox/postgres`. New `outbox.TenantPoolResolver` interface, `postgres.NewMultiTenantRepository` / `MultiTenantConfig` wiring, `postgres.NewManagerPoolResolver` (tenant-manager-backed resolver), and `postgres.NoopTenantResolver`. Each tenant's outbox rows live in a dedicated database (tenant-manager "isolated" model); the pool is the isolation boundary, so there is no schema scan and no tenant column. Pool resolution is tiered and fail-closed (context-installed pool → resolver → `ErrTenantPoolUnavailable`, never the root pool), `ListTenants` prefers an explicit `TenantDiscoverer` over the resolver, the platform default tenant routes to the root pool, and a tenant database missing the outbox table is skipped-and-logged (WARN) with a 60s TTL presence cache. The `outbox_events` table is provisioned by the consumer's per-tenant migration runner using the same DDL as the schema track. Schema-per-tenant remains supported but is now documented as legacy; new adopters should prefer pool-per-tenant.
  - Added `commons.CurrentEnv()` and `commons.MustCurrentEnv()` plus `EnvStaging` / `EnvProduction` helpers for consistent environment detection across services.
  - Added `commons/events.TenantEventsChannel(env)` and `TenantEventsChannelPrefix` to derive the canonical per-environment Redis Pub/Sub channel name for tenant lifecycle events.
  - Added 6 boolean security toggles in `commons/security.go` replacing the prior override framework: `AllowInsecureTLS`, `RateLimitEnabled` (default false — explicit opt-in), `AllowRateLimitFailOpen`, `AllowCORSWildcard`, `AllowInsecureOTEL`, `AllowWebhookPrivateNet`. Truthy values: `true`, `1`, `yes`, `on` (case-insensitive). Internal callers (`commons/redis`, `commons/postgres`, `commons/rabbitmq`, `commons/mongo`, `commons/net/http/ratelimit`, `commons/net/http/withCORS`, `commons/webhook`) now use direct boolean checks with WARN log on bypass for audit.

- Removed (breaking, config-level):
  - `commons/security_override.go` (and the historical `commons/security_tier.go`) are deleted.
  - `CheckSecurityRule`, `EnforceSecurityRule`, `ReadSecurityOverride`, `SecurityOverride`, `ErrSecurityViolation`, `EffectiveSecurityTier`, `SecurityTier`, `TierStrict`, `TierModerate`, `TierPermissive`, `CurrentTier`, `IsSecurityEnforcementEnabled`, and the `RuleXxx` constants are removed.
  - `SECURITY_TIER` and `SECURITY_ENFORCEMENT` environment variables are silently ignored at runtime. No warning, no fallback.
  - Required `"reason"` text in `ALLOW_*="..."` deploys is dropped. Operators using prior `ALLOW_*="<reason>"` deploys MUST switch to `ALLOW_X=true`; any non-boolean value (including prior audit strings) is now treated as `false`.
  - BREAKING (operational): RATE_LIMIT_ENABLED default flipped from true to
    false. Apps that previously relied on the silent default to enable rate
    limiting MUST now set RATE_LIMIT_ENABLED=true in their deploy configs.
    Affected consumers (per gh search/code): br-sfn, br-sisbajud,
    br-spb-bc-correios, br-spb, br-spi, br-sta, go-boilerplate-ddd,
    lerian-notification, matcher, plugin-access-manager, plugin-auth,
    plugin-br-bank-transfer, plugin-identity, underwriter.
  - BREAKING (config): ALLOW_RATELIMIT_DISABLED env var removed entirely.
    Deployments that previously set ALLOW_RATELIMIT_DISABLED=true to bypass
    the rate limiter MUST drop that variable (rate limit is now off by
    default — RATE_LIMIT_ENABLED defaults to false). Affected helm/gitops
    references (13 known):
      helm: charts/fetcher/{worker,values}, charts/midaz/ledger,
            charts/plugin-br-pix-indirect-btg/{inbound,outbound,pix,reconciliation},
            charts/reporter/{manager,worker}
      helm-internal: charts/tenant-manager/configmap
      lerian-aws-gitops: environments/staging/helmfile/{fetcher-mt,reporter-mt,reporter}
      midaz-firmino-gitops: environments/anacleto/.../midaz
    Apps that previously relied on the silent default-true to enable rate
    limiting MUST now set RATE_LIMIT_ENABLED=true. Affected (per gh
    search/code): br-sfn, br-sisbajud, br-spb-bc-correios, br-spb, br-spi,
    br-sta, go-boilerplate-ddd, lerian-notification, matcher,
    plugin-access-manager, plugin-auth, plugin-br-bank-transfer,
    plugin-identity, underwriter.

- Migration notes:
  - Code consuming the removed APIs must switch to the new boolean helpers in `commons/security.go`. No external app in the LerianStudio org references the deleted framework (verified via repo search).
  - The `ALLOW_WEBHOOK_PRIVATE_NETWORK` env-var name is preserved; only the semantics change to boolean.
  - `RATE_LIMIT_ENABLED` now defaults to `false`. Apps that need rate limiting MUST explicitly set `RATE_LIMIT_ENABLED=true` in their helm / `.env` configs.
  - TLS enforcement no longer depends on environment: every internal client now requires TLS unless `ALLOW_INSECURE_TLS=true` is set, regardless of `ENV_NAME` / `ENV` / `GO_ENV`.

Contributors: @jeffersonrodrigues92, @lerian-studio.

---

## [5.3.3](https://github.com/LerianStudio/lib-commons/releases/tag/v5.3.3)

- Fixes:
  - Equalized generic key in multi-module PG/Mongo resolution for tenant-manager middleware.

Contributors: @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.3.2...v5.3.3)

---

## [5.3.2](https://github.com/LerianStudio/lib-commons/releases/tag/v5.3.2)

- Fixes:
  - Added `CACertBase64` to `TenantPubSubRedisConfig` to improve security and configuration flexibility.

Contributors: @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.3.1...v5.3.2)

---

## [5.3.1](https://github.com/LerianStudio/lib-commons/releases/tag/v5.3.1)

- Fixes:
  - Build HTTP transport explicitly to avoid HTTP/2 leak in `lib-commons`.

Contributors: @brunobls, @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.3.0...v5.3.1)

---

## [5.3.0](https://github.com/LerianStudio/lib-commons/releases/tag/v5.3.0)

- Features:
  - Migrate from `mongo-driver` `v1` to `v2`.

- Fixes:
  - Address CodeRabbit feedback on PR review.
  - Record metrics through builders in the outbox component.

- Improvements:
  - Use bounded context for all Disconnect call sites in the mongo module.
  - Delegate redaction constants for better observability.

Contributors: @bedatty, @fredcamaral, @jeffersonrodrigues92, @lerian-studio.

[Compare changes](https://github.com/LerianStudio/lib-commons/compare/v5.2.1...v5.3.0)

---

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

