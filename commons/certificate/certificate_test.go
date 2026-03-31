//go:build unit

package certificate

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestCert(t *testing.T, notAfter time.Time) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	certFile, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, certFile.Close())

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	// Key files must be 0600 or stricter — the production code enforces this.
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, keyFile.Close())

	return certPath, keyPath
}

func TestNewManager_Empty(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)
	assert.Nil(t, m.GetCertificate())
	assert.Nil(t, m.GetSigner())
	assert.Nil(t, m.PublicKey())
	assert.True(t, m.ExpiresAt().IsZero())
	assert.Equal(t, -1, m.DaysUntilExpiry())
}

func TestNewManager_WithCert(t *testing.T) {
	t.Parallel()

	certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))
	m, err := NewManager(certPath, keyPath)
	require.NoError(t, err)
	assert.NotNil(t, m.GetCertificate())
	assert.NotNil(t, m.GetSigner())
	assert.NotNil(t, m.PublicKey())
	assert.False(t, m.ExpiresAt().IsZero())
	assert.True(t, m.DaysUntilExpiry() > 300)
}

func TestNewManager_OnlyOnePath(t *testing.T) {
	t.Parallel()

	_, err := NewManager("cert.pem", "")
	assert.ErrorIs(t, err, ErrIncompleteConfig)

	_, err = NewManager("", "key.pem")
	assert.ErrorIs(t, err, ErrIncompleteConfig)
}

func TestNewManager_InvalidCertPath(t *testing.T) {
	t.Parallel()

	_, err := NewManager("/nonexistent/cert.pem", "/nonexistent/key.pem")
	assert.Error(t, err)
}

func TestNewManager_InvalidPEM(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	require.NoError(t, os.WriteFile(certPath, []byte("not pem"), 0o644))
	require.NoError(t, os.WriteFile(keyPath, []byte("not pem"), 0o644))

	_, err := NewManager(certPath, keyPath)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNoPEMBlock)
}

func TestNewManager_MismatchedCertificateAndKey(t *testing.T) {
	t.Parallel()

	certPath, _ := generateTestCert(t, time.Now().Add(365*24*time.Hour))
	_, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))

	_, err := NewManager(certPath, keyPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyMismatch)
}

func TestRotate_Success(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))
	cert, signer, err := LoadFromFiles(certPath, keyPath)
	require.NoError(t, err)

	assert.NoError(t, m.Rotate(cert, signer))
	assert.NotNil(t, m.GetCertificate())
}

func TestRotate_NilCert(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	assert.ErrorIs(t, m.Rotate(nil, nil), ErrCertRequired)
}

func TestRotate_NilSigner(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))
	cert, _, err := LoadFromFiles(certPath, keyPath)
	require.NoError(t, err)

	assert.ErrorIs(t, m.Rotate(cert, nil), ErrKeyRequired)
}

func TestRotate_ExpiredCert(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	certPath, keyPath := generateTestCert(t, time.Now().Add(-time.Hour))
	cert, signer, loadErr := LoadFromFiles(certPath, keyPath)
	require.NoError(t, loadErr)

	rotateErr := m.Rotate(cert, signer)
	assert.ErrorIs(t, rotateErr, ErrExpired)
}

func TestRotate_NilManager(t *testing.T) {
	t.Parallel()

	var m *Manager
	assert.ErrorIs(t, m.Rotate(nil, nil), ErrNilManager)
}

func TestDaysUntilExpiry_SoonExpiring(t *testing.T) {
	t.Parallel()

	certPath, keyPath := generateTestCert(t, time.Now().Add(48*time.Hour))
	m, err := NewManager(certPath, keyPath)
	require.NoError(t, err)

	days := m.DaysUntilExpiry()
	assert.True(t, days >= 1 && days <= 2)
}

func TestNilManager_AllMethods(t *testing.T) {
	t.Parallel()

	var m *Manager
	assert.Nil(t, m.GetCertificate())
	assert.Nil(t, m.GetSigner())
	assert.Nil(t, m.PublicKey())
	assert.True(t, m.ExpiresAt().IsZero())
	assert.Equal(t, -1, m.DaysUntilExpiry())
}

