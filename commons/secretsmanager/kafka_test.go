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
	"strconv"
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

const (
	kafkaTestEnv      = "staging"
	kafkaTestTenantID = "0198b1c2-3d4e-5f60-8a9b-0c1d2e3f4a5b"
	kafkaTestTenantNo = "0198b1c23d4e5f608a9b0c1d2e3f4a5b"
	kafkaTestModule   = "onboarding"
	kafkaTestPassword = "s3cr3t-scram-password"
)

// mockSecretsListerClient implements SecretsListerClient for testing. Pages are
// returned in order; every received input is recorded for assertions.
type mockSecretsListerClient struct {
	mu     sync.Mutex
	pages  []*secretsmanager.ListSecretsOutput
	errs   []error
	calls  []secretsmanager.ListSecretsInput
	cursor int
}

func (m *mockSecretsListerClient) ListSecrets(
	_ context.Context,
	params *secretsmanager.ListSecretsInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.ListSecretsOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, *params)

	idx := m.cursor
	m.cursor++

	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}

	if idx >= len(m.pages) {
		return &secretsmanager.ListSecretsOutput{}, nil
	}

	return m.pages[idx], nil
}

func (m *mockSecretsListerClient) recordedCalls() []secretsmanager.ListSecretsInput {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]secretsmanager.ListSecretsInput, len(m.calls))
	copy(out, m.calls)

	return out
}

type cyclingTokenListerClient struct {
	mu     sync.Mutex
	tokens []string
	calls  int
}

func (c *cyclingTokenListerClient) ListSecrets(
	_ context.Context,
	_ *secretsmanager.ListSecretsInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.ListSecretsOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	token := c.tokens[c.calls%len(c.tokens)]
	c.calls++

	return &secretsmanager.ListSecretsOutput{NextToken: aws.String(token)}, nil
}

func (c *cyclingTokenListerClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.calls
}

func listPage(nextToken string, names ...string) *secretsmanager.ListSecretsOutput {
	entries := make([]smtypes.SecretListEntry, 0, len(names))
	for _, n := range names {
		entries = append(entries, smtypes.SecretListEntry{Name: aws.String(n)})
	}

	out := &secretsmanager.ListSecretsOutput{SecretList: entries}
	if nextToken != "" {
		out.NextToken = aws.String(nextToken)
	}

	return out
}

func kafkaSecretJSON(t *testing.T, fields map[string]any) string {
	t.Helper()

	b, err := json.Marshal(fields)
	require.NoError(t, err)

	return string(b)
}

// ============================================================================
// Test: SanitizeKafkaSegment
// ============================================================================

func TestSanitizeKafkaSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "already sanitized passes through", input: "onboarding", expected: "onboarding"},
		{name: "uppercase is lowercased", input: "Onboarding", expected: "onboarding"},
		{name: "dashes are stripped", input: "Reporter-Manager", expected: "reportermanager"},
		{name: "underscores are stripped", input: "br_spi_worker", expected: "brspiworker"},
		{name: "dots are stripped", input: "external.openapi", expected: "externalopenapi"},
		{name: "slashes are stripped", input: "a/../b", expected: "ab"},
		{name: "digits are preserved", input: "midaz2", expected: "midaz2"},
		{name: "spaces are stripped", input: " tracer api ", expected: "tracerapi"},
		{name: "empty stays empty", input: "", expected: ""},
		{name: "all punctuation collapses to empty", input: "-_./", expected: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SanitizeKafkaSegment(tt.input)
			assert.Equal(t, tt.expected, got)
			assert.Equal(t, got, SanitizeKafkaSegment(got), "sanitizer must be idempotent")
		})
	}
}

// ============================================================================
// Test: BuildModuleKafkaSecretPath
// ============================================================================

func TestBuildModuleKafkaSecretPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      string
		tenantID string
		module   string
		expected string
	}{
		{
			name:     "dashed tenant id is dash-stripped",
			env:      "staging",
			tenantID: "0198b1c2-3d4e-5f60-8a9b-0c1d2e3f4a5b",
			module:   "onboarding",
			expected: "tenants/staging/0198b1c23d4e5f608a9b0c1d2e3f4a5b/onboarding/kafka",
		},
		{
			name:     "dash-free tenant id is unchanged",
			env:      "staging",
			tenantID: "0198b1c23d4e5f608a9b0c1d2e3f4a5b",
			module:   "onboarding",
			expected: "tenants/staging/0198b1c23d4e5f608a9b0c1d2e3f4a5b/onboarding/kafka",
		},
		{
			name:     "mixed-case module is lowercased",
			env:      "production",
			tenantID: "abc-def",
			module:   "Onboarding",
			expected: "tenants/production/abcdef/onboarding/kafka",
		},
		{
			name:     "punctuated module is stripped to [a-z0-9]",
			env:      "production",
			tenantID: "abc-def",
			module:   "Reporter-Manager",
			expected: "tenants/production/abcdef/reportermanager/kafka",
		},
		{
			name:     "empty env omits the env segment",
			env:      "",
			tenantID: "0198b1c2-3d4e-5f60-8a9b-0c1d2e3f4a5b",
			module:   "onboarding",
			expected: "tenants/0198b1c23d4e5f608a9b0c1d2e3f4a5b/onboarding/kafka",
		},
		{
			name:     "empty module yields an empty segment",
			env:      "staging",
			tenantID: "abcdef",
			module:   "",
			expected: "tenants/staging/abcdef//kafka",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, BuildModuleKafkaSecretPath(tt.env, tt.tenantID, tt.module))
		})
	}
}

func TestBuildModuleKafkaSecretPath_IsIdempotentOnSanitizedModule(t *testing.T) {
	t.Parallel()

	raw := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, "Reporter-Manager")
	pre := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, "reportermanager")

	assert.Equal(t, pre, raw)
}

// ============================================================================
// Test: ParseModuleKafkaSecretPath
// ============================================================================

func TestParseModuleKafkaSecretPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		env          string
		path         string
		expectedOK   bool
		expectTenant string
		expectModule string
	}{
		{
			name:         "canonical env-scoped kafka path",
			env:          "staging",
			path:         "tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka",
			expectedOK:   true,
			expectTenant: kafkaTestTenantNo,
			expectModule: "onboarding",
		},
		{
			name:         "legacy path with empty env",
			env:          "",
			path:         "tenants/" + kafkaTestTenantNo + "/onboarding/kafka",
			expectedOK:   true,
			expectTenant: kafkaTestTenantNo,
			expectModule: "onboarding",
		},
		{
			name:       "sibling postgres resource is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/onboarding/postgres",
			expectedOK: false,
		},
		{
			name:       "sibling mongodb resource is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/onboarding/mongodb",
			expectedOK: false,
		},
		{
			name:       "sibling rabbitmq resource is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/onboarding/rabbitmq",
			expectedOK: false,
		},
		{
			name:       "seven-segment m2m credentials path is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/plugin-pix/m2m/ledger/credentials",
			expectedOK: false,
		},
		{
			name:       "seven-segment external credentials path is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/plugin-pix/external/stripe/credentials",
			expectedOK: false,
		},
		{
			name:       "other environment is rejected",
			env:        "staging",
			path:       "tenants/production/" + kafkaTestTenantNo + "/onboarding/kafka",
			expectedOK: false,
		},
		{
			name:       "cluster admin path is rejected",
			env:        "staging",
			path:       "clusters/staging/kafka/shared/admin",
			expectedOK: false,
		},
		{
			name:       "empty tenant segment is rejected",
			env:        "staging",
			path:       "tenants/staging//onboarding/kafka",
			expectedOK: false,
		},
		{
			name:       "empty module segment is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "//kafka",
			expectedOK: false,
		},
		{
			name:       "hyphenated tenant segment is rejected",
			env:        "staging",
			path:       "tenants/staging/123-456/onboarding/kafka",
			expectedOK: false,
		},
		{
			name:       "unsanitized module segment is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/plugin-pix/kafka",
			expectedOK: false,
		},
		{
			name:       "uppercase module segment is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/Onboarding/kafka",
			expectedOK: false,
		},
		{
			name:       "env-scoped path is rejected when env is empty",
			env:        "",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka",
			expectedOK: false,
		},
		{
			name:       "trailing slash is rejected",
			env:        "staging",
			path:       "tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka/",
			expectedOK: false,
		},
		{
			name:       "empty path is rejected",
			env:        "staging",
			path:       "",
			expectedOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref, ok := ParseModuleKafkaSecretPath(tt.env, tt.path)
			require.Equal(t, tt.expectedOK, ok)

			if !tt.expectedOK {
				return
			}

			assert.Equal(t, tt.expectTenant, ref.TenantID)
			assert.Equal(t, tt.expectModule, ref.Module)
			assert.Equal(t, tt.path, ref.SecretPath)
		})
	}
}

