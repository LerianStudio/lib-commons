//go:build unit

package signedcursor_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/signedcursor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(seed byte) []byte {
	key := make([]byte, signedcursor.KeySize)
	for i := range key {
		key[i] = seed + byte(i)
	}

	return key
}

func newTestCodec(t *testing.T, seed byte) *signedcursor.Codec {
	t.Helper()

	codec, err := signedcursor.New(testKey(seed))
	require.NoError(t, err)

	return codec
}

func tenantBinding() signedcursor.Binding {
	return signedcursor.Binding{
		Identity: []string{"tenant-a", "scope-reader"},
		Context:  []string{"2026-09-01T00:00:00Z", "2026-09-11T00:00:00Z"},
	}
}

func TestNewKeySize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     []byte
		wantErr bool
	}{
		{name: "exact 32 bytes", key: make([]byte, signedcursor.KeySize), wantErr: false},
		{name: "nil key", key: nil, wantErr: true},
		{name: "empty key", key: []byte{}, wantErr: true},
		{name: "31 bytes", key: make([]byte, signedcursor.KeySize-1), wantErr: true},
		{name: "33 bytes", key: make([]byte, signedcursor.KeySize+1), wantErr: true},
		{name: "64 bytes", key: make([]byte, 64), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			codec, err := signedcursor.New(tt.key)
			if tt.wantErr {
				require.ErrorIs(t, err, signedcursor.ErrInvalidKey)
				assert.Nil(t, codec)

				return
			}

			require.NoError(t, err)
			assert.NotNil(t, codec)
		})
	}
}

func TestNewCopiesKey(t *testing.T) {
	t.Parallel()

	key := testKey(1)

	codec, err := signedcursor.New(key)
	require.NoError(t, err)

	token, err := codec.Encode([]byte("payload"), tenantBinding())
	require.NoError(t, err)

	// Mutating the caller's buffer must not change verification.
	for i := range key {
		key[i] = 0
	}

	payload, err := codec.Decode(token, tenantBinding())
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), payload)
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		binding signedcursor.Binding
	}{
		{name: "typical ordering tuple", payload: []byte(`{"rank":3,"id":"sub_01"}`), binding: tenantBinding()},
		{name: "empty payload", payload: []byte{}, binding: tenantBinding()},
		{name: "nil payload", payload: nil, binding: tenantBinding()},
		{name: "binary payload", payload: []byte{0x00, 0xFF, 0x7B, 0x0A}, binding: tenantBinding()},
		{
			name:    "no identity terms",
			payload: []byte("p"),
			binding: signedcursor.Binding{Context: []string{"w1"}},
		},
		{
			name:    "no context terms",
			payload: []byte("p"),
			binding: signedcursor.Binding{Identity: []string{"tenant-a"}},
		},
		{name: "no binding at all", payload: []byte("p"), binding: signedcursor.Binding{}},
		{
			name:    "terms containing the separator characters",
			payload: []byte("p"),
			binding: signedcursor.Binding{Identity: []string{"a\x00b", "c:d"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			codec := newTestCodec(t, 7)

			token, err := codec.Encode(tt.payload, tt.binding)
			require.NoError(t, err)
			assert.NotEmpty(t, token)

			got, err := codec.Decode(token, tt.binding)
			require.NoError(t, err)

			if len(tt.payload) == 0 {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.payload, got)
			}
		})
	}
}

func TestTokenIsURLSafeAndUnpadded(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 3)

	token, err := codec.Encode([]byte(`{"rank":1}`), tenantBinding())
	require.NoError(t, err)

	assert.NotContains(t, token, "+")
	assert.NotContains(t, token, "/")
	assert.NotContains(t, token, "=")
}

func TestEncodeIsDeterministic(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 5)

	first, err := codec.Encode([]byte("p"), tenantBinding())
	require.NoError(t, err)

	second, err := codec.Encode([]byte("p"), tenantBinding())
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestEncodeRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 9)

	_, err := codec.Encode(make([]byte, signedcursor.MaxPayloadLen+1), tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrPayloadTooLarge)

	// The boundary itself is accepted, and the token it mints decodes.
	token, err := codec.Encode(make([]byte, signedcursor.MaxPayloadLen), tenantBinding())
	require.NoError(t, err)

	_, err = codec.Decode(token, tenantBinding())
	require.NoError(t, err)
}

