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
//	newCert, newKey, err := certificate.LoadFromFiles("new.crt", "new.key")
//	if err != nil {
//	    log.Printf("pre-flight validation failed: %v", err)
//	} else if err := m.Rotate(newCert, newKey); err != nil {
//	    log.Printf("certificate rotation failed: %v", err)
//	}
//
// # Key formats
//
// Private keys are parsed in order: PKCS#8 first, then PKCS#1 (RSA) fallback,
// then EC (SEC 1) fallback. The manager validates that the certificate's public
// key matches the private key at load time to prevent silent misconfiguration.
//
// # Private key file permissions
//
// A private key file must not be group-writable and must grant no permission at
// all to other. The rule is a forbidden-bit mask, not a ceiling mode: owner bits
// are unconstrained, so 0400, 0440, 0600, 0640 and 0740 are accepted, while
// 0620, 0644, 0660 and any other mode carrying group-write or an `other` bit is
// rejected at load time.
//
// Group-READ is permitted on purpose. In Kubernetes, Secret-volume files are
// owned by root, so a non-root container can only read them through a group bit
// granted by the pod's fsGroup. That group is the pod's own supplementary group,
// so group-read does not widen exposure beyond the pod itself. This matches how
// cert-manager and Istio ship key material (0440/0640) and avoids forcing either
// a root workload or an init-container staging step — the latter would copy the
// key into an emptyDir and silently break in-place rotation.
//
// # Nil safety
//
// Read helpers on a nil *Manager ([Manager.GetCertificate], [Manager.GetSigner],
// [Manager.PublicKey], [Manager.ExpiresAt], [Manager.DaysUntilExpiry],
// [Manager.TLSCertificate]) return zero values without panicking.
// [Manager.Rotate] returns [ErrNilManager] on a nil receiver.
// [Manager.GetCertificateFunc] on a nil receiver returns a live closure
// that always returns [ErrNilManager].
package certificate
