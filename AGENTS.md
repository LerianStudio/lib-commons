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

HTTP and server:
- `commons/net/http`: Fiber HTTP helpers (response, error rendering, cursor/offset/sort pagination, validation, SSRF-protected reverse proxy, CORS, basic auth, telemetry middleware, health checks, access logging)
- `commons/net/http/ratelimit`: Redis-backed rate limit storage for Fiber
- `commons/server`: `ServerManager`-based graceful shutdown and lifecycle helpers

Resilience and safety:
- `commons/circuitbreaker`: circuit breaker manager with preset configs and health checker
- `commons/backoff`: exponential backoff with jitter and context-aware sleep
- `commons/runtime`: panic recovery, panic metrics, safe goroutine wrappers, error reporter, production mode
- `commons/assert`: production-safe assertions with telemetry integration and domain predicates
- `commons/safe`: panic-free math/regex/slice operations with error returns
- `commons/security`: sensitive field detection and handling
- `commons/errgroup`: goroutine coordination with panic recovery

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

### Other packages

- **Backoff:** `ExponentialWithJitter()` and `WaitContext()`. Used by redis and postgres for retry rate-limiting.
- **Errgroup:** `WithContext(ctx) (*Group, context.Context)`; `Go(fn)` with panic recovery; `SetLogger(logger)`.
- **Crypto:** `Crypto` struct with `GenerateHash`, `InitializeCipher`, `Encrypt`, `Decrypt`. `String()` / `GoString()` redact credentials.
- **License:** `New(opts...) *ManagerShutdown` with `WithLogger()` option. `SetHandler()`, `Terminate()`, `TerminateWithError()`, `TerminateSafe()`.
- **Pointers:** `String()`, `Bool()`, `Time()`, `Int()`, `Int64()`, `Float64()`.
- **Cron:** `Parse(expr) (Schedule, error)`; `Schedule.Next(t) (time.Time, error)`.
- **Security:** `IsSensitiveField(name)`, `DefaultSensitiveFields()`, `DefaultSensitiveFieldsMap()`.
- **Transaction:** `BuildIntentPlan()` + `ValidateBalanceEligibility()` + `ApplyPosting()` with typed `IntentPlan`, `Posting`, `LedgerTarget`. `ResolveOperation(pending, isSource, status) (Operation, error)`.
- **Constants:** `SanitizeMetricLabel(value) string` for OTEL label safety.

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
