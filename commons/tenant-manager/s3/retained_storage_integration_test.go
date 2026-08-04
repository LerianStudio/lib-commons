//go:build integration

package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/sync/errgroup"
)

const (
	localStackImage          = "localstack/localstack:3.8.1@sha256:b279c01f4cfb8f985a482e4014cabc1e2697b9d7a6c8c8db2e40f4d9f93687c7"
	localStackEdgePort       = "4566/tcp"
	localStackStartupTimeout = 2 * time.Minute
	localStackTestTimeout    = 5 * time.Minute
	localStackRegion         = "us-east-1"
	concurrentCreateCount    = 8
)

func TestIntegration_RetainedStorage_CreateRecoverConcurrentAndDenyDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), localStackTestTimeout)
	t.Cleanup(cancel)

	client := setupRetainedStorageLocalStack(ctx, t)
	bucket := "retained-storage-test"
	_, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket:                     aws.String(bucket),
		ObjectLockEnabledForBucket: aws.Bool(true),
	})
	require.NoError(t, err)

	fiveYears := int32(5)
	_, err = client.PutObjectLockConfiguration(ctx, &awss3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &s3types.ObjectLockConfiguration{
			ObjectLockEnabled: s3types.ObjectLockEnabledEnabled,
			Rule: &s3types.ObjectLockRule{DefaultRetention: &s3types.DefaultRetention{
				Mode:  s3types.ObjectLockRetentionModeCompliance,
				Years: &fiveYears,
			}},
		},
	})
	require.NoError(t, err)

	store, err := NewRetainedStorage(client, bucket)
	require.NoError(t, err)
	require.NoError(t, store.ValidateDefaultRetention(ctx))

	retainedUntil := time.Now().UTC().AddDate(5, 0, 1).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	require.NotZero(t, retainedUntil.Nanosecond())
	canonicalRetainedUntil := retainedUntil.Truncate(time.Second)
	payload := []byte("signed-contract")
	metadata, err := store.CreateRetained(
		ctx,
		"contracts/123/signed-ccb.pdf",
		bytes.NewReader(payload),
		"application/pdf",
		Retention{Mode: RetentionModeCompliance, RetainUntil: retainedUntil},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, metadata.VersionID)
	assert.Equal(t, RetentionModeCompliance, metadata.Retention.Mode)
	assert.Equal(t, canonicalRetainedUntil, metadata.Retention.RetainUntil)

	_, err = store.CreateRetained(
		ctx,
		"contracts/123/signed-ccb.pdf",
		bytes.NewReader(payload),
		"application/pdf",
		Retention{Mode: RetentionModeCompliance, RetainUntil: retainedUntil},
	)
	require.ErrorIs(t, err, ErrObjectAlreadyExists)

	_, err = store.CreateRetained(
		ctx,
		"contracts/123/signed-ccb.pdf",
		bytes.NewReader(payload),
		"application/pdf",
		Retention{Mode: RetentionModeCompliance, RetainUntil: retainedUntil},
	)
	require.ErrorIs(t, err, ErrObjectAlreadyExists)

	statMetadata, err := store.StatVersion(ctx, "contracts/123/signed-ccb.pdf", metadata.VersionID)
	require.NoError(t, err)
	assert.Equal(t, metadata, statMetadata)
	assert.Equal(t, RetentionModeCompliance, statMetadata.Retention.Mode)
	assert.Equal(t, canonicalRetainedUntil, statMetadata.Retention.RetainUntil)

	body, err := store.DownloadVersion(ctx, "contracts/123/signed-ccb.pdf", metadata.VersionID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, body.Close()) })

	downloaded, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, payload, downloaded)

	recoverableStore, err := NewRecoverableRetainedStorage(client, bucket)
	require.NoError(t, err)
	expected := ExpectedRetainedObject{
		ContentType:   "application/pdf",
		ContentLength: int64(len(payload)),
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: retainedUntil,
		},
	}

	firstWrite, err := recoverableStore.CreateOrRecoverRetained(
		ctx,
		"contracts/recoverable.pdf",
		bytes.NewReader(payload),
		expected,
	)
	require.NoError(t, err)

	recoveredRetry, err := recoverableStore.CreateOrRecoverRetained(
		ctx,
		"contracts/recoverable.pdf",
		bytes.NewReader(payload),
		expected,
	)
	require.NoError(t, err)
	assert.Equal(t, firstWrite, recoveredRetry)
	assert.Equal(t, firstWrite.VersionID, recoveredRetry.VersionID)

	versionIDs := make(chan string, concurrentCreateCount)
	group, concurrentCtx := errgroup.WithContext(ctx)
	for range concurrentCreateCount {
		group.Go(func() error {
			created, createErr := recoverableStore.CreateOrRecoverRetained(
				concurrentCtx,
				"contracts/concurrent.pdf",
				bytes.NewReader(payload),
				expected,
			)
			if createErr != nil {
				return createErr
			}

			versionIDs <- created.VersionID

			return nil
		})
	}
	require.NoError(t, group.Wait())
	close(versionIDs)

	var concurrentVersionID string
	for versionID := range versionIDs {
		assert.NotEmpty(t, versionID)
		if concurrentVersionID == "" {
			concurrentVersionID = versionID
		}
		assert.Equal(t, concurrentVersionID, versionID)
	}

	_, err = client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket:    aws.String(bucket),
		Key:       aws.String("contracts/123/signed-ccb.pdf"),
		VersionId: aws.String(metadata.VersionID),
	})
	require.Error(t, err)

	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, []string{"AccessDenied", "InvalidRequest"}, apiErr.ErrorCode())
}

func setupRetainedStorageLocalStack(ctx context.Context, t *testing.T) *awss3.Client {
	t.Helper()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        localStackImage,
			ExposedPorts: []string{localStackEdgePort},
			Env: map[string]string{
				"SERVICES":                     "s3",
				"AWS_DEFAULT_REGION":           localStackRegion,
				"LOCALSTACK_HOST":              "localhost",
				"S3_SKIP_SIGNATURE_VALIDATION": "1",
			},
			WaitingFor: wait.ForLog("Ready.").WithStartupTimeout(localStackStartupTimeout),
		},
		Started: true,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		require.NoError(t, container.Terminate(cleanupCtx))
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	mappedPort, err := container.MappedPort(ctx, localStackEdgePort)
	require.NoError(t, err)
	endpoint := fmt.Sprintf("http://%s:%s", host, mappedPort.Port())

	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test", Source: "integration-test"}, nil
	})

	return awss3.NewFromConfig(aws.Config{
		Region:       localStackRegion,
		Credentials:  credentials,
		BaseEndpoint: aws.String(endpoint),
	}, func(options *awss3.Options) {
		options.UsePathStyle = true
	})
}
