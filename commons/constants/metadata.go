package constant

import obsconstants "github.com/LerianStudio/lib-observability/v2/constants"

const (
	// MetadataID is the metadata key that carries the request context identifier.
	MetadataID = "metadata_id"
	// MetadataTraceparent is the metadata key for W3C traceparent.
	MetadataTraceparent = obsconstants.MetadataTraceparent
	// MetadataTracestate is the metadata key for W3C tracestate.
	MetadataTracestate = obsconstants.MetadataTracestate
	// MetadataAuthorization is the metadata key for authorization propagation.
	MetadataAuthorization = "authorization"
)
