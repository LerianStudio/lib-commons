# AGENTS

This file provides repository-specific guidance for coding agents working on `lib-commons`.

## Project snapshot

- Module: `github.com/LerianStudio/lib-commons/v4`
- Language: Go
- Go version: `1.25.7` (see `go.mod`)
- Current API generation: v4

## Primary objective for changes

- Preserve v4 public API contracts unless a task explicitly asks for breaking changes.
- Prefer explicit error returns over panic paths in production code.
- Keep behavior nil-safe and concurrency-safe by default.

## Repository shape

Root:
- `commons/`: root shared helpers (`app`, `context`, `errors`, utilities, time, string, os)

Observability and logging:
- `commons/opentelemetry`: telemetry bootstrap, propagation, redaction, span helpers
- `commons/opentelemetry/metrics`: metric factory + fluent builders (Counter, Gauge, Histogram)
- `commons/log`: logging abstraction (`Logger` interface), typed `Field` constructors, log-injection prevention, sanitizer
- `commons/zap`: zap adapter for `commons/log` with OTEL bridge support

Data and messaging:
- `commons/postgres`: Postgres connector with `dbresolver`, migrations, OTEL spans, backoff-based lazy-connect
- `commons/mongo`: MongoDB connector with functional options, URI builder, index helpers, OTEL spans
- `commons/redis`: Redis connector with topology-based config (standalone/sentinel/cluster), GCP IAM auth, distributed locking (Redsync), backoff-based reconnect
- `commons/rabbitmq`: AMQP connection/channel/health helpers with context-aware methods
- `commons/dlq`: Redis-backed dead letter queue with tenant-scoped keys, exponential backoff, and a background consumer with retry/exhaust lifecycle
- `commons/systemplane`: dual-backend (Postgres/MongoDB) hot-reload runtime configuration store with LISTEN/NOTIFY and change-stream subscriptions, admin HTTP routes, and test contract suite

HTTP and server:
- `commons/net/http`: Fiber HTTP helpers (response, error rendering, cursor/offset/sort pagination, validation, SSRF-protected reverse proxy, CORS, basic auth, telemetry middleware, health checks, access logging)
- `commons/net/http/ratelimit`: Redis-backed rate limit storage for Fiber
- `commons/net/http/idempotency`: Redis-backed at-most-once request middleware for Fiber (SetNX, fail-open, 409 for in-flight, response replay)
- `commons/server`: `ServerManager`-based graceful shutdown and lifecycle helpers
- `commons/webhook`: outbound webhook delivery with SSRF protection, HMAC-SHA256 signing, DNS pinning, concurrency control, and exponential backoff retries

Resilience and safety:
- `commons/circuitbreaker`: circuit breaker manager with preset configs and health checker
- `commons/backoff`: exponential backoff with jitter and context-aware sleep
- `commons/runtime`: panic recovery, panic metrics, safe goroutine wrappers, error reporter, production mode
- `commons/assert`: production-safe assertions with telemetry integration and domain predicates
- `commons/safe`: panic-free math/regex/slice operations with error returns
- `commons/security`: sensitive field detection and handling
- `commons/security/ssrf`: canonical SSRF validation — IP blocking (CIDR blocklist + stdlib predicates), hostname blocking (metadata endpoints, dangerous suffixes), URL validation, DNS-pinned resolution with TOCTOU elimination
- `commons/errgroup`: goroutine coordination with panic recovery
- `commons/certificate`: thread-safe TLS certificate manager with hot reload, PEM file loading, PKCS#8/PKCS#1/EC key support, and `tls.Config` integration

Domain and support:
- `commons/transaction`: intent-based transaction planning, balance eligibility validation, posting flow
- `commons/crypto`: hashing and symmetric encryption with credential-safe `fmt` output
- `commons/jwt`: HMAC-based JWT signing, verification, and time-claim validation
- `commons/license`: license validation and enforcement with functional options
- `commons/pointers`: pointer conversion helpers
- `commons/cron`: cron expression parsing and scheduling
- `commons/constants`: shared constants (headers, errors, pagination, transactions, metadata, datasource status, OTEL attributes, obfuscation)