// ============================================================================
// Test: GetModuleKafkaCredentials
// ============================================================================

func TestGetModuleKafkaCredentials_FullPayloadRoundTrip(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{
		secrets: map[string]string{
			path: kafkaSecretJSON(t, map[string]any{
				"brokers":     "b-1.msk:9096,b-2.msk:9096",
				"username":    "onboarding_" + kafkaTestTenantNo,
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": "ledger.,reporter.",
			}),
		},
	}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.NoError(t, err)
	require.NotNil(t, creds)

	assert.Equal(t, []string{"b-1.msk:9096", "b-2.msk:9096"}, creds.Brokers)
	assert.Equal(t, "onboarding_"+kafkaTestTenantNo, creds.Username)
	assert.Equal(t, kafkaTestPassword, creds.Password)
	assert.Equal(t, "SCRAM-SHA-512", creds.Mechanism)
	assert.True(t, creds.TLS)
	assert.Equal(t, []string{"ledger.", "reporter."}, creds.ACLPrefixes)
}

func TestGetModuleKafkaCredentials_TLSFalseIsPreserved(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{
		secrets: map[string]string{
			path: kafkaSecretJSON(t, map[string]any{
				"brokers":   "b-1.msk:9092",
				"username":  "u",
				"password":  kafkaTestPassword,
				"mechanism": "SCRAM-SHA-256",
				"tls":       false,
			}),
		},
	}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.NoError(t, err)
	assert.False(t, creds.TLS)
}

func TestGetModuleKafkaCredentials_ACLPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  map[string]any
		expected []string
	}{
		{
			name: "legacy secret without the aclPrefixes key yields an empty set",
			payload: map[string]any{
				"brokers":   "b-1:9096",
				"username":  "u",
				"password":  kafkaTestPassword,
				"mechanism": "SCRAM-SHA-512",
				"tls":       true,
			},
			expected: []string{},
		},
		{
			name: "empty aclPrefixes yields an empty set",
			payload: map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": "",
			},
			expected: []string{},
		},
		{
			name: "whitespace-only aclPrefixes yields an empty set",
			payload: map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": "   ",
			},
			expected: []string{},
		},
		{
			name: "separator-only aclPrefixes yields an empty set",
			payload: map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": ",,,",
			},
			expected: []string{},
		},
		{
			name: "malformed segments are dropped and members trimmed",
			payload: map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": " ledger. , ,reporter. ,",
			},
			expected: []string{"ledger.", "reporter."},
		},
		{
			name: "single member",
			payload: map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": "ledger.",
			},
			expected: []string{"ledger."},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			client := &mockSecretsManagerClient{
				secrets: map[string]string{path: kafkaSecretJSON(t, tt.payload)},
			}

			creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			require.NoError(t, err)
			require.NotNil(t, creds)

			assert.NotNil(t, creds.ACLPrefixes, "ACLPrefixes must never be nil")
			assert.Equal(t, tt.expected, creds.ACLPrefixes)
			assert.NotContains(t, creds.ACLPrefixes, "*", "an empty prefix set must never become a wildcard")
		})
	}
}

