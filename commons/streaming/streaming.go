// Package streaming provides a CloudEvents-framed domain event publisher to
// Redpanda/Kafka with circuit-breaker + outbox + per-topic DLQ semantics.
//
// The package's public surface is intentionally narrow: a three-method Emitter
// interface, an Event struct, and a set of functional options. Callers inject
// an Emitter into service constructors and either swap in MockEmitter for tests
// or NoopEmitter for feature-flag-off paths.
//
// This file defines the Emitter interface, the 8-class ErrorClass enum,
// sentinel errors, and the EmitError custom type used to carry structured
// diagnostic context to observers (logs, spans, metrics).
package streaming

import (
	"context"
	"errors"
	"fmt"
)

// Emitter publishes domain events. The three-method surface is the full
// contract every production service sees — Producer, MockEmitter, and
// NoopEmitter all satisfy it.
//
// Emit is safe for concurrent use from any number of goroutines. Implementations
// must document their concurrency and lifecycle semantics in their own godoc.
type Emitter interface {
	// Emit publishes a single Event. The context carries cancellation and
	// deadline; tenant identity is in Event.TenantID. See EmitError for
	// the structured diagnostic envelope returned on failure.
	Emit(ctx context.Context, event Event) error

	// Close releases any underlying connections and flushes buffered records.
	// Close MUST be idempotent: subsequent calls return nil without error.
	// Service methods must not call Close — the app bootstrap owns lifecycle.
	Close() error

	// Healthy reports readiness. Returns nil when the emitter can accept
	// new events. On failure, returns an error whose chain carries a
	// HealthState via the .State() method (see HealthError in health.go).
	Healthy(ctx context.Context) error
}

// ErrorClass classifies the root cause of a publish failure into one of eight
// operational buckets. Used for metric labels, log fields, span attributes,
// and retry-policy decisions. The eight classes are closed — new failures
// must be mapped to one of these by classifyError (see classify.go in T2).
type ErrorClass string

// Error class constants. See TRD §C9 for the mapping rules.
const (
	// ClassSerialization covers payload-shape faults: malformed JSON,
	// records that exceed broker limits, corrupt bytes.
	ClassSerialization ErrorClass = "serialization_error"

	// ClassValidation covers caller-supplied invalid field combinations:
	// missing source, missing tenant on non-system events, etc.
	ClassValidation ErrorClass = "validation_error"

	// ClassAuth covers authorization / authentication failures: bad SASL,
	// topic ACL denied.
	ClassAuth ErrorClass = "auth_error"

	// ClassTopicNotFound covers missing topics that the broker did not
	// auto-create (typical in production where auto-create is off).
	ClassTopicNotFound ErrorClass = "topic_not_found"

	// ClassBrokerUnavailable covers transient broker unreachability: DNS
	// failures, connection refused, cluster rolling restart.
	ClassBrokerUnavailable ErrorClass = "broker_unavailable"

	// ClassNetworkTimeout covers network-level timeouts, including franz-go
	// record delivery timeout.
	ClassNetworkTimeout ErrorClass = "network_timeout"

	// ClassContextCanceled covers caller-side cancellation via ctx.Done or
	// deadline expiry propagated from upstream.
	ClassContextCanceled ErrorClass = "context_canceled"

	// ClassBrokerOverloaded covers quota / throttling / policy-violation
	// responses from the broker.
	ClassBrokerOverloaded ErrorClass = "broker_overloaded"
)

