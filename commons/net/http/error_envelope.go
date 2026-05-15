package http

// ErrorEnvelope is the richer error wire format for Lerian services with
// taxonomy-coded error contracts. It is a sibling to ErrorResponse — not a
// replacement — and is the recommended envelope for new services and for any
// service whose clients pattern-match on stable application-level error codes.
//
// Wire format (when wrapped by ErrorPayload):
//
//	{
//	  "error": {
//	    "code": "BTF-0429",
//	    "service": "plugin-br-bank-transfer",
//	    "category": "rate_limit",
//	    "message": "Rate limit exceeded",
//	    "requestId": "6d3e2a68-1f2b-4c3d-9e4f-5a6b7c8d9e0f",
//	    "fields": {"limit": 100, "tenantHash": "8b6f3e2a91c4d7e0", "windowSeconds": 60}
//	  }
//	}
//
// The HTTP status code is carried on the response status line, NOT in the
// envelope — this matches taxonomy conventions where Code is a stable
// application-level identifier that does not change when the underlying
// status flips (e.g. an upstream timeout may surface as 503 or 504, but the
// taxonomy code stays "BTF-2000"). Use ErrorResponse for simple services
// that do not maintain a stable error-code taxonomy.
type ErrorEnvelope struct {
	// Code is the stable taxonomy identifier (e.g. "BTF-0429"). Clients
	// pattern-match on this across HTTP-status boundaries.
	Code string `json:"code"`
	// Service identifies which Lerian service produced the error
	// (e.g. "plugin-br-bank-transfer").
	Service string `json:"service"`
	// Category is the taxonomy bucket (e.g. "rate_limit", "validation",
	// "upstream"). Machine-parseable; not human-readable.
	Category string `json:"category"`
	// Message is a human-readable description; may be localized.
	Message string `json:"message"`
	// RequestID is the per-request correlation ID, typically propagated
	// from request middleware (e.g. Fiber's requestid middleware). Omitted
	// from the wire format when empty.
	RequestID string `json:"requestId,omitempty"`
	// Fields carries structured context such as validation errors, retry
	// hints, or rate-limit windows. Omitted from the wire format when nil.
	Fields map[string]any `json:"fields,omitempty"`
}

// ErrorPayload is the top-level wrapper for ErrorEnvelope responses. Wrapping
// the envelope in a top-level "error" key lets consumers add response-level
// metadata next to the error (e.g. correlationId, links) in a future evolution
// without breaking the error contract — a property the flat ErrorResponse
// shape cannot express.
type ErrorPayload struct {
	Error ErrorEnvelope `json:"error"`
}
