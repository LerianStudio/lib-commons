# Telemetry Dictionary

## _meta

```yaml
generated_at: "2026-05-07T00:00:00Z"
schema_version: "1.0.0"
target: "testdata/services/broken-crosscut"
tool: "telemetry-inventory"
```

## Counters

### broken_total

```yaml
description: ""
emission_sites:
  - file: "main.go"
    line: 16
    tier: 2
instrument_type: "counter"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: "1"
```

## Histograms

_None detected._

## Gauges

_None detected._

## Spans

### Broken.Op

```yaml
attributes: []
emission_sites:
  - file: "main.go"
    line: 14
record_on_error_observed: false
status_on_error_observed: false
unbounded_span: false
```

## Log Fields

### operation

```yaml
emission_sites:
  - file: "main.go"
    line: 23
level_distribution:
  info: 1
pii_risk_flag: false
```

## Frameworks

_None detected._

## Cross-Cutting

### error_attribution at main.go

```yaml
detail: "error branch missing span.RecordError, span.SetStatus, error log"
function: "broken"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 17
```

### error_attribution at main.go

```yaml
detail: "error branch missing span.RecordError, span.SetStatus, error log"
function: "broken"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 20
```

### error_attribution at main.go

```yaml
detail: "error branch missing span.RecordError, span.SetStatus, error log"
function: "broken"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 24
```

### tenant_consistency at main.go

```yaml
detail: "metric is tenant-scoped but span lacks tenant_id attribute"
function: "broken"
kind: "tenant_consistency"
site:
  - file: "main.go"
    line: 14
```

### tenant_consistency at main.go

```yaml
detail: "metric is tenant-scoped but logs lack tenant_id field"
function: "broken"
kind: "tenant_consistency"
site:
  - file: "main.go"
    line: 23
```

