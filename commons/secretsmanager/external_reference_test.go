//go:build unit

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testExternalVersion                 = "550e8400-e29b-41d4-a716-446655440000"
	testExternalVersionReference        = "tenants/staging/org_01ABC/br-consignado-gw/external/dataprev/credentials/versions/" + testExternalVersion
	testExternalVersionReferenceNoEnv   = "tenants/org_01ABC/br-consignado-gw/external/dataprev/credentials/versions/" + testExternalVersion
	testExternalSensitiveUpstreamSecret = "upstream-secret-must-not-leak"
)

var testExternalScope = ExternalCredentialScope{
	Environment: "staging",
	TenantID:    "org_01ABC",
	Application: "br-consignado-gw",
	Target:      "dataprev",
}

type referenceSecretsManagerClient struct {
	output       *awssm.GetSecretValueOutput
	err          error
	requestedID  string
	versionID    *string
	versionStage *string
}

func (m *referenceSecretsManagerClient) GetSecretValue(
	_ context.Context,
	params *awssm.GetSecretValueInput,
	_ ...func(*awssm.Options),
) (*awssm.GetSecretValueOutput, error) {
	if params != nil && params.SecretId != nil {
		m.requestedID = *params.SecretId
		m.versionID = params.VersionId
		m.versionStage = params.VersionStage
	}

	return m.output, m.err
}

type typedNilAPIError struct{}

func (*typedNilAPIError) Error() string                 { return "typed nil API error" }
func (*typedNilAPIError) ErrorCode() string             { panic("ErrorCode called on typed nil") }
func (*typedNilAPIError) ErrorMessage() string          { return "typed nil API error" }
func (*typedNilAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestBuildExternalSecretVersionReference(t *testing.T) {
	t.Parallel()

	reference, err := BuildExternalSecretVersionReference(
		testExternalScope.Environment,
		testExternalScope.TenantID,
		testExternalScope.Application,
		testExternalScope.Target,
		testExternalVersion,
	)

	require.NoError(t, err)
	assert.Equal(t, testExternalVersionReference, reference.SecretID())
	assert.NotContains(t, fmt.Sprintf("%v %#v", reference, reference), testExternalVersionReference)
}

func TestBuildExternalSecretVersionReference_WithoutEnvironment(t *testing.T) {
	t.Parallel()

	reference, err := BuildExternalSecretVersionReference("", "org_01ABC", "br-consignado-gw", "dataprev", testExternalVersion)

	require.NoError(t, err)
	assert.Equal(t, testExternalVersionReferenceNoEnv, reference.SecretID())
}

func TestExternalCredentialReference_AcceptsExactly512Bytes(t *testing.T) {
	t.Parallel()

	probe, err := BuildExternalSecretVersionReference("e", "org", "app", "target", testExternalVersion)
	require.NoError(t, err)
	environment := strings.Repeat("e", maxSecretIDLength-len(probe.SecretID())+1)

	built, err := BuildExternalSecretVersionReference(environment, "org", "app", "target", testExternalVersion)
	require.NoError(t, err)
	require.Len(t, built.SecretID(), maxSecretIDLength)

	parsed, err := ParseExternalCredentialReference(built.SecretID(), ExternalCredentialScope{
		Environment: environment,
		TenantID:    "org",
		Application: "app",
		Target:      "target",
	})
	require.NoError(t, err)
	assert.Equal(t, built.SecretID(), parsed.SecretID())
}

func TestBuildExternalSecretVersionReference_RejectsInvalidSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, env, tenant, app, target, version string
		want                                    error
	}{
		{name: "empty tenant", env: "staging", tenant: "", app: "app", target: "target", version: testExternalVersion, want: ErrExternalInvalidInput},
		{name: "empty application", env: "staging", tenant: "org", app: "", target: "target", version: testExternalVersion, want: ErrExternalInvalidInput},
		{name: "empty target", env: "staging", tenant: "org", app: "app", target: "", version: testExternalVersion, want: ErrExternalInvalidInput},
		{name: "empty version", env: "staging", tenant: "org", app: "app", target: "target", version: "", want: ErrExternalInvalidInput},
		{name: "traversal", env: "staging", tenant: "../org", app: "app", target: "target", version: testExternalVersion, want: ErrExternalInvalidPathSegment},
		{name: "slash", env: "staging", tenant: "org", app: "app/name", target: "target", version: testExternalVersion, want: ErrExternalInvalidPathSegment},
		{name: "control", env: "staging", tenant: "org", app: "app", target: "target", version: testExternalVersion + "\n", want: ErrExternalInvalidPathSegment},
		{name: "noncanonical whitespace", env: " staging", tenant: "org", app: "app", target: "target", version: testExternalVersion, want: ErrExternalInvalidPathSegment},
		{name: "invalid AWS character", env: "staging", tenant: "org", app: "app#bad", target: "target", version: testExternalVersion, want: ErrExternalInvalidPathSegment},
		{name: "version not UUID", env: "staging", tenant: "org", app: "app", target: "target", version: "v1", want: ErrExternalInvalidPathSegment},
		{name: "UUID not lowercase", env: "staging", tenant: "org", app: "app", target: "target", version: strings.ToUpper(testExternalVersion), want: ErrExternalInvalidPathSegment},
		{name: "SecretId over 512 bytes", env: strings.Repeat("e", 450), tenant: "org", app: "app", target: "target", version: testExternalVersion, want: ErrExternalInvalidPathSegment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reference, err := BuildExternalSecretVersionReference(tt.env, tt.tenant, tt.app, tt.target, tt.version)

			require.ErrorIs(t, err, tt.want)
			assert.Empty(t, reference.SecretID())
		})
	}
}

