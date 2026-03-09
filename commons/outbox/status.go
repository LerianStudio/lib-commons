package outbox

import "fmt"

// OutboxEventStatus represents a valid outbox event lifecycle state.
type OutboxEventStatus string

const (
	StatusPending    OutboxEventStatus = OutboxStatusPending
	StatusProcessing OutboxEventStatus = OutboxStatusProcessing
	StatusPublished  OutboxEventStatus = OutboxStatusPublished
	StatusFailed     OutboxEventStatus = OutboxStatusFailed
	StatusInvalid    OutboxEventStatus = OutboxStatusInvalid
)

// ParseOutboxEventStatus validates and converts a raw string status.
func ParseOutboxEventStatus(raw string) (OutboxEventStatus, error) {
	status := OutboxEventStatus(raw)

	if !status.IsValid() {
		return "", fmt.Errorf("%w: %q", ErrOutboxStatusInvalid, raw)
	}

	return status, nil
}

// IsValid reports whether the status is part of the outbox lifecycle.
func (status OutboxEventStatus) IsValid() bool {
	switch status {
	case StatusPending, StatusProcessing, StatusPublished, StatusFailed, StatusInvalid:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether a transition from status to next is allowed.
func (status OutboxEventStatus) CanTransitionTo(next OutboxEventStatus) bool {
	switch status {
	case StatusPending:
		return next == StatusProcessing
	case StatusFailed:
		return next == StatusProcessing
	case StatusProcessing:
		return next == StatusProcessing || next == StatusPublished || next == StatusFailed || next == StatusInvalid
	case StatusPublished, StatusInvalid:
		return false
	default:
		return false
	}
}

// ValidateOutboxTransition validates a status transition using typed lifecycle rules.
func ValidateOutboxTransition(fromRaw, toRaw string) error {
	from, err := ParseOutboxEventStatus(fromRaw)
	if err != nil {
		return fmt.Errorf("from status: %w", err)
	}

	to, err := ParseOutboxEventStatus(toRaw)
	if err != nil {
		return fmt.Errorf("to status: %w", err)
	}

	if !from.CanTransitionTo(to) {
		return fmt.Errorf("%w: %s -> %s", ErrOutboxTransitionInvalid, from, to)
	}

	return nil
}

func (status OutboxEventStatus) String() string {
	return string(status)
}
