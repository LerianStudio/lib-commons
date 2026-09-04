//go:build unit

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/require"
)

// fakeVault serves the KV v2 surface the client uses, so classification is
// exercised over a real HTTP round-trip rather than a stubbed method.
type fakeVault struct {
	status int
	body   string
}

func newFakeVaultClient(t *testing.T, handler http.Handler) *VaultClient {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewVaultClient(VaultConfig{Address: server.URL, Token: "test-token"})
	require.NoError(t, err)

	return client
}

func (f fakeVault) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.body))
}

func getSecret(t *testing.T, client *VaultClient, secretID string) (*awssm.GetSecretValueOutput, error) {
	t.Helper()

	return client.GetSecretValue(context.Background(), &awssm.GetSecretValueInput{SecretId: aws.String(secretID)})
}

func TestVaultClient_ReadsKVv2DataAsJSONDocument(t *testing.T) {
	client := newFakeVaultClient(t, fakeVault{
		status: http.StatusOK,
		body:   `{"data":{"data":{"clientId":"abc","clientSecret":"xyz"},"metadata":{"version":1}}}`,
	})

	output, err := getSecret(t, client, "tenants/production/org_1/lender/m2m/matcher/credentials")
	require.NoError(t, err)
	require.NotNil(t, output.SecretString)

	var document map[string]string
	require.NoError(t, json.Unmarshal([]byte(*output.SecretString), &document))
	require.Equal(t, map[string]string{"clientId": "abc", "clientSecret": "xyz"}, document)
}

