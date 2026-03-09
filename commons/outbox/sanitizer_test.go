//go:build unit

package outbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeErrorForStorage_RedactsSecrets(t *testing.T) {
	t.Parallel()

	err := errors.New("bearer eyJabc.def.ghi api_key=secret123 user@mail.com 4111111111111111")
	msg := sanitizeErrorForStorage(err)

	require.NotContains(t, msg, "secret123")
	require.NotContains(t, msg, "user@mail.com")
	require.NotContains(t, msg, "4111111111111111")
	require.Contains(t, msg, redactedValue)
}

func TestSanitizeErrorForStorage_Truncates(t *testing.T) {
	t.Parallel()

	err := errors.New(strings.Repeat("x", maxErrorLength+30))
	msg := sanitizeErrorForStorage(err)

	require.LessOrEqual(t, len([]rune(msg)), maxErrorLength)
	require.Contains(t, msg, errorTruncatedSuffix)
}

func TestSanitizeErrorForStorage_RedactsConnectionStringsAndCloudSecrets(t *testing.T) {
	t.Parallel()

	err := errors.New(
		"dial postgres://user:myPassword@db.local:5432/app " +
			"AKIAIOSFODNN7EXAMPLE aws_secret_access_key=abcd1234",
	)

	msg := sanitizeErrorForStorage(err)

	require.NotContains(t, msg, "myPassword")
	require.NotContains(t, msg, "AKIAIOSFODNN7EXAMPLE")
	require.NotContains(t, msg, "abcd1234")
	require.Contains(t, msg, redactedValue)
}

func TestSanitizeErrorForStorage_NilError(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", sanitizeErrorForStorage(nil))
}

func TestSanitizeErrorMessageForStorage_ShortMessageUnchanged(t *testing.T) {
	t.Parallel()

	msg := "safe short error"
	require.Equal(t, msg, SanitizeErrorMessageForStorage(msg))
}

func TestSanitizeErrorMessageForStorage_RedactsQueryParameterCredentials(t *testing.T) {
	t.Parallel()

	message := "request failed https://api.test.local/callback?password=super-secret&mode=sync"
	sanitized := SanitizeErrorMessageForStorage(message)

	require.NotContains(t, sanitized, "super-secret")
	require.Contains(t, sanitized, "password="+redactedValue)
}

func TestSanitizeErrorMessageForStorage_DoesNotRedactNonLuhnLongNumbers(t *testing.T) {
	t.Parallel()

	message := "failed at unix_ms=1700000000000 while parsing request"
	sanitized := SanitizeErrorMessageForStorage(message)

	require.Contains(t, sanitized, "1700000000000")
	require.NotContains(t, sanitized, redactedValue)
}

func TestSanitizeErrorMessageForStorage_RedactsAuthorizationBasicHeader(t *testing.T) {
	t.Parallel()

	message := "downstream call failed Authorization: Basic dXNlcjpwYXNz"
	sanitized := SanitizeErrorMessageForStorage(message)

	require.NotContains(t, sanitized, "dXNlcjpwYXNz")
	require.Contains(t, sanitized, "Authorization: Basic "+redactedValue)
}

func TestSanitizeErrorMessageForStorage_UnicodeInput(t *testing.T) {
	t.Parallel()

	message := "erro de autenticao 🔒 usuario=test@example.com senha=segredo"
	sanitized := SanitizeErrorMessageForStorage(message)

	require.Contains(t, sanitized, "🔒")
	require.NotContains(t, sanitized, "test@example.com")
	require.Contains(t, sanitized, redactedValue)
}
