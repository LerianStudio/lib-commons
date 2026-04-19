package streaming

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/LerianStudio/lib-commons/v5/commons"
)

// Event is the CloudEvents-aligned envelope produced by a service method.
// Field names are spelled out and free of Kafka/AMQP vocabulary (DX-A02).
//
// Required CloudEvents fields (ce-* headers on the wire):
//
//   - TenantID: maps to ce-tenantid (Lerian extension). Required for
//     non-system events; when SystemEvent is true, TenantID is optional.
//     The library does NOT derive TenantID from ambient context — callers
//     must populate it explicitly (see the struct field doc below).
//   - ResourceType / EventType: composed into ce-type as
//     "studio.lerian.<ResourceType>.<EventType>".
//   - EventID: maps to ce-id. Auto-populated by ApplyDefaults using uuid.NewV7.
//   - SchemaVersion: maps to ce-schemaversion (extension). Default "1.0.0".
//     When the parsed major version is >= 2, Topic() appends ".v<major>".
//   - Timestamp: maps to ce-time. Auto-populated to time.Now().UTC() when zero.
//   - Source: maps to ce-source. Required, e.g. "//lerian.midaz/transaction-service".
//
// Optional CloudEvents fields:
//
//   - Subject: maps to ce-subject. Typically the aggregate ID.
//   - DataContentType: maps to ce-datacontenttype. Default "application/json".
//   - DataSchema: maps to ce-dataschema. Optional schema URI.
//
// Lerian extensions:
//
//   - SystemEvent: when true, emits ce-systemevent: "true" and allows an
//     empty TenantID. The PartitionKey becomes "system:" + EventType.
//   - Payload: the raw domain payload bytes, sent unchanged as the Kafka
//     message value. Consumers read metadata from the ce-* headers.
type Event struct {
	// TenantID identifies the tenant that owns this event. Required for
	// non-system events (see SystemEvent). The library does NOT cross-
	// check TenantID against any ambient context value — the caller is
	// responsible for ensuring TenantID matches the authenticated tenant
	// on the request context. Mismatches are silently accepted.
	//
	// Services using lib-commons multitenancy SHOULD set:
	//
	//	tenantID, _ := tmcore.GetTenantIDContext(ctx)
	//	event.TenantID = tenantID
	TenantID      string
	ResourceType  string
	EventType     string
	EventID       string
	SchemaVersion string
	Timestamp     time.Time
	Source        string

	Subject         string
	DataContentType string
	DataSchema      string

	// SystemEvent marks this event as platform-level (not tenant-scoped).
	// When true, the producer emits ce-systemevent: "true", omits
	// ce-tenantid from headers, and uses "system:" + EventType as the
	// partition key.
	//
	// This is a privileged capability. The producer MUST be constructed
	// with WithAllowSystemEvents() — otherwise preFlight rejects the emit
	// with ErrSystemEventsNotAllowed. FORBIDDEN for per-tenant service
	// flows: a buggy service that sets SystemEvent=true would hijack the
	// system:* partition space.
	SystemEvent bool
	Payload     json.RawMessage
}

// defaultSchemaVersion is the ce-schemaversion used when the caller leaves
// Event.SchemaVersion empty. Chosen so Topic() yields the base form (no
// ".v<major>" suffix) for first-version events.
const defaultSchemaVersion = "1.0.0"

// defaultDataContentType is the ce-datacontenttype used when the caller
// leaves Event.DataContentType empty. Matches the CloudEvents spec default.
const defaultDataContentType = "application/json"

// topicPrefix is the namespace prefix for every Lerian streaming topic.
// Downstream consumers rely on this prefix to route to the streaming bus.
const topicPrefix = "lerian.streaming."

