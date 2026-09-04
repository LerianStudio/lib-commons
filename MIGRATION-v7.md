# lib-commons v7 — migration guide

v7 removes every `github.com/LerianStudio/lib-observability` type from the
public API of lib-commons.

## Why

Go types have **nominal identity**. A single exported field of type
`log.Logger` makes the lib-observability *major* part of the lib-commons
contract: consumers were pinned to whichever major lib-commons happened to
choose, and nobody could move alone. Nine repositories in lockstep.

`commons/obs` declares the same three capabilities using **stdlib types only**.
An adapter written against *any* lib-observability major satisfies them, so
lib-commons and its consumers can now move majors independently.

There is no deprecation layer. No parallel fields, no `WithXxxDeprecated`
options, no shims. Types were replaced in place and the old ones deleted.

---

## 1. The new contracts — `commons/obs`

```go
package obs

const (
	LevelError = 0
	LevelWarn  = 1
	LevelInfo  = 2
	LevelDebug = 3
)

type Logger interface {
	Log(ctx context.Context, level int, msg string, kv ...any)
	Enabled(level int) bool
	Sync(ctx context.Context) error
}

type MetricsRecorder interface {
	AddCounter(ctx context.Context, name, description, unit string, attrs map[string]string, delta int64) error
	SetGauge(ctx context.Context, name, description, unit string, attrs map[string]string, value int64) error
	RecordHistogram(ctx context.Context, name, description, unit string, attrs map[string]string, value float64, buckets []float64) error
}

type TelemetryShutdowner interface {
	ShutdownTelemetry()
}

// Free functions, never methods.
func With(l Logger, kv ...any) Logger
func WithGroup(l Logger, name string) Logger

func Nop() Logger
func NormalizeKV(kv ...any) []any
```

The level scale is identical to `log.Level`: **lower is more severe**.

### Why `With`/`WithGroup` are free functions

An interface method that returns the interface it is declared on cannot be
satisfied by any implementation outside the declaring package — the method set
never matches. That is what sank the obvious design. A free function can name
the local type freely, so `obs.With` returns a lib-commons wrapper that
*delegates* to your logger.

### `RecordHistogram` records integers

lib-observability has no float64 histogram instrument; `MetricsFactory`
produces an OTEL `Int64Histogram`. The parameter is `float64` for call-site
convenience, but the recorder rounds. **Record durations in milliseconds, not
seconds** — `0.004` becomes `0`. `*metrics.MetricsFactory` rejects NaN, ±Inf
and out-of-range values with `metrics.ErrHistogramValueNotRepresentable`
instead of casting blindly.

---

## 2. Getting an `obs.Logger` from a lib-observability logger

There is nothing to get. Since **lib-observability v4** every logger that
library produces — `log.NewNop()`, `*log.GoLogger`, the zap adapter, the value
returned by `NewLoggerFromContext` — carries `Log(ctx, int, string, ...any)`,
`Enabled(int)` and `Sync(ctx)`, so it satisfies `obs.Logger` **directly**.
`*metrics.MetricsFactory` carries the three flattened recorder methods, so it
satisfies `obs.MetricsRecorder` directly. No adapter, no wrapper, no import of
lib-commons' internals:

```go
cfg.Logger          = myLibObsLogger   // log.Logger      -> obs.Logger
cfg.MetricsRecorder = myLibObsFactory  // *MetricsFactory -> obs.MetricsRecorder

sm := server.NewServerManager(licenseClient, telemetry, myLibObsLogger)
```

The same holds for a logger declared in **your** package that has never
imported lib-observability. Three methods and it goes in.

`commons/obs/obsbridge` still exists, but it is no longer an adapter: it holds
three helpers that read the ambient telemetry out of a `context.Context` and
hand it back typed as the `commons/obs` contracts. It is the only package that
reaches lib-observability for *that* — every other importer of the library in
lib-commons is calling real functionality (`tracing`, `constants`, `assert`,
`runtime`, `redaction`, `sqlobs`/`redisobs`/`httpobs`), never converting a
type.

```go
import "github.com/LerianStudio/lib-commons/v7/commons/obs/obsbridge"

logger := obsbridge.LoggerFromContext(ctx)                     // obs.Logger
rec    := obsbridge.MetricsFromContext(ctx)                     // obs.MetricsRecorder
l, tracer, headerID, r := obsbridge.TrackingFromContext(ctx)    // all four at once
```

Nothing outside `obsbridge` requires you to go through it, on any major.

### Writing your own adapter

Only three methods. `obs.NormalizeKV` gives every adapter the same rules for
malformed input (non-string key becomes `arg_N`, trailing key pairs with `nil`):

```go
type myAdapter struct{ base mylog.Logger }

func (a myAdapter) Log(ctx context.Context, level int, msg string, kv ...any) {
	a.base.Log(ctx, mylog.Level(level), msg, toFields(obs.NormalizeKV(kv...))...)
}

func (a myAdapter) Enabled(level int) bool          { return a.base.Enabled(mylog.Level(level)) }
func (a myAdapter) Sync(ctx context.Context) error  { return a.base.Sync(ctx) }
```

