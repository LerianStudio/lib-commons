package dlq

import "errors"

var (
	// ErrNilHandler is returned when a Handler method is called on a nil receiver.
	ErrNilHandler = errors.New("dlq handler is nil")
	// ErrNilRetryFunc is returned when a Consumer is created without a retry function.
	ErrNilRetryFunc = errors.New("dlq retry function is nil")
	// ErrMessageExhausted indicates a message exceeded its maximum retry count.
	ErrMessageExhausted = errors.New("dlq message exhausted all retries")
)
