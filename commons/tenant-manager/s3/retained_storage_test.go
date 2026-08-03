//go:build unit

package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRetainedObjectAPI struct {
	putInput       *awss3.PutObjectInput
	getInput       *awss3.GetObjectInput
	headInput      *awss3.HeadObjectInput
	lockInput      *awss3.GetObjectLockConfigurationInput
	listInput      *awss3.ListObjectVersionsInput
	putOutput      *awss3.PutObjectOutput
	getOutput      *awss3.GetObjectOutput
	headOutput     *awss3.HeadObjectOutput
	lockOutput     *awss3.GetObjectLockConfigurationOutput
	listOutput     *awss3.ListObjectVersionsOutput
	listOutputs    []*awss3.ListObjectVersionsOutput
	putErr         error
	getErr         error
	headErr        error
	lockErr        error
	listErr        error
	listContextErr error
	putCallCount   int
	headCallCount  int
	lockCallCount  int
	listCallCount  int
}

type nilMapRetainedObjectAPI map[string]string

func (nilMapRetainedObjectAPI) PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	return nil, nil
}

func (nilMapRetainedObjectAPI) GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	return nil, nil
}

func (nilMapRetainedObjectAPI) HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	return nil, nil
}

func (nilMapRetainedObjectAPI) GetObjectLockConfiguration(context.Context, *awss3.GetObjectLockConfigurationInput, ...func(*awss3.Options)) (*awss3.GetObjectLockConfigurationOutput, error) {
	return nil, nil
}

func (f *fakeRetainedObjectAPI) PutObject(_ context.Context, input *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	f.putInput = input
	f.putCallCount++

	return f.putOutput, f.putErr
}

func (f *fakeRetainedObjectAPI) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	f.getInput = input

	return f.getOutput, f.getErr
}

func (f *fakeRetainedObjectAPI) HeadObject(_ context.Context, input *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	f.headInput = input
	f.headCallCount++

	return f.headOutput, f.headErr
}

func (f *fakeRetainedObjectAPI) GetObjectLockConfiguration(_ context.Context, input *awss3.GetObjectLockConfigurationInput, _ ...func(*awss3.Options)) (*awss3.GetObjectLockConfigurationOutput, error) {
	f.lockInput = input
	f.lockCallCount++

	return f.lockOutput, f.lockErr
}

func (f *fakeRetainedObjectAPI) ListObjectVersions(ctx context.Context, input *awss3.ListObjectVersionsInput, _ ...func(*awss3.Options)) (*awss3.ListObjectVersionsOutput, error) {
	f.listInput = input
	f.listCallCount++
	f.listContextErr = ctx.Err()

	if len(f.listOutputs) > 0 {
		output := f.listOutputs[0]
		f.listOutputs = f.listOutputs[1:]

		return output, f.listErr
	}

	return f.listOutput, f.listErr
}

func TestRetainedStorage_CreateRetained_UsesConditionalComplianceWriteAndReturnsVersionMetadata(t *testing.T) {
	t.Parallel()

	retainUntil := time.Date(2031, time.August, 3, 12, 30, 0, 123456789, time.FixedZone("BRT", -3*60*60))
	canonicalRetainUntil := retainUntil.UTC().Truncate(time.Second)
	lastModified := time.Date(2026, time.August, 3, 15, 31, 0, 0, time.UTC)
	fake := &fakeRetainedObjectAPI{
		putOutput: &awss3.PutObjectOutput{VersionId: aws.String("version-1")},
		headOutput: &awss3.HeadObjectOutput{
			VersionId:                 aws.String("version-1"),
			ETag:                      aws.String("etag-1"),
			ContentType:               aws.String("application/pdf"),
			ContentLength:             aws.Int64(7),
			LastModified:              &lastModified,
			ObjectLockMode:            s3types.ObjectLockModeCompliance,
			ObjectLockRetainUntilDate: aws.Time(canonicalRetainUntil),
		},
	}
	store, err := NewRetainedStorage(fake, "  retained-bucket  ")
	require.NoError(t, err)

	metadata, err := store.CreateRetained(
		multiTenantCtx("org_01ABC"),
		"contracts/123/signed-ccb.pdf",
		bytes.NewReader([]byte("payload")),
		"application/pdf",
		Retention{Mode: RetentionModeCompliance, RetainUntil: retainUntil},
	)
	require.NoError(t, err)

	require.NotNil(t, fake.putInput)
	assert.Equal(t, "retained-bucket", aws.ToString(fake.putInput.Bucket))
	assert.Equal(t, "org_01ABC/contracts/123/signed-ccb.pdf", aws.ToString(fake.putInput.Key))
	assert.Equal(t, "*", aws.ToString(fake.putInput.IfNoneMatch))
	assert.Equal(t, "application/pdf", aws.ToString(fake.putInput.ContentType))
	assert.Equal(t, s3types.ObjectLockModeCompliance, fake.putInput.ObjectLockMode)
	require.NotNil(t, fake.putInput.ObjectLockRetainUntilDate)
	assert.Equal(t, time.UTC, fake.putInput.ObjectLockRetainUntilDate.Location())
	assert.Equal(t, canonicalRetainUntil, *fake.putInput.ObjectLockRetainUntilDate)
	require.NotNil(t, fake.headInput)
	assert.Equal(t, "version-1", aws.ToString(fake.headInput.VersionId))
	assert.Equal(t, ObjectMetadata{
		VersionID:     "version-1",
		ETag:          "etag-1",
		ContentType:   "application/pdf",
		ContentLength: 7,
		LastModified:  lastModified,
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: canonicalRetainUntil,
		},
	}, metadata)
}