// Sentinel errors. Each maps to a well-defined error condition that callers
// can match with errors.Is. Not all sentinels are caller faults — lifecycle
// and infrastructure errors (ErrEmitterClosed, ErrCircuitOpen,
// ErrOutboxNotConfigured, ErrNilProducer, ErrNilOutboxRegistry) are included
// here alongside caller-correctable validation errors. The full truth table
// lives in IsCallerError and is mirrored in the godoc of each sentinel.
var (
	// ErrMissingTenantID is returned when Event.TenantID is empty and
	// Event.SystemEvent is false. Returned synchronously before any I/O.
	ErrMissingTenantID = errors.New("streaming: tenant_id required for non-system events")

	// ErrSystemEventsNotAllowed is returned synchronously when an Event
	// arrives with SystemEvent=true but the Producer was constructed
	// without WithAllowSystemEvents. SystemEvents bypass tenant discipline
	// and hijack the "system:*" partition space — producers MUST opt in
	// explicitly at bootstrap. See WithAllowSystemEvents for the full
	// contract. Classified as ClassValidation; IsCallerError returns true.
	ErrSystemEventsNotAllowed = errors.New("streaming: system events not permitted; construct Producer with WithAllowSystemEvents()")

	// ErrMissingSource is returned when Event.Source is empty. Source is a
	// required CloudEvents attribute (ce-source).
	ErrMissingSource = errors.New("streaming: Event.Source required (CloudEvents ce-source)")

	// ErrMissingResourceType is returned when Event.ResourceType is empty.
	// ResourceType is part of the derived Kafka topic name and the ce-type
	// header — an empty value yields a degenerate topic (lerian.streaming..)
	// and a malformed ce-type (studio.lerian..EventType). Classified as
	// ClassValidation; IsCallerError returns true.
	ErrMissingResourceType = errors.New("streaming: Event.ResourceType required")

	// ErrMissingEventType is returned when Event.EventType is empty. Same
	// rationale as ErrMissingResourceType — EventType is part of the topic
	// name and the ce-type header.
	ErrMissingEventType = errors.New("streaming: Event.EventType required")

	// ErrInvalidTenantID is returned when Event.TenantID contains control
	// characters (< 0x20 or == 0x7F) or exceeds 256 bytes. Both shapes
	// would corrupt downstream log parsers, OTEL label pipelines, and
	// header consumers. Classified as ClassValidation.
	ErrInvalidTenantID = errors.New("streaming: Event.TenantID contains control chars or exceeds 256 bytes")

	// ErrInvalidResourceType is returned when Event.ResourceType contains
	// control characters or exceeds 128 bytes. ResourceType is part of the
	// Kafka topic name and the ce-type header; a malformed value would
	// break topic routing at the broker level.
	ErrInvalidResourceType = errors.New("streaming: Event.ResourceType contains control chars or exceeds 128 bytes")

	// ErrInvalidEventType is returned when Event.EventType contains control
	// characters or exceeds 128 bytes. Same rationale as
	// ErrInvalidResourceType — EventType lands in the topic name and the
	// ce-type header.
	ErrInvalidEventType = errors.New("streaming: Event.EventType contains control chars or exceeds 128 bytes")

	// ErrInvalidSource is returned when Event.Source contains control
	// characters or exceeds 2048 bytes. Distinct from ErrMissingSource
	// (empty): this sentinel fires on a populated but malformed value.
	ErrInvalidSource = errors.New("streaming: Event.Source contains control chars or exceeds 2048 bytes")

	// ErrInvalidSubject is returned when Event.Subject contains control
	// characters or exceeds 1024 bytes. Subject is an optional CloudEvents
	// attribute but still travels as a header and must be header-safe.
	ErrInvalidSubject = errors.New("streaming: Event.Subject contains control chars or exceeds 1024 bytes")

	// ErrInvalidEventID is returned when Event.EventID exceeds
	// maxEventIDBytes or contains control characters. EventID travels as
	// ce-id; a malformed value would corrupt downstream correlation
	// tooling and trace context.
	ErrInvalidEventID = errors.New("streaming: Event.EventID contains control chars or exceeds 256 bytes")

	// ErrInvalidSchemaVersion is returned when Event.SchemaVersion exceeds
	// maxSchemaVersionBytes or contains control characters. SchemaVersion
	// travels as ce-schemaversion and is used by major-version topic
	// suffixing in Topic().
	ErrInvalidSchemaVersion = errors.New("streaming: Event.SchemaVersion contains control chars or exceeds 64 bytes")

	// ErrInvalidDataContentType is returned when Event.DataContentType
	// exceeds maxDataContentTypeBytes or contains control characters.
	// DataContentType travels as ce-datacontenttype.
	ErrInvalidDataContentType = errors.New("streaming: Event.DataContentType contains control chars or exceeds 256 bytes")

	// ErrInvalidDataSchema is returned when Event.DataSchema exceeds
	// maxDataSchemaBytes or contains control characters. DataSchema
	// travels as ce-dataschema.
	ErrInvalidDataSchema = errors.New("streaming: Event.DataSchema contains control chars or exceeds 2048 bytes")

	// ErrEmitterClosed is returned from Emit after Close has been called.
	ErrEmitterClosed = errors.New("streaming: emitter is closed")

	// ErrEventDisabled is returned when Config.EventToggles has disabled the
	// resource.event combination at runtime.
	ErrEventDisabled = errors.New("streaming: event disabled by configuration toggle")

	// ErrPayloadTooLarge is returned when Event.Payload exceeds the 1 MiB
	// limit. Checked synchronously before any I/O.
	ErrPayloadTooLarge = errors.New("streaming: payload exceeds max size (1 MiB)")

	// ErrNotJSON is returned when Event.Payload fails json.Valid. Prevents
	// malformed messages from reaching consumers and poisoning DLQ replay.
	ErrNotJSON = errors.New("streaming: payload must be valid JSON")

	// ErrMissingBrokers is returned by LoadConfig when STREAMING_ENABLED=true
	// but STREAMING_BROKERS is empty.
	ErrMissingBrokers = errors.New("streaming: STREAMING_BROKERS required when ENABLED=true")

	// ErrInvalidCompression is returned when the compression codec is not
	// one of snappy, lz4, zstd, gzip, none. Surfaced by both LoadConfig
	// (env-var validation) and buildKgoOpts (defensive re-check).
	ErrInvalidCompression = errors.New("streaming: invalid compression codec")

	// ErrInvalidAcks is returned when the required-acks value is not one
	// of all, leader, none. Surfaced by both LoadConfig (env-var validation)
	// and buildKgoOpts (defensive re-check).
	ErrInvalidAcks = errors.New("streaming: invalid required-acks value")

	// ErrNilProducer is returned when a method is invoked on a nil *Producer.
	// Parallels circuitbreaker.ErrNilCircuitBreaker. Callers should treat this
	// as a programming error — a nil Producer indicates construction was
	// skipped or silently failed upstream.
	ErrNilProducer = errors.New("streaming: nil producer")

	// ErrCircuitOpen is returned from Emit when the circuit breaker is open
	// AND no outbox repository has been wired via WithOutboxRepository. When
	// an outbox IS configured, a circuit-open Emit writes to the outbox and
	// returns nil instead — callers never see this sentinel in that case.
	//
	// ErrCircuitOpen is NOT a caller error — it signals runtime
	// infrastructure degradation, not a caller-side mistake. IsCallerError
	// returns false for it.
	ErrCircuitOpen = errors.New("streaming: circuit breaker open")

	// ErrOutboxNotConfigured is returned from publishToOutbox when the
	// Producer has no OutboxRepository wired. Reachable only through the
	// unexported helper; Emit itself falls back to ErrCircuitOpen when the
	// circuit is open and no outbox is configured, not to this sentinel.
	//
	// NOT a caller error — it signals a setup/infrastructure gap (the
	// operator forgot WithOutboxRepository). IsCallerError returns false.
	ErrOutboxNotConfigured = errors.New("streaming: outbox repository not configured for fallback")

	// ErrNilOutboxRegistry is returned from RegisterOutboxHandler when the
	// supplied *outbox.HandlerRegistry is nil. Caller must construct the
	// registry before handing it to the Producer.
	//
	// NOT a caller error in the runtime-validation sense — it signals a
	// wiring bug at bootstrap. IsCallerError returns false. Parallels
	// ErrNilProducer.
	ErrNilOutboxRegistry = errors.New("streaming: outbox registry is nil")
)

