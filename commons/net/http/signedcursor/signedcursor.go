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
// It is a SIBLING package rather than more files in commons/net/http because that
// package is Fiber-oriented (its handlers, middleware and error rendering import
// github.com/gofiber/fiber/v3), while this codec is pure crypto with no transport
// of its own: a worker or a gRPC service should be able to mint a cursor without
// linking Fiber and fasthttp. The Fiber-free siblings pacing and problem set the
// same precedent.
package signedcursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// KeySize is the exact HMAC key length, in raw bytes. It matches the SHA-256
// DIGEST width (32 bytes; the block size is 64 and is not what this tracks): a
// shorter key weakens every signature, and a longer one buys nothing — HMAC-SHA256's
// strength is capped at the 256-bit digest, and past the 64-byte block the key is
// hashed down to 32 bytes anyway. Both are refused rather than stretched or
// truncated.
const KeySize = sha256.Size

// version is the current cursor layout. It lives INSIDE the signed body, so a
// caller cannot present a forged version to select a different parse. It is
// unexported because it is this package's own wire detail: a caller has nothing
// to do with it, and exporting it invites a service to branch on it.
const version = 1

// MaxPayloadLen bounds the caller's ordering tuple at mint time. An ordering
// tuple is a handful of columns and never grows with the result set, so two
// kilobytes is far past any legitimate one; the bound exists so a service cannot
// mint a token its own Decode would then refuse on length.
const MaxPayloadLen = 2048

// MaxTokenLen bounds a token BEFORE any decoding happens. Rejecting on LENGTH
// means an oversized or hostile input never reaches the base64 decoder's
// allocation. It is sized to admit every token mintable from a MaxPayloadLen
// payload — the envelope's base64 of the payload, its two fingerprints and the
// tag — with headroom.
const MaxTokenLen = 4096

// fingerprintBytes is how much of each binding digest travels in the token: 16
// bytes / 128 bits, far past any accidental collision between two of one
// deployment's scopes while keeping the token short.
const fingerprintBytes = 16

// Domain labels. Every MAC this package computes starts with one of these three
// constant labels, which is what makes the three constructions provably disjoint
// under the single signing key: no input to one is a valid input to another, so
// none can be made to mint another's tag.
//
// Each is versioned in its own right. Changing what a construction covers must
// change its label, so tokens minted under the old construction stop verifying
// instead of being reinterpreted under the new one.
const (
	bodyDomain     = "lc-signedcursor-body-v1"
	identityDomain = "lc-signedcursor-identity-v1"
	contextDomain  = "lc-signedcursor-context-v1"
)

// Sentinels.
//
// Every CALLER-FAULT decode failure wraps ErrInvalidCursor, which an HTTP handler
// typically maps to 422, while the specific cause lets it name the fault without
// matching on message text. The two faults below that sit OUTSIDE that parent are
// not the caller's to fix — a misconfigured key is a deployment fault and an
// oversized payload is a programming fault — and mapped to 422 they would tell a
// caller to correct a token that is not the problem.
var (
	// ErrInvalidKey — the configured HMAC key is not exactly KeySize raw bytes.
	// A CONSTRUCTION failure, not a request failure.
	ErrInvalidKey = errors.New("HMAC key must be exactly 32 bytes")

	// ErrEmptyIdentity — the Binding's identity reduced to nothing: no terms, or
	// only empty ones. A SERVER fault, and the reason it sits outside
	// ErrInvalidCursor: the request never resolved who it was reading as, so the
	// caller has no token to fix. Every holder who lost their identity would
	// fingerprint identically and could resume each other's page.
	ErrEmptyIdentity = errors.New("cursor identity has no non-empty term")

	// ErrPayloadTooLarge — the ordering tuple handed to Encode exceeds
	// MaxPayloadLen. A PROGRAMMING failure: the token would be unmintable-then-
	// unreadable, so it is refused at the mint rather than at the next page.
	ErrPayloadTooLarge = errors.New("payload exceeds the maximum cursor payload size")

	// ErrInvalidCursor is the parent of every cursor rejection.
	ErrInvalidCursor = errors.New("invalid cursor")

	// ErrMalformed — the token is not unpadded base64url, is longer than
	// MaxTokenLen, or is too short to carry a signature at all.
	ErrMalformed = fmt.Errorf("%w: not a well-formed cursor token", ErrInvalidCursor)

	// ErrSignature — the token's MAC does not match. It was minted by another
	// deployment, under a rotated key, or edited in flight.
	ErrSignature = fmt.Errorf("%w: signature does not verify", ErrInvalidCursor)

	// ErrVersion — an authentic token from a different codec version. It is
	// refused rather than parsed under today's layout, because a field that moved
	// would be read as a different field.
	ErrVersion = fmt.Errorf("%w: unsupported cursor version", ErrInvalidCursor)

	// ErrIdentityMismatch — the token was minted under a different identity than
	// the one now presenting it.
	ErrIdentityMismatch = fmt.Errorf("%w: the cursor was issued for a different identity", ErrInvalidCursor)

	// ErrContextMismatch — the token was minted over a different context (a
	// different window, filter set, or sort order) than the one now requested.
	ErrContextMismatch = fmt.Errorf("%w: the cursor was issued for a different context", ErrInvalidCursor)
)