// The reference is the KV path verbatim. Nothing re-derives the environment
// segment, so a reference cannot resolve outside the scope it was built for.
func TestVaultClient_UsesReferenceVerbatimAsPath(t *testing.T) {
	var requested string

	client := newFakeVaultClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"k":"v"},"metadata":{}}}`))
	}))

	reference := "tenants/production/org_1/br-consignado-gw/external/dataprev-cert/credentials/versions/" +
		"6f1a2b3c-4d5e-4f60-8a9b-0c1d2e3f4a5b"

	_, err := getSecret(t, client, reference)
	require.NoError(t, err)
	require.Equal(t, "/v1/"+DefaultVaultMount+"/data/"+reference, requested)
}

func TestVaultClient_ClassifiesAbsenceAndRefusal(t *testing.T) {
	tests := map[string]struct {
		status  int
		body    string
		wantErr error
	}{
		"missing secret is an absence":    {http.StatusNotFound, `{"errors":[]}`, ErrBackendSecretNotFound},
		"no policy is a refusal":          {http.StatusForbidden, `{"errors":["permission denied"]}`, ErrBackendAccessDenied},
		"missing token is a refusal":      {http.StatusUnauthorized, `{"errors":["missing client token"]}`, ErrBackendAccessDenied},
		"soft-deleted version is absence": {http.StatusOK, `{"data":{"data":null,"metadata":{"destroyed":true}}}`, ErrBackendSecretNotFound},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			client := newFakeVaultClient(t, fakeVault{status: testCase.status, body: testCase.body})

			_, err := getSecret(t, client, "tenants/production/org_1/lender/m2m/matcher/credentials")
			require.ErrorIs(t, err, testCase.wantErr)
		})
	}
}

// This is the property that lets a Vault deployment keep every caller's
// fallback branch working: the readers must translate a Vault absence into the
// SAME sentinel an AWS absence produces.
func TestVaultClient_AbsenceReachesReadersAsDomainNotFound(t *testing.T) {
	client := newFakeVaultClient(t, fakeVault{status: http.StatusNotFound, body: `{"errors":[]}`})
	ctx := context.Background()

	_, err := GetM2MCredentials(ctx, client, "production", "org_1", "lender", "matcher")
	require.ErrorIs(t, err, ErrM2MCredentialsNotFound)

	_, err = GetExternalCredentials(ctx, client, "production", "org_1", "gw", "dataprev")
	require.ErrorIs(t, err, ErrExternalCredentialsNotFound)

	reference, err := BuildExternalSecretVersionReference(
		"production", "org_1", "gw", "dataprev", "6f1a2b3c-4d5e-4f60-8a9b-0c1d2e3f4a5b")
	require.NoError(t, err)

	_, err = GetExternalCredentialsByReference(ctx, client, reference)
	require.ErrorIs(t, err, ErrExternalCredentialsNotFound)
}

func TestVaultClient_RefusalReachesReadersAsAccessDenied(t *testing.T) {
	client := newFakeVaultClient(t, fakeVault{status: http.StatusForbidden, body: `{"errors":["permission denied"]}`})

	_, err := GetM2MCredentials(context.Background(), client, "production", "org_1", "lender", "matcher")
	require.ErrorIs(t, err, ErrM2MVaultAccessDenied)
}

// An AWS-only request option cannot be honoured by Vault. Dropping it silently
// would change the meaning of a credential read without telling anyone.
func TestVaultClient_RefusesAWSRequestOptions(t *testing.T) {
	client := newFakeVaultClient(t, fakeVault{status: http.StatusOK, body: `{"data":{"data":{"k":"v"}}}`})

	_, err := client.GetSecretValue(
		context.Background(),
		&awssm.GetSecretValueInput{SecretId: aws.String("tenants/p/org/app/m2m/t/credentials")},
		func(*awssm.Options) {},
	)
	require.ErrorIs(t, err, ErrBackendOptionsUnsupported)
}

func TestVaultClient_RefusesMissingSecretID(t *testing.T) {
	client := newFakeVaultClient(t, fakeVault{status: http.StatusOK, body: `{}`})
	ctx := context.Background()

	_, err := client.GetSecretValue(ctx, nil)
	require.ErrorIs(t, err, ErrBackendMisconfigured)

	_, err = client.GetSecretValue(ctx, &awssm.GetSecretValueInput{})
	require.ErrorIs(t, err, ErrBackendMisconfigured)

	_, err = client.GetSecretValue(ctx, &awssm.GetSecretValueInput{SecretId: aws.String("   ")})
	require.ErrorIs(t, err, ErrBackendMisconfigured)
}

func TestVaultClient_RefusesZeroValue(t *testing.T) {
	_, err := (&VaultClient{}).GetSecretValue(
		context.Background(),
		&awssm.GetSecretValueInput{SecretId: aws.String(testSecretID)},
	)
	require.ErrorIs(t, err, ErrBackendMisconfigured)
}

// A Vault backend selected without any credential must refuse to build. The
// alternative — an unauthenticated client that 403s on every read — would look
// identical to a policy problem and send an operator hunting in the wrong place.
func TestNewVaultClient_RequiresToken(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")

	_, err := NewVaultClient(VaultConfig{Address: "https://vault.example.com"})
	require.ErrorIs(t, err, ErrBackendMisconfigured)
}

// The address follows Vault's own convention rather than a Lerian-specific one.
func TestNewVaultClient_AddressFollowsVaultConvention(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault.from-env.example.com")
	t.Setenv("VAULT_TOKEN", "")

	client, err := NewVaultClient(VaultConfig{Token: "t"})
	require.NoError(t, err)
	require.Equal(t, "https://vault.from-env.example.com", client.api.Address())

	client, err = NewVaultClient(VaultConfig{Address: "https://explicit.example.com", Token: "t"})
	require.NoError(t, err)
	require.Equal(t, "https://explicit.example.com", client.api.Address())
}

func TestNewVaultClientFrom_DefaultsMountAndRejectsNilClient(t *testing.T) {
	_, err := NewVaultClientFrom(nil, "secret")
	require.ErrorIs(t, err, ErrBackendMisconfigured)

	client := newFakeVaultClient(t, fakeVault{status: http.StatusOK, body: `{"data":{"data":{}}}`})

	wrapped, err := NewVaultClientFrom(client.api, "  ")
	require.NoError(t, err)
	require.Equal(t, DefaultVaultMount, wrapped.mount)

	wrapped, err = NewVaultClientFrom(client.api, "/kv-consignado/")
	require.NoError(t, err)
	require.Equal(t, "kv-consignado", wrapped.mount)
}

func TestVaultClient_ErrorsNeverCarryTheSecretPath(t *testing.T) {
	tenant := "org_supersecret_tenant"

	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		client := newFakeVaultClient(t, fakeVault{
			status: status,
			body:   `{"errors":["failure at tenants/production/` + tenant + `"]}`,
		})

		_, err := getSecret(t, client, "tenants/production/"+tenant+"/lender/m2m/matcher/credentials")
		require.Error(t, err)
		require.NotContains(t, err.Error(), tenant, "a Vault response must not leak the tenant path")
		require.False(t, strings.Contains(err.Error(), "tenants/production/"+tenant))
	}
}
