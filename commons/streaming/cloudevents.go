package streaming

import (
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// CloudEvents binary-mode header keys. Every CloudEvents context attribute
// travels as a Kafka record header with the "ce-" prefix (required by the
// CloudEvents Kafka protocol binding, v1.0).
const (
	headerCESpecVersion     = "ce-specversion"
	headerCEID              = "ce-id"
	headerCESource          = "ce-source"
	headerCEType            = "ce-type"
	headerCETime            = "ce-time"
	headerCESubject         = "ce-subject"
	headerCEDataContentType = "ce-datacontenttype"
	headerCEDataSchema      = "ce-dataschema"
	headerCETenantID        = "ce-tenantid"
	headerCESchemaVersion   = "ce-schemaversion"
	headerCEResourceType    = "ce-resourcetype"
	headerCEEventType       = "ce-eventtype"
	headerCESystemEvent     = "ce-systemevent"
)

// cloudEventsSpecVersion is the CloudEvents binary-mode spec version
// literal. Only 1.0 is supported; bumping this requires a coordinated
// downstream consumer upgrade.
const cloudEventsSpecVersion = "1.0"

// ceSpecVersion1_0 is the only CloudEvents specversion accepted by
// ParseCloudEventsHeaders. Mirrors cloudEventsSpecVersion but kept as a
// separate symbol so the parse-side value check reads as explicit
// version-gate rather than accidental coupling with the producer emit path.
const ceSpecVersion1_0 = "1.0"

// cloudEventsTypePrefix is the reverse-DNS namespace prepended to every
// ce-type header value. Composed as "studio.lerian.<resource>.<event>".
// Matches TRD §6.1 verbatim.
const cloudEventsTypePrefix = "studio.lerian."

// buildCloudEventsHeaders assembles the kgo.RecordHeader slice for an Event
// per TRD §6.1. Only required headers are always emitted; optional headers
// (subject, datacontenttype, dataschema) are skipped when empty.
//
// ce-systemevent is emitted with the literal "true" only when
// event.SystemEvent is true — omitting the header is equivalent to "false",
// keeping the wire format lean for the common case.
//
// ce-tenantid is omitted when empty AND event.SystemEvent is true. For a
// non-system event, the caller-side validation in publishDirect has already
// enforced a non-empty TenantID, so the header is always present downstream.
func buildCloudEventsHeaders(event Event) []kgo.RecordHeader {
	// Pre-size the slice for the maximum number of headers we emit so we
	// don't reallocate. 13 is the full set including every optional.
	headers := make([]kgo.RecordHeader, 0, 13)

	headers = append(headers,
		kgo.RecordHeader{Key: headerCESpecVersion, Value: []byte(cloudEventsSpecVersion)},
		kgo.RecordHeader{Key: headerCEID, Value: []byte(event.EventID)},
		kgo.RecordHeader{Key: headerCESource, Value: []byte(event.Source)},
		kgo.RecordHeader{
			Key:   headerCEType,
			Value: []byte(cloudEventsTypePrefix + event.ResourceType + "." + event.EventType),
		},
		kgo.RecordHeader{Key: headerCETime, Value: []byte(event.Timestamp.Format(time.RFC3339Nano))},
		kgo.RecordHeader{Key: headerCESchemaVersion, Value: []byte(event.SchemaVersion)},
		kgo.RecordHeader{Key: headerCEResourceType, Value: []byte(event.ResourceType)},
		kgo.RecordHeader{Key: headerCEEventType, Value: []byte(event.EventType)},
	)

	// Optional: ce-subject.
	if event.Subject != "" {
		headers = append(headers, kgo.RecordHeader{
			Key:   headerCESubject,
			Value: []byte(event.Subject),
		})
	}

	// Optional: ce-datacontenttype. ApplyDefaults ensures "application/json"
	// when unset, so in practice this is always present after pre-flight —
	// but we still guard to keep buildCloudEventsHeaders callable on raw
	// events (useful for tests that construct Event values directly).
	if event.DataContentType != "" {
		headers = append(headers, kgo.RecordHeader{
			Key:   headerCEDataContentType,
			Value: []byte(event.DataContentType),
		})
	}

	// Optional: ce-dataschema.
	if event.DataSchema != "" {
		headers = append(headers, kgo.RecordHeader{
			Key:   headerCEDataSchema,
			Value: []byte(event.DataSchema),
		})
	}

	// Optional: ce-tenantid. Skipped for system events with empty tenant.
	if event.TenantID != "" {
		headers = append(headers, kgo.RecordHeader{
			Key:   headerCETenantID,
			Value: []byte(event.TenantID),
		})
	}

	// Optional: ce-systemevent. Omitted when false (implicit default).
	if event.SystemEvent {
		headers = append(headers, kgo.RecordHeader{
			Key:   headerCESystemEvent,
			Value: []byte("true"),
		})
	}

	return headers
}

// ErrMissingRequiredHeader is returned by ParseCloudEventsHeaders when a
// required CloudEvents header (ce-specversion, ce-id, ce-source, ce-type,
// ce-time) is missing. Exported so integration tests in other packages can
// match the error precisely via errors.Is.
var ErrMissingRequiredHeader = errors.New("streaming: missing required CloudEvents header")

// ParseCloudEventsHeaders is the inverse of buildCloudEventsHeaders. It is
// primarily used by tests that verify the on-wire format round-trips back to
// the same Event shape — particularly the CloudEvents SDK contract test in
// T9 (integration). Real consumers doing production Kafka consumption
// typically reach for cloudevents/sdk-go/v2 instead, which handles the
// full CloudEvents spec (structured mode, extension registry, validation).
//
// Missing required headers surface as ErrMissingRequiredHeader wrapped with
// the specific key. Optional headers (subject, datacontenttype, dataschema,
// systemevent) default to their zero values when absent.
//
// ce-time is parsed via time.RFC3339Nano to preserve sub-second precision
// across the round trip.
//
// When a header key appears multiple times, the LAST occurrence wins.
// Callers relying on duplicate-header semantics should parse headers
// directly from kgo.Record.Headers instead.
func ParseCloudEventsHeaders(headers []kgo.RecordHeader) (Event, error) {
	// Flatten headers into a map so we can do O(1) lookups. The last header
	// with a given key wins — mirrors producer behavior where later keys
	// overwrite earlier ones.
	index := make(map[string]string, len(headers))
	for _, h := range headers {
		index[h.Key] = string(h.Value)
	}

	// Required headers — missing any is a parse failure. ce-specversion
	// must be "1.0" — the only version this package targets.
	specVersion, ok := index[headerCESpecVersion]
	if !ok {
		return Event{}, fmt.Errorf("%w: %s", ErrMissingRequiredHeader, headerCESpecVersion)
	}

	if specVersion != ceSpecVersion1_0 {
		return Event{}, fmt.Errorf("%w: unsupported specversion %q (want %q)", ErrMissingRequiredHeader, specVersion, ceSpecVersion1_0)
	}

	eventID, ok := index[headerCEID]
	if !ok {
		return Event{}, fmt.Errorf("%w: %s", ErrMissingRequiredHeader, headerCEID)
	}

	source, ok := index[headerCESource]
	if !ok {
		return Event{}, fmt.Errorf("%w: %s", ErrMissingRequiredHeader, headerCESource)
	}

	if _, ok := index[headerCEType]; !ok {
		return Event{}, fmt.Errorf("%w: %s", ErrMissingRequiredHeader, headerCEType)
	}

	timeRaw, ok := index[headerCETime]
	if !ok {
		return Event{}, fmt.Errorf("%w: %s", ErrMissingRequiredHeader, headerCETime)
	}

	ts, err := time.Parse(time.RFC3339Nano, timeRaw)
	if err != nil {
		return Event{}, fmt.Errorf("streaming: invalid ce-time %q: %w", timeRaw, err)
	}

	// Extensions are treated as optional on parse — ce-resourcetype and
	// ce-eventtype are always emitted by the producer but we accept inputs
	// from other producers that may not emit them.
	return Event{
		EventID:         eventID,
		Source:          source,
		Timestamp:       ts,
		Subject:         index[headerCESubject],
		DataContentType: index[headerCEDataContentType],
		DataSchema:      index[headerCEDataSchema],
		TenantID:        index[headerCETenantID],
		SchemaVersion:   index[headerCESchemaVersion],
		ResourceType:    index[headerCEResourceType],
		EventType:       index[headerCEEventType],
		SystemEvent:     index[headerCESystemEvent] == "true",
	}, nil
}
