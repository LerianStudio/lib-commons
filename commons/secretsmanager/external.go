// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// # External Credentials
//
// External credentials are arbitrary key/value secrets stored in AWS Secrets Manager
// for third-party integrations (Stripe, Twilio, Salesforce, webhooks, etc.).
//
// They live alongside M2M credentials under the same tenant/application path
// convention but use the `external` segment instead of `m2m`:
//
//	tenants/{env}/{tenantOrgID}/{applicationName}/external/{targetService}/credentials
//
// Unlike M2M credentials — which are strictly OAuth2 clientId/clientSecret pairs —
// external credentials hold a free-form map[string]string because every third-party
// integration ships a different shape (api_key, webhook_secret, access_token, etc.).
// Callers are responsible for extracting the keys they need.
//
// # Usage
//
//	cfg, err := awsconfig.LoadDefaultConfig(ctx)
//	if err != nil {
//	    // handle error
//	}
//	client := secretsmanager.NewFromConfig(cfg)
//
//	creds, err := secretsmanager.GetExternalCredentials(ctx, client, "staging", tenantOrgID, "plugin-pix", "stripe")
//	if err != nil {
//	    // handle error
//	}
//
//	apiKey := creds["api_key"]
//
// # Thread Safety
//
// GetExternalCredentials and BuildExternalSecretPath are safe for concurrent use.
// No package-level mutable state is maintained.

// Sentinel errors for external credential operations.
// These mirror the M2M sentinels but are kept distinct so logs and error
// classification can tell M2M and external-integration failures apart.
var (
	// ErrExternalCredentialsNotFound is returned when external credentials cannot be found at the expected path.
	ErrExternalCredentialsNotFound = errors.New("external credentials not found")

	// ErrExternalVaultAccessDenied is returned when access to the vault is denied (missing IAM permissions or expired tokens).
	ErrExternalVaultAccessDenied = errors.New("vault access denied for external credentials")

	// ErrExternalRetrievalFailed is returned when external credential retrieval fails due to infrastructure issues.
	ErrExternalRetrievalFailed = errors.New("failed to retrieve external credentials")

	// ErrExternalUnmarshalFailed is returned when the secret value cannot be deserialized into a key/value map.
	ErrExternalUnmarshalFailed = errors.New("failed to unmarshal external credentials")

	// ErrExternalInvalidInput is returned when required input parameters are missing.
	ErrExternalInvalidInput = errors.New("invalid input for external credentials")

	// ErrExternalInvalidCredentials is returned when the retrieved secret is an empty key/value map.
	ErrExternalInvalidCredentials = errors.New("external credentials are empty")

	// ErrExternalBinarySecretNotSupported is returned when the secret is stored as binary data rather than a string.
	ErrExternalBinarySecretNotSupported = errors.New("binary secrets are not supported for external credentials")

	// ErrExternalInvalidPathSegment is returned when a path segment contains path traversal characters.
	ErrExternalInvalidPathSegment = errors.New("invalid path segment for external credentials")
)

// BuildExternalSecretPath constructs the secret path for external integration credentials.
//
// Format: tenants/{env}/{tenantOrgID}/{applicationName}/external/{targetService}/credentials
//
// When env is empty, the path omits the environment segment for backward compatibility:
//
//	tenants/{tenantOrgID}/{applicationName}/external/{targetService}/credentials
//
// This builder is exported so callers can construct paths without duplicating
// the convention (e.g., provisioning tooling, integration tests).
func BuildExternalSecretPath(env, tenantOrgID, applicationName, targetService string) string {
	envPrefix := ""
	if env != "" {
		envPrefix = env + "/"
	}

	return fmt.Sprintf("tenants/%s%s/%s/external/%s/credentials", envPrefix, tenantOrgID, applicationName, targetService)
}

// GetExternalCredentials fetches external integration credentials from AWS Secrets Manager.
//
// Unlike GetM2MCredentials — which expects a strict {clientId, clientSecret} payload —
// this function returns the secret as a free-form map[string]string because third-party
// integrations vary in shape (api_key, webhook_secret, access_token, etc.).
//
// Parameters:
//   - ctx: context for cancellation and tracing
//   - client: AWS Secrets Manager client (must not be nil)
//   - env: deployment environment (e.g., "staging", "production"); empty string is accepted for backward compatibility
//   - tenantOrgID: resolved from request context (JWT owner claim); must not be empty
//   - applicationName: the plugin or service name (e.g., "plugin-pix"); must not be empty
//   - targetService: the third-party service name (e.g., "stripe", "twilio"); must not be empty
//
// Path convention:
//
//	tenants/{env}/{tenantOrgID}/{applicationName}/external/{targetService}/credentials
//
// Returns descriptive errors when:
//   - client is nil
//   - required parameters are missing or contain path traversal characters
//   - secret not found at path
//   - vault credentials are missing or expired
//   - secret value is not a valid JSON object of string→string
//   - secret value is binary (not supported)
//   - secret value decodes to an empty map
//
// Safe for concurrent use (no shared mutable state).
func GetExternalCredentials(ctx context.Context, client SecretsManagerClient, env, tenantOrgID, applicationName, targetService string) (map[string]string, error) {
	// Validate inputs - check for typed-nil client using reflect
	if isNilInterface(client) {
		return nil, fmt.Errorf("%w: client is required", ErrExternalInvalidInput)
	}

	// Validate and sanitize path segments (trims whitespace, rejects traversal chars)
	cleanTenantOrgID, err := validatePathSegmentWithErrors("tenantOrgID", tenantOrgID, ErrExternalInvalidInput, ErrExternalInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	cleanAppName, err := validatePathSegmentWithErrors("applicationName", applicationName, ErrExternalInvalidInput, ErrExternalInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	cleanTargetService, err := validatePathSegmentWithErrors("targetService", targetService, ErrExternalInvalidInput, ErrExternalInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	cleanEnv, err := validateOptionalEnv(env, ErrExternalInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	// Build the secret path
	secretPath := BuildExternalSecretPath(cleanEnv, cleanTenantOrgID, cleanAppName, cleanTargetService)
	redacted := redactPath(secretPath)

	// Fetch the secret from AWS Secrets Manager
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretPath),
	}

	output, err := client.GetSecretValue(ctx, input)
	if err != nil {
		return nil, classifyAWSErrorWithSentinels(err, secretPath, ErrExternalCredentialsNotFound, ErrExternalVaultAccessDenied, ErrExternalRetrievalFailed)
	}

	// Check for binary secret FIRST (before attempting JSON unmarshal)
	if output == nil || output.SecretString == nil {
		return nil, fmt.Errorf("%w: secret at %s is binary or nil", ErrExternalBinarySecretNotSupported, redacted)
	}

	// Unmarshal as a free-form key/value map
	creds := make(map[string]string)
	if err := json.Unmarshal([]byte(*output.SecretString), &creds); err != nil {
		return nil, fmt.Errorf("%w: secret at %s: %w", ErrExternalUnmarshalFailed, redacted, err)
	}

	// Reject empty payloads — an empty map is never a valid external credential set.
	if len(creds) == 0 {
		return nil, fmt.Errorf("%w: secret at %s: empty credential map", ErrExternalInvalidCredentials, redacted)
	}

	return creds, nil
}