// Binding is what a token is tied to. Both term lists are opaque to this package:
// it fingerprints them and compares fingerprints, and never inspects a term.
//
// Identity is who the page is read as — typically tenant id plus any delegated
// scope. Context is what the page is read over — typically the query window, the
// filter set, and the sort order.
//
// TERMS ARE COMPARED BYTE FOR BYTE, so the caller owes a CANONICAL rendering: two
// spellings of one instant ("2026-09-01T00:00:00Z" and "2026-09-01T00:00:00.000Z")
// are two different contexts here. Render timestamps in UTC at a fixed precision,
// and normalize case and ordering, before binding them.
//
// IDENTITY IS MANDATORY: a list with no term, or only empty terms, is refused
// with ErrEmptyIdentity on BOTH Encode and Decode. It is not a wildcard and not
// an anonymous read — it is an identity the request failed to resolve, and
// signing it would give every caller who lost theirs the same fingerprint, so
// each could resume the others' page. One non-empty term is enough; an empty term
// beside a real one is fine and still binds distinctly.
//
// CONTEXT MAY BE EMPTY. A read with no window is legitimate, and an empty context
// fingerprints differently from every non-empty one, so it is a distinct binding
// rather than a wildcard.
type Binding struct {
	// Identity is the ordered list of terms identifying who the page was read as.
	Identity []string

	// Context is the ordered list of terms identifying what the page was read over.
	Context []string
}

// envelope is the signed wire body: compact JSON with short keys. Neither the
// identity terms nor the context terms appear here — only their keyed
// fingerprints — because the token is opaque but NOT secret: its holder reads
// every byte.
type envelope struct {
	Version  int    `json:"v"`
	Payload  []byte `json:"p"`
	Identity string `json:"i"`
	Context  string `json:"c"`
}

// Codec mints and verifies signed cursors. A value is safe to share across
// goroutines and replicas: signing and verification both need only the key, and
// neither mutates it.
type Codec struct {
	key []byte
}

// New builds a codec over a raw HMAC key. It fails closed on any length other
// than KeySize, and copies the key so a caller mutating its own buffer afterwards
// cannot silently change every future signature.
func New(key []byte) (*Codec, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w (got %d bytes)", ErrInvalidKey, len(key))
	}

	owned := make([]byte, KeySize)
	copy(owned, key)

	return &Codec{key: owned}, nil
}

// Encode mints the opaque token for payload: unpadded base64url of the signed
// JSON body followed by its HMAC-SHA256 tag.
//
// payload is the caller's ordering tuple, already serialized however the caller
// prefers — JSON, a fixed binary layout, anything. This package signs bytes and
// does not interpret them.
//
// binding is the identity and context the page was READ under, which the caller
// resolved from its own trusted state. Both are fingerprinted into the body,
// never stored in it, and Decode refuses a token whose fingerprints do not match
// the binding presenting it.
func (c *Codec) Encode(payload []byte, binding Binding) (string, error) {
	if err := c.usable(); err != nil {
		return "", err
	}

	if !hasIdentity(binding.Identity) {
		return "", ErrEmptyIdentity
	}

	if len(payload) > MaxPayloadLen {
		return "", fmt.Errorf("%w (got %d bytes, limit %d)", ErrPayloadTooLarge, len(payload), MaxPayloadLen)
	}

	body, err := json.Marshal(envelope{
		Version:  version,
		Payload:  payload,
		Identity: c.fingerprint(identityDomain, binding.Identity),
		Context:  c.fingerprint(contextDomain, binding.Context),
	})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(append(body, c.mac(bodyDomain, body)...)), nil
}

