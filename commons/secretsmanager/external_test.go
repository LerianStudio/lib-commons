//go:build unit

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Test: BuildExternalSecretPath (path construction)
// ============================================================================

func TestBuildExternalSecretPath(t *testing.T) {
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
			targetService:   "stripe",
			expectedPath:    "tenants/staging/org_01KHVKQQP6D2N4RDJK0ADEKQX1/plugin-pix/external/stripe/credentials",
		},
		{
			name:            "production environment",
			env:             "production",
			tenantOrgID:     "org_02ABCDEF",
			applicationName: "plugin-auth",
			targetService:   "twilio",
			expectedPath:    "tenants/production/org_02ABCDEF/plugin-auth/external/twilio/credentials",
		},
		{
			name:            "empty env for backward compatibility",
			env:             "",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-crm",
			targetService:   "salesforce",
			expectedPath:    "tenants/org_01ABC/plugin-crm/external/salesforce/credentials",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := BuildExternalSecretPath(tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			assert.Equal(t, tt.expectedPath, path)
		})
	}
}

// ============================================================================
// Test: BuildM2MSecretPath (now exported, must still work)
// ============================================================================

func TestBuildM2MSecretPath_Exported(t *testing.T) {
	t.Parallel()

	path := BuildM2MSecretPath("staging", "org_01ABC", "plugin-pix", "ledger")
	assert.Equal(t, "tenants/staging/org_01ABC/plugin-pix/m2m/ledger/credentials", path)

	// Backward compat: empty env
	pathNoEnv := BuildM2MSecretPath("", "org_01ABC", "plugin-pix", "ledger")
	assert.Equal(t, "tenants/org_01ABC/plugin-pix/m2m/ledger/credentials", pathNoEnv)
}

// ============================================================================
// Test: GetExternalCredentials - valid JSON deserialization (arbitrary keys)
// ============================================================================

func TestGetExternalCredentials_ValidJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		secretValue     string
		env             string
		tenantOrgID     string
		applicationName string
		targetService   string
		expected        map[string]string
	}{
		{
			name:            "api_key shape",
			secretValue:     `{"api_key":"sk_live_123","api_secret":"shhh"}`,
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "stripe",
			expected: map[string]string{
				"api_key":    "sk_live_123",
				"api_secret": "shhh",
			},
		},
		{
			name:            "webhook_secret shape",
			secretValue:     `{"webhook_secret":"whsec_abc","webhook_url":"https://example.com/hook"}`,
			env:             "production",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "twilio",
			expected: map[string]string{
				"webhook_secret": "whsec_abc",
				"webhook_url":    "https://example.com/hook",
			},
		},
		{
			name:            "single-key access_token",
			secretValue:     `{"access_token":"tok_xyz"}`,
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-auth",
			targetService:   "salesforce",
			expected: map[string]string{
				"access_token": "tok_xyz",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secretPath := BuildExternalSecretPath(tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			mock := &mockSecretsManagerClient{
				secrets: map[string]string{
					secretPath: tt.secretValue,
				},
				errors: map[string]error{},
			}

			creds, err := GetExternalCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)

			require.NoError(t, err)
			require.NotNil(t, creds)
			assert.Equal(t, tt.expected, creds)
		})
	}
}

// ============================================================================
// Test: GetExternalCredentials - invalid JSON deserialization
// ============================================================================

