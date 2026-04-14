package certificate

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Sentinel errors for certificate operations.
var (
	// ErrNilManager is returned when a method is called on a nil *Manager.
	ErrNilManager = errors.New("certificate manager is nil")
	// ErrCertRequired is returned when Rotate is called with a nil certificate.
	ErrCertRequired = errors.New("certificate is required")
	// ErrKeyRequired is returned when Rotate is called with a nil private key.
	ErrKeyRequired = errors.New("private key is required")
	// ErrExpired is returned when the certificate's NotAfter time is in the past.
	ErrExpired = errors.New("certificate is expired")
	// ErrNoPEMBlock is returned when no valid PEM block can be decoded from the input.
	ErrNoPEMBlock = errors.New("no PEM block found")
	// ErrKeyParseFailure is returned when none of the supported key formats (PKCS#8, PKCS#1, EC) can parse the key bytes.
	ErrKeyParseFailure = errors.New("failed to parse private key")
	// ErrNotSigner is returned when the parsed private key does not implement crypto.Signer.
	ErrNotSigner = errors.New("private key does not implement crypto.Signer")
	// ErrKeyMismatch is returned when the certificate's public key does not match the provided private key.
	ErrKeyMismatch = errors.New("certificate public key does not match private key")
	// ErrIncompleteConfig is returned when exactly one of certPath/keyPath is provided; both or neither are required.
	ErrIncompleteConfig = errors.New("both certificate and key paths are required; got only one")
)

// Manager manages the current certificate and key with thread-safe hot reload.
// All public methods are safe for concurrent use.
type Manager struct {
	mu     sync.RWMutex
	cert   *x509.Certificate
	signer crypto.Signer
	chain  [][]byte // DER-encoded certificate chain (leaf first, then intermediates)
}

// NewManager creates a manager and loads the initial certificate from the given
// PEM file paths. If both paths are empty, an empty (unconfigured) manager is
// returned — useful for services where TLS is optional.
func NewManager(certPath, keyPath string) (*Manager, error) {
	m := &Manager{}

	if (certPath != "") != (keyPath != "") {
		return nil, ErrIncompleteConfig
	}

	if certPath != "" && keyPath != "" {
		cert, signer, chain, err := loadFromFiles(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load initial certificate: %w", err)
		}

		m.cert = cert
		m.signer = signer
		m.chain = chain
	}

	return m, nil
}

// Rotate replaces the current certificate and key atomically (hot reload, no restart).
// It rejects expired certificates to prevent silent deployment of invalid credentials.
// Optional intermediates are DER-encoded intermediate certificates appended after
// the leaf in the chain. When omitted, the chain contains only the leaf certificate.
// To preserve a full chain during hot reload, pass the intermediate DER bytes
// obtained from [LoadFromFilesWithChain], e.g.:
//
//	cert, signer, chain, err := LoadFromFilesWithChain(certPath, keyPath)
//	if err != nil { ... }
//	if err := m.Rotate(cert, signer, chain[1:]...); err != nil { ... }
func (m *Manager) Rotate(cert *x509.Certificate, key crypto.Signer, intermediates ...[]byte) error {
	if m == nil {
		return ErrNilManager
	}

	if cert == nil {
		return ErrCertRequired
	}

	if key == nil {
		return ErrKeyRequired
	}

	// Guard against interface wrapping a nil concrete value. Real crypto types
	// (ecdsa, rsa, ed25519) panic on nil receiver in Public(), so we recover
	// rather than relying on the return value check.
	pub, panicked := safePublic(key)
	if panicked || pub == nil {
		return ErrKeyRequired
	}

	now := time.Now()

	if now.Before(cert.NotBefore) {
		return fmt.Errorf("certificate is not yet valid (notBefore: %s)", cert.NotBefore)
	}

	if now.After(cert.NotAfter) {
		return fmt.Errorf("%w (notAfter: %s)", ErrExpired, cert.NotAfter)
	}

	if !publicKeysMatch(cert.PublicKey, key.Public()) {
		return ErrKeyMismatch
	}

	// Deep-copy the leaf to prevent aliasing caller-owned memory.
	// x509.ParseCertificate does NOT deep-copy the input DER, so cert.Raw
	// may alias the caller's buffer. Re-parsing from a copy ensures the
	// manager owns independent memory.
	rawCopy := make([]byte, len(cert.Raw))
	copy(rawCopy, cert.Raw)

	ownedCert, err := x509.ParseCertificate(rawCopy)
	if err != nil {
		return fmt.Errorf("certificate: failed to re-parse leaf: %w", err)
	}

	m.mu.Lock()
	m.cert = ownedCert
	m.signer = key
	chain := make([][]byte, 0, 1+len(intermediates))
	chain = append(chain, rawCopy)

	for _, inter := range intermediates {
		interCopy := make([]byte, len(inter))
		copy(interCopy, inter)
		chain = append(chain, interCopy)
	}

	m.chain = chain
	m.mu.Unlock()

	return nil
}

