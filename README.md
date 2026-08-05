# lib-commons

`lib-commons` is Lerian's shared Go toolkit for service primitives, connectors, HTTP/server utilities, security, resilience, tenant-manager primitives, outbox, DLQ, certificate, JWT, and transaction helpers.

The current API surface is published on the **v5 minor line**. The v5 split-library line intentionally extracts observability/logging/runtime instrumentation to `lib-observability`, runtime configuration to `lib-systemplane`, and CloudEvents/Kafka streaming to `lib-streaming`.

---

**Migrating from older packages?**  
Use the library boundary table below as the canonical direction for renamed, redesigned, removed, or extracted APIs in the split-library `lib-commons` line. Observability, logging, runtime, and assertion APIs are no longer exposed from `lib-commons`; import the owning library directly.

---

## Requirements

- Go `1.26.3` or newer

## Installation

```bash
go get github.com/LerianStudio/lib-commons/v6
```

## Lerian Library Boundaries

Lerian's shared platform code is split across four libraries:

| Library | Ownership |
|---------|-----------|
| `github.com/LerianStudio/lib-commons` | Core helpers, connectors, HTTP/server utilities, security, resilience, tenant-manager primitives, outbox, DLQ, certificate, JWT, transaction helpers |
| `github.com/LerianStudio/lib-observability` | Logging, zap adapter, tracing, metrics, redaction, panic instrumentation, assertions, observability constants |
| `github.com/LerianStudio/lib-systemplane` | Runtime configuration, hot reload, systemplane admin routes, tenant-scoped runtime knobs, systemplane contract tests |
| `github.com/LerianStudio/lib-streaming` | CloudEvents/Kafka streaming, event emitters, streaming DLQs, outbox replay for streaming events |

## What is in this library

### Core (`commons`)

- `app.go`: `Launcher` for concurrent app lifecycle management with `NewLauncher(opts...)` and `RunApp` options
- `context.go`: request-scoped logger/tracer/metrics/header-id tracking via `ContextWith*` helpers, safe timeout with `WithTimeoutSafe`, span attribute propagation
- `errors.go`: standardized business error mapping with `ValidateBusinessError`
- `utils.go`: UUID generation (`GenerateUUIDv7` returns error), struct-to-JSON, map merging, CPU/memory metrics, internal service detection
- `stringUtils.go`: accent removal, case conversion, UUID placeholder replacement, lowercase hexadecimal SHA-256 hashing for strings (`HashSHA256`) and byte slices (`HashSHA256Bytes`), server address validation
- `time.go`: date/time validation, range checking, parsing with end-of-day support
- `os.go`: environment variable helpers (`GetenvOrDefault`, `GetenvBoolOrDefault`, `GetenvIntOrDefault`, `GetenvDurationOrDefault`), struct population from env tags via `SetConfigFromEnvVars`
- `commons/constants`: shared constants for datasource status, errors, headers, metadata, pagination, transactions, and obfuscation values

### Observability and logging

Observability has moved to `github.com/LerianStudio/lib-observability`. Use that library directly for logging, zap adapters, tracing, metrics, redaction, panic instrumentation, assertions, and observability constants.

The former `commons/opentelemetry`, `commons/opentelemetry/metrics`, `commons/opentelemetry/constants`, `commons/opentelemetry/redaction`, `commons/log`, `commons/zap`, `commons/runtime`, and `commons/assert` packages have been removed from `lib-commons/v5`. Consumers must import `github.com/LerianStudio/lib-observability/{log,zap,assert,runtime,tracing,metrics,constants,redaction}` directly.

### Data and messaging connectors

