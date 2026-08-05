//go:build unit

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-observability/v2/constants"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBinarySecretsManagerClient returns a nil SecretString to simulate binary secrets.
type mockBinarySecretsManagerClient struct{}

func (m *mockBinarySecretsManagerClient) GetSecretValue(
	_ context.Context,
	_ *secretsmanager.GetSecretValueInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.GetSecretValueOutput, error) {
	return &secretsmanager.GetSecretValueOutput{
		SecretBinary: []byte{0x01, 0x02, 0x03},
		SecretString: nil,
	}, nil
}

// mockSecretsManagerClient implements SecretsManagerClient for testing.
type mockSecretsManagerClient struct {
	secrets map[string]string
	errors  map[string]error
}

func (m *mockSecretsManagerClient) GetSecretValue(
	ctx context.Context,
	params *secretsmanager.GetSecretValueInput,
	optFns ...func(*secretsmanager.Options),
) (*secretsmanager.GetSecretValueOutput, error) {
	if params.SecretId == nil {
		return nil, errors.New("InvalidParameterException: secret ID is required")
	}

	secretPath := *params.SecretId

	if err, ok := m.errors[secretPath]; ok {
		return nil, err
	}

	if secret, ok := m.secrets[secretPath]; ok {
		return &secretsmanager.GetSecretValueOutput{
			SecretString: aws.String(secret),
		}, nil
	}

	return nil, &smtypes.ResourceNotFoundException{
		Message: aws.String("Secrets Manager can't find the specified secret. path=" + secretPath),
	}
}

// ============================================================================
// Test: BuildM2MSecretPath (path construction)
// ============================================================================

