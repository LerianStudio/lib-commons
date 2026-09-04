//go:build unit

package crypto

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validHexKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestCrypto(t *testing.T) *Crypto {
	t.Helper()

	c := &Crypto{
		HashSecretKey:    "hash-secret",
		EncryptSecretKey: validHexKey,
		Logger:           obs.Nop(),
	}

	require.NoError(t, c.InitializeCipher())

	return c
}

func ptr(s string) *string { return &s }

func TestGenerateHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     *string
		expectLen int
	}{
		{
			name:      "nil input returns empty string",
			input:     nil,
			expectLen: 0,
		},
		{
			name:      "non-nil input returns 64-char hex hash",
			input:     ptr("hello"),
			expectLen: 64,
		},
		{
			name:      "empty string input returns 64-char hex hash",
			input:     ptr(""),
			expectLen: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Crypto{HashSecretKey: "test-key", Logger: obs.Nop()}
			result := c.GenerateHash(tt.input)

			if tt.input == nil {
				assert.Empty(t, result)
			} else {
				assert.Len(t, result, tt.expectLen)
			}
		})
	}
}

func TestGenerateHash_Consistency(t *testing.T) {
	t.Parallel()

	c := &Crypto{HashSecretKey: "test-key", Logger: obs.Nop()}
	input := ptr("hello")

	hash1 := c.GenerateHash(input)
	hash2 := c.GenerateHash(input)

	assert.Equal(t, hash1, hash2)
}

func TestGenerateHash_DifferentInputs(t *testing.T) {
	t.Parallel()

	c := &Crypto{HashSecretKey: "test-key", Logger: obs.Nop()}

	hash1 := c.GenerateHash(ptr("hello"))
	hash2 := c.GenerateHash(ptr("world"))

	assert.NotEqual(t, hash1, hash2)
}

func TestInitializeCipher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       string
		expectErr bool
	}{
		{
			name:      "valid 32-byte hex key succeeds",
			key:       validHexKey,
			expectErr: false,
		},
		{
			name:      "invalid hex characters",
			key:       "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			expectErr: true,
		},
		{
			name:      "wrong key length (15 bytes)",
			key:       "0123456789abcdef0123456789abcd",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Crypto{EncryptSecretKey: tt.key, Logger: obs.Nop()}
			err := c.InitializeCipher()

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, c.Cipher)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, c.Cipher)
			}
		})
	}
}

func TestInitializeCipher_AlreadyInitialized(t *testing.T) {
	t.Parallel()

	c := newTestCrypto(t)
	originalCipher := c.Cipher

	err := c.InitializeCipher()

	assert.NoError(t, err)
	assert.Equal(t, originalCipher, c.Cipher)
}

func TestEncrypt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		initCipher bool
		input      *string
		expectNil  bool
		expectErr  bool
		sentinel   error
	}{
		{
			name:       "nil input returns error",
			initCipher: true,
			input:      nil,
			expectNil:  true,
			expectErr:  true,
			sentinel:   ErrNilInput,
		},
		{
			name:       "uninitialized cipher returns error",
			initCipher: false,
			input:      ptr("hello"),
			expectNil:  true,
			expectErr:  true,
			sentinel:   ErrCipherNotInitialized,
		},
		{
			name:       "successful encryption",
			initCipher: true,
			input:      ptr("hello world"),
			expectNil:  false,
			expectErr:  false,
		},
		{
			name:       "empty string encrypts successfully",
			initCipher: true,
			input:      ptr(""),
			expectNil:  false,
			expectErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Crypto{
				EncryptSecretKey: validHexKey,
				Logger:           obs.Nop(),
			}
			if tt.initCipher {
				require.NoError(t, c.InitializeCipher())
			}

			result, err := c.Encrypt(tt.input)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.sentinel != nil {
					assert.ErrorIs(t, err, tt.sentinel)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.NotEmpty(t, *result)
				// Result must be valid base64
				_, decErr := base64.StdEncoding.DecodeString(*result)
				assert.NoError(t, decErr)
			}
		})
	}
}