// ---------------------------------------------------------------------------
// 1. TestLoadFromFiles_DirectCoverage
// ---------------------------------------------------------------------------

func TestLoadFromFiles_DirectCoverage(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()

		certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))
		cert, signer, err := LoadFromFiles(certPath, keyPath)
		require.NoError(t, err)
		assert.NotNil(t, cert)
		assert.NotNil(t, signer)
	})

	t.Run("nonexistent cert file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		_, _, err := LoadFromFiles(
			filepath.Join(dir, "missing-cert.pem"),
			filepath.Join(dir, "missing-key.pem"),
		)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "read cert"), "expected 'read cert' in error, got: %s", err)
	})

	t.Run("nonexistent key file", func(t *testing.T) {
		t.Parallel()

		// Create a valid cert file but no key file.
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(42),
			Subject:      pkix.Name{CommonName: "test"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
		}

		certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		require.NoError(t, err)

		dir := t.TempDir()
		certPath := filepath.Join(dir, "cert.pem")

		certFile, err := os.Create(certPath)
		require.NoError(t, err)
		require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
		require.NoError(t, certFile.Close())

		_, _, loadErr := LoadFromFiles(certPath, filepath.Join(dir, "missing-key.pem"))
		require.Error(t, loadErr)
		// stat key file fails before read, so the error says "stat key file"
		assert.True(t, strings.Contains(loadErr.Error(), "key"), "expected key-related error, got: %s", loadErr)
	})

	t.Run("invalid PEM cert", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath := filepath.Join(dir, "cert.pem")
		keyPath := filepath.Join(dir, "key.pem")

		require.NoError(t, os.WriteFile(certPath, []byte("not pem data at all"), 0o644))
		require.NoError(t, os.WriteFile(keyPath, []byte("not pem data at all"), 0o600))

		_, _, err := LoadFromFiles(certPath, keyPath)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNoPEMBlock)
	})

	t.Run("invalid PEM key", func(t *testing.T) {
		t.Parallel()

		// Valid cert, but key file is not PEM.
		certPath, _ := generateTestCert(t, time.Now().Add(time.Hour))

		dir := t.TempDir()
		keyPath := filepath.Join(dir, "key.pem")
		require.NoError(t, os.WriteFile(keyPath, []byte("this is not pem"), 0o600))

		_, _, err := LoadFromFiles(certPath, keyPath)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNoPEMBlock)
	})

	t.Run("garbage DER in cert PEM", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath := filepath.Join(dir, "cert.pem")
		keyPath := filepath.Join(dir, "key.pem")

		// A syntactically valid PEM block, but the DER bytes inside are garbage.
		garbage := []byte("definitely not ASN.1")
		var certBuf strings.Builder
		require.NoError(t, pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: garbage}))
		require.NoError(t, os.WriteFile(certPath, []byte(certBuf.String()), 0o644))
		require.NoError(t, os.WriteFile(keyPath, []byte("placeholder"), 0o600))

		_, _, err := LoadFromFiles(certPath, keyPath)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "parse cert"), "expected 'parse cert' in error, got: %s", err)
	})

	t.Run("garbage DER in key PEM", func(t *testing.T) {
		t.Parallel()

		// Use a valid cert file, then a PEM key file with garbage DER bytes.
		certPath, _ := generateTestCert(t, time.Now().Add(time.Hour))

		dir := t.TempDir()
		keyPath := filepath.Join(dir, "key.pem")

		var keyBuf strings.Builder
		require.NoError(t, pem.Encode(&keyBuf, &pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage der")}))
		require.NoError(t, os.WriteFile(keyPath, []byte(keyBuf.String()), 0o600))

		_, _, err := LoadFromFiles(certPath, keyPath)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrKeyParseFailure)
	})

	t.Run("mismatched cert and key", func(t *testing.T) {
		t.Parallel()

		certPath, _ := generateTestCert(t, time.Now().Add(time.Hour))
		_, keyPath := generateTestCert(t, time.Now().Add(time.Hour))

		_, _, err := LoadFromFiles(certPath, keyPath)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrKeyMismatch)
	})

	t.Run("PKCS1 RSA key", func(t *testing.T) {
		t.Parallel()

		// Generate an RSA key and encode it as PKCS#1 ("RSA PRIVATE KEY").
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)

		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(99),
			Subject:      pkix.Name{CommonName: "rsa-test"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
		}

		certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &rsaKey.PublicKey, rsaKey)
		require.NoError(t, err)

		dir := t.TempDir()
		certPath := filepath.Join(dir, "cert.pem")
		keyPath := filepath.Join(dir, "key.pem")

		certFile, err := os.Create(certPath)
		require.NoError(t, err)
		require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
		require.NoError(t, certFile.Close())

		// PKCS#1 encoding — the legacy "RSA PRIVATE KEY" PEM type.
		pkcs1DER := x509.MarshalPKCS1PrivateKey(rsaKey)

		keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		require.NoError(t, err)
		require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1DER}))
		require.NoError(t, keyFile.Close())

		cert, signer, loadErr := LoadFromFiles(certPath, keyPath)
		require.NoError(t, loadErr)
		assert.NotNil(t, cert)
		assert.NotNil(t, signer)
	})
}