Build and shell:
- `commons/shell/`: Makefile include helpers (`makefile_colors.mk`, `makefile_utils.mk`), shell scripts, ASCII art

## API invariants to respect

### Telemetry (`commons/opentelemetry`)

- Initialization is explicit with `opentelemetry.NewTelemetry(cfg TelemetryConfig) (*Telemetry, error)`.
- Global OTEL providers are opt-in via `(*Telemetry).ApplyGlobals()`.
- `(*Telemetry).Tracer(name) (trace.Tracer, error)` and `(*Telemetry).Meter(name) (metric.Meter, error)` for named providers.
- Shutdown via `ShutdownTelemetry()` or `ShutdownTelemetryWithContext(ctx) error`.
- `TelemetryConfig` includes `InsecureExporter`, `Propagator`, and `Redactor` fields.
- Redaction uses `Redactor` with `RedactionRule` patterns; `NewDefaultRedactor()` and `NewRedactor(rules, mask)`. Old `FieldObfuscator` interface is removed.
- `RedactingAttrBagSpanProcessor` redacts sensitive span attributes using a `Redactor`.

### Metrics (`commons/opentelemetry/metrics`)

- Metric factory/builder operations return errors and should not be silently ignored.
- Supports Counter, Histogram, and Gauge instrument types.
- `NewMetricsFactory(meter, logger) (*MetricsFactory, error)`.
- `NewNopFactory() *MetricsFactory` for tests / disabled metrics.
- Builder pattern: `.WithLabels(map)` or `.WithAttributes(attrs...)` then `.Add()` / `.Set()` / `.Record()`.
- Convenience recorders: `RecordAccountCreated`, `RecordTransactionProcessed`, etc. (no more org/ledger positional args).

### Logging (`commons/log`)

- `Logger` interface: 5 methods -- `Log(ctx, level, msg, fields...)`, `With(fields...)`, `WithGroup(name)`, `Enabled(level)`, `Sync(ctx)`.
- Level constants: `LevelError` (0), `LevelWarn` (1), `LevelInfo` (2), `LevelDebug` (3).
- Field constructors: `String()`, `Int()`, `Bool()`, `Err()`, `Any()`.
- `NewNop() Logger` for test/disabled logging.
- `GoLogger` provides a stdlib-based implementation with CWE-117 log-injection prevention.
- Sanitizer: `SafeError()` and `SanitizeExternalResponse()`.

### Zap adapter (`commons/zap`)

- `New(cfg Config) (*Logger, error)` for construction.
- `Logger` implements `log.Logger` and also exposes `Raw() *zap.Logger`, `Level() zap.AtomicLevel`.
- Direct zap convenience: `Debug()`, `Info()`, `Warn()`, `Error()`, `WithZapFields()`.
- `Config` has `Environment` (typed string), `Level`, `OTelLibraryName` fields.
- Field constructors: `Any()`, `String()`, `Int()`, `Bool()`, `Duration()`, `ErrorField()`.

### HTTP helpers (`commons/net/http`)

- Response: `Respond`, `RespondStatus`, `RespondError`, `RenderError`, `FiberErrorHandler`. Individual status helpers (BadRequestError, etc.) are removed.
- Health: `Ping` (returns `"pong"`), `HealthWithDependencies(deps...)` with AND semantics (both circuit breaker and health check must pass).
- Reverse proxy: `ServeReverseProxy(target, policy, res, req) error` with `ReverseProxyPolicy` for SSRF protection.
- Pagination: offset-based (`ParsePagination`), opaque cursor (`ParseOpaqueCursorPagination`), timestamp cursor, and sort cursor APIs. All encode functions return errors.
- Validation: `ParseBodyAndValidate`, `ValidateStruct`, `GetValidator`, `ValidateSortDirection`, `ValidateLimit`, `ValidateQueryParamLength`.
- Context/ownership: `ParseAndVerifyTenantScopedID`, `ParseAndVerifyResourceScopedID` with `TenantOwnershipVerifier` and `ResourceOwnershipVerifier` func types.
- Middleware: `WithHTTPLogging`, `WithGrpcLogging`, `WithCORS`, `AllowFullOptionsWithCORS`, `WithBasicAuth`, `NewTelemetryMiddleware`.
- `ErrorResponse` has `Code int` (not string), `Title`, `Message`; implements `error`.

