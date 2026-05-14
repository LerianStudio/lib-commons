// Package constants provides shared constants for OpenTelemetry observability.
// This package delegates to github.com/LerianStudio/lib-observability/constants.
//
// Deprecated: This package is a compatibility shim. Import github.com/LerianStudio/lib-observability/constants instead.
// This package will be removed in a future major version of lib-commons.
package constants

import libobsconst "github.com/LerianStudio/lib-observability/constants"

// TelemetrySDKName identifies this library in OTEL telemetry resource attributes.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/constants.TelemetrySDKName instead.
const TelemetrySDKName = libobsconst.TelemetrySDKName

// MaxMetricLabelLength is the maximum length for metric labels to prevent cardinality explosion.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/constants.MaxMetricLabelLength instead.
const MaxMetricLabelLength = libobsconst.MaxMetricLabelLength

// Telemetry attribute key prefixes.
const (
	// AttrPrefixAppRequest is the attribute key prefix for application request attributes.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.AttrPrefixAppRequest instead.
	AttrPrefixAppRequest = libobsconst.AttrPrefixAppRequest
	// AttrPrefixAssertion is the attribute key prefix for assertion attributes.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.AttrPrefixAssertion instead.
	AttrPrefixAssertion = libobsconst.AttrPrefixAssertion
	// AttrPrefixPanic is the attribute key prefix for panic attributes.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.AttrPrefixPanic instead.
	AttrPrefixPanic = libobsconst.AttrPrefixPanic
)

// Telemetry attribute keys for database connectors.
const (
	// AttrDBSystem is the attribute key for the database system type.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.AttrDBSystem instead.
	AttrDBSystem = libobsconst.AttrDBSystem
	// AttrDBName is the attribute key for the database name.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.AttrDBName instead.
	AttrDBName = libobsconst.AttrDBName
	// AttrDBMongoDBCollection is the attribute key for the MongoDB collection name.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.AttrDBMongoDBCollection instead.
	AttrDBMongoDBCollection = libobsconst.AttrDBMongoDBCollection
)

// Database system identifiers.
const (
	// DBSystemPostgreSQL identifies PostgreSQL as the database system.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.DBSystemPostgreSQL instead.
	DBSystemPostgreSQL = libobsconst.DBSystemPostgreSQL
	// DBSystemMongoDB identifies MongoDB as the database system.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.DBSystemMongoDB instead.
	DBSystemMongoDB = libobsconst.DBSystemMongoDB
	// DBSystemRedis identifies Redis as the database system.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.DBSystemRedis instead.
	DBSystemRedis = libobsconst.DBSystemRedis
	// DBSystemRabbitMQ identifies RabbitMQ as the messaging system.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.DBSystemRabbitMQ instead.
	DBSystemRabbitMQ = libobsconst.DBSystemRabbitMQ
)

// Telemetry metric names.
const (
	// MetricPanicRecoveredTotal is the metric name for the total count of recovered panics.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.MetricPanicRecoveredTotal instead.
	MetricPanicRecoveredTotal = libobsconst.MetricPanicRecoveredTotal
	// MetricAssertionFailedTotal is the metric name for the total count of failed assertions.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.MetricAssertionFailedTotal instead.
	MetricAssertionFailedTotal = libobsconst.MetricAssertionFailedTotal
)

// Telemetry event names.
const (
	// EventAssertionFailed is the span event name recorded on assertion failures.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.EventAssertionFailed instead.
	EventAssertionFailed = libobsconst.EventAssertionFailed
	// EventPanicRecovered is the span event name recorded on recovered panics.
	//
	// Deprecated: Use github.com/LerianStudio/lib-observability/constants.EventPanicRecovered instead.
	EventPanicRecovered = libobsconst.EventPanicRecovered
)

// SanitizeMetricLabel truncates a label value to MaxMetricLabelLength runes.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/constants.SanitizeMetricLabel instead.
var SanitizeMetricLabel = libobsconst.SanitizeMetricLabel
