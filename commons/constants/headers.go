// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package constant

const (
	HeaderUserAgent     = "User-Agent"
	HeaderRealIP        = "X-Real-Ip"
	HeaderForwardedFor  = "X-Forwarded-For"
	HeaderForwardedHost = "X-Forwarded-Host"
	HeaderHost          = "Host"
	DSL                 = "dsl"
	FileExtension       = ".gold"
	HeaderID            = "X-Request-Id"
	HeaderTraceparent   = "Traceparent"
	IdempotencyKey      = "X-Idempotency"
	IdempotencyTTL      = "X-TTL"
	IdempotencyReplayed = "X-Idempotency-Replayed"
	Authorization       = "Authorization"
	Basic               = "Basic"
	BasicAuth           = "Basic Auth"
	WWWAuthenticate     = "WWW-Authenticate"

	// Rate Limit Headers
	RateLimitLimit     = "X-RateLimit-Limit"
	RateLimitRemaining = "X-RateLimit-Remaining"
	RateLimitReset     = "X-RateLimit-Reset"
)
