package http

import (
	"github.com/gofiber/fiber/v2"
)

// RespondErrorEnvelope writes a richer error envelope at the supplied HTTP
// status. It is a sibling to RespondError — RespondError ships a flat
// {code, title, message} envelope for simple services; RespondErrorEnvelope
// ships a taxonomy-coded envelope for services with stable error codes
// (e.g. "BTF-0429"). Choose based on whether your clients pattern-match
// on application-level codes that cross HTTP status boundaries.
//
// Behavior:
//   - Returns ErrContextNotFound when c is nil.
//   - Normalizes invalid HTTP status codes using Respond's existing semantics.
//   - Writes ErrorPayload{Error: env} as the response body.
//   - Returns serialization/write errors from Respond.
//
// Consumers that need to customize the responder (e.g. emit a Prometheus
// counter on every error response, or attach an OTel span event) wrap this
// helper at their call site — keep RespondErrorEnvelope narrowly scoped to
// the JSON write.
func RespondErrorEnvelope(c *fiber.Ctx, status int, env ErrorEnvelope) error {
	if c == nil {
		return ErrContextNotFound
	}

	return Respond(c, status, ErrorPayload{Error: env})
}