func TestGetExternalCredentials_InvalidJSON(t *testing.T) {
	t.Parallel()

	secretPath := BuildExternalSecretPath("staging", "org_01ABC", "plugin-pix", "stripe")

	tests := []struct {
		name        string
		secretValue string
		expectedErr error
	}{
		{
			name:        "malformed JSON",
			secretValue: `{invalid-json`,
			expectedErr: ErrExternalUnmarshalFailed,
		},
		{
			name:        "empty string",
			secretValue: ``,
			expectedErr: ErrExternalUnmarshalFailed,
		},
		{
			name:        "plain text instead of JSON",
			secretValue: `not-json-at-all`,
			expectedErr: ErrExternalUnmarshalFailed,
		},
		{
			name:        "JSON array not object",
			secretValue: `["foo","bar"]`,
			expectedErr: ErrExternalUnmarshalFailed,
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

			creds, err := GetExternalCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "stripe")

			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetExternalCredentials - empty map rejected
// ============================================================================

func TestGetExternalCredentials_EmptyMap(t *testing.T) {
	t.Parallel()

	secretPath := BuildExternalSecretPath("staging", "org_01ABC", "plugin-pix", "stripe")

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			secretPath: `{}`,
		},
		errors: map[string]error{},
	}

	creds, err := GetExternalCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "stripe")

	require.ErrorIs(t, err, ErrExternalInvalidCredentials)
	assert.Nil(t, creds)
}

// ============================================================================
// Test: GetExternalCredentials - secret not found
// ============================================================================

func TestGetExternalCredentials_SecretNotFound(t *testing.T) {
	t.Parallel()

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{},
		errors:  map[string]error{},
	}

	creds, err := GetExternalCredentials(context.Background(), mock, "staging", "org_nonexistent", "plugin-pix", "stripe")

	require.ErrorIs(t, err, ErrExternalCredentialsNotFound)
	assert.Nil(t, creds)
}

// ============================================================================
// Test: GetExternalCredentials - AWS access denied / token expired / generic
// ============================================================================

func TestGetExternalCredentials_AWSErrors(t *testing.T) {
	t.Parallel()

	secretPath := BuildExternalSecretPath("staging", "org_01ABC", "plugin-pix", "stripe")

	tests := []struct {
		name        string
		awsError    error
		expectedErr error
	}{
		{
			name: "access denied",
			awsError: &smithy.GenericAPIError{
				Code:    "AccessDeniedException",
				Message: "User is not authorized to access this resource",
			},
			expectedErr: ErrExternalVaultAccessDenied,
		},
		{
			name: "credentials expired",
			awsError: &smithy.GenericAPIError{
				Code:    "ExpiredTokenException",
				Message: "The security token included in the request is expired",
			},
			expectedErr: ErrExternalVaultAccessDenied,
		},
		{
			name:        "generic AWS error",
			awsError:    errors.New("InternalServiceError: service unavailable"),
			expectedErr: ErrExternalRetrievalFailed,
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

			creds, err := GetExternalCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "stripe")
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetExternalCredentials - input validation
// ============================================================================

func TestGetExternalCredentials_InputValidation(t *testing.T) {
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
			targetService:   "stripe",
			expectedErr:     ErrExternalInvalidInput,
		},
		{
			name:            "empty applicationName",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "",
			targetService:   "stripe",
			expectedErr:     ErrExternalInvalidInput,
		},
		{
			name:            "empty targetService",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "",
			expectedErr:     ErrExternalInvalidInput,
		},
		{
			name:            "whitespace-only tenantOrgID",
			env:             "staging",
			tenantOrgID:     "   ",
			applicationName: "plugin-pix",
			targetService:   "stripe",
			expectedErr:     ErrExternalInvalidInput,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			creds, err := GetExternalCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)
			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetExternalCredentials - nil client (both untyped and typed nil)
// ============================================================================

func TestGetExternalCredentials_NilClient(t *testing.T) {
	t.Parallel()

	t.Run("untyped nil client", func(t *testing.T) {
		t.Parallel()

		creds, err := GetExternalCredentials(context.Background(), nil, "staging", "org_01ABC", "plugin-pix", "stripe")
		require.ErrorIs(t, err, ErrExternalInvalidInput)
		assert.Nil(t, creds)
	})

	t.Run("typed nil client", func(t *testing.T) {
		t.Parallel()

		var typedNil *mockSecretsManagerClient
		creds, err := GetExternalCredentials(context.Background(), typedNil, "staging", "org_01ABC", "plugin-pix", "stripe")
		require.ErrorIs(t, err, ErrExternalInvalidInput)
		assert.Nil(t, creds)
	})
}

// ============================================================================
// Test: GetExternalCredentials - path traversal rejection
// ============================================================================

func TestGetExternalCredentials_PathTraversal(t *testing.T) {
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
			targetService:   "stripe",
			expectedErr:     ErrExternalInvalidPathSegment,
		},
		{
			name:            "applicationName with backslash",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin\\pix",
			targetService:   "stripe",
			expectedErr:     ErrExternalInvalidPathSegment,
		},
		{
			name:            "targetService with dot-dot",
			env:             "staging",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "..secret",
			expectedErr:     ErrExternalInvalidPathSegment,
		},
		{
			name:            "env with traversal",
			env:             "staging/../../admin",
			tenantOrgID:     "org_01ABC",
			applicationName: "plugin-pix",
			targetService:   "stripe",
			expectedErr:     ErrExternalInvalidPathSegment,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			creds, err := GetExternalCredentials(context.Background(), mock, tt.env, tt.tenantOrgID, tt.applicationName, tt.targetService)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, creds)
		})
	}
}