// ---------------------------------------------------------------------------
// TestLoadFromFiles_ValidCertInvalidKeyPEM — L-CERT-3
// Covers the specific combination: cert file is valid PEM, key file exists but
// contains non-PEM data (not merely a missing/unreadable file).
// ---------------------------------------------------------------------------

func TestLoadFromFiles_ValidCertInvalidKeyPEM(t *testing.T) {
	t.Parallel()

	// Generate a valid cert; discard its key — we will supply a broken key file.
	certPath, _ := generateTestCert(t, time.Now().Add(time.Hour))

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")

	// Key file exists and is readable, but contains no PEM block at all.
	require.NoError(t, os.WriteFile(keyPath, []byte("this is not pem data"), 0o600))

	_, _, err := LoadFromFiles(certPath, keyPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoPEMBlock,
		"valid cert + non-PEM key must return ErrNoPEMBlock, got: %s", err)
}

// ---------------------------------------------------------------------------
// 2. TestConcurrentRotateAndRead
// ---------------------------------------------------------------------------

func TestConcurrentRotateAndRead(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	// Pre-load a cert so readers have something to work with from the start.
	certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))
	initialCert, initialSigner, err := LoadFromFiles(certPath, keyPath)
	require.NoError(t, err)
	require.NoError(t, m.Rotate(initialCert, initialSigner))

	const goroutines = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()

			if i%2 == 0 {
				// Writers: rotate with a fresh cert.
				cp, kp := generateTestCert(t, time.Now().Add(365*24*time.Hour))
				c, s, loadErr := LoadFromFiles(cp, kp)
				if loadErr != nil {
					return
				}
				// Ignore the error — another goroutine may have already rotated.
				_ = m.Rotate(c, s)
			} else {
				// Readers: call all read methods.
				_ = m.GetCertificate()
				_ = m.GetSigner()
				_ = m.PublicKey()
				_ = m.ExpiresAt()
				_ = m.DaysUntilExpiry()
				_ = m.TLSCertificate()
			}
		}(i)
	}

	wg.Wait()

	// After all goroutines finish the manager must still be in a valid state.
	assert.NotNil(t, m.GetCertificate())
}

// ---------------------------------------------------------------------------
// 3. TestRotate_AtomicityOnError
// ---------------------------------------------------------------------------

func TestRotate_AtomicityOnError(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	// State before: everything nil.
	require.Nil(t, m.GetCertificate())
	require.Nil(t, m.GetSigner())

	// Attempt to rotate with an expired cert (should fail).
	certPath, keyPath := generateTestCert(t, time.Now().Add(-time.Hour))
	expiredCert, expiredSigner, loadErr := LoadFromFiles(certPath, keyPath)
	require.NoError(t, loadErr)

	rotateErr := m.Rotate(expiredCert, expiredSigner)
	require.ErrorIs(t, rotateErr, ErrExpired)

	// State must be unchanged — still nil.
	assert.Nil(t, m.GetCertificate(), "GetCertificate must remain nil after failed Rotate")
	assert.Nil(t, m.GetSigner(), "GetSigner must remain nil after failed Rotate")
}