func TestGetModuleKafkaCredentials_BrokersCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		brokers  string
		expected []string
	}{
		{name: "single broker", brokers: "b-1:9096", expected: []string{"b-1:9096"}},
		{name: "multiple brokers", brokers: "b-1:9096,b-2:9096,b-3:9096", expected: []string{"b-1:9096", "b-2:9096", "b-3:9096"}},
		{name: "whitespace and trailing separators dropped", brokers: " b-1:9096 , ,b-2:9096 ,", expected: []string{"b-1:9096", "b-2:9096"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			client := &mockSecretsManagerClient{
				secrets: map[string]string{
					path: kafkaSecretJSON(t, map[string]any{
						"brokers":   tt.brokers,
						"username":  "u",
						"password":  kafkaTestPassword,
						"mechanism": "SCRAM-SHA-512",
						"tls":       true,
					}),
				},
			}

			creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, creds.Brokers)
		})
	}
}

func TestGetModuleKafkaCredentials_MissingRequiredFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload map[string]any
		missing string
	}{
		{
			name:    "missing brokers",
			payload: map[string]any{"username": "u", "password": kafkaTestPassword, "mechanism": "SCRAM-SHA-512"},
			missing: "brokers",
		},
		{
			name:    "whitespace-only brokers",
			payload: map[string]any{"brokers": " , ", "username": "u", "password": kafkaTestPassword, "mechanism": "SCRAM-SHA-512"},
			missing: "brokers",
		},
		{
			name:    "missing username",
			payload: map[string]any{"brokers": "b-1:9096", "password": kafkaTestPassword, "mechanism": "SCRAM-SHA-512"},
			missing: "username",
		},
		{
			name:    "missing password",
			payload: map[string]any{"brokers": "b-1:9096", "username": "u", "mechanism": "SCRAM-SHA-512"},
			missing: "password",
		},
		{
			name:    "missing mechanism",
			payload: map[string]any{"brokers": "b-1:9096", "username": "u", "password": kafkaTestPassword},
			missing: "mechanism",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			client := &mockSecretsManagerClient{
				secrets: map[string]string{path: kafkaSecretJSON(t, tt.payload)},
			}

			creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			require.Error(t, err)
			assert.Nil(t, creds)
			assert.ErrorIs(t, err, ErrKafkaInvalidCredentials)
			assert.Contains(t, err.Error(), tt.missing)
			assert.NotContains(t, err.Error(), kafkaTestPassword)
		})
	}
}

func TestGetModuleKafkaCredentials_SecretAbsent(t *testing.T) {
	t.Parallel()

	client := &mockSecretsManagerClient{secrets: map[string]string{}}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.Error(t, err)
	assert.Nil(t, creds)
	assert.ErrorIs(t, err, ErrKafkaCredentialsNotFound)
	assert.NotContains(t, err.Error(), kafkaTestTenantNo, "the full secret path must be redacted")
}

