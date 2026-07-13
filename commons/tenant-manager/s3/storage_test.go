//go:build unit

package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBucket = "test-bucket"

// fakeObjectAPI is an in-memory implementation of objectAPI for unit tests.
// It records the last computed key for prefix assertions and stores objects
// keyed by their S3 key.
type fakeObjectAPI struct {
	objects map[string][]byte

	lastPutKey         string
	lastGetKey         string
	lastDeleteKey      string
	lastHeadKey        string
	lastBucket         string
	lastPutIfNoneMatch string
	lastListPrefix     string

	putErr    error
	getErr    error
	deleteErr error
	headErr   error
	listErr   error

	// listPages, when non-empty, is returned page-by-page by ListObjectsV2
	// instead of deriving the result from objects. Each entry is one page of
	// full S3 keys; pages before the last are returned as truncated.
	listPages [][]string
	// listCallCount records how many times ListObjectsV2 was invoked, so tests
	// can assert pagination drove more than one round trip.
	listCallCount int
	// listNilOutput, when true, makes ListObjectsV2 return a nil output with a
	// nil error, simulating a misbehaving custom objectAPI.
	listNilOutput bool
	// listKeysWithNil, when non-empty, is returned as a single page whose
	// Contents includes a nil-Key object (a "" entry marks the nil-Key slot),
	// exercising the nil-key skip.
	listKeysWithNil []string
	// listTruncatedNoToken, when true, returns a page marked truncated but with
	// no NextContinuationToken, exercising the error path.
	listTruncatedNoToken bool
	// listTruncatedEmptyToken, when true, returns a page marked truncated with a
	// present-but-empty NextContinuationToken, exercising the same error path.
	listTruncatedEmptyToken bool

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
	f.lastPutIfNoneMatch = deref(in.IfNoneMatch)

	if f.putErr != nil {
		return nil, f.putErr
	}

	// Emulate S3 conditional write: IfNoneMatch "*" succeeds only when the
	// object does not already exist; otherwise S3 returns HTTP 412 with the
	// PreconditionFailed API error code.
	if deref(in.IfNoneMatch) == "*" {
		if _, ok := f.objects[deref(in.Key)]; ok {
			return nil, &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusPreconditionFailed}},
				Err:      &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "At least one of the pre-conditions you specified did not hold"},
			}
		}
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

func (f *fakeObjectAPI) ListObjectsV2(_ context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	f.lastBucket = deref(in.Bucket)
	f.lastListPrefix = deref(in.Prefix)
	f.listCallCount++

	if f.listErr != nil {
		return nil, f.listErr
	}

	if f.listNilOutput {
		return nil, nil //nolint:nilnil // deliberately simulating a misbehaving objectAPI
	}

	if f.listTruncatedNoToken {
		return &awss3.ListObjectsV2Output{IsTruncated: aws.Bool(true)}, nil
	}

	if f.listTruncatedEmptyToken {
		empty := ""

		return &awss3.ListObjectsV2Output{IsTruncated: aws.Bool(true), NextContinuationToken: &empty}, nil
	}

	if len(f.listKeysWithNil) > 0 {
		contents := make([]s3types.Object, 0, len(f.listKeysWithNil))

		for _, k := range f.listKeysWithNil {
			if k == "" {
				contents = append(contents, s3types.Object{Key: nil})
				continue
			}

			key := k
			contents = append(contents, s3types.Object{Key: &key})
		}

		return &awss3.ListObjectsV2Output{Contents: contents}, nil
	}

	// Scripted multi-page mode: return one page per call, marking every page
	// but the last as truncated so the implementation must follow the
	// continuation token to collect all keys.
	if len(f.listPages) > 0 {
		idx := 0

		if in.ContinuationToken != nil {
			var err error

			idx, err = pageIndexFromToken(deref(in.ContinuationToken))
			if err != nil {
				return nil, err
			}
		}

		// Fail fast on an out-of-range index instead of panicking or silently
		// serving page 0, so a malformed/unexpected continuation token is a
		// visible test failure rather than misleading data.
		if idx < 0 || idx >= len(f.listPages) {
			return nil, fmt.Errorf("fake ListObjectsV2: page index %d out of range [0,%d)", idx, len(f.listPages))
		}

		page := f.listPages[idx]
		contents := make([]s3types.Object, 0, len(page))

		for _, k := range page {
			key := k
			contents = append(contents, s3types.Object{Key: &key})
		}

		out := &awss3.ListObjectsV2Output{Contents: contents}

		if idx < len(f.listPages)-1 {
			next := tokenForPageIndex(idx + 1)
			out.IsTruncated = aws.Bool(true)
			out.NextContinuationToken = &next
		}

		return out, nil
	}

	// In-memory mode: derive the result from the stored objects, filtering by
	// the requested prefix.
	prefix := deref(in.Prefix)
	contents := make([]s3types.Object, 0, len(f.objects))

	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			key := k
			contents = append(contents, s3types.Object{Key: &key})
		}
	}

	return &awss3.ListObjectsV2Output{Contents: contents}, nil
}