func TestBuildM2MSecretPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		env             string
		tenantOrgID     string
		applicationName string
		targetService   string
		expectedPath    string
	}{
		{
			name:            "standard path with all parameters",
			env:             "staging",
			tenantOrgID:     "org_01KHVKQQP6D2N4RDJK0ADEKQX1",
			applicationName: "plugin-pix",
			targetService:   "ledger",
			expectedPath:    "tenants/staging/org_01KHVKQQP6D2N4RDJK0ADEKQX1/plugin-pix/m2m/ledger/credentials",
		},
		{
			name:            "production environment",
			env:             "production",
			tenantOrgID:     "org_02ABCDEF",
			applicationName: "plugin-auth",
			targetService:   "midaz",
			expectedPath:    "tenants/production/org_02ABCDEF/plugin-auth/m2m/midaz/credentials",
		},
		{
			name:            "empty env for backward compatibility",
			env:             "",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-crm",
			targetService:   "ledger",
			expectedPath:    "tenants/org_01ABC/plugin-crm/m2m/ledger/credentials",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			path := BuildM2MSecretPath(tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			// Assert
			assert.Equal(t, tt.expectedPath, path)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - valid JSON deserialization
// ============================================================================

func TestGetM2MCredentials_ValidJSON(t *testing.T) {
	t.Parallel()

	validCreds := M2MCredentials{
		ClientID:     "plg_01KHVKQQP6D2N4RDJK0ADEKQX1",
		ClientSecret: "sec_super-secret-value",
	}

	credsJSON, err := json.Marshal(validCreds)
	require.NoError(t, err, "test setup: marshalling valid credentials should not fail")

	secretPath := "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials"

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			secretPath: string(credsJSON),
		},
		errors: map[string]error{},
	}

	tests := []struct {
		name             string
		env              string
		tenantOrgID      string
		applicationName  string
		targetService    string
		expectedClientID string
		expectedSecret   string
	}{
		{
			name:             "deserializes all fields correctly",
			env:              "staging",
			tenantOrgID:      "org_01ABC",
			applicationName:  "plugin-pix",
			targetService:    "ledger",
			expectedClientID: "plg_01KHVKQQP6D2N4RDJK0ADEKQX1",
			expectedSecret:   "sec_super-secret-value",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			creds, err := GetM2MCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			// Assert
			require.NoError(t, err)
			require.NotNil(t, creds)
			assert.Equal(t, tt.expectedClientID, creds.ClientID)
			assert.Equal(t, tt.expectedSecret, creds.ClientSecret)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - invalid JSON deserialization
// ============================================================================

func TestGetM2MCredentials_InvalidJSON(t *testing.T) {
	t.Parallel()

	secretPath := "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials"

	tests := []struct {
		name        string
		secretValue string
		expectedErr error
	}{
		{
			name:        "malformed JSON",
			secretValue: `{invalid-json`,
			expectedErr: ErrM2MUnmarshalFailed,
		},
		{
			name:        "empty string",
			secretValue: ``,
			expectedErr: ErrM2MUnmarshalFailed,
		},
		{
			name:        "plain text instead of JSON",
			secretValue: `not-json-at-all`,
			expectedErr: ErrM2MUnmarshalFailed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockSecretsManagerClient{
				secrets: map[string]string{
					secretPath: tt.secretValue,
				},
				errors: map[string]error{},
			}

			// Act
			creds, err := GetM2MCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "ledger")

			// Assert
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - incomplete credentials (missing required fields)
// ============================================================================

func TestGetM2MCredentials_IncompleteCredentials(t *testing.T) {
	t.Parallel()

	secretPath := "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials"

	tests := []struct {
		name        string
		secretValue string
		expectedErr error
	}{
		{
			name:        "empty JSON object - all fields missing",
			secretValue: `{}`,
			expectedErr: ErrM2MInvalidCredentials,
		},
		{
			name:        "only clientId present",
			secretValue: `{"clientId":"id1"}`,
			expectedErr: ErrM2MInvalidCredentials,
		},
		{
			name:        "only clientSecret missing",
			secretValue: `{"clientId":"id1"}`,
			expectedErr: ErrM2MInvalidCredentials,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockSecretsManagerClient{
				secrets: map[string]string{
					secretPath: tt.secretValue,
				},
				errors: map[string]error{},
			}

			// Act
			creds, err := GetM2MCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "ledger")

			// Assert
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - secret not found error
// ============================================================================

func TestGetM2MCredentials_SecretNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		env             string
		tenantOrgID     string
		applicationName string
		targetService   string
		expectedErr     error
	}{
		{
			name:            "secret does not exist in vault",
			env:             "staging",
			tenantOrgID:     "org_nonexistent",
			applicationName: "plugin-pix",
			targetService:   "ledger",
			expectedErr:     ErrM2MCredentialsNotFound,
		},
		{
			name:            "different tenant not provisioned",
			env:             "production",
			tenantOrgID:     "org_notprovisioned",
			applicationName: "plugin-auth",
			targetService:   "midaz",
			expectedErr:     ErrM2MCredentialsNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockSecretsManagerClient{
				secrets: map[string]string{},
				errors:  map[string]error{},
			}

			// Act
			creds, err := GetM2MCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			// Assert
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - AWS error messages are scrubbed
// ============================================================================

func TestGetM2MCredentials_AWSErrorsDoNotLeakTenantIdentity(t *testing.T) {
	t.Parallel()

	const tenantOrgID = "org_01ABCDEFGHIJKLMNOP"

	secretPath := BuildM2MSecretPath("staging", tenantOrgID, "plugin-pix", "ledger")
	arn := "arn:aws:secretsmanager:us-east-1:123456789012:secret:" + secretPath + "-AbCdEf"

	tests := []struct {
		name        string
		awsError    error
		expectedErr error
	}{
		{
			name:        "access denied echoes the secret arn",
			awsError:    &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: secretsmanager:GetSecretValue on resource: " + arn},
			expectedErr: ErrM2MVaultAccessDenied,
		},
		{
			name:        "expired token echoes the secret arn",
			awsError:    &smithy.GenericAPIError{Code: "ExpiredTokenException", Message: "token expired while reading " + arn},
			expectedErr: ErrM2MVaultAccessDenied,
		},
		{
			name:        "infrastructure failure echoes the secret arn",
			awsError:    errors.New("InternalServiceError while reading " + arn),
			expectedErr: ErrM2MRetrievalFailed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockSecretsManagerClient{errors: map[string]error{secretPath: tt.awsError}}

			creds, err := GetM2MCredentials(context.Background(), mock, "staging", tenantOrgID, "plugin-pix", "ledger")
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)

			message := err.Error()
			assert.NotContains(t, message, tenantOrgID, "the tenant org id must never reach the error message")
			assert.NotContains(t, message, secretPath, "the secret path must never reach the error message")
			assert.NotContains(t, message, "plugin-pix", "the application name must never reach the error message")
			assert.Contains(t, message, redactPath(secretPath), "the redacted path must identify the secret instead")
		})
	}
}

// TestClassifyAWSError_TypedNilAPIError proves the M2M classifier tolerates a
// typed-nil smithy.APIError in the chain: errors.As matches the nil concrete
// pointer, and ErrorCode would panic without the nil-interface guard.
func TestClassifyAWSError_TypedNilAPIError(t *testing.T) {
	t.Parallel()

	var nilAPIError *typedNilAPIError

	var err error

	assert.NotPanics(t, func() {
		err = classifyAWSError(nilAPIError, "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials")
	})
	require.ErrorIs(t, err, ErrM2MRetrievalFailed,
		"a typed-nil API error is not an access-denied; it must classify as retrieval failure")
}

// ============================================================================
// Test: GetM2MCredentials - AWS credentials/access missing
// ============================================================================

func TestGetM2MCredentials_AWSCredentialsMissing(t *testing.T) {
	t.Parallel()

	secretPath := "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials"

	tests := []struct {
		name        string
		awsError    error
		expectedErr error
	}{
		{
			name: "access denied - missing IAM permissions",
			awsError: &smithy.GenericAPIError{
				Code:    "AccessDeniedException",
				Message: "User is not authorized to access this resource",
			},
			expectedErr: ErrM2MVaultAccessDenied,
		},
		{
			name: "credentials expired",
			awsError: &smithy.GenericAPIError{
				Code:    "ExpiredTokenException",
				Message: "The security token included in the request is expired",
			},
			expectedErr: ErrM2MVaultAccessDenied,
		},
		{
			name:        "generic AWS error",
			awsError:    errors.New("InternalServiceError: service unavailable"),
			expectedErr: ErrM2MRetrievalFailed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockSecretsManagerClient{
				secrets: map[string]string{},
				errors: map[string]error{
					secretPath: tt.awsError,
				},
			}

			// Act
			creds, err := GetM2MCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "ledger")

			// Assert
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - input validation
// ============================================================================

func TestGetM2MCredentials_InputValidation(t *testing.T) {
	t.Parallel()

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{},
		errors:  map[string]error{},
	}

	tests := []struct {
		name            string
		env             string
		tenantOrgID     string
		applicationName string
		targetService   string
		expectedErr     error
	}{
		{
			name:            "empty tenantOrgID",
			env:             "staging",
			tenantOrgID:     "",
			applicationName: "plugin-pix",
			targetService:   "ledger",
			expectedErr:     ErrM2MInvalidInput,
		},
		{
			name:            "empty applicationName",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "",
			targetService:   "ledger",
			expectedErr:     ErrM2MInvalidInput,
		},
		{
			name:            "empty targetService",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "",
			expectedErr:     ErrM2MInvalidInput,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			creds, err := GetM2MCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			// Assert
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetM2MCredentials - nil client
// ============================================================================

func TestGetM2MCredentials_NilClient(t *testing.T) {
	t.Parallel()

	t.Run("nil client returns descriptive error", func(t *testing.T) {
		t.Parallel()

		// Act
		creds, err := GetM2MCredentials(context.Background(), nil, "staging", "org_01ABC", "plugin-pix", "ledger")

		// Assert
		require.ErrorIs(t, err, ErrM2MInvalidInput)
		assert.Nil(t, creds)
	})
}

// ============================================================================
// Test: GetM2MCredentials - concurrent safety
// ============================================================================

func TestGetM2MCredentials_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	validCreds := M2MCredentials{
		ClientID:     "plg_concurrent_test",
		ClientSecret: "sec_concurrent_secret",
	}

	credsJSON, err := json.Marshal(validCreds)
	require.NoError(t, err, "test setup: marshalling valid credentials should not fail")

	secretPath := "tenants/staging/org_concurrent/plugin-pix/m2m/ledger/credentials"

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			secretPath: string(credsJSON),
		},
		errors: map[string]error{},
	}

	const goroutineCount = 50

	t.Run("concurrent calls do not race or panic", func(t *testing.T) {
		t.Parallel()

		var wg sync.WaitGroup
		wg.Add(goroutineCount)

		results := make([]*M2MCredentials, goroutineCount)
		errs := make([]error, goroutineCount)

		for i := range goroutineCount {
			go func(idx int) {
				defer wg.Done()
				results[idx], errs[idx] = GetM2MCredentials(
					context.Background(),
					mock,
					"staging",
					"org_concurrent",
					"plugin-pix",
					"ledger",
				)
			}(i)
		}

		wg.Wait()

		// Assert: all goroutines should succeed with identical results
		for i := range goroutineCount {
			require.NoError(t, errs[i], "goroutine %d should not error", i)
			require.NotNil(t, results[i], "goroutine %d should return credentials", i)
			assert.Equal(t, "plg_concurrent_test", results[i].ClientID, "goroutine %d should have correct clientId", i)
			assert.Equal(t, "sec_concurrent_secret", results[i].ClientSecret, "goroutine %d should have correct clientSecret", i)
		}
	})
}

// ============================================================================
// Test: M2MCredentials struct JSON tags
// ============================================================================

func TestM2MCredentials_JSONTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		json     string
		expected M2MCredentials
	}{
		{
			name: "standard camelCase JSON fields",
			json: `{"clientId":"id1","clientSecret":"sec1"}`,
			expected: M2MCredentials{
				ClientID:     "id1",
				ClientSecret: "sec1",
			},
		},
		{
			name: "extra fields are ignored",
			json: `{"clientId":"id2","clientSecret":"sec2","tokenUrl":"https://example.com/token","tenantId":"t1","targetService":"ledger"}`,
			expected: M2MCredentials{
				ClientID:     "id2",
				ClientSecret: "sec2",
			},
		},
		{
			name:     "missing fields default to empty strings",
			json:     `{}`,
			expected: M2MCredentials{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var creds M2MCredentials
			err := json.Unmarshal([]byte(tt.json), &creds)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, creds)
		})
	}
}

func TestM2MCredentials_StringRedactsSecret(t *testing.T) {
	t.Parallel()

	creds := M2MCredentials{
		ClientID:     "client-visible-id",
		ClientSecret: "sec_super-secret-value",
	}

	formatted := fmt.Sprintf("%v", creds)
	goFormatted := fmt.Sprintf("%#v", creds)

	assert.Contains(t, formatted, "ClientSecret:"+constants.ObfuscatedValue)
	assert.Contains(t, goFormatted, "ClientSecret:"+constants.ObfuscatedValue)
	assert.NotContains(t, formatted, creds.ClientSecret)
	assert.NotContains(t, goFormatted, creds.ClientSecret)
	assert.Contains(t, formatted, creds.ClientID)
	assert.Contains(t, goFormatted, creds.ClientID)
}

// ============================================================================
// Test: Path traversal prevention
// ============================================================================

func TestGetM2MCredentials_PathTraversal(t *testing.T) {
	t.Parallel()

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{},
		errors:  map[string]error{},
	}

	tests := []struct {
		name            string
		env             string
		tenantOrgID     string
		applicationName string
		targetService   string
		expectedErr     error
	}{
		{
			name:            "tenantOrgID with slash",
			env:             "staging",
			tenantOrgID:     "org/../admin",
			applicationName: "plugin-pix",
			targetService:   "ledger",
			expectedErr:     ErrM2MInvalidPathSegment,
		},
		{
			name:            "applicationName with backslash",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin\\pix",
			targetService:   "ledger",
			expectedErr:     ErrM2MInvalidPathSegment,
		},
		{
			name:            "targetService with dot-dot",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "..secret",
			expectedErr:     ErrM2MInvalidPathSegment,
		},
		{
			name:            "env with slash",
			env:             "staging/../../admin",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "ledger",
			expectedErr:     ErrM2MInvalidPathSegment,
		},
		{
			name:            "whitespace-only tenantOrgID",
			env:             "staging",
			tenantOrgID:     "   ",
			applicationName: "plugin-pix",
			targetService:   "ledger",
			expectedErr:     ErrM2MInvalidInput,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			creds, err := GetM2MCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: Binary secret detection
// ============================================================================

func TestGetM2MCredentials_BinarySecret(t *testing.T) {
	t.Parallel()

	mock := &mockBinarySecretsManagerClient{}

	creds, err := GetM2MCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "ledger")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrM2MBinarySecretNotSupported)
	assert.Nil(t, creds)
}

// ============================================================================
// Test: Error path redaction
// ============================================================================

func TestGetM2MCredentials_ErrorsDoNotLeakFullPath(t *testing.T) {
	t.Parallel()

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{},
		errors:  map[string]error{},
	}

	// Secret not found → error should contain redacted path, not full path
	_, err := GetM2MCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "ledger")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrM2MCredentialsNotFound)
	// Full path should not appear in the error
	assert.NotContains(t, err.Error(), "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials")
	// Redacted path should contain the last segment
	assert.Contains(t, err.Error(), "credentials")
}

// ============================================================================
// Test: Typed-nil client detection
// ============================================================================

func TestGetM2MCredentials_TypedNilClient(t *testing.T) {
	t.Parallel()

	// A typed-nil interface value should be caught.
	var typedNil *mockSecretsManagerClient

	creds, err := GetM2MCredentials(context.Background(), typedNil, "staging", "org_01ABC", "plugin-pix", "ledger")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrM2MInvalidInput)
	assert.Nil(t, creds)
}

// ============================================================================
// Test: Whitespace trimming in segments
// ============================================================================

func TestGetM2MCredentials_WhitespaceTrimming(t *testing.T) {
	t.Parallel()

	validCreds := M2MCredentials{
		ClientID:     "plg_trimmed",
		ClientSecret: "sec_trimmed",
	}

	credsJSON, err := json.Marshal(validCreds)
	require.NoError(t, err)

	// The trimmed path should be used
	secretPath := "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials"

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			secretPath: string(credsJSON),
		},
		errors: map[string]error{},
	}

	// Segments with leading/trailing whitespace should be trimmed
	creds, err := GetM2MCredentials(context.Background(), mock, " staging ", " org_01ABC ", " plugin-pix ", " ledger ")
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "plg_trimmed", creds.ClientID)
}

