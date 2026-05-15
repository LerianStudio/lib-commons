# lib-commons

`lib-commons` is Lerian's shared Go toolkit for service primitives, connectors, observability, and runtime safety.

The current major API surface is **v5**. If you are migrating from older `lib-commons` code, see `MIGRATION_MAP.md`.

---

**Migrating from older packages?**  
Use `MIGRATION_MAP.md` as the canonical map for renamed, redesigned, or removed APIs in the unified `lib-commons` line.

---

## Requirements

- Go `1.25.9` or newer

## Installation

```bash
go get github.com/LerianStudio/lib-commons/v5
```

## What is in this library

### Core (`commons`)

- `app.go`: `Launcher` for concurrent app lifecycle management with `NewLauncher(opts...)` and `RunApp` options
- `context.go`: request-scoped logger/tracer/metrics/header-id tracking via `ContextWith*` helpers, safe timeout with `WithTimeoutSafe`, span attribute propagation
- `errors.go`: standardized business error mapping with `ValidateBusinessError`
- `utils.go`: UUID generation (`GenerateUUIDv7` returns error), struct-to-JSON, map merging, CPU/memory metrics, internal service detection
- `stringUtils.go`: accent removal, case conversion, UUID placeholder replacement, SHA-256 hashing, server address validation
- `time.go`: date/time validation, range checking, parsing with end-of-day support
- `os.go`: environment variable helpers (`GetenvOrDefault`, `GetenvBoolOrDefault`, `GetenvIntOrDefault`), struct population from env tags via `SetConfigFromEnvVars`
- `commons/constants`: shared constants for datasource status, errors, headers, metadata, pagination, transactions, OTEL attributes, obfuscation values, and `SanitizeMetricLabel` utility

### Observability and logging

- `commons/opentelemetry`: telemetry bootstrap (`NewTelemetry`), normalized deployment environment resource attributes (`deployment.environment.name` after trim/lowercase), propagation (HTTP/gRPC/queue), span helpers, redaction (`Redactor` with `RedactionRule` patterns), struct-to-attribute conversion
- `commons/opentelemetry/metrics`: fluent metrics factory (`NewMetricsFactory`, `NewNopFactory`) with Counter/Gauge/Histogram builders, explicit error returns, `CounterBuilder.WithAttributeSet(attribute.Set)` for callers that already hold a prebuilt OTel attribute set, convenience recorders for accounts/transactions
- `commons/log`: v2 logging interface (`Logger` with `Log`/`With`/`WithGroup`/`Enabled`/`Sync`), typed `Field` constructors (`String`, `Int`, `Bool`, `Err`, `Any`), `GoLogger` with CWE-117 log-injection prevention, sanitizer (`SafeError`, `SanitizeExternalResponse`)
- `commons/zap`: zap adapter for `commons/log` with OTEL bridge, `Config`-based construction via `New()`, direct zap convenience methods (`Debug`/`Info`/`Warn`/`Error`), underlying access via `Raw()` and `Level()`

### Data and messaging connectors

