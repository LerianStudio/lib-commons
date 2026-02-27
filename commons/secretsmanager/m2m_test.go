// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		return nil, fmt.Errorf("InvalidParameterException: secret ID is required")
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
		Message: aws.String(fmt.Sprintf("Secrets Manager can't find the specified secret. path=%s", secretPath)),
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
			path := buildM2MSecretPath(tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

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
			awsError:    fmt.Errorf("InternalServiceError: service unavailable"),
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