func TestNewRetainedStorage_TypedNilMap_ReturnsError(t *testing.T) {
	t.Parallel()

	var client nilMapRetainedObjectAPI

	store, err := NewRetainedStorage(client, testBucket)

	require.Error(t, err)
	assert.Nil(t, store)
}

func TestRetainedStorage_CreateRetained_FailsClosedOnInvalidOrIncompleteWrite(t *testing.T) {
	t.Parallel()

	retainUntil := time.Date(2031, time.August, 3, 12, 30, 0, 0, time.UTC)
	preconditionFailed := &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusPreconditionFailed}},
		Err:      &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object exists"},
	}
	tests := []struct {
		name          string
		fake          *fakeRetainedObjectAPI
		body          io.Reader
		retention     Retention
		wantErr       error
		wantHeadCalls int
	}{
		{
			name:      "nil body",
			fake:      &fakeRetainedObjectAPI{},
			retention: Retention{Mode: RetentionModeCompliance, RetainUntil: retainUntil},
			wantErr:   ErrRetainedBodyRequired,
		},
		{
			name:      "wrong retention mode",
			fake:      &fakeRetainedObjectAPI{},
			body:      bytes.NewReader([]byte("payload")),
			retention: Retention{Mode: "GOVERNANCE", RetainUntil: retainUntil},
			wantErr:   ErrInvalidRetention,
		},
		{
			name:      "missing retain until",
			fake:      &fakeRetainedObjectAPI{},
			body:      bytes.NewReader([]byte("payload")),
			retention: Retention{Mode: RetentionModeCompliance},
			wantErr:   ErrInvalidRetention,
		},
		{
			name:      "precondition failed",
			fake:      &fakeRetainedObjectAPI{putErr: preconditionFailed},
			body:      bytes.NewReader([]byte("payload")),
			retention: Retention{Mode: RetentionModeCompliance, RetainUntil: retainUntil},
			wantErr:   ErrObjectAlreadyExists,
		},
		{
			name:      "missing put version ID",
			fake:      &fakeRetainedObjectAPI{putOutput: &awss3.PutObjectOutput{}},
			body:      bytes.NewReader([]byte("payload")),
			retention: Retention{Mode: RetentionModeCompliance, RetainUntil: retainUntil},
			wantErr:   ErrVersionIDRequired,
		},
		{
			name:          "head missing lock metadata",
			fake:          &fakeRetainedObjectAPI{putOutput: &awss3.PutObjectOutput{VersionId: aws.String("version-1")}, headOutput: &awss3.HeadObjectOutput{VersionId: aws.String("version-1")}},
			body:          bytes.NewReader([]byte("payload")),
			retention:     Retention{Mode: RetentionModeCompliance, RetainUntil: retainUntil},
			wantErr:       ErrRetentionMetadataRequired,
			wantHeadCalls: 1,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewRetainedStorage(test.fake, testBucket)
			require.NoError(t, err)

			_, err = store.CreateRetained(context.Background(), "artifact", test.body, "application/octet-stream", test.retention)

			require.ErrorIs(t, err, test.wantErr)
			assert.Equal(t, test.wantHeadCalls, test.fake.headCallCount)
		})
	}
}