// ---------------------------------------------------------------------------
// 4. TestNewManager_WithCert_StrongAssertions
// ---------------------------------------------------------------------------

func TestNewManager_WithCert_StrongAssertions(t *testing.T) {
	t.Parallel()

	notAfter := time.Now().Add(365 * 24 * time.Hour)
	certPath, keyPath := generateTestCert(t, notAfter)

	m, err := NewManager(certPath, keyPath)
	require.NoError(t, err)

	cert := m.GetCertificate()
	require.NotNil(t, cert)
	assert.Equal(t, "test", cert.Subject.CommonName)

	// ExpiresAt should be close to the notAfter we passed in.
	expiresAt := m.ExpiresAt()
	assert.False(t, expiresAt.IsZero())
	assert.WithinDuration(t, notAfter, expiresAt, 2*time.Second)

	// PublicKey from manager must match the signer's public key.
	signer := m.GetSigner()
	require.NotNil(t, signer)

	managerPub := m.PublicKey()
	require.NotNil(t, managerPub)
	assert.True(t, publicKeysMatch(managerPub, signer.Public()),
		"PublicKey() must match GetSigner().Public()")
}

// ---------------------------------------------------------------------------
// 5. TestRotate_Success_StrongAssertions
// ---------------------------------------------------------------------------

func TestRotate_Success_StrongAssertions(t *testing.T) {
	t.Parallel()

	m, err := NewManager("", "")
	require.NoError(t, err)

	certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))
	newCert, newSigner, err := LoadFromFiles(certPath, keyPath)
	require.NoError(t, err)

	require.NoError(t, m.Rotate(newCert, newSigner))

	// The certificate stored in the manager must be the exact one we rotated in
	// (same SerialNumber, since generateTestCert always uses serial 1).
	got := m.GetCertificate()
	require.NotNil(t, got)
	assert.Equal(t, newCert.SerialNumber, got.SerialNumber,
		"GetCertificate().SerialNumber must match the rotated cert")

	// Signer and PublicKey must also be updated.
	gotSigner := m.GetSigner()
	require.NotNil(t, gotSigner)
	assert.True(t, publicKeysMatch(newSigner.Public(), gotSigner.Public()),
		"GetSigner().Public() must match the rotated signer")

	gotPub := m.PublicKey()
	require.NotNil(t, gotPub)
	assert.True(t, publicKeysMatch(newCert.PublicKey, gotPub),
		"PublicKey() must match the rotated cert's public key")
}

// ---------------------------------------------------------------------------
// 6. TestLoadFromFiles_CertificateChain
// ---------------------------------------------------------------------------

func TestLoadFromFiles_CertificateChain(t *testing.T) {
	t.Parallel()

	// Generate a "leaf" key and cert.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, leafTmpl, &leafKey.PublicKey, leafKey)
	require.NoError(t, err)

	// Generate a separate "intermediate" cert (self-signed, just for chain testing).
	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	intermediateTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "intermediate"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTmpl, intermediateTmpl, &intermediateKey.PublicKey, intermediateKey)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "chain.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Write both certs into a single PEM file: leaf first, then intermediate.
	certFile, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER}))
	require.NoError(t, certFile.Close())

	// Write the leaf key.
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	require.NoError(t, err)

	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}))
	require.NoError(t, keyFile.Close())

	// LoadFromFiles should succeed and return the leaf cert.
	cert, signer, err := LoadFromFiles(certPath, keyPath)
	require.NoError(t, err)
	assert.Equal(t, "leaf", cert.Subject.CommonName)
	assert.NotNil(t, signer)

	// Load via NewManager so we can inspect TLSCertificate() chain.
	m, err := NewManager(certPath, keyPath)
	require.NoError(t, err)

	tlsCert := m.TLSCertificate()
	// The chain should contain both DER blocks: leaf + intermediate.
	assert.Len(t, tlsCert.Certificate, 2, "TLSCertificate chain must contain leaf + intermediate")
	assert.Equal(t, leafDER, tlsCert.Certificate[0], "first chain entry must be the leaf DER")
	assert.Equal(t, intermediateDER, tlsCert.Certificate[1], "second chain entry must be the intermediate DER")
}

