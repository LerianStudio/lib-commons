//go:build unit

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"testing"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/require"
)

type stubAWSReader struct{}

func (stubAWSReader) GetSecretValue(
	_ context.Context,
	_ *awssm.GetSecretValueInput,
	_ ...func(*awssm.Options),
) (*awssm.GetSecretValueOutput, error) {
	return nil, nil //nolint:nilnil // stub: selection tests never read through it
}

func TestParseBackend(t *testing.T) {
	tests := map[string]struct {
		raw     string
		want    Backend
		wantErr error
	}{
		"empty keeps the current default": {"", BackendAWS, nil},
		"whitespace keeps the default":    {"   ", BackendAWS, nil},
		"aws":                             {"aws", BackendAWS, nil},
		"vault":                           {"vault", BackendVault, nil},
		"case insensitive":                {"VAULT", BackendVault, nil},
		"typo is refused, not defaulted":  {"valut", "", ErrBackendUnknown},
		"other cloud is refused":          {"gcp", "", ErrBackendUnknown},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseBackend(testCase.raw)
			if testCase.wantErr != nil {
				require.ErrorIs(t, err, testCase.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}
}

func TestConfig_NewReaderSelectsBackend(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")

	awsClient := stubAWSReader{}

	reader, err := Config{}.NewReader(awsClient)
	require.NoError(t, err)
	require.IsType(t, stubAWSReader{}, reader, "the zero config must keep the AWS backend")

	reader, err = Config{Backend: BackendAWS}.NewReader(awsClient)
	require.NoError(t, err)
	require.IsType(t, stubAWSReader{}, reader)

	reader, err = Config{
		Backend: BackendVault,
		Vault:   VaultConfig{Address: "https://vault.example.com", Token: "t"},
	}.NewReader(awsClient)
	require.NoError(t, err)
	require.IsType(t, &VaultClient{}, reader)
}

// The no-fallback rule, stated as a test: a broken selection is an error, never
// a quiet switch to the other backend. A credential store that answers from
// somewhere other than where the operator pointed it is a money-path defect.
func TestConfig_NeverFallsBackBetweenBackends(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")

	// Vault selected but unusable: must fail, must NOT return the AWS client
	// that was handed in and is perfectly healthy.
	//
	// The nil assertions compare the INTERFACE directly rather than using
	// require.Nil, which reflects into the interface and would accept a
	// typed-nil *VaultClient wrapped in a non-nil interface — the exact
	// failure a caller checking `if reader != nil` would trip over.
	reader, err := Config{Backend: BackendVault}.NewReader(stubAWSReader{})
	require.ErrorIs(t, err, ErrBackendMisconfigured)
	require.True(t, reader == nil, "a failed construction must return a nil interface, not a typed nil")

	writer, err := Config{Backend: BackendVault}.NewWriter(&fakeAWSWriter{})
	require.ErrorIs(t, err, ErrBackendMisconfigured)
	require.True(t, writer == nil, "a failed construction must return a nil interface, not a typed nil")

	// AWS selected without a client: must fail, must NOT reach for Vault even
	// though Vault settings are present and valid.
	reader, err = Config{
		Backend: BackendAWS,
		Vault:   VaultConfig{Address: "https://vault.example.com", Token: "t"},
	}.NewReader(nil)
	require.ErrorIs(t, err, ErrBackendMisconfigured)
	require.Nil(t, reader)

	writer, err = Config{
		Backend: BackendAWS,
		Vault:   VaultConfig{Address: "https://vault.example.com", Token: "t"},
	}.NewWriter(nil)
	require.ErrorIs(t, err, ErrBackendMisconfigured)
	require.Nil(t, writer)

	// An unknown backend stops the process rather than defaulting.
	reader, err = Config{Backend: "valut"}.NewReader(stubAWSReader{})
	require.ErrorIs(t, err, ErrBackendUnknown)
	require.Nil(t, reader)
}

// A typed-nil AWS client must be caught too: it is not nil to the interface,
// and letting it through would defer the failure to the first credential read.
func TestConfig_RejectsTypedNilAWSClient(t *testing.T) {
	var typedNil *awssm.Client

	_, err := Config{Backend: BackendAWS}.NewReader(typedNil)
	require.ErrorIs(t, err, ErrBackendMisconfigured)

	_, err = Config{Backend: BackendAWS}.NewWriter(typedNil)
	require.ErrorIs(t, err, ErrBackendMisconfigured)
}

func TestConfig_NewWriterSelectsBackend(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")

	writer, err := Config{}.NewWriter(&fakeAWSWriter{})
	require.NoError(t, err)
	require.IsType(t, &awsSecretWriter{}, writer)

	writer, err = Config{
		Backend: BackendVault,
		Vault:   VaultConfig{Address: "https://vault.example.com", Token: "t"},
	}.NewWriter(&fakeAWSWriter{})
	require.NoError(t, err)
	require.IsType(t, &vaultSecretWriter{}, writer)
}