`obs.TelemetryShutdowner` needs **no adapter at all**: it names no nominal type,
so `*tracing.Telemetry` from any major satisfies it as-is.

---

## 3. What broke — symbol by symbol

### Type replacements (mechanical)

| Old | New |
| --- | --- |
| `log.Logger` | `obs.Logger` |
| `log.Level` | `int` (`obs.LevelError` … `obs.LevelDebug`) |
| `...log.Field` | `...any` (alternating key/value) |
| `*metrics.MetricsFactory` | `obs.MetricsRecorder` |
| `*tracing.Telemetry` | `obs.TelemetryShutdowner` |
| `log.NewNop()` | `obs.Nop()` |
| `log.String(k, v)` / `log.Int(k, v)` / `log.Bool(k, v)` / `log.Any(k, v)` | `k, v` |
| `log.Err(err)` | `"error", err` |

### Renamed fields and options

| Old | New |
| --- | --- |
| `postgres.Config.MetricsFactory` | `postgres.Config.MetricsRecorder` |
| `mongo.Config.MetricsFactory` | `mongo.Config.MetricsRecorder` |
| `redis.Config.MetricsFactory` | `redis.Config.MetricsRecorder` |
| `rabbitmq.RabbitMQConnection.MetricsFactory` | `rabbitmq.RabbitMQConnection.MetricsRecorder` |
| `circuitbreaker.WithMetricsFactory` | `circuitbreaker.WithMetricsRecorder` |

### Removed

| Symbol | Replacement |
| --- | --- |
| `(*tenantlog.TenantAwareLogger).With(...log.Field) log.Logger` | `obs.With(logger, kv...)` |
| `(*tenantlog.TenantAwareLogger).WithGroup(string) log.Logger` | `obs.WithGroup(logger, name)` |

> **Bug fixed in passing.** Both removed methods returned `l.base.With(...)`,
> handing back the **undecorated base logger**. Every logger derived from a
> `TenantAwareLogger` silently lost `tenant_id`. The free functions wrap instead
> of unwrapping, so the decoration survives — covered by
> `TestObsWithAndWithGroup_KeepTenantIDOnDerivedLoggers`.

### Changed signatures

| Symbol | Before | After |
| --- | --- | --- |
| `commons.SafeIntToUint32` | `(value int, defaultVal uint32, logger log.Logger, fieldName string) uint32` | `(value int) (uint32, bool)` |
| `commons.GetCPUUsage` | `(ctx, *metrics.MetricsFactory)` | `(ctx, func(context.Context, int64) error)` |
| `commons.GetMemUsage` | `(ctx, *metrics.MetricsFactory)` | `(ctx, func(context.Context, int64) error)` |
| `server.NewServerManager` | `(*license.ManagerShutdown, *tracing.Telemetry, log.Logger)` | `(*license.ManagerShutdown, obs.TelemetryShutdowner, obs.Logger)` |

### Every migrated entry point

`obs.Logger` replaced `log.Logger` in all of the following. Call sites need no
change beyond producing an `obs.Logger`:

- `commons`: `WithLogger`, `Launcher.Logger`
- `commons/circuitbreaker`: `NewManager`, `NewHealthCheckerWithValidation`
- `commons/crypto`: `Crypto.Logger`
- `commons/dlq`: `WithLogger`, `WithConsumerLogger`
- `commons/errgroup`: `(*Group).SetLogger`
- `commons/license`: `WithLogger`, `ManagerShutdown.Logger`
- `commons/mongo`: `Config.Logger`
- `commons/net/http`: `ReverseProxyPolicy.Logger`, `WithCORSLogger`
- `commons/net/http/idempotency`: `WithLogger`
- `commons/net/http/openapi`: `ServeSpec`
- `commons/net/http/pacing`: `WithLogger`
- `commons/net/http/ratelimit`: `WithLogger`, `WithRedisStorageLogger`
- `commons/outbox`: `NewDispatcher`
- `commons/outbox/mongo`, `commons/outbox/postgres`: `WithLogger`
- `commons/postgres`: `Config.Logger`, `MigrationConfig.Logger`
- `commons/rabbitmq`: `RabbitMQConnection.Logger`, `WithLogger`
- `commons/redis`: `Config.Logger`, `SetPackageLogger`
- `commons/server`: `NewServerManager`
- `commons/tenant-manager/client`: `NewClient`
- `commons/tenant-manager/consumer`: `NewMultiTenantConsumerWithError`,
  `NewMultiTenantConsumerWithRabbitMQ`, `NewMultiTenantConsumerWithRedis`