// EmitError is the structured error type returned from Emit on publish
// failure. It carries enough context for observers to log, span-annotate, and
// classify the failure without further parsing.
//
// Callers use errors.As to extract the fields and errors.Is to match the
// wrapped Cause. EmitError's Error() method runs through sanitizeBrokerURL so
// credentials are redacted before surfacing.
type EmitError struct {
	// ResourceType is the Event.ResourceType at the time of failure.
	ResourceType string
	// EventType is the Event.EventType at the time of failure.
	EventType string
	// TenantID is the Event.TenantID at the time of failure.
	TenantID string
	// Topic is the derived Kafka topic name.
	Topic string
	// Class is the classified error bucket.
	Class ErrorClass
	// Cause is the underlying error (franz-go kerr, net error, etc.).
	Cause error
}

// Error returns a diagnostic string with credentials stripped via
// sanitizeBrokerURL. Never returns a message containing SASL passwords or
// full credentialed URLs.
func (e *EmitError) Error() string {
	if e == nil {
		return "<nil>"
	}

	cause := ""
	if e.Cause != nil {
		cause = ": " + sanitizeBrokerURL(e.Cause.Error())
	}

	return fmt.Sprintf(
		"streaming emit failed: class=%s topic=%s resource=%s event=%s%s",
		e.Class, e.Topic, e.ResourceType, e.EventType, cause,
	)
}

