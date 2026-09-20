//go:build unit

package signedcursor

import (
	"encoding/base64"
	"encoding/json"
)

// Version re-exports the unexported layout version so a test can mint a token
// one version off the current one. It is not part of the public API.
const Version = version

// DomainLabels re-exports the three constant MAC domain labels so a test can
// assert they are prefix-free, which is what keeps the three constructions
// disjoint. It is not part of the public API.
var DomainLabels = []string{bodyDomain, identityDomain, contextDomain}

// EncodeAtVersion mints an AUTHENTIC token carrying an arbitrary layout version.
// It exists only so the version check can be tested with a token that passes the
// signature check first — forging one from outside the package is impossible by
// design, which is the point of the check. It is not part of the public API.
func EncodeAtVersion(c *Codec, payload []byte, binding Binding, version int) (string, error) {
	body, err := json.Marshal(envelope{
		Version:  version,
		Payload:  payload,
		Identity: c.fingerprint(identityDomain, binding.Identity),
		Context:  c.fingerprint(contextDomain, binding.Context),
	})
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(append(body, c.mac(bodyDomain, body)...)), nil
}

// SignRawBody mints a token over an ARBITRARY body, bypassing the envelope. It
// exists only to reach Decode's post-signature parse failure — a body that
// verifies but is not an envelope, which a forger without the key cannot
// produce. It is not part of the public API.
func SignRawBody(c *Codec, body []byte) string {
	return base64.RawURLEncoding.EncodeToString(append(body, c.mac(bodyDomain, body)...))
}
