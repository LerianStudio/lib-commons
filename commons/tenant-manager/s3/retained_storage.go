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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const minimumDefaultRetentionDays int32 = 1827

// RetentionMode identifies the immutable retention mode applied to an object version.
type RetentionMode string

// RetentionModeCompliance is the only retention mode accepted by RetainedStorage.
const RetentionModeCompliance RetentionMode = "COMPLIANCE"

var (
	// ErrRetainedBodyRequired reports a retained create without a body.
	ErrRetainedBodyRequired = errors.New("retained object body is required")
	// ErrInvalidRetention reports missing or non-COMPLIANCE retained-create settings.
	ErrInvalidRetention = errors.New("retention must use COMPLIANCE mode and an explicit retain-until time")
	// ErrVersionIDRequired reports an absent S3 object version identifier.
	ErrVersionIDRequired = errors.New("object version ID is required")
	// ErrVersionIDMismatch reports metadata for a version other than the requested version.
	ErrVersionIDMismatch = errors.New("object version ID does not match requested version")
	// ErrVersionMetadataRequired reports an absent S3 response or response body.
	ErrVersionMetadataRequired = errors.New("object version metadata is required")
	// ErrRetentionMetadataRequired reports incomplete Object Lock metadata for a version.
	ErrRetentionMetadataRequired = errors.New("object retention metadata is required")
	// ErrRetentionNotCompliance reports a retention mode weaker than COMPLIANCE.
	ErrRetentionNotCompliance = errors.New("object retention mode must be COMPLIANCE")
	// ErrObjectLockConfigurationRequired reports absent bucket Object Lock metadata.
	ErrObjectLockConfigurationRequired = errors.New("object lock configuration is required")
	// ErrObjectLockNotEnabled reports a bucket without Object Lock enabled.
	ErrObjectLockNotEnabled = errors.New("object lock must be enabled")
	// ErrDefaultRetentionRequired reports an absent or incomplete default retention rule.
	ErrDefaultRetentionRequired = errors.New("default object lock retention is required")
	// ErrRetentionTooShort reports a default retention shorter than five years.
	ErrRetentionTooShort = errors.New("default object lock retention must be at least five years")
)

// Retention describes the immutable retention policy attached to one object version.
type Retention struct {
	Mode        RetentionMode
	RetainUntil time.Time
}

// ObjectMetadata describes one exact, immutable S3 object version.
type ObjectMetadata struct {
	VersionID     string
	ETag          string
	ContentType   string
	ContentLength int64
	LastModified  time.Time
	Retention     Retention
}

// RetainedStorage stores and reads immutable, COMPLIANCE-retained S3 object versions.
// It deliberately exposes no delete operation or Object Lock bypass.
type RetainedStorage interface {
	// CreateRetained atomically creates a retained object and returns its exact version metadata.
	CreateRetained(ctx context.Context, key string, body io.Reader, contentType string, retention Retention) (ObjectMetadata, error)
	// DownloadVersion returns the bytes of the exact requested version.
	DownloadVersion(ctx context.Context, key, versionID string) (io.ReadCloser, error)
	// StatVersion returns metadata for the exact requested version.
	StatVersion(ctx context.Context, key, versionID string) (ObjectMetadata, error)
	// ValidateDefaultRetention requires an enabled COMPLIANCE bucket rule of at least five years.
	ValidateDefaultRetention(ctx context.Context) error
}

type retainedObjectAPI interface {
	PutObject(ctx context.Context, params *awss3.PutObjectInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	HeadObject(ctx context.Context, params *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObjectLockConfiguration(ctx context.Context, params *awss3.GetObjectLockConfigurationInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectLockConfigurationOutput, error)
}

type retainedStorage struct {
	client retainedObjectAPI
	bucket string
}

// NewRetainedStorage constructs a tenant-scoped retained storage over a narrow S3 object API.
func NewRetainedStorage(client retainedObjectAPI, bucket string) (RetainedStorage, error) {
	if isNilRetainedObjectAPI(client) {
		return nil, errors.New("s3 client must not be nil")
	}

	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, errors.New("bucket must not be empty")
	}

	return &retainedStorage{client: client, bucket: bucket}, nil
}

func (s *retainedStorage) CreateRetained(
	ctx context.Context,
	key string,
	body io.Reader,
	contentType string,
	retention Retention,
) (ObjectMetadata, error) {
	if body == nil {
		return ObjectMetadata{}, ErrRetainedBodyRequired
	}

	if retention.Mode != RetentionModeCompliance || retention.RetainUntil.IsZero() {
		return ObjectMetadata{}, ErrInvalidRetention
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return ObjectMetadata{}, fmt.Errorf("resolve storage key: %w", err)
	}

	retainUntil := retention.RetainUntil.UTC()

	input := &awss3.PutObjectInput{
		Bucket:                    &s.bucket,
		Key:                       &resolvedKey,
		Body:                      body,
		IfNoneMatch:               aws.String("*"),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: &retainUntil,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}

	output, err := s.client.PutObject(ctx, input)
	if err != nil {
		if isAlreadyExists(err) {
			return ObjectMetadata{}, fmt.Errorf("%w: %q", ErrObjectAlreadyExists, resolvedKey)
		}

		return ObjectMetadata{}, fmt.Errorf("create retained object %q: %w", resolvedKey, err)
	}

	if output == nil || output.VersionId == nil || *output.VersionId == "" {
		return ObjectMetadata{}, fmt.Errorf("create retained object %q: %w", resolvedKey, ErrVersionIDRequired)
	}

	return s.statResolvedVersion(ctx, resolvedKey, *output.VersionId)
}

func (s *retainedStorage) DownloadVersion(ctx context.Context, key, versionID string) (io.ReadCloser, error) {
	if versionID == "" {
		return nil, ErrVersionIDRequired
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("resolve storage key: %w", err)
	}

	output, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket:    &s.bucket,
		Key:       &resolvedKey,
		VersionId: &versionID,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %q version %q", ErrObjectNotFound, resolvedKey, versionID)
		}

		return nil, fmt.Errorf("download object %q version %q: %w", resolvedKey, versionID, err)
	}

	if output == nil || output.Body == nil {
		return nil, fmt.Errorf("download object %q version %q: %w", resolvedKey, versionID, ErrVersionMetadataRequired)
	}

	return output.Body, nil
}

