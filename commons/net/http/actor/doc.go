// Package actor carries a partner's credential from an inbound request to the
// outbound calls a service makes to other Lerian services while serving it.
//
// # Why
//
// When a partner calls product A and A calls product B to serve that request,
// B authorizes A's own (application) credential. To let B also enforce the
// partner's rules for B, A forwards the partner's bearer token in the
// X-Lerian-Actor header, and B's authorization layer evaluates it alongside the
// caller.
//
// # Flow
//
//  1. The inbound authorization layer stores the partner's raw bearer token with
//     ContextWithToken. Nothing is stored for users or plain applications.
//  2. Outbound clients to Lerian services wrap their transport with
//     NewTransport, naming the hosts that client talks to.
//  3. On each request the transport sets X-Lerian-Actor only when the request
//     context carries a token AND the request host is on that allowlist.
//
// # Fail closed
//
// The header is opt-in per host. An empty allowlist, an unlisted host, a
// different port, or an absent or blank token all leave the request untouched,
// so the token never reaches a third party (a bank, a rail, a customer webhook).
// A redirect is re-evaluated against the allowlist on each hop, because
// http.Client rebuilds the next request from the caller's original headers, not
// from the ones this transport added.
//
// The token is never logged, never placed in an error, and formatting a context
// that carries it prints a redaction marker instead of the value.
package actor
