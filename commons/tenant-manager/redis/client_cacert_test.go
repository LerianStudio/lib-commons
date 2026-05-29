//go:build unit

package redis

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateSelfSignedCAPEM creates an in-memory self-signed CA certificate and
// returns its PEM-encoded bytes. Keeping the cert generation inside the test
// suite avoids embedding any public CA bundle and keeps the table-driven tests
// deterministic.
func generateSelfSignedCAPEM(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "lib-commons-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestBuildOptions_CACertBase64_EmptyWithTLS_PreservesSystemTrust(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host:         "redis.example.com",
		TLS:          true,
		CACertBase64: "",
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
	// Regression assertion: empty CACertBase64 MUST leave RootCAs nil so the
	// Go runtime falls back to the system trust pool. This guards against
	// accidental future initializations of an empty CertPool that would
	// silently break system-trust callers.
	assert.Nil(t, opts.TLSConfig.RootCAs, "RootCAs must be nil when CACertBase64 is empty")
}

func TestBuildOptions_CACertBase64_ValidPEM_PopulatesRootCAs(t *testing.T) {
	t.Parallel()

	pemBytes := generateSelfSignedCAPEM(t)
	encoded := base64.StdEncoding.EncodeToString(pemBytes)

	cfg := TenantPubSubRedisConfig{
		Host:         "redis.example.com",
		TLS:          true,
		CACertBase64: encoded,
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
	require.NotNil(t, opts.TLSConfig.RootCAs, "RootCAs must be populated when CACertBase64 is set")
	// x509.CertPool exposes Subjects() (deprecated but still functional) — use
	// Equal/empty assertions on a parsed cert to confirm the decoded PEM was
	// added rather than relying on internal cert pool state.
	parsed, perr := parseFirstCert(pemBytes)
	require.NoError(t, perr)

	secondPool := x509.NewCertPool()
	secondPool.AddCert(parsed)
	assert.True(t, opts.TLSConfig.RootCAs.Equal(secondPool), "RootCAs should contain the supplied self-signed CA")
}

func TestBuildOptions_CACertBase64_InvalidBase64_ReturnsWrappedError(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host:         "redis.example.com",
		TLS:          true,
		CACertBase64: "!!!not-base64!!!",
	}

	opts, err := BuildOptions(cfg)

	require.Error(t, err)
	assert.Nil(t, opts)
	assert.Contains(t, err.Error(), "decode CA cert base64")
}

func TestBuildOptions_CACertBase64_ValidBase64_NotPEM_ReturnsError(t *testing.T) {
	t.Parallel()

	// "hello world" base64-encoded — valid base64, but no PEM blocks.
	cfg := TenantPubSubRedisConfig{
		Host:         "redis.example.com",
		TLS:          true,
		CACertBase64: base64.StdEncoding.EncodeToString([]byte("hello world")),
	}

	opts, err := BuildOptions(cfg)

	require.Error(t, err)
	assert.Nil(t, opts)
	assert.Contains(t, err.Error(), "no PEM blocks found")
}

func TestBuildOptions_CACertBase64_IgnoredWhenTLSDisabled(t *testing.T) {
	t.Parallel()

	pemBytes := generateSelfSignedCAPEM(t)
	encoded := base64.StdEncoding.EncodeToString(pemBytes)

	cfg := TenantPubSubRedisConfig{
		Host:         "redis.example.com",
		TLS:          false,
		CACertBase64: encoded,
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Nil(t, opts.TLSConfig, "TLS disabled must produce nil TLSConfig regardless of CACertBase64")
}

func TestBuildOptions_CACertBase64_TableDriven(t *testing.T) {
	t.Parallel()

	pemBytes := generateSelfSignedCAPEM(t)
	validEncoded := base64.StdEncoding.EncodeToString(pemBytes)
	nonPEMEncoded := base64.StdEncoding.EncodeToString([]byte("not a pem block at all"))

	tests := []struct {
		name             string
		cfg              TenantPubSubRedisConfig
		wantErr          bool
		wantErrSubstr    string
		wantTLSConfigNil bool
		wantRootCAsNil   bool
	}{
		{
			name:             "TLS off, empty CA — backward compat",
			cfg:              TenantPubSubRedisConfig{Host: "h", TLS: false, CACertBase64: ""},
			wantErr:          false,
			wantTLSConfigNil: true,
		},
		{
			name:             "TLS off, CA set — silently ignored",
			cfg:              TenantPubSubRedisConfig{Host: "h", TLS: false, CACertBase64: validEncoded},
			wantErr:          false,
			wantTLSConfigNil: true,
		},
		{
			name:             "TLS on, empty CA — system trust fallback",
			cfg:              TenantPubSubRedisConfig{Host: "h", TLS: true, CACertBase64: ""},
			wantErr:          false,
			wantTLSConfigNil: false,
			wantRootCAsNil:   true,
		},
		{
			name:             "TLS on, valid CA — RootCAs populated",
			cfg:              TenantPubSubRedisConfig{Host: "h", TLS: true, CACertBase64: validEncoded},
			wantErr:          false,
			wantTLSConfigNil: false,
			wantRootCAsNil:   false,
		},
		{
			name:          "TLS on, invalid base64 — wrapped decode error",
			cfg:           TenantPubSubRedisConfig{Host: "h", TLS: true, CACertBase64: "@@@"},
			wantErr:       true,
			wantErrSubstr: "decode CA cert base64",
		},
		{
			name:          "TLS on, valid base64 but not PEM — explicit error",
			cfg:           TenantPubSubRedisConfig{Host: "h", TLS: true, CACertBase64: nonPEMEncoded},
			wantErr:       true,
			wantErrSubstr: "no PEM blocks found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts, err := BuildOptions(tt.cfg)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSubstr)
				assert.Nil(t, opts)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, opts)

			if tt.wantTLSConfigNil {
				assert.Nil(t, opts.TLSConfig)
				return
			}

			require.NotNil(t, opts.TLSConfig)
			assert.Equal(t, uint16(0x0303), opts.TLSConfig.MinVersion) // tls.VersionTLS12

			if tt.wantRootCAsNil {
				assert.Nil(t, opts.TLSConfig.RootCAs)
			} else {
				assert.NotNil(t, opts.TLSConfig.RootCAs)
			}
		})
	}
}

// parseFirstCert decodes the first CERTIFICATE block from a PEM bundle.
func parseFirstCert(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errPEMDecode
	}

	return x509.ParseCertificate(block.Bytes)
}

var errPEMDecode = errPEMDecodeT("pem decode failed")

type errPEMDecodeT string

func (e errPEMDecodeT) Error() string { return string(e) }