func TestRecoverableRetainedStorage_CreateOrRecoverRetained_CreatesOrReturnsExactExistingVersion(t *testing.T) {
	t.Parallel()

	retainUntil := time.Date(2031, time.August, 3, 15, 30, 0, 123456789, time.UTC)
	canonicalRetainUntil := retainUntil.Truncate(time.Second)
	expected := ExpectedRetainedObject{
		ContentType:   "application/pdf",
		ContentLength: 7,
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: retainUntil,
		},
	}
	preconditionFailed := &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusPreconditionFailed}},
		Err:      &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object exists"},
	}
	canceledCtx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()
	tests := []struct {
		name                      string
		ctx                       context.Context
		fake                      *fakeRetainedObjectAPI
		wantListCallCount         int
		wantKeyMarker             string
		wantVersionIDMarker       string
		wantRecoveryContextActive bool
	}{
		{
			name: "first write",
			fake: &fakeRetainedObjectAPI{
				putOutput:  &awss3.PutObjectOutput{VersionId: aws.String("version-created")},
				headOutput: retainedHeadOutput("version-created", expected.ContentType, expected.ContentLength, canonicalRetainUntil),
			},
		},
		{
			name: "retry recovers existing write",
			fake: &fakeRetainedObjectAPI{
				putErr:     preconditionFailed,
				listOutput: retainedVersionListOutput("artifact", "version-existing"),
				headOutput: retainedHeadOutput("version-existing", expected.ContentType, expected.ContentLength, canonicalRetainUntil),
			},
			wantListCallCount: 1,
		},
		{
			name: "ambiguous timeout recovers completed write",
			ctx:  canceledCtx,
			fake: &fakeRetainedObjectAPI{
				putErr:     context.DeadlineExceeded,
				listOutput: retainedVersionListOutput("artifact", "version-after-timeout"),
				headOutput: retainedHeadOutput("version-after-timeout", expected.ContentType, expected.ContentLength, canonicalRetainUntil),
			},
			wantListCallCount:         1,
			wantRecoveryContextActive: true,
		},
		{
			name: "canceled request recovers completed write",
			ctx:  canceledCtx,
			fake: &fakeRetainedObjectAPI{
				putErr:     context.Canceled,
				listOutput: retainedVersionListOutput("artifact", "version-after-cancel"),
				headOutput: retainedHeadOutput("version-after-cancel", expected.ContentType, expected.ContentLength, canonicalRetainUntil),
			},
			wantListCallCount:         1,
			wantRecoveryContextActive: true,
		},
		{
			name: "recovery skips sibling prefix keys on truncated page",
			fake: &fakeRetainedObjectAPI{
				putErr: preconditionFailed,
				listOutput: &awss3.ListObjectVersionsOutput{
					Versions: []s3types.ObjectVersion{
						{Key: aws.String("artifact"), VersionId: aws.String("version-existing"), IsLatest: aws.Bool(true)},
						{Key: aws.String("artifact.bak"), VersionId: aws.String("version-sibling"), IsLatest: aws.Bool(true)},
					},
					IsTruncated:         aws.Bool(true),
					NextKeyMarker:       aws.String("artifact.bak"),
					NextVersionIdMarker: aws.String("version-sibling"),
				},
				headOutput: retainedHeadOutput("version-existing", expected.ContentType, expected.ContentLength, canonicalRetainUntil),
			},
			wantListCallCount: 1,
		},
		{
			name: "recovery paginates truncated exact-key page",
			fake: &fakeRetainedObjectAPI{
				putErr: preconditionFailed,
				listOutputs: []*awss3.ListObjectVersionsOutput{
					{
						Versions: []s3types.ObjectVersion{
							{Key: aws.String("artifact"), VersionId: aws.String("version-existing"), IsLatest: aws.Bool(true)},
						},
						IsTruncated:         aws.Bool(true),
						NextKeyMarker:       aws.String("artifact"),
						NextVersionIdMarker: aws.String("version-existing"),
					},
					{
						Versions: []s3types.ObjectVersion{
							{Key: aws.String("artifact.bak"), VersionId: aws.String("version-sibling"), IsLatest: aws.Bool(true)},
						},
					},
				},
				headOutput: retainedHeadOutput("version-existing", expected.ContentType, expected.ContentLength, canonicalRetainUntil),
			},
			wantListCallCount:   2,
			wantKeyMarker:       "artifact",
			wantVersionIDMarker: "version-existing",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewRecoverableRetainedStorage(test.fake, testBucket)
			require.NoError(t, err)

			requestCtx := test.ctx
			if requestCtx == nil {
				requestCtx = context.Background()
			}

			metadata, err := store.CreateOrRecoverRetained(
				requestCtx,
				"artifact",
				bytes.NewReader([]byte("payload")),
				expected,
			)
			require.NoError(t, err)

			assert.NotEmpty(t, metadata.VersionID)
			assert.Equal(t, expected.ContentType, metadata.ContentType)
			assert.Equal(t, expected.ContentLength, metadata.ContentLength)
			assert.Equal(t, canonicalRetainUntil, metadata.Retention.RetainUntil)
			assert.Equal(t, test.wantListCallCount, test.fake.listCallCount)
			assert.Equal(t, metadata.VersionID, aws.ToString(test.fake.headInput.VersionId))
			if test.wantListCallCount > 0 {
				require.NotNil(t, test.fake.listInput)
				assert.Equal(t, "artifact", aws.ToString(test.fake.listInput.Prefix))
				assert.Equal(t, test.wantKeyMarker, aws.ToString(test.fake.listInput.KeyMarker))
				assert.Equal(t, test.wantVersionIDMarker, aws.ToString(test.fake.listInput.VersionIdMarker))
			}
			if test.wantRecoveryContextActive {
				assert.NoError(t, test.fake.listContextErr)
			}
		})
	}
}

