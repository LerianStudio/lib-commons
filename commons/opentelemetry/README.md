# OpenTelemetry v2

This package now exposes a strict v2 API with deliberate breakage from v1.

## Breaking changes

- No fatal initializer. Use `NewTelemetry` and handle returned errors.
- No implicit global mutation during initialization.
- Span helpers use `trace.Span` (value) instead of `*trace.Span`.
- Struct-to-single-JSON-attribute helpers were removed.
- Obfuscation is explicit and deterministic via `Redactor` rules.
- Metrics factory and metric builders return errors (no silent no-op).
- High-cardinality label helpers were removed.

## Create telemetry instance

```go
cfg := opentelemetry.TelemetryConfig{
    LibraryName:               "payments",
    ServiceName:               "payments-api",
    ServiceVersion:            "2.0.0",
    DeploymentEnv:             "prod",
    CollectorExporterEndpoint: "otel-collector:4317",
    EnableTelemetry:           true,
    InsecureExporter:          false,
    Logger:                    log.NewNop(),
}

tl, err := opentelemetry.NewTelemetry(cfg)
if err != nil {
    return err
}
defer tl.ShutdownTelemetry()
```

If you still want global providers, call it explicitly:

```go
tl.ApplyGlobals()
```

## Span attributes from objects

```go
err := opentelemetry.SetSpanAttributesFromValue(span, "request", payload, opentelemetry.NewDefaultRedactor())
```

This flattens nested values into typed attributes (`request.user.id`, `request.amount`, etc.).

## Redaction

```go
redactor, err := opentelemetry.NewRedactor([]opentelemetry.RedactionRule{
    {FieldPattern: `(?i)^password$`, Action: opentelemetry.RedactionMask},
    {FieldPattern: `(?i)^document$`, Action: opentelemetry.RedactionHash},
    {PathPattern: `(?i)^session\.token$`, FieldPattern: `(?i)^token$`, Action: opentelemetry.RedactionDrop},
}, "***")
```

Available actions:

- `RedactionMask`
- `RedactionHash`
- `RedactionDrop`

## Propagation

Use carrier-first APIs:

- `InjectTraceContext(ctx, carrier)`
- `ExtractTraceContext(ctx, carrier)`

Transport adapters remain available for HTTP/gRPC/queue integration.