// Decode verifies token against binding and returns the payload Encode was given.
//
// THE ORDER OF THE CHECKS IS A SECURITY PROPERTY. The length guard runs before any
// decoding, and the MAC is verified BEFORE any byte of the body is parsed, so an
// unauthenticated token never reaches the JSON decoder — including the version
// field, which is why the version lives inside the signed body rather than as a
// readable prefix.
//
// binding is what the CURRENT request resolved from its own trusted state. Nothing
// is read out of the token: the two fingerprints are recomputed from binding and
// compared. Identity is checked before context, so a token replayed across tenants
// is named as such whatever window it carries.
func (c *Codec) Decode(token string, binding Binding) ([]byte, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}

	if !hasIdentity(binding.Identity) {
		return nil, ErrEmptyIdentity
	}

	if len(token) > MaxTokenLen {
		return nil, fmt.Errorf("%w (length %d exceeds the %d-byte limit)", ErrMalformed, len(token), MaxTokenLen)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrMalformed
	}

	if len(raw) <= sha256.Size {
		return nil, ErrMalformed
	}

	body, tag := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if !hmac.Equal(tag, c.mac(bodyDomain, body)) {
		return nil, ErrSignature
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, ErrMalformed
	}

	if env.Version != version {
		return nil, fmt.Errorf("%w (got %d, want %d)", ErrVersion, env.Version, version)
	}

	// Constant-time: each fingerprint is a truncated MAC under the signing key, so
	// a byte-at-a-time comparison would leak how far a forged prefix matched.
	if !hmac.Equal([]byte(env.Identity), []byte(c.fingerprint(identityDomain, binding.Identity))) {
		return nil, ErrIdentityMismatch
	}

	if !hmac.Equal([]byte(env.Context), []byte(c.fingerprint(contextDomain, binding.Context))) {
		return nil, ErrContextMismatch
	}

	return env.Payload, nil
}

// usable reports whether the codec can sign at all. A nil receiver and a
// zero-value Codec both reach here — a struct literal skips New entirely — and
// both fail closed rather than signing under an empty key.
func (c *Codec) usable() error {
	if c == nil || len(c.key) != KeySize {
		return ErrInvalidKey
	}

	return nil
}

// hasIdentity reports whether the terms say WHO, rather than merely being a list.
// A nil list, an empty list and a list of empty strings are the same fact — the
// identity was never resolved — and each would otherwise mint a perfectly valid
// token that every other caller in the same state could also present.
func hasIdentity(terms []string) bool {
	for _, term := range terms {
		if term != "" {
			return true
		}
	}

	return false
}

// fingerprint is the one-way binding between a cursor and one axis of the state
// it was minted under: HMAC-SHA256 over the domain label and the terms, truncated
// to fingerprintBytes and encoded unpadded base64url.
//
// It is KEYED rather than a plain digest because the terms it covers are
// low-entropy strings the token's holder can enumerate: an unkeyed digest is
// reproducible by anyone, so a holder could confirm a guessed tenant id by
// digesting it and comparing. Under the signing key the value is neither
// reproducible nor invertible without the key, which is the only sense in which
// the terms are hidden. It remains a BINDING and never an authorization.
func (c *Codec) fingerprint(domain string, terms []string) string {
	parts := make([][]byte, len(terms))
	for i, term := range terms {
		parts[i] = []byte(term)
	}

	return base64.RawURLEncoding.EncodeToString(c.mac(domain, parts...)[:fingerprintBytes])
}

// mac computes HMAC-SHA256 over a domain label followed by each part, every part
// preceded by its length as a big-endian uint64.
//
// THE LENGTH PREFIX IS DOMAIN SEPARATION between the parts. They are
// variable-length and concatenated, so without it the identities
// (tenant "ab", scope "c") and (tenant "a", scope "bc") would cover the same bytes
// and one would resume the other's page. It replaces a separator byte, which
// separates only as long as no term ever contains that byte — a property nothing
// upstream of this package enforces.
func (c *Codec) mac(domain string, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(domain))

	var length [8]byte

	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		m.Write(length[:])
		m.Write(part)
	}

	return m.Sum(nil)
}