- `commons/postgres`: `Config`-based constructor (`New`), `Resolver(ctx)` for dbresolver access, `Primary()` for raw `*sql.DB`, `NewMigrator` for schema migrations, backoff-based lazy-connect; dual-driver SQLSTATE error classification that unwraps both pgx (`*pgconn.PgError`) and lib/pq (`*pq.Error`) through wrapped chains via `errors.As` — accessors `SQLState(err) (string, bool)` / `Constraint(err) (string, bool)` / `DriverMessage(err) (string, bool)` and predicates `IsUniqueViolation` (23505) / `IsForeignKeyViolation` (23503) / `IsCheckViolation` (23514) / `IsUndefinedTable` (42P01); all nil-safe (nil or non-driver errors classify false / report absent)
- `commons/mongo`: `Config`-based client with functional options (`NewClient`), URI builder (`BuildURI`), `Client(ctx)`/`ResolveClient(ctx)` for access, `EnsureIndexes` (variadic), TLS support, credential clearing
- `commons/redis`: topology-based `Config` (standalone/sentinel/cluster), GCP IAM auth with token refresh, distributed locking via `LockManager` interface (`NewRedisLockManager`, `LockHandle`), `SetPackageLogger` for diagnostics, pool controls including `ConnectionOptions.MaxActiveConns`, TLS defaults to a TLS1.2 minimum floor with `AllowLegacyMinVersion` as an explicit temporary compatibility override, and TLS without a custom CA uses the host system trust store
- `commons/rabbitmq`: connection/channel/health helpers for AMQP with `*Context()` variants, `HealthCheck() (bool, error)`, `Close()`/`CloseContext()`, confirmable publisher with broker acks and auto-recovery, DLQ topology utilities, and health-check hardening (`AllowInsecureHealthCheck`, `HealthCheckAllowedHosts`, `RequireHealthCheckAllowedHosts`)
- `commons/dlq`: Redis-backed dead letter queue with `New(conn, keyPrefix, maxRetries, opts...)` returning nil when conn is nil (all methods guard nil receiver via `ErrNilHandler`); key operations: `Enqueue` (RPush, stamps `CreatedAt`/`MaxRetries` on first enqueue), `Dequeue` (LPop, at-most-once), `QueueLength`, `ScanQueues` (non-blocking SCAN for background consumers without tenant context), `PruneExhaustedMessages` (dequeue-discard-reenqueue cycle up to limit), `ExtractTenantFromKey`; tenant-scoped Redis keys (`"<prefix><tenantID>:<source>"`), backoff via exponential-with-jitter (base 30s, floor 5s, AWS Full Jitter); functional options `WithLogger`/`WithTracer`/`WithMetrics`/`WithModule`; `DLQMetrics` interface (`RecordRetried`/`RecordExhausted`, nil-safe); `NewConsumer(handler, retryFn, opts...) (*Consumer, error)` for background poll loop — `Run(ctx)` blocks until stop, `Stop()` idempotent, `ProcessOnce(ctx)` exported for tests; consumer options `WithConsumerLogger`/`WithConsumerTracer`/`WithConsumerMetrics`/`WithConsumerModule`/`WithPollInterval`/`WithBatchSize`/`WithSources`; sentinel errors `ErrNilHandler`, `ErrNilRetryFunc`, `ErrMessageExhausted`
- Streaming has moved to `github.com/LerianStudio/lib-streaming`; runtime configuration has moved to `github.com/LerianStudio/lib-systemplane`.

### HTTP and server utilities

