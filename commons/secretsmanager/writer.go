// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	vaultapi "github.com/hashicorp/vault/api"
)

// AWSSecretsManagerWriter is the subset of AWS Secrets Manager write operations
// the credential writer needs. It is satisfied by *awssm.Client.
type AWSSecretsManagerWriter interface {
	CreateSecret(ctx context.Context, params *awssm.CreateSecretInput, optFns ...func(*awssm.Options)) (*awssm.CreateSecretOutput, error)
	DeleteSecret(ctx context.Context, params *awssm.DeleteSecretInput, optFns ...func(*awssm.Options)) (*awssm.DeleteSecretOutput, error)
}

// SecretWriter is the custody write port, the counterpart to the read side of
// this package. It exists here rather than in each consumer because credential
// custody is only portable across backends if BOTH halves are: a deployment
// whose reads can move to Vault but whose writes cannot is still an
// AWS deployment.
//
// The contract is create-only on purpose. Rotation allocates a NEW versioned
// reference (see BuildExternalSecretVersionReference) and publishes it; it never
// overwrites material at an existing reference. That is what makes a persisted
// reference a stable capability: whatever a reference resolved to once, it
// resolves to forever, until it is deleted outright.
type SecretWriter interface {
	// CreateSecretString stores secretJSON at secretID, failing with
	// ErrBackendSecretExists when material is already present there.
	//
	// secretJSON must be a JSON object whose members are the credential
	// fields. Backends store it differently — AWS keeps the document opaque,
	// Vault stores its members as native KV pairs so an operator can read and
	// provision them with `vault kv`— but both return the same document to
	// the readers in this package.
	CreateSecretString(ctx context.Context, secretID, secretJSON string) error

	// DeleteSecret permanently removes the material at secretID, with no
	// recovery window. An already-absent secret succeeds: deletion is
	// idempotent so a retried cleanup converges instead of wedging.
	DeleteSecret(ctx context.Context, secretID string) error
}

// awsSecretWriter writes credential material to AWS Secrets Manager.
type awsSecretWriter struct {
	client AWSSecretsManagerWriter
}

// NewAWSSecretWriter builds the AWS-backed writer.
func NewAWSSecretWriter(client AWSSecretsManagerWriter) SecretWriter {
	return &awsSecretWriter{client: client}
}

// CreateSecretString implements the create-only contract on AWS. CreateSecret
// is already create-only: it refuses a name that exists.
func (writer *awsSecretWriter) CreateSecretString(ctx context.Context, secretID, secretJSON string) error {
	if isNilInterface(writer.client) {
		return fmt.Errorf("%w: AWS Secrets Manager client is required", ErrBackendMisconfigured)
	}

	if err := validateWritableSecret(secretID, secretJSON); err != nil {
		return err
	}

	_, err := writer.client.CreateSecret(ctx, &awssm.CreateSecretInput{
		Name:         aws.String(strings.TrimSpace(secretID)),
		SecretString: aws.String(secretJSON),
	})
	if err != nil {
		var exists *smtypes.ResourceExistsException
		if errors.As(err, &exists) {
			return fmt.Errorf("%w at %s", ErrBackendSecretExists, redactPath(secretID))
		}

		if isVaultAccessDeniedError(err) {
			return fmt.Errorf("%w at %s", ErrBackendAccessDenied, redactPath(secretID))
		}

		return fmt.Errorf("create secret at %s: %w", redactPath(secretID), newScrubbedAWSError(err, secretID, redactPath(secretID)))
	}

	return nil
}

