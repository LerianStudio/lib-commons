package webhook

import "errors"

var (
	// ErrNilDeliverer is returned when a method is called on a nil Deliverer receiver.
	ErrNilDeliverer = errors.New("webhook: nil deliverer")

	// ErrSSRFBlocked is returned when an endpoint URL is rejected by SSRF protection.
	// This includes private/loopback/link-local/CGNAT/RFC-reserved IP ranges,
	// disallowed URL schemes (anything other than http/https), and DNS lookup failures
	// (fail-closed).
	ErrSSRFBlocked = errors.New("webhook: SSRF blocked")

	// ErrDeliveryFailed is returned when delivery to an endpoint exhausts all retry attempts.
	ErrDeliveryFailed = errors.New("webhook: delivery failed")

	// ErrInvalidURL is returned when an endpoint URL fails validation. This covers
	// parse errors, empty hostnames, unresolvable DNS (no valid IPs), and other
	// structural URL problems.
	ErrInvalidURL = errors.New("webhook: invalid URL")
)
