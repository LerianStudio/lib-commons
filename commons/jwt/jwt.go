package jwt

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"slices"
	"strings"
	"time"
)

const (
	// AlgHS256 identifies the HMAC-SHA256 signing algorithm.
	AlgHS256 = "HS256"
	// AlgHS384 identifies the HMAC-SHA384 signing algorithm.
	AlgHS384 = "HS384"
	// AlgHS512 identifies the HMAC-SHA512 signing algorithm.
	AlgHS512 = "HS512"

	// jwtPartCount is the number of dot-separated parts in a valid JWT (header.payload.signature).
	jwtPartCount = 3
)

// MapClaims is a convenience alias for an unstructured JWT payload.
type MapClaims = map[string]any

// Token represents a parsed JWT with its header, claims, and validation state.
// SignatureValid is true only when the token's cryptographic signature has been
// verified successfully. It does NOT indicate that time-based claims (exp, nbf,
// iat) have been validated. Use ParseAndValidate for full validation, or call
// Token.ValidateTimeClaims after Parse.
type Token struct {
	Claims         MapClaims
	SignatureValid bool
	Header         map[string]any
}

var (
	// ErrInvalidToken indicates the token string is malformed or cannot be decoded.
	ErrInvalidToken = errors.New("invalid token")
	// ErrUnsupportedAlgorithm indicates the signing algorithm is not supported or not allowed.
	ErrUnsupportedAlgorithm = errors.New("unsupported signing algorithm")
	// ErrSignatureInvalid indicates the token signature does not match the expected value.
	ErrSignatureInvalid = errors.New("signature verification failed")
	// ErrTokenExpired indicates the token's exp claim is in the past.
	ErrTokenExpired = errors.New("token has expired")
	// ErrTokenNotYetValid indicates the token's nbf claim is in the future.
	ErrTokenNotYetValid = errors.New("token is not yet valid")
	// ErrTokenIssuedInFuture indicates the token's iat claim is in the future.
	ErrTokenIssuedInFuture = errors.New("token issued in the future")
	// ErrEmptySecret indicates an empty secret was provided for signing or verification.
	ErrEmptySecret = errors.New("secret must not be empty")
	// ErrNilToken indicates a method was called on a nil *Token.
	ErrNilToken = errors.New("token is nil")
	// ErrInvalidTimeClaim indicates a time claim is present but has an unsupported or unparseable type.
	ErrInvalidTimeClaim = errors.New("invalid time claim type")
)

// Parse validates and decodes a JWT token string. It expects three dot-separated
// base64url-encoded parts (header, payload, signature), verifies the algorithm
// is in the allowedAlgorithms whitelist, and checks the HMAC signature using
// the provided secret with constant-time comparison. Returns a populated Token
// on success, or ErrInvalidToken, ErrUnsupportedAlgorithm, or ErrSignatureInvalid
// on failure.
//
// Note: Token.SignatureValid indicates only that the cryptographic signature
// has been verified successfully. It does NOT validate time-based claims such
// as exp (expiration) or nbf (not-before). Use ParseAndValidate for a single-
// step parse-and-validate flow, or call Token.ValidateTimeClaims after Parse.
func Parse(tokenString string, secret []byte, allowedAlgorithms []string) (*Token, error) {
	const maxTokenLength = 8192 // 8KB is generous for any legitimate JWT

	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}

	if len(tokenString) > maxTokenLength {
		return nil, fmt.Errorf("token exceeds maximum length of %d bytes: %w", maxTokenLength, ErrInvalidToken)
	}

	if tokenString == "" {
		return nil, fmt.Errorf("empty token string: %w", ErrInvalidToken)
	}

	parts := strings.Split(tokenString, ".")
	if len(parts) != jwtPartCount {
		return nil, fmt.Errorf("token must have %d parts: %w", jwtPartCount, ErrInvalidToken)
	}

	header, alg, err := parseHeader(parts[0], allowedAlgorithms)
	if err != nil {
		return nil, err
	}

	if err := verifySignature(parts, alg, secret); err != nil {
		return nil, err
	}

	claims, err := parseClaims(parts[1])
	if err != nil {
		return nil, err
	}

	return &Token{
		Claims:         claims,
		SignatureValid: true,
		Header:         header,
	}, nil
}