func TestRecoverableRetainedStorage_CreateOrRecoverRetained_RejectsAmbiguousOrMismatchedExistingVersion(t *testing.T) {
	t.Parallel()

	retainUntil := time.Date(2031, time.August, 3, 15, 30, 0, 123000000, time.UTC)
	expected := ExpectedRetainedObject{
		ContentType:   "application/pdf",
		ContentLength: 7,
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: retainUntil,
		},
	}
	preconditionFailed := &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object exists"}
	tests := []struct {
		name    string
		fake    *fakeRetainedObjectAPI
		wantErr error
	}{
		{
			name: "first write wrong content type",
			fake: &fakeRetainedObjectAPI{
				putOutput:  &awss3.PutObjectOutput{VersionId: aws.String("version-created")},
				headOutput: retainedHeadOutput("version-created", "text/plain", expected.ContentLength, retainUntil),
			},
			wantErr: ErrRetainedMetadataMismatch,
		},
		{
			name: "multiple exact versions",
			fake: &fakeRetainedObjectAPI{
				putErr: preconditionFailed,
				listOutput: &awss3.ListObjectVersionsOutput{Versions: []s3types.ObjectVersion{
					{Key: aws.String("artifact"), VersionId: aws.String("version-latest"), IsLatest: aws.Bool(true)},
					{Key: aws.String("artifact"), VersionId: aws.String("version-old"), IsLatest: aws.Bool(false)},
				}},
			},
			wantErr: ErrRetainedVersionAmbiguous,
		},
		{
			name: "truncated page without continuation markers",
			fake: &fakeRetainedObjectAPI{
				putErr: preconditionFailed,
				listOutput: &awss3.ListObjectVersionsOutput{
					Versions: []s3types.ObjectVersion{
						{Key: aws.String("artifact"), VersionId: aws.String("version-existing"), IsLatest: aws.Bool(true)},
					},
					IsTruncated: aws.Bool(true),
				},
			},
			wantErr: ErrRetainedVersionAmbiguous,
		},
		{
			name: "wrong content type",
			fake: &fakeRetainedObjectAPI{
				putErr:     preconditionFailed,
				listOutput: retainedVersionListOutput("artifact", "version-existing"),
				headOutput: retainedHeadOutput("version-existing", "text/plain", expected.ContentLength, retainUntil),
			},
			wantErr: ErrRetainedMetadataMismatch,
		},
		{
			name: "wrong content length",
			fake: &fakeRetainedObjectAPI{
				putErr:     preconditionFailed,
				listOutput: retainedVersionListOutput("artifact", "version-existing"),
				headOutput: retainedHeadOutput("version-existing", expected.ContentType, expected.ContentLength+1, retainUntil),
			},
			wantErr: ErrRetainedMetadataMismatch,
		},
		{
			name: "wrong retain until",
			fake: &fakeRetainedObjectAPI{
				putErr:     preconditionFailed,
				listOutput: retainedVersionListOutput("artifact", "version-existing"),
				headOutput: retainedHeadOutput("version-existing", expected.ContentType, expected.ContentLength, retainUntil.Add(time.Second)),
			},
			wantErr: ErrRetainedMetadataMismatch,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewRecoverableRetainedStorage(test.fake, testBucket)
			require.NoError(t, err)

			_, err = store.CreateOrRecoverRetained(
				context.Background(),
				"artifact",
				bytes.NewReader([]byte("payload")),
				expected,
			)

			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestRecoverableRetainedStorage_CreateOrRecoverRetained_RejectsBodyLengthMismatchBeforeWrite(t *testing.T) {
	t.Parallel()

	expected := ExpectedRetainedObject{
		ContentType:   "application/pdf",
		ContentLength: 7,
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: time.Date(2031, time.August, 3, 15, 30, 0, 0, time.UTC),
		},
	}
	tests := []struct {
		name string
		body io.Reader
	}{
		{name: "body shorter than expected", body: bytes.NewReader([]byte("short"))},
		{name: "body longer than expected", body: bytes.NewReader([]byte("payload-too-long"))},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRetainedObjectAPI{}
			store, err := NewRecoverableRetainedStorage(fake, testBucket)
			require.NoError(t, err)

			_, err = store.CreateOrRecoverRetained(context.Background(), "artifact", test.body, expected)

			require.ErrorIs(t, err, ErrRetainedMetadataMismatch)
			assert.Zero(t, fake.putCallCount)
		})
	}
}