// ============================================================================
// Test: GetExternalCredentials - binary secret not supported
// ============================================================================

func TestGetExternalCredentials_BinarySecret(t *testing.T) {
	t.Parallel()

	mock := &mockBinarySecretsManagerClient{}

	creds, err := GetExternalCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "stripe")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExternalBinarySecretNotSupported)
	assert.Nil(t, creds)
}

// ============================================================================
// Test: GetExternalCredentials - errors redact full path
// ============================================================================

func TestGetExternalCredentials_ErrorsDoNotLeakFullPath(t *testing.T) {
	t.Parallel()

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{},
		errors:  map[string]error{},
	}

	_, err := GetExternalCredentials(context.Background(), mock, "staging", "org_01ABC", "plugin-pix", "stripe")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExternalCredentialsNotFound)
	assert.NotContains(t, err.Error(), "tenants/staging/org_01ABC/plugin-pix/external/stripe/credentials")
	assert.Contains(t, err.Error(), "credentials")
}

// ============================================================================
// Test: GetExternalCredentials - whitespace trimming in segments
// ============================================================================

func TestGetExternalCredentials_WhitespaceTrimming(t *testing.T) {
	t.Parallel()

	secretValue := `{"api_key":"sk_test"}`
	// Trimmed path should be used
	secretPath := BuildExternalSecretPath("staging", "org_01ABC", "plugin-pix", "stripe")

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			secretPath: secretValue,
		},
		errors: map[string]error{},
	}

	creds, err := GetExternalCredentials(context.Background(), mock, " staging ", " org_01ABC ", " plugin-pix ", " stripe ")
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "sk_test", creds["api_key"])
}

// ============================================================================
// Test: GetExternalCredentials - concurrent safety
// ============================================================================

func TestGetExternalCredentials_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	payload := map[string]string{
		"api_key":    "sk_concurrent",
		"api_secret": "shhh_concurrent",
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	secretPath := BuildExternalSecretPath("staging", "org_concurrent", "plugin-pix", "stripe")

	mock := &mockSecretsManagerClient{
		secrets: map[string]string{
			secretPath: string(payloadJSON),
		},
		errors: map[string]error{},
	}

	const goroutineCount = 50

	var wg sync.WaitGroup
	wg.Add(goroutineCount)

	results := make([]map[string]string, goroutineCount)
	errs := make([]error, goroutineCount)

	for i := range goroutineCount {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = GetExternalCredentials(
				context.Background(),
				mock,
				"staging",
				"org_concurrent",
				"plugin-pix",
				"stripe",
			)
		}(i)
	}

	wg.Wait()

	for i := range goroutineCount {
		require.NoError(t, errs[i], "goroutine %d should not error", i)
		require.NotNil(t, results[i], "goroutine %d should return credentials", i)
		assert.Equal(t, "sk_concurrent", results[i]["api_key"], "goroutine %d should have correct api_key", i)
		assert.Equal(t, "shhh_concurrent", results[i]["api_secret"], "goroutine %d should have correct api_secret", i)
	}
}
