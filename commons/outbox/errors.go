package outbox

import "errors"

var (
	ErrOutboxEventRequired        = errors.New("outbox event is required")
	ErrOutboxRepositoryRequired   = errors.New("outbox repository is required")
	ErrOutboxDispatcherRequired   = errors.New("outbox dispatcher is required")
	ErrOutboxDispatcherRunning    = errors.New("outbox dispatcher is already running")
	ErrOutboxEventPayloadRequired = errors.New("outbox event payload is required")
	ErrOutboxEventPayloadTooLarge = errors.New("outbox event payload exceeds maximum allowed size")
	ErrOutboxEventPayloadNotJSON  = errors.New("outbox event payload must be valid JSON (stored as JSONB)")
	ErrHandlerRegistryRequired    = errors.New("handler registry is required")
	ErrEventTypeRequired          = errors.New("event type is required")
	ErrEventHandlerRequired       = errors.New("event handler is required")
	ErrHandlerAlreadyRegistered   = errors.New("event handler already registered")
	ErrHandlerNotRegistered       = errors.New("event handler is not registered")
	ErrTenantIDRequired           = errors.New("tenant id is required")
	ErrOutboxStatusInvalid        = errors.New("invalid outbox status")
	ErrOutboxTransitionInvalid    = errors.New("invalid outbox status transition")
	ErrReplayConflict             = errors.New("outbox event replay conflict: same id with divergent content")
)

// ErrOutboxRetentionConfigInvalid is returned by NewDispatcher for a negative
// retention window or, while retention is enabled, a negative batch size.
var ErrOutboxRetentionConfigInvalid = errors.New("invalid outbox retention config")

// ErrOutboxRetentionUnsupported is returned by NewDispatcher when a retention
// is enabled on a repository that does not implement PublishedPurger (for
// WithRetentionPublished) or InvalidPurger (for WithRetentionInvalid).
var ErrOutboxRetentionUnsupported = errors.New("outbox repository does not support the enabled retention")