// DeleteSecret force-deletes the secret with no recovery window, matching the
// Vault backend, which has no recovery window to offer.
func (writer *awsSecretWriter) DeleteSecret(ctx context.Context, secretID string) error {
	if isNilInterface(writer.client) {
		return fmt.Errorf("%w: AWS Secrets Manager client is required", ErrBackendMisconfigured)
	}

	cleanID := strings.TrimSpace(secretID)
	if cleanID == "" {
		return fmt.Errorf("%w: secret id is required", ErrBackendMisconfigured)
	}

	_, err := writer.client.DeleteSecret(ctx, &awssm.DeleteSecretInput{
		SecretId:                   aws.String(cleanID),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	if err == nil {
		return nil
	}

	var notFound *smtypes.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return nil
	}

	if isVaultAccessDeniedError(err) {
		return fmt.Errorf("%w at %s", ErrBackendAccessDenied, redactPath(cleanID))
	}

	return fmt.Errorf("delete secret at %s: %w", redactPath(cleanID), newScrubbedAWSError(err, cleanID, redactPath(cleanID)))
}

// vaultSecretWriter writes credential material to Vault's KV v2 engine.
type vaultSecretWriter struct {
	client *VaultClient
}

// NewVaultSecretWriter builds the Vault-backed writer over an existing
// VaultClient, so reads and writes share one authenticated connection and one
// mount — they cannot drift onto different vaults.
func NewVaultSecretWriter(client *VaultClient) SecretWriter {
	return &vaultSecretWriter{client: client}
}

// CreateSecretString implements the create-only contract on Vault.
//
// It uses KV v2's check-and-set with version 0, which Vault honours as "write
// only if nothing is here". That is an atomic primitive: reading first to see
// whether the path is free and then writing would leave a window in which two
// concurrent rotations both believe they won, and one tenant's credential would
// silently replace another write's.
func (writer *vaultSecretWriter) CreateSecretString(ctx context.Context, secretID, secretJSON string) error {
	api, mount, err := writer.client.apiClient()
	if err != nil {
		return err
	}

	if err := validateWritableSecret(secretID, secretJSON); err != nil {
		return err
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(secretJSON), &data); err != nil {
		return fmt.Errorf("%w: secret payload must be a JSON object: %w", ErrBackendMisconfigured, err)
	}

	secretPath := strings.Trim(strings.TrimSpace(secretID), "/")

	_, err = api.KVv2(mount).Put(ctx, secretPath, data, vaultapi.WithCheckAndSet(0))
	if err != nil {
		return classifyVaultWriteError(err, secretID)
	}

	return nil
}

// DeleteSecret destroys every version and the metadata at the path. KV v2's
// plain Delete only tombstones the current version, leaving the material
// readable by version — not a deletion of secret material, which is what a
// credential cleanup has to guarantee.
func (writer *vaultSecretWriter) DeleteSecret(ctx context.Context, secretID string) error {
	api, mount, err := writer.client.apiClient()
	if err != nil {
		return err
	}

	cleanID := strings.TrimSpace(secretID)
	if cleanID == "" {
		return fmt.Errorf("%w: secret id is required", ErrBackendMisconfigured)
	}

	err = api.KVv2(mount).DeleteMetadata(ctx, strings.Trim(cleanID, "/"))
	if err == nil {
		return nil
	}

	if errors.Is(classifyVaultError(err), ErrBackendSecretNotFound) {
		return nil
	}

	return classifyVaultWriteError(err, secretID)
}

// validateWritableSecret enforces the boundary contract both writers share.
func validateWritableSecret(secretID, secretJSON string) error {
	if strings.TrimSpace(secretID) == "" {
		return fmt.Errorf("%w: secret id is required", ErrBackendMisconfigured)
	}

	if strings.TrimSpace(secretJSON) == "" {
		return fmt.Errorf("%w: secret payload is required", ErrBackendMisconfigured)
	}

	// Both backends must accept the same payloads, so the narrower backend
	// sets the contract: Vault KV stores key/value members and cannot hold a
	// bare scalar or array. Refusing those here means a payload that writes on
	// AWS also writes on Vault, instead of a deployment discovering the
	// difference the day it migrates.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(secretJSON), &probe); err != nil {
		return fmt.Errorf("%w: secret payload must be a JSON object: %w", ErrBackendMisconfigured, err)
	}

	return nil
}

// classifyVaultWriteError maps Vault write failures onto the backend-neutral
// sentinels.
func classifyVaultWriteError(err error, secretID string) error {
	var responseErr *vaultapi.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		switch responseErr.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			return fmt.Errorf("%w at %s", ErrBackendAccessDenied, redactPath(secretID))
		case http.StatusBadRequest:
			// Vault reports a lost check-and-set as a 400 whose body names
			// the check. There is no distinct status code or error type for
			// it, so the message is the only signal the API offers; a 400
			// that is NOT the CAS check falls through as a generic failure
			// rather than being reported as a benign "already exists".
			if strings.Contains(strings.ToLower(strings.Join(responseErr.Errors, " ")), "check-and-set") {
				return fmt.Errorf("%w at %s", ErrBackendSecretExists, redactPath(secretID))
			}
		}
	}

	return fmt.Errorf("write secret at %s: %w", redactPath(secretID), newScrubbedVaultError(err))
}
