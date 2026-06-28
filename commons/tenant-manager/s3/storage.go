// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ErrObjectNotFound is returned by Download when the requested object does not
// exist in the bucket. Callers can match it with errors.Is.
var ErrObjectNotFound = errors.New("object not found")

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
	// Upload writes body to the resolved key with the given content type.
	Upload(ctx context.Context, key string, body io.Reader, contentType string) error
	// Download returns a reader for the object at the resolved key.
	// Returns ErrObjectNotFound when the object does not exist.
	// The caller must close the returned reader.
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object at the resolved key. Deleting a missing
	// object is not an error (S3 delete is idempotent).
	Delete(ctx context.Context, key string) error
	// Exists reports whether an object exists at the resolved key.
	Exists(ctx context.Context, key string) (bool, error)
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

// isNilObjectAPI returns true if the interface value is nil or holds a typed nil.
func isNilObjectAPI(i objectAPI) bool {
	if i == nil {
		return true
	}

	v := reflect.ValueOf(i)

	return v.Kind() == reflect.Pointer && v.IsNil()
}
