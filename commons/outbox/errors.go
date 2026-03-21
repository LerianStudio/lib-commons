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
)
