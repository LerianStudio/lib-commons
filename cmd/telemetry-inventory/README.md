# telemetry-inventory

`telemetry-inventory` statically analyzes a Go service for telemetry primitives and renders a deterministic telemetry dictionary for Grafana/dashboard planning and CI drift gates.

## Install

```bash
go install github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory@latest
```

## Usage

Generate Markdown:

```bash
telemetry-inventory inventory --output-md ./docs/dashboards/telemetry-dictionary.md .
```

Generate JSON to stdout:

```bash
telemetry-inventory inventory --json .
```

Verify a committed dictionary:

```bash
telemetry-inventory verify --committed ./docs/dashboards/telemetry-dictionary.md .
```

Exit codes:

- `0`: dictionary is in sync.
- `1`: telemetry drift detected.
- `2`: usage error or schema mismatch.

## Detection Surface

- Counters, histograms, and gauges across OTel direct calls, `MetricsFactory` builders, and registered `MetricsFactory.Record*` helpers.
- Spans from `trace.Tracer.Start`, including missing `span.End()` detection.
- Structured log fields from `commons/log` and `commons/zap` field constructors, including PII-risk field names.
- Framework auto-instrumentation for OTel HTTP/gRPC/Fiber and lib-commons HTTP telemetry hooks.
- Cross-cutting tenant consistency, error attribution, and trace-correlation findings.

## Schema Versioning

The current schema version is `1.0.0` and is defined in `internal/schema`. It changes only when the dictionary shape changes, independent of the lib-commons module version.

## Development

Add a new analyzer under `internal/analyzers`, return typed findings through `analysis.Analyzer.ResultType`, wire it into `internal/orchestrator`, and add an `analysistest` fixture under `internal/analyzers/testdata/src`.

Golden files are updated with:

```bash
make update-goldens
```