func tokenForPageIndex(idx int) string {
	return "page-" + strconv.Itoa(idx)
}

func pageIndexFromToken(token string) (int, error) {
	if !strings.HasPrefix(token, "page-") {
		return 0, fmt.Errorf("fake ListObjectsV2: unexpected continuation token %q", token)
	}

	idx, err := strconv.Atoi(strings.TrimPrefix(token, "page-"))
	if err != nil {
		return 0, fmt.Errorf("fake ListObjectsV2: malformed continuation token %q: %w", token, err)
	}

	return idx, nil
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

func TestStorage_Create_FreshKey_SetsIfNoneMatchAndStores(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	body := []byte("<schema/>")

	err = storage.Create(ctx, "schemas/xsd/foo/v1/schema.xsd", bytes.NewReader(body), "application/xml")
	require.NoError(t, err)

	// Conditional create must set IfNoneMatch "*" so the write only succeeds
	// when the object does not already exist.
	assert.Equal(t, "*", fake.lastPutIfNoneMatch)
	// The object must be present after a successful create.
	assert.Equal(t, body, fake.objects["org_01ABC/schemas/xsd/foo/v1/schema.xsd"])
}

func TestStorage_Create_ExistingKey_ReturnsErrObjectAlreadyExists(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	original := []byte("original-payload")

	// Seed the object via an unconditional Upload first.
	require.NoError(t, storage.Upload(ctx, "schemas/x.xsd", bytes.NewReader(original), "application/xml"))

	// A second Create against the same key must fail with the sentinel and
	// must NOT overwrite the original object.
	err = storage.Create(ctx, "schemas/x.xsd", bytes.NewReader([]byte("new-payload")), "application/xml")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectAlreadyExists)
	assert.Equal(t, original, fake.objects["org_01ABC/schemas/x.xsd"])
}

func TestStorage_Create_AppliesTenantPrefix_MultiTenant(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	err = storage.Create(ctx, "schemas/xsd/foo/v1/schema.xsd", bytes.NewReader([]byte("payload")), "application/xml")
	require.NoError(t, err)

	assert.Equal(t, "org_01ABC/schemas/xsd/foo/v1/schema.xsd", fake.lastPutKey)
	assert.Equal(t, testBucket, fake.lastBucket)
}

func TestStorage_Create_BareKey_SingleTenant(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	err = storage.Create(context.Background(), "schemas/xsd/foo/v1/schema.xsd", bytes.NewReader([]byte("payload")), "application/xml")
	require.NoError(t, err)

	assert.Equal(t, "schemas/xsd/foo/v1/schema.xsd", fake.lastPutKey)
}

func TestStorage_Create_NilBody(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	err = storage.Create(multiTenantCtx("org_01ABC"), "x.xsd", nil, "application/xml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "body")
}

func TestStorage_Create_MapsPreconditionFailedAPIError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	// Simulate a generic smithy APIError carrying the PreconditionFailed code
	// without an HTTP ResponseError wrapper, exercising the typed-code path.
	fake.putErr = &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "At least one of the pre-conditions you specified did not hold"}
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	err = storage.Create(multiTenantCtx("org_01ABC"), "x.xsd", bytes.NewReader([]byte("x")), "application/xml")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObjectAlreadyExists)
}

func TestStorage_Create_PropagatesUnexpectedError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	fake.putErr = errors.New("network blip")
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	err = storage.Create(multiTenantCtx("org_01ABC"), "x.xsd", bytes.NewReader([]byte("x")), "application/xml")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrObjectAlreadyExists)
}

