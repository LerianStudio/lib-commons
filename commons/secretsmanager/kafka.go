// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LerianStudio/lib-observability/v4/constants"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// # Per-module Kafka credentials
//
// Tenant Manager provisions one Kafka SCRAM principal per (tenant, module) pair and
// writes it to AWS Secrets Manager at:
//
//	tenants/{env}/{tenantId}/{module}/kafka
//
// This file is the READER half of that contract and the single source of truth for
// the path convention: BuildModuleKafkaSecretPath here and the Tenant Manager
// provisioner MUST produce byte-identical paths for the same inputs.
//
// The stored payload is deliberately FLAT and all-scalar so External Secrets Operator
// and other key/value consumers can map each top-level key to one environment
// variable:
//
//	{"brokers":"b-1:9096,b-2:9096","username":"onboarding_abc","password":"...",
//	 "mechanism":"SCRAM-SHA-512","tls":true,"aclPrefixes":"ledger.,reporter."}
//
// Both brokers and aclPrefixes are comma-separated STRINGS on the wire, never JSON
// arrays. This reader splits them into slices.
//
// # Usage
//
//	cfg, err := awsconfig.LoadDefaultConfig(ctx)
//	if err != nil {
//	    // handle error
//	}
//	// awssm is github.com/aws/aws-sdk-go-v2/service/secretsmanager, aliased so it
//	// cannot collide with this package's import name.
//	client := awssm.NewFromConfig(cfg)
//
//	refs, err := secretsmanager.ListModuleKafkaSecrets(ctx, client, "staging")
//	if err != nil {
//	    // handle error
//	}
//
//	for _, ref := range refs {
//	    creds, err := secretsmanager.GetModuleKafkaCredentials(ctx, client, "staging", ref.TenantID, ref.Module)
//	    // ...
//	}
//
// # Thread Safety
//
// All functions in this file are safe for concurrent use. No package-level mutable
// state is maintained.

// Sentinel errors for per-module Kafka credential operations. They mirror the M2M
// sentinels but stay distinct so logs and error classification can tell Kafka
// credential failures apart from M2M and external-integration failures.
var (
	// ErrKafkaCredentialsNotFound is returned when no Kafka credential exists at the expected path.
	ErrKafkaCredentialsNotFound = errors.New("kafka module credentials not found")

	// ErrKafkaVaultAccessDenied is returned when access to the vault is denied (missing IAM permissions or expired tokens).
	ErrKafkaVaultAccessDenied = errors.New("vault access denied for kafka module credentials")

	// ErrKafkaRetrievalFailed is returned when Kafka credential retrieval fails due to infrastructure issues.
	ErrKafkaRetrievalFailed = errors.New("failed to retrieve kafka module credentials")

	// ErrKafkaListFailed is returned when enumerating Kafka credential secrets fails.
	ErrKafkaListFailed = errors.New("failed to list kafka module credentials")

	// ErrKafkaUnmarshalFailed is returned when the secret value cannot be deserialized into the flat Kafka payload.
	ErrKafkaUnmarshalFailed = errors.New("failed to unmarshal kafka module credentials")

	// ErrKafkaInvalidInput is returned when required input parameters are missing.
	ErrKafkaInvalidInput = errors.New("invalid input for kafka module credentials")

	// ErrKafkaInvalidCredentials is returned when retrieved credentials are missing fields required to connect.
	ErrKafkaInvalidCredentials = errors.New("incomplete kafka module credentials")

	// ErrKafkaBinarySecretNotSupported is returned when the secret is stored as binary data rather than a string.
	ErrKafkaBinarySecretNotSupported = errors.New("binary secrets are not supported for kafka module credentials")

	// ErrKafkaInvalidPathSegment is returned when a path segment contains path traversal characters.
	ErrKafkaInvalidPathSegment = errors.New("invalid path segment for kafka module credentials")
)

const (
	tenantsPathRoot     = "tenants"
	kafkaSecretLeaf     = "kafka"
	kafkaCSVSeparator   = ","
	kafkaListMaxResults = 100
	kafkaListMaxPages   = 10_000

	kafkaPathSegmentCount    = 4
	kafkaEnvPathSegmentCount = 5
)

// SanitizeKafkaSegment normalizes a service or module name into the Kafka-safe
// identifier used both as the SCRAM principal segment and as the module segment of
// the credential secret path: it lowercases the input and strips every rune outside
// [a-z0-9], so "Reporter-Manager" yields "reportermanager".
//
// The rule is deliberately strict because '.' is the ACL prefix boundary and '_' is
// the SCRAM username segment separator. The function is idempotent.
//
// Distinct source names can normalize to the SAME segment ("sub-module" and
// "submodule"), which would make them share a principal, ACL prefix, and secret
// path. Guarding against that belongs to name creation upstream, not here.
func SanitizeKafkaSegment(s string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}

	return b.String()
}