// ============================================================================
// Test: redactPath helper
// ============================================================================

func TestRedactPath(t *testing.T) {
	t.Parallel()

	result := redactPath("tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials")

	// Should contain the last segment
	assert.Contains(t, result, "credentials")
	// Should NOT contain the full path
	assert.NotContains(t, result, "tenants/staging")
	// Should contain a hash marker
	assert.Contains(t, result, "[")
	assert.Contains(t, result, "]")
}

// ============================================================================
// Test: validatePathSegment helper
// ============================================================================

func TestValidatePathSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       string
		expectErr   bool
		expectedErr error
		expected    string
	}{
		{name: "valid segment", value: "org_01ABC", expectErr: false, expected: "org_01ABC"},
		{name: "trimmed segment", value: "  org_01ABC  ", expectErr: false, expected: "org_01ABC"},
		{name: "empty", value: "", expectErr: true, expectedErr: ErrM2MInvalidInput},
		{name: "whitespace only", value: "   ", expectErr: true, expectedErr: ErrM2MInvalidInput},
		{name: "contains slash", value: "org/admin", expectErr: true, expectedErr: ErrM2MInvalidPathSegment},
		{name: "contains backslash", value: "org\\admin", expectErr: true, expectedErr: ErrM2MInvalidPathSegment},
		{name: "contains dot-dot", value: "..admin", expectErr: true, expectedErr: ErrM2MInvalidPathSegment},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := validatePathSegment("test", tt.value)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ============================================================================
// Test: TargetBaseURL (optional target location carried by the credential)
// ============================================================================

func TestM2MCredentials_TargetBaseURLDeserialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		json     string
		expected M2MCredentials
	}{
		{
			name: "targetBaseUrl is read when present",
			json: `{"clientId":"id1","clientSecret":"sec1","targetBaseUrl":"https://ledger.staging.svc:8080"}`,
			expected: M2MCredentials{
				ClientID:      "id1",
				ClientSecret:  "sec1",
				TargetBaseURL: "https://ledger.staging.svc:8080",
			},
		},
		{
			name: "absent targetBaseUrl deserializes exactly as before, without error",
			json: `{"clientId":"id2","clientSecret":"sec2"}`,
			expected: M2MCredentials{
				ClientID:     "id2",
				ClientSecret: "sec2",
			},
		},
		{
			name: "explicit empty targetBaseUrl is accepted as absent",
			json: `{"clientId":"id3","clientSecret":"sec3","targetBaseUrl":""}`,
			expected: M2MCredentials{
				ClientID:     "id3",
				ClientSecret: "sec3",
			},
		},
		{
			name: "unknown extra fields are still ignored alongside targetBaseUrl",
			json: `{"clientId":"id4","clientSecret":"sec4","targetBaseUrl":"http://localhost:3000","tokenUrl":"https://example.com/token","tenantId":"t1","futureField":42}`,
			expected: M2MCredentials{
				ClientID:      "id4",
				ClientSecret:  "sec4",
				TargetBaseURL: "http://localhost:3000",
			},
		},
		{
			name: "value is passed through verbatim without normalization",
			json: `{"clientId":"id5","clientSecret":"sec5","targetBaseUrl":"  HTTPS://Ledger.Example.COM:8080/  "}`,
			expected: M2MCredentials{
				ClientID:      "id5",
				ClientSecret:  "sec5",
				TargetBaseURL: "  HTTPS://Ledger.Example.COM:8080/  ",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var creds M2MCredentials
			err := json.Unmarshal([]byte(tt.json), &creds)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, creds)
		})
	}
}

