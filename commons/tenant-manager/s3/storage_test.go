//go:build unit

package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBucket = "test-bucket"

// fakeObjectAPI is an in-memory implementation of objectAPI for unit tests.
// It records the last computed key for prefix assertions and stores objects
// keyed by their S3 key.
type fakeObjectAPI struct {
	objects map[string][]byte

	lastPutKey    string
	lastGetKey    string
	lastDeleteKey string
	lastHeadKey   string
	lastBucket    string

	putErr    error
	getErr    error
	deleteErr error
	headErr   error

	// getNilOutput, when true, makes GetObject return a nil *GetObjectOutput
	// with a nil error, simulating a misbehaving custom objectAPI.
	getNilOutput bool
	// getNilBody, when true, makes GetObject return a non-nil output whose
	// Body is nil, simulating a misbehaving custom objectAPI.
	getNilBody bool
}

func newFakeObjectAPI() *fakeObjectAPI {
	return &fakeObjectAPI{objects: make(map[string][]byte)}
}

func (f *fakeObjectAPI) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	f.lastBucket = deref(in.Bucket)
	f.lastPutKey = deref(in.Key)

	if f.putErr != nil {
		return nil, f.putErr
	}

	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}

	f.objects[deref(in.Key)] = data

	return &awss3.PutObjectOutput{}, nil
}

func (f *fakeObjectAPI) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	f.lastBucket = deref(in.Bucket)
	f.lastGetKey = deref(in.Key)

	if f.getErr != nil {
		return nil, f.getErr
	}

	if f.getNilOutput {
		return nil, nil //nolint:nilnil // deliberately simulating a misbehaving objectAPI
	}

	if f.getNilBody {
		return &awss3.GetObjectOutput{Body: nil}, nil
	}

	data, ok := f.objects[deref(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}

	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (f *fakeObjectAPI) DeleteObject(_ context.Context, in *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	f.lastBucket = deref(in.Bucket)
	f.lastDeleteKey = deref(in.Key)

	if f.deleteErr != nil {
		return nil, f.deleteErr
	}

	delete(f.objects, deref(in.Key))

	return &awss3.DeleteObjectOutput{}, nil
}

func (f *fakeObjectAPI) HeadObject(_ context.Context, in *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	f.lastBucket = deref(in.Bucket)
	f.lastHeadKey = deref(in.Key)

	if f.headErr != nil {
		return nil, f.headErr
	}

	if _, ok := f.objects[deref(in.Key)]; !ok {
		return nil, &s3types.NotFound{}
	}

	return &awss3.HeadObjectOutput{}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func multiTenantCtx(tenantID string) context.Context {
	return core.ContextWithTenantID(context.Background(), tenantID)
}

func TestNewStorage_NilClient(t *testing.T) {
	t.Parallel()

	_, err := NewStorage(nil, testBucket)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client")
}

func TestNewStorage_EmptyBucket(t *testing.T) {
	t.Parallel()

	_, err := NewStorage(newFakeObjectAPI(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket")
}

func TestNewStorage_WhitespaceOnlyBucket(t *testing.T) {
	t.Parallel()

	_, err := NewStorage(newFakeObjectAPI(), "   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket")
}

func TestNewStorage_NormalizesBucketWhitespace(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, "  my-bucket  ")
	require.NoError(t, err)

	// The stored bucket must be the trimmed value, so S3 calls never carry
	// stray whitespace.
	err = storage.Upload(context.Background(), "x.xsd", bytes.NewReader([]byte("x")), "application/xml")
	require.NoError(t, err)
	assert.Equal(t, "my-bucket", fake.lastBucket)
}

func TestStorage_Upload_AppliesTenantPrefix_MultiTenant(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	err = storage.Upload(ctx, "schemas/xsd/foo/v1/schema.xsd", bytes.NewReader([]byte("payload")), "application/xml")
	require.NoError(t, err)

	assert.Equal(t, "org_01ABC/schemas/xsd/foo/v1/schema.xsd", fake.lastPutKey)
	assert.Equal(t, testBucket, fake.lastBucket)
}

func TestStorage_Upload_BareKey_SingleTenant(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	err = storage.Upload(context.Background(), "schemas/xsd/foo/v1/schema.xsd", bytes.NewReader([]byte("payload")), "application/xml")
	require.NoError(t, err)

	assert.Equal(t, "schemas/xsd/foo/v1/schema.xsd", fake.lastPutKey)
}

func TestStorage_UploadDownload_RoundTrip(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	body := []byte("<schema/>")

	require.NoError(t, storage.Upload(ctx, "schemas/x.xsd", bytes.NewReader(body), "application/xml"))

	rc, err := storage.Download(ctx, "schemas/x.xsd")
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, body, got)

	// Download must use the same tenant-prefixed key.
	assert.Equal(t, "org_01ABC/schemas/x.xsd", fake.lastGetKey)
}

func TestStorage_Download_NotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	_, err = storage.Download(multiTenantCtx("org_01ABC"), "missing.xsd")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStorage_Download_NilOutput_ReturnsError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	fake.getNilOutput = true
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	rc, err := storage.Download(multiTenantCtx("org_01ABC"), "x.xsd")
	require.Error(t, err)
	assert.Nil(t, rc)
	assert.NotErrorIs(t, err, ErrObjectNotFound)
}

func TestStorage_Download_NilBody_ReturnsError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	fake.getNilBody = true
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	rc, err := storage.Download(multiTenantCtx("org_01ABC"), "x.xsd")
	require.Error(t, err)
	assert.Nil(t, rc)
	assert.NotErrorIs(t, err, ErrObjectNotFound)
}

func TestStorage_Delete_AppliesTenantPrefix(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	require.NoError(t, storage.Upload(ctx, "schemas/x.xsd", bytes.NewReader([]byte("x")), "application/xml"))

	require.NoError(t, storage.Delete(ctx, "schemas/x.xsd"))
	assert.Equal(t, "org_01ABC/schemas/x.xsd", fake.lastDeleteKey)

	exists, err := storage.Exists(ctx, "schemas/x.xsd")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestStorage_Exists_True(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	require.NoError(t, storage.Upload(ctx, "schemas/x.xsd", bytes.NewReader([]byte("x")), "application/xml"))

	exists, err := storage.Exists(ctx, "schemas/x.xsd")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "org_01ABC/schemas/x.xsd", fake.lastHeadKey)
}

func TestStorage_Exists_False_NotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	exists, err := storage.Exists(multiTenantCtx("org_01ABC"), "missing.xsd")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestStorage_Exists_PropagatesUnexpectedError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	fake.headErr = errors.New("network blip")
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	_, err = storage.Exists(multiTenantCtx("org_01ABC"), "x.xsd")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrObjectNotFound)
}

func TestStorage_Download_MapsNoSuchKeyAPIError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	// Simulate a generic smithy APIError with NoSuchKey code (not the typed struct).
	fake.getErr = &smithy.GenericAPIError{Code: "NoSuchKey", Message: "The specified key does not exist."}
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	_, err = storage.Download(multiTenantCtx("org_01ABC"), "x.xsd")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStorage_Upload_NilBody(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	err = storage.Upload(multiTenantCtx("org_01ABC"), "x.xsd", nil, "application/xml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "body")
}
