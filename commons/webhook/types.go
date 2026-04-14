package webhook

import "context"

// Endpoint represents a webhook receiver URL with an optional signing secret.
type Endpoint struct {
	// ID uniquely identifies this endpoint (used in logs and metrics).
	ID string

	// URL is the HTTP(S) endpoint that receives webhook events.
	URL string

	// Secret is the HMAC-SHA256 signing secret. May be plaintext or encrypted
	// with an "enc:" prefix when a SecretDecryptor is configured.
	Secret string

	// Active indicates whether this endpoint should receive deliveries.
	Active bool
}

// Event represents a webhook event to be delivered.
type Event struct {
	// Type identifies the event kind (e.g., "order.created", "payment.completed").
	Type string

	// Payload is the JSON-encoded event body.
	Payload []byte

	// Timestamp is the Unix epoch seconds when the event was produced.
	Timestamp int64
}

// EndpointLister retrieves active webhook endpoints for the current context.
// Implementations typically query a database filtered by tenant ID extracted
// from the context.
type EndpointLister interface {
	ListActiveEndpoints(ctx context.Context) ([]Endpoint, error)
}

// DeliveryResult captures the outcome of a single endpoint delivery attempt.
type DeliveryResult struct {
	// EndpointID is the ID of the endpoint that was targeted.
	EndpointID string

	// StatusCode is the HTTP response status, or 0 if the request failed before
	// receiving a response.
	StatusCode int

	// Success is true when the endpoint returned a 2xx status code.
	Success bool

	// Error is non-nil when delivery failed after all retries.
	Error error

	// Attempts is the total number of HTTP requests made (initial + retries).
	Attempts int
}

// DeliveryMetrics allows callers to record webhook delivery outcomes for
// monitoring and alerting. Implementations typically emit OpenTelemetry
// metrics or Prometheus counters.
type DeliveryMetrics interface {
	RecordDelivery(ctx context.Context, endpointID string, success bool, statusCode int, attempts int)
}

// SecretDecryptor decrypts an encrypted endpoint secret. The input carries the
// "enc:" prefix stripped — only the ciphertext is passed. Returning an error
// aborts delivery to that endpoint (fail-closed).
type SecretDecryptor func(encrypted string) (string, error)
