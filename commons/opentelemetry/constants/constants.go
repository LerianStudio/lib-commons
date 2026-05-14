// Package constants provides shared constants for OpenTelemetry observability.
// This package delegates to github.com/LerianStudio/lib-observability/constants.
package constants

import libobsconst "github.com/LerianStudio/lib-observability/constants"

// TelemetrySDKName identifies this library in OTEL telemetry resource attributes.
const TelemetrySDKName = libobsconst.TelemetrySDKName

// MaxMetricLabelLength is the maximum length for metric labels to prevent cardinality explosion.
const MaxMetricLabelLength = libobsconst.MaxMetricLabelLength

// Telemetry attribute key prefixes.
const (
	AttrPrefixAppRequest = libobsconst.AttrPrefixAppRequest
	AttrPrefixAssertion  = libobsconst.AttrPrefixAssertion
	AttrPrefixPanic      = libobsconst.AttrPrefixPanic
)

// Telemetry attribute keys for database connectors.
const (
	AttrDBSystem                = libobsconst.AttrDBSystem
	AttrDBName                  = libobsconst.AttrDBName
	AttrDBMongoDBCollection     = libobsconst.AttrDBMongoDBCollection
)

// Database system identifiers.
const (
	DBSystemPostgreSQL = libobsconst.DBSystemPostgreSQL
	DBSystemMongoDB    = libobsconst.DBSystemMongoDB
	DBSystemRedis      = libobsconst.DBSystemRedis
	DBSystemRabbitMQ   = libobsconst.DBSystemRabbitMQ
)

// Telemetry metric names.
const (
	MetricPanicRecoveredTotal  = libobsconst.MetricPanicRecoveredTotal
	MetricAssertionFailedTotal = libobsconst.MetricAssertionFailedTotal
)

// Telemetry event names.
const (
	EventAssertionFailed = libobsconst.EventAssertionFailed
	EventPanicRecovered  = libobsconst.EventPanicRecovered
)

// SanitizeMetricLabel truncates a label value to MaxMetricLabelLength runes.
var SanitizeMetricLabel = libobsconst.SanitizeMetricLabel
