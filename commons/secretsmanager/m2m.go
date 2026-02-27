// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package secretsmanager provides functions for retrieving M2M (machine-to-machine)
// credentials from AWS Secrets Manager.
//
// This package is designed to be self-contained with no dependency on internal packages,
// making it suitable for migration to lib-commons.
//
// # M2M Credentials
//
// M2M credentials are OAuth2 client credentials stored in AWS Secrets Manager
// following the path convention:
//
//	tenants/{env}/{tenantOrgID}/{applicationName}/m2m/{targetService}/credentials
//
// # Usage
//
// A plugin retrieves credentials to authenticate with a product service:
//
//	// Create AWS Secrets Manager client
//	cfg, err := awsconfig.LoadDefaultConfig(ctx)
//	if err != nil {
//	    // handle error
//	}
//	client := secretsmanager.NewFromConfig(cfg)
//
//	// Fetch M2M credentials
//	creds, err := secretsmanager.GetM2MCredentials(ctx, client, "staging", tenantOrgID, "plugin-pix", "ledger")
//	if err != nil {
//	    // handle error
//	}
//
//	// Use credentials to obtain an access token via client_credentials grant
//	// POST creds.TokenURL with grant_type=client_credentials
//	// Authorization: Basic(creds.ClientID, creds.ClientSecret)
//
// # Thread Safety
//
// All functions in this package are safe for concurrent use.
// No package-level mutable state is maintained.
package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	smithy "github.com/aws/smithy-go"
)

// Sentinel errors for M2M credential operations.
var (
	// ErrM2MCredentialsNotFound is returned when M2M credentials cannot be found at the expected path.
	ErrM2MCredentialsNotFound = errors.New("M2M credentials not found")

	// ErrM2MVaultAccessDenied is returned when access to the vault is denied (missing IAM permissions or expired tokens).
	ErrM2MVaultAccessDenied = errors.New("vault access denied")

	// ErrM2MRetrievalFailed is returned when M2M credential retrieval fails due to infrastructure issues.
	ErrM2MRetrievalFailed = errors.New("failed to retrieve M2M credentials")

	// ErrM2MUnmarshalFailed is returned when the secret value cannot be deserialized into M2MCredentials.
	ErrM2MUnmarshalFailed = errors.New("failed to unmarshal M2M credentials")

	// ErrM2MInvalidInput is returned when required input parameters are missing.
	ErrM2MInvalidInput = errors.New("invalid input")

	// ErrM2MInvalidCredentials is returned when retrieved credentials are incomplete (missing required fields).
	ErrM2MInvalidCredentials = errors.New("incomplete M2M credentials")
)

// M2MCredentials holds credentials retrieved from the Secret Vault.
// These credentials are used for OAuth2 client_credentials grant
// to authenticate plugins with product services.
type M2MCredentials struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	TokenURL     string `json:"tokenUrl"`
}

// SecretsManagerClient abstracts AWS Secrets Manager operations.
// This interface allows for easier testing with mocks.
type SecretsManagerClient interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// GetM2MCredentials fetches M2M credentials from AWS Secrets Manager.
//
// Parameters:
//   - ctx: context for cancellation and tracing
//   - client: AWS Secrets Manager client (must not be nil)
//   - env: deployment environment (e.g., "staging", "production"); empty string is accepted for backward compatibility
//   - tenantOrgID: resolved from request context (JWT owner claim); must not be empty
//   - applicationName: the plugin name (e.g., "plugin-pix"); must not be empty
//   - targetService: the product service name (e.g., "ledger"); must not be empty
//
// Path convention:
//
//	tenants/{env}/{tenantOrgID}/{applicationName}/m2m/{targetService}/credentials
//
// Returns descriptive errors when:
//   - client is nil
//   - required parameters are missing
//   - secret not found at path
//   - vault credentials are missing or expired
//   - secret value is not valid JSON
//
// Safe for concurrent use (no shared mutable state).
func GetM2MCredentials(ctx context.Context, client SecretsManagerClient, env, tenantOrgID, applicationName, targetService string) (*M2MCredentials, error) {
	// Validate inputs
	if client == nil {
		return nil, fmt.Errorf("%w: client is required", ErrM2MInvalidInput)
	}

	if tenantOrgID == "" {
		return nil, fmt.Errorf("%w: tenantOrgID is required", ErrM2MInvalidInput)
	}

	if applicationName == "" {
		return nil, fmt.Errorf("%w: applicationName is required", ErrM2MInvalidInput)
	}

	if targetService == "" {
		return nil, fmt.Errorf("%w: targetService is required", ErrM2MInvalidInput)
	}

	// Build the secret path
	secretPath := buildM2MSecretPath(env, tenantOrgID, applicationName, targetService)

	// Fetch the secret from AWS Secrets Manager
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretPath),
	}

	output, err := client.GetSecretValue(ctx, input)
	if err != nil {
		return nil, classifyAWSError(err, secretPath)
	}

	// Extract the secret string
	var secretValue string
	if output != nil && output.SecretString != nil {
		secretValue = *output.SecretString
	}

	// Unmarshal the JSON credentials
	var creds M2MCredentials
	if err := json.Unmarshal([]byte(secretValue), &creds); err != nil {
		return nil, fmt.Errorf("%w: path=%s: %v", ErrM2MUnmarshalFailed, secretPath, err)
	}

	// Validate required credential fields
	var missing []string
	if creds.ClientID == "" {
		missing = append(missing, "clientId")
	}

	if creds.ClientSecret == "" {
		missing = append(missing, "clientSecret")
	}

	if creds.TokenURL == "" {
		missing = append(missing, "tokenUrl")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: path=%s: missing fields: %s", ErrM2MInvalidCredentials, secretPath, strings.Join(missing, ", "))
	}

	return &creds, nil
}

// buildM2MSecretPath constructs the secret path for M2M credentials.
//
// Format: tenants/{env}/{tenantOrgID}/{applicationName}/m2m/{targetService}/credentials
//
// When env is empty, the path omits the environment segment for backward compatibility:
//
//	tenants/{tenantOrgID}/{applicationName}/m2m/{targetService}/credentials
func buildM2MSecretPath(env, tenantOrgID, applicationName, targetService string) string {
	envPrefix := ""
	if env != "" {
		envPrefix = env + "/"
	}

	return fmt.Sprintf("tenants/%s%s/%s/m2m/%s/credentials", envPrefix, tenantOrgID, applicationName, targetService)
}

// classifyAWSError maps AWS SDK errors to domain-specific sentinel errors.
func classifyAWSError(err error, secretPath string) error {
	var notFoundErr *smtypes.ResourceNotFoundException
	if errors.As(err, &notFoundErr) {
		return fmt.Errorf("%w at path: %s", ErrM2MCredentialsNotFound, secretPath)
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDeniedException", "ExpiredTokenException":
			return fmt.Errorf("%w: %v", ErrM2MVaultAccessDenied, err)
		}
	}

	return fmt.Errorf("%w: path=%s: %v", ErrM2MRetrievalFailed, secretPath, err)
}
