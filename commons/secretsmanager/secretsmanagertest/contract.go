// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package secretsmanagertest provides backend-agnostic contract tests for
// commons/secretsmanager custody backends. Both the AWS Secrets Manager and the
// Vault KV v2 backends run this shared suite, which is what makes "the backend
// is an infrastructure choice, not a semantic one" a measured claim rather than
// an intention.
//
// The suite deliberately drives the backends through the PUBLIC readers
// (GetM2MCredentials, GetExternalCredentials, GetExternalCredentialsByReference)
// rather than through the raw client, because the property that has to hold is
// what a caller observes: the same reference yields the same document, and the
// same absence yields the same sentinel.
package secretsmanagertest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/secretsmanager"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Backend is one custody backend under test: a reader and the writer that
// stocks it. Both must address the same store.
type Backend struct {
	Reader secretsmanager.SecretsManagerClient
	Writer secretsmanager.SecretWriter
}

// Factory builds a fresh, isolated backend for a single subtest.
type Factory func(t *testing.T) Backend

// Run executes the full custody contract against one backend.
func Run(t *testing.T, newBackend Factory) {
	t.Helper()

	t.Run("M2MCredentialsRoundTrip", func(t *testing.T) { runM2MRoundTrip(t, newBackend(t)) })
	t.Run("ExternalCredentialsRoundTrip", func(t *testing.T) { runExternalRoundTrip(t, newBackend(t)) })
	t.Run("VersionedReferenceRoundTrip", func(t *testing.T) { runVersionedReferenceRoundTrip(t, newBackend(t)) })
	t.Run("AbsentSecretClassifiesAsNotFound", func(t *testing.T) { runAbsentSecret(t, newBackend(t)) })
	t.Run("CreateIsCreateOnly", func(t *testing.T) { runCreateOnly(t, newBackend(t)) })
	t.Run("DeleteRemovesMaterialAndIsIdempotent", func(t *testing.T) { runDelete(t, newBackend(t)) })
	t.Run("EnvironmentSegmentIsolatesReferences", func(t *testing.T) { runEnvironmentIsolation(t, newBackend(t)) })
	t.Run("NonObjectPayloadIsRefused", func(t *testing.T) { runNonObjectPayload(t, newBackend(t)) })
}

const (
	testTenant = "org_01ABCDEF"
	testApp    = "lender"
	testTarget = "matcher"
	testEnv    = "production"

	keyClientID     = "clientId"
	keyClientSecret = "clientSecret"
)

func runM2MRoundTrip(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()
	path := secretsmanager.BuildM2MSecretPath(testEnv, testTenant, testApp, testTarget)

	writeJSON(t, backend, path, map[string]string{
		keyClientID:     "client-abc",
		keyClientSecret: "secret-xyz",
		"targetBaseUrl": "https://matcher.example.com",
	})

	creds, err := secretsmanager.GetM2MCredentials(ctx, backend.Reader, testEnv, testTenant, testApp, testTarget)
	require.NoError(t, err)
	require.Equal(t, "client-abc", creds.ClientID)
	require.Equal(t, "secret-xyz", creds.ClientSecret)
	require.Equal(t, "https://matcher.example.com", creds.TargetBaseURL)
}

func runExternalRoundTrip(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()
	path := secretsmanager.BuildExternalSecretPath(testEnv, testTenant, testApp, "dataprev-cert")
	payload := map[string]string{"certPem": "-----BEGIN CERTIFICATE-----", "keyPem": "-----BEGIN PRIVATE KEY-----"}

	writeJSON(t, backend, path, payload)

	creds, err := secretsmanager.GetExternalCredentials(ctx, backend.Reader, testEnv, testTenant, testApp, "dataprev-cert")
	require.NoError(t, err)
	require.Equal(t, payload, creds)
}

func runVersionedReferenceRoundTrip(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()
	version := uuid.NewString()

	reference, err := secretsmanager.BuildExternalSecretVersionReference(testEnv, testTenant, testApp, "dataprev-oauth", version)
	require.NoError(t, err)

	payload := map[string]string{keyClientID: "rail-client", keyClientSecret: "rail-secret"}
	writeJSON(t, backend, reference.SecretID(), payload)

	creds, err := secretsmanager.GetExternalCredentialsByReference(ctx, backend.Reader, reference)
	require.NoError(t, err)
	require.Equal(t, payload, creds)
}

