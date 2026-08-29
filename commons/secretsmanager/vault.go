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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	vaultapi "github.com/hashicorp/vault/api"
)

// DefaultVaultMount is the KV v2 mount used when VaultConfig leaves Mount
// empty. It matches the mount a stock Vault enables at `secret/`.
const DefaultVaultMount = "secret"

// defaultVaultTimeout bounds a single credential round-trip. A credential read
// sits in front of an outbound money-path call, so it must fail rather than
// hang the caller behind an unreachable vault.
const defaultVaultTimeout = 10 * time.Second

// VaultConfig configures the Vault KV v2 custody backend.
//
// Fields left empty fall back to Vault's own environment convention
// (VAULT_ADDR, VAULT_TOKEN, VAULT_CACERT, VAULT_NAMESPACE, ...), so an operator
// can configure the backend the way every other Vault client in their estate is
// configured instead of learning a Lerian-specific spelling.
type VaultConfig struct {
	// Address is the Vault base URL. Empty falls back to VAULT_ADDR.
	Address string

	// Token authenticates this client. Empty falls back to VAULT_TOKEN.
	//
	// A static token is the simplest posture, not the best one: a deployment
	// that wants AppRole or Kubernetes auth (with the token renewal those
	// imply) should authenticate its own *vaultapi.Client and hand it to
	// NewVaultClientFrom, which keeps the token lifecycle where the
	// deployment can see it rather than hidden inside this package.
	Token string

	// Mount is the KV v2 mount path. Empty means DefaultVaultMount.
	Mount string

	// Namespace selects a Vault Enterprise namespace. Empty falls back to
	// VAULT_NAMESPACE, and then to the root namespace.
	Namespace string

	// Timeout bounds one request. Zero means defaultVaultTimeout.
	Timeout time.Duration
}

// VaultClient reads credential material from Vault's KV v2 engine while
// satisfying SecretsManagerClient, so every reader in this package
// (GetM2MCredentials, GetExternalCredentials, GetExternalCredentialsByReference)
// works over Vault with no change to its signature or its path convention.
//
// The reference is used VERBATIM as the KV v2 secret path beneath the
// configured mount. Nothing about it is re-derived here — not the tenant, not
// the application, and in particular not the environment segment, which the
// reference already carries and which
// ParseExternalCredentialReference validates against a trusted scope before a
// read ever reaches this type. A backend that re-interpreted the environment
// would let a reference written under one environment resolve under another,
// which is precisely the cross-environment read that scope binding forbids.
type VaultClient struct {
	api   *vaultapi.Client
	mount string
}

// NewVaultClient builds a Vault-backed reader from configuration, using a
// static token (or VAULT_TOKEN) to authenticate.
func NewVaultClient(cfg VaultConfig) (*VaultClient, error) {
	apiConfig := vaultapi.DefaultConfig()
	if apiConfig.Error != nil {
		return nil, fmt.Errorf("configure vault client: %w", apiConfig.Error)
	}

	// An unset address keeps Vault's own convention (VAULT_ADDR, then
	// https://127.0.0.1:8200). Overriding that would invent a Lerian-specific
	// rule for a setting every Vault tool in the operator's estate already
	// resolves the same way.
	if address := strings.TrimSpace(cfg.Address); address != "" {
		apiConfig.Address = address
	}

	apiConfig.Timeout = defaultVaultTimeout
	if cfg.Timeout > 0 {
		apiConfig.Timeout = cfg.Timeout
	}

	client, err := vaultapi.NewClient(apiConfig)
	if err != nil {
		return nil, fmt.Errorf("build vault client: %w", err)
	}

	if token := strings.TrimSpace(cfg.Token); token != "" {
		client.SetToken(token)
	}

	if client.Token() == "" {
		return nil, fmt.Errorf("%w: vault token is required (set Token or VAULT_TOKEN)", ErrBackendMisconfigured)
	}

	if namespace := strings.TrimSpace(cfg.Namespace); namespace != "" {
		client.SetNamespace(namespace)
	}

	return NewVaultClientFrom(client, cfg.Mount)
}

