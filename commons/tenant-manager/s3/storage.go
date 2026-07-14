// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// ErrObjectNotFound is returned by Download when the requested object does not
// exist in the bucket. Callers can match it with errors.Is.
var ErrObjectNotFound = errors.New("object not found")

// ErrObjectAlreadyExists is returned by Create when the target object already
// exists. Create uses an S3 conditional write (IfNoneMatch: "*"), so this maps
// the HTTP 412 / "PreconditionFailed" response into a typed sentinel that
// callers can match with errors.Is for atomic create-if-absent semantics.
var ErrObjectAlreadyExists = errors.New("object already exists")

// Storage is a tenant-scoped blob storage abstraction over an S3 bucket.
//
// Callers pass LOGICAL keys (e.g. "schemas/xsd/{name}/{version}/schema.xsd").
// The implementation applies the tenant prefix automatically via
// GetS3KeyStorageContext, so the physical S3 key becomes:
//
//	multi-tenant (tenant in context): "{tenantID}/{key}"
//	single-tenant (no tenant):        "{key}"
//
// All operations resolve the key the same way, so a value written under one
// tenant context is only visible under that same tenant context.
type Storage interface {
	// Upload writes body to the resolved key with the given content type,
	// overwriting any existing object at that key.
	Upload(ctx context.Context, key string, body io.Reader, contentType string) error
	// Create writes body to the resolved key only if no object already exists
	// there, using an S3 conditional write (IfNoneMatch: "*"). This is an
	// atomic create-if-absent with no time-of-check/time-of-use race.
	// Returns ErrObjectAlreadyExists if the object already exists.
	Create(ctx context.Context, key string, body io.Reader, contentType string) error
	// Download returns a reader for the object at the resolved key.
	// Returns ErrObjectNotFound when the object does not exist.
	// The caller must close the returned reader.
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object at the resolved key. Deleting a missing
	// object is not an error (S3 delete is idempotent).
	Delete(ctx context.Context, key string) error
	// Exists reports whether an object exists at the resolved key.
	Exists(ctx context.Context, key string) (bool, error)
	// List enumerates the LOGICAL keys of every object stored under prefix.
	//
	// The prefix is resolved through the same tenant key resolution the other
	// methods use, so a tenant context lists only that tenant's space and a
	// global/empty-tenant context lists under the bare prefix. The returned
	// keys are logical (the tenant prefix is stripped back off), matching the
	// keys callers passed to Upload/Create. Results are paginated internally,
	// so a prefix with more objects than one S3 page still returns every key.
	// Returns an empty (non-nil) slice when nothing matches.
	List(ctx context.Context, prefix string) ([]string, error)
}

// objectAPI is the narrow seam over the AWS S3 client used by Storage.
// It covers only the operations this package needs, which lets tests inject
// a fake without touching real AWS. *github.com/aws/aws-sdk-go-v2/service/s3.Client
// satisfies this interface.
type objectAPI interface {
	PutObject(ctx context.Context, params *awss3.PutObjectInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *awss3.DeleteObjectInput, optFns ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *awss3.ListObjectsV2Input, optFns ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
}

// storage is the aws-sdk-go-v2 backed implementation of Storage.
type storage struct {
	client objectAPI
	bucket string
}

// NewStorage constructs a tenant-scoped blob Storage backed by the given S3
// client and bucket.
//
// The client is typically built by the caller from its own AWS config:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	client := s3.NewFromConfig(cfg)
//	store, err := s3.NewStorage(client, os.Getenv("BLOB_BUCKET"))
//
// The bucket is supplied by the caller (from its own env); it is never
// hardcoded here. Returns an error if client is nil (including a typed-nil
// interface value) or bucket is empty.
func NewStorage(client objectAPI, bucket string) (Storage, error) {
	if isNilObjectAPI(client) {
		return nil, errors.New("s3 client must not be nil")
	}

	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, errors.New("bucket must not be empty")
	}

	return &storage{client: client, bucket: bucket}, nil
}

// Upload writes body to the tenant-resolved key.
func (s *storage) Upload(ctx context.Context, key string, body io.Reader, contentType string) error {
	if body == nil {
		return errors.New("body must not be nil")
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return fmt.Errorf("resolve storage key: %w", err)
	}

	input := &awss3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &resolvedKey,
		Body:   body,
	}

	if contentType != "" {
		input.ContentType = &contentType
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("upload object %q: %w", resolvedKey, err)
	}

	return nil
}