// ParseAndValidate parses the JWT, verifies the cryptographic signature,
// and validates time-based claims (exp, nbf, iat). This is the recommended
// single-step validation for most use cases. It returns the token only if
// both the signature and time claims are valid.
func ParseAndValidate(tokenString string, secret []byte, allowedAlgorithms []string) (*Token, error) {
	token, err := Parse(tokenString, secret, allowedAlgorithms)
	if err != nil {
		return nil, err
	}

	if err := token.ValidateTimeClaims(); err != nil {
		return nil, err
	}

	return token, nil
}

// parseHeader decodes and validates the JWT header part. It base64url-decodes
// the header, unmarshals it, extracts the signing algorithm, and verifies it
// is in the allowed list.
func parseHeader(headerPart string, allowedAlgorithms []string) (map[string]any, string, error) {
	headerBytes, err := base64.RawURLEncoding.DecodeString(headerPart)
	if err != nil {
		return nil, "", fmt.Errorf("decode header: %w", ErrInvalidToken)
	}

	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, "", fmt.Errorf("unmarshal header: %w", ErrInvalidToken)
	}

	alg, ok := header["alg"].(string)
	if !ok || alg == "" {
		return nil, "", fmt.Errorf("missing alg in header: %w", ErrInvalidToken)
	}

	if !isAllowed(alg, allowedAlgorithms) {
		return nil, "", fmt.Errorf("algorithm %q not allowed: %w", alg, ErrUnsupportedAlgorithm)
	}

	return header, alg, nil
}

// verifySignature checks the HMAC signature of the JWT. It computes the expected
// signature from the signing input (header.payload) and compares it against the
// actual signature using constant-time comparison.
func verifySignature(parts []string, alg string, secret []byte) error {
	hashFunc, err := hashForAlgorithm(alg)
	if err != nil {
		return err
	}

	signingInput := parts[0] + "." + parts[1]

	expectedSig, err := computeHMAC([]byte(signingInput), secret, hashFunc)
	if err != nil {
		return fmt.Errorf("compute signature: %w", ErrInvalidToken)
	}

	actualSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decode signature: %w", ErrInvalidToken)
	}

	if !hmac.Equal(expectedSig, actualSig) {
		return ErrSignatureInvalid
	}

	return nil
}

// parseClaims decodes and unmarshals the JWT payload part into a MapClaims map.
// Uses json.Decoder with UseNumber() to preserve numeric fidelity for time
// claims (iat, exp, nbf) instead of converting them to float64.
func parseClaims(payloadPart string) (MapClaims, error) {
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", ErrInvalidToken)
	}

	var claims MapClaims

	dec := json.NewDecoder(bytes.NewReader(payloadBytes))
	dec.UseNumber()

	if err := dec.Decode(&claims); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", ErrInvalidToken)
	}

	return claims, nil
}

// Sign produces a compact JWT serialization from the given claims. It encodes
// the header and payload as base64url, computes an HMAC signature using the
// specified algorithm and secret, and returns the three-part dot-separated
// token string. Supported algorithms: HS256, HS384, HS512.
func Sign(claims MapClaims, algorithm string, secret []byte) (string, error) {
	if len(secret) == 0 {
		return "", ErrEmptySecret
	}

	hashFunc, err := hashForAlgorithm(algorithm)
	if err != nil {
		return "", err
	}

	header := map[string]string{"alg": algorithm, "typ": "JWT"}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	headerEncoded := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadEncoded := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerEncoded + "." + payloadEncoded

	sig, err := computeHMAC([]byte(signingInput), secret, hashFunc)
	if err != nil {
		return "", fmt.Errorf("compute signature: %w", err)
	}

	sigEncoded := base64.RawURLEncoding.EncodeToString(sig)

	return signingInput + "." + sigEncoded, nil
}

func isAllowed(alg string, allowed []string) bool {
	return slices.Contains(allowed, alg)
}

func hashForAlgorithm(alg string) (func() hash.Hash, error) {
	switch alg {
	case AlgHS256:
		return sha256.New, nil
	case AlgHS384:
		return sha512.New384, nil
	case AlgHS512:
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("algorithm %q: %w", alg, ErrUnsupportedAlgorithm)
	}
}

