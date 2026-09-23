// Package crypto provides hashing and symmetric encryption helpers.
//
// The Crypto type supports:
//   - HMAC-SHA256 hashing for deterministic fingerprints
//   - AES-GCM encryption/decryption for confidential payloads
//
// InitializeCipher must be called before Encrypt or Decrypt.
//
// The Sealer type seals raw byte payloads at rest:
//   - AES-256-GCM under a key derived from an operator secret of any format,
//     at least 32 bytes, by HKDF-SHA256 (salt nil, info = purpose), so one
//     secret can serve several purposes without key reuse
//   - caller-supplied additional data bound to every ciphertext
//   - output is nonce || ciphertext, no encoding
//   - Rotate installs a new secret while payloads sealed under the previous
//     one keep opening; exactly one previous generation is kept
//   - safe for concurrent use, including Rotate racing Seal and Open
//
// Open returns ErrOpenFailed for a wrong key and for a tampered payload alike.
package crypto