// NewVaultClientFrom wraps an already-authenticated Vault client. Use it when
// the deployment owns the auth method and token renewal — AppRole, Kubernetes
// service-account login — instead of a static token.
func NewVaultClientFrom(client *vaultapi.Client, mount string) (*VaultClient, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: vault client is required", ErrBackendMisconfigured)
	}

	cleanMount := strings.Trim(strings.TrimSpace(mount), "/")
	if cleanMount == "" {
		cleanMount = DefaultVaultMount
	}

	return &VaultClient{api: client, mount: cleanMount}, nil
}

// apiClient validates the exported client's otherwise-unconstructible state.
// A consumer can still create the zero value, so every operation must reject it
// rather than dereference its nil API handle.
func (client *VaultClient) apiClient() (*vaultapi.Client, string, error) {
	if client == nil || client.api == nil {
		return nil, "", fmt.Errorf("%w: vault client is required", ErrBackendMisconfigured)
	}

	return client.api, client.mount, nil
}

// GetSecretValue reads the secret at params.SecretId from Vault KV v2 and
// returns it in the shape SecretsManagerClient promises.
//
// The KV v2 key/value pairs are re-marshalled into the single JSON document the
// readers in this package expect, VERBATIM: values are not coerced, so a
// document written through this package's writer round-trips unchanged, and a
// document an operator wrote by hand (`vault kv put`) is read back exactly as
// stored. A value that is not a string therefore reaches the reader as a
// non-string, and the reader rejects or ignores it on its own rules — the same
// outcome the AWS backend produces for the same document.
func (client *VaultClient) GetSecretValue(
	ctx context.Context,
	params *awssm.GetSecretValueInput,
	optFns ...func(*awssm.Options),
) (*awssm.GetSecretValueOutput, error) {
	api, mount, err := client.apiClient()
	if err != nil {
		return nil, err
	}

	if len(optFns) > 0 {
		return nil, fmt.Errorf("%w: backend %q", ErrBackendOptionsUnsupported, BackendVault)
	}

	if params == nil || params.SecretId == nil || strings.TrimSpace(*params.SecretId) == "" {
		return nil, fmt.Errorf("%w: secret id is required", ErrBackendMisconfigured)
	}

	secretPath := strings.Trim(strings.TrimSpace(*params.SecretId), "/")

	secret, err := api.KVv2(mount).Get(ctx, secretPath)
	if err != nil {
		return nil, classifyVaultError(err)
	}

	// A soft-deleted version answers with no error and no data. That is an
	// absence, and it must classify as one so callers keep their
	// not-found branch instead of failing closed on a live infrastructure.
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("%w: the current version holds no data (deleted or destroyed)", ErrBackendSecretNotFound)
	}

	document, err := json.Marshal(secret.Data)
	if err != nil {
		return nil, fmt.Errorf("encode vault secret payload: %w", err)
	}

	return &awssm.GetSecretValueOutput{
		Name:         aws.String(secretPath),
		SecretString: aws.String(string(document)),
	}, nil
}

// classifyVaultError maps Vault transport failures onto the backend-neutral
// sentinels, so a Vault absence is indistinguishable from an AWS absence to
// every reader above.
func classifyVaultError(err error) error {
	if errors.Is(err, vaultapi.ErrSecretNotFound) {
		return ErrBackendSecretNotFound
	}

	var responseErr *vaultapi.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		switch responseErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: vault response status %d", ErrBackendSecretNotFound, responseErr.StatusCode)
		case http.StatusForbidden, http.StatusUnauthorized:
			// 403 is Vault's answer both to "no policy grants this path"
			// and to an expired or revoked token; 401 covers a missing
			// one. All three are the caller's access to the vault, not
			// the secret's existence.
			return fmt.Errorf("%w: vault response status %d", ErrBackendAccessDenied, responseErr.StatusCode)
		}
	}

	return newScrubbedVaultError(err)
}

// newScrubbedVaultError keeps Vault's request URL and server-controlled error
// text out of returned errors. Both may contain the tenant-bearing secret path.
func newScrubbedVaultError(err error) error {
	var responseErr *vaultapi.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		return fmt.Errorf("vault request failed with status %d", responseErr.StatusCode)
	}

	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("vault request failed: %w", context.Canceled)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("vault request failed: %w", context.DeadlineExceeded)
	}

	return errors.New("vault request failed")
}
