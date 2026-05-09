# Telemetry Dictionary

## _meta

```yaml
generated_at: "2026-05-07T00:00:00Z"
schema_version: "1.0.0"
target: "testdata/services/leak-spans"
tool: "telemetry-inventory"
```

## Counters

_None detected._

## Histograms

_None detected._

## Gauges

_None detected._

## Spans

### Balanced.Op

```yaml
attributes: []
emission_sites:
  - file: "main.go"
    line: 10
record_on_error_observed: false
status_on_error_observed: false
unbounded_span: false
```

### Leaky.Op

```yaml
attributes: []
emission_sites:
  - file: "main.go"
    line: 16
record_on_error_observed: false
status_on_error_observed: false
unbounded_span: true
```

## Log Fields

_None detected._

## Frameworks

_None detected._

## Cross-Cutting

_None detected._