// Topic returns the derived Kafka topic name for this event.
//
// Base form: "lerian.streaming.<ResourceType>.<EventType>".
//
// When the parsed major version of SchemaVersion is >= 2, Topic appends
// ".v<major>" — e.g. SchemaVersion="2.3.1" yields
// "lerian.streaming.<resource>.<event>.v2". Invalid or empty semver falls
// through to the base form.
//
// Semver parsing is delegated to golang.org/x/mod/semver.Major, which is the
// canonical Go ecosystem library for semver classification. Input is accepted
// both with and without a leading "v" — we normalize to the "v"-prefixed form
// before delegating (golang.org/x/mod/semver requires the "v" prefix).
func (e *Event) Topic() string {
	if e == nil {
		return ""
	}

	base := topicPrefix + e.ResourceType + "." + e.EventType

	major := parseMajorVersion(e.SchemaVersion)
	if major < 2 {
		return base
	}

	return base + ".v" + strconv.Itoa(major)
}

// PartitionKey returns the Kafka partition key for this event.
//
// Default: TenantID — preserves per-tenant FIFO ordering under a sticky-key
// partitioner.
//
// When SystemEvent is true: "system:" + EventType. Gives tenant-less events
// a deterministic key so they still partition cleanly.
//
// Operators may override this per-Emitter via WithPartitionKey. This method
// returns the struct-level default only.
func (e *Event) PartitionKey() string {
	if e == nil {
		return ""
	}

	if e.SystemEvent {
		return "system:" + e.EventType
	}

	return e.TenantID
}

// ApplyDefaults MUTATES the receiver in place, filling zero-valued optional
// fields with sensible defaults:
//
//   - EventID → commons.GenerateUUIDv7().String() when empty
//   - Timestamp → time.Now().UTC() when zero
//   - SchemaVersion → "1.0.0" when empty
//   - DataContentType → "application/json" when empty
//
// Explicit values are preserved. Safe to call on a fully-populated event.
//
// Non-destructiveness on the Emit path is a property of Emit (which passes
// event by value, so ApplyDefaults lands on a local copy), NOT a property of
// this method. External callers who invoke (*Event).ApplyDefaults() on their
// own struct WILL see mutation on the receiver.
//
// If UUIDv7 generation fails (vanishingly unlikely — falls back to random
// bytes), EventID is left empty and the caller's own validation can surface
// the issue.
func (e *Event) ApplyDefaults() {
	if e == nil {
		return
	}

	if e.EventID == "" {
		if id, err := commons.GenerateUUIDv7(); err == nil {
			e.EventID = id.String()
		}
	}

	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}

	if e.SchemaVersion == "" {
		e.SchemaVersion = defaultSchemaVersion
	}

	if e.DataContentType == "" {
		e.DataContentType = defaultDataContentType
	}
}

// parseMajorVersion extracts the major version from a semver string. Returns
// 0 on any parse failure so callers can guard with "< 2" to fall through to
// the base topic form.
//
// Accepts input with or without a leading "v" (e.g. "v2.3.1" or "2.3.1").
// Delegates to golang.org/x/mod/semver.Major, which is the canonical semver
// classifier in the Go ecosystem. We normalize the input to the "v"-prefixed
// form because semver.Major requires it; a missing "v" would be reported as
// an invalid semver otherwise.
func parseMajorVersion(v string) int {
	if v == "" {
		return 0
	}

	// Fast path for the overwhelmingly-common production case: first-major
	// schemas. defaultSchemaVersion ("1.0.0") is the value ApplyDefaults
	// writes when the caller leaves SchemaVersion empty, so the vast
	// majority of events flowing through Topic() hit this branch. Bypassing
	// semver.Major here saves a full semver parse per Emit.
	if v == defaultSchemaVersion || v == "v"+defaultSchemaVersion || v == "1" || v == "v1" {
		return 1
	}

	// semver.Major requires a leading "v". Normalize by re-prefixing.
	trimmed := strings.TrimPrefix(v, "v")
	canonical := "v" + trimmed

	major := semver.Major(canonical)
	if major == "" {
		return 0
	}

	// semver.Major returns "vN" on success; strip the "v" and parse.
	n, err := strconv.Atoi(strings.TrimPrefix(major, "v"))
	if err != nil || n < 0 {
		return 0
	}

	return n
}