// ---------------------------------------------------------------------------
// 7. TestTLSCertificate_And_GetCertificateFunc
// ---------------------------------------------------------------------------

func TestTLSCertificate_And_GetCertificateFunc(t *testing.T) {
	t.Parallel()

	t.Run("empty manager returns empty TLSCertificate", func(t *testing.T) {
		t.Parallel()

		m, err := NewManager("", "")
		require.NoError(t, err)

		tlsCert := m.TLSCertificate()
		assert.Nil(t, tlsCert.Certificate, "empty manager must return zero tls.Certificate")
		assert.Nil(t, tlsCert.Leaf)
	})

	t.Run("loaded manager returns valid tls.Certificate", func(t *testing.T) {
		t.Parallel()

		certPath, keyPath := generateTestCert(t, time.Now().Add(24*time.Hour))
		m, err := NewManager(certPath, keyPath)
		require.NoError(t, err)

		tlsCert := m.TLSCertificate()
		require.NotNil(t, tlsCert.Certificate, "TLSCertificate must have DER chain")
		require.NotNil(t, tlsCert.PrivateKey, "TLSCertificate must have PrivateKey")
		require.NotNil(t, tlsCert.Leaf, "TLSCertificate must have Leaf")

		assert.Equal(t, "test", tlsCert.Leaf.Subject.CommonName)

		// The DER bytes in the chain must decode to the same cert.
		parsed, parseErr := x509.ParseCertificate(tlsCert.Certificate[0])
		require.NoError(t, parseErr)
		assert.Equal(t, tlsCert.Leaf.SerialNumber, parsed.SerialNumber)
	})

	t.Run("GetCertificateFunc returns cert for loaded manager", func(t *testing.T) {
		t.Parallel()

		certPath, keyPath := generateTestCert(t, time.Now().Add(24*time.Hour))
		m, err := NewManager(certPath, keyPath)
		require.NoError(t, err)

		fn := m.GetCertificateFunc()
		require.NotNil(t, fn)

		tlsCert, callErr := fn(&tls.ClientHelloInfo{})
		require.NoError(t, callErr)
		require.NotNil(t, tlsCert)
		assert.NotNil(t, tlsCert.Leaf)
	})

	t.Run("GetCertificateFunc returns error when no cert loaded", func(t *testing.T) {
		t.Parallel()

		m, err := NewManager("", "")
		require.NoError(t, err)

		fn := m.GetCertificateFunc()
		require.NotNil(t, fn)

		_, callErr := fn(&tls.ClientHelloInfo{})
		require.Error(t, callErr)
		assert.ErrorIs(t, callErr, ErrCertRequired)
	})
}

// ---------------------------------------------------------------------------
// 8. TestLoadFromFiles_FilePermissions
// ---------------------------------------------------------------------------

func TestLoadFromFiles_FilePermissions(t *testing.T) {
	t.Parallel()

	// We need a valid cert file; the cert itself is readable at any permission.
	certPath, _ := generateTestCert(t, time.Now().Add(time.Hour))

	// Build a valid key DER separately so we can write it to our own key file.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	// Re-create the cert so it matches this key.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "perm-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	certFile, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, certFile.Close())

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	t.Run("0644 key file is rejected", func(t *testing.T) {
		// Write key file with overly permissive 0644 mode.
		keyFile, createErr := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		require.NoError(t, createErr)
		require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
		require.NoError(t, keyFile.Close())

		_, _, loadErr := LoadFromFiles(certPath, keyPath)
		require.Error(t, loadErr)
		assert.True(t, strings.Contains(loadErr.Error(), "permissive"),
			"error must mention permissive mode, got: %s", loadErr)
	})

	t.Run("0600 key file is accepted", func(t *testing.T) {
		// Tighten permissions to 0600.
		require.NoError(t, os.Chmod(keyPath, 0o600))

		cert, signer, loadErr := LoadFromFiles(certPath, keyPath)
		require.NoError(t, loadErr)
		assert.NotNil(t, cert)
		assert.NotNil(t, signer)
	})
}