func TestDecrypt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		initCipher bool
		input      *string
		expectNil  bool
		expectErr  bool
		sentinel   error
	}{
		{
			name:       "nil input returns error",
			initCipher: true,
			input:      nil,
			expectNil:  true,
			expectErr:  true,
			sentinel:   ErrNilInput,
		},
		{
			name:       "uninitialized cipher returns error",
			initCipher: false,
			input:      ptr("c29tZXRoaW5n"),
			expectNil:  true,
			expectErr:  true,
			sentinel:   ErrCipherNotInitialized,
		},
		{
			name:       "invalid base64 input",
			initCipher: true,
			input:      ptr("!!!not-base64!!!"),
			expectNil:  true,
			expectErr:  true,
		},
		{
			name:       "ciphertext too short",
			initCipher: true,
			input:      ptr(base64.StdEncoding.EncodeToString([]byte("short"))),
			expectNil:  true,
			expectErr:  true,
			sentinel:   ErrCiphertextTooShort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Crypto{
				EncryptSecretKey: validHexKey,
				Logger:           obs.Nop(),
			}
			if tt.initCipher {
				require.NoError(t, c.InitializeCipher())
			}

			result, err := c.Decrypt(tt.input)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.sentinel != nil {
					assert.ErrorIs(t, err, tt.sentinel)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectNil {
				assert.Nil(t, result)
			}
		})
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	t.Parallel()

	c := newTestCrypto(t)

	inputs := []string{
		"hello world",
		"",
		"special chars: !@#$%^&*()",
		"unicode: 日本語テスト 🎉",
		"a longer string that exercises the AES-GCM cipher with more data to process in blocks",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			encrypted, err := c.Encrypt(ptr(input))
			require.NoError(t, err)
			require.NotNil(t, encrypted)

			decrypted, err := c.Decrypt(encrypted)
			require.NoError(t, err)
			require.NotNil(t, decrypted)

			assert.Equal(t, input, *decrypted)
		})
	}
}

func TestEncrypt_ProducesUniqueOutputs(t *testing.T) {
	t.Parallel()

	c := newTestCrypto(t)
	input := ptr("same plaintext")

	enc1, err1 := c.Encrypt(input)
	require.NoError(t, err1)

	enc2, err2 := c.Encrypt(input)
	require.NoError(t, err2)

	assert.NotEqual(t, *enc1, *enc2, "AES-GCM with random nonce should produce different ciphertexts")
}

func TestGenerateHash_EmptyKey(t *testing.T) {
	t.Parallel()

	c := &Crypto{HashSecretKey: "", Logger: obs.Nop()}
	input := ptr("hello")

	result := c.GenerateHash(input)
	assert.Empty(t, result, "GenerateHash with empty key should return empty string")
}

func TestLogger(t *testing.T) {
	t.Parallel()

	t.Run("returns configured logger", func(t *testing.T) {
		t.Parallel()

		nop := obs.Nop()
		c := &Crypto{Logger: nop}

		assert.Equal(t, nop, c.logger())
	})

	t.Run("returns NopLogger when Logger is nil", func(t *testing.T) {
		t.Parallel()

		c := &Crypto{}
		l := c.logger()

		assert.NotNil(t, l)
		assert.IsType(t, obs.Nop(), l)
	})

	t.Run("returns NopLogger for typed-nil Logger", func(t *testing.T) {
		t.Parallel()

		// Simulate a typed-nil: the interface holds a nil pointer of a
		// concrete logger type. This exercises the isNilInterface path.
		var nilLogger *typedNilLogger
		c := &Crypto{Logger: nilLogger}
		l := c.logger()

		assert.NotNil(t, l)
		assert.IsType(t, obs.Nop(), l)
	})
}

// typedNilLogger exists only so a test can put a typed nil into an obs.Logger
// interface value.
type typedNilLogger struct{}

func (*typedNilLogger) Log(context.Context, int, string, ...any) {}

func (*typedNilLogger) Enabled(int) bool { return false }

func (*typedNilLogger) Sync(context.Context) error { return nil }