### Server lifecycle (`commons/server`)

- `ServerManager` exclusively; `GracefulShutdown` is removed.
- `NewServerManager(licenseClient, telemetry, logger) *ServerManager`.
- Chainable config: `WithHTTPServer`, `WithGRPCServer`, `WithShutdownChannel`, `WithShutdownTimeout`, `WithShutdownHook`.
- `StartWithGracefulShutdown()` (exits on error) or `StartWithGracefulShutdownWithError() error` (returns error).
- `ServersStarted() <-chan struct{}` for test coordination.

### Circuit breaker (`commons/circuitbreaker`)

- `Manager` interface with `NewManager(logger, opts...) (Manager, error)` constructor.
- `GetOrCreate` returns `(CircuitBreaker, error)` and validates config.
- Preset configs: `DefaultConfig()`, `AggressiveConfig()`, `ConservativeConfig()`, `HTTPServiceConfig()`, `DatabaseConfig()`.
- Metrics via `WithMetricsFactory` option.
- `NewHealthCheckerWithValidation(manager, interval, timeout, logger) (HealthChecker, error)`.

### Certificate manager (`commons/certificate`)

- `NewManager(certPath, keyPath string) (*Manager, error)` — loads PEM files at construction; if both paths are empty an unconfigured manager is returned (TLS optional). Returns `ErrIncompleteConfig` when exactly one path is provided.
- Key parsing order: PKCS#8 → PKCS#1 (RSA) → EC (SEC 1). Key file must have mode `0600` or stricter; looser permissions return an error before reading.
- Atomic hot-reload via `(*Manager).Rotate(cert *x509.Certificate, key crypto.Signer, intermediates ...[]byte) error` — validates expiry and public-key match before swapping under a write lock.
- Sentinel errors: `ErrNilManager`, `ErrCertRequired`, `ErrKeyRequired`, `ErrExpired`, `ErrNoPEMBlock`, `ErrKeyParseFailure`, `ErrNotSigner`, `ErrKeyMismatch`, `ErrIncompleteConfig`.
- Read accessors (all nil-safe, read-locked): `GetCertificate() *x509.Certificate`, `GetSigner() crypto.Signer`, `PublicKey() crypto.PublicKey`, `ExpiresAt() time.Time`, `DaysUntilExpiry() int`.
- TLS integration: `TLSCertificate() tls.Certificate` (returns populated `tls.Certificate` struct); `GetCertificateFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error)` — suitable for assignment to `tls.Config.GetCertificate` for transparent hot-reload.
- Package-level helper: `LoadFromFiles(certPath, keyPath string) (*x509.Certificate, crypto.Signer, error)` — validates without touching any manager state, useful for pre-flight checking before calling `Rotate`.
- Package-level helper: `LoadFromFilesWithChain(certPath, keyPath string) (*x509.Certificate, crypto.Signer, [][]byte, error)` — returns the full DER chain for chain-preserving hot-reload.

### SSRF validation (`commons/security/ssrf`)

- `IsBlockedIP(net.IP)` and `IsBlockedAddr(netip.Addr)` for IP-level SSRF blocking. `IsBlockedAddr` is the core; `IsBlockedIP` delegates after conversion.
- `IsBlockedHostname(hostname)` for hostname-level blocking: localhost, cloud metadata endpoints, `.local`/`.internal`/`.cluster.local` suffixes.
- `BlockedPrefixes() []netip.Prefix` returns a copy of the canonical CIDR blocklist (8 ranges: this-network, CGNAT, IETF assignments, TEST-NET-1/2/3, benchmarking, reserved).
- `ValidateURL(ctx, rawURL, opts...)` validates scheme, hostname, and parsed IP without DNS.
- `ResolveAndValidate(ctx, rawURL, opts...) (*ResolveResult, error)` performs DNS-pinned validation. `ResolveResult` has `PinnedURL`, `Authority` (host:port for HTTP Host), `SNIHostname` (for TLS ServerName).
- Functional options: `WithHTTPSOnly()`, `WithAllowPrivateNetwork()`, `WithLookupFunc(fn)`, `WithAllowHostname(hostname)`.
- Sentinel errors: `ErrBlocked`, `ErrInvalidURL`, `ErrDNSFailed`.
- Both `commons/webhook` and `commons/net/http` delegate to this package — it is the single source of truth for SSRF blocking across all Lerian services.

