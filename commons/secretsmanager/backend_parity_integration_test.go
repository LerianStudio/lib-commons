//go:build integration

// Copyright Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package secretsmanager_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/secretsmanager"
	"github.com/LerianStudio/lib-commons/v6/commons/secretsmanager/secretsmanagertest"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcvault "github.com/testcontainers/testcontainers-go/modules/vault"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	vaultImage      = "hashicorp/vault:1.18"
	vaultRootToken  = "root-token-for-tests"
	localStackImage = "localstack/localstack:3.8.1"
	localStackPort  = "4566/tcp"
	awsRegion       = "us-east-1"
	// Generous because the first run on a clean host pays for the image pull;
	// later runs hit the local image cache and start in seconds.
	setupTimeout = 10 * time.Minute
)

// TestIntegration_CustodyBackendParity is the measurement behind the claim that
// the custody backend is an infrastructure choice and not a semantic one. The
// SAME contract suite runs against a real Vault and a real AWS Secrets Manager
// API; a behaviour that differs between them fails here rather than in a
// client's production cutover.
func TestIntegration_CustodyBackendParity(t *testing.T) {
	t.Run("vault", func(t *testing.T) {
		newBackend := vaultBackendFactory(t)
		secretsmanagertest.Run(t, newBackend)
	})

	t.Run("aws", func(t *testing.T) {
		newBackend := awsBackendFactory(t)
		secretsmanagertest.Run(t, newBackend)
	})
}

// vaultBackendFactory starts ONE Vault for the whole suite, on the KV v2 engine
// dev mode already mounts at secret/. Subtests share it safely because each one
// writes under its own target segment of the reference.
func vaultBackendFactory(t *testing.T) secretsmanagertest.Factory {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	t.Cleanup(cancel)

	container, err := tcvault.Run(ctx, vaultImage, tcvault.WithToken(vaultRootToken))
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	address, err := container.HttpHostAddress(ctx)
	require.NoError(t, err)

	client, err := secretsmanager.NewVaultClient(secretsmanager.VaultConfig{
		Address: address,
		Token:   vaultRootToken,
	})
	require.NoError(t, err)

	return func(t *testing.T) secretsmanagertest.Backend {
		t.Helper()

		return secretsmanagertest.Backend{
			Reader: client,
			Writer: secretsmanager.NewVaultSecretWriter(client),
		}
	}
}

// awsBackendFactory starts ONE LocalStack for the whole suite. AWS Secrets
// Manager has no mounts, so isolation comes from the tenant segment of the
// reference each subtest writes under.
func awsBackendFactory(t *testing.T) secretsmanagertest.Factory {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	t.Cleanup(cancel)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        localStackImage,
			ExposedPorts: []string{localStackPort},
			Env:          map[string]string{"SERVICES": "secretsmanager", "EAGER_SERVICE_LOADING": "1"},
			// The port opens well before Secrets Manager answers, so waiting
			// on the port alone hands back a container whose first API call
			// fails. The health endpoint reports per-service readiness.
			WaitingFor: wait.ForAll(
				wait.ForListeningPort(localStackPort),
				wait.ForHTTP("/_localstack/health").
					WithPort(localStackPort).
					WithResponseMatcher(localStackSecretsManagerReady),
			).WithStartupTimeout(setupTimeout),
		},
		Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	endpoint, err := container.PortEndpoint(ctx, localStackPort, "http")
	require.NoError(t, err)

	client := awssm.New(awssm.Options{
		Region:       awsRegion,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  awscreds.NewStaticCredentialsProvider("test", "test", ""),
	})

	return func(t *testing.T) secretsmanagertest.Backend {
		t.Helper()

		return secretsmanagertest.Backend{
			Reader: client,
			Writer: secretsmanager.NewAWSSecretWriter(client),
		}
	}
}

// localStackSecretsManagerReady reports whether LocalStack has finished loading
// the Secrets Manager service, as opposed to merely having opened its port.
func localStackSecretsManagerReady(body io.Reader) bool {
	var health struct {
		Services map[string]string `json:"services"`
	}

	if err := json.NewDecoder(body).Decode(&health); err != nil {
		return false
	}

	status := health.Services["secretsmanager"]

	return status == "available" || status == "running"
}
