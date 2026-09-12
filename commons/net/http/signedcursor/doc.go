// Package signedcursor mints and verifies opaque, HMAC-SHA256-signed keyset
// pagination cursors.
//
// # What a signed cursor is for
//
// A keyset cursor carries the ordering tuple of the last row a caller received,
// and the next page's WHERE clause consumes it directly. Unsigned, that tuple is
// a query predicate the caller writes: an edited rank, timestamp or id lets the
// caller steer the query's ORDER BY and skip or repeat rows at will. The
// signature makes the tuple the server's own statement, returned verbatim.
//
// A cursor is SIGNED, never encrypted, and that is deliberate: the payload is an
// ordering tuple of a row the caller just received, so it reveals nothing the
// holder does not already hold. Integrity is the requirement; confidentiality is
// not.
//
// # Binding, and the rule that goes with it
//
// A token is bound to two opaque term lists the caller supplies: an IDENTITY
// (who the page was read as) and a CONTEXT (what the page was read over). This
// package never learns what either means — a service binds whatever makes the
// ordering meaningful, typically tenant plus delegated scope as the identity and
// the query window as the context.
//
// Neither travels in the token. Each is reduced to a keyed fingerprint, and the
// rule that makes the whole scheme work is:
//
//	The consumer NEVER reads identity or context FROM the cursor. It re-resolves
//	both from its own trusted context on every page — the validated JWT, the
//	request parameters — and only COMPARES.
//
// Read out of the token instead, a stolen cursor would BE the authorization. Only
// compared, a stolen token can at most reorder rows inside its own holder's
// scope, and a token replayed across scopes is refused.
//
// Refusing rather than re-basing a mismatched cursor is also deliberate: a keyset
// position is only meaningful WITHIN one ordering. Resumed under a different
// identity, the page would silently skip every row of the new identity that sorts
// before the old position, and the caller would see a short list and no error.
//
// # Relationship to the unsigned cursors in commons/net/http
//
// The parent package ships Cursor, SortCursor and TimestampCursor: unsigned,
// standard-base64 JSON bodies carrying a single sort value and binding nothing.
// They remain the right tool for a single-column page over a public list. This
// package is the signed sibling, for a multi-term ordering tuple over a
// tenant-scoped aggregate. Nothing here changes those.
//
// # Adopting from a local implementation
//
// A service replacing its own codec with this one should expect three visible
// differences, none of them accidental.
//
// The checks run IDENTITY BEFORE CONTEXT, so a token replayed across tenants is
// named as a cross-tenant replay whatever window it also carries. That is the
// more useful first answer, and it means a service whose tests assert a specific
// 422 error code for a mixed-up cursor will see the identity code where it used
// to see the context one.
//
// The error VOCABULARY is closed and wraps one parent, so a handler maps the
// family once rather than matching on message text.
//
// An identity that reduces to nothing is refused rather than signed, on both
// Encode and Decode, and ErrEmptyIdentity is deliberately OUTSIDE that parent: it
// is a 500, not a 422. A service that previously minted cursors with an empty
// identity in some code path will find that path failing loudly.
//
// It is a SIBLING package rather than more files in commons/net/http because that
// package is Fiber-oriented (its handlers, middleware and error rendering import
// github.com/gofiber/fiber/v3), while this codec is pure crypto with no transport
// of its own: a worker or a gRPC service should be able to mint a cursor without
// linking Fiber and fasthttp. The Fiber-free siblings pacing and problem set the
// same precedent.
package signedcursor
