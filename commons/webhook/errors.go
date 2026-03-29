package webhook

import "errors"

var (
	// ErrNilDeliverer is returned when a method is called on a nil Deliverer receiver.
	ErrNilDeliverer = errors.New("webhook: nil deliverer")

	// ErrSSRFBlocked is returned when an endpoint URL resolves to a private, loopback,
	// or link-local IP address.
	ErrSSRFBlocked = errors.New("webhook: SSRF blocked")

	// ErrDeliveryFailed is returned when delivery to an endpoint exhausts all retry attempts.
	ErrDeliveryFailed = errors.New("webhook: delivery failed")

	// ErrInvalidURL is returned when an endpoint URL cannot be parsed.
	ErrInvalidURL = errors.New("webhook: invalid URL")
)
