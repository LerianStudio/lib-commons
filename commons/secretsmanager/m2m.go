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
//	// Post to the token endpoint with grant_type=client_credentials
//	// Authorization: Basic(creds.ClientID, creds.ClientSecret)
//
// # Thread Safety
//
// All functions in this package are safe for concurrent use.
// No package-level mutable state is maintained.
package secretsmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

	// ErrM2MBinarySecretNotSupported is returned when the secret is stored as binary data rather than a string.
	ErrM2MBinarySecretNotSupported = errors.New("binary secrets are not supported for M2M credentials")

	// ErrM2MInvalidPathSegment is returned when a path segment contains path traversal characters.
	ErrM2MInvalidPathSegment = errors.New("invalid path segment")
)

// validatePathSegment checks that a path segment is safe for use in secret paths.
// It rejects segments containing path traversal characters (/, .., \) and
// trims leading/trailing whitespace.
func validatePathSegment(name, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%w: %s is required", ErrM2MInvalidInput, name)
	}

	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") || strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("%w: %s contains path traversal characters", ErrM2MInvalidPathSegment, name)
	}

	return trimmed, nil
}

// redactPath returns a safe representation of a secret path for error messages.
// It includes only the last path segment and a truncated hash of the full path.
func redactPath(secretPath string) string {
	parts := strings.Split(secretPath, "/")
	lastSegment := parts[len(parts)-1]

	h := sha256.Sum256([]byte(secretPath))
	shortHash := hex.EncodeToString(h[:4]) // 8 hex chars

	return fmt.Sprintf(".../%s [%s]", lastSegment, shortHash)
}

// isNilInterface returns true if the interface value is nil or holds a typed nil.
func isNilInterface(i any) bool {
	if i == nil {
		return true
	}

	v := reflect.ValueOf(i)

	return v.Kind() == reflect.Ptr && v.IsNil()
}

// M2MCredentials holds credentials retrieved from the Secret Vault.
// These credentials are used for OAuth2 client_credentials grant
// to authenticate plugins with product services.
type M2MCredentials struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"` // #nosec G117 -- secret payload is intentionally deserialized from AWS Secrets Manager and redacted by String/GoString
}

// String redacts secret material from formatted output.
func (c M2MCredentials) String() string {
	return fmt.Sprintf("M2MCredentials{ClientID:%q, ClientSecret:REDACTED}", c.ClientID)
}

// GoString redacts secret material from Go-syntax formatted output.
func (c M2MCredentials) GoString() string {
	return c.String()
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
	// Validate inputs - check for typed-nil client using reflect
	if isNilInterface(client) {
		return nil, fmt.Errorf("%w: client is required", ErrM2MInvalidInput)
	}

	// Validate and sanitize path segments (trims whitespace, rejects traversal chars)
	cleanTenantOrgID, err := validatePathSegment("tenantOrgID", tenantOrgID)
	if err != nil {
		return nil, err
	}

	cleanAppName, err := validatePathSegment("applicationName", applicationName)
	if err != nil {
		return nil, err
	}

	cleanTargetService, err := validatePathSegment("targetService", targetService)
	if err != nil {
		return nil, err
	}

	// env is optional (empty for backward compat) but must be safe if provided
	cleanEnv := strings.TrimSpace(env)
	if cleanEnv != "" {
		if strings.Contains(cleanEnv, "/") || strings.Contains(cleanEnv, "\\") || strings.Contains(cleanEnv, "..") {
			return nil, fmt.Errorf("%w: env contains path traversal characters", ErrM2MInvalidPathSegment)
		}
	}

	// Build the secret path
	secretPath := buildM2MSecretPath(cleanEnv, cleanTenantOrgID, cleanAppName, cleanTargetService)
	redacted := redactPath(secretPath)

	// Fetch the secret from AWS Secrets Manager
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretPath),
	}

	output, err := client.GetSecretValue(ctx, input)
	if err != nil {
		return nil, classifyAWSError(err, secretPath)
	}

	// Check for binary secret FIRST (before attempting JSON unmarshal)
	if output == nil || output.SecretString == nil {
		return nil, fmt.Errorf("%w: secret at %s is binary or nil", ErrM2MBinarySecretNotSupported, redacted)
	}

	// Unmarshal the JSON credentials
	var creds M2MCredentials
	if err := json.Unmarshal([]byte(*output.SecretString), &creds); err != nil {
		return nil, fmt.Errorf("%w: secret at %s: %w", ErrM2MUnmarshalFailed, redacted, err)
	}

	// Validate required credential fields
	var missing []string
	if creds.ClientID == "" {
		missing = append(missing, "clientId")
	}

	if creds.ClientSecret == "" {
		missing = append(missing, "clientSecret")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: secret at %s: missing fields: %s", ErrM2MInvalidCredentials, redacted, strings.Join(missing, ", "))
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
// Secret paths are redacted in returned errors to prevent information leakage.
func classifyAWSError(err error, secretPath string) error {
	redacted := redactPath(secretPath)

	var notFoundErr *smtypes.ResourceNotFoundException
	if errors.As(err, &notFoundErr) {
		return fmt.Errorf("%w at %s", ErrM2MCredentialsNotFound, redacted)
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDeniedException", "ExpiredTokenException":
			return fmt.Errorf("%w: %w", ErrM2MVaultAccessDenied, err)
		}
	}

	return fmt.Errorf("%w: %s: %w", ErrM2MRetrievalFailed, redacted, err)
}