func TestStorage_List_ReturnsLogicalKeysUnderPrefix_MultiTenant(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := multiTenantCtx("org_01ABC")
	require.NoError(t, storage.Upload(ctx, "schemas/openapi/foo/v1/spec.json", bytes.NewReader([]byte("a")), "application/json"))
	require.NoError(t, storage.Upload(ctx, "schemas/openapi/foo/v2/spec.json", bytes.NewReader([]byte("b")), "application/json"))
	// An object under a different prefix must NOT be returned.
	require.NoError(t, storage.Upload(ctx, "schemas/xsd/bar/v1/schema.xsd", bytes.NewReader([]byte("c")), "application/xml"))

	keys, err := storage.List(ctx, "schemas/openapi/foo/")
	require.NoError(t, err)

	sort.Strings(keys)
	// Returned keys are LOGICAL (tenant prefix stripped), matching the keys the
	// caller passed to Upload.
	assert.Equal(t, []string{
		"schemas/openapi/foo/v1/spec.json",
		"schemas/openapi/foo/v2/spec.json",
	}, keys)

	// The S3 request must carry the tenant-RESOLVED prefix.
	assert.Equal(t, "org_01ABC/schemas/openapi/foo/", fake.lastListPrefix)
	assert.Equal(t, testBucket, fake.lastBucket)
}

func TestStorage_List_BarePrefix_SingleTenant(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, storage.Upload(ctx, "schemas/openapi/foo/v1/spec.json", bytes.NewReader([]byte("a")), "application/json"))

	keys, err := storage.List(ctx, "schemas/openapi/foo/")
	require.NoError(t, err)

	assert.Equal(t, []string{"schemas/openapi/foo/v1/spec.json"}, keys)
	// Global/empty-tenant context lists under the bare prefix (no tenant segment).
	assert.Equal(t, "schemas/openapi/foo/", fake.lastListPrefix)
}

func TestStorage_List_PaginatesAcrossTruncatedResponses(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	// Two pages of full (resolved) S3 keys. The first page is truncated, so the
	// implementation must follow the continuation token to fetch the second.
	fake.listPages = [][]string{
		{"org_01ABC/schemas/openapi/foo/v1/spec.json", "org_01ABC/schemas/openapi/foo/v2/spec.json"},
		{"org_01ABC/schemas/openapi/foo/v3/spec.json"},
	}

	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	keys, err := storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/foo/")
	require.NoError(t, err)

	sort.Strings(keys)
	assert.Equal(t, []string{
		"schemas/openapi/foo/v1/spec.json",
		"schemas/openapi/foo/v2/spec.json",
		"schemas/openapi/foo/v3/spec.json",
	}, keys)

	// Both pages must have been fetched (>= 2 round trips).
	assert.GreaterOrEqual(t, fake.listCallCount, 2)
}

func TestStorage_List_EmptyResult_ReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	keys, err := storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/none/")
	require.NoError(t, err)
	assert.NotNil(t, keys)
	assert.Empty(t, keys)
}

func TestStorage_List_PropagatesUnexpectedError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	fake.listErr = errors.New("network blip")
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	_, err = storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/foo/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network blip")
}

func TestStorage_List_NilOutput_ReturnsError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	fake.listNilOutput = true
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	_, err = storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/foo/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil response")
}

func TestStorage_List_SkipsNilKeyObjects(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	// The middle entry ("") is returned as an object with a nil Key, which must
	// be skipped without producing a spurious key.
	fake.listKeysWithNil = []string{
		"org_01ABC/schemas/openapi/foo/v1/spec.json",
		"",
		"org_01ABC/schemas/openapi/foo/v2/spec.json",
	}
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	keys, err := storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/foo/")
	require.NoError(t, err)

	sort.Strings(keys)
	assert.Equal(t, []string{
		"schemas/openapi/foo/v1/spec.json",
		"schemas/openapi/foo/v2/spec.json",
	}, keys)
}

func TestStorage_List_TruncatedWithoutToken_ReturnsError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	// A response marked truncated but missing a continuation token means S3 has
	// more results it cannot hand back. Returning the partial slice would
	// silently drop data, so List must surface an error instead.
	fake.listTruncatedNoToken = true
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	keys, err := storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/foo/")
	require.Error(t, err)
	assert.Nil(t, keys)
	assert.Contains(t, err.Error(), "truncated response missing continuation token")
	// Only the single (truncated) page is fetched; the loop does not continue.
	assert.Equal(t, 1, fake.listCallCount)
}

func TestStorage_List_TruncatedWithEmptyToken_ReturnsError(t *testing.T) {
	t.Parallel()

	fake := newFakeObjectAPI()
	// An empty (non-nil) continuation token is just as unusable as a nil one;
	// List must treat it the same way and refuse to return partial data.
	fake.listTruncatedEmptyToken = true
	storage, err := NewStorage(fake, testBucket)
	require.NoError(t, err)

	keys, err := storage.List(multiTenantCtx("org_01ABC"), "schemas/openapi/foo/")
	require.Error(t, err)
	assert.Nil(t, keys)
	assert.Contains(t, err.Error(), "truncated response missing continuation token")
	assert.Equal(t, 1, fake.listCallCount)
}