### Assertions (`commons/assert`)

- `New(ctx, logger, component, operation) *Asserter` and return errors instead of panicking.
- Methods: `That()`, `NotNil()`, `NotEmpty()`, `NoError()`, `Never()`, `Halt()`.
- Metrics: `InitAssertionMetrics(factory)`, `GetAssertionMetrics()`, `ResetAssertionMetrics()`.
- Predicates library (`predicates.go`): `Positive`, `NonNegative`, `InRange`, `ValidUUID`, `ValidAmount`, `PositiveDecimal`, `NonNegativeDecimal`, `ValidPort`, `ValidSSLMode`, `DebitsEqualCredits`, `TransactionCanTransitionTo`, `BalanceSufficientForRelease`, and more.

### Runtime (`commons/runtime`)

- Recovery: `RecoverAndLog`, `RecoverAndCrash`, `RecoverWithPolicy` (and `*WithContext` variants).
- Safe goroutines: `SafeGo`, `SafeGoWithContext`, `SafeGoWithContextAndComponent` with `PanicPolicy` (KeepRunning/CrashProcess).
- Panic metrics: `InitPanicMetrics(factory[, logger])`, `GetPanicMetrics()`, `ResetPanicMetrics()`.
- Span recording: `RecordPanicToSpan`, `RecordPanicToSpanWithComponent`.
- Error reporter: `SetErrorReporter(reporter)`, `GetErrorReporter()`.
- Production mode: `SetProductionMode(bool)`, `IsProductionMode() bool`.

### Safe operations (`commons/safe`)

- Math: `Divide`, `DivideRound`, `DivideOrZero`, `DivideOrDefault`, `Percentage`, `PercentageOrZero`, `DivideFloat64`, `DivideFloat64OrZero`.
- Regex: `Compile`, `CompilePOSIX`, `MatchString`, `FindString`, `ClearCache` (all with caching).
- Slices: `First[T]`, `Last[T]`, `At[T]` with error returns and `*OrDefault` variants.

### JWT (`commons/jwt`)

- `Parse(token, secret, allowedAlgs) (*Token, error)` -- signature verification only.
- `ParseAndValidate(token, secret, allowedAlgs) (*Token, error)` -- signature + time claims.
- `Sign(claims, secret, alg) (string, error)`.
- `ValidateTimeClaims(claims)` and `ValidateTimeClaimsAt(claims, now)`.
- `Token.SignatureValid` (bool) -- replaces v1 `Token.Valid`; clarifies signature-only scope.
- Algorithms: `AlgHS256`, `AlgHS384`, `AlgHS512`.

### Data connectors

- **Postgres:** `New(cfg Config) (*Client, error)` with explicit `Config`; `Resolver(ctx)` replaces `GetDB()`. `Primary() (*sql.DB, error)` for raw access. Migrations via `NewMigrator(cfg)`.
- **Mongo:** `NewClient(ctx, cfg, opts...) (*Client, error)`; methods `Client(ctx)`, `ResolveClient(ctx)`, `Database(ctx)`, `Ping(ctx)`, `Close(ctx)`, `EnsureIndexes(ctx, collection, indexes...)`.
- **Redis:** `New(ctx, cfg) (*Client, error)` with topology-based `Config` (standalone/sentinel/cluster). `GetClient(ctx)`, `Close()`, `Status()`, `IsConnected()`, `LastRefreshError()`. `SetPackageLogger(logger)` for nil-receiver diagnostics.
- **Redis locking:** `NewRedisLockManager(conn) (*RedisLockManager, error)` and `LockManager` interface. `LockHandle` for acquired locks. `DefaultLockOptions()`, `RateLimiterLockOptions()`.
- **RabbitMQ:** `*Context()` variants of all lifecycle methods; `HealthCheck() (bool, error)`.