- `commons/net/http`: Fiber HTTP helpers -- response (`Respond`/`RespondStatus`/`RespondError`/`RespondErrorEnvelope`/`RenderError`; `RespondErrorEnvelope` preserves a caller-supplied status code and machine-readable error envelope), health (`Ping`/`HealthWithDependencies`), SSRF-protected reverse proxy (`ServeReverseProxy` with `ReverseProxyPolicy`), pagination (offset/opaque cursor/timestamp cursor/sort cursor), validation (`ParseBodyAndValidate`/`ValidateStruct`/`ValidateSortDirection`/`ValidateLimit`), context/ownership (`ParseAndVerifyTenantScopedID`/`ParseAndVerifyResourceScopedID`), middleware (`WithHTTPLogging`/`WithGrpcLogging`/`WithCORS`/`WithBasicAuth`/`NewTelemetryMiddleware`), `FiberErrorHandler`
- `commons/net/http/ratelimit`: Redis-backed distributed rate limiting middleware for Fiber — `New(conn, opts...)` returns a `*RateLimiter` (nil when disabled, nil-safe for pass-through), `WithDefaultRateLimit(conn, opts...)` as a one-liner that wires `New` + `DefaultTier` into a ready-to-use `fiber.Handler`, fixed-window counter via atomic Lua script (INCR + PEXPIRE), `RedisStorage.Increment(ctx,key,window)` as the storage-only atomic primitive, `WithRateLimit(tier)` for static tiers, `WithDynamicRateLimit(TierFunc)` for per-request tier selection, `MethodTierSelector` for write-vs-read split, preset tiers (`DefaultTier` / `AggressiveTier` / `RelaxedTier`) configurable via env vars, identity extractors (`IdentityFromIP` / `IdentityFromHeader` / `IdentityFromIPAndHeader` — uses `#` separator to avoid conflict with IPv6 colons), fail-open/fail-closed policy, `WithOnLimited` callback, `WithExceededHandler` for caller-controlled 429 response bodies after standard rate-limit headers are set, and standard `X-RateLimit-*` / `Retry-After` headers; also exports `RedisStorage` (`NewRedisStorage`) for use with third-party Fiber middleware
- `commons/net/http/idempotency`: atomic at-most-once request middleware for Fiber — the shipped `New(conn, opts...) *Middleware` go-redis API remains fail-open by default and returns nil for a nil connection; `NewWithStore(store, opts...) *Middleware` accepts a backend-neutral `Store` and always fails closed on a missing/errored backend; `NewRedisStore(conn)` exposes the built-in Redis adapter for store-contract composition; `Store` preserves middleware-owned opaque bytes through only atomic `Acquire`, compare-safe `Complete`, and compare-safe `Release`, with reusable adapter contract tests in `idempotency/idempotencytest.Run`; applies only to mutating methods (POST/PUT/PATCH/DELETE), passes GET/HEAD/OPTIONS unconditionally; reads `Idempotency-Key` (missing key passes through); key length defaults to 256 UTF-8 bytes; duplicate outcomes are matching exact response replay (status, content type, body, and multi-value headers) with `Idempotency-Replayed: true`, matching in-flight → 409 `IDEMPOTENCY_CONFLICT` plus `Retry-After: 1`, and different method/path/body → 422 `IDEMPOTENCY_KEY_REUSE`; an exact response that cannot be captured, encoded, persisted, or decoded fails closed with 503 `IDEMPOTENCY_UNAVAILABLE` instead of fabricating success; successful responses and handler failure/5xx releases are owner-compared so an expired acquisition cannot overwrite or delete its replacement; 4xx responses are cached by default and may instead release ownership via `WithClientErrorPolicy(ClientErrorPolicyRelease)`; request-specific retention is available through `WithTTLProvider`; sensitive replay payloads can use authenticated encryption through `WithResponseCodec`; `WithMaxBodyCache` bounds the raw response and encoded output is bounded to twice that value; tenant-scoped keys remain `"<prefix><tenantID>:<idempotencyKey>"`; rejection bodies remain customizable through `WithRejectedHandler`, `WithUnavailableHandler`, `WithConflictHandler`, and `WithKeyReuseHandler`
- `commons/webhook`: outbound webhook delivery with `NewDeliverer(lister, opts...) *Deliverer` returning nil when lister is nil (both `Deliver`/`DeliverWithResults` guard nil receiver); `Deliver(ctx, *Event) error` fans out to all active endpoints concurrently, returns errors only for pre-flight failures (nil receiver, nil event, listing failure) — per-endpoint failures are logged and metricked but do not propagate; `DeliverWithResults(ctx, *Event) []DeliveryResult` returns per-endpoint outcomes for callers needing individual results; SSRF protection via `resolveAndValidateIP`: single DNS lookup validates all resolved IPs against private/loopback/link-local/CGNAT/RFC-reserved ranges then pins URL to first resolved IP (eliminates DNS rebinding TOCTOU); `WithAllowPrivateNetwork()` only relaxes blocking for explicit private/loopback IP-literal URLs (for example `127.0.0.1`, `10.0.0.5`) when local/development tier allows it or `ALLOW_WEBHOOK_PRIVATE_NETWORK` supplies an explicit override reason; hostnames resolving to private IPs remain blocked; redirects blocked entirely to prevent 302-to-internal bypass; HMAC-SHA256 signing via `X-Webhook-Signature: sha256=<hex>` over raw payload (timestamp not included — replay protection is the receiver's responsibility); encrypted secrets via `SecretDecryptor` func (receives ciphertext with `enc:` prefix stripped, no decryptor + encrypted secret = fail-closed); retry with exponential backoff+jitter (base 1s), non-retryable on 4xx except 429; concurrency capped by semaphore (default 20); `EndpointLister` interface (`ListActiveEndpoints`), `DeliveryMetrics` interface (`RecordDelivery`); functional options `WithLogger`/`WithTracer`/`WithMetrics`/`WithMaxConcurrency`/`WithMaxRetries`/`WithHTTPClient`/`WithSecretDecryptor`/`WithAllowPrivateNetwork`; sentinel errors `ErrNilDeliverer`/`ErrSSRFBlocked`/`ErrDeliveryFailed`/`ErrInvalidURL`
- `commons/server`: `ServerManager`-based graceful shutdown with `WithHTTPServer` for Fiber, `WithStdlibHTTPServer` for caller-owned `*net/http.Server`, `WithStdlibHTTPListener` for pre-bound stdlib listeners (stdlib HTTP variants are mutually exclusive with Fiber HTTP), `WithGRPCServer`/`WithShutdownChannel`/`WithShutdownTimeout`/`WithShutdownHook`, `StartWithGracefulShutdown()`/`StartWithGracefulShutdownWithError()`, `ServersStarted()` for test coordination

### Resilience and safety

- `commons/certificate`: thread-safe TLS certificate manager with hot reload — `NewManager(certPath, keyPath string) (*Manager, error)` loads PEM files at construction; both paths empty returns unconfigured manager (TLS optional), exactly one path → `ErrIncompleteConfig`; key file must have mode `0600` or stricter (checked before reading); PKCS#8 → PKCS#1 (RSA) → EC (SEC 1) key parsing order; full PEM chain parsed (all `CERTIFICATE` blocks, leaf first then intermediates); `Rotate(cert *x509.Certificate, key crypto.Signer) error` atomically hot-reloads under write lock — validates `NotBefore`/`NotAfter` temporal bounds and public-key match (`ErrKeyMismatch`) before swapping; read accessors (all nil-safe, read-locked): `GetCertificate()`/`GetSigner()`/`PublicKey()`/`ExpiresAt()`/`DaysUntilExpiry()`; TLS integration: `TLSCertificate() tls.Certificate` builds populated struct with full chain; `GetCertificateFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error)` for assignment to `tls.Config.GetCertificate` for transparent hot-reload; package-level `LoadFromFiles(certPath, keyPath string) (*x509.Certificate, crypto.Signer, error)` for pre-flight validation without touching manager state; sentinel errors `ErrNilManager`/`ErrCertRequired`/`ErrKeyRequired`/`ErrExpired`/`ErrNoPEMBlock`/`ErrKeyParseFailure`/`ErrNotSigner`/`ErrKeyMismatch`/`ErrIncompleteConfig`
- `commons/circuitbreaker`: `Manager` interface with error-returning constructors (`NewManager`), `TenantAwareManager` tenant/service overloads for isolated per-tenant breakers (tenant-aware methods require non-empty valid tenant IDs; legacy `Manager` methods are the no-tenant/process-wide path), `NewPassthroughManager`/`NewPassthroughTenantAwareManager` for feature-flagged bypass while preserving validation contracts, config validation, preset configs (`DefaultConfig`/`AggressiveConfig`/`ConservativeConfig`/`HTTPServiceConfig`/`DatabaseConfig`), health checker (`NewHealthCheckerWithValidation`), metrics via `WithMetricsFactory` using `tenant_hash` for tenant-aware breakers instead of raw tenant IDs while preserving the legacy no-tenant metric label set
- `commons/backoff`: exponential backoff with jitter (`ExponentialWithJitter`) and context-aware sleep (`WaitContext`)
- `commons/errgroup`: error-group concurrency with panic recovery (`WithContext`, `Go`, `Wait`), configurable logger via `SetLogger`
- `commons/safe`: panic-safe math (`Divide`/`DivideRound`/`Percentage` on `decimal.Decimal`, `DivideFloat64`), regex with caching (`Compile`/`MatchString`/`FindString`), slices (`First`/`Last`/`At` with `*OrDefault` variants)
- `commons/security`: sensitive field detection (`IsSensitiveField`), default field lists (`DefaultSensitiveFields`/`DefaultSensitiveFieldsMap`)

### Domain and support packages

- `commons/transaction`: intent-based transaction planning (`BuildIntentPlan`), balance eligibility validation (`ValidateBalanceEligibility`), posting flow (`ApplyPosting`), operation resolution (`ResolveOperation`), typed domain errors (`NewDomainError`)
- `commons/outbox`: transactional outbox contracts, dispatcher, sanitizer, and tenant-aware persistence adapters. PostgreSQL supports pool-per-tenant, schema-per-tenant, and column-per-tenant. A pool-per-tenant service with generic and module databases wraps its existing resolvers with `postgres.NewModulePoolResolver(genericResolver, defaultTenantID, loadConfig, postgres.ModulePool{Name: "consignado", Resolver: consignadoResolver})`, then passes the result as `MultiTenantConfig.PoolResolver`. `TenantDispatchScope` identity is the exact `(real TenantID, opaque PoolKey)` pair: handlers always receive the real tenant, table-presence cache entries cannot leak between generic and module pools, and physical databases with the same canonical host/port/database/schema are scanned once. Empty scopes back off to `ColdDispatchInterval` (one minute by default, configured with `WithColdDispatchInterval`) while active and recently active scopes retain `DispatchInterval`. Module topology is cached and refreshed by one caller per interval (one minute by default); use `NewModulePoolResolverWithConfig` to align `ModulePoolResolverConfig.TopologyRefreshInterval` with a service-specific cold interval. A failed refresh may use last-known-good topology under the existing fail-open contract, but every failure is retried after the interval and a failed first ownership lookup is never made permanent. `ModulePoolResolver.InvalidateTopology()` forces the next enumeration to refresh after tenant additions or topology changes. `ModulePoolResolver.EvictTenant(tenantID)` removes every scope immediately and forces refresh after removal or suspension; stale in-flight refreshes cannot restore evicted scopes. Newly committed/retryable/stuck rows remain governed by dispatcher cold-scope polling, which is unchanged. Legacy `TenantPoolResolver` implementations and direct `ManagerPoolResolver` wiring remain one-scope-per-tenant and unchanged. MongoDB retains row-scoped tenants plus optional module database resolution through `mongo.WithModule` / `mongo.WithTenantDatabaseResolver`. Tenant-aware repositories return `ErrInvalidTenantID` for IDs rejected by `tenant-manager/core.IsValidTenantID`.
  PostgreSQL additionally implements the optional `outbox.TransactionalBatchWriter` contract: `CreateManyWithTx(ctx, tx, events)` validates the full batch before issuing one set-wise `INSERT`, returns rows in input order, and treats an empty batch as a no-op.
- `commons/crypto`: hashing (`GenerateHash`) and symmetric encryption (`InitializeCipher`/`Encrypt`/`Decrypt`) with credential-safe `fmt` output (`String()`/`GoString()` redact secrets)
- `commons/jwt`: HS256/384/512 JWT signing (`Sign`), signature verification (`Parse`), combined signature + time-claim validation (`ParseAndValidate`), standalone time-claim validation (`ValidateTimeClaims`/`ValidateTimeClaimsAt`)
- `commons/license`: license validation with functional options (`New(opts...)`, `WithLogger`, `WithFailClosed`), fail-closed default termination (`Terminate` exits with code 1 unless a custom handler is configured), handler management (`SetHandler`), error-returning validation (`TerminateWithError`/`TerminateSafe`)
- `commons/pointers`: pointer conversion helpers (`String`, `Bool`, `Time`, `Int`, `Int64`, `Float64`)
- `commons/cron`: cron expression parser (`Parse`) and scheduler (`Schedule.Next`)
- `commons/secretsmanager`: AWS Secrets Manager M2M and external credential retrieval via `GetM2MCredentials` / `GetExternalCredentials`; version-addressed external credentials use the opaque `ExternalCredentialReference` capability, created by `BuildExternalSecretVersionReference` or parsed from storage with `ParseExternalCredentialReference(reference, trustedScope)` before `GetExternalCredentialsByReference`; canonical UUID-versioned SecretIds (`tenants/{env?}/{tenant}/{app}/external/{target}/credentials/versions/{uuid}`), exact scope binding, strict input validation, typed retrieval errors, non-null string-only JSON objects, and the `SecretsManagerClient` test seam

### Multi-tenant packages

- `commons/tenant-manager/core`: shared tenant types, context helpers (`ContextWithTenantID`, `GetTenantIDFromContext`), and tenant-manager error contracts
- `commons/tenant-manager/cache`: exported tenant-config cache contract (`ConfigCache`), `ErrCacheMiss`, and in-memory cache implementation used by the HTTP client
- `commons/tenant-manager/client`: Tenant Manager HTTP client with circuit breaker, cache options (`WithCache`, `WithCacheTTL`, `WithSkipCache`), cache invalidation, and response hardening
- `commons/tenant-manager/consumer`: dynamic multi-tenant queue consumer lifecycle management with tenant discovery, sync, retry, and per-tenant handlers
- `commons/tenant-manager/event`: canonical tenant lifecycle dispatcher. Module-aware services register every PostgreSQL manager with `WithPostgresManagers` and every MongoDB manager with `WithMongoManagers`; removal events close all registered pools, while connection-setting events route only to the PostgreSQL manager matching the payload module. Existing singular options remain supported.
- `commons/tenant-manager/middleware`: Fiber middleware for tenant extraction, upstream auth assertion checks, and tenant-scoped DB resolution
- `commons/tenant-manager/postgres`: tenant-scoped PostgreSQL connection manager with LRU eviction, async settings revalidation, pool controls, and `Module()` for canonical lifecycle routing (`""` identifies the generic resource)
- `commons/tenant-manager/mongo`: tenant-scoped MongoDB connection manager with LRU eviction and idle-timeout controls
- `commons/tenant-manager/rabbitmq`: tenant-scoped RabbitMQ connection manager with soft connection-pool limits and eviction
- `commons/tenant-manager/s3`: tenant-prefixed S3/object storage. `NewStorage` preserves the general upload/create/download/delete/list API. `NewRetainedStorage` adds immutable version custody: `CreateRetained` performs an atomic `If-None-Match: *` write with explicit COMPLIANCE retention, canonicalizes retain-until to S3's whole-second UTC precision, and returns exact-version metadata; `DownloadVersion` and `StatVersion` require a `VersionID`; `ValidateDefaultRetention` fails closed unless Object Lock is enabled with COMPLIANCE retention of at least five years (`Years >= 5` or `Days >= 1827`). `NewRecoverableRetainedStorage` adds deterministic create-or-recover for callers that can grant `s3:ListBucketVersions`: after a duplicate or ambiguous PUT timeout, it uses a detached, bounded lookup and returns only one exact-key version that is both sole and latest, has a non-empty exact `VersionID`, remains under COMPLIANCE retention, and exactly matches the caller's expected content type, content length, and canonical retain-until time. Missing permission, multiple versions, a latest delete marker, truncated/empty listings, or metadata drift fail closed. Payload digest verification remains the caller's responsibility. The retained surfaces expose no delete or retention-bypass operation.
- `commons/tenant-manager/valkey`: tenant-prefixed Redis/Valkey key and pattern helpers with delimiter validation

### Build and shell

- `commons/shell/`: Makefile include helpers (`makefile_colors.mk`, `makefile_utils.mk`), shell scripts (`colors.sh`, `ascii.sh`), ASCII art (`logo.txt`)

## Minimal v5 usage

```go
import (
    "github.com/LerianStudio/lib-commons/v5/commons"
)

func newRequestID() (string, error) {
    id, err := commons.GenerateUUIDv7()
    if err != nil {
        return "", err
    }

    return id.String(), nil
}
```

## Environment Variables

The following environment variables are recognized by lib-commons or by canonical sibling libraries that lib-commons integrates with. Observability variables are owned by `lib-observability`.

| Variable | Type | Default | Package | Description |
| :--- | :--- | :--- | :--- | :--- |
| `VERSION` | `string` | `"NO-VERSION"` | `commons` | Application version, printed at startup by `InitLocalEnvConfig` |
| `ENV_NAME` | `string` | `"local"` | `commons` | Environment name; when `"local"`, a `.env` file is loaded automatically |
| `ENV` | `string` | _(none)_ | `lib-observability/assert` | When set to `"production"`, stack traces are omitted from assertion failures |
| `GO_ENV` | `string` | _(none)_ | `lib-observability/assert` | Fallback production check (same behavior as `ENV`) |
| `LOG_LEVEL` | `string` | `"debug"` (dev/local) / `"info"` (other) | `lib-observability/zap` | Log level override (`debug`, `info`, `warn`, `error`); `Config.Level` takes precedence if set |
| `LOG_ENCODING` | `string` | `"console"` (dev/local) / `"json"` (other) | `lib-observability/zap` | Log output format: `"json"` for structured JSON, `"console"` for human-readable colored output |
| `LOG_OBFUSCATION_DISABLED` | `bool` | `false` | `commons/net/http` | Set to `"true"` to disable sensitive-field obfuscation in HTTP access logs (**not recommended in production**) |
| `METRICS_COLLECTION_INTERVAL` | `duration` | `"5s"` | `commons/net/http` | Background system-metrics collection interval (Go duration format, e.g. `"10s"`, `"1m"`) |
| `ACCESS_CONTROL_ALLOW_CREDENTIALS` | `bool` | `"false"` | `commons/net/http` | CORS `Access-Control-Allow-Credentials` header value |
| `ACCESS_CONTROL_ALLOW_ORIGIN` | `string` | `"*"` | `commons/net/http` | CORS `Access-Control-Allow-Origin` header value |
| `ACCESS_CONTROL_ALLOW_METHODS` | `string` | `"POST, GET, OPTIONS, PUT, DELETE, PATCH"` | `commons/net/http` | CORS `Access-Control-Allow-Methods` header value |
| `ACCESS_CONTROL_ALLOW_HEADERS` | `string` | `"Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization"` | `commons/net/http` | CORS `Access-Control-Allow-Headers` header value |
| `ACCESS_CONTROL_EXPOSE_HEADERS` | `string` | `""` | `commons/net/http` | CORS `Access-Control-Expose-Headers` header value |
| `RATE_LIMIT_ENABLED` | `bool` | `"false"` | `commons/net/http/ratelimit` | Explicit opt-in: set to `"true"` to enable rate limiting. When unset or falsy, `New` returns nil and all requests pass through |
| `RATE_LIMIT_MAX` | `int` | `500` | `commons/net/http/ratelimit` | Maximum requests per window for `DefaultTier` |
| `RATE_LIMIT_WINDOW_SEC` | `int` | `60` | `commons/net/http/ratelimit` | Window duration in seconds for `DefaultTier` |
| `AGGRESSIVE_RATE_LIMIT_MAX` | `int` | `100` | `commons/net/http/ratelimit` | Maximum requests per window for `AggressiveTier` |
| `AGGRESSIVE_RATE_LIMIT_WINDOW_SEC` | `int` | `60` | `commons/net/http/ratelimit` | Window duration in seconds for `AggressiveTier` |
| `RELAXED_RATE_LIMIT_MAX` | `int` | `1000` | `commons/net/http/ratelimit` | Maximum requests per window for `RelaxedTier` |
| `RELAXED_RATE_LIMIT_WINDOW_SEC` | `int` | `60` | `commons/net/http/ratelimit` | Window duration in seconds for `RelaxedTier` |
| `RATE_LIMIT_REDIS_TIMEOUT_MS` | `int` | `500` | `commons/net/http/ratelimit` | Timeout in milliseconds for Redis operations; exceeded requests follow fail-open/fail-closed policy |
| `SECURITY_ENFORCEMENT` | `bool` | `false` | `commons` | Enables hard enforcement for configured security-tier checks that otherwise warn during migration phases |
| `ALLOW_INSECURE_OTEL` | `string` | `""` | `lib-observability/tracing` | Justification override that allows insecure OTEL exporter endpoints in strict tier |
| `ALLOW_WEBHOOK_PRIVATE_NETWORK` | `string` | `""` | `commons/webhook` | Justification override that enables `WithAllowPrivateNetwork` outside permissive tier for explicit private IP-literal webhook targets |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `string` | _(none)_ | `lib-observability/tracing` | General OTLP endpoint read by the OTel SDK; bare `host:port` values are normalized to `http://host:port` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `string` | _(none)_ | `lib-observability/tracing` | Traces-specific OTLP endpoint; bare `host:port` values are normalized to `http://host:port` |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | `string` | _(none)_ | `lib-observability/tracing` | Metrics-specific OTLP endpoint; bare `host:port` values are normalized to `http://host:port` |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | `string` | _(none)_ | `lib-observability/tracing` | Logs-specific OTLP endpoint; bare `host:port` values are normalized to `http://host:port` |

Additionally, `commons.SetConfigFromEnvVars` populates any struct using `env:"VAR_NAME"` field tags, supporting `string`, `bool`, integer types, `time.Duration` and `[]string`. Consuming applications define their own variable names through these tags.

#### Defaults

A field may carry an `envDefault` tag giving the value to use when its variable is unset, blank, or unparseable for the field's type:

```go
type Config struct {
    AuthEnabled bool          `env:"PLUGIN_AUTH_ENABLED"  envDefault:"true"`
    Port        int           `env:"SERVER_PORT"          envDefault:"8080"`
    Timeout     time.Duration `env:"REQUEST_TIMEOUT"      envDefault:"30s"`
    Origins     []string      `env:"CORS_ALLOWED_ORIGINS" envDefault:"https://app.example.com"`
}
```

An explicit, non-blank, parseable value always wins, including `false` — the default only fills a gap, it does not override an operator.

A value that is present but **unparseable** for the field's type takes the default instead, and `GetenvBoolOrDefault`/`GetenvIntOrDefault`/`GetenvDurationOrDefault` warn to stderr when they do. That predates this tag and is deliberately unchanged: the alternative is refusing to boot on a typo in a variable the field has a working default for. It does mean `PLUGIN_AUTH_ENABLED=flase` yields `true` here rather than an error — so a guard that must reject an explicitly disabled value in production belongs in a validator that reads the raw variable, not in the default.

**Without the tag a field takes its zero value, and for a bool that is `false`.** A flag that must be ON unless an operator turns it off therefore MUST declare the default; relying on the variable being present ships the feature OFF to whoever forgets it. `envDefault` is the only accepted spelling — `default` is read by nothing, and a tag that is silently ignored is worse than no tag, because a reviewer sees it and passes.

An `envDefault` the field's type cannot hold — `envDefault:"maybe"` on a bool, or `envDefault:"999"` on an `int8` — returns `ErrInvalidDefaultValue` at load time rather than falling back to zero. A default that does not apply is indistinguishable from no default at all, which is the failure mode this tag exists to remove.

#### Durations

A `time.Duration` field takes a value **with a unit** — `30s`, `2m`, `720h`, `150ms` — in both the environment variable and the `envDefault` tag. It is matched on its type, ahead of the integer types, because `time.Duration` is defined as an `int64`: a switch on reflect kind cannot tell the two apart, so until this was handled explicitly `envDefault:"30s"` failed the load outright and `envDefault:"30"` silently meant thirty **nanoseconds**.

A unit-less integer remains a **nanosecond** count, matching `time.Duration`'s own numeric meaning and this loader's historical reading. Deployed configuration relies on it — a Helm value of `"2000000000"` means two seconds — so re-reading a bare integer as seconds would silently stretch a two-second timeout to roughly 63 years. Write the unit; the unit-less spelling is legacy that keeps working.

`commons.GetenvDurationOrDefault(key, fallback)` applies the same parsing to a single variable, for code that reads one value rather than populating a struct.

## Development commands

### Core

- `make build` -- build all packages
- `make ci` -- run the local fix + verify pipeline (`lint-fix`, `format`, `tidy`, `check-tests`, `sec`, `vet`, `test-unit`, `test-integration`)
- `make clean` -- clean build artifacts and caches
- `make tidy` -- clean dependencies (`go mod tidy`)
- `make format` -- format code with gofmt
- `make help` -- display all available commands

### Testing

- `make test` -- run unit tests (uses gotestsum if available)
- `make test-unit` -- run unit tests excluding integration
- `make test-integration` -- run integration tests with testcontainers (requires Docker)
- `make test-all` -- run all tests (unit + integration)

### Coverage

- `make coverage-unit` -- unit tests with coverage report (respects `.ignorecoverunit`)
- `make coverage-integration` -- integration tests with coverage
- `make coverage` -- run all coverage targets

### Code quality

- `make lint` -- run lint checks (read-only)
- `make lint-fix` -- auto-fix lint issues
- `make vet` -- run `go vet` on all packages
- `make sec` -- run security checks using gosec (`make sec SARIF=1` for SARIF output)
- `make check-tests` -- verify test coverage for packages

### Test flags

- `LOW_RESOURCE=1` -- reduces parallelism and disables race detector for constrained machines
- `RETRY_ON_FAIL=1` -- retries failed tests once
- `RUN=<pattern>` -- filter integration tests by name pattern
- `PKG=<path>` -- filter to specific package(s)

### Git hooks

- `make setup-git-hooks` -- install and configure git hooks
- `make check-hooks` -- verify git hooks installation
- `make check-envs` -- check hooks + environment file security

### Tooling and release

- `make tools` -- install test tools (gotestsum)
- `make goreleaser` -- create release snapshot

## Project Rules

For coding standards, architecture patterns, testing requirements, and development guidelines, see [`docs/PROJECT_RULES.md`](docs/PROJECT_RULES.md).

## License

This project is licensed under the terms in `LICENSE`.