func TestGetModuleKafkaCredentials_AccessDenied(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{
		errors: map[string]error{path: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"}},
	}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.Error(t, err)
	assert.Nil(t, creds)
	assert.ErrorIs(t, err, ErrKafkaVaultAccessDenied)
}

func TestGetModuleKafkaCredentials_AWSErrorsDoNotLeakTenantIdentity(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	arn := "arn:aws:secretsmanager:us-east-1:123456789012:secret:" + path + "-AbCdEf"

	tests := []struct {
		name           string
		awsErr         error
		expectedErr    error
		retainedSignal string
	}{
		{
			name: "access denied echoes the secret arn",
			awsErr: &smithy.GenericAPIError{
				Code:    "AccessDeniedException",
				Message: "User: arn:aws:sts::123456789012:assumed-role/consumer is not authorized to perform: secretsmanager:GetSecretValue on resource: " + arn,
			},
			expectedErr:    ErrKafkaVaultAccessDenied,
			retainedSignal: "AccessDeniedException",
		},
		{
			name: "expired token echoes the secret arn",
			awsErr: &smithy.GenericAPIError{
				Code:    "ExpiredTokenException",
				Message: "The security token included in the request is expired for " + arn,
			},
			expectedErr:    ErrKafkaVaultAccessDenied,
			retainedSignal: "ExpiredTokenException",
		},
		{
			name:           "infrastructure failure echoes the secret arn",
			awsErr:         errors.New("InternalServiceError while reading " + arn),
			expectedErr:    ErrKafkaRetrievalFailed,
			retainedSignal: "InternalServiceError",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockSecretsManagerClient{errors: map[string]error{path: tt.awsErr}}

			creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			require.Error(t, err)
			assert.Nil(t, creds)
			assert.ErrorIs(t, err, tt.expectedErr)

			message := err.Error()
			assert.NotContains(t, message, kafkaTestTenantNo, "the dash-stripped tenant id must never reach the error message")
			assert.NotContains(t, message, kafkaTestTenantID, "the dashed tenant id must never reach the error message")
			assert.NotContains(t, message, path, "the secret path must never reach the error message")
			assert.NotContains(t, message, kafkaTestModule, "the module must never reach the error message")
			assert.Contains(t, message, redactPath(path), "the redacted path must identify the secret instead")
			assert.Contains(t, message, tt.retainedSignal, "redaction must not destroy the operational signal")
		})
	}
}

func TestGetModuleKafkaCredentials_AWSErrorChainSurvivesRedaction(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{
		errors: map[string]error{
			path: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied on " + path},
		},
	}

	_, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.ErrorIs(t, err, ErrKafkaVaultAccessDenied)

	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "AccessDeniedException", apiErr.ErrorCode())
}

func TestGetModuleKafkaCredentials_RetrievalFailed(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{
		errors: map[string]error{path: errors.New("network unreachable")},
	}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.Error(t, err)
	assert.Nil(t, creds)
	assert.ErrorIs(t, err, ErrKafkaRetrievalFailed)
}

func TestGetModuleKafkaCredentials_BinarySecret(t *testing.T) {
	t.Parallel()

	creds, err := GetModuleKafkaCredentials(context.Background(), &mockBinarySecretsManagerClient{}, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.Error(t, err)
	assert.Nil(t, creds)
	assert.ErrorIs(t, err, ErrKafkaBinarySecretNotSupported)
}

func TestGetModuleKafkaCredentials_MalformedJSON(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{secrets: map[string]string{path: `{"brokers":`}}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	require.Error(t, err)
	assert.Nil(t, creds)
	assert.ErrorIs(t, err, ErrKafkaUnmarshalFailed)
}

func TestGetModuleKafkaCredentials_InvalidInput(t *testing.T) {
	t.Parallel()

	validClient := &mockSecretsManagerClient{secrets: map[string]string{}}

	tests := []struct {
		name        string
		client      SecretsManagerClient
		env         string
		tenantID    string
		module      string
		expectedErr error
	}{
		{name: "nil client", client: nil, env: kafkaTestEnv, tenantID: kafkaTestTenantID, module: kafkaTestModule, expectedErr: ErrKafkaInvalidInput},
		{name: "typed-nil client", client: (*mockSecretsManagerClient)(nil), env: kafkaTestEnv, tenantID: kafkaTestTenantID, module: kafkaTestModule, expectedErr: ErrKafkaInvalidInput},
		{name: "empty tenant id", client: validClient, env: kafkaTestEnv, tenantID: "", module: kafkaTestModule, expectedErr: ErrKafkaInvalidInput},
		{name: "whitespace tenant id", client: validClient, env: kafkaTestEnv, tenantID: "   ", module: kafkaTestModule, expectedErr: ErrKafkaInvalidInput},
		{name: "empty module", client: validClient, env: kafkaTestEnv, tenantID: kafkaTestTenantID, module: "", expectedErr: ErrKafkaInvalidInput},
		{name: "module sanitizes to empty", client: validClient, env: kafkaTestEnv, tenantID: kafkaTestTenantID, module: "-_./", expectedErr: ErrKafkaInvalidInput},
		{name: "tenant id with path traversal", client: validClient, env: kafkaTestEnv, tenantID: "../other", module: kafkaTestModule, expectedErr: ErrKafkaInvalidPathSegment},
		{name: "env with path traversal", client: validClient, env: "../prod", tenantID: kafkaTestTenantID, module: kafkaTestModule, expectedErr: ErrKafkaInvalidPathSegment},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			creds, err := GetModuleKafkaCredentials(context.Background(), tt.client, tt.env, tt.tenantID, tt.module)
			require.Error(t, err)
			assert.Nil(t, creds)
			assert.ErrorIs(t, err, tt.expectedErr)
		})
	}
}