// TestM2MCredentials_TargetBaseURLWireContract pins the serialized key so the
// json tag cannot be dropped or renamed silently: without the tag Go would emit
// the Go field name, and without omitempty an absent value would emit an empty key.
func TestM2MCredentials_TargetBaseURLWireContract(t *testing.T) {
	t.Parallel()

	t.Run("present value serializes under the camelCase key", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(M2MCredentials{
			ClientID:      "id1",
			ClientSecret:  "sec1",
			TargetBaseURL: "https://ledger.staging.svc:8080",
		})
		require.NoError(t, err)

		assert.JSONEq(t, `{"clientId":"id1","clientSecret":"sec1","targetBaseUrl":"https://ledger.staging.svc:8080"}`, string(encoded))
		assert.Contains(t, string(encoded), `"targetBaseUrl":`)
		assert.NotContains(t, string(encoded), "TargetBaseURL")
	})

	t.Run("absent value is omitted from the payload", func(t *testing.T) {
		t.Parallel()

		encoded, err := json.Marshal(M2MCredentials{ClientID: "id2", ClientSecret: "sec2"})
		require.NoError(t, err)

		assert.JSONEq(t, `{"clientId":"id2","clientSecret":"sec2"}`, string(encoded))
		assert.NotContains(t, string(encoded), "argetBaseURL")
		assert.NotContains(t, string(encoded), "targetBaseUrl")
	})

	t.Run("round trip preserves the value", func(t *testing.T) {
		t.Parallel()

		original := M2MCredentials{
			ClientID:      "id3",
			ClientSecret:  "sec3",
			TargetBaseURL: "https://ledger.example.com:9090",
		}

		encoded, err := json.Marshal(original)
		require.NoError(t, err)

		var decoded M2MCredentials
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, original, decoded)
	})
}

