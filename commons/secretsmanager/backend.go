// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"errors"
	"fmt"
	"strings"
)

// Backend names the custody backend that holds tenant credential material.
//
// The backend is an infrastructure choice, never a semantic one: every backend
// addresses a secret by the SAME reference string this package already builds
// (BuildM2MSecretPath, BuildExternalSecretPath,
// BuildExternalSecretVersionReference), and returns the SAME document shape to
// the readers above. A deployment that moves from AWS to Vault keeps every path
// convention, including the environment segment, byte for byte.
type Backend string

const (
	// BackendAWS custodies credentials in AWS Secrets Manager. It is the
	// default: an empty Backend resolves to this one, so an existing
	// deployment that configures nothing keeps the behaviour it has today.
	BackendAWS Backend = "aws"

	// BackendVault custodies credentials in HashiCorp Vault's KV v2 engine.
	// It exists so a client running outside AWS — the BYOC case — can hold
	// its own credential material without the platform assuming a cloud.
	BackendVault Backend = "vault"
)

// Backend-neutral sentinels. Every backend classifies its own transport
// failures onto these, and the readers in this package translate them into the
// M2M/External sentinels callers already branch on.
//
// This indirection is what keeps the two backends interchangeable. Callers such
// as a per-tenant credential store decide "fall back to static configuration"
// on ErrExternalCredentialsNotFound; if a Vault 404 arrived as a generic
// retrieval failure instead, that fallback would never fire and the caller
// would fail closed forever on a secret that is merely absent.
var (
	// ErrBackendSecretNotFound reports that the backend holds no secret at the
	// reference. It is an absence, not a fault.
	ErrBackendSecretNotFound = errors.New("custody backend: secret not found at reference")

	// ErrBackendAccessDenied reports that the backend refused the caller's
	// identity — a missing policy, an expired token, revoked credentials.
	ErrBackendAccessDenied = errors.New("custody backend: access denied")

	// ErrBackendSecretExists reports that a create-only write lost to material
	// already present at the reference. Writes are immutable by contract:
	// rotation allocates a NEW versioned reference and never overwrites.
	ErrBackendSecretExists = errors.New("custody backend: secret already exists at reference")

	// ErrBackendUnknown reports a backend name that is neither aws nor vault.
	// It is deliberately an error rather than a fallback to the default: a
	// typo in configuration must stop the process, never silently custody a
	// tenant's credentials somewhere the operator did not choose.
	ErrBackendUnknown = errors.New("custody backend: unknown backend")

	// ErrBackendMisconfigured reports that the selected backend was chosen
	// without the dependency it needs to work.
	ErrBackendMisconfigured = errors.New("custody backend: selected backend is not configured")

	// ErrBackendOptionsUnsupported reports that a caller passed AWS SDK request
	// options to a backend that is not AWS. It fails closed rather than
	// ignoring them: an option a backend cannot honour has changed the meaning
	// of the request, and silently dropping it on a credential read is exactly
	// the kind of divergence this package exists to prevent.
	ErrBackendOptionsUnsupported = errors.New("custody backend: AWS request options are not supported by this backend")
)

// ParseBackend resolves a configured backend name. An empty value resolves to
// BackendAWS so existing deployments keep their current behaviour without
// setting anything; any other unrecognised value is refused.
func ParseBackend(raw string) (Backend, error) {
	switch backend := Backend(strings.ToLower(strings.TrimSpace(raw))); backend {
	case "":
		return BackendAWS, nil
	case BackendAWS, BackendVault:
		return backend, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrBackendUnknown, raw)
	}
}

// Config selects the custody backend and carries the settings the non-default
// backends need. Its zero value selects AWS, which is the current behaviour.
type Config struct {
	// Backend selects custody. Empty means BackendAWS.
	Backend Backend

	// Vault configures the Vault backend. It is required when Backend is
	// BackendVault and ignored otherwise.
	Vault VaultConfig
}

// NewReader builds the credential reader for the configured backend.
//
// awsClient is the caller's own AWS Secrets Manager client and is used ONLY
// when the backend is AWS — this package does not build AWS configuration on
// the caller's behalf, because the caller already owns that identity.
//
// There is no fallback anywhere in this function by construction: selecting
// Vault and failing to reach it yields an error, never an AWS client; selecting
// AWS without a client yields an error, never a Vault client. A credential
// backend that quietly answers from somewhere other than where the operator
// pointed it is a money-path defect, not a resilience feature.
func (cfg Config) NewReader(awsClient SecretsManagerClient) (SecretsManagerClient, error) {
	backend, err := ParseBackend(string(cfg.Backend))
	if err != nil {
		return nil, err
	}

	switch backend {
	case BackendVault:
		// Assigned and returned explicitly rather than `return
		// NewVaultClient(...)`: on failure that form hands back a typed-nil
		// *VaultClient wrapped in a NON-nil interface, so a caller checking
		// the returned reader for nil would sail past a failed construction
		// and only discover it on the first credential read.
		client, vaultErr := NewVaultClient(cfg.Vault)
		if vaultErr != nil {
			return nil, vaultErr
		}

		return client, nil
	case BackendAWS:
		if isNilInterface(awsClient) {
			return nil, fmt.Errorf("%w: backend %q requires an AWS Secrets Manager client", ErrBackendMisconfigured, BackendAWS)
		}

		return awsClient, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrBackendUnknown, backend)
	}
}

// NewWriter builds the credential writer for the configured backend. It obeys
// the same no-fallback rule as NewReader, and awsClient is likewise consulted
// only when the backend is AWS.
func (cfg Config) NewWriter(awsClient AWSSecretsManagerWriter) (SecretWriter, error) {
	backend, err := ParseBackend(string(cfg.Backend))
	if err != nil {
		return nil, err
	}

	switch backend {
	case BackendVault:
		client, vaultErr := NewVaultClient(cfg.Vault)
		if vaultErr != nil {
			return nil, vaultErr
		}

		return NewVaultSecretWriter(client), nil
	case BackendAWS:
		if isNilInterface(awsClient) {
			return nil, fmt.Errorf("%w: backend %q requires an AWS Secrets Manager client", ErrBackendMisconfigured, BackendAWS)
		}

		return NewAWSSecretWriter(awsClient), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrBackendUnknown, backend)
	}
}