func TestGetModuleKafkaCredentials_EmptyEnvUsesLegacyPath(t *testing.T) {
	t.Parallel()

	path := "tenants/" + kafkaTestTenantNo + "/onboarding/kafka"
	client := &mockSecretsManagerClient{
		secrets: map[string]string{
			path: kafkaSecretJSON(t, map[string]any{
				"brokers":   "b-1:9096",
				"username":  "u",
				"password":  kafkaTestPassword,
				"mechanism": "SCRAM-SHA-512",
				"tls":       true,
			}),
		},
	}

	creds, err := GetModuleKafkaCredentials(context.Background(), client, "", kafkaTestTenantID, kafkaTestModule)
	require.NoError(t, err)
	assert.Equal(t, "u", creds.Username)
}

func TestGetModuleKafkaCredentials_Concurrent(t *testing.T) {
	t.Parallel()

	path := BuildModuleKafkaSecretPath(kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
	client := &mockSecretsManagerClient{
		secrets: map[string]string{
			path: kafkaSecretJSON(t, map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": "ledger.",
			}),
		},
	}

	var wg sync.WaitGroup

	for range 32 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			creds, err := GetModuleKafkaCredentials(context.Background(), client, kafkaTestEnv, kafkaTestTenantID, kafkaTestModule)
			if !assert.NoError(t, err) || !assert.NotNil(t, creds) {
				return
			}

			assert.Equal(t, []string{"ledger."}, creds.ACLPrefixes)
		}()
	}

	wg.Wait()
}

// ============================================================================
// Test: KafkaModuleCredentials redaction
// ============================================================================

func TestKafkaModuleCredentials_RedactsPassword(t *testing.T) {
	t.Parallel()

	creds := KafkaModuleCredentials{
		Brokers:     []string{"b-1:9096"},
		Username:    "onboarding_abc",
		Password:    kafkaTestPassword,
		Mechanism:   "SCRAM-SHA-512",
		TLS:         true,
		ACLPrefixes: []string{"ledger."},
	}

	for _, rendered := range []string{
		creds.String(),
		creds.GoString(),
		fmt.Sprintf("%v", creds),
		fmt.Sprintf("%s", creds),
		fmt.Sprintf("%#v", creds),
		fmt.Sprintf("%v", &creds),
	} {
		assert.NotContains(t, rendered, kafkaTestPassword)
		assert.Contains(t, rendered, constants.ObfuscatedValue)
		assert.Contains(t, rendered, "onboarding_abc")
	}
}

// ============================================================================
// Test: ListModuleKafkaSecrets
// ============================================================================