// GetCertificate returns a deep copy of the current certificate, or nil if
// none is loaded. The returned *x509.Certificate is an independent clone that
// callers may freely modify without affecting the manager's internal state.
// Use [LoadFromFiles] + [Manager.Rotate] to replace the managed certificate.
func (m *Manager) GetCertificate() *x509.Certificate {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return cloneCert(m.cert)
}

// GetSigner returns the current private key as a crypto.Signer, or nil if none is loaded.
// The returned signer shares the underlying private key material with the manager (it is
// NOT a copy). Callers must not modify the concrete key via unsafe type assertions.
// Ownership of the key is transferred to the manager at [Rotate] time — the caller
// must not mutate the key after calling Rotate.
func (m *Manager) GetSigner() crypto.Signer {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.signer
}

// PublicKey returns a detached copy of the public key from the current certificate.
// The returned value is a deep clone for known key types ([*rsa.PublicKey],
// [*ecdsa.PublicKey], [ed25519.PublicKey]) so callers get a concurrency-safe copy
// that does not alias internal manager state.
// Returns nil if no certificate is loaded.
func (m *Manager) PublicKey() crypto.PublicKey {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cert == nil {
		return nil
	}

	return clonePublicKey(m.cert.PublicKey)
}

// clonePublicKey returns a deep copy of pub for known concrete key types.
// Unknown types are returned as-is (best effort).
func clonePublicKey(pub crypto.PublicKey) crypto.PublicKey {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return &rsa.PublicKey{
			N: new(big.Int).Set(k.N),
			E: k.E,
		}
	case *ecdsa.PublicKey:
		return &ecdsa.PublicKey{
			Curve: k.Curve,
			X:     new(big.Int).Set(k.X),
			Y:     new(big.Int).Set(k.Y),
		}
	case ed25519.PublicKey:
		cp := make(ed25519.PublicKey, len(k))
		copy(cp, k)

		return cp
	default:
		return pub
	}
}

// ExpiresAt returns when the current certificate expires.
// Returns the zero time if no certificate is loaded.
func (m *Manager) ExpiresAt() time.Time {
	if m == nil {
		return time.Time{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cert == nil {
		return time.Time{}
	}

	return m.cert.NotAfter
}

// DaysUntilExpiry returns the number of days until the certificate expires.
// It returns -1 when no certificate is loaded (nil receiver or no certificate
// configured via [NewManager]). Otherwise it returns the number of days until
// expiry, which may be negative for already-expired certificates (e.g. -3
// means the certificate expired 3 days ago).
func (m *Manager) DaysUntilExpiry() int {
	if m == nil {
		return -1
	}

	exp := m.ExpiresAt()
	if exp.IsZero() {
		return -1
	}

	return int(time.Until(exp).Hours() / 24)
}

// TLSCertificate returns a [tls.Certificate] built from the currently loaded
// certificate chain and private key. Returns an empty [tls.Certificate] if no
// certificate is loaded. Both the Certificate [][]byte chain and the Leaf are
// deep copies, so callers never receive references aliasing internal state.
// Safe to call on a nil receiver (returns an empty [tls.Certificate]).
func (m *Manager) TLSCertificate() tls.Certificate {
	if m == nil {
		return tls.Certificate{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cert == nil {
		return tls.Certificate{}
	}

	chainCopy := make([][]byte, len(m.chain))
	for i, der := range m.chain {
		derCopy := make([]byte, len(der))
		copy(derCopy, der)
		chainCopy[i] = derCopy
	}

	return tls.Certificate{
		Certificate: chainCopy,
		PrivateKey:  m.signer,
		Leaf:        cloneCert(m.cert),
	}
}

// GetCertificateFunc returns a function suitable for use as [tls.Config.GetCertificate].
// The returned function always serves the most recently loaded certificate, making
// hot-reload transparent to the TLS layer.
// Safe to call on a nil receiver (returns a function that always returns [ErrNilManager]).
func (m *Manager) GetCertificateFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if m == nil {
		return func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return nil, ErrNilManager
		}
	}

	return func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert := m.TLSCertificate()
		if cert.Certificate == nil {
			return nil, ErrCertRequired
		}

		return &cert, nil
	}
}

// LoadFromFiles loads and validates a certificate and private key from PEM files
// without modifying any manager state. This allows callers to validate a new
// cert/key pair before committing the swap via [Manager.Rotate].
//
// Private keys are parsed as PKCS#8 first, with PKCS#1 and EC key fallback.
// Returns an error if the certificate's public key does not match the private key.
func LoadFromFiles(certPath, keyPath string) (*x509.Certificate, crypto.Signer, error) {
	cert, signer, _, err := loadFromFiles(certPath, keyPath)
	return cert, signer, err
}

// LoadFromFilesWithChain loads and validates a certificate and private key from
// PEM files and also returns the full DER-encoded certificate chain (leaf first,
// then intermediates). Use this when you need to pass intermediates to
// [Manager.Rotate] for chain-preserving hot reload:
//
//	cert, signer, chain, err := LoadFromFilesWithChain(certPath, keyPath)
//	if err != nil { ... }
//	if err := m.Rotate(cert, signer, chain[1:]...); err != nil { ... }
func LoadFromFilesWithChain(certPath, keyPath string) (*x509.Certificate, crypto.Signer, [][]byte, error) {
	return loadFromFiles(certPath, keyPath)
}

// loadFromFiles is the internal implementation that also returns the full DER chain.
func loadFromFiles(certPath, keyPath string) (*x509.Certificate, crypto.Signer, [][]byte, error) {
	certPath = filepath.Clean(certPath)
	keyPath = filepath.Clean(keyPath)

	cert, certChain, err := parseCertPEM(certPath)
	if err != nil {
		return nil, nil, nil, err
	}

	signer, err := parseKeyFile(keyPath)
	if err != nil {
		return nil, nil, nil, err
	}

	if !publicKeysMatch(cert.PublicKey, signer.Public()) {
		return nil, nil, nil, ErrKeyMismatch
	}

	return cert, signer, certChain, nil
}

// parseCertPEM reads PEM-encoded certificates from certPath, parses the leaf
// certificate, and validates its NotBefore/NotAfter lifetime window. It returns
// the parsed leaf certificate and the full DER chain (leaf first).
func parseCertPEM(certPath string) (*x509.Certificate, [][]byte, error) {
	certPEM, err := os.ReadFile(certPath) // #nosec G304 -- cert path comes from trusted configuration
	if err != nil {
		return nil, nil, fmt.Errorf("read cert: %w", err)
	}

	var certChain [][]byte

	rest := certPEM

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type == "CERTIFICATE" {
			certChain = append(certChain, block.Bytes)
		}
	}

	if len(certChain) == 0 {
		return nil, nil, fmt.Errorf("cert file: %w", ErrNoPEMBlock)
	}

	cert, err := x509.ParseCertificate(certChain[0])
	if err != nil {
		return nil, nil, fmt.Errorf("parse cert: %w", err)
	}

	now := time.Now()
	if now.Before(cert.NotBefore) {
		return nil, nil, fmt.Errorf("certificate is not yet valid (notBefore: %s)", cert.NotBefore)
	}

	if now.After(cert.NotAfter) {
		return nil, nil, fmt.Errorf("%w (notAfter: %s)", ErrExpired, cert.NotAfter)
	}

	return cert, certChain, nil
}