// BuildModuleKafkaSecretPath constructs the per-(tenant, module) Kafka credential
// secret path.
//
// Format: tenants/{env}/{tenantId}/{module}/kafka
//
// When env is empty the environment segment is omitted for backward compatibility,
// consistent with BuildM2MSecretPath.
//
// The tenantID is dash-stripped and the module is normalized via
// SanitizeKafkaSegment. This MUST stay byte-identical to the Tenant Manager
// provisioner's builder for the same inputs — writer and reader share this one
// implementation so they cannot drift. An empty module yields an empty segment
// rather than an error, matching the sibling resource builders; callers that need
// rejection get it from GetModuleKafkaCredentials.
func BuildModuleKafkaSecretPath(env, tenantID, module string) string {
	envPrefix := ""
	if env != "" {
		envPrefix = env + "/"
	}

	safeTenantID := strings.ReplaceAll(tenantID, "-", "")

	return fmt.Sprintf("%s/%s%s/%s/%s", tenantsPathRoot, envPrefix, safeTenantID, SanitizeKafkaSegment(module), kafkaSecretLeaf)
}

// KafkaModuleCredentials is the decoded per-(tenant, module) Kafka credential.
//
// Brokers and ACLPrefixes arrive as comma-separated strings on the wire and are
// split here; both are always non-nil so callers never have to distinguish nil from
// empty.
//
// ACLPrefixes names the "{service}." Kafka ACL prefixes this principal is authorized
// on, for TOPIC and consumer GROUP alike. A consumer cannot derive them from the
// credential itself because the Kafka identity is {module}_{tenantId} and carries no
// service. An EMPTY set means the secret says the principal is authorized on
// nothing — it is never an error here and never a wildcard. Acting on that (which
// must be fail-closed) is the consumer's decision, not this layer's.
type KafkaModuleCredentials struct {
	Brokers     []string
	Username    string
	Password    string // #nosec G117 -- secret payload deserialized from AWS Secrets Manager, redacted by String/GoString
	Mechanism   string
	TLS         bool
	ACLPrefixes []string
}

// String redacts secret material from formatted output.
func (c KafkaModuleCredentials) String() string {
	return fmt.Sprintf(
		"KafkaModuleCredentials{Brokers:%v, Username:%q, Password:%s, Mechanism:%q, TLS:%t, ACLPrefixes:%v}",
		c.Brokers, c.Username, constants.ObfuscatedValue, c.Mechanism, c.TLS, c.ACLPrefixes,
	)
}

// GoString redacts secret material from Go-syntax formatted output.
func (c KafkaModuleCredentials) GoString() string {
	return c.String()
}

// kafkaModuleCredentialSecret is the flat, all-scalar payload Tenant Manager writes.
type kafkaModuleCredentialSecret struct {
	Brokers     string `json:"brokers"`
	Username    string `json:"username"`
	Password    string `json:"password"` // #nosec G117 -- secret payload deserialized from AWS Secrets Manager
	Mechanism   string `json:"mechanism"`
	TLS         bool   `json:"tls"`
	ACLPrefixes string `json:"aclPrefixes"`
}

// ModuleKafkaSecretRef identifies one discovered per-(tenant, module) Kafka
// credential. TenantID is the dash-stripped form carried by the path.
type ModuleKafkaSecretRef struct {
	TenantID   string
	Module     string
	SecretPath string
}