// Create writes body to the tenant-resolved key only if the object does not
// already exist, using the S3 conditional-write header IfNoneMatch: "*".
//
// The conditional write is evaluated atomically by S3, so concurrent callers
// cannot both succeed and there is no time-of-check/time-of-use window. When
// the object already exists, S3 responds with HTTP 412 / "PreconditionFailed",
// which is mapped to ErrObjectAlreadyExists.
func (s *storage) Create(ctx context.Context, key string, body io.Reader, contentType string) error {
	if body == nil {
		return errors.New("body must not be nil")
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return fmt.Errorf("resolve storage key: %w", err)
	}

	input := &awss3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &resolvedKey,
		Body:        body,
		IfNoneMatch: aws.String("*"),
	}

	if contentType != "" {
		input.ContentType = &contentType
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		if isAlreadyExists(err) {
			return fmt.Errorf("%w: %q", ErrObjectAlreadyExists, resolvedKey)
		}

		return fmt.Errorf("create object %q: %w", resolvedKey, err)
	}

	return nil
}

// Download returns a reader for the object at the tenant-resolved key.
func (s *storage) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("resolve storage key: %w", err)
	}

	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &resolvedKey,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %q", ErrObjectNotFound, resolvedKey)
		}

		return nil, fmt.Errorf("download object %q: %w", resolvedKey, err)
	}

	if out == nil || out.Body == nil {
		return nil, fmt.Errorf("download object %q: nil response body", resolvedKey)
	}

	return out.Body, nil
}

// Delete removes the object at the tenant-resolved key.
func (s *storage) Delete(ctx context.Context, key string) error {
	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return fmt.Errorf("resolve storage key: %w", err)
	}

	if _, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &resolvedKey,
	}); err != nil {
		return fmt.Errorf("delete object %q: %w", resolvedKey, err)
	}

	return nil
}

// Exists reports whether an object exists at the tenant-resolved key.
func (s *storage) Exists(ctx context.Context, key string) (bool, error) {
	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return false, fmt.Errorf("resolve storage key: %w", err)
	}

	if _, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &resolvedKey,
	}); err != nil {
		if isNotFound(err) {
			return false, nil
		}

		return false, fmt.Errorf("head object %q: %w", resolvedKey, err)
	}

	return true, nil
}

// List enumerates the logical keys of every object stored under the
// tenant-resolved prefix.
//
// The prefix is resolved with GetS3KeyStorageContext exactly like every other
// method, so listing is tenant-scoped: a tenant context only sees that tenant's
// objects, a global/empty-tenant context lists under the bare prefix. The
// ListObjectsV2 response is paginated, so this follows the continuation token
// until IsTruncated is false to collect every matching key. If S3 reports a
// truncated page without a continuation token, this returns an error rather
// than a silently incomplete result set. Each physical S3 key is converted
// back to its logical form (tenant prefix stripped) so the values returned
// match what callers passed to Upload/Create. An empty, non-nil slice is
// returned when nothing matches.
func (s *storage) List(ctx context.Context, prefix string) ([]string, error) {
	resolvedPrefix, err := GetS3KeyStorageContext(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("resolve storage key: %w", err)
	}

	tenantID := core.GetTenantIDContext(ctx)

	keys := make([]string, 0)

	var continuationToken *string

	for {
		out, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket:            &s.bucket,
			Prefix:            &resolvedPrefix,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list objects under prefix %q: %w", resolvedPrefix, err)
		}

		if out == nil {
			return nil, fmt.Errorf("list objects under prefix %q: nil response", resolvedPrefix)
		}

		for i := range out.Contents {
			obj := out.Contents[i]
			if obj.Key == nil {
				continue
			}

			logicalKey, err := StripObjectStoragePrefix(tenantID, *obj.Key)
			if err != nil {
				return nil, fmt.Errorf("strip tenant prefix from key %q: %w", *obj.Key, err)
			}

			keys = append(keys, logicalKey)
		}

		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}

		// S3 reported more results but gave us no continuation token to fetch
		// them. Returning here would silently drop the remaining objects, so
		// fail loudly rather than hand back a partial result set.
		if out.NextContinuationToken == nil || *out.NextContinuationToken == "" {
			return nil, fmt.Errorf("list objects under prefix %q: truncated response missing continuation token", prefix)
		}

		continuationToken = out.NextContinuationToken
	}

	return keys, nil
}

// isNotFound reports whether err represents an S3 "object does not exist"
// condition. It matches the typed not-found errors (NoSuchKey from GetObject,
// NotFound from HeadObject) as well as the generic smithy APIError codes the
// SDK surfaces for the same conditions.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}

	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}

	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}

	return false
}

// isAlreadyExists reports whether err represents a failed conditional create,
// i.e. an object already existing at the target key when IfNoneMatch: "*" was
// requested. S3 surfaces this as the "PreconditionFailed" API error code,
// returned with HTTP status 412. It matches the typed API-error code first and
// falls back to the HTTP status so it stays correct even if the SDK changes how
// it labels the error code.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}

	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusPreconditionFailed {
		return true
	}

	return false
}

// isNilObjectAPI returns true if the interface value is nil or holds a typed nil.
func isNilObjectAPI(i objectAPI) bool {
	if i == nil {
		return true
	}

	v := reflect.ValueOf(i)

	return v.Kind() == reflect.Pointer && v.IsNil()
}
