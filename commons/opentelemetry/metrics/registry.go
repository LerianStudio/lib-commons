package metrics

// MetricType identifies an OpenTelemetry instrument category.
type MetricType int

// MetricType values are exposed as package-level constants for use in
// HelperSpec entries and other registry consumers. They share their
// short names (Counter, Histogram, Gauge) with the *MetricsFactory
// instrument-creation methods, which is intentional and unambiguous:
//
//	metrics.Counter            // a MetricType constant (this declaration)
//	factory.Counter(metric)    // an instrument-creation method
//
// Call-site syntax distinguishes the two — package-qualified value vs
// receiver method invocation.
const (
	// Counter is a monotonically increasing instrument.
	Counter MetricType = iota
	// Histogram records a distribution of values.
	Histogram
	// Gauge records a current value at a point in time.
	Gauge
)

// HelperSignatureKind classifies the call shape of a MetricsFactory helper.
// Static analyzers branch on this to know whether trailing arguments are
// label key/value pairs or a scalar value.
type HelperSignatureKind int

const (
	// SignatureAttributesVariadic is (ctx, ...attribute.KeyValue).
	SignatureAttributesVariadic HelperSignatureKind = iota
	// SignatureScalarValue is (ctx, value <numeric>).
	SignatureScalarValue
)

// String returns the lowercase canonical name used in dictionary output.
func (m MetricType) String() string {
	switch m {
	case Counter:
		return "counter"
	case Histogram:
		return "histogram"
	case Gauge:
		return "gauge"
	default:
		return "unknown"
	}
}

// HelperSpec describes a single MetricsFactory.Record* helper in machine-readable form.
type HelperSpec struct {
	// GoFunctionName is the exact Go method name on *MetricsFactory.
	GoFunctionName string
	// MetricName is the Prometheus/OTel metric name the helper emits.
	MetricName string
	// InstrumentType is the OTel instrument category.
	InstrumentType MetricType
	// Unit is the OTel unit string, such as "1", "percentage", or "ms".
	Unit string
	// Description matches the Metric.Description used by the helper.
	Description string
	// DefaultLabels are labels that the helper always attaches.
	// Forward-compat: populated when a helper attaches default labels at
	// metric definition time. Currently unused by all six registry entries
	// because they emit zero default labels; kept on the spec so that a
	// future helper (e.g. RecordTenantQuotaExceeded) can declare the
	// canonical label set without an analyzer-side schema change.
	DefaultLabels []string
	// SignatureKind formalizes the helper call shape so static analyzers
	// can pick the correct argument-extraction strategy. Defaults to
	// SignatureAttributesVariadic (the zero value) for the common case.
	SignatureKind HelperSignatureKind
}

// Helpers is the canonical registry of MetricsFactory.Record* helpers.
//
// Adding a Record* method to MetricsFactory must add an entry here. Removing
// one must remove the entry. TestHelpersMatchRegistry enforces both sides.
var Helpers = []HelperSpec{
	{
		GoFunctionName: "RecordAccountCreated",
		MetricName:     "accounts_created",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of accounts created by the server.",
	},
	{
		GoFunctionName: "RecordTransactionProcessed",
		MetricName:     "transactions_processed",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of transactions processed by the server.",
	},
	{
		GoFunctionName: "RecordTransactionRouteCreated",
		MetricName:     "transaction_routes_created",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of transaction routes created by the server.",
	},
	{
		GoFunctionName: "RecordOperationRouteCreated",
		MetricName:     "operation_routes_created",
		InstrumentType: Counter,
		Unit:           "1",
		Description:    "Measures the number of operation routes created by the server.",
	},
	{
		GoFunctionName: "RecordSystemCPUUsage",
		MetricName:     "system.cpu.usage",
		InstrumentType: Gauge,
		Unit:           metricUnitPercentage,
		Description:    "Current CPU usage percentage of the process host.",
		SignatureKind:  SignatureScalarValue,
	},
	{
		GoFunctionName: "RecordSystemMemUsage",
		MetricName:     "system.mem.usage",
		InstrumentType: Gauge,
		Unit:           metricUnitPercentage,
		Description:    "Current memory usage percentage of the process host.",
		SignatureKind:  SignatureScalarValue,
	},
}