func TestRetainedStorage_CreateRetained_PostWriteStatFailure_ReportsCreateOperation(t *testing.T) {
	t.Parallel()

	fake := &fakeRetainedObjectAPI{
		putOutput: &awss3.PutObjectOutput{VersionId: aws.String("version-1")},
		headErr:   errors.New("head failed"),
	}
	store, err := NewRetainedStorage(fake, testBucket)
	require.NoError(t, err)

	_, err = store.CreateRetained(
		context.Background(),
		"artifact",
		bytes.NewReader([]byte("payload")),
		"application/pdf",
		Retention{Mode: RetentionModeCompliance, RetainUntil: time.Now().UTC().AddDate(5, 0, 1)},
	)

	require.Error(t, err)
	var retainedErr *RetainedStorageError
	require.ErrorAs(t, err, &retainedErr)
	assert.Equal(t, "create", retainedErr.Operation())
}

func TestRecoverableRetainedStorage_CreateOrRecoverRetained_RequiresExpectedMetadata(t *testing.T) {
	t.Parallel()

	validRetention := Retention{
		Mode:        RetentionModeCompliance,
		RetainUntil: time.Now().UTC().AddDate(5, 0, 1),
	}
	tests := []struct {
		name     string
		expected ExpectedRetainedObject
	}{
		{
			name: "missing content type",
			expected: ExpectedRetainedObject{
				ContentLength: 7,
				Retention:     validRetention,
			},
		},
		{
			name: "negative content length",
			expected: ExpectedRetainedObject{
				ContentType:   "application/pdf",
				ContentLength: -1,
				Retention:     validRetention,
			},
		},
		{
			name: "wrong retention mode",
			expected: ExpectedRetainedObject{
				ContentType:   "application/pdf",
				ContentLength: 7,
				Retention: Retention{
					Mode:        "GOVERNANCE",
					RetainUntil: validRetention.RetainUntil,
				},
			},
		},
		{
			name: "missing retain until",
			expected: ExpectedRetainedObject{
				ContentType:   "application/pdf",
				ContentLength: 7,
				Retention: Retention{
					Mode: RetentionModeCompliance,
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRetainedObjectAPI{}
			store, err := NewRecoverableRetainedStorage(fake, testBucket)
			require.NoError(t, err)

			_, err = store.CreateOrRecoverRetained(
				context.Background(),
				"artifact",
				bytes.NewReader([]byte("payload")),
				test.expected,
			)

			require.ErrorIs(t, err, ErrExpectedRetainedObjectRequired)
			assert.Zero(t, fake.putCallCount)
		})
	}
}

func TestRetainedStorage_Operations_ReturnTypedTelemetrySafeErrorsWithClassifications(t *testing.T) {
	t.Parallel()

	const (
		sensitiveBucket  = "customer-secret-bucket"
		sensitiveKey     = "customer-secret-key"
		sensitiveVersion = "customer-secret-version"
	)
	upstreamFailure := errors.New("upstream failure for " + sensitiveBucket + "/" + sensitiveKey + "/" + sensitiveVersion)
	preconditionFailed := &smithy.GenericAPIError{Code: "PreconditionFailed", Message: sensitiveKey}
	tests := []struct {
		name    string
		fake    *fakeRetainedObjectAPI
		invoke  func(RetainedStorage) error
		wantErr error
	}{
		{
			name: "create conflict",
			fake: &fakeRetainedObjectAPI{putErr: preconditionFailed},
			invoke: func(store RetainedStorage) error {
				_, err := store.CreateRetained(
					context.Background(),
					sensitiveKey,
					bytes.NewReader([]byte("payload")),
					"application/pdf",
					Retention{Mode: RetentionModeCompliance, RetainUntil: time.Now().UTC().AddDate(5, 0, 1)},
				)

				return err
			},
			wantErr: ErrObjectAlreadyExists,
		},
		{
			name: "download not found",
			fake: &fakeRetainedObjectAPI{getErr: &s3types.NoSuchKey{Message: aws.String(sensitiveKey)}},
			invoke: func(store RetainedStorage) error {
				_, err := store.DownloadVersion(context.Background(), sensitiveKey, sensitiveVersion)

				return err
			},
			wantErr: ErrObjectNotFound,
		},
		{
			name: "stat upstream failure",
			fake: &fakeRetainedObjectAPI{headErr: upstreamFailure},
			invoke: func(store RetainedStorage) error {
				_, err := store.StatVersion(context.Background(), sensitiveKey, sensitiveVersion)

				return err
			},
			wantErr: upstreamFailure,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewRetainedStorage(test.fake, sensitiveBucket)
			require.NoError(t, err)

			err = test.invoke(store)
			require.ErrorIs(t, err, test.wantErr)

			var retainedErr *RetainedStorageError
			require.ErrorAs(t, err, &retainedErr)
			assert.NotEmpty(t, retainedErr.Operation())
			assert.NotContains(t, err.Error(), sensitiveBucket)
			assert.NotContains(t, err.Error(), sensitiveKey)
			assert.NotContains(t, err.Error(), sensitiveVersion)
			assert.False(t, strings.Contains(err.Error(), "customer-secret"))
		})
	}
}