// runAbsentSecret pins the single most consequential parity property. Callers
// branch on the not-found sentinel to fall back to static configuration; if one
// backend reported an absence as an infrastructure failure instead, that caller
// would fail closed forever on a secret that simply was never written.
func runAbsentSecret(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()

	_, err := secretsmanager.GetM2MCredentials(ctx, backend.Reader, testEnv, testTenant, testApp, "never-written")
	require.ErrorIs(t, err, secretsmanager.ErrM2MCredentialsNotFound)

	_, err = secretsmanager.GetExternalCredentials(ctx, backend.Reader, testEnv, testTenant, testApp, "never-written")
	require.ErrorIs(t, err, secretsmanager.ErrExternalCredentialsNotFound)

	reference, err := secretsmanager.BuildExternalSecretVersionReference(testEnv, testTenant, testApp, "absent", uuid.NewString())
	require.NoError(t, err)

	_, err = secretsmanager.GetExternalCredentialsByReference(ctx, backend.Reader, reference)
	require.ErrorIs(t, err, secretsmanager.ErrExternalCredentialsNotFound)
}

// runCreateOnly proves rotation cannot overwrite. A persisted reference is a
// capability: what it resolved to once, it resolves to until deleted.
func runCreateOnly(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()
	path := secretsmanager.BuildExternalSecretPath(testEnv, testTenant, testApp, "immutable")

	writeJSON(t, backend, path, map[string]string{"value": "first"})

	err := backend.Writer.CreateSecretString(ctx, path, `{"value":"second"}`)
	require.ErrorIs(t, err, secretsmanager.ErrBackendSecretExists)

	creds, err := secretsmanager.GetExternalCredentials(ctx, backend.Reader, testEnv, testTenant, testApp, "immutable")
	require.NoError(t, err)
	require.Equal(t, "first", creds["value"], "a lost create must not have mutated the stored material")
}

func runDelete(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()
	path := secretsmanager.BuildExternalSecretPath(testEnv, testTenant, testApp, "disposable")

	writeJSON(t, backend, path, map[string]string{"value": "present"})
	require.NoError(t, backend.Writer.DeleteSecret(ctx, path))

	_, err := secretsmanager.GetExternalCredentials(ctx, backend.Reader, testEnv, testTenant, testApp, "disposable")
	require.ErrorIs(t, err, secretsmanager.ErrExternalCredentialsNotFound)

	require.NoError(t, backend.Writer.DeleteSecret(ctx, path), "deleting an absent secret must succeed so a retried cleanup converges")
}

// runEnvironmentIsolation pins the environment segment's behaviour on both
// backends. The reference carries the environment, so material written under
// one environment must be unreachable under another — an operator who writes
// under `development` and reads under `production` gets an absence, never
// another environment's credential.
func runEnvironmentIsolation(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()

	// A target of its own: the backends are shared across subtests, and
	// reusing testTarget here would read material another subtest wrote under
	// the production environment, turning a real isolation failure green.
	const target = "env-isolation"

	writeJSON(t, backend, secretsmanager.BuildM2MSecretPath("development", testTenant, testApp, target), map[string]string{
		keyClientID: "dev-client", keyClientSecret: "dev-secret",
	})

	_, err := secretsmanager.GetM2MCredentials(ctx, backend.Reader, "production", testTenant, testApp, target)
	require.ErrorIs(t, err, secretsmanager.ErrM2MCredentialsNotFound)

	creds, err := secretsmanager.GetM2MCredentials(ctx, backend.Reader, "development", testTenant, testApp, target)
	require.NoError(t, err)
	require.Equal(t, "dev-client", creds.ClientID)
}

// runNonObjectPayload keeps the write contract identical across backends: the
// narrower backend sets the rule, so a payload accepted on one is accepted on
// both.
func runNonObjectPayload(t *testing.T, backend Backend) {
	t.Helper()

	ctx := context.Background()
	path := secretsmanager.BuildExternalSecretPath(testEnv, testTenant, testApp, "scalar")

	require.Error(t, backend.Writer.CreateSecretString(ctx, path, `"a bare string"`))
	require.Error(t, backend.Writer.CreateSecretString(ctx, path, `[1,2,3]`))
	require.Error(t, backend.Writer.CreateSecretString(ctx, path, ``))
}

func writeJSON(t *testing.T, backend Backend, secretID string, payload map[string]string) {
	t.Helper()

	document, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, backend.Writer.CreateSecretString(context.Background(), secretID, string(document)))
}
