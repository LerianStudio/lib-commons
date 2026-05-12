# Telemetry Dictionary

## _meta

```yaml
generated_at: "2026-05-07T00:00:00Z"
schema_version: "1.0.0"
target: "testdata/services/mixed-realistic"
tool: "telemetry-inventory"
```

## Counters

### accounts_created

```yaml
description: "Measures the number of accounts created by the server."
emission_sites:
  - file: "main.go"
    line: 43
    tier: 3
instrument_type: "counter"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: "1"
```

### builder_accounts_total

```yaml
description: "builder accounts"
emission_sites:
  - file: "main.go"
    line: 34
    tier: 2
instrument_type: "counter"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: "1"
```

### direct_accounts_total

```yaml
description: ""
emission_sites:
  - file: "main.go"
    line: 28
    tier: 1
instrument_type: "counter"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: ""
```

## Histograms

### direct_request_duration_ms

```yaml
buckets: [1, 5, 10]
description: ""
emission_sites:
  - file: "main.go"
    line: 31
    tier: 1
instrument_type: "histogram"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: ""
```

## Gauges

_None detected._

## Spans

### Accounts.Create

```yaml
attributes: ["tenant_id"]
emission_sites:
  - file: "main.go"
    line: 24
record_on_error_observed: true
status_on_error_observed: false
unbounded_span: false
```

## Log Fields

### tenant_id

```yaml
emission_sites:
  - file: "main.go"
    line: 26
level_distribution:
  info: 1
pii_risk_flag: false
```

### trace_id

```yaml
emission_sites:
  - file: "main.go"
    line: 26
level_distribution:
  info: 1
pii_risk_flag: false
```

## Frameworks

### grpc/otelgrpc

```yaml
auto_metrics: ["rpc.server.duration"]
auto_spans: ["gRPC server"]
emission_sites:
  - file: "main.go"
    line: 19
```

### lib-commons/http-telemetry

```yaml
auto_metrics: ["http.server.duration"]
auto_spans: ["HTTP route"]
emission_sites:
  - file: "main.go"
    line: 20
```

### net/http/otelhttp

```yaml
auto_metrics: ["http.server.duration", "http.server.request.size"]
auto_spans: ["HTTP route"]
emission_sites:
  - file: "main.go"
    line: 18
```

## Cross-Cutting

### error_attribution at main.go

```yaml
detail: "error branch missing span.SetStatus, error log"
function: "emit"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 35
```

### error_attribution at main.go

```yaml
detail: "error branch missing span.SetStatus, error log"
function: "emit"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 39
```

