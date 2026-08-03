//go:build unit

package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
	putInput      *awss3.PutObjectInput
	getInput      *awss3.GetObjectInput
	headInput     *awss3.HeadObjectInput
	lockInput     *awss3.GetObjectLockConfigurationInput
	putOutput     *awss3.PutObjectOutput
	getOutput     *awss3.GetObjectOutput
	headOutput    *awss3.HeadObjectOutput
	lockOutput    *awss3.GetObjectLockConfigurationOutput
	putErr        error
	getErr        error
	headErr       error
	lockErr       error
	headCallCount int
	lockCallCount int
}

func (f *fakeRetainedObjectAPI) PutObject(_ context.Context, input *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	f.putInput = input

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

func TestRetainedStorage_CreateRetained_UsesConditionalComplianceWriteAndReturnsVersionMetadata(t *testing.T) {
	t.Parallel()

	retainUntil := time.Date(2031, time.August, 3, 12, 30, 0, 0, time.FixedZone("BRT", -3*60*60))
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
			ObjectLockRetainUntilDate: aws.Time(retainUntil.UTC()),
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
	assert.True(t, retainUntil.UTC().Equal(*fake.putInput.ObjectLockRetainUntilDate))
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
			RetainUntil: retainUntil.UTC(),
		},
	}, metadata)
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
