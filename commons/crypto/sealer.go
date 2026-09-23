package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync/atomic"

	constant "github.com/LerianStudio/lib-observability/v4/constants"
)

// sealerKeySize is the AES-256 key length derived from the operator secret.
const sealerKeySize = 32

// minSecretLen is the shortest operator secret a Sealer accepts. HKDF adds no
// work factor, so a shorter secret would cap the derived key's strength below
// AES-256 and invite offline brute force from a single sealed payload.
const minSecretLen = 32

var (
	// ErrNilSealer is returned when a Sealer method is called on a nil receiver.
	ErrNilSealer = errors.New("sealer instance is nil")
	// ErrEmptyPurpose is returned when a Sealer is built without a purpose label.
	ErrEmptyPurpose = errors.New("sealer purpose must not be empty")
	// ErrEmptySecret is returned when a Sealer is built or rotated with an empty secret.
	ErrEmptySecret = errors.New("sealer secret must not be empty")
	// ErrSecretTooShort is returned when a Sealer is built or rotated with a
	// non-empty secret shorter than 32 bytes.
	ErrSecretTooShort = errors.New("sealer secret must be at least 32 bytes")
	// ErrOpenFailed is returned when a sealed payload cannot be opened. It does
	// not say whether the key was wrong or the payload or additional data was
	// tampered with.
	ErrOpenFailed = errors.New("sealed payload could not be opened")
)

// keyset is immutable once published; Rotate swaps in a new one.
type keyset struct {
	current cipher.AEAD
	// currentID is a SHA-256 fingerprint of the derived current key, kept so
	// Rotate can recognise a rotation to the secret already in use and leave
	// the previous generation alone. It is derived from the key, never the
	// secret, and is not itself a secret.
	currentID [sha256.Size]byte
	// ponytail: exactly one previous generation, so a second rotation orphans
	// payloads sealed two secrets ago; hold a slice of previous AEADs if a
	// deployment ever needs a longer rotation window.
	previous cipher.AEAD
}

// Sealer seals byte payloads with AES-256-GCM under a key derived from an
// operator secret by HKDF-SHA256 (info = purpose, salt = nil), binds the
// caller's additional data, and opens payloads sealed under the current OR
// the previous secret so a rotation does not orphan stored ciphertexts.
//
// Two Sealers built from the same purpose and secret interoperate, including
// across processes. A Sealer is safe for concurrent use; Rotate may race with
// Seal and Open.
type Sealer struct {
	purpose string
	keys    atomic.Pointer[keyset]
}

// NewSealer derives an AES-256-GCM key from secret, scoped by purpose.
// secret is an operator secret of any format, at least 32 bytes; a shorter one
// returns ErrSecretTooShort. Distinct purposes yield unrelated keys from the
// same secret.
func NewSealer(purpose string, secret []byte) (*Sealer, error) {
	if purpose == "" {
		return nil, ErrEmptyPurpose
	}

	aead, id, err := deriveAEAD(purpose, secret)
	if err != nil {
		return nil, err
	}

	s := &Sealer{purpose: purpose}
	s.keys.Store(&keyset{current: aead, currentID: id})

	return s, nil
}

func deriveAEAD(purpose string, secret []byte) (cipher.AEAD, [sha256.Size]byte, error) {
	var id [sha256.Size]byte

	if len(secret) == 0 {
		return nil, id, ErrEmptySecret
	}

	if len(secret) < minSecretLen {
		return nil, id, ErrSecretTooShort
	}

	key, err := hkdf.Key(sha256.New, secret, nil, purpose, sealerKeySize)
	if err != nil {
		return nil, id, fmt.Errorf("crypto: derive sealer key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, id, fmt.Errorf("crypto: create AES block cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, id, fmt.Errorf("crypto: create GCM cipher: %w", err)
	}

	return aead, sha256.Sum256(key), nil
}

func (s *Sealer) keyset() (*keyset, error) {
	if s == nil {
		return nil, ErrNilSealer
	}

	ks := s.keys.Load()
	if ks == nil {
		return nil, ErrCipherNotInitialized
	}

	return ks, nil
}

// Seal encrypts plaintext under the current key, binding additionalData.
// It returns nonce || ciphertext as raw bytes.
func (s *Sealer) Seal(plaintext, additionalData []byte) ([]byte, error) {
	ks, err := s.keyset()
	if err != nil {
		return nil, err
	}

	nonceSize := ks.current.NonceSize()
	out := make([]byte, nonceSize, nonceSize+len(plaintext)+ks.current.Overhead())

	if _, err := rand.Read(out); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}

	return ks.current.Seal(out, out[:nonceSize], plaintext, additionalData), nil
}

// Open decrypts a payload produced by Seal with the same additionalData,
// trying the current key and then the previous one.
func (s *Sealer) Open(sealed, additionalData []byte) ([]byte, error) {
	ks, err := s.keyset()
	if err != nil {
		return nil, err
	}

	nonceSize := ks.current.NonceSize()
	if len(sealed) < nonceSize {
		return nil, ErrCiphertextTooShort
	}

	nonce, ciphertext := sealed[:nonceSize], sealed[nonceSize:]

	for _, aead := range [...]cipher.AEAD{ks.current, ks.previous} {
		if aead == nil {
			continue
		}

		// #nosec G407: nonce is read from the payload, generated randomly by Seal.
		if plaintext, err := aead.Open(nil, nonce, ciphertext, additionalData); err == nil {
			return plaintext, nil
		}
	}

	return nil, ErrOpenFailed
}

// Rotate makes next the current secret and demotes the current one to
// previous. The generation before that is dropped. A next that derives the
// key already in use is a no-op, so a configuration refresh that re-sends an
// unchanged secret does not overwrite the previous generation with a copy of
// the current one. An empty next returns ErrEmptySecret, a next shorter than
// 32 bytes returns ErrSecretTooShort, and both leave the keys unchanged.
func (s *Sealer) Rotate(next []byte) error {
	if _, err := s.keyset(); err != nil {
		return err
	}

	aead, id, err := deriveAEAD(s.purpose, next)
	if err != nil {
		return err
	}

	for {
		old := s.keys.Load()
		if old.currentID == id {
			return nil
		}

		if s.keys.CompareAndSwap(old, &keyset{current: aead, currentID: id, previous: old.current}) {
			return nil
		}
	}
}

// String implements fmt.Stringer to prevent accidental key exposure in logs or spans.
func (s *Sealer) String() string {
	if s == nil {
		return "<nil>"
	}

	return "Sealer{purpose:" + s.purpose + ", keys:" + constant.ObfuscatedValue + "}"
}

// GoString implements fmt.GoStringer to prevent accidental key exposure in %#v formatting.
func (s *Sealer) GoString() string {
	return s.String()
}