// ---------------------------------------------------------------------------
// 9. TestNewManager_InvalidCertPath_ErrorSpecificity
// ---------------------------------------------------------------------------

func TestNewManager_InvalidCertPath_ErrorSpecificity(t *testing.T) {
	t.Parallel()

	t.Run("cert file not found", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		_, err := NewManager(
			filepath.Join(dir, "no-cert.pem"),
			filepath.Join(dir, "no-key.pem"),
		)
		require.Error(t, err)
		// The error must reference cert reading, not key reading.
		assert.True(t, strings.Contains(err.Error(), "cert"),
			"error must mention cert, got: %s", err)
	})

	t.Run("key file not found distinguishable from cert not found", func(t *testing.T) {
		t.Parallel()

		// Create a valid cert file but no key file.
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(5),
			Subject:      pkix.Name{CommonName: "test"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
		}

		certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		require.NoError(t, err)

		dir := t.TempDir()
		certPath := filepath.Join(dir, "cert.pem")

		certFile, err := os.Create(certPath)
		require.NoError(t, err)
		require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
		require.NoError(t, certFile.Close())

		_, certErrOnly := NewManager(
			filepath.Join(dir, "no-cert.pem"),
			filepath.Join(dir, "no-key.pem"),
		)
		require.Error(t, certErrOnly)

		_, keyErrOnly := NewManager(certPath, filepath.Join(dir, "no-key.pem"))
		require.Error(t, keyErrOnly)

		// The two errors must not be identical — they should reference different things.
		assert.NotEqual(t, certErrOnly.Error(), keyErrOnly.Error(),
			"missing-cert and missing-key errors should be distinguishable")

		assert.False(t,
			strings.Contains(keyErrOnly.Error(), "read cert"),
			"key-missing error must not blame cert reading, got: %s", keyErrOnly)
		assert.True(t,
			strings.Contains(keyErrOnly.Error(), "key"),
			"key-missing error must mention key, got: %s", keyErrOnly)
	})
}

// ---------------------------------------------------------------------------
// 10. TestNilManager_SubTests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// TestLoadFromFilesWithChain — verify chain is returned correctly
// ---------------------------------------------------------------------------

func TestLoadFromFilesWithChain(t *testing.T) {
	t.Parallel()

	certPath, keyPath := generateTestCert(t, time.Now().Add(365*24*time.Hour))

	cert, signer, chain, err := LoadFromFilesWithChain(certPath, keyPath)
	require.NoError(t, err)
	assert.NotNil(t, cert, "certificate must not be nil")
	assert.NotNil(t, signer, "signer must not be nil")
	require.NotNil(t, chain, "chain must not be nil")
	require.Len(t, chain, 1, "single self-signed cert should produce a 1-element chain")
	assert.Equal(t, cert.Raw, chain[0],
		"chain[0] must equal the leaf certificate's raw DER bytes")
}

func TestNilManager_SubTests(t *testing.T) {
	t.Parallel()

	var m *Manager

	t.Run("GetCertificate returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, m.GetCertificate())
	})

	t.Run("GetSigner returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, m.GetSigner())
	})

	t.Run("PublicKey returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, m.PublicKey())
	})

	t.Run("ExpiresAt returns zero time", func(t *testing.T) {
		t.Parallel()
		assert.True(t, m.ExpiresAt().IsZero())
	})

	t.Run("DaysUntilExpiry returns -1", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, -1, m.DaysUntilExpiry())
	})

	t.Run("TLSCertificate returns empty on nil receiver", func(t *testing.T) {
		t.Parallel()
		// TLSCertificate is nil-receiver-safe: the nil guard precedes the
		// RLock call, consistent with every other Manager method.
		tlsCert := m.TLSCertificate()
		assert.Equal(t, tls.Certificate{}, tlsCert)
	})
}
