# Telemetry Dictionary

## _meta

```yaml
generated_at: "2026-05-07T00:00:00Z"
schema_version: "1.0.0"
target: "testdata/services/pure-tier2"
tool: "telemetry-inventory"
```

## Counters

### ledger_tx_total

```yaml
description: "ledger transactions"
emission_sites:
  - file: "main.go"
    line: 11
    tier: 2

instrument_type: "counter"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: "1"
```

## Histograms

### ledger_tx_duration_ms

```yaml
buckets: [1, 5, 10]
description: "ledger latency"
emission_sites:
  - file: "main.go"
    line: 18
    tier: 2

instrument_type: "histogram"
label_cardinality_estimate: 1
labels: ["tenant_id"]
tenant_scoped: true
unit: "ms"
```

## Gauges

_None detected._

## Spans

_None detected._

## Log Fields

_None detected._

## Frameworks

_None detected._

## Cross-Cutting

### error_attribution at main.go

```yaml
detail: "error branch missing span.RecordError, span.SetStatus, error log"
function: "emit"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 12

```

### error_attribution at main.go

```yaml
detail: "error branch missing span.RecordError, span.SetStatus, error log"
function: "emit"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 15

```

### error_attribution at main.go

```yaml
detail: "error branch missing span.RecordError, span.SetStatus, error log"
function: "emit"
kind: "error_attribution"
site:
  - file: "main.go"
    line: 19

```

### trace_correlation at main.go

```yaml
detail: "histogram emitted without an active span in the same function"
function: "emit"
kind: "trace_correlation"
site:
  - file: "main.go"
    line: 18
    tier: 2

```