### Dead letter queue (`commons/dlq`)

- `New(conn *libRedis.Client, keyPrefix string, maxRetries int, opts ...Option) *Handler` — returns nil when `conn` is nil; all Handler methods guard against a nil receiver with `ErrNilHandler`.
- Functional options for Handler: `WithLogger`, `WithTracer`, `WithMetrics`, `WithModule`.
- `DLQMetrics` interface: `RecordRetried(ctx, source)`, `RecordExhausted(ctx, source)`, `RecordLost(ctx, source)` — a nop implementation is used when not set.
- Key operations: `Enqueue(ctx, *FailedMessage) error` (RPush), `Dequeue(ctx, source string) (*FailedMessage, error)` (LPop, destructive), `QueueLength(ctx, source string) (int64, error)`.
- Tenant-scoped Redis keys: `"<prefix><tenantID>:<source>"` (e.g. `"dlq:tenant-abc:outbound"`); falls back to `"<prefix><source>"` when no tenant is in context.
- `ScanQueues(ctx, source string) ([]string, error)` — uses `SCAN` (non-blocking) to discover all tenant-scoped keys for a source; suitable for background consumers without tenant context.
- `ExtractTenantFromKey(key, source string) string` — recovers the tenant ID from a scoped Redis key.
- `PruneExhaustedMessages(ctx, source string, limit int) (int, error)` — dequeues up to `limit` messages, discards exhausted ones, re-enqueues the rest; at-most-once semantics.
- Backoff: exponential with AWS Full Jitter, base 30s, floor 5s, computed by `backoffDuration(retryCount)`.
- Sentinel errors: `ErrNilHandler`, `ErrNilRetryFunc`, `ErrMessageExhausted`.
- `NewConsumer(handler *Handler, retryFn RetryFunc, opts ...ConsumerOption) (*Consumer, error)` — errors if handler or retryFn is nil.
- Consumer functional options: `WithConsumerLogger`, `WithConsumerTracer`, `WithConsumerMetrics`, `WithConsumerModule`, `WithPollInterval`, `WithBatchSize`, `WithSources`.
- Consumer lifecycle: `Run(ctx)` — blocks, stops on ctx cancel or `Stop()`; `Stop()` — safe to call multiple times; `ProcessOnce(ctx)` — exported for testing.
- `FailedMessage` fields: `Source`, `OriginalData`, `ErrorMessage`, `RetryCount`, `MaxRetries`, `CreatedAt`, `NextRetryAt`, `TenantID`.

### Idempotency middleware (`commons/net/http/idempotency`)

- `New(conn *libRedis.Client, opts ...Option) *Middleware` — returns nil when `conn` is nil; `Check()` on a nil `*Middleware` returns a pass-through Fiber handler (fail-open by design).
- Functional options: `WithLogger`, `WithKeyPrefix` (default `"idempotency:"`), `WithKeyTTL` (default 7 days), `WithMaxKeyLength` (default 256), `WithRedisTimeout` (default 500ms), `WithRejectedHandler`, `WithMaxBodyCache` (default 1 MB).
- `(*Middleware).Check() fiber.Handler` — registers the middleware on a Fiber route.
- Only applies to mutating methods (POST, PUT, PATCH, DELETE); GET/HEAD/OPTIONS pass through unconditionally.
- Idempotency key is read from the `X-Idempotency` request header (`constants.IdempotencyKey`); missing key passes through.
- Key too long → 400 JSON `VALIDATION_ERROR` (or custom `WithRejectedHandler`).
- Redis SetNX atomically claims the key as `"processing"` for the TTL duration.
- Duplicate request behavior:
  - Cached response available → replays status + body verbatim, sets `Idempotency-Replayed: true` header.
  - Key in `"processing"` state (in-flight) → 409 JSON `IDEMPOTENCY_CONFLICT`.
  - Key in `"complete"` but no cached body → 200 JSON `IDEMPOTENT`.