// SecretsListerClient abstracts the AWS Secrets Manager enumeration call. It is kept
// separate from SecretsManagerClient so existing implementers of that interface do
// not have to grow a method they never use; the real *secretsmanager.Client
// satisfies both.
type SecretsListerClient interface {
	ListSecrets(ctx context.Context, params *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

// GetModuleKafkaCredentials fetches the per-(tenant, module) Kafka credential from
// AWS Secrets Manager.
//
// Parameters:
//   - ctx: context for cancellation and tracing
//   - client: AWS Secrets Manager client (must not be nil)
//   - env: deployment environment (e.g., "staging"); empty is accepted for backward compatibility
//   - tenantID: the tenant's internal UUID, with or without dashes; must not be empty
//   - module: the module name, raw or already sanitized; must not sanitize to empty
//
// Brokers, username, password and mechanism are all required to open a SASL
// connection, so a payload missing any of them yields ErrKafkaInvalidCredentials
// rather than a partially usable credential. An absent or empty aclPrefixes is NOT
// an error — it decodes to an empty slice.
//
// Secret paths are redacted in returned errors and the password never appears in
// any error or formatted output.
//
// Safe for concurrent use.
func GetModuleKafkaCredentials(ctx context.Context, client SecretsManagerClient, env, tenantID, module string) (*KafkaModuleCredentials, error) {
	if isNilInterface(client) {
		return nil, fmt.Errorf("%w: client is required", ErrKafkaInvalidInput)
	}

	cleanTenantID, err := validatePathSegmentWithErrors("tenantID", tenantID, ErrKafkaInvalidInput, ErrKafkaInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	cleanModule := SanitizeKafkaSegment(module)
	if cleanModule == "" {
		return nil, fmt.Errorf("%w: module is required and must contain at least one [a-z0-9] character", ErrKafkaInvalidInput)
	}

	cleanEnv, err := validateOptionalEnv(env, ErrKafkaInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	secretPath := BuildModuleKafkaSecretPath(cleanEnv, cleanTenantID, cleanModule)
	redacted := redactPath(secretPath)

	output, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretPath)})
	if err != nil {
		return nil, classifyAWSErrorWithSentinels(err, secretPath, ErrKafkaCredentialsNotFound, ErrKafkaVaultAccessDenied, ErrKafkaRetrievalFailed)
	}

	if output == nil || output.SecretString == nil {
		return nil, fmt.Errorf("%w: secret at %s is binary or nil", ErrKafkaBinarySecretNotSupported, redacted)
	}

	var wire kafkaModuleCredentialSecret
	if err := json.Unmarshal([]byte(*output.SecretString), &wire); err != nil {
		return nil, fmt.Errorf("%w: secret at %s: %w", ErrKafkaUnmarshalFailed, redacted, err)
	}

	creds := KafkaModuleCredentials{
		Brokers:     splitKafkaCSV(wire.Brokers),
		Username:    strings.TrimSpace(wire.Username),
		Password:    wire.Password,
		Mechanism:   strings.TrimSpace(wire.Mechanism),
		TLS:         wire.TLS,
		ACLPrefixes: splitKafkaCSV(wire.ACLPrefixes),
	}

	if missing := missingKafkaCredentialFields(creds); len(missing) > 0 {
		return nil, fmt.Errorf("%w: secret at %s: missing fields: %s", ErrKafkaInvalidCredentials, redacted, strings.Join(missing, ", "))
	}

	return &creds, nil
}

// ListModuleKafkaSecrets enumerates every (tenant, module) pair that has a Kafka
// credential in the given environment, paginating through AWS Secrets Manager with a
// name-prefix filter on tenants/{env}/ (or tenants/ when env is empty).
//
// This is a DISCOVERY mechanism: there is no other tenant or module inventory to
// consult, so newly provisioned tenants and modules appear here automatically.
//
// Sibling secrets living under the same prefix — .../{module}/postgres, /mongodb,
// /rabbitmq and the 7-segment .../m2m|external/.../credentials paths — are skipped,
// as is anything whose shape does not match the Kafka convention for the requested
// environment. Nothing outside the Kafka credential set is ever returned or read.
//
// Results are sorted by secret path and the slice is non-nil even when empty.
//
// Safe for concurrent use.
func ListModuleKafkaSecrets(ctx context.Context, client SecretsListerClient, env string) ([]ModuleKafkaSecretRef, error) {
	if isNilInterface(client) {
		return nil, fmt.Errorf("%w: client is required", ErrKafkaInvalidInput)
	}

	cleanEnv, err := validateOptionalEnv(env, ErrKafkaInvalidPathSegment)
	if err != nil {
		return nil, err
	}

	prefix := buildModuleKafkaSecretPrefix(cleanEnv)
	refs := make([]ModuleKafkaSecretRef, 0)

	var token *string

	for page := 0; ; page++ {
		if page >= kafkaListMaxPages {
			return nil, fmt.Errorf("%w: pagination exceeded %d pages for prefix %q", ErrKafkaListFailed, kafkaListMaxPages, prefix)
		}

		output, err := client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{
			Filters:    []smtypes.Filter{{Key: smtypes.FilterNameStringTypeName, Values: []string{prefix}}},
			MaxResults: aws.Int32(kafkaListMaxResults),
			NextToken:  token,
		})
		if err != nil {
			return nil, classifyKafkaListError(err, prefix)
		}

		if output == nil {
			break
		}

		for _, entry := range output.SecretList {
			if entry.Name == nil {
				continue
			}

			if ref, ok := ParseModuleKafkaSecretPath(cleanEnv, *entry.Name); ok {
				refs = append(refs, ref)
			}
		}

		next := aws.ToString(output.NextToken)
		if next == "" {
			break
		}

		if token != nil && *token == next {
			return nil, fmt.Errorf("%w: pagination stalled on a repeated token for prefix %q", ErrKafkaListFailed, prefix)
		}

		token = output.NextToken
	}

	slices.SortFunc(refs, func(a, b ModuleKafkaSecretRef) int {
		return strings.Compare(a.SecretPath, b.SecretPath)
	})

	return refs, nil
}

