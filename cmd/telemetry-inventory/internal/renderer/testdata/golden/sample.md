# Telemetry Dictionary

## _meta

```yaml
generated_at: "2026-05-07T00:00:00Z"
schema_version: "1.0.0"
target: "fixture"
tool: "telemetry-inventory"
```

## Counters

### a_counter

```yaml
description: ""
emission_sites:
  - file: "a.go"
    line: 10
    tier: 1

instrument_type: "counter"
label_cardinality_estimate: 0
labels: []
tenant_scoped: false
unit: "1"
```

### z_counter

```yaml
description: ""
emission_sites:
  - file: "b.go"
    line: 20
    tier: 2

instrument_type: "counter"
label_cardinality_estimate: 2
labels: ["result", "tenant_id"]
tenant_scoped: true
unit: "1"
```

## Histograms

### latency_ms

```yaml
buckets: [1, 5, 10]
description: ""
emission_sites:
  - file: "c.go"
    line: 30
    tier: 1

instrument_type: "histogram"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: "ms"
```

## Gauges

### queue_depth

```yaml
description: ""
emission_sites:
  - file: "d.go"
    line: 40
    tier: 3

instrument_type: "gauge"
label_cardinality_estimate: 0
labels: []
tenant_scoped: false
unit: "1"
```

## Spans

### Operation

```yaml
attributes: ["tenant_id"]
emission_sites:
  - file: "e.go"
    line: 50

record_on_error_observed: true
status_on_error_observed: true
unbounded_span: false
```

## Log Fields

### tenant_id

```yaml
emission_sites:
  - file: "f.go"
    line: 60

level_distribution:
  error: 1
  info: 2

pii_risk_flag: false
```

## Frameworks

### net/http/otelhttp

```yaml
auto_metrics: ["http.server.duration"]
auto_spans: ["HTTP route"]
emission_sites:
  - file: "g.go"
    line: 70

```

## Cross-Cutting

### tenant_consistency at h.go

```yaml
detail: "span lacks tenant_id"
function: "Handle"
kind: "tenant_consistency"
site:
  - file: "h.go"
    line: 80

```

