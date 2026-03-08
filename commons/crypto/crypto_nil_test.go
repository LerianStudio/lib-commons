//go:build unit

package crypto

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNilReceiver(t *testing.T) {
	t.Parallel()

	t.Run("InitializeCipher returns ErrNilCrypto", func(t *testing.T) {
		t.Parallel()

		var c *Crypto

		err := c.InitializeCipher()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilCrypto)
	})

	t.Run("Encrypt returns ErrNilCrypto", func(t *testing.T) {
		t.Parallel()

		var c *Crypto
		input := "data"

		result, err := c.Encrypt(&input)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilCrypto)
		assert.Nil(t, result)
	})

	t.Run("Decrypt returns ErrNilCrypto", func(t *testing.T) {
		t.Parallel()

		var c *Crypto
		input := "data"

		result, err := c.Decrypt(&input)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilCrypto)
		assert.Nil(t, result)
	})

	t.Run("GenerateHash returns empty string", func(t *testing.T) {
		t.Parallel()

		var c *Crypto
		input := "data"

		result := c.GenerateHash(&input)
		assert.Empty(t, result)
	})

	t.Run("String returns nil marker", func(t *testing.T) {
		t.Parallel()

		var c *Crypto
		assert.Equal(t, "<nil>", c.String())
	})

	t.Run("GoString returns nil marker", func(t *testing.T) {
		t.Parallel()

		var c *Crypto
		assert.Equal(t, "<nil>", c.GoString())
	})

	t.Run("logger returns NopLogger on nil receiver", func(t *testing.T) {
		t.Parallel()

		var c *Crypto
		l := c.logger()
		assert.NotNil(t, l)
	})
}

func TestRedaction(t *testing.T) {
	t.Parallel()

	t.Run("String returns REDACTED text", func(t *testing.T) {
		t.Parallel()

		c := Crypto{
			HashSecretKey:    "super-secret-hash-key",
			EncryptSecretKey: "super-secret-encrypt-key",
		}

		s := c.String()
		assert.Contains(t, s, "REDACTED")
		assert.NotContains(t, s, "super-secret-hash-key")
		assert.NotContains(t, s, "super-secret-encrypt-key")
	})

	t.Run("GoString returns REDACTED text", func(t *testing.T) {
		t.Parallel()

		c := Crypto{
			HashSecretKey:    "super-secret-hash-key",
			EncryptSecretKey: "super-secret-encrypt-key",
		}

		s := c.GoString()
		assert.Contains(t, s, "REDACTED")
		assert.NotContains(t, s, "super-secret-hash-key")
		assert.NotContains(t, s, "super-secret-encrypt-key")
	})

	t.Run("fmt Sprintf %v does not leak secrets", func(t *testing.T) {
		t.Parallel()

		c := &Crypto{
			HashSecretKey:    "secret-hash-value",
			EncryptSecretKey: "secret-encrypt-value",
		}

		output := fmt.Sprintf("%v", c)
		assert.NotContains(t, output, "secret-hash-value")
		assert.NotContains(t, output, "secret-encrypt-value")
		assert.Contains(t, output, "REDACTED")
	})

	t.Run("fmt Sprintf %#v does not leak secrets", func(t *testing.T) {
		t.Parallel()

		c := &Crypto{
			HashSecretKey:    "secret-hash-value",
			EncryptSecretKey: "secret-encrypt-value",
		}

		output := fmt.Sprintf("%#v", c)
		assert.NotContains(t, output, "secret-hash-value")
		assert.NotContains(t, output, "secret-encrypt-value")
		assert.Contains(t, output, "REDACTED")
	})
}