func TestListModuleKafkaSecrets_PaginatesAcrossPages(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{
		pages: []*secretsmanager.ListSecretsOutput{
			listPage("tok-1",
				"tenants/staging/"+kafkaTestTenantNo+"/onboarding/kafka",
				"tenants/staging/"+kafkaTestTenantNo+"/transaction/kafka",
			),
			listPage("tok-2",
				"tenants/staging/aaaa1111/reportermanager/kafka",
			),
			listPage("",
				"tenants/staging/bbbb2222/crm/kafka",
			),
		},
	}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.NoError(t, err)
	require.Len(t, refs, 4)

	assert.Equal(t, []ModuleKafkaSecretRef{
		{TenantID: kafkaTestTenantNo, Module: "onboarding", SecretPath: "tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka"},
		{TenantID: kafkaTestTenantNo, Module: "transaction", SecretPath: "tenants/staging/" + kafkaTestTenantNo + "/transaction/kafka"},
		{TenantID: "aaaa1111", Module: "reportermanager", SecretPath: "tenants/staging/aaaa1111/reportermanager/kafka"},
		{TenantID: "bbbb2222", Module: "crm", SecretPath: "tenants/staging/bbbb2222/crm/kafka"},
	}, refs)

	calls := client.recordedCalls()
	require.Len(t, calls, 3)

	for i, call := range calls {
		require.Len(t, call.Filters, 1)
		assert.Equal(t, smtypes.FilterNameStringTypeName, call.Filters[0].Key)
		assert.Equal(t, []string{"tenants/staging/"}, call.Filters[0].Values)

		switch i {
		case 0:
			assert.Nil(t, call.NextToken)
		default:
			require.NotNil(t, call.NextToken)
			assert.Equal(t, "tok-"+strconv.Itoa(i), *call.NextToken)
		}
	}
}

func TestListModuleKafkaSecrets_SkipsNonKafkaSiblings(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{
		pages: []*secretsmanager.ListSecretsOutput{
			listPage("",
				"tenants/staging/"+kafkaTestTenantNo+"/onboarding/kafka",
				"tenants/staging/"+kafkaTestTenantNo+"/onboarding/postgres",
				"tenants/staging/"+kafkaTestTenantNo+"/onboarding/mongodb",
				"tenants/staging/"+kafkaTestTenantNo+"/onboarding/rabbitmq",
				"tenants/staging/"+kafkaTestTenantNo+"/plugin-pix/m2m/ledger/credentials",
				"tenants/staging/"+kafkaTestTenantNo+"/plugin-pix/external/stripe/credentials",
				"tenants/production/"+kafkaTestTenantNo+"/onboarding/kafka",
				"tenants/staging/"+kafkaTestTenantNo+"//kafka",
				"tenants/staging//onboarding/kafka",
				"clusters/staging/kafka/shared/admin",
			),
		},
	}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.NoError(t, err)

	assert.Equal(t, []ModuleKafkaSecretRef{
		{TenantID: kafkaTestTenantNo, Module: "onboarding", SecretPath: "tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka"},
	}, refs)
}

func TestListModuleKafkaSecrets_EmptyEnvListsLegacyLayout(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{
		pages: []*secretsmanager.ListSecretsOutput{
			listPage("",
				"tenants/"+kafkaTestTenantNo+"/onboarding/kafka",
				"tenants/staging/"+kafkaTestTenantNo+"/onboarding/kafka",
			),
		},
	}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, "")
	require.NoError(t, err)

	assert.Equal(t, []ModuleKafkaSecretRef{
		{TenantID: kafkaTestTenantNo, Module: "onboarding", SecretPath: "tenants/" + kafkaTestTenantNo + "/onboarding/kafka"},
	}, refs)

	calls := client.recordedCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, []string{"tenants/"}, calls[0].Filters[0].Values)
}

func TestListModuleKafkaSecrets_EmptyResultIsNonNilSlice(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{pages: []*secretsmanager.ListSecretsOutput{listPage("")}}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.NoError(t, err)
	assert.NotNil(t, refs)
	assert.Empty(t, refs)
}

func TestListModuleKafkaSecrets_SkipsEntriesWithNilName(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{
		pages: []*secretsmanager.ListSecretsOutput{
			{SecretList: []smtypes.SecretListEntry{
				{Name: nil},
				{Name: aws.String("tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka")},
			}},
		},
	}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "onboarding", refs[0].Module)
}

func TestListModuleKafkaSecrets_ToleratesNilOutput(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{
		pages: []*secretsmanager.ListSecretsOutput{
			listPage("tok-1", "tenants/staging/"+kafkaTestTenantNo+"/onboarding/kafka"),
			nil,
		},
	}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "onboarding", refs[0].Module)
}

