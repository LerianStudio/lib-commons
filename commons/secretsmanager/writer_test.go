//go:build unit

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/require"
)

type fakeAWSWriter struct {
	createErr   error
	deleteErr   error
	createdID   string
	createdJSON string
	deletedID   string
	forceDelete bool
}

func (f *fakeAWSWriter) CreateSecret(
	_ context.Context,
	params *awssm.CreateSecretInput,
	_ ...func(*awssm.Options),
) (*awssm.CreateSecretOutput, error) {
	if params != nil && params.Name != nil {
		f.createdID = *params.Name
	}

	if params != nil && params.SecretString != nil {
		f.createdJSON = *params.SecretString
	}

	if f.createErr != nil {
		return nil, f.createErr
	}

	return &awssm.CreateSecretOutput{}, nil
}

func (f *fakeAWSWriter) DeleteSecret(
	_ context.Context,
	params *awssm.DeleteSecretInput,
	_ ...func(*awssm.Options),
) (*awssm.DeleteSecretOutput, error) {
	if params != nil && params.SecretId != nil {
		f.deletedID = *params.SecretId
	}

	if params != nil && params.ForceDeleteWithoutRecovery != nil {
		f.forceDelete = *params.ForceDeleteWithoutRecovery
	}

	if f.deleteErr != nil {
		return nil, f.deleteErr
	}

	return &awssm.DeleteSecretOutput{}, nil
}

const testSecretID = "tenants/production/org_1/gw/external/dataprev-cert/credentials/versions/" +
	"6f1a2b3c-4d5e-4f60-8a9b-0c1d2e3f4a5b"

func TestAWSSecretWriter_CreatesSecretVerbatim(t *testing.T) {
	fake := &fakeAWSWriter{}
	writer := NewAWSSecretWriter(fake)

	require.NoError(t, writer.CreateSecretString(context.Background(), testSecretID, `{"certPem":"x","keyPem":"y"}`))
	require.Equal(t, testSecretID, fake.createdID)
	require.JSONEq(t, `{"certPem":"x","keyPem":"y"}`, fake.createdJSON)
}

// Rotation allocates a new versioned reference; it never overwrites. A lost
// create must say so with the shared sentinel, because the caller's next move
// (discard the staged material, keep the published reference) depends on
// telling "already there" apart from "infrastructure broke".
func TestAWSSecretWriter_CreateIsCreateOnly(t *testing.T) {
	fake := &fakeAWSWriter{createErr: &smtypes.ResourceExistsException{}}
	writer := NewAWSSecretWriter(fake)

	err := writer.CreateSecretString(context.Background(), testSecretID, `{"k":"v"}`)
	require.ErrorIs(t, err, ErrBackendSecretExists)
}

func TestAWSSecretWriter_DeleteForcesAndIsIdempotent(t *testing.T) {
	fake := &fakeAWSWriter{}
	writer := NewAWSSecretWriter(fake)

	require.NoError(t, writer.DeleteSecret(context.Background(), testSecretID))
	require.Equal(t, testSecretID, fake.deletedID)
	require.True(t, fake.forceDelete, "credential cleanup must leave no recovery window")

	absent := &fakeAWSWriter{deleteErr: &smtypes.ResourceNotFoundException{}}
	require.NoError(t, NewAWSSecretWriter(absent).DeleteSecret(context.Background(), testSecretID),
		"a retried cleanup must converge instead of wedging")
}

func TestAWSSecretWriter_ErrorsNeverCarryTheSecretPath(t *testing.T) {
	tenant := "org_supersecret_tenant"
	secretID := "tenants/production/" + tenant + "/gw/external/dataprev-cert/credentials"
	fake := &fakeAWSWriter{createErr: errors.New("boom at tenants/production/" + tenant + "/gw")}

	err := NewAWSSecretWriter(fake).CreateSecretString(context.Background(), secretID, `{"k":"v"}`)
	require.Error(t, err)
	require.NotContains(t, err.Error(), tenant)
}