func computeHMAC(data, secret []byte, hashFunc func() hash.Hash) ([]byte, error) {
	mac := hmac.New(hashFunc, secret)

	if _, err := mac.Write(data); err != nil {
		return nil, fmt.Errorf("hmac write: %w", err)
	}

	return mac.Sum(nil), nil
}

// ValidateTimeClaims checks the standard JWT time-based claims (exp, nbf, iat)
// on this token against the current UTC time.
// Returns ErrNilToken if called on a nil *Token.
func (t *Token) ValidateTimeClaims() error {
	if t == nil {
		return ErrNilToken
	}

	return ValidateTimeClaimsAt(t.Claims, time.Now().UTC())
}

// ValidateTimeClaimsAt checks the standard JWT time-based claims on this token
// against the provided time.
// Returns ErrNilToken if called on a nil *Token.
func (t *Token) ValidateTimeClaimsAt(now time.Time) error {
	if t == nil {
		return ErrNilToken
	}

	return ValidateTimeClaimsAt(t.Claims, now)
}

// ValidateTimeClaimsAt checks the standard JWT time-based claims against the provided time.
// Each claim is optional: if absent from the map, the corresponding check is skipped.
// Returns ErrTokenExpired if the token has expired (at or past the expiry time, per
// RFC 7519 §4.1.4), ErrTokenNotYetValid if the token cannot be used yet, or
// ErrTokenIssuedInFuture if the issued-at time is in the future.
// Returns ErrInvalidTimeClaim if a time claim is present but has an unsupported type.
func ValidateTimeClaimsAt(claims MapClaims, now time.Time) error {
	exp, expOK, err := extractTime(claims, "exp")
	if err != nil {
		return err
	}

	if expOK {
		// RFC 7519 §4.1.4: the token MUST NOT be accepted on or after the expiration time.
		// !now.Before(exp) is equivalent to now >= exp.
		if !now.Before(exp) {
			return fmt.Errorf("token expired at %s: %w", exp.Format(time.RFC3339), ErrTokenExpired)
		}
	}

	nbf, nbfOK, err := extractTime(claims, "nbf")
	if err != nil {
		return err
	}

	if nbfOK {
		if now.Before(nbf) {
			return fmt.Errorf("token not valid until %s: %w", nbf.Format(time.RFC3339), ErrTokenNotYetValid)
		}
	}

	iat, iatOK, err := extractTime(claims, "iat")
	if err != nil {
		return err
	}

	if iatOK {
		if now.Before(iat) {
			return fmt.Errorf("token issued at %s which is in the future: %w", iat.Format(time.RFC3339), ErrTokenIssuedInFuture)
		}
	}

	return nil
}

// ValidateTimeClaims checks the standard JWT time-based claims (exp, nbf, iat)
// against the current UTC time.
func ValidateTimeClaims(claims MapClaims) error {
	return ValidateTimeClaimsAt(claims, time.Now().UTC())
}

// extractTime retrieves a time value from claims by key. It supports float64
// (the default from encoding/json), json.Number (when using a decoder with
// UseNumber), and integer types (int, int32, int64).
//
// Returns:
//   - (time, true, nil) if the claim is present and successfully parsed
//   - (zero, false, nil) if the claim is absent
//   - (zero, false, error) if the claim is present but has an unsupported or unparseable type
func extractTime(claims MapClaims, key string) (time.Time, bool, error) {
	raw, exists := claims[key]
	if !exists {
		return time.Time{}, false, nil
	}

	switch v := raw.(type) {
	case float64:
		return time.Unix(int64(v), 0).UTC(), true, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return time.Time{}, false, fmt.Errorf("claim %q: unparseable json.Number %q: %w", key, v.String(), ErrInvalidTimeClaim)
		}

		return time.Unix(int64(f), 0).UTC(), true, nil
	case int:
		return time.Unix(int64(v), 0).UTC(), true, nil
	case int32:
		return time.Unix(int64(v), 0).UTC(), true, nil
	case int64:
		return time.Unix(v, 0).UTC(), true, nil
	default:
		return time.Time{}, false, fmt.Errorf("claim %q: unsupported type %T: %w", key, raw, ErrInvalidTimeClaim)
	}
}
