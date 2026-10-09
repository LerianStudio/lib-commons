package actor

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// HeaderName is the header that carries the partner's bearer token to a
// downstream Lerian service.
const HeaderName = "X-Lerian-Actor"

// contextKey is unexported so no other package can read or overwrite the token
// under the same key.
type contextKey struct{}

// secret holds the token in the context. A context printed in a log line or an
// error must never reveal it, and a plain string value would: context.WithValue
// stringifies string values, and %#v reflects into the stored value. A pointer
// that formats as a redaction marker defeats both, because fmt prints only the
// address of a pointer nested inside another value.
type secret struct{ token string }

const redacted = "[REDACTED]"

func (*secret) String() string { return redacted }

func (*secret) GoString() string { return redacted }

// ContextWithToken returns a copy of ctx carrying the actor token. A blank token
// is stored as absent, so it also clears a token set by an outer context. A nil
// ctx is treated as context.Background().
func ContextWithToken(ctx context.Context, token string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, contextKey{}, &secret{token: strings.TrimSpace(token)})
}

// TokenFromContext returns the actor token carried by ctx, and false when there
// is none or it is blank.
func TokenFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}

	value, ok := ctx.Value(contextKey{}).(*secret)
	if !ok || value == nil || value.token == "" {
		return "", false
	}

	return value.token, true
}

// transport sets HeaderName on requests to allowlisted hosts.
type transport struct {
	base    http.RoundTripper
	allowed map[string]struct{}
}

// NewTransport wraps base so that each request whose context carries an actor
// token AND whose URL host (host[:port], compared case-insensitively) is one of
// allowedHosts is sent with HeaderName set to that token. Every other request is
// passed to base unchanged. The request is cloned before the header is set, so
// the caller's request is never mutated. A nil base falls back to
// http.DefaultTransport. Blank entries in allowedHosts match nothing.
func NewTransport(base http.RoundTripper, allowedHosts ...string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	allowed := make(map[string]struct{}, len(allowedHosts))

	for _, host := range allowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			allowed[host] = struct{}{}
		}
	}

	return &transport{base: base, allowed: allowed}
}

// RoundTrip implements http.RoundTripper.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("actor: request is nil")
	}

	if !t.allows(req) {
		return t.base.RoundTrip(req)
	}

	token, ok := TokenFromContext(req.Context())
	if !ok {
		return t.base.RoundTrip(req)
	}

	out := req.Clone(req.Context())
	out.Header.Set(HeaderName, token)

	return t.base.RoundTrip(out)
}

func (t *transport) allows(req *http.Request) bool {
	if req.URL == nil {
		return false
	}

	_, ok := t.allowed[strings.ToLower(req.URL.Host)]

	return ok
}
