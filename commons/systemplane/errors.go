package systemplane

import "errors"

// Sentinel errors returned by Client methods.
var (
	// ErrClosed is returned when a method is called on a nil or closed Client.
	ErrClosed = errors.New("systemplane: client is closed or nil")

	// ErrNotStarted is returned when a read/write is attempted before Start.
	ErrNotStarted = errors.New("systemplane: client not started")

	// ErrAlreadyStarted is returned when Start is called more than once.
	ErrAlreadyStarted = errors.New("systemplane: client already started")

	// ErrRegisterAfterStart is returned when Register is called after Start.
	ErrRegisterAfterStart = errors.New("systemplane: register called after start")

	// ErrUnknownKey is returned when Get or Set references an unregistered key.
	ErrUnknownKey = errors.New("systemplane: unknown key")

	// ErrValidation is returned when a value fails its registered validator.
	ErrValidation = errors.New("systemplane: validation failed")
)