func TestRecoverableRetainedStorage_RecoveryFailure_ReturnsTelemetrySafeErrorWithBothClassifications(t *testing.T) {
	t.Parallel()

	const (
		sensitiveBucket  = "customer-secret-bucket"
		sensitiveKey     = "customer-secret-key"
		sensitiveVersion = "customer-secret-version"
	)
	listFailure := errors.New("list failed for " + sensitiveBucket + "/" + sensitiveKey + "/" + sensitiveVersion)
	fake := &fakeRetainedObjectAPI{
		putErr:  &smithy.GenericAPIError{Code: "PreconditionFailed", Message: sensitiveKey},
		listErr: listFailure,
	}
	store, err := NewRecoverableRetainedStorage(fake, sensitiveBucket)
	require.NoError(t, err)

	_, err = store.CreateOrRecoverRetained(
		context.Background(),
		sensitiveKey,
		bytes.NewReader([]byte("payload")),
		ExpectedRetainedObject{
			ContentType:   "application/pdf",
			ContentLength: 7,
			Retention: Retention{
				Mode:        RetentionModeCompliance,
				RetainUntil: time.Now().UTC().AddDate(5, 0, 1),
			},
		},
	)

	require.ErrorIs(t, err, ErrObjectAlreadyExists)
	require.ErrorIs(t, err, listFailure)
	var retainedErr *RetainedStorageError
	require.ErrorAs(t, err, &retainedErr)
	assert.Equal(t, "create-or-recover", retainedErr.Operation())
	assert.NotContains(t, err.Error(), sensitiveBucket)
	assert.NotContains(t, err.Error(), sensitiveKey)
	assert.NotContains(t, err.Error(), sensitiveVersion)
}

func retainedVersionListOutput(key, versionID string) *awss3.ListObjectVersionsOutput {
	return &awss3.ListObjectVersionsOutput{Versions: []s3types.ObjectVersion{{
		Key:       aws.String(key),
		VersionId: aws.String(versionID),
		IsLatest:  aws.Bool(true),
	}}}
}

func retainedHeadOutput(versionID, contentType string, contentLength int64, retainUntil time.Time) *awss3.HeadObjectOutput {
	return &awss3.HeadObjectOutput{
		VersionId:                 aws.String(versionID),
		ContentType:               aws.String(contentType),
		ContentLength:             aws.Int64(contentLength),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: aws.Time(retainUntil),
	}
}

func TestRetainedStorage_DownloadVersion_RequestsExactVersion(t *testing.T) {
	t.Parallel()

	fake := &fakeRetainedObjectAPI{getOutput: &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte("versioned")))}}
	store, err := NewRetainedStorage(fake, testBucket)
	require.NoError(t, err)

	body, err := store.DownloadVersion(multiTenantCtx("org_01ABC"), "artifact", "version-7")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, body.Close()) })

	content, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, []byte("versioned"), content)
	require.NotNil(t, fake.getInput)
	assert.Equal(t, "org_01ABC/artifact", aws.ToString(fake.getInput.Key))
	assert.Equal(t, "version-7", aws.ToString(fake.getInput.VersionId))
}