func TestParseExternalCredentialReference_BindsTrustedScope(t *testing.T) {
	t.Parallel()

	reference, err := ParseExternalCredentialReference(testExternalVersionReference, testExternalScope)
	require.NoError(t, err)
	assert.Equal(t, testExternalVersionReference, reference.SecretID())

	withoutEnv := testExternalScope
	withoutEnv.Environment = ""
	reference, err = ParseExternalCredentialReference(testExternalVersionReferenceNoEnv, withoutEnv)
	require.NoError(t, err)
	assert.Equal(t, testExternalVersionReferenceNoEnv, reference.SecretID())
}

func TestParseExternalCredentialReference_RejectsWrongScope(t *testing.T) {
	t.Parallel()

	tests := []ExternalCredentialScope{
		{Environment: "production", TenantID: "org_01ABC", Application: "br-consignado-gw", Target: "dataprev"},
		{Environment: "staging", TenantID: "org_other", Application: "br-consignado-gw", Target: "dataprev"},
		{Environment: "staging", TenantID: "org_01ABC", Application: "other-app", Target: "dataprev"},
		{Environment: "staging", TenantID: "org_01ABC", Application: "br-consignado-gw", Target: "other-target"},
	}

	for _, scope := range tests {
		reference, err := ParseExternalCredentialReference(testExternalVersionReference, scope)
		require.ErrorIs(t, err, ErrExternalInvalidPathSegment)
		assert.Empty(t, reference.SecretID())
		assert.NotContains(t, err.Error(), testExternalVersionReference)
	}
}

func TestParseExternalCredentialReference_RejectsMalformedReferenceAndScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, reference string
		scope           ExternalCredentialScope
		want            error
	}{
		{name: "empty", reference: "", scope: testExternalScope, want: ErrExternalInvalidInput},
		{name: "too long", reference: strings.Repeat("a", maxSecretIDLength+1), scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "wrong shape", reference: "tenants/staging/org/app/external/target/credentials", scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "extra part", reference: testExternalVersionReference + "/extra", scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "traversal", reference: "tenants/staging/org/../external/dataprev/credentials/versions/" + testExternalVersion, scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "control", reference: testExternalVersionReference + "\n", scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "wrong context", reference: strings.Replace(testExternalVersionReference, "/external/", "/m2m/", 1), scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "wrong context without environment", reference: "tenants/org_01ABC/br-consignado-gw/m2m/dataprev/credentials/versions/" + testExternalVersion, scope: ExternalCredentialScope{TenantID: "org_01ABC", Application: "br-consignado-gw", Target: "dataprev"}, want: ErrExternalInvalidPathSegment},
		{name: "empty env slot", reference: strings.Replace(testExternalVersionReference, "/staging/", "//", 1), scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "uppercase UUID", reference: strings.Replace(testExternalVersionReference, testExternalVersion, strings.ToUpper(testExternalVersion), 1), scope: testExternalScope, want: ErrExternalInvalidPathSegment},
		{name: "invalid trusted tenant", reference: testExternalVersionReference, scope: ExternalCredentialScope{Environment: "staging", TenantID: "../org", Application: "app", Target: "target"}, want: ErrExternalInvalidPathSegment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reference, err := ParseExternalCredentialReference(tt.reference, tt.scope)

			require.ErrorIs(t, err, tt.want)
			assert.Empty(t, reference.SecretID())
		})
	}
}

func TestGetExternalCredentialsByReference(t *testing.T) {
	t.Parallel()

	reference, err := ParseExternalCredentialReference(testExternalVersionReference, testExternalScope)
	require.NoError(t, err)
	client := &referenceSecretsManagerClient{output: &awssm.GetSecretValueOutput{
		SecretString: aws.String(`{"client_id":"client-123","client_secret":"secret-456"}`),
	}}

	credentials, err := GetExternalCredentialsByReference(context.Background(), client, reference)

	require.NoError(t, err)
	assert.Equal(t, testExternalVersionReference, client.requestedID)
	assert.Nil(t, client.versionID)
	assert.Nil(t, client.versionStage)
	assert.Equal(t, map[string]string{"client_id": "client-123", "client_secret": "secret-456"}, credentials)
}

