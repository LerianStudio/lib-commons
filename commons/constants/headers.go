package constant

const (
	// HeaderUserAgent is the HTTP User-Agent header key.
	HeaderUserAgent = "User-Agent"
	// HeaderRealIP is the de-facto upstream real client IP header key.
	HeaderRealIP = "X-Real-Ip"
	// HeaderForwardedFor is the X-Forwarded-For header key.
	HeaderForwardedFor = "X-Forwarded-For"
	// HeaderForwardedHost is the X-Forwarded-Host header key.
	HeaderForwardedHost = "X-Forwarded-Host"
	// HeaderHost is the Host header key.
	HeaderHost = "Host"
	// DSL is the file kind marker used for DSL resources.
	DSL = "dsl"
	// FileExtension is the default extension for DSL files.
	FileExtension = ".gold"
	// HeaderID is the request identifier header key.
	HeaderID = "X-Request-Id"
	// HeaderTraceparent is the W3C traceparent header key.
	HeaderTraceparent = "Traceparent"
	// IdempotencyKey is the idempotency key request header.
	IdempotencyKey = "X-Idempotency"
	// IdempotencyTTL is the idempotency record TTL header.
	IdempotencyTTL = "X-TTL"
	// IdempotencyReplayed signals whether a request was replayed.
	IdempotencyReplayed = "X-Idempotency-Replayed"
	// Authorization is the HTTP Authorization header key.
	Authorization = "Authorization"
	// Basic is the HTTP Basic auth scheme token.
	Basic = "Basic"
	// BasicAuth is the human-readable Basic auth label.
	BasicAuth = "Basic Auth"
	// WWWAuthenticate is the HTTP WWW-Authenticate header key.
	WWWAuthenticate = "WWW-Authenticate"
	// Bearer is the HTTP Bearer auth scheme token.
	Bearer = "Bearer"

	// HeaderReferer is the HTTP Referer header key.
	HeaderReferer = "Referer"
	// HeaderContentType is the HTTP Content-Type header key.
	HeaderContentType = "Content-Type"
	// HeaderTraceparentPascal is the PascalCase variant of the Traceparent header for gRPC metadata.
	HeaderTraceparentPascal = "Traceparent"
	// HeaderTracestatePascal is the PascalCase variant of the Tracestate header for gRPC metadata.
	HeaderTracestatePascal = "Tracestate"

	// RateLimitLimit is the header containing the configured request quota.
	RateLimitLimit = "X-RateLimit-Limit"
	// RateLimitRemaining is the header containing remaining requests in the current window.
	RateLimitRemaining = "X-RateLimit-Remaining"
	// RateLimitReset is the header containing the reset time for the current window.
	RateLimitReset = "X-RateLimit-Reset"
)