func TestRetainedStorage_DownloadVersion_MapsErrorsAndRejectsMissingVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fake      *fakeRetainedObjectAPI
		versionID string
		wantErr   error
	}{
		{name: "missing version", fake: &fakeRetainedObjectAPI{}, wantErr: ErrVersionIDRequired},
		{name: "not found", fake: &fakeRetainedObjectAPI{getErr: &s3types.NoSuchKey{}}, versionID: "missing-version", wantErr: ErrObjectNotFound},
		{name: "no such version", fake: &fakeRetainedObjectAPI{getErr: &smithy.GenericAPIError{Code: "NoSuchVersion", Message: "version does not exist"}}, versionID: "missing-version", wantErr: ErrObjectNotFound},
		{name: "nil body", fake: &fakeRetainedObjectAPI{getOutput: &awss3.GetObjectOutput{}}, versionID: "version-1", wantErr: ErrVersionMetadataRequired},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewRetainedStorage(test.fake, testBucket)
			require.NoError(t, err)

			_, err = store.DownloadVersion(context.Background(), "artifact", test.versionID)

			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestRetainedStorage_StatVersion_RequestsExactVersionAndReturnsImmutableMetadata(t *testing.T) {
	t.Parallel()

	lastModified := time.Date(2026, time.August, 3, 15, 31, 0, 0, time.UTC)
	retainUntil := time.Date(2031, time.August, 3, 15, 31, 0, 0, time.UTC)
	fake := &fakeRetainedObjectAPI{headOutput: &awss3.HeadObjectOutput{
		VersionId:                 aws.String("version-3"),
		ETag:                      aws.String("etag-3"),
		ContentType:               aws.String("application/pdf"),
		ContentLength:             aws.Int64(42),
		LastModified:              &lastModified,
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: &retainUntil,
	}}
	store, err := NewRetainedStorage(fake, testBucket)
	require.NoError(t, err)

	metadata, err := store.StatVersion(multiTenantCtx("org_01ABC"), "artifact", "version-3")
	require.NoError(t, err)

	require.NotNil(t, fake.headInput)
	assert.Equal(t, "org_01ABC/artifact", aws.ToString(fake.headInput.Key))
	assert.Equal(t, "version-3", aws.ToString(fake.headInput.VersionId))
	assert.Equal(t, "version-3", metadata.VersionID)
	assert.Equal(t, "etag-3", metadata.ETag)
	assert.Equal(t, "application/pdf", metadata.ContentType)
	assert.EqualValues(t, 42, metadata.ContentLength)
	assert.Equal(t, lastModified, metadata.LastModified)
	assert.Equal(t, Retention{Mode: RetentionModeCompliance, RetainUntil: retainUntil}, metadata.Retention)
}

func TestRetainedStorage_StatVersion_FailsClosedOnMissingOrWrongVersionMetadata(t *testing.T) {
	t.Parallel()

	retainUntil := time.Date(2031, time.August, 3, 15, 31, 0, 0, time.UTC)
	tests := []struct {
		name      string
		fake      *fakeRetainedObjectAPI
		versionID string
		wantErr   error
	}{
		{name: "missing requested version", fake: &fakeRetainedObjectAPI{}, wantErr: ErrVersionIDRequired},
		{name: "head not found", fake: &fakeRetainedObjectAPI{headErr: &s3types.NotFound{}}, versionID: "version-1", wantErr: ErrObjectNotFound},
		{name: "missing response", fake: &fakeRetainedObjectAPI{}, versionID: "version-1", wantErr: ErrVersionMetadataRequired},
		{name: "missing response version", fake: &fakeRetainedObjectAPI{headOutput: &awss3.HeadObjectOutput{ObjectLockMode: s3types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: &retainUntil}}, versionID: "version-1", wantErr: ErrVersionIDRequired},
		{name: "different response version", fake: &fakeRetainedObjectAPI{headOutput: &awss3.HeadObjectOutput{VersionId: aws.String("version-2"), ObjectLockMode: s3types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: &retainUntil}}, versionID: "version-1", wantErr: ErrVersionIDMismatch},
		{name: "missing lock mode", fake: &fakeRetainedObjectAPI{headOutput: &awss3.HeadObjectOutput{VersionId: aws.String("version-1"), ObjectLockRetainUntilDate: &retainUntil}}, versionID: "version-1", wantErr: ErrRetentionMetadataRequired},
		{name: "governance lock mode", fake: &fakeRetainedObjectAPI{headOutput: &awss3.HeadObjectOutput{VersionId: aws.String("version-1"), ObjectLockMode: s3types.ObjectLockModeGovernance, ObjectLockRetainUntilDate: &retainUntil}}, versionID: "version-1", wantErr: ErrRetentionNotCompliance},
		{name: "missing retain until", fake: &fakeRetainedObjectAPI{headOutput: &awss3.HeadObjectOutput{VersionId: aws.String("version-1"), ObjectLockMode: s3types.ObjectLockModeCompliance}}, versionID: "version-1", wantErr: ErrRetentionMetadataRequired},
		{name: "no such version", fake: &fakeRetainedObjectAPI{headErr: &smithy.GenericAPIError{Code: "NoSuchVersion", Message: "missing"}}, versionID: "missing-version", wantErr: ErrObjectNotFound},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewRetainedStorage(test.fake, testBucket)
			require.NoError(t, err)

			_, err = store.StatVersion(context.Background(), "artifact", test.versionID)

			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestRetainedStorage_ValidateDefaultRetention_RequiresFiveYearComplianceLock(t *testing.T) {
	t.Parallel()

	compliance := s3types.ObjectLockRetentionModeCompliance
	governance := s3types.ObjectLockRetentionModeGovernance
	enabled := s3types.ObjectLockEnabledEnabled
	disabled := s3types.ObjectLockEnabled("Disabled")
	yearsFour := int32(4)
	yearsFive := int32(5)
	daysShort := int32(1826)
	daysMinimum := int32(1827)
	lockLookupErr := &smithy.GenericAPIError{Code: "ObjectLockConfigurationNotFoundError", Message: "object lock configuration does not exist"}
	tests := []struct {
		name    string
		output  *awss3.GetObjectLockConfigurationOutput
		lockErr error
		wantErr error
	}{
		{name: "missing response", wantErr: ErrObjectLockConfigurationRequired},
		{name: "configuration lookup fails", lockErr: lockLookupErr, wantErr: lockLookupErr},
		{name: "missing configuration", output: &awss3.GetObjectLockConfigurationOutput{}, wantErr: ErrObjectLockConfigurationRequired},
		{name: "object lock disabled", output: lockConfiguration(disabled, compliance, &yearsFive, nil), wantErr: ErrObjectLockNotEnabled},
		{name: "missing rule", output: &awss3.GetObjectLockConfigurationOutput{ObjectLockConfiguration: &s3types.ObjectLockConfiguration{ObjectLockEnabled: enabled}}, wantErr: ErrDefaultRetentionRequired},
		{name: "missing default retention", output: &awss3.GetObjectLockConfigurationOutput{ObjectLockConfiguration: &s3types.ObjectLockConfiguration{ObjectLockEnabled: enabled, Rule: &s3types.ObjectLockRule{}}}, wantErr: ErrDefaultRetentionRequired},
		{name: "governance mode", output: lockConfiguration(enabled, governance, &yearsFive, nil), wantErr: ErrRetentionNotCompliance},
		{name: "four years", output: lockConfiguration(enabled, compliance, &yearsFour, nil), wantErr: ErrRetentionTooShort},
		{name: "1826 days", output: lockConfiguration(enabled, compliance, nil, &daysShort), wantErr: ErrRetentionTooShort},
		{name: "missing period", output: lockConfiguration(enabled, compliance, nil, nil), wantErr: ErrDefaultRetentionRequired},
		{name: "both periods", output: lockConfiguration(enabled, compliance, &yearsFive, &daysMinimum), wantErr: ErrDefaultRetentionRequired},
		{name: "five years", output: lockConfiguration(enabled, compliance, &yearsFive, nil)},
		{name: "1827 days", output: lockConfiguration(enabled, compliance, nil, &daysMinimum)},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRetainedObjectAPI{lockOutput: test.output, lockErr: test.lockErr}
			store, err := NewRetainedStorage(fake, "  retained-bucket  ")
			require.NoError(t, err)

			err = store.ValidateDefaultRetention(context.Background())

			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, fake.lockInput)
			assert.Equal(t, "retained-bucket", aws.ToString(fake.lockInput.Bucket))
			assert.Equal(t, 1, fake.lockCallCount)
		})
	}
}

func TestRetainedStorage_ValidateDefaultRetention_APIError_PreservesCause(t *testing.T) {
	t.Parallel()

	apiFailure := &smithy.GenericAPIError{Code: "ObjectLockConfigurationNotFoundError", Message: "missing"}
	fake := &fakeRetainedObjectAPI{lockErr: apiFailure}
	store, err := NewRetainedStorage(fake, testBucket)
	require.NoError(t, err)

	err = store.ValidateDefaultRetention(context.Background())

	require.ErrorIs(t, err, apiFailure)
	var retainedErr *RetainedStorageError
	require.ErrorAs(t, err, &retainedErr)
	assert.Equal(t, "validate-default-retention", retainedErr.Operation())
}

func lockConfiguration(enabled s3types.ObjectLockEnabled, mode s3types.ObjectLockRetentionMode, years, days *int32) *awss3.GetObjectLockConfigurationOutput {
	return &awss3.GetObjectLockConfigurationOutput{ObjectLockConfiguration: &s3types.ObjectLockConfiguration{
		ObjectLockEnabled: enabled,
		Rule: &s3types.ObjectLockRule{DefaultRetention: &s3types.DefaultRetention{
			Mode:  mode,
			Years: years,
			Days:  days,
		}},
	}}
}