- On handler success: stores response body under `<key>:response` (if ≤ `maxBodyCache`), marks key as `"complete"`.
- On handler error: deletes both keys so the client may retry with the same idempotency key.
- Redis unavailable → fail-open (request proceeds without idempotency enforcement, logged at WARN).
- Keys are tenant-scoped: `"<prefix><tenantID>:<idempotencyKey>"`.

### Webhook delivery (`commons/webhook`)

- `NewDeliverer(lister EndpointLister, opts ...Option) *Deliverer` — returns nil when `lister` is nil; both `Deliver` and `DeliverWithResults` guard against a nil receiver.
- `EndpointLister` interface: `ListActiveEndpoints(ctx context.Context) ([]Endpoint, error)`.
- Functional options: `WithLogger`, `WithTracer`, `WithMetrics`, `WithMaxConcurrency` (default 20), `WithMaxRetries` (default 3), `WithHTTPClient`, `WithSecretDecryptor`.
- `Deliver(ctx, *Event) error` — fans out to all active endpoints concurrently; only returns an error for pre-flight failures (nil receiver, nil event, endpoint listing failure). Per-endpoint failures are logged + metricked but do not propagate.
- `DeliverWithResults(ctx, *Event) []DeliveryResult` — same fan-out, returns one `DeliveryResult` per active endpoint for callers that need individual outcomes.
- `Endpoint` fields: `ID`, `URL`, `Secret` (plaintext or `enc:` prefix for encrypted), `Active`.
- `Event` fields: `Type`, `Payload []byte`, `Timestamp int64` (Unix epoch seconds).
- `DeliveryResult` fields: `EndpointID`, `StatusCode`, `Success`, `Error`, `Attempts`.
- `DeliveryMetrics` interface: `RecordDelivery(ctx, endpointID string, success bool, statusCode, attempts int)`.
- `SecretDecryptor` type: `func(encrypted string) (string, error)` — receives ciphertext with `enc:` prefix stripped. No decryptor + encrypted secret = fail-closed (delivery skipped with error).
- SSRF protection: `resolveAndValidateIP` performs a single DNS lookup, validates all resolved IPs against private/loopback/link-local/CGNAT/RFC-reserved ranges, then pins the URL to the first resolved IP — eliminates DNS rebinding TOCTOU. Only `http` and `https` schemes are allowed.
- HMAC signing: `X-Webhook-Signature: sha256=<hex(HMAC-SHA256(payload, secret))>`. Timestamp is NOT included in the signature — replay protection is the receiver's responsibility.
- HTTP client blocks all redirects to prevent SSRF bypass via 302 to internal addresses.
- Retry strategy: exponential backoff with jitter (`commons/backoff`), base 1s. Non-retryable on 4xx except 429.
- Sentinel errors: `ErrNilDeliverer`, `ErrSSRFBlocked`, `ErrDeliveryFailed`, `ErrInvalidURL`.

### Runtime configuration (`commons/systemplane`)