func TestDecodeRejections(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 11)

	valid, err := codec.Encode([]byte(`{"rank":3}`), tenantBinding())
	require.NoError(t, err)

	tamperedBody := func(t *testing.T) string {
		t.Helper()

		raw, decErr := base64.RawURLEncoding.DecodeString(valid)
		require.NoError(t, decErr)

		// Flip one bit inside the signed JSON body, leaving the tag intact.
		raw[2] ^= 0x01

		return base64.RawURLEncoding.EncodeToString(raw)
	}

	tamperedTag := func(t *testing.T) string {
		t.Helper()

		raw, decErr := base64.RawURLEncoding.DecodeString(valid)
		require.NoError(t, decErr)

		raw[len(raw)-1] ^= 0x01

		return base64.RawURLEncoding.EncodeToString(raw)
	}

	tests := []struct {
		name    string
		token   func(t *testing.T) string
		binding signedcursor.Binding
		wantErr error
	}{
		{
			name:    "empty token",
			token:   func(*testing.T) string { return "" },
			binding: tenantBinding(),
			wantErr: signedcursor.ErrMalformed,
		},
		{
			name:    "not base64url",
			token:   func(*testing.T) string { return "!!!not base64!!!" },
			binding: tenantBinding(),
			wantErr: signedcursor.ErrMalformed,
		},
		{
			name:    "standard base64 alphabet is refused",
			token:   func(*testing.T) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xFB}, 64)) },
			binding: tenantBinding(),
			wantErr: signedcursor.ErrMalformed,
		},
		{
			name:    "shorter than a signature tag",
			token:   func(*testing.T) string { return base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)) },
			binding: tenantBinding(),
			wantErr: signedcursor.ErrMalformed,
		},
		{
			name:    "truncated valid token",
			token:   func(*testing.T) string { return valid[:len(valid)/2] },
			binding: tenantBinding(),
			wantErr: signedcursor.ErrInvalidCursor,
		},
		{
			name: "oversized token is refused on length before decoding",
			token: func(*testing.T) string {
				// Deliberately VALID base64url of a length that is a multiple of
				// four: only the pre-decode length guard can reject this, so the
				// case goes red if that guard is removed instead of silently
				// falling through to a signature failure.
				return strings.Repeat("A", signedcursor.MaxTokenLen+4)
			},
			binding: tenantBinding(),
			wantErr: signedcursor.ErrMalformed,
		},
		{
			name:    "tampered signed body",
			token:   tamperedBody,
			binding: tenantBinding(),
			wantErr: signedcursor.ErrSignature,
		},
		{
			name:    "tampered signature tag",
			token:   tamperedTag,
			binding: tenantBinding(),
			wantErr: signedcursor.ErrSignature,
		},
		{
			name:  "wrong identity",
			token: func(*testing.T) string { return valid },
			binding: signedcursor.Binding{
				Identity: []string{"tenant-b", "scope-reader"},
				Context:  tenantBinding().Context,
			},
			wantErr: signedcursor.ErrIdentityMismatch,
		},
		{
			name:  "identity terms re-split at a different boundary",
			token: func(*testing.T) string { return valid },
			binding: signedcursor.Binding{
				Identity: []string{"tenant-ascope", "-reader"},
				Context:  tenantBinding().Context,
			},
			wantErr: signedcursor.ErrIdentityMismatch,
		},
		{
			name:  "identity terms dropped",
			token: func(*testing.T) string { return valid },
			binding: signedcursor.Binding{
				Identity: []string{"tenant-a"},
				Context:  tenantBinding().Context,
			},
			wantErr: signedcursor.ErrIdentityMismatch,
		},
		{
			name:  "wrong context",
			token: func(*testing.T) string { return valid },
			binding: signedcursor.Binding{
				Identity: tenantBinding().Identity,
				Context:  []string{"2026-09-02T00:00:00Z", "2026-09-11T00:00:00Z"},
			},
			wantErr: signedcursor.ErrContextMismatch,
		},
		{
			name:  "context terms re-split at a different boundary",
			token: func(*testing.T) string { return valid },
			binding: signedcursor.Binding{
				Identity: tenantBinding().Identity,
				Context:  []string{"2026-09-01T00:00:00Z2026-09-11T00:00:00Z"},
			},
			wantErr: signedcursor.ErrContextMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := codec.Decode(tt.token(t), tt.binding)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorIs(t, err, signedcursor.ErrInvalidCursor,
				"every decode rejection must wrap the single parent sentinel")
		})
	}
}

func TestDecodeWrongKey(t *testing.T) {
	t.Parallel()

	minter := newTestCodec(t, 1)
	verifier := newTestCodec(t, 2)

	token, err := minter.Encode([]byte("p"), tenantBinding())
	require.NoError(t, err)

	_, err = verifier.Decode(token, tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrSignature)
}

func TestIdentityIsNotReadableFromTheToken(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 13)

	token, err := codec.Encode([]byte("p"), tenantBinding())
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	// The binding travels as a keyed fingerprint, never as its terms.
	assert.NotContains(t, string(raw), "tenant-a")
	assert.NotContains(t, string(raw), "scope-reader")
	assert.NotContains(t, string(raw), "2026-09-01T00:00:00Z")
}