- `commons/tenant-manager/event`: `WithDispatcherLogger`, `WithListenerLogger`
- `commons/tenant-manager/log`: `NewTenantAwareLogger`, `(*TenantAwareLogger).Log`, `.Enabled`
- `commons/tenant-manager/mongo`: `MongoConnection.Logger`, `WithLogger`
- `commons/tenant-manager/postgres`: `PostgresConnection.Logger`, `WithLogger`
- `commons/tenant-manager/rabbitmq`: `WithLogger`
- `commons/tenant-manager/tenantcache`: `NewTenantLoader`
- `commons/webhook`: `WithLogger`

---

## 4. Before / after — the entry points midaz uses

### `postgres.Config` and `postgres.MigrationConfig`

```go
// BEFORE
cfg := postgres.Config{
	Logger:         libLogger,          // lib-observability/v2/log.Logger
	MetricsFactory: libFactory,         // *lib-observability/v2/metrics.MetricsFactory
}
mig := postgres.MigrationConfig{Logger: libLogger}

// AFTER
cfg := postgres.Config{
	Logger:          libLogger,   // unchanged: it satisfies obs.Logger as-is
	MetricsRecorder: libFactory,  // unchanged: only the FIELD was renamed
}
mig := postgres.MigrationConfig{Logger: libLogger}
```

### `mongo.Config` and `redis.Config`

```go
// BEFORE
mongoCfg := mongo.Config{Logger: libLogger, MetricsFactory: libFactory}
redisCfg := redis.Config{Logger: libLogger, MetricsFactory: libFactory}

// AFTER
mongoCfg := mongo.Config{Logger: libLogger, MetricsRecorder: libFactory}
redisCfg := redis.Config{Logger: libLogger, MetricsRecorder: libFactory}
```

### `rabbitmq.RabbitMQConnection`

```go
// BEFORE
conn := &rabbitmq.RabbitMQConnection{
	ConnectionStringSource: uri,
	Logger:                 libLogger,
	MetricsFactory:         libFactory,
}

// AFTER
conn := &rabbitmq.RabbitMQConnection{
	ConnectionStringSource: uri,
	Logger:                 libLogger,
	MetricsRecorder:        libFactory,
}
```

### `commons.NewLauncher`

```go
// BEFORE
launcher := commons.NewLauncher(
	commons.WithLogger(libLogger),
	commons.RunApp("api", server),
)

// AFTER
launcher := commons.NewLauncher(
	commons.WithLogger(libLogger),
	commons.RunApp("api", server),
)
```

### `server.NewServerManager`

```go
// BEFORE
sm := server.NewServerManager(licenseClient, telemetry, libLogger)

// AFTER — telemetry is passed UNCHANGED; *tracing.Telemetry satisfies
// obs.TelemetryShutdowner from any major with no adapter.
sm := server.NewServerManager(licenseClient, telemetry, libLogger)
```

### Your own logging calls into lib-commons contracts

```go
// BEFORE
logger.Log(ctx, log.LevelWarn, "publish failed", log.String("topic", topic), log.Err(err))

// AFTER
logger.Log(ctx, obs.LevelWarn, "publish failed", "topic", topic, "error", err)
```

### `SafeIntToUint32`

```go
// BEFORE
port := commons.SafeIntToUint32(rawPort, 8080, logger, "port")

// AFTER
port, ok := commons.SafeIntToUint32(rawPort)
if !ok {
	port = 8080
	logger.Log(ctx, obs.LevelDebug, "invalid port, using default", "value", rawPort, "default", port)
}
```

### `GetCPUUsage` / `GetMemUsage`

```go
// BEFORE
commons.GetCPUUsage(ctx, factory)
commons.GetMemUsage(ctx, factory)

// AFTER — pass the method value; works with any lib-observability major.
commons.GetCPUUsage(ctx, factory.RecordSystemCPUUsage)
commons.GetMemUsage(ctx, factory.RecordSystemMemUsage)
```

---

## 5. Module path rename to `/v7`

**Not applied in this branch, on purpose.** The rename touches every import in
every file and would bury the review of what actually changed. Apply it as the
last commit before release:

```bash
# from the repository root
git ls-files -z '*.go' 'go.mod' '*.md' \
  | xargs -0 sed -i 's|github.com/LerianStudio/lib-commons/v6|github.com/LerianStudio/lib-commons/v7|g'

gofmt -w commons
go mod tidy
go build ./... && go vet -tags unit ./... && go test -tags unit ./...
```

Then, in each consumer:

```bash
git ls-files -z '*.go' 'go.mod' \
  | xargs -0 sed -i 's|github.com/LerianStudio/lib-commons/v6|github.com/LerianStudio/lib-commons/v7|g'
go mod tidy
```

---

## 6. The boundary is enforced

`commons/obs/boundary` walks every non-test file with `go/ast` and fails if any
exported symbol mentions a lib-observability type. The allowlist is **empty**:
since lib-observability v4 declares every logger and recorder parameter with
universal types, not even `commons/obs/obsbridge` needs to name one.

```bash
go test -tags unit ./commons/obs/boundary/
```

Adding an entry to that allowlist re-opens the coupling this whole change
exists to close. Don't.