// parseKeyFile reads a PEM-encoded private key from keyPath after verifying that
// file permissions are 0600 or stricter. It tries PKCS#8, PKCS#1 (RSA), and
// SEC 1 (EC) encodings in order and returns the first successful parse as a
// crypto.Signer.
func parseKeyFile(keyPath string) (crypto.Signer, error) {
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("stat key file: %w", err)
	}

	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("key file %q has overly permissive mode %04o; expected 0600 or stricter", keyPath, perm)
	}

	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 -- key path comes from trusted configuration
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("key file: %w", ErrNoPEMBlock)
	}

	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		// PKCS#1 fallback for legacy RSA keys.
		key, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			// EC key fallback for PEM-encoded SEC 1 keys.
			key, err = x509.ParseECPrivateKey(keyBlock.Bytes)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrKeyParseFailure, err)
			}
		}
	}

	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, ErrNotSigner
	}

	return signer, nil
}

// safePublic calls key.Public() with panic recovery. Real crypto types (ecdsa,
// rsa, ed25519) panic when called on a nil concrete value behind a non-nil
// interface. This function converts that panic to a (nil, true) return.
func safePublic(key crypto.Signer) (pub crypto.PublicKey, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()

	return key.Public(), false
}

// publicKeysMatch compares two public keys by their DER-encoded PKIX representation.
func publicKeysMatch(certPublicKey, signerPublicKey any) bool {
	certDER, err := x509.MarshalPKIXPublicKey(certPublicKey)
	if err != nil {
		return false
	}

	signerDER, err := x509.MarshalPKIXPublicKey(signerPublicKey)
	if err != nil {
		return false
	}

	return bytes.Equal(certDER, signerDER)
}

// cloneCert returns a deep copy of cert by re-parsing its DER encoding.
// Returns nil when cert is nil.
func cloneCert(cert *x509.Certificate) *x509.Certificate {
	if cert == nil {
		return nil
	}

	raw := make([]byte, len(cert.Raw))
	copy(raw, cert.Raw)

	// ParseCertificate is the canonical way to deep-copy an x509.Certificate.
	// Errors here are unexpected (the DER was already parsed once), but we
	// return nil rather than panicking to stay consistent with the package's
	// nil-safe contract.
	clone, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil
	}

	return clone
}
