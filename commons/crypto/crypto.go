package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
)

var (
	// ErrCipherNotInitialized is returned when encryption/decryption is attempted before InitializeCipher.
	ErrCipherNotInitialized = errors.New("cipher not initialized")
	// ErrCiphertextTooShort is returned when the ciphertext is shorter than the nonce size.
	ErrCiphertextTooShort = errors.New("ciphertext too short")
	// ErrNilCrypto is returned when a Crypto method is called on a nil receiver.
	ErrNilCrypto = errors.New("crypto instance is nil")
	// ErrNilInput is returned when a nil pointer is passed to Encrypt or Decrypt.
	ErrNilInput = errors.New("nil input")
	// ErrEmptyKey is returned when an empty hash secret key is provided to GenerateHash.
	ErrEmptyKey = errors.New("hash secret key must not be empty")
)

// isNilInterface returns true if the interface value is nil or holds a typed nil.
func isNilInterface(i any) bool {
	if i == nil {
		return true
	}

	v := reflect.ValueOf(i)

	return v.Kind() == reflect.Ptr && v.IsNil()
}

// Crypto groups hashing and symmetric encryption helpers.
type Crypto struct {
	HashSecretKey    string
	EncryptSecretKey string
	Logger           libLog.Logger
	Cipher           cipher.AEAD
}

// String implements fmt.Stringer to prevent accidental secret key exposure in logs or spans.
func (c *Crypto) String() string {
	if c == nil {
		return "<nil>"
	}

	return "Crypto{keys:REDACTED}"
}

// GoString implements fmt.GoStringer to prevent accidental secret key exposure in %#v formatting.
func (c *Crypto) GoString() string {
	return c.String()
}

// logger returns the configured Logger, falling back to a NopLogger if nil.
// Uses isNilInterface to detect typed nils (e.g. (*MyLogger)(nil)).
func (c *Crypto) logger() libLog.Logger {
	if c == nil || isNilInterface(c.Logger) {
		return libLog.NewNop()
	}

	return c.Logger
}

// GenerateHash produces an HMAC-SHA256 hex-encoded hash of the plaintext.
//
// Returns "" for nil receiver or nil input as intentional safe degradation:
// callers that cannot supply a Crypto instance or input get a deterministic
// empty result rather than an error, which simplifies optional-hash pipelines.
//
// Returns "" with a logged error if HashSecretKey is empty, since HMAC with
// an empty key produces a valid but insecure hash.
func (c *Crypto) GenerateHash(plaintext *string) string {
	if c == nil || plaintext == nil {
		return ""
	}

	if c.HashSecretKey == "" {
		c.logger().Log(context.Background(), libLog.LevelError, "GenerateHash called with empty HashSecretKey")
		return ""
	}

	h := hmac.New(sha256.New, []byte(c.HashSecretKey))
	h.Write([]byte(*plaintext))

	hash := hex.EncodeToString(h.Sum(nil))

	return hash
}

// InitializeCipher loads an AES-GCM block cipher for encryption/decryption.
// The EncryptSecretKey must be a hex-encoded key of 16, 24, or 32 bytes
// (corresponding to AES-128, AES-192, or AES-256 respectively).
func (c *Crypto) InitializeCipher() error {
	if c == nil {
		return ErrNilCrypto
	}

	if !isNilInterface(c.Cipher) {
		c.logger().Log(context.Background(), libLog.LevelInfo, "Cipher already initialized")
		return nil
	}

	decodedKey, err := hex.DecodeString(c.EncryptSecretKey)
	if err != nil {
		c.logger().Log(context.Background(), libLog.LevelError, "Failed to decode hex private key", libLog.Err(err))
		return fmt.Errorf("crypto: hex decode key: %w", err)
	}

	blockCipher, err := aes.NewCipher(decodedKey)
	if err != nil {
		c.logger().Log(context.Background(), libLog.LevelError, "Error creating AES block cipher with the private key", libLog.Err(err))
		return fmt.Errorf("crypto: create AES block cipher: %w", err)
	}

	aesGcm, err := cipher.NewGCM(blockCipher)
	if err != nil {
		c.logger().Log(context.Background(), libLog.LevelError, "Error creating GCM cipher", libLog.Err(err))
		return fmt.Errorf("crypto: create GCM cipher: %w", err)
	}

	c.Cipher = aesGcm

	return nil
}

// Encrypt a plaintext using AES-GCM with a random 12-byte nonce.
// Requires InitializeCipher to have been called with a valid AES key
// (16, 24, or 32 bytes for AES-128, AES-192, or AES-256 respectively).
// Returns a base64-encoded string with the nonce prepended to the ciphertext.
func (c *Crypto) Encrypt(plainText *string) (*string, error) {
	if c == nil {
		return nil, ErrNilCrypto
	}

	if plainText == nil {
		return nil, ErrNilInput
	}

	if isNilInterface(c.Cipher) {
		return nil, ErrCipherNotInitialized
	}

	// Generates random nonce with a size of 12 bytes
	nonce := make([]byte, c.Cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		c.logger().Log(context.Background(), libLog.LevelError, "Failed to generate nonce", libLog.Err(err))
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}

	// Cipher Text prefixed with the random Nonce
	cipherText := c.Cipher.Seal(nonce, nonce, []byte(*plainText), nil)

	result := base64.StdEncoding.EncodeToString(cipherText)

	return &result, nil
}

// Decrypt a base64 encoded encrypted plaintext.
// The encrypted plain text must be prefixed with the random nonce used for encryption.
func (c *Crypto) Decrypt(encryptedText *string) (*string, error) {
	if c == nil {
		return nil, ErrNilCrypto
	}

	if encryptedText == nil {
		return nil, ErrNilInput
	}

	if isNilInterface(c.Cipher) {
		return nil, ErrCipherNotInitialized
	}

	decodedEncryptedText, err := base64.StdEncoding.DecodeString(*encryptedText)
	if err != nil {
		c.logger().Log(context.Background(), libLog.LevelError, "Failed to decode encrypted text", libLog.Err(err))
		return nil, fmt.Errorf("crypto: decode base64: %w", err)
	}

	nonceSize := c.Cipher.NonceSize()
	if len(decodedEncryptedText) < nonceSize {
		c.logger().Log(context.Background(), libLog.LevelError, "Failed to decrypt ciphertext", libLog.Err(ErrCiphertextTooShort))

		return nil, ErrCiphertextTooShort
	}

	// Separating nonce from ciphertext before decrypting
	nonce, cipherText := decodedEncryptedText[:nonceSize], decodedEncryptedText[nonceSize:]

	// #nosec G407: Nonce is randomly generated by the Encrypt method
	// False positive described at https://github.com/securego/gosec/issues/1209
	plainText, err := c.Cipher.Open(nil, nonce, cipherText, nil)
	if err != nil {
		c.logger().Log(context.Background(), libLog.LevelError, "Failed to decrypt ciphertext", libLog.Err(err))
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}

	result := string(plainText)

	return &result, nil
}