// ParseModuleKafkaSecretPath extracts the tenant and module from a Kafka credential
// secret path, reporting false for any path that is not a Kafka credential for the
// requested environment.
//
// It is the exact inverse of BuildModuleKafkaSecretPath: the path must have the
// expected segment count for the environment (5 with env, 4 without), start at
// tenants, end at kafka, carry the requested environment, have non-empty tenant
// and module segments, and carry those segments in the only forms the builder
// ever writes: a dash-free tenant and a module already in its
// SanitizeKafkaSegment form. Sibling resource secrets, the deeper m2m/external
// credential paths, and paths with a hyphenated tenant or unsanitized module
// segment therefore never parse, and every returned ref is usable as an input to
// GetModuleKafkaCredentials.
func ParseModuleKafkaSecretPath(env, secretPath string) (ModuleKafkaSecretRef, bool) {
	cleanEnv := strings.TrimSpace(env)

	expected := kafkaPathSegmentCount
	if cleanEnv != "" {
		expected = kafkaEnvPathSegmentCount
	}

	segments := strings.Split(secretPath, "/")
	if len(segments) != expected {
		return ModuleKafkaSecretRef{}, false
	}

	if segments[0] != tenantsPathRoot || segments[len(segments)-1] != kafkaSecretLeaf {
		return ModuleKafkaSecretRef{}, false
	}

	if cleanEnv != "" && segments[1] != cleanEnv {
		return ModuleKafkaSecretRef{}, false
	}

	tenantID := segments[len(segments)-3]

	module := segments[len(segments)-2]
	if tenantID == "" || module == "" {
		return ModuleKafkaSecretRef{}, false
	}

	// BuildModuleKafkaSecretPath dash-strips the tenant segment and writes the
	// module segment through SanitizeKafkaSegment, so a segment that does not
	// round-trip unchanged was not written by the builder. Accepting it would hand
	// back a ref that GetModuleKafkaCredentials re-canonicalizes into a DIFFERENT
	// path — a not-found (or wrong-tenant/wrong-module read) for a secret this
	// parse just discovered.
	if strings.ReplaceAll(tenantID, "-", "") != tenantID {
		return ModuleKafkaSecretRef{}, false
	}

	if SanitizeKafkaSegment(module) != module {
		return ModuleKafkaSecretRef{}, false
	}

	return ModuleKafkaSecretRef{TenantID: tenantID, Module: module, SecretPath: secretPath}, true
}

// buildModuleKafkaSecretPrefix returns the name-prefix filter that scopes a
// ListSecrets sweep to one environment's tenant secrets.
func buildModuleKafkaSecretPrefix(env string) string {
	if env == "" {
		return tenantsPathRoot + "/"
	}

	return tenantsPathRoot + "/" + env + "/"
}

// splitKafkaCSV decodes a comma-separated field into its members, trimming
// surrounding whitespace and dropping empty segments. An empty or whitespace-only
// input decodes to an empty, non-nil slice — never nil, never an error, and never a
// wildcard.
func splitKafkaCSV(csv string) []string {
	segments := strings.Split(csv, kafkaCSVSeparator)
	out := make([]string, 0, len(segments))

	for _, segment := range segments {
		if s := strings.TrimSpace(segment); s != "" {
			out = append(out, s)
		}
	}

	return out
}

// missingKafkaCredentialFields lists the fields a consumer cannot connect without.
// aclPrefixes is deliberately absent: an empty authorization set is valid data.
func missingKafkaCredentialFields(creds KafkaModuleCredentials) []string {
	missing := make([]string, 0, 4)

	if len(creds.Brokers) == 0 {
		missing = append(missing, "brokers")
	}

	if creds.Username == "" {
		missing = append(missing, "username")
	}

	if creds.Password == "" {
		missing = append(missing, "password")
	}

	if creds.Mechanism == "" {
		missing = append(missing, "mechanism")
	}

	return missing
}

// classifyKafkaListError maps AWS SDK errors from ListSecrets to Kafka sentinels.
// The prefix is non-sensitive (it carries no tenant identity) so it is reported
// verbatim to keep the operational signal readable.
func classifyKafkaListError(err error, prefix string) error {
	if isVaultAccessDeniedError(err) {
		return fmt.Errorf("%w: %w", ErrKafkaVaultAccessDenied, err)
	}

	return fmt.Errorf("%w: prefix %q: %w", ErrKafkaListFailed, prefix, err)
}