// The two backends must accept the same payloads, so the narrower one sets the
// contract. Vault KV cannot hold a bare scalar or array; refusing those on BOTH
// backends means a payload that writes today still writes after a migration.
func TestSecretWriter_RefusesPayloadsVaultCannotHold(t *testing.T) {
	writers := map[string]SecretWriter{
		"aws":   NewAWSSecretWriter(&fakeAWSWriter{}),
		"vault": NewVaultSecretWriter(&VaultClient{}),
	}

	payloads := map[string]string{
		"bare string": `"just a string"`,
		"array":       `[1,2,3]`,
		"number":      `42`,
		"null":        `null`,
		"empty":       ``,
		"not json":    `certPem=x`,
	}

	for backend, writer := range writers {
		for name, payload := range payloads {
			t.Run(backend+"/"+name, func(t *testing.T) {
				require.Error(t, writer.CreateSecretString(context.Background(), testSecretID, payload))
			})
		}
	}
}

func TestValidateWritableSecret_ParsesOnceWithoutLosingNumberPrecision(t *testing.T) {
	data, err := validateWritableSecret(testSecretID, `{"serial":9007199254740993}`)
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), data["serial"])

	_, err = validateWritableSecret(testSecretID, `null`)
	require.ErrorIs(t, err, ErrBackendMisconfigured)

	_, err = validateWritableSecret(testSecretID, `{"first":1} {"second":2}`)
	require.ErrorIs(t, err, ErrBackendMisconfigured)
}

func TestSecretWriter_RefusesEmptySecretID(t *testing.T) {
	writers := map[string]SecretWriter{
		"aws":   NewAWSSecretWriter(&fakeAWSWriter{}),
		"vault": NewVaultSecretWriter(&VaultClient{}),
	}

	for backend, writer := range writers {
		t.Run(backend, func(t *testing.T) {
			require.ErrorIs(t, writer.CreateSecretString(context.Background(), "  ", `{"k":"v"}`), ErrBackendMisconfigured)
			require.ErrorIs(t, writer.DeleteSecret(context.Background(), "  "), ErrBackendMisconfigured)
		})
	}
}

func TestVaultSecretWriter_RequiresClient(t *testing.T) {
	for name, client := range map[string]*VaultClient{"nil": nil, "zero value": {}} {
		t.Run(name, func(t *testing.T) {
			writer := NewVaultSecretWriter(client)
			require.ErrorIs(t, writer.CreateSecretString(context.Background(), testSecretID, `{"k":"v"}`), ErrBackendMisconfigured)
			require.ErrorIs(t, writer.DeleteSecret(context.Background(), testSecretID), ErrBackendMisconfigured)
		})
	}
}

// Vault reports a lost check-and-set as a 400 whose body names the check; a 400
// that is NOT the CAS check must stay a failure rather than be reported as a
// benign "already exists", which would make a caller discard staged material it
// should have retried.
func TestClassifyVaultWriteError_OnlyCASConflictMeansExists(t *testing.T) {
	casConflict := &vaultapi.ResponseError{
		StatusCode: http.StatusBadRequest,
		Errors:     []string{"check-and-set parameter did not match the current version"},
	}
	require.ErrorIs(t, classifyVaultWriteError(casConflict, testSecretID), ErrBackendSecretExists)

	otherBadRequest := &vaultapi.ResponseError{
		StatusCode: http.StatusBadRequest,
		Errors:     []string{"missing data field"},
	}
	err := classifyVaultWriteError(otherBadRequest, testSecretID)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBackendSecretExists)

	denied := &vaultapi.ResponseError{StatusCode: http.StatusForbidden, Errors: []string{"permission denied"}}
	require.ErrorIs(t, classifyVaultWriteError(denied, testSecretID), ErrBackendAccessDenied)
}

func TestClassifyVaultWriteError_RedactsResponseDetails(t *testing.T) {
	tenant := "org_supersecret_tenant"
	err := &vaultapi.ResponseError{
		StatusCode: http.StatusInternalServerError,
		Errors:     []string{"failure at tenants/production/" + tenant},
	}

	classified := classifyVaultWriteError(err, "tenants/production/"+tenant+"/credentials")
	require.NotContains(t, classified.Error(), tenant)
}