// envelopeFields reads the signed JSON body out of a token so a test can assert
// on the fingerprints themselves rather than only on the token as a whole. A
// token is opaque but not secret, so this is exactly what its holder can do.
func envelopeFields(t *testing.T, token string) map[string]any {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	require.Greater(t, len(raw), sha256.Size)

	fields := map[string]any{}
	require.NoError(t, json.Unmarshal(raw[:len(raw)-sha256.Size], &fields))

	return fields
}

func TestFingerprintIsKeyed(t *testing.T) {
	t.Parallel()

	first := newTestCodec(t, 1)
	second := newTestCodec(t, 2)

	firstToken, err := first.Encode([]byte("p"), tenantBinding())
	require.NoError(t, err)

	secondToken, err := second.Encode([]byte("p"), tenantBinding())
	require.NoError(t, err)

	// Same binding, different signing key: the fingerprints themselves must
	// differ. An UNKEYED digest would be reproducible by anyone, letting a token
	// holder confirm a guessed tenant id by digesting it and comparing.
	firstFields := envelopeFields(t, firstToken)
	secondFields := envelopeFields(t, secondToken)

	assert.NotEqual(t, firstFields["i"], secondFields["i"], "identity fingerprint must be keyed")
	assert.NotEqual(t, firstFields["c"], secondFields["c"], "context fingerprint must be keyed")
}

func TestIdentityAndContextAreDistinctDomains(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 17)

	// Identical terms on both axes. The two fingerprints must still differ, or
	// the two constructions share a domain and an identity term could be
	// presented as a context term.
	token, err := codec.Encode([]byte("p"), signedcursor.Binding{
		Identity: []string{"x", "y"},
		Context:  []string{"x", "y"},
	})
	require.NoError(t, err)

	fields := envelopeFields(t, token)
	assert.NotEqual(t, fields["i"], fields["c"],
		"identity and context must be fingerprinted under distinct domain labels")
}

func TestFingerprintSeparatesTermsUnambiguously(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 23)

	// Two term lists whose CONCATENATION is byte-identical. Without a length
	// prefix on each term they would fingerprint the same, and one caller would
	// resume the other's page.
	token, err := codec.Encode([]byte("p"), signedcursor.Binding{Identity: []string{"ab", "c"}})
	require.NoError(t, err)

	_, err = codec.Decode(token, signedcursor.Binding{Identity: []string{"a", "bc"}})
	require.ErrorIs(t, err, signedcursor.ErrIdentityMismatch)

	// Same trap on the context axis.
	token, err = codec.Encode([]byte("p"), signedcursor.Binding{Context: []string{"ab", "c"}})
	require.NoError(t, err)

	_, err = codec.Decode(token, signedcursor.Binding{Context: []string{"a", "bc"}})
	require.ErrorIs(t, err, signedcursor.ErrContextMismatch)

	// And an empty trailing term is a different binding from no term at all.
	token, err = codec.Encode([]byte("p"), signedcursor.Binding{Identity: []string{"a", ""}})
	require.NoError(t, err)

	_, err = codec.Decode(token, signedcursor.Binding{Identity: []string{"a"}})
	require.ErrorIs(t, err, signedcursor.ErrIdentityMismatch)
}

func TestDecodeUnsupportedVersion(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 19)

	token, err := signedcursor.EncodeAtVersion(codec, []byte("p"), tenantBinding(), signedcursor.Version+1)
	require.NoError(t, err)

	_, err = codec.Decode(token, tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrVersion)
}

func TestZeroValueCodecIsRefused(t *testing.T) {
	t.Parallel()

	var codec signedcursor.Codec

	_, err := codec.Encode([]byte("p"), tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrInvalidKey)

	_, err = codec.Decode("whatever", tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrInvalidKey)
}

func TestNilCodecIsRefused(t *testing.T) {
	t.Parallel()

	var codec *signedcursor.Codec

	_, err := codec.Encode([]byte("p"), tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrInvalidKey)

	_, err = codec.Decode("whatever", tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrInvalidKey)
}

func TestServerFaultsDoNotWrapTheCallerParent(t *testing.T) {
	t.Parallel()

	// A misconfigured key and an oversized payload are deployment/programming
	// faults. Wrapping them in ErrInvalidCursor would tell a caller to fix a
	// token that is not the problem.
	assert.False(t, errors.Is(signedcursor.ErrInvalidKey, signedcursor.ErrInvalidCursor))
	assert.False(t, errors.Is(signedcursor.ErrPayloadTooLarge, signedcursor.ErrInvalidCursor))
}

func TestDecodeAuthenticBodyThatIsNotAnEnvelope(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t, 29)

	// A body that passes the signature check but is not the envelope: the parse
	// failure must still be a malformed cursor, never a panic or a zero payload
	// returned as success.
	token := signedcursor.SignRawBody(codec, []byte("not json at all"))

	_, err := codec.Decode(token, tenantBinding())
	require.ErrorIs(t, err, signedcursor.ErrMalformed)
}
