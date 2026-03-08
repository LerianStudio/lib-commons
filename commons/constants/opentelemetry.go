package constant

// TelemetrySDKName identifies this library in OTEL telemetry resource attributes.
const TelemetrySDKName = "lib-commons/opentelemetry"

// MaxMetricLabelLength is the maximum length for metric labels to prevent cardinality explosion.
// Used by assert, runtime, and circuitbreaker packages for label sanitization.
const MaxMetricLabelLength = 64

// Telemetry attribute key prefixes.
const (
	// AttrPrefixAppRequest is the prefix for application request attributes.
	AttrPrefixAppRequest = "app.request."
	// AttrPrefixAssertion is the prefix for assertion event attributes.
	AttrPrefixAssertion = "assertion."
	// AttrPrefixPanic is the prefix for panic event attributes.
	AttrPrefixPanic = "panic."
)

// Telemetry attribute keys for database connectors.
const (
	// AttrDBSystem is the OTEL semantic convention attribute key for the database system name.
	AttrDBSystem = "db.system"
	// AttrDBName is the OTEL semantic convention attribute key for the database name.
	AttrDBName = "db.name"
	// AttrDBMongoDBCollection is the OTEL semantic convention attribute key for the MongoDB collection.
	AttrDBMongoDBCollection = "db.mongodb.collection"
)

// Database system identifiers used as values for AttrDBSystem.
const (
	// DBSystemPostgreSQL is the OTEL semantic convention value for PostgreSQL.
	DBSystemPostgreSQL = "postgresql"
	// DBSystemMongoDB is the OTEL semantic convention value for MongoDB.
	DBSystemMongoDB = "mongodb"
	// DBSystemRedis is the OTEL semantic convention value for Redis.
	DBSystemRedis = "redis"
	// DBSystemRabbitMQ is the OTEL semantic convention value for RabbitMQ.
	DBSystemRabbitMQ = "rabbitmq"
)

// Telemetry metric names.
const (
	// MetricPanicRecoveredTotal is the counter metric for recovered panics.
	MetricPanicRecoveredTotal = "panic_recovered_total"
	// MetricAssertionFailedTotal is the counter metric for failed assertions.
	MetricAssertionFailedTotal = "assertion_failed_total"
)

// Telemetry event names.
const (
	// EventAssertionFailed is the span event name for assertion failures.
	EventAssertionFailed = "assertion.failed"
	// EventPanicRecovered is the span event name for recovered panics.
	EventPanicRecovered = "panic.recovered"
)

// SanitizeMetricLabel truncates a label value to MaxMetricLabelLength
// to prevent metric cardinality explosion in OTEL backends.
func SanitizeMetricLabel(value string) string {
	if len(value) > MaxMetricLabelLength {
		return value[:MaxMetricLabelLength]
	}

	return value
}