// Unwrap returns the wrapped Cause so errors.Is / errors.As can walk the chain.
func (e *EmitError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}

// callerErrorClasses enumerates the ErrorClass values that signal a caller-
// correctable fault. Kept as a map for O(1) lookup; matches TRD §C9.
var callerErrorClasses = map[ErrorClass]struct{}{
	ClassSerialization: {},
	ClassValidation:    {},
	ClassAuth:          {}, // auth is a deployment config fault, not a runtime one
}

// callerErrorSentinels enumerates the sentinel errors that signal a caller-
// correctable fault. Used by IsCallerError via errors.Is walking.
var callerErrorSentinels = []error{
	ErrMissingTenantID,
	ErrSystemEventsNotAllowed,
	ErrMissingSource,
	ErrMissingResourceType,
	ErrMissingEventType,
	ErrPayloadTooLarge,
	ErrNotJSON,
	ErrEventDisabled,
	ErrMissingBrokers,
	ErrInvalidCompression,
	ErrInvalidAcks,
	ErrInvalidTenantID,
	ErrInvalidResourceType,
	ErrInvalidEventType,
	ErrInvalidSource,
	ErrInvalidSubject,
	ErrInvalidEventID,
	ErrInvalidSchemaVersion,
	ErrInvalidDataContentType,
	ErrInvalidDataSchema,
}

// IsCallerError reports whether err represents a caller-correctable fault
// (invalid input, misconfiguration, authorization) as opposed to a
// transient infrastructure fault (broker down, network timeout).
//
// Returns true when err (or any error in its chain) is one of:
//   - ErrMissingTenantID, ErrSystemEventsNotAllowed, ErrMissingSource,
//     ErrMissingResourceType, ErrMissingEventType, ErrPayloadTooLarge,
//     ErrNotJSON, ErrEventDisabled, ErrMissingBrokers, ErrInvalidCompression,
//     ErrInvalidAcks
//   - Header-sanitization sentinels: ErrInvalidTenantID, ErrInvalidResourceType,
//     ErrInvalidEventType, ErrInvalidSource, ErrInvalidSubject,
//     ErrInvalidEventID, ErrInvalidSchemaVersion, ErrInvalidDataContentType,
//     ErrInvalidDataSchema
//   - An *EmitError whose Class is ClassSerialization, ClassValidation, or
//     ClassAuth.
//
// Returns false for nil, unrelated errors, ErrEmitterClosed (lifecycle), and
// any *EmitError with an infrastructure class.
func IsCallerError(err error) bool {
	if err == nil {
		return false
	}

	for _, sentinel := range callerErrorSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	var ee *EmitError
	if errors.As(err, &ee) && ee != nil {
		_, ok := callerErrorClasses[ee.Class]
		return ok
	}

	return false
}
