// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	smithy "github.com/aws/smithy-go"
	"github.com/google/uuid"
)

const (
	externalVersionReferencePartCount      = 9
	externalVersionReferencePartCountNoEnv = 8
	maxSecretIDLength                      = 512
	externalTenantPathSegment              = "tenants"
)

// ExternalCredentialScope identifies the trusted namespace in which an
// external credential reference is allowed to resolve.
type ExternalCredentialScope struct {
	Environment string
	TenantID    string
	Application string
	Target      string
}

// ExternalCredentialReference is a validated, scope-bound capability for an
// external credential version. Its zero value is invalid.
type ExternalCredentialReference struct {
	secretID string
}

// SecretID returns the canonical SecretId for explicit persistence or
// transport. Parse transported values again with
// ParseExternalCredentialReference and a trusted scope before retrieval.
func (r ExternalCredentialReference) SecretID() string {
	return r.secretID
}

// String redacts the SecretId so accidental formatting does not expose tenant
// or integration identifiers.
func (r ExternalCredentialReference) String() string {
	return "ExternalCredentialReference{redacted}"
}

// GoString redacts the SecretId from Go-syntax formatting.
func (r ExternalCredentialReference) GoString() string {
	return r.String()
}

// BuildExternalSecretVersionReference constructs a scope-bound AWS Secrets
// Manager SecretId capability for a version-addressed external credential.
//
// Format:
//
//	tenants/{env}/{tenantOrgID}/{applicationName}/external/{targetService}/credentials/versions/{version}
//	tenants/{tenantOrgID}/{applicationName}/external/{targetService}/credentials/versions/{version}
//
// The environment is optional for compatibility with BuildExternalSecretPath;
// every other segment is required. Version must be a canonical lowercase RFC
// 4122 UUID.
func BuildExternalSecretVersionReference(env, tenantOrgID, applicationName, targetService, version string) (ExternalCredentialReference, error) {
	scope := ExternalCredentialScope{
		Environment: env,
		TenantID:    tenantOrgID,
		Application: applicationName,
		Target:      targetService,
	}
	if err := validateExternalCredentialScope(scope); err != nil {
		return ExternalCredentialReference{}, err
	}

	if err := validateCanonicalExternalVersion(version); err != nil {
		return ExternalCredentialReference{}, err
	}

	parts := []string{externalTenantPathSegment}
	if env != "" {
		parts = append(parts, env)
	}

	parts = append(parts, tenantOrgID, applicationName, "external", targetService, "credentials", "versions", version)
	if !externalReferenceLengthAllowed(parts) {
		return ExternalCredentialReference{}, fmt.Errorf("%w: reference is not canonical", ErrExternalInvalidPathSegment)
	}

	reference := strings.Join(parts, "/")

	return ParseExternalCredentialReference(reference, scope)
}

func externalReferenceLengthAllowed(parts []string) bool {
	length := len(parts) - 1
	for _, part := range parts {
		if length > maxSecretIDLength || len(part) > maxSecretIDLength-length {
			return false
		}

		length += len(part)
	}

	return true
}

// ParseExternalCredentialReference validates a transported reference against a
// trusted namespace and returns a scope-bound capability. It rejects references
// for a different environment, tenant, application, or target.
func ParseExternalCredentialReference(reference string, scope ExternalCredentialScope) (ExternalCredentialReference, error) {
	if strings.TrimSpace(reference) == "" {
		return ExternalCredentialReference{}, fmt.Errorf("%w: reference is required", ErrExternalInvalidInput)
	}

	if len(reference) > maxSecretIDLength {
		return ExternalCredentialReference{}, fmt.Errorf("%w: reference is not canonical", ErrExternalInvalidPathSegment)
	}

	if err := validateExternalCredentialScope(scope); err != nil {
		return ExternalCredentialReference{}, err
	}

	parsed, ok := parseExternalSecretVersionReference(strings.Split(reference, "/"))
	if !ok || !parsed.matches(scope) {
		return ExternalCredentialReference{}, fmt.Errorf("%w: reference is not canonical for scope", ErrExternalInvalidPathSegment)
	}

	if err := validateParsedExternalReference(parsed); err != nil {
		return ExternalCredentialReference{}, err
	}

	return ExternalCredentialReference{secretID: reference}, nil
}

// GetExternalCredentialsByReference fetches a version-addressed external
// credential using a scope-bound capability produced by
// BuildExternalSecretVersionReference or ParseExternalCredentialReference.
//
// The secret must be a non-empty JSON object whose values are non-null strings.
// Binary secrets are not supported. Errors preserve the existing external
// credential sentinels and never include the SecretId or secret payload.
func GetExternalCredentialsByReference(ctx context.Context, client SecretsManagerClient, reference ExternalCredentialReference) (map[string]string, error) {
	if isNilInterface(client) {
		return nil, fmt.Errorf("%w: client is required", ErrExternalInvalidInput)
	}

	if reference.secretID == "" {
		return nil, fmt.Errorf("%w: reference is required", ErrExternalInvalidInput)
	}

	output, err := client.GetSecretValue(ctx, &awssm.GetSecretValueInput{SecretId: aws.String(reference.secretID)})
	if err != nil {
		return nil, classifyExternalReferenceAWSError(err)
	}

	if output == nil || output.SecretString == nil {
		return nil, fmt.Errorf("retrieve external credentials by reference: %w", ErrExternalBinarySecretNotSupported)
	}

	credentials, err := decodeExternalCredentials(*output.SecretString)
	if err != nil {
		return nil, err
	}

	return credentials, nil
}

