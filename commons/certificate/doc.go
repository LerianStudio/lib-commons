// Package certificate provides a thread-safe TLS certificate manager with hot reload.
//
// The [Manager] loads X.509 certificates and private keys from PEM files, supports
// zero-downtime rotation via [Manager.Rotate], and provides concurrent read access
// through an internal sync.RWMutex.
//
// # Quick start
//
//	m, err := certificate.NewManager("server.crt", "server.key")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Use in TLS config
//	cert := m.GetCertificate()
//	signer := m.GetSigner()
//
//	// Hot-reload without restart
//	newCert, newKey, _ := certificate.LoadFromFiles("new.crt", "new.key")
//	m.Rotate(newCert, newKey)
//
// # Key formats
//
// Private keys are parsed in order: PKCS#8 first, then PKCS#1 fallback.
// The manager validates that the certificate's public key matches the private key
// at load time to prevent silent misconfiguration.
//
// # Nil safety
//
// All methods on a nil *Manager return zero values without panicking.
package certificate