func (s *retainedStorage) StatVersion(ctx context.Context, key, versionID string) (ObjectMetadata, error) {
	if versionID == "" {
		return ObjectMetadata{}, ErrVersionIDRequired
	}

	resolvedKey, err := GetS3KeyStorageContext(ctx, key)
	if err != nil {
		return ObjectMetadata{}, fmt.Errorf("resolve storage key: %w", err)
	}

	return s.statResolvedVersion(ctx, resolvedKey, versionID)
}

func (s *retainedStorage) statResolvedVersion(ctx context.Context, resolvedKey, versionID string) (ObjectMetadata, error) {
	output, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket:    &s.bucket,
		Key:       &resolvedKey,
		VersionId: &versionID,
	})
	if err != nil {
		if isNotFound(err) {
			return ObjectMetadata{}, fmt.Errorf("%w: %q version %q", ErrObjectNotFound, resolvedKey, versionID)
		}

		return ObjectMetadata{}, fmt.Errorf("stat object %q version %q: %w", resolvedKey, versionID, err)
	}

	return metadataFromHead(output, versionID)
}

func metadataFromHead(output *awss3.HeadObjectOutput, requestedVersionID string) (ObjectMetadata, error) {
	if output == nil {
		return ObjectMetadata{}, ErrVersionMetadataRequired
	}

	versionID := aws.ToString(output.VersionId)
	if versionID == "" {
		return ObjectMetadata{}, ErrVersionIDRequired
	}

	if versionID != requestedVersionID {
		return ObjectMetadata{}, ErrVersionIDMismatch
	}

	if output.ObjectLockMode == "" || output.ObjectLockRetainUntilDate == nil {
		return ObjectMetadata{}, ErrRetentionMetadataRequired
	}

	if output.ObjectLockMode != s3types.ObjectLockModeCompliance {
		return ObjectMetadata{}, ErrRetentionNotCompliance
	}

	metadata := ObjectMetadata{
		VersionID:     versionID,
		ETag:          aws.ToString(output.ETag),
		ContentType:   aws.ToString(output.ContentType),
		ContentLength: aws.ToInt64(output.ContentLength),
		Retention: Retention{
			Mode:        RetentionModeCompliance,
			RetainUntil: output.ObjectLockRetainUntilDate.UTC(),
		},
	}
	if output.LastModified != nil {
		metadata.LastModified = output.LastModified.UTC()
	}

	return metadata, nil
}

func (s *retainedStorage) ValidateDefaultRetention(ctx context.Context) error {
	output, err := s.client.GetObjectLockConfiguration(ctx, &awss3.GetObjectLockConfigurationInput{
		Bucket: &s.bucket,
	})
	if err != nil {
		return fmt.Errorf("get object lock configuration: %w", err)
	}

	if output == nil || output.ObjectLockConfiguration == nil {
		return ErrObjectLockConfigurationRequired
	}

	configuration := output.ObjectLockConfiguration
	if configuration.ObjectLockEnabled != s3types.ObjectLockEnabledEnabled {
		return ErrObjectLockNotEnabled
	}

	if configuration.Rule == nil || configuration.Rule.DefaultRetention == nil {
		return ErrDefaultRetentionRequired
	}

	defaultRetention := configuration.Rule.DefaultRetention
	if defaultRetention.Mode != s3types.ObjectLockRetentionModeCompliance {
		return ErrRetentionNotCompliance
	}

	if !validDefaultRetentionPeriod(defaultRetention) {
		return defaultRetentionPeriodError(defaultRetention)
	}

	return nil
}

func validDefaultRetentionPeriod(retention *s3types.DefaultRetention) bool {
	if (retention.Years == nil) == (retention.Days == nil) {
		return false
	}

	if retention.Years != nil {
		return *retention.Years >= 5
	}

	return *retention.Days >= minimumDefaultRetentionDays
}

func defaultRetentionPeriodError(retention *s3types.DefaultRetention) error {
	if (retention.Years == nil) == (retention.Days == nil) {
		return ErrDefaultRetentionRequired
	}

	return ErrRetentionTooShort
}

func isNilRetainedObjectAPI(value retainedObjectAPI) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)

	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}
