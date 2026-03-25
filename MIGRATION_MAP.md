# MIGRATION_MAP

This file maps common migrations from pre-v4 `lib-commons` usage to the current
v4 APIs.

It is intentionally practical: old symbol or pattern -> current v4 replacement.

## Core and context

- Old: `commons.GenerateUUIDv7()` returning only value
  - v4: `commons.GenerateUUIDv7()` returns `(string, error)`

## Telemetry (`commons/opentelemetry`)

- Old: implicit/global-first telemetry setup
  - v4: explicit constructor `opentelemetry.NewTelemetry(cfg)`
- Old: field obfuscator interface usage
  - v4: `Redactor` + `RedactionRule` (`NewDefaultRedactor`, `NewRedactor`)
- Old: using global providers by default
  - v4: opt-in `(*Telemetry).ApplyGlobals()`

## Metrics (`commons/opentelemetry/metrics`)

- Old: ad-hoc metric initialization patterns
  - v4: `metrics.NewMetricsFactory(meter, logger)`
- Old: fire-and-forget metric calls
  - v4: builder operations return errors; handle `.Add()`, `.Set()`, `.Record()` errors
- Old: positional org/ledger metric helpers
  - v4: convenience recorders such as `RecordAccountCreated`, `RecordTransactionProcessed`

## Logging (`commons/log` and `commons/zap`)

- Old: logger interfaces with printf-style methods
  - v4: structured `Logger` with `Log(ctx, level, msg, fields...)`
- Old: custom field structs per adapter
  - v4: typed field constructors (`String`, `Int`, `Bool`, `Err`, `Any`)
- Old: direct zap-only contracts for shared code
  - v4: `commons/zap.Logger` implements `commons/log.Logger`

## HTTP helpers (`commons/net/http`)

- Old: status-specific helper functions for each error kind
  - v4: consolidated `Respond`, `RespondStatus`, `RespondError`, `RenderError`, `FiberErrorHandler`
- Old: legacy reverse proxy helpers without strict SSRF policy
  - v4: `ServeReverseProxy(target, policy, res, req)` with `ReverseProxyPolicy`

## Server lifecycle (`commons/server`)

- Old: `GracefulShutdown` helper usage
  - v4: `ServerManager` (`NewServerManager`, `With*`, `StartWithGracefulShutdown*`)

## JWT (`commons/jwt`)

- Old: single parse API conflating signature and claims semantics
  - v4: split APIs
    - `Parse(token, secret, allowedAlgs)` -> signature verification only
    - `ParseAndValidate(token, secret, allowedAlgs)` -> signature + time claims
- Old: token validity through `Token.Valid`
  - v4: `Token.SignatureValid`

## Postgres (`commons/postgres`)

- Old: immediate connection assumptions via getter-style access
  - v4: `New(cfg)` + lazy connect through `Resolver(ctx)`
- Old: `GetDB()` patterns
  - v4: `Resolver(ctx)` for dbresolver and `Primary()` for raw `*sql.DB`
- Old: migration bootstrapping coupled to connection client
  - v4: explicit `NewMigrator(MigrationConfig)`

## Circuit breaker (`commons/circuitbreaker`)

- Old: constructors and getters without error returns
  - v4: `NewManager(...) (Manager, error)` and `GetOrCreate(...) (CircuitBreaker, error)`

## Runtime and assertions

- Old: panic-centric defensive checks
  - v4: production-safe assertions via `assert.New(...).That/NotNil/NotEmpty/NoError/...`
- Old: ad-hoc panic handling in goroutines
  - v4: `runtime.SafeGo*` and `RecoverWithPolicy*`

## Notes

- If your service still references removed/renamed symbols not listed here, map them
  to the nearest v4 package by behavior and update this file in the same change.
- Keep migrations fail-closed for auth/security code paths and avoid introducing panic
  paths in production logic.