- `commons/postgres`: `Config`-based constructor (`New`), `Resolver(ctx)` for dbresolver access, `Primary()` for raw `*sql.DB`, `NewMigrator` for schema migrations, backoff-based lazy-connect
- `commons/mongo`: `Config`-based client with functional options (`NewClient`), URI builder (`BuildURI`), `Client(ctx)`/`ResolveClient(ctx)` for access, `EnsureIndexes` (variadic), TLS support, credential clearing
- `commons/redis`: topology-based `Config` (standalone/sentinel/cluster), GCP IAM auth with token refresh, distributed locking via `LockManager` interface (`NewRedisLockManager`, `LockHandle`), `SetPackageLogger` for diagnostics, TLS defaults to a TLS1.2 minimum floor with `AllowLegacyMinVersion` as an explicit temporary compatibility override
- `commons/rabbitmq`: connection/channel/health helpers for AMQP with `*Context()` variants, `HealthCheck() (bool, error)`, `Close()`/`CloseContext()`, confirmable publisher with broker acks and auto-recovery, DLQ topology utilities, and health-check hardening (`AllowInsecureHealthCheck`, `HealthCheckAllowedHosts`, `RequireHealthCheckAllowedHosts`)
- `commons/streaming`: CloudEvents 1.0 binary-mode domain-event publisher to Redpanda/Kafka via `franz-go`, with circuit-breaker + transactional-outbox fallback and per-topic DLQ; `Emitter` interface (`Emit`/`Close`/`Healthy`) plus `*Producer` implementing `commons.App`; fail-safe `New(ctx, cfg, opts...)` returns `NoopEmitter` when disabled, strict `NewProducer(ctx, cfg, opts...)` returns `ErrMissingBrokers`; functional options `WithLogger`/`WithMetricsFactory`/`WithTracer`/`WithCircuitBreakerManager`/`WithOutboxRepository`/`WithPartitionKey`/`WithCloseTimeout`/`WithTLSConfig`/`WithSASL`; 8 error classes + 9 sentinels via `IsCallerError`; OTEL observability (6 instruments, `streaming.emit` span, bounded labels — `tenant_id` on spans only); `MockEmitter` + `AssertEventEmitted`/`AssertEventCount`/`AssertTenantID`/`AssertNoEvents`/`WaitForEvent` helpers for unit tests
- `commons/dlq`: Redis-backed dead letter queue with `New(conn, keyPrefix, maxRetries, opts...)` returning nil when conn is nil (all methods guard nil receiver via `ErrNilHandler`); key operations: `Enqueue` (RPush, stamps `CreatedAt`/`MaxRetries` on first enqueue), `Dequeue` (LPop, at-most-once), `QueueLength`, `ScanQueues` (non-blocking SCAN for background consumers without tenant context), `PruneExhaustedMessages` (dequeue-discard-reenqueue cycle up to limit), `ExtractTenantFromKey`; tenant-scoped Redis keys (`"<prefix><tenantID>:<source>"`), backoff via exponential-with-jitter (base 30s, floor 5s, AWS Full Jitter); functional options `WithLogger`/`WithTracer`/`WithMetrics`/`WithModule`; `DLQMetrics` interface (`RecordRetried`/`RecordExhausted`, nil-safe); `NewConsumer(handler, retryFn, opts...) (*Consumer, error)` for background poll loop — `Run(ctx)` blocks until stop, `Stop()` idempotent, `ProcessOnce(ctx)` exported for tests; consumer options `WithConsumerLogger`/`WithConsumerTracer`/`WithConsumerMetrics`/`WithConsumerModule`/`WithPollInterval`/`WithBatchSize`/`WithSources`; sentinel errors `ErrNilHandler`, `ErrNilRetryFunc`, `ErrMessageExhausted`
- `commons/systemplane`: dual-backend (Postgres LISTEN/NOTIFY, MongoDB change streams) hot-reload runtime configuration store with typed accessors, `OnChange` subscriptions, admin HTTP routes (`commons/systemplane/admin`), and a backend-agnostic contract test suite (`systemplanetest`); adds tenant-scoped keys (`RegisterTenantScoped`, `GetForTenant`, `SetForTenant`, `DeleteForTenant`, `ListTenantsForKey`, `OnTenantChange`, typed accessor mirrors) with three new admin HTTP routes under `:prefix/:namespace/:key/tenants[/:tenantID]` and the `WithTenantAuthorizer` option — additive and non-breaking; see `commons/systemplane/MIGRATION_TENANT_SCOPED.md` for the adoption guide

### HTTP and server utilities

