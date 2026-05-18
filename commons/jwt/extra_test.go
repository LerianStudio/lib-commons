//go:build unit

package jwt

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSecret = []byte("test-secret-for-jwt-unit-tests")

// TestParseAndValidate_ValidToken covers the success path.
func TestParseAndValidate_ValidToken(t *testing.T) {
	t.Parallel()

	claims := jwt.MapClaims{
		"sub": "user-123",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr, err := Sign(claims, AlgHS256, testSecret)
	require.NoError(t, err)

	token, err := ParseAndValidate(tokenStr, testSecret, allAlgorithms)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.True(t, token.SignatureValid)
}

// TestParseAndValidate_ExpiredToken covers the expired token path.
func TestParseAndValidate_ExpiredToken(t *testing.T) {
	t.Parallel()

	claims := jwt.MapClaims{
		"sub": "user-123",
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-1 * time.Hour).Unix(), // expired
	}

	tokenStr, err := Sign(claims, AlgHS256, testSecret)
	require.NoError(t, err)

	_, err = ParseAndValidate(tokenStr, testSecret, allAlgorithms)
	require.Error(t, err)
}

// TestParseAndValidate_InvalidSignature covers the invalid signature path.
func TestParseAndValidate_InvalidSignature(t *testing.T) {
	t.Parallel()

	claims := jwt.MapClaims{
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	tokenStr, err := Sign(claims, AlgHS256, testSecret)
	require.NoError(t, err)

	_, err = ParseAndValidate(tokenStr, []byte("wrong-secret"), allAlgorithms)
	require.Error(t, err)
}