func decodeExternalCredentials(secret string) (map[string]string, error) {
	raw := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(secret), &raw); err != nil {
		return nil, fmt.Errorf("decode external credentials by reference: %w", ErrExternalUnmarshalFailed)
	}

	if len(raw) == 0 {
		return nil, fmt.Errorf("validate external credentials by reference: %w", ErrExternalInvalidCredentials)
	}

	credentials := make(map[string]string, len(raw))
	for key, value := range raw {
		if string(value) == "null" {
			return nil, fmt.Errorf("decode external credentials by reference: %w", ErrExternalUnmarshalFailed)
		}

		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, fmt.Errorf("decode external credentials by reference: %w", ErrExternalUnmarshalFailed)
		}

		credentials[key] = decoded
	}

	return credentials, nil
}

type externalReferenceSegments struct {
	env             string
	tenantOrgID     string
	applicationName string
	targetService   string
	version         string
}

func (s externalReferenceSegments) matches(scope ExternalCredentialScope) bool {
	return s.env == scope.Environment &&
		s.tenantOrgID == scope.TenantID &&
		s.applicationName == scope.Application &&
		s.targetService == scope.Target
}

func parseExternalSecretVersionReference(parts []string) (externalReferenceSegments, bool) {
	switch len(parts) {
	case externalVersionReferencePartCount:
		if parts[0] != externalTenantPathSegment || parts[1] == "" || parts[4] != "external" ||
			parts[6] != "credentials" || parts[7] != "versions" {
			return externalReferenceSegments{}, false
		}

		return externalReferenceSegments{
			env: parts[1], tenantOrgID: parts[2], applicationName: parts[3],
			targetService: parts[5], version: parts[8],
		}, true
	case externalVersionReferencePartCountNoEnv:
		if parts[0] != externalTenantPathSegment || parts[3] != "external" ||
			parts[5] != "credentials" || parts[6] != "versions" {
			return externalReferenceSegments{}, false
		}

		return externalReferenceSegments{
			tenantOrgID: parts[1], applicationName: parts[2],
			targetService: parts[4], version: parts[7],
		}, true
	default:
		return externalReferenceSegments{}, false
	}
}

func validateExternalCredentialScope(scope ExternalCredentialScope) error {
	segments := []struct {
		name, value string
	}{
		{name: "tenantID", value: scope.TenantID},
		{name: "application", value: scope.Application},
		{name: "target", value: scope.Target},
	}
	if scope.Environment != "" {
		segments = append(segments, struct{ name, value string }{name: "environment", value: scope.Environment})
	}

	for _, segment := range segments {
		if err := validateCanonicalExternalSegment(segment.name, segment.value); err != nil {
			return err
		}
	}

	return nil
}

func validateParsedExternalReference(parsed externalReferenceSegments) error {
	if err := validateCanonicalExternalVersion(parsed.version); err != nil {
		return fmt.Errorf("%w: reference is not canonical", ErrExternalInvalidPathSegment)
	}

	return nil
}

func validateCanonicalExternalSegment(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrExternalInvalidInput, name)
	}

	if value != strings.TrimSpace(value) || strings.Contains(value, "..") {
		return fmt.Errorf("%w: %s is not canonical", ErrExternalInvalidPathSegment, name)
	}

	for _, char := range value {
		if !isAWSSecretNameCharacter(char) {
			return fmt.Errorf("%w: %s is not canonical", ErrExternalInvalidPathSegment, name)
		}
	}

	return nil
}

func isAWSSecretNameCharacter(char rune) bool {
	return char >= 'a' && char <= 'z' ||
		char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' ||
		strings.ContainsRune("_+=.@-", char)
}

func validateCanonicalExternalVersion(version string) error {
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("%w: version is required", ErrExternalInvalidInput)
	}

	parsed, err := uuid.Parse(version)
	if err != nil || parsed.String() != version {
		return fmt.Errorf("%w: version is not canonical", ErrExternalInvalidPathSegment)
	}

	return nil
}

func classifyExternalReferenceAWSError(err error) error {
	var notFoundErr *smtypes.ResourceNotFoundException
	if errors.As(err, &notFoundErr) {
		return fmt.Errorf("retrieve external credentials by reference: %w", ErrExternalCredentialsNotFound)
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && !isNilInterface(apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDeniedException", "ExpiredTokenException":
			return fmt.Errorf("retrieve external credentials by reference: %w", ErrExternalVaultAccessDenied)
		}
	}

	return fmt.Errorf("retrieve external credentials by reference: %w", ErrExternalRetrievalFailed)
}

func isNilableKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return true
	default:
		return false
	}
}