- `commons/net/http`: Fiber HTTP helpers -- response (`Respond`/`RespondStatus`/`RespondError`/`RespondErrorEnvelope`/`RenderError`; `RespondErrorEnvelope` preserves a caller-supplied status code and machine-readable error envelope), health (`Ping`/`HealthWithDependencies`), SSRF-protected reverse proxy (`ServeReverseProxy` with `ReverseProxyPolicy`), pagination (offset/opaque cursor/timestamp cursor/sort cursor), validation (`ParseBodyAndValidate`/`ValidateStruct`/`ValidateSortDirection`/`ValidateLimit`), context/ownership (`ParseAndVerifyTenantScopedID`/`ParseAndVerifyResourceScopedID`), middleware (`WithHTTPLogging`/`WithGrpcLogging`/`WithCORS`/`WithBasicAuth`/`NewTelemetryMiddleware`), `FiberErrorHandler`
- `commons/net/http/ratelimit`: Redis-backed distributed rate limiting middleware for Fiber — `New(conn, opts...)` returns a `*RateLimiter` (nil when disabled, nil-safe for pass-through), `WithDefaultRateLimit(conn, opts...)` as a one-liner that wires `New` + `DefaultTier` into a ready-to-use `fiber.Handler`, fixed-window counter via atomic Lua script (INCR + PEXPIRE), `RedisStorage.Increment(ctx,key,window)` as the storage-only atomic primitive, `WithRateLimit(tier)` for static tiers, `WithDynamicRateLimit(TierFunc)` for per-request tier selection, `MethodTierSelector` for write-vs-read split, preset tiers (`DefaultTier` / `AggressiveTier` / `RelaxedTier`) configurable via env vars, identity extractors (`IdentityFromIP` / `IdentityFromHeader` / `IdentityFromIPAndHeader` — uses `#` separator to avoid conflict with IPv6 colons), fail-open/fail-closed policy, `WithOnLimited` callback, `WithExceededHandler` for caller-controlled 429 response bodies after standard rate-limit headers are set, and standard `X-RateLimit-*` / `Retry-After` headers; also exports `RedisStorage` (`NewRedisStorage`) for use with third-party Fiber middleware
- `commons/net/http/idempotency`: Redis-backed at-most-once request middleware for Fiber — `New(conn, opts...) *Middleware` returns nil when conn is nil (`Check()` on nil returns pass-through handler, fail-open by design); `Check() fiber.Handler` registers on a Fiber route; applies only to mutating methods (POST/PUT/PATCH/DELETE), passes GET/HEAD/OPTIONS unconditionally; reads key from `Idempotency-Key` header (missing key passes through); key too long → 400 `VALIDATION_ERROR`; SetNX atomically claims key as `"processing"` for TTL; duplicate request outcomes: cached response available → replays status+body verbatim with `Idempotency-Replayed: true` header, key in-flight → 409 `IDEMPOTENCY_CONFLICT`, key `"complete"` but no body cache → 200 `IDEMPOTENT`; on handler success caches response under `<key>:response` (if ≤ `maxBodyCache`) and marks key `"complete"`; on handler error deletes both keys to allow client retry with same key; Redis unavailable → fail-open logged at WARN; tenant-scoped keys `"<prefix><tenantID>:<idempotencyKey>"`; functional options `WithLogger`/`WithKeyPrefix`/`WithKeyTTL`/`WithMaxKeyLength`/`WithRedisTimeout`/`WithRejectedHandler`/`WithMaxBodyCache`
- `commons/webhook`: outbound webhook delivery with `NewDeliverer(lister, opts...) *Deliverer` returning nil when lister is nil (both `Deliver`/`DeliverWithResults` guard nil receiver); `Deliver(ctx, *Event) error` fans out to all active endpoints concurrently, returns errors only for pre-flight failures (nil receiver, nil event, listing failure) — per-endpoint failures are logged and metricked but do not propagate; `DeliverWithResults(ctx, *Event) []DeliveryResult` returns per-endpoint outcomes for callers needing individual results; SSRF protection via `resolveAndValidateIP`: single DNS lookup validates all resolved IPs against private/loopback/link-local/CGNAT/RFC-reserved ranges then pins URL to first resolved IP (eliminates DNS rebinding TOCTOU); `WithAllowPrivateNetwork()` only relaxes blocking for explicit private/loopback IP-literal URLs (for example `127.0.0.1`, `10.0.0.5`) when local/development tier allows it or `ALLOW_WEBHOOK_PRIVATE_NETWORK` supplies an explicit override reason; hostnames resolving to private IPs remain blocked; redirects blocked entirely to prevent 302-to-internal bypass; HMAC-SHA256 signing via `X-Webhook-Signature: sha256=<hex>` over raw payload (timestamp not included — replay protection is the receiver's responsibility); encrypted secrets via `SecretDecryptor` func (receives ciphertext with `enc:` prefix stripped, no decryptor + encrypted secret = fail-closed); retry with exponential backoff+jitter (base 1s), non-retryable on 4xx except 429; concurrency capped by semaphore (default 20); `EndpointLister` interface (`ListActiveEndpoints`), `DeliveryMetrics` interface (`RecordDelivery`); functional options `WithLogger`/`WithTracer`/`WithMetrics`/`WithMaxConcurrency`/`WithMaxRetries`/`WithHTTPClient`/`WithSecretDecryptor`/`WithAllowPrivateNetwork`; sentinel errors `ErrNilDeliverer`/`ErrSSRFBlocked`/`ErrDeliveryFailed`/`ErrInvalidURL`
- `commons/server`: `ServerManager`-based graceful shutdown with `WithHTTPServer` for Fiber, `WithStdlibHTTPServer` for caller-owned `*net/http.Server` (mutually exclusive with Fiber HTTP), `WithGRPCServer`/`WithShutdownChannel`/`WithShutdownTimeout`/`WithShutdownHook`, `StartWithGracefulShutdown()`/`StartWithGracefulShutdownWithError()`, `ServersStarted()` for test coordination

### Resilience and safety

- `commons/certificate`: thread-safe TLS certificate manager with hot reload — `NewManager(certPath, keyPath string) (*Manager, error)` loads PEM files at construction; both paths empty returns unconfigured manager (TLS optional), exactly one path → `ErrIncompleteConfig`; key file must have mode `0600` or stricter (checked before reading); PKCS#8 → PKCS#1 (RSA) → EC (SEC 1) key parsing order; full PEM chain parsed (all `CERTIFICATE` blocks, leaf first then intermediates); `Rotate(cert *x509.Certificate, key crypto.Signer) error` atomically hot-reloads under write lock — validates `NotBefore`/`NotAfter` temporal bounds and public-key match (`ErrKeyMismatch`) before swapping; read accessors (all nil-safe, read-locked): `GetCertificate()`/`GetSigner()`/`PublicKey()`/`ExpiresAt()`/`DaysUntilExpiry()`; TLS integration: `TLSCertificate() tls.Certificate` builds populated struct with full chain; `GetCertificateFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error)` for assignment to `tls.Config.GetCertificate` for transparent hot-reload; package-level `LoadFromFiles(certPath, keyPath string) (*x509.Certificate, crypto.Signer, error)` for pre-flight validation without touching manager state; sentinel errors `ErrNilManager`/`ErrCertRequired`/`ErrKeyRequired`/`ErrExpired`/`ErrNoPEMBlock`/`ErrKeyParseFailure`/`ErrNotSigner`/`ErrKeyMismatch`/`ErrIncompleteConfig`
- `commons/circuitbreaker`: `Manager` interface with error-returning constructors (`NewManager`), `TenantAwareManager` tenant/service overloads for isolated per-tenant breakers (tenant-aware methods require non-empty valid tenant IDs; legacy `Manager` methods are the no-tenant/process-wide path), `NewPassthroughManager` for feature-flagged bypass while preserving validation contracts, config validation, preset configs (`DefaultConfig`/`AggressiveConfig`/`ConservativeConfig`/`HTTPServiceConfig`/`DatabaseConfig`), health checker (`NewHealthCheckerWithValidation`), metrics via `WithMetricsFactory` using `tenant_hash` instead of raw tenant IDs
- `commons/backoff`: exponential backoff with jitter (`ExponentialWithJitter`) and context-aware sleep (`WaitContext`)
- `commons/errgroup`: error-group concurrency with panic recovery (`WithContext`, `Go`, `Wait`), configurable logger via `SetLogger`
- `commons/runtime`: panic recovery (`RecoverAndLog`/`RecoverAndCrash`/`RecoverWithPolicy` with `*WithContext` variants), safe goroutines (`SafeGo`/`SafeGoWithContext`/`SafeGoWithContextAndComponent`), panic metrics (`InitPanicMetrics`), span recording (`RecordPanicToSpan`), error reporter (`SetErrorReporter`/`GetErrorReporter`), production mode (`SetProductionMode`/`IsProductionMode`)
- `commons/assert`: production-safe assertions (`New` + `That`/`NotNil`/`NotEmpty`/`NoError`/`Never`/`Halt`), assertion metrics (`InitAssertionMetrics`), domain predicates (`Positive`/`ValidUUID`/`ValidAmount`/`DebitsEqualCredits`/`TransactionCanTransitionTo`/`BalanceSufficientForRelease` and more)
- `commons/safe`: panic-safe math (`Divide`/`DivideRound`/`Percentage` on `decimal.Decimal`, `DivideFloat64`), regex with caching (`Compile`/`MatchString`/`FindString`), slices (`First`/`Last`/`At` with `*OrDefault` variants)
- `commons/security`: sensitive field detection (`IsSensitiveField`), default field lists (`DefaultSensitiveFields`/`DefaultSensitiveFieldsMap`)

### Domain and support packages

- `commons/transaction`: intent-based transaction planning (`BuildIntentPlan`), balance eligibility validation (`ValidateBalanceEligibility`), posting flow (`ApplyPosting`), operation resolution (`ResolveOperation`), typed domain errors (`NewDomainError`)
- `commons/outbox`: transactional outbox contracts, dispatcher, sanitizer, PostgreSQL adapters for schema-per-tenant or column-per-tenant models, and a MongoDB repository with row-scoped tenants plus optional tenant-manager module database resolution via `mongo.WithModule` / `mongo.WithTenantDatabaseResolver` (`TenantDatabaseResolver.ListTenants` + `DatabaseForTenant` lets generic dispatcher loops drain tenant Mongo databases; schema resolver requires tenant context by default; column migration uses composite key `(tenant_id, id)`; tenant-aware repositories return `ErrInvalidTenantID` for IDs rejected by `tenant-manager/core.IsValidTenantID`)
- `commons/crypto`: hashing (`GenerateHash`) and symmetric encryption (`InitializeCipher`/`Encrypt`/`Decrypt`) with credential-safe `fmt` output (`String()`/`GoString()` redact secrets)
- `commons/jwt`: HS256/384/512 JWT signing (`Sign`), signature verification (`Parse`), combined signature + time-claim validation (`ParseAndValidate`), standalone time-claim validation (`ValidateTimeClaims`/`ValidateTimeClaimsAt`)
- `commons/license`: license validation with functional options (`New(opts...)`, `WithLogger`), handler management (`SetHandler`), termination (`Terminate`/`TerminateWithError`/`TerminateSafe`)
- `commons/pointers`: pointer conversion helpers (`String`, `Bool`, `Time`, `Int`, `Int64`, `Float64`)
- `commons/cron`: cron expression parser (`Parse`) and scheduler (`Schedule.Next`)
- `commons/secretsmanager`: AWS Secrets Manager M2M credential retrieval via `GetM2MCredentials`, typed retrieval errors, and the `SecretsManagerClient` test seam

### Multi-tenant packages

- `commons/tenant-manager/core`: shared tenant types, context helpers (`ContextWithTenantID`, `GetTenantIDFromContext`), and tenant-manager error contracts
- `commons/tenant-manager/cache`: exported tenant-config cache contract (`ConfigCache`), `ErrCacheMiss`, and in-memory cache implementation used by the HTTP client
- `commons/tenant-manager/client`: Tenant Manager HTTP client with circuit breaker, cache options (`WithCache`, `WithCacheTTL`, `WithSkipCache`), cache invalidation, and response hardening
- `commons/tenant-manager/consumer`: dynamic multi-tenant queue consumer lifecycle management with tenant discovery, sync, retry, and per-tenant handlers
- `commons/tenant-manager/middleware`: Fiber middleware for tenant extraction, upstream auth assertion checks, and tenant-scoped DB resolution
- `commons/tenant-manager/postgres`: tenant-scoped PostgreSQL connection manager with LRU eviction, async settings revalidation, and pool controls
- `commons/tenant-manager/mongo`: tenant-scoped MongoDB connection manager with LRU eviction and idle-timeout controls
- `commons/tenant-manager/rabbitmq`: tenant-scoped RabbitMQ connection manager with soft connection-pool limits and eviction
- `commons/tenant-manager/s3`: tenant-prefixed S3/object-storage key helpers with delimiter validation
- `commons/tenant-manager/valkey`: tenant-prefixed Redis/Valkey key and pattern helpers with delimiter validation

### Build and shell

- `commons/shell/`: Makefile include helpers (`makefile_colors.mk`, `makefile_utils.mk`), shell scripts (`colors.sh`, `ascii.sh`), ASCII art (`logo.txt`)

## Minimal v5 usage

```go
import (
    "context"

    "github.com/LerianStudio/lib-commons/v5/commons/log"
    "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
)

func bootstrap() error {
    logger := log.NewNop()

    tl, err := opentelemetry.NewTelemetry(opentelemetry.TelemetryConfig{
        LibraryName:               "my-service",
        ServiceName:               "my-service-api",
        ServiceVersion:            "2.0.0",
        DeploymentEnv:             "local",
        CollectorExporterEndpoint: "localhost:4317",
        EnableTelemetry:           false, // Set to true when collector is available
        InsecureExporter:          true,
        Logger:                    logger,
    })
    if err != nil {
        return err
    }
    defer tl.ShutdownTelemetry()

    tl.ApplyGlobals()

    _ = context.Background()

    return nil
}
```

## Environment Variables

The following environment variables are recognized by lib-commons:

| Variable | Type | Default | Package | Description |
| :--- | :--- | :--- | :--- | :--- |
| `VERSION` | `string` | `"NO-VERSION"` | `commons` | Application version, printed at startup by `InitLocalEnvConfig` |
| `ENV_NAME` | `string` | `"local"` | `commons` | Environment name; when `"local"`, a `.env` file is loaded automatically |
| `ENV` | `string` | _(none)_ | `commons/assert` | When set to `"production"`, stack traces are omitted from assertion failures |
| `GO_ENV` | `string` | _(none)_ | `commons/assert` | Fallback production check (same behavior as `ENV`) |
| `LOG_LEVEL` | `string` | `"debug"` (dev/local) / `"info"` (other) | `commons/zap` | Log level override (`debug`, `info`, `warn`, `error`); `Config.Level` takes precedence if set |
| `LOG_ENCODING` | `string` | `"console"` (dev/local) / `"json"` (other) | `commons/zap` | Log output format: `"json"` for structured JSON, `"console"` for human-readable colored output |
| `LOG_OBFUSCATION_DISABLED` | `bool` | `false` | `commons/net/http` | Set to `"true"` to disable sensitive-field obfuscation in HTTP access logs (**not recommended in production**) |
| `METRICS_COLLECTION_INTERVAL` | `duration` | `"5s"` | `commons/net/http` | Background system-metrics collection interval (Go duration format, e.g. `"10s"`, `"1m"`) |
| `ACCESS_CONTROL_ALLOW_CREDENTIALS` | `bool` | `"false"` | `commons/net/http` | CORS `Access-Control-Allow-Credentials` header value |
| `ACCESS_CONTROL_ALLOW_ORIGIN` | `string` | `"*"` | `commons/net/http` | CORS `Access-Control-Allow-Origin` header value |
| `ACCESS_CONTROL_ALLOW_METHODS` | `string` | `"POST, GET, OPTIONS, PUT, DELETE, PATCH"` | `commons/net/http` | CORS `Access-Control-Allow-Methods` header value |
| `ACCESS_CONTROL_ALLOW_HEADERS` | `string` | `"Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization"` | `commons/net/http` | CORS `Access-Control-Allow-Headers` header value |
| `ACCESS_CONTROL_EXPOSE_HEADERS` | `string` | `""` | `commons/net/http` | CORS `Access-Control-Expose-Headers` header value |
| `RATE_LIMIT_ENABLED` | `bool` | `"true"` | `commons/net/http/ratelimit` | Set to `"false"` to disable rate limiting globally; `New` returns nil and all requests pass through |
| `RATE_LIMIT_MAX` | `int` | `500` | `commons/net/http/ratelimit` | Maximum requests per window for `DefaultTier` |
| `RATE_LIMIT_WINDOW_SEC` | `int` | `60` | `commons/net/http/ratelimit` | Window duration in seconds for `DefaultTier` |
| `AGGRESSIVE_RATE_LIMIT_MAX` | `int` | `100` | `commons/net/http/ratelimit` | Maximum requests per window for `AggressiveTier` |
| `AGGRESSIVE_RATE_LIMIT_WINDOW_SEC` | `int` | `60` | `commons/net/http/ratelimit` | Window duration in seconds for `AggressiveTier` |
| `RELAXED_RATE_LIMIT_MAX` | `int` | `1000` | `commons/net/http/ratelimit` | Maximum requests per window for `RelaxedTier` |
| `RELAXED_RATE_LIMIT_WINDOW_SEC` | `int` | `60` | `commons/net/http/ratelimit` | Window duration in seconds for `RelaxedTier` |
| `RATE_LIMIT_REDIS_TIMEOUT_MS` | `int` | `500` | `commons/net/http/ratelimit` | Timeout in milliseconds for Redis operations; exceeded requests follow fail-open/fail-closed policy |

Additionally, `commons.SetConfigFromEnvVars` populates any struct using `env:"VAR_NAME"` field tags, supporting `string`, `bool`, and integer types. Consuming applications define their own variable names through these tags.

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
