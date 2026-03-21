// Package crypto provides hashing and symmetric encryption helpers.
//
// The Crypto type supports:
//   - HMAC-SHA256 hashing for deterministic fingerprints
//   - AES-GCM encryption/decryption for confidential payloads
//
// InitializeCipher must be called before Encrypt or Decrypt.
package crypto