func TestListModuleKafkaSecrets_RejectsRepeatedNextToken(t *testing.T) {
	t.Parallel()

	client := &mockSecretsListerClient{
		pages: []*secretsmanager.ListSecretsOutput{
			listPage("stuck", "tenants/staging/"+kafkaTestTenantNo+"/onboarding/kafka"),
			listPage("stuck", "tenants/staging/"+kafkaTestTenantNo+"/transaction/kafka"),
		},
	}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.Error(t, err)
	assert.Nil(t, refs)
	assert.ErrorIs(t, err, ErrKafkaListFailed)
}

func TestListModuleKafkaSecrets_RejectsCyclingNextTokens(t *testing.T) {
	t.Parallel()

	client := &cyclingTokenListerClient{tokens: []string{"A", "B"}}

	refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
	require.Error(t, err)
	assert.Nil(t, refs)
	assert.ErrorIs(t, err, ErrKafkaListFailed)
	assert.Equal(t, kafkaListMaxPages, client.callCount())
	assert.Contains(t, err.Error(), strconv.Itoa(kafkaListMaxPages))
}

func TestListModuleKafkaSecrets_ClassifiesAWSErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		expectedErr error
	}{
		{
			name:        "access denied",
			err:         &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"},
			expectedErr: ErrKafkaVaultAccessDenied,
		},
		{
			name:        "expired token",
			err:         &smithy.GenericAPIError{Code: "ExpiredTokenException", Message: "expired"},
			expectedErr: ErrKafkaVaultAccessDenied,
		},
		{
			name:        "generic infrastructure failure",
			err:         errors.New("network unreachable"),
			expectedErr: ErrKafkaListFailed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockSecretsListerClient{errs: []error{tt.err}}

			refs, err := ListModuleKafkaSecrets(context.Background(), client, kafkaTestEnv)
			require.Error(t, err)
			assert.Nil(t, refs)
			assert.ErrorIs(t, err, tt.expectedErr)
		})
	}
}

func TestListModuleKafkaSecrets_InvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		client      SecretsListerClient
		env         string
		expectedErr error
	}{
		{name: "nil client", client: nil, env: kafkaTestEnv, expectedErr: ErrKafkaInvalidInput},
		{name: "typed-nil client", client: (*mockSecretsListerClient)(nil), env: kafkaTestEnv, expectedErr: ErrKafkaInvalidInput},
		{name: "env with path traversal", client: &mockSecretsListerClient{}, env: "../prod", expectedErr: ErrKafkaInvalidPathSegment},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			refs, err := ListModuleKafkaSecrets(context.Background(), tt.client, tt.env)
			require.Error(t, err)
			assert.Nil(t, refs)
			assert.ErrorIs(t, err, tt.expectedErr)
		})
	}
}

func TestListModuleKafkaSecrets_RefsFeedTheGetter(t *testing.T) {
	t.Parallel()

	path := "tenants/staging/" + kafkaTestTenantNo + "/onboarding/kafka"
	lister := &mockSecretsListerClient{pages: []*secretsmanager.ListSecretsOutput{listPage("", path)}}

	refs, err := ListModuleKafkaSecrets(context.Background(), lister, kafkaTestEnv)
	require.NoError(t, err)
	require.Len(t, refs, 1)

	getter := &mockSecretsManagerClient{
		secrets: map[string]string{
			path: kafkaSecretJSON(t, map[string]any{
				"brokers":     "b-1:9096",
				"username":    "u",
				"password":    kafkaTestPassword,
				"mechanism":   "SCRAM-SHA-512",
				"tls":         true,
				"aclPrefixes": "ledger.",
			}),
		},
	}

	creds, err := GetModuleKafkaCredentials(context.Background(), getter, kafkaTestEnv, refs[0].TenantID, refs[0].Module)
	require.NoError(t, err)
	assert.Equal(t, []string{"ledger."}, creds.ACLPrefixes)
}