func TestM2MCredentials_StringShowsTargetBaseURLInClear(t *testing.T) {
	t.Parallel()

	creds := M2MCredentials{
		ClientID:      "client-visible-id",
		ClientSecret:  "sec_super-secret-value",
		TargetBaseURL: "https://ledger.staging.svc:8080",
	}

	formatted := fmt.Sprintf("%v", creds)
	goFormatted := fmt.Sprintf("%#v", creds)

	for _, out := range []string{formatted, goFormatted} {
		assert.Contains(t, out, creds.TargetBaseURL)
		assert.Contains(t, out, "ClientSecret:"+constants.ObfuscatedValue)
		assert.NotContains(t, out, creds.ClientSecret)
	}
}

func TestGetM2MCredentials_TargetBaseURL(t *testing.T) {
	t.Parallel()

	const (
		withURLPath    = "tenants/staging/tenant-with-url/plugin-hub/m2m/ledger/credentials"
		withoutURLPath = "tenants/staging/tenant-no-url/plugin-hub/m2m/ledger/credentials"
	)

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			withURLPath:    `{"clientId":"id1","clientSecret":"sec1","targetBaseUrl":"https://ledger.staging.svc:8080"}`,
			withoutURLPath: `{"clientId":"id2","clientSecret":"sec2"}`,
		},
	}

	t.Run("returns the registered base URL", func(t *testing.T) {
		t.Parallel()

		creds, err := GetM2MCredentials(context.Background(), mock, "staging", "tenant-with-url", "plugin-hub", "ledger")

		require.NoError(t, err)
		require.NotNil(t, creds)
		assert.Equal(t, "https://ledger.staging.svc:8080", creds.TargetBaseURL)
		assert.Equal(t, "id1", creds.ClientID)
		assert.Equal(t, "sec1", creds.ClientSecret)
	})

	t.Run("absence is a valid state, not an incomplete credential", func(t *testing.T) {
		t.Parallel()

		creds, err := GetM2MCredentials(context.Background(), mock, "staging", "tenant-no-url", "plugin-hub", "ledger")

		require.NoError(t, err)
		require.NotNil(t, creds)
		assert.Empty(t, creds.TargetBaseURL)
		assert.Equal(t, "id2", creds.ClientID)
		assert.Equal(t, "sec2", creds.ClientSecret)
	})
}