- Dual-backend hot-reload config store. Consumers choose at construction: `NewPostgres(db *sql.DB, listenDSN string, opts ...Option) (*Client, error)` or `NewMongoDB(client *mongo.Client, database string, opts ...Option) (*Client, error)`.
- Client lifecycle: construct → `Register(namespace, key, default, opts...)` for each known key → `Start(ctx)` (hydrates from store, begins Subscribe) → runtime operations → `Close()`. Register after Start returns `ErrRegisterAfterStart`. Nil-receiver safe on all read paths.
- Reads: `Get(ns, key) (any, bool)` plus typed accessors `GetString`, `GetInt`, `GetBool`, `GetFloat64`, `GetDuration`. All nil-safe; return zero values on miss.
- Writes: `Set(ctx, ns, key, value, actor)` — last-write-wins, write-through cache for same-process read consistency, fires subscribers via the changefeed echo (not synchronously from Set).
- Subscriptions: `OnChange(ns, key, fn)` returns an `unsubscribe` func. Callbacks invoked serially with panic recovery via `commons/runtime.RecoverAndLog`. Nil-receiver safe.
- Listing/metadata: `List(namespace) []ListEntry` returns sorted entries in a namespace; `KeyRedaction(ns, key) RedactPolicy` for admin redaction.
- Namespaces are free-text (convention: `"global"`, `"tenant:<id>"`, `"feature-flags"`). Authorization is enforced at the admin HTTP boundary, not in the Client.
- Registered keys carry: default value, description, validator func, redaction policy (`RedactNone | RedactMask | RedactFull`). Options: `WithDescription`, `WithValidator`, `WithRedaction`.
- Client options: `WithLogger`, `WithTelemetry`, `WithDebounce` (default 100ms), `WithListenChannel` (Postgres default `"systemplane_changes"`), `WithTable` (Postgres default `"systemplane_entries"`), `WithCollection` (MongoDB default `"systemplane_entries"`), `WithPollInterval` (MongoDB — non-zero switches from change-streams to polling; required for standalone MongoDB without a replica set).
- Admin HTTP surface (`commons/systemplane/admin`): `Mount(router, client, opts...)` registers three routes at a configurable prefix (default `/system`): `GET :prefix/:namespace` (list), `GET :prefix/:namespace/:key` (read), `PUT :prefix/:namespace/:key` (write). Options: `WithPathPrefix`, `WithAuthorizer` (hook with `"read"` / `"write"` actions), `WithActorExtractor`. Values are redacted per the registered `RedactPolicy` before responding.
- Internal `Store` interface (`internal/store`) has two implementations: `internal/postgres` (LISTEN/NOTIFY, pgx/v5) and `internal/mongodb` (change streams with polling fallback, mongo-driver/v2). Both satisfy a backend-agnostic contract suite in `systemplanetest.Run(t, factory)`.
- Sentinel errors: `ErrClosed`, `ErrNotStarted`, `ErrAlreadyStarted`, `ErrRegisterAfterStart`, `ErrUnknownKey`, `ErrValidation`.
- `NewForTesting(s TestStore, opts ...Option) (*Client, error)` is an explicit out-of-package test helper for consumers that need a Client bound to a caller-controlled store (e.g., the admin subpackage's tests). Not a promised production API.
- Scope: runtime-mutable knobs only. Bootstrap-only config (DB DSNs, secrets, TLS paths, telemetry init, server identity) should live in env-vars or the secret manager, not in systemplane.

### Other packages

- **Backoff:** `ExponentialWithJitter()` and `WaitContext()`. Used by redis and postgres for retry rate-limiting.
- **Errgroup:** `WithContext(ctx) (*Group, context.Context)`; `Go(fn)` with panic recovery; `SetLogger(logger)`.
- **Crypto:** `Crypto` struct with `GenerateHash`, `InitializeCipher`, `Encrypt`, `Decrypt`. `String()` / `GoString()` redact credentials.
- **License:** `New(opts...) *ManagerShutdown` with `WithLogger()` option. `SetHandler()`, `Terminate()`, `TerminateWithError()`, `TerminateSafe()`.
- **Pointers:** `String()`, `Bool()`, `Time()`, `Int()`, `Int64()`, `Float64()`.
- **Cron:** `Parse(expr) (Schedule, error)`; `Schedule.Next(t) (time.Time, error)`.
- **Security:** `IsSensitiveField(name)`, `DefaultSensitiveFields()`, `DefaultSensitiveFieldsMap()`.
- **SSRF:** `IsBlockedIP()`, `IsBlockedAddr()`, `IsBlockedHostname()`, `BlockedPrefixes()`, `ValidateURL()`, `ResolveAndValidate()`. Single source of truth for all SSRF protection. Both `webhook` and `net/http` delegate here.
- **Transaction:** `BuildIntentPlan()` + `ValidateBalanceEligibility()` + `ApplyPosting()` with typed `IntentPlan`, `Posting`, `LedgerTarget`. `ResolveOperation(pending, isSource, status) (Operation, error)`.
- **Constants:** `SanitizeMetricLabel(value) string` for OTEL label safety.
- **Certificate:** `NewManager(certPath, keyPath) (*Manager, error)`; `Rotate(cert, key)`, `TLSCertificate()`, `GetCertificateFunc()`; package-level `LoadFromFiles(certPath, keyPath)` for pre-flight validation.
- **DLQ:** `New(conn, keyPrefix, maxRetries, opts...) *Handler`; `NewConsumer(handler, retryFn, opts...) (*Consumer, error)`; `Run(ctx)` / `Stop()` / `ProcessOnce(ctx)` for consumer lifecycle.
- **Idempotency:** `New(conn, opts...) *Middleware`; `(*Middleware).Check() fiber.Handler`; fail-open when Redis is unavailable.
- **Webhook:** `NewDeliverer(lister, opts...) *Deliverer`; `Deliver(ctx, event) error`; `DeliverWithResults(ctx, event) []DeliveryResult`; SSRF-protected with DNS pinning and HMAC-SHA256 signing.

## Coding rules

- Do not add `panic(...)` in production paths.
- Do not swallow errors; return or handle with context.
- Keep exported docs aligned with behavior.
- Reuse existing package patterns before introducing new abstractions.
- Avoid introducing high-cardinality telemetry labels by default.
- Use the structured log interface (`Log(ctx, level, msg, fields...)`) -- do not add printf-style methods.

## Testing and validation

### Core commands

- `make test` -- run unit tests (uses gotestsum if available)
- `make test-unit` -- run unit tests excluding integration
- `make test-integration` -- run integration tests with testcontainers (requires Docker)
- `make test-all` -- run all tests (unit + integration)
- `make ci` -- run the local fix + verify pipeline (`lint-fix`, `format`, `tidy`, `check-tests`, `sec`, `vet`, `test-unit`, `test-integration`)
- `make lint` -- run lint checks (read-only)
- `make lint-fix` -- auto-fix lint issues
- `make build` -- build all packages
- `make format` -- format code with gofmt
- `make tidy` -- clean dependencies
- `make vet` -- run `go vet` on all packages
- `make sec` -- run security checks using gosec (`SARIF=1` for SARIF output)
- `make clean` -- clean build artifacts

### Coverage

- `make coverage-unit` -- unit tests with coverage report (respects `.ignorecoverunit`)
- `make coverage-integration` -- integration tests with coverage
- `make coverage` -- run all coverage targets

### Test flags

- `LOW_RESOURCE=1` -- sets `-p=1 -parallel=1`, disables `-race` for constrained machines
- `RETRY_ON_FAIL=1` -- retries failed tests once
- `RUN=<pattern>` -- filter integration tests by name pattern
- `PKG=<path>` -- filter to specific package(s)
- `DISABLE_OSX_LINKER_WORKAROUND=1` -- disable macOS ld_classic workaround

### Integration test conventions

- Test files: `*_integration_test.go`
- Test functions: `TestIntegration_<Name>`
- Build tag: `integration`

### Other

- `make tools` -- install gotestsum
- `make check-tests` -- verify test coverage for packages
- `make setup-git-hooks` -- install git hooks
- `make check-hooks` -- verify git hooks installation
- `make check-envs` -- check hooks + environment file security
- `make goreleaser` -- create release snapshot

## Migration awareness

- If a task touches renamed/removed v1 symbols, update `MIGRATION_MAP.md`.
- If a task changes package-level behavior or API expectations, update `README.md`.

## Project rules

- Full coding standards, architecture patterns, and development guidelines are in [`docs/PROJECT_RULES.md`](docs/PROJECT_RULES.md).

## Documentation policy

- Keep docs factual and code-backed.
- Avoid speculative roadmap text.
- Prefer concise package-level examples that compile with current API names.
- When adding, removing, or changing any environment variable consumed by lib-commons, update `.env.reference` in the same change.