func TestGetExternalCredentialsByReference_InvalidInputs(t *testing.T) {
	t.Parallel()

	reference, err := ParseExternalCredentialReference(testExternalVersionReference, testExternalScope)
	require.NoError(t, err)

	credentials, err := GetExternalCredentialsByReference(context.Background(), nil, reference)
	require.ErrorIs(t, err, ErrExternalInvalidInput)
	assert.Nil(t, credentials)

	var typedNil *referenceSecretsManagerClient
	credentials, err = GetExternalCredentialsByReference(context.Background(), typedNil, reference)
	require.ErrorIs(t, err, ErrExternalInvalidInput)
	assert.Nil(t, credentials)

	credentials, err = GetExternalCredentialsByReference(context.Background(), &referenceSecretsManagerClient{}, ExternalCredentialReference{})
	require.ErrorIs(t, err, ErrExternalInvalidInput)
	assert.Nil(t, credentials)
}

func TestGetExternalCredentialsByReference_AWSErrorsDoNotLeak(t *testing.T) {
	t.Parallel()

	reference, err := ParseExternalCredentialReference(testExternalVersionReference, testExternalScope)
	require.NoError(t, err)
	var nilAPIError *typedNilAPIError
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: &smtypes.ResourceNotFoundException{Message: aws.String(testExternalVersionReference + testExternalSensitiveUpstreamSecret)}, want: ErrExternalCredentialsNotFound},
		{name: "access denied", err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: testExternalVersionReference + testExternalSensitiveUpstreamSecret}, want: ErrExternalVaultAccessDenied},
		{name: "expired token", err: &smithy.GenericAPIError{Code: "ExpiredTokenException", Message: testExternalVersionReference + testExternalSensitiveUpstreamSecret}, want: ErrExternalVaultAccessDenied},
		{name: "generic", err: errors.New(testExternalVersionReference + testExternalSensitiveUpstreamSecret), want: ErrExternalRetrievalFailed},
		{name: "typed nil API error", err: nilAPIError, want: ErrExternalRetrievalFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			credentials, gotErr := GetExternalCredentialsByReference(context.Background(), &referenceSecretsManagerClient{err: tt.err}, reference)

			require.ErrorIs(t, gotErr, tt.want)
			assert.Nil(t, credentials)
			assert.NotContains(t, gotErr.Error(), testExternalVersionReference)
			assert.NotContains(t, gotErr.Error(), testExternalSensitiveUpstreamSecret)
		})
	}
}

func TestGetExternalCredentialsByReference_RejectsInvalidSecretValues(t *testing.T) {
	t.Parallel()

	reference, err := ParseExternalCredentialReference(testExternalVersionReference, testExternalScope)
	require.NoError(t, err)
	tests := []struct {
		name   string
		output *awssm.GetSecretValueOutput
		want   error
	}{
		{name: "nil output", want: ErrExternalBinarySecretNotSupported},
		{name: "missing SecretString", output: &awssm.GetSecretValueOutput{SecretBinary: []byte("secret")}, want: ErrExternalBinarySecretNotSupported},
		{name: "empty string", output: &awssm.GetSecretValueOutput{SecretString: aws.String("")}, want: ErrExternalUnmarshalFailed},
		{name: "malformed JSON", output: &awssm.GetSecretValueOutput{SecretString: aws.String(`{"token":"sensitive-value"`)}, want: ErrExternalUnmarshalFailed},
		{name: "JSON null", output: &awssm.GetSecretValueOutput{SecretString: aws.String(`null`)}, want: ErrExternalInvalidCredentials},
		{name: "empty object", output: &awssm.GetSecretValueOutput{SecretString: aws.String(`{}`)}, want: ErrExternalInvalidCredentials},
		{name: "array", output: &awssm.GetSecretValueOutput{SecretString: aws.String(`[]`)}, want: ErrExternalUnmarshalFailed},
		{name: "null field", output: &awssm.GetSecretValueOutput{SecretString: aws.String(`{"token":null}`)}, want: ErrExternalUnmarshalFailed},
		{name: "non-string", output: &awssm.GetSecretValueOutput{SecretString: aws.String(`{"token":42}`)}, want: ErrExternalUnmarshalFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			credentials, gotErr := GetExternalCredentialsByReference(context.Background(), &referenceSecretsManagerClient{output: tt.output}, reference)

			require.ErrorIs(t, gotErr, tt.want)
			assert.Nil(t, credentials)
			assert.NotContains(t, gotErr.Error(), "sensitive-value")
		})
	}
}

func TestIsNilInterface_AllNilableKinds(t *testing.T) {
	t.Parallel()

	var pointer *referenceSecretsManagerClient
	var channel chan struct{}
	var function func()
	var mapping map[string]string
	var slice []string
	var unsafePointer unsafe.Pointer
	for name, value := range map[string]any{
		"pointer": pointer, "channel": channel, "function": function,
		"map": mapping, "slice": slice, "unsafe pointer": unsafePointer,
	} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, isNilInterface(value))
		})
	}
	assert.False(t, isNilInterface(0))
}
